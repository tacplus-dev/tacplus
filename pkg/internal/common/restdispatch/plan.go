// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

// Package restdispatch resolves a (spec.service).rest_dispatch build's REST
// route table and REST -> gRPC dispatch arms into language-neutral facts. The
// per-language generators map these to their own client, JSON and validation
// names and render the literals; which routes exist, what each one dials,
// whom it entitles callers against and which methods can be dispatched at all
// is decided once, here.
package restdispatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/verbs"
	internalproto "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// Auth policies a REST route carries, as the generated route table spells them.
const (
	AuthNoAuth   = "NoAuth"   // no authentication needed (auth_disabled)
	AuthAnyUser  = "AnyUser"  // valid C2S token required, no entitlement check (auth_any_user)
	AuthEntitled = "Entitled" // valid C2S token + service entitlement check (default)
)

// Service is the slice of a generator's service record the planner needs.
type Service struct {
	ProtoFile   string
	PackageName string
	ServiceName string
	ParentDir   string
	IsS2S       bool
	EventLabel  string
	HostLabel   string
}

// Route is one entry of the generated route table.
type Route struct {
	HttpPath  string // empty for a gRPC-proxy-only entry, never matched as REST
	GrpcPath  string
	ServiceID string // the physical service the route dials
	// EntitlementService, when set, is the audience entitlement checks use
	// instead of ServiceID. aud validation still uses ServiceID.
	EntitlementService string
	HttpMethods        []string
	AuthPolicy         string
	EntitlementScopes  []string
	CapabilityScope    string
	ServerStreaming    bool
	ClientStreaming    bool
	// TargetNamespaceField is (spec.rpc).target_namespace_field; empty when unset.
	TargetNamespaceField string
	// FormBody is (spec.rest_browser).form_body: the route also accepts an
	// application/x-www-form-urlencoded body.
	FormBody bool
	// RedirectField is (spec.rest_browser).redirect_field as declared (the
	// proto field name), and RedirectJSONName its proto3 JSON name. A
	// generator picks whichever spelling its JSON encoder emits. Both are
	// empty when the route answers JSON.
	RedirectField    string
	RedirectJSONName string
}

// Arm is one dispatchable method: a unary or server-streaming REST route whose
// request the generated dispatcher decodes, validates and forwards over gRPC.
type Arm struct {
	GrpcPath        string
	ServiceID       string
	PackageName     string // proto package of the service
	ServiceName     string
	ServiceDir      string // the service's own parent dir, see PkgToDirFor
	MethodName      string
	InputType       string // fully-qualified proto input type
	OutputType      string // fully-qualified proto output type
	IsEmptyInput    bool
	IsEmptyOutput   bool
	ValidateInput   bool
	ValidateOutput  bool
	ServerStreaming bool
}

// Plan is the resolved route table and dispatch arms, in emission order.
type Plan struct {
	Routes []Route
	Arms   []Arm
	// PkgToDir maps every proto package in the build to its parent directory,
	// so cross-package type references resolve to the right generated module.
	PkgToDir map[string]string
}

// PkgToDirFor is PkgToDir with the arm's own package pinned to the arm's
// service directory: a package that spans both services/ and vservices/
// would otherwise resolve to whichever directory was seen first.
func (p *Plan) PkgToDirFor(arm Arm) map[string]string {
	if p.PkgToDir[arm.PackageName] == arm.ServiceDir {
		return p.PkgToDir
	}
	m := make(map[string]string, len(p.PkgToDir))
	for k, v := range p.PkgToDir {
		m[k] = v
	}
	m[arm.PackageName] = arm.ServiceDir
	return m
}

// Detect reports the self package's (spec.service).gateway and rest_dispatch
// options.
func Detect(dp *parser.DescriptorParser, selfPackage string) (isGateway, hasRestDispatch bool) {
	if selfPackage == "" {
		return false, false
	}
	for _, fd := range dp.GetAllFiles() {
		if fd.Package != selfPackage {
			continue
		}
		if fd.ServiceGateway {
			isGateway = true
		}
		if fd.ServiceRestDispatch {
			hasRestDispatch = true
		}
	}
	return isGateway, hasRestDispatch
}

// Collect resolves the route table and dispatch arms for the given services.
// inputDir is the proto root the descriptor parser was created from.
func Collect(dp *parser.DescriptorParser, config *tactype.Config, services []Service, inputDir string) (*Plan, error) {
	plan := &Plan{PkgToDir: make(map[string]string)}
	for _, svc := range services {
		if _, exists := plan.PkgToDir[svc.PackageName]; !exists {
			plan.PkgToDir[svc.PackageName] = svc.ParentDir
		}
	}
	// Every proto package, not just the services', so cross-package type
	// references resolve.
	for path, desc := range dp.GetAllFiles() {
		if _, exists := plan.PkgToDir[desc.Package]; !exists {
			dir := filepath.Dir(path)
			if dir == "." {
				dir = ""
			}
			plan.PkgToDir[desc.Package] = dir
		}
	}

	// The client-reachable (C2S proxy) token minter is whichever service
	// declares (spec.token_minter) {proxy: true}; a gateway dispatches it
	// specially, so it gets routes but no arms.
	var proxyMinter *parser.TokenExchangerInfo
	for _, m := range dp.TokenMinters() {
		if m.IsProxy {
			mm := m
			proxyMinter = &mm
			break
		}
	}

	for _, svc := range services {
		// S2S services aren't exposed through REST. But a gateway's generic
		// gRPC proxy still needs the auth policy of any auth_disabled methods it
		// proxies (e.g. the token minter's bootstrap calls), or it defaults them
		// to Entitled and rejects the unauthenticated calls they are designed to
		// accept. Emit proxy-only routes (no HTTP path, NoAuth) for those.
		if svc.IsS2S {
			if config.IsGateway {
				if err := collectProxyAuthDisabledRoutes(dp, svc, plan); err != nil {
					return nil, fmt.Errorf("collecting S2S auth-disabled proxy routes for %s: %w", svc.ServiceName, err)
				}
			}
			continue
		}

		if config.IsGateway && proxyMinter != nil && svc.PackageName == proxyMinter.Package && svc.ServiceName == proxyMinter.ServiceName {
			if err := collectRoutesForService(dp, svc, plan); err != nil {
				return nil, fmt.Errorf("collecting minter routes for %s: %w", svc.ServiceName, err)
			}
			continue
		}

		protoInfo, err := dp.ConvertToProtoInfo(svc.ProtoFile)
		if err != nil {
			return nil, fmt.Errorf("parsing proto for dispatch %s: %w", svc.ProtoFile, err)
		}
		target := findService(protoInfo, svc.ServiceName)
		if target == nil {
			continue
		}

		// Resolved once per service: which physical service the gateway dials,
		// and which audience it entitles callers against. The two differ for
		// trait-hosted and event-labelled C2S surfaces.
		traitName := ""
		if svc.EventLabel == "" && svc.HostLabel != "" {
			hostFd, err := dp.GetFileDescriptor(svc.ProtoFile)
			if err != nil {
				return nil, fmt.Errorf("resolving trait identity for host-labelled service %s: %w", svc.ServiceName, err)
			}
			traitName = hostFd.VServiceName
		}
		serviceID, entitlementService, err := RouteIdentity(svc, traitName)
		if err != nil {
			return nil, err
		}

		for _, method := range target.Methods {
			if method.HttpPath == "" {
				continue
			}
			grpcPath := fmt.Sprintf("/%s.%s/%s", svc.PackageName, svc.ServiceName, method.Name)

			// Route entries for ALL methods (including bidi/client-streaming)
			// so a gRPC proxy can look up auth policy for non-REST-dispatchable RPCs.
			scopes, capability, err := capabilityMetadata(method)
			if err != nil {
				return nil, fmt.Errorf("resolving REST route capability metadata for %s.%s/%s: %w", svc.PackageName, svc.ServiceName, method.Name, err)
			}
			route := Route{
				HttpPath:             method.HttpPath,
				GrpcPath:             grpcPath,
				ServiceID:            serviceID,
				EntitlementService:   entitlementService,
				HttpMethods:          method.HttpMethods,
				AuthPolicy:           AuthPolicy(target, method),
				EntitlementScopes:    scopes,
				CapabilityScope:      capability,
				ServerStreaming:      method.ServerStreaming,
				ClientStreaming:      method.ClientStreaming,
				TargetNamespaceField: method.TargetNamespaceField,
				FormBody:             FormBody(method),
				RedirectField:        redirectField(method),
				RedirectJSONName:     RedirectJSONName(method),
			}
			plan.Routes = append(plan.Routes, route)
			if method.HttpAliasPath != "" {
				alias := route
				alias.HttpPath = method.HttpAliasPath
				plan.Routes = append(plan.Routes, alias)
			}

			// Client-streaming and bidi RPCs can't be dispatched as REST.
			if method.ClientStreaming {
				continue
			}
			isEmptyInput := method.FullInputType == "google.protobuf.Empty"
			isEmptyOutput := method.FullOutputType == "google.protobuf.Empty"
			plan.Arms = append(plan.Arms, Arm{
				GrpcPath:        grpcPath,
				ServiceID:       serviceID,
				PackageName:     svc.PackageName,
				ServiceName:     svc.ServiceName,
				ServiceDir:      svc.ParentDir,
				MethodName:      method.Name,
				InputType:       method.FullInputType,
				OutputType:      method.FullOutputType,
				IsEmptyInput:    isEmptyInput,
				IsEmptyOutput:   isEmptyOutput,
				ValidateInput:   !isEmptyInput && CanValidate(method.FullInputType),
				ValidateOutput:  !isEmptyOutput && CanValidate(method.FullOutputType),
				ServerStreaming: method.ServerStreaming,
			})
		}
	}

	// Vservice route entries for exported topics (gateway only). Route
	// discovery must fail generation if it cannot complete: a swallowed error
	// here would silently drop the gateway's topic routes.
	if config.IsGateway {
		registryDir := ""
		if config.InputDir != "" {
			registryPath := filepath.Join(config.InputDir, "registry.yaml")
			if _, err := os.Stat(registryPath); err == nil {
				registryDir = config.InputDir
			} else if err != nil && !os.IsNotExist(err) {
				return nil, fmt.Errorf("checking service registry for vservice route discovery: %w", err)
			}
		}
		vsReg, err := internalproto.LoadServiceRegistry(registryDir)
		if err != nil {
			return nil, fmt.Errorf("loading service registry for vservice route discovery: %w", err)
		}
		vsEntries, err := internalproto.ScanVServiceRPCs(inputDir, vsReg)
		if err != nil {
			return nil, fmt.Errorf("scanning vservice RPCs for REST routes: %w", err)
		}
		for _, vs := range vsEntries {
			for i, rpc := range vs.RPCs {
				clientStreaming := strings.HasPrefix(rpc, "Publish")
				serverStreaming := !strings.HasPrefix(rpc, "Publish")
				class, err := verbs.Classify(verbs.MethodShape{
					Name:            rpc,
					ClientStreaming: clientStreaming,
					ServerStreaming: serverStreaming,
				})
				if err != nil {
					return nil, fmt.Errorf("resolving vservice route capability metadata for %s/%s: %w", vs.Name, rpc, err)
				}
				// The gRPC path comes from Routes, never from HostLabel + rpc:
				// Routes holds what a caller must actually dial. The HTTP path
				// deliberately keeps the flat rpc name: it is a published URL
				// and the entitlement key, so renaming it is a separate,
				// breaking decision from fixing the wire.
				if i >= len(vs.Routes) {
					return nil, fmt.Errorf("vservice %s: no per-topic route for RPC %s (Routes and RPCs must stay index-aligned)", vs.Name, rpc)
				}
				route := vs.Routes[i]
				plan.Routes = append(plan.Routes, Route{
					HttpPath:           fmt.Sprintf("/%s/%s", vs.Name, rpc),
					GrpcPath:           fmt.Sprintf("/%s/%s", route.Service, route.Method),
					ServiceID:          vs.HostLabel,
					EntitlementService: vs.Name,
					HttpMethods:        []string{"POST"},
					AuthPolicy:         AuthEntitled,
					EntitlementScopes:  scopeNames(class),
					CapabilityScope:    string(class.CapabilityScope()),
					ServerStreaming:    serverStreaming, // Publish=client-streaming(unary path), Subscribe=server-streaming
					ClientStreaming:    clientStreaming,
				})
			}
		}
	}

	return plan, nil
}

// collectRoutesForService collects route entries for a service without
// building dispatch arms: the gateway's proxy minter needs routes in the table
// but its dispatch is handled specially.
func collectRoutesForService(dp *parser.DescriptorParser, svc Service, plan *Plan) error {
	protoInfo, err := dp.ConvertToProtoInfo(svc.ProtoFile)
	if err != nil {
		return err
	}
	target := findService(protoInfo, svc.ServiceName)
	if target == nil {
		return nil
	}
	for _, method := range target.Methods {
		if method.HttpPath == "" {
			continue
		}
		scopes, capability, err := capabilityMetadata(method)
		if err != nil {
			return fmt.Errorf("resolving REST route capability metadata for %s.%s/%s: %w", svc.PackageName, svc.ServiceName, method.Name, err)
		}
		route := Route{
			HttpPath:             method.HttpPath,
			GrpcPath:             fmt.Sprintf("/%s.%s/%s", svc.PackageName, svc.ServiceName, method.Name),
			ServiceID:            svc.PackageName,
			HttpMethods:          method.HttpMethods,
			AuthPolicy:           AuthPolicy(target, method),
			EntitlementScopes:    scopes,
			CapabilityScope:      capability,
			ServerStreaming:      method.ServerStreaming,
			ClientStreaming:      method.ClientStreaming,
			TargetNamespaceField: method.TargetNamespaceField,
			FormBody:             FormBody(method),
			RedirectField:        redirectField(method),
			RedirectJSONName:     RedirectJSONName(method),
		}
		plan.Routes = append(plan.Routes, route)
		if method.HttpAliasPath != "" {
			alias := route
			alias.HttpPath = method.HttpAliasPath
			plan.Routes = append(plan.Routes, alias)
		}
	}
	return nil
}

// collectProxyAuthDisabledRoutes adds proxy-only routes (no HTTP path, NoAuth)
// for an S2S service's auth_disabled methods. A gateway's generic gRPC proxy
// looks up the inbound grpc_path in the route table to decide auth; without an
// entry it defaults to Entitled. auth_disabled methods (e.g. the token
// minter's MintClientToken / MintServiceToken / TokenMint) authenticate via
// credentials in the request body and are designed to be reachable without a
// bearer, so the proxy must treat them as NoAuth. Non-auth_disabled S2S
// methods are NOT added: they stay at the proxy's Entitled default and the S2S
// backend authorizer fails closed for C2S callers, so nothing service-private
// is exposed.
func collectProxyAuthDisabledRoutes(dp *parser.DescriptorParser, svc Service, plan *Plan) error {
	// Enumerate from the RAW descriptor rather than ConvertToProtoInfo, which
	// would run the verb/pattern classification gate over EVERY method of the
	// service: a C2S/REST concern that must never be enforced on S2S surfaces
	// such as the record store's gRPC-only query verbs.
	fileDesc, err := dp.GetFileDescriptor(svc.ProtoFile)
	if err != nil {
		return err
	}
	var target *parser.ServiceDescriptor
	for i := range fileDesc.Services {
		if fileDesc.Services[i].Name == svc.ServiceName {
			target = &fileDesc.Services[i]
			break
		}
	}
	if target == nil {
		return nil
	}
	for _, method := range target.Methods {
		if !method.AuthDisabled {
			continue
		}
		plan.Routes = append(plan.Routes, Route{
			GrpcPath:        fmt.Sprintf("/%s.%s/%s", svc.PackageName, svc.ServiceName, method.Name),
			ServiceID:       svc.PackageName,
			AuthPolicy:      AuthNoAuth,
			ServerStreaming: method.ServerStreaming,
			ClientStreaming: method.ClientStreaming,
		})
	}
	return nil
}

// RouteIdentity resolves the two identities a route carries: ServiceID, the
// physical service the gateway dials, and EntitlementService, the audience it
// checks the caller's token against (empty when it is ServiceID's own).
//
// An event-labelled C2S service is declared in the client's contract package
// but physically served by the event broker. Its event label is the physical
// ServiceID, while its package remains the entitlement audience. EventLabel
// takes precedence over HostLabel if both are present because
// spec.event_label explicitly defines the gateway's physical service_id.
//
// For an ordinary C2S service the surface, the host and the audience are all
// the same name.
//
// A trait-hosted C2S surface splits them: (spec.service_c2s).label names the
// implementor that serves the RPCs, while the audience stays the TRAIT name.
// The audience is the contract, not the concrete service: pinning it to the
// host would re-couple every client to the implementor, so a second
// implementor of the same trait would force every client's token to be
// re-issued. This is the same pair the hosted-vservice topic routes emit
// (ServiceID = HostLabel, EntitlementService = vservice name).
func RouteIdentity(svc Service, traitName string) (serviceID, entitlementService string, err error) {
	if svc.EventLabel != "" {
		return svc.EventLabel, svc.PackageName, nil
	}
	if svc.HostLabel == "" {
		return svc.PackageName, "", nil
	}
	if traitName == "" {
		return "", "", fmt.Errorf("service %s.%s declares (spec.service_c2s).label %q but its proto declares no (spec.trait).name to entitle callers against", svc.PackageName, svc.ServiceName, svc.HostLabel)
	}
	return svc.HostLabel, traitName, nil
}

// AuthPolicy is the policy a method's route carries. Method-level annotations
// take precedence over service-level defaults.
func AuthPolicy(svc *parser.ServiceInfo, method *parser.MethodInfo) string {
	if method.AuthDisabled {
		return AuthNoAuth
	}
	if method.AuthAnyUser {
		return AuthAnyUser
	}
	if svc.AuthDisabled {
		return AuthNoAuth
	}
	if svc.AuthAnyUser {
		return AuthAnyUser
	}
	return AuthEntitled
}

// CanValidate reports whether a message type has a generated validator: user
// messages do, google.protobuf.* well-known types carry no assert_* constraints.
func CanValidate(fullType string) bool {
	fullType = strings.TrimPrefix(fullType, ".")
	return fullType != "" && fullType != "google.protobuf.Empty" && !strings.HasPrefix(fullType, "google.protobuf.")
}

// FormBody reports whether the method's REST route accepts a form body.
func FormBody(method *parser.MethodInfo) bool {
	return method.RestBrowser != nil && method.RestBrowser.FormBody
}

// RedirectJSONName is the proto3 JSON name of the field the method's REST
// route redirects to; empty when it answers JSON.
func RedirectJSONName(method *parser.MethodInfo) string {
	if method.RestBrowser == nil {
		return ""
	}
	return method.RestBrowser.RedirectJSONName
}

func redirectField(method *parser.MethodInfo) string {
	if method.RestBrowser == nil {
		return ""
	}
	return method.RestBrowser.RedirectField
}

func findService(protoInfo *parser.ProtoInfo, name string) *parser.ServiceInfo {
	for i := range protoInfo.Services {
		if protoInfo.Services[i].Name == name {
			return protoInfo.Services[i]
		}
	}
	return nil
}

func capabilityMetadata(method *parser.MethodInfo) (scopes []string, capability string, err error) {
	if method == nil || method.Class == nil {
		name := ""
		if method != nil {
			name = method.Name
		}
		return nil, "", fmt.Errorf("method %q has no resolved verb classification", name)
	}
	return scopeNames(*method.Class), string(method.Class.CapabilityScope()), nil
}

func scopeNames(class verbs.Classification) []string {
	admitting := class.ScopesAdmitting()
	scopes := make([]string, 0, len(admitting))
	for _, scope := range admitting {
		scopes = append(scopes, string(scope))
	}
	return scopes
}
