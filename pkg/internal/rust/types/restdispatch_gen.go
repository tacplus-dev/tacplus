// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/header"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/verbs"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/consts"
	internalproto "gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// dispatchArm holds resolved data for a single dispatch match arm.
type dispatchArm struct {
	GrpcPath        string // e.g., "/identity.IdentityService/GetUser"
	ServiceID       string // e.g., "identity" — target service for channel/auth resolution
	MethodSnake     string // e.g., "get_user"
	IsEmptyInput    bool
	IsEmptyOutput   bool
	ValidateOutput  bool
	ClientStreaming bool
	ClientPath      string // fully-qualified Rust tonic client path
	FromJsonPath    string // fully-qualified Rust from_json function path
	ToJsonPath      string // fully-qualified Rust to_json function path
}

// routeEntry holds data for one entry in the ROUTES static table.
type routeEntry struct {
	HttpPath           string
	GrpcPath           string
	ServiceID          string
	EntitlementService string // Rust Option<&str> literal: "None" or "Some(\"name\")"
	HttpMethodsLiteral string // e.g., `"POST", "GET"`
	AuthPolicy         string // "NoAuth", "AnyUser", "Entitled"
	EntitlementScopes  string // Rust static slice literal, e.g. `&["ANY", "READ"]`
	CapabilityScope    string // Rust Option<&str> literal: "None" or "Some(\"READ\")"
	ServerStreaming    bool
	ClientStreaming    bool
	TargetNamespace    string // Rust Option<&str> literal: "None" or "Some(\"field\")"
}

// restDispatchData is the template data for rest_dispatch.rs.tmpl.
type restDispatchData struct {
	Header             string
	RestRootPath       string
	HasStreaming       bool
	HasClientStreaming bool
	UnaryArms          []dispatchArm
	StreamArms         []dispatchArm
	Routes             []routeEntry
}

// proxyDispatchArm holds resolved data for a single proxy dispatch match arm.
type proxyDispatchArm struct {
	GrpcPath               string // e.g., "/sentinel.Dashboard/GetDashboard"
	IsEmptyInput           bool
	IsEmptyOutput          bool
	FromBinaryPath         string // fully-qualified Rust from_binary function for request type
	FromJsonPath           string // fully-qualified Rust from_json function for request type
	ResponseFromBinaryPath string // fully-qualified Rust from_binary function for response type
	ResponseToJsonPath     string // fully-qualified Rust to_json function for response type
	TargetNamespaceField   string // Rust field identifier for target_namespace_field, empty when unset
	AccountingMethod       string // Rust AccountingMethod literal, empty when no accounting enum entry exists
}

// proxyDispatchData is the template data for proxy_dispatch.rs.tmpl.
type proxyDispatchData struct {
	Header string
	Arms   []proxyDispatchArm
}

// generateRestDispatch generates the REST→gRPC dispatch module for services with
// rest_dispatch annotation. Replaces the standalone rest-dispatch-gen crate.
func generateRestDispatch(config *tactype.Config, servicesInfo []ServiceInfo, inputDir string) error {
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	// Build package → parentDir mapping for Rust path resolution.
	// Includes ALL proto packages (not just services) so cross-package type
	// references (e.g., tokenrequest.TokenMintRequest) resolve correctly.
	pkgToDir := buildPkgToDirMap(servicesInfo)
	for path, desc := range descriptorParser.GetAllFiles() {
		if _, exists := pkgToDir[desc.Package]; !exists {
			dir := filepath.Dir(path)
			if dir == "." {
				dir = ""
			}
			pkgToDir[desc.Package] = dir
		}
	}

	// The REST dispatch module lives inside the tacplus crate, so all type
	// references use `crate::` rather than the external crate name.
	cratePrefix := "crate"

	var unaryArms []dispatchArm
	var streamArms []dispatchArm
	var routes []routeEntry

	// Resolve the client-reachable (C2S proxy) token minter from the
	// (spec.token_minter) {proxy: true} declaration instead of the hardcoded
	// gateway/Mint pair (OF-3): the gateway mint front is whichever service
	// declares it, so renaming the service no longer silently drops its
	// special-cased dispatch.
	var proxyMinter *parser.TokenExchangerInfo
	for _, m := range descriptorParser.TokenMinters() {
		if m.IsProxy {
			mm := m
			proxyMinter = &mm
			break
		}
	}

	for _, svc := range servicesInfo {
		// S2S services aren't exposed through REST. But the gateway's generic
		// gRPC proxy still needs the auth policy of any auth_disabled methods it
		// proxies (e.g. the token minter's MintClientToken / MintServiceToken /
		// TokenMint), or it defaults them to Entitled and rejects the
		// unauthenticated bootstrap calls those methods are designed to accept.
		// Emit proxy-only ROUTES (no HTTP path, NoAuth) for those, then skip REST.
		if svc.IsS2S {
			if config.IsGateway {
				if err := collectProxyAuthDisabledRoutes(descriptorParser, svc, &routes); err != nil {
					return fmt.Errorf("collecting S2S auth-disabled proxy routes for %s: %w", svc.ServiceName, err)
				}
			}
			continue
		}

		// In gateway mode, skip the declared C2S proxy minter service (handled
		// specially in rest_handler), keyed off (spec.token_minter) not a literal.
		if config.IsGateway && proxyMinter != nil && svc.PackageName == proxyMinter.Package && svc.ServiceName == proxyMinter.ServiceName {
			// Still collect routes for the minter (needed for ROUTES table and handler).
			if err := collectRoutesForService(descriptorParser, svc, pkgToDir, cratePrefix, &routes); err != nil {
				return fmt.Errorf("collecting minter routes for %s: %w", svc.ServiceName, err)
			}
			continue
		}

		protoInfo, err := descriptorParser.ConvertToProtoInfo(svc.ProtoFile)
		if err != nil {
			return fmt.Errorf("parsing proto for dispatch %s: %w", svc.ProtoFile, err)
		}

		var targetService *parser.ServiceInfo
		for i := range protoInfo.Services {
			if protoInfo.Services[i].Name == svc.ServiceName {
				targetService = protoInfo.Services[i]
				break
			}
		}
		if targetService == nil {
			continue
		}

		for _, method := range targetService.Methods {
			if method.HttpPath == "" {
				continue
			}

			grpcPath := fmt.Sprintf("/%s.%s/%s", svc.PackageName, svc.ServiceName, method.Name)

			// Build route entries for ALL methods (including bidi/client-streaming)
			// so the gRPC proxy can look up auth policy for non-REST-dispatchable RPCs.
			authPolicy := resolveAuthPolicy(targetService, method)
			methodsLiteral := formatHttpMethods(method.HttpMethods)
			scopesLiteral, capabilityLiteral, err := routeCapabilityMetadata(method)
			if err != nil {
				return fmt.Errorf("resolving REST route capability metadata for %s.%s/%s: %w", svc.PackageName, svc.ServiceName, method.Name, err)
			}
			serviceID := svc.PackageName

			routes = append(routes, routeEntry{
				HttpPath:           method.HttpPath,
				GrpcPath:           grpcPath,
				ServiceID:          serviceID,
				EntitlementService: "None",
				HttpMethodsLiteral: methodsLiteral,
				AuthPolicy:         authPolicy,
				EntitlementScopes:  scopesLiteral,
				CapabilityScope:    capabilityLiteral,
				ServerStreaming:    method.ServerStreaming,
				ClientStreaming:    method.ClientStreaming,
				TargetNamespace:    rustOptionString(method.TargetNamespaceField),
			})

			if method.HttpAliasPath != "" {
				routes = append(routes, routeEntry{
					HttpPath:           method.HttpAliasPath,
					GrpcPath:           grpcPath,
					ServiceID:          serviceID,
					EntitlementService: "None",
					HttpMethodsLiteral: methodsLiteral,
					AuthPolicy:         authPolicy,
					EntitlementScopes:  scopesLiteral,
					CapabilityScope:    capabilityLiteral,
					ServerStreaming:    method.ServerStreaming,
					ClientStreaming:    method.ClientStreaming,
					TargetNamespace:    rustOptionString(method.TargetNamespaceField),
				})
			}

			// Client-streaming and bidi RPCs can't be dispatched as REST.
			if method.ClientStreaming {
				continue
			}

			methodSnake := prostSnakeName(method.Name)
			isEmptyInput := method.FullInputType == "google.protobuf.Empty"
			isEmptyOutput := method.FullOutputType == "google.protobuf.Empty"

			// Use the service's own ParentDir for its package to handle packages
			// that span both services/ and vservices/ (e.g. "stratum"). Without
			// this override pkgToDir["stratum"] would resolve to "services" even
			// when the service being dispatched lives in vservices/.
			svcPkgToDir := pkgToDir
			if pkgToDir[svc.PackageName] != svc.ParentDir {
				svcPkgToDir = make(map[string]string, len(pkgToDir))
				for k, v := range pkgToDir {
					svcPkgToDir[k] = v
				}
				svcPkgToDir[svc.PackageName] = svc.ParentDir
			}

			arm := dispatchArm{
				GrpcPath:        grpcPath,
				ServiceID:       serviceID,
				MethodSnake:     methodSnake,
				IsEmptyInput:    isEmptyInput,
				IsEmptyOutput:   isEmptyOutput,
				ValidateOutput:  !isEmptyOutput && rustW2SCanValidate(method.FullOutputType),
				ClientStreaming: method.ClientStreaming,
				ClientPath:      resolveClientPath(svc.ServiceName, svc.PackageName, svcPkgToDir, cratePrefix),
				FromJsonPath:    resolveFromJsonPath(method.FullInputType, svcPkgToDir, cratePrefix),
				ToJsonPath:      resolveToJsonPath(method.FullOutputType, svcPkgToDir, cratePrefix),
			}

			if method.ServerStreaming {
				streamArms = append(streamArms, arm)
			} else {
				unaryArms = append(unaryArms, arm)
			}
		}
	}

	// Add vservice route entries for exported topics (gateway only).
	if config.IsGateway {
		// REST dispatch route discovery must fail generation if it cannot
		// complete: a swallowed error here silently drops gateway routes for
		// exported topics (OF-1), so the gateway would generate without the
		// routes it is supposed to expose.
		registryDir := ""
		if config.InputDir != "" {
			registryPath := filepath.Join(config.InputDir, "registry.yaml")
			if _, err := os.Stat(registryPath); err == nil {
				registryDir = config.InputDir
			} else if err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("checking service registry for vservice route discovery: %w", err)
			}
		}
		vsReg, err := internalproto.LoadServiceRegistry(registryDir)
		if err != nil {
			return fmt.Errorf("loading service registry for vservice route discovery: %w", err)
		}
		vsEntries, err := internalproto.ScanVServiceRPCs(inputDir, vsReg)
		if err != nil {
			return fmt.Errorf("scanning vservice RPCs for REST routes: %w", err)
		}
		restRoot := descriptorParser.RestRootPath()
		for _, vs := range vsEntries {
			for i, rpc := range vs.RPCs {
				clientStreaming := strings.HasPrefix(rpc, "Publish")
				serverStreaming := !strings.HasPrefix(rpc, "Publish")
				scopesLiteral, capabilityLiteral, err := routeCapabilityMetadataFromShape(rpc, clientStreaming, serverStreaming, "")
				if err != nil {
					return fmt.Errorf("resolving vservice route capability metadata for %s/%s: %w", vs.Name, rpc, err)
				}
				// The gRPC path comes from Routes, never from HostTag + rpc.
				// This is the gateway's REST -> gRPC table: the browser-facing
				// way into eventbus. It was the THIRD place building this path,
				// and the last one still composing the flat broker-qualified
				// name from a host tag plus the RPC stem, after publishers and
				// subscribers had moved — which would have made every
				// REST-initiated publish dispatch to a method the broker is
				// about to stop serving.
				//
				// The HTTP path deliberately keeps the flat rpc name: it is a
				// published URL and the entitlement key, so renaming it is a
				// separate, breaking decision from fixing the wire.
				if i >= len(vs.Routes) {
					return fmt.Errorf("vservice %s: no per-topic route for RPC %s (Routes and RPCs must stay index-aligned)", vs.Name, rpc)
				}
				route := vs.Routes[i]
				grpcPath := fmt.Sprintf("/%s/%s", route.Service, route.Method)
				httpPath := fmt.Sprintf("%s/%s/%s", restRoot, vs.Name, rpc)
				routes = append(routes, routeEntry{
					HttpPath:           httpPath,
					GrpcPath:           grpcPath,
					ServiceID:          vs.HostTag,
					EntitlementService: fmt.Sprintf("Some(\"%s\")", vs.Name),
					HttpMethodsLiteral: `"POST"`,
					AuthPolicy:         "Entitled",
					EntitlementScopes:  scopesLiteral,
					CapabilityScope:    capabilityLiteral,
					ServerStreaming:    serverStreaming, // Publish=client-streaming(unary path), Subscribe=server-streaming
					ClientStreaming:    clientStreaming,
					TargetNamespace:    "None",
				})
			}
		}
	}

	hasStreaming := len(streamArms) > 0
	hasClientStreaming := false
	for _, arm := range append(unaryArms, streamArms...) {
		if arm.ClientStreaming {
			hasClientStreaming = true
			break
		}
	}

	data := restDispatchData{
		Header:             header.GeneratedHeader(consts.LangRust, "rest_dispatch"),
		RestRootPath:       descriptorParser.RestRootPath(),
		HasStreaming:       hasStreaming,
		HasClientStreaming: hasClientStreaming,
		UnaryArms:          unaryArms,
		StreamArms:         streamArms,
		Routes:             routes,
	}

	// Render template
	outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "rest_dispatch")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create rest_dispatch directory: %w", err)
	}
	outputPath := filepath.Join(outputDir, "mod.rs")

	zap.L().Info("Generating REST dispatch module",
		zap.String("path", outputPath),
		zap.Int("unaryArms", len(unaryArms)),
		zap.Int("streamArms", len(streamArms)),
		zap.Int("routes", len(routes)),
		zap.Bool("isGateway", config.IsGateway),
	)

	tmplStr, err := loadTemplate("rest_dispatch.rs.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load rest_dispatch template: %w", err)
	}

	// Use custom delimiters to avoid conflicts with Rust's {{ }} brace escaping.
	tmpl, err := template.New("rest_dispatch").Delims("<<", ">>").Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse rest_dispatch template: %w", err)
	}

	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, data); err != nil {
		return fmt.Errorf("failed to execute rest_dispatch template: %w", err)
	}

	return nil
}

// collectRoutesForService collects route entries for a service without building dispatch arms.
// Used for gateway.Mint which needs routes in the ROUTES table but dispatch is handled specially.
func collectRoutesForService(dp *parser.DescriptorParser, svc ServiceInfo, pkgToDir map[string]string, cratePrefix string, routes *[]routeEntry) error {
	protoInfo, err := dp.ConvertToProtoInfo(svc.ProtoFile)
	if err != nil {
		return err
	}
	var targetService *parser.ServiceInfo
	for i := range protoInfo.Services {
		if protoInfo.Services[i].Name == svc.ServiceName {
			targetService = protoInfo.Services[i]
			break
		}
	}
	if targetService == nil {
		return nil
	}
	for _, method := range targetService.Methods {
		if method.HttpPath == "" {
			continue
		}
		grpcPath := fmt.Sprintf("/%s.%s/%s", svc.PackageName, svc.ServiceName, method.Name)
		scopesLiteral, capabilityLiteral, err := routeCapabilityMetadata(method)
		if err != nil {
			return fmt.Errorf("resolving REST route capability metadata for %s.%s/%s: %w", svc.PackageName, svc.ServiceName, method.Name, err)
		}
		*routes = append(*routes, routeEntry{
			HttpPath:           method.HttpPath,
			GrpcPath:           grpcPath,
			ServiceID:          svc.PackageName,
			EntitlementService: "None",
			HttpMethodsLiteral: formatHttpMethods(method.HttpMethods),
			AuthPolicy:         resolveAuthPolicy(targetService, method),
			EntitlementScopes:  scopesLiteral,
			CapabilityScope:    capabilityLiteral,
			ServerStreaming:    method.ServerStreaming,
			ClientStreaming:    method.ClientStreaming,
			TargetNamespace:    rustOptionString(method.TargetNamespaceField),
		})
		if method.HttpAliasPath != "" {
			*routes = append(*routes, routeEntry{
				HttpPath:           method.HttpAliasPath,
				GrpcPath:           grpcPath,
				ServiceID:          svc.PackageName,
				EntitlementService: "None",
				HttpMethodsLiteral: formatHttpMethods(method.HttpMethods),
				AuthPolicy:         resolveAuthPolicy(targetService, method),
				EntitlementScopes:  scopesLiteral,
				CapabilityScope:    capabilityLiteral,
				ServerStreaming:    method.ServerStreaming,
				ClientStreaming:    method.ClientStreaming,
				TargetNamespace:    rustOptionString(method.TargetNamespaceField),
			})
		}
	}
	return nil
}

// collectProxyAuthDisabledRoutes adds proxy-only ROUTES entries (no HTTP path,
// NoAuth) for an S2S service's auth_disabled methods. The gateway's generic gRPC
// proxy looks up the inbound grpc_path in ROUTES to decide auth; without an
// entry it defaults to Entitled. auth_disabled methods (e.g. the token minter's
// MintClientToken / MintServiceToken / TokenMint) authenticate via credentials
// in the request body and are designed to be reachable without a bearer, so the
// proxy must treat them as NoAuth. Non-auth_disabled S2S methods are NOT added —
// they stay at the proxy's Entitled default and the S2S backend authorizer fails
// closed for C2S callers, so nothing service-private is exposed. HTTP path is
// empty: these are gRPC-proxy-only routes, never matched as REST.
func collectProxyAuthDisabledRoutes(dp *parser.DescriptorParser, svc ServiceInfo, routes *[]routeEntry) error {
	// Enumerate the S2S service's methods from the RAW descriptor rather than
	// ConvertToProtoInfo. The gateway's gRPC proxy only needs each auth_disabled
	// method's identity + streaming flags to emit a NoAuth proxy route (no HTTP
	// path, no entitlement). ConvertToProtoInfo would run the verb/pattern
	// classification (EnforceVerbs) gate over EVERY method of the service — a
	// C2S/REST concern that must never be enforced on S2S surfaces such as the
	// record store's Open/Pull/Count/Sample/Refine/Close query verbs, which are
	// gRPC-only and never REST-exposed.
	fileDesc, err := dp.GetFileDescriptor(svc.ProtoFile)
	if err != nil {
		return err
	}
	var targetService *parser.ServiceDescriptor
	for i := range fileDesc.Services {
		if fileDesc.Services[i].Name == svc.ServiceName {
			targetService = &fileDesc.Services[i]
			break
		}
	}
	if targetService == nil {
		return nil
	}
	for _, method := range targetService.Methods {
		if !method.AuthDisabled {
			continue
		}
		*routes = append(*routes, routeEntry{
			HttpPath:           "",
			GrpcPath:           fmt.Sprintf("/%s.%s/%s", svc.PackageName, svc.ServiceName, method.Name),
			ServiceID:          svc.PackageName,
			EntitlementService: "None",
			HttpMethodsLiteral: "",
			AuthPolicy:         "NoAuth",
			EntitlementScopes:  "&[]",
			CapabilityScope:    "None",
			ServerStreaming:    method.ServerStreaming,
			ClientStreaming:    method.ClientStreaming,
			TargetNamespace:    "None",
		})
	}
	return nil
}

// buildPkgToDirMap builds a mapping from proto package name to its parent directory.
// e.g., "identity" → "services", "eventbus" → "common"
func buildPkgToDirMap(servicesInfo []ServiceInfo) map[string]string {
	m := make(map[string]string)
	for _, svc := range servicesInfo {
		if _, exists := m[svc.PackageName]; !exists {
			m[svc.PackageName] = svc.ParentDir
		}
	}
	return m
}

// resolveClientPath builds the fully-qualified Rust path to a tonic client type.
// e.g., gateway_tacplus::types::services::identity::identity_service_client::IdentityServiceClient
func resolveClientPath(serviceName, packageName string, pkgToDir map[string]string, cratePrefix string) string {
	clientMod := tonicServiceModuleName(serviceName) + "_client"
	clientType := tonicServiceTypeName(serviceName) + "Client"
	if parentDir, ok := pkgToDir[packageName]; ok {
		modPath := strings.ReplaceAll(strings.ReplaceAll(parentDir, "-", "_"), string(filepath.Separator), "::")
		return fmt.Sprintf("%s::types::%s::%s::%s::%s", cratePrefix, modPath, packageName, clientMod, clientType)
	}
	return clientType
}

// resolveFromJsonPath builds the fully-qualified Rust path to a from_json function.
// e.g., gateway_tacplus::types::services::identity::get_user_request_from_json
func resolveFromJsonPath(protoFQN string, pkgToDir map[string]string, cratePrefix string) string {
	dot := strings.Index(protoFQN, ".")
	if dot >= 0 {
		pkg := protoFQN[:dot]
		typeName := protoFQN[dot+1:]
		if parentDir, ok := pkgToDir[pkg]; ok {
			modPath := strings.ReplaceAll(strings.ReplaceAll(parentDir, "-", "_"), string(filepath.Separator), "::")
			fnName := prostSnakeName(typeName) + "_from_json"
			return fmt.Sprintf("%s::types::%s::%s::%s", cratePrefix, modPath, pkg, fnName)
		}
	}
	typeName := protoFQN
	if dot >= 0 {
		typeName = protoFQN[dot+1:]
	}
	return prostSnakeName(typeName) + "_from_json"
}

// resolveToJsonPath builds the fully-qualified Rust path to a to_json function.
// e.g., gateway_tacplus::types::services::identity::get_user_response_to_json
func resolveToJsonPath(protoFQN string, pkgToDir map[string]string, cratePrefix string) string {
	dot := strings.Index(protoFQN, ".")
	if dot >= 0 {
		pkg := protoFQN[:dot]
		typeName := protoFQN[dot+1:]
		if parentDir, ok := pkgToDir[pkg]; ok {
			modPath := strings.ReplaceAll(strings.ReplaceAll(parentDir, "-", "_"), string(filepath.Separator), "::")
			fnName := prostSnakeName(typeName) + "_to_json"
			return fmt.Sprintf("%s::types::%s::%s::%s", cratePrefix, modPath, pkg, fnName)
		}
	}
	typeName := protoFQN
	if dot >= 0 {
		typeName = protoFQN[dot+1:]
	}
	return prostSnakeName(typeName) + "_to_json"
}

// resolveAuthPolicy determines the AuthPolicy for a specific RPC method.
// Method-level annotations take precedence over service-level defaults.
func resolveAuthPolicy(svc *parser.ServiceInfo, method *parser.MethodInfo) string {
	// Method-level overrides first.
	if method.AuthDisabled {
		return "NoAuth"
	}
	if method.AuthAnyUser {
		return "AnyUser"
	}
	// Fall back to service-level defaults.
	if svc.AuthDisabled {
		return "NoAuth"
	}
	if svc.AuthAnyUser {
		return "AnyUser"
	}
	return "Entitled"
}

func collectionMode(method *parser.MethodInfo) string {
	if method == nil || method.Collection == nil {
		return ""
	}
	return method.Collection.Mode
}

func routeCapabilityMetadata(method *parser.MethodInfo) (scopesLiteral, capabilityLiteral string, err error) {
	if method == nil || method.Class == nil {
		name := ""
		if method != nil {
			name = method.Name
		}
		return "", "", fmt.Errorf("method %q has no resolved verb classification", name)
	}
	return routeCapabilityMetadataFromClass(*method.Class), rustOptionString(string(method.Class.CapabilityScope())), nil
}

func routeCapabilityMetadataFromShape(methodName string, clientStreaming, serverStreaming bool, collectionMode string) (scopesLiteral, capabilityLiteral string, err error) {
	class, err := verbs.Classify(verbs.MethodShape{
		Name:            methodName,
		ClientStreaming: clientStreaming,
		ServerStreaming: serverStreaming,
		CollectionMode:  collectionMode,
	})
	if err != nil {
		return "", "", err
	}
	return routeCapabilityMetadataFromClass(class), rustOptionString(string(class.CapabilityScope())), nil
}

func routeCapabilityMetadataFromClass(class verbs.Classification) string {
	scopes := class.ScopesAdmitting()
	quoted := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		quoted = append(quoted, fmt.Sprintf("%q", string(scope)))
	}
	return "&[" + strings.Join(quoted, ", ") + "]"
}

// formatHttpMethods formats HTTP methods as a Rust static array literal.
// e.g., ["POST", "GET"] → `"POST", "GET"`
func formatHttpMethods(methods []string) string {
	quoted := make([]string, len(methods))
	for i, m := range methods {
		quoted[i] = fmt.Sprintf("%q", m)
	}
	return strings.Join(quoted, ", ")
}

func rustOptionString(value string) string {
	if value == "" {
		return "None"
	}
	return fmt.Sprintf("Some(%q)", value)
}

// resolveFromBinaryPath builds the fully-qualified Rust path to a from_binary function.
// e.g., crate::types::services::sentinel::get_dashboard_request_from_binary
func resolveFromBinaryPath(protoFQN string, pkgToDir map[string]string, cratePrefix string) string {
	dot := strings.Index(protoFQN, ".")
	if dot >= 0 {
		pkg := protoFQN[:dot]
		typeName := protoFQN[dot+1:]
		if parentDir, ok := pkgToDir[pkg]; ok {
			modPath := strings.ReplaceAll(strings.ReplaceAll(parentDir, "-", "_"), string(filepath.Separator), "::")
			fnName := prostSnakeName(typeName) + "_from_binary"
			return fmt.Sprintf("%s::types::%s::%s::%s", cratePrefix, modPath, pkg, fnName)
		}
	}
	typeName := protoFQN
	if dot >= 0 {
		typeName = protoFQN[dot+1:]
	}
	return prostSnakeName(typeName) + "_from_binary"
}

// generateProxyDispatch generates the proxy dispatch module for gateway.
// Produces validate_request() and response_to_json() functions that map
// grpc_path to the correct per-type from_binary/validate/to_json helpers.
func generateProxyDispatch(config *tactype.Config, servicesInfo []ServiceInfo, inputDir string, accountingMap map[string]service.AccountingInfo) error {
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	pkgToDir := buildPkgToDirMap(servicesInfo)
	for path, desc := range descriptorParser.GetAllFiles() {
		if _, exists := pkgToDir[desc.Package]; !exists {
			dir := filepath.Dir(path)
			if dir == "." {
				dir = ""
			}
			pkgToDir[desc.Package] = dir
		}
	}

	cratePrefix := "crate"
	var arms []proxyDispatchArm

	for _, svc := range servicesInfo {
		if svc.IsS2S {
			continue
		}

		protoInfo, err := descriptorParser.ConvertToProtoInfo(svc.ProtoFile)
		if err != nil {
			zap.L().Warn("Failed to parse proto for proxy dispatch", zap.String("proto", svc.ProtoFile), zap.Error(err))
			continue
		}

		var targetService *parser.ServiceInfo
		for i := range protoInfo.Services {
			if protoInfo.Services[i].Name == svc.ServiceName {
				targetService = protoInfo.Services[i]
				break
			}
		}
		if targetService == nil {
			continue
		}

		for _, method := range targetService.Methods {
			if method.HttpPath == "" {
				continue
			}

			grpcPath := fmt.Sprintf("/%s.%s/%s", svc.PackageName, svc.ServiceName, method.Name)
			isEmptyInput := method.FullInputType == "google.protobuf.Empty"
			isEmptyOutput := method.FullOutputType == "google.protobuf.Empty"
			if method.TargetNamespaceField != "" && isEmptyInput {
				return fmt.Errorf("%s target_namespace_field %q requires a non-empty request message", grpcPath, method.TargetNamespaceField)
			}

			// Use the service's own ParentDir for its package to handle packages
			// that span both services/ and vservices/ (e.g. "stratum").
			svcPkgToDir := pkgToDir
			if pkgToDir[svc.PackageName] != svc.ParentDir {
				svcPkgToDir = make(map[string]string, len(pkgToDir))
				for k, v := range pkgToDir {
					svcPkgToDir[k] = v
				}
				svcPkgToDir[svc.PackageName] = svc.ParentDir
			}

			arm := proxyDispatchArm{
				GrpcPath:         grpcPath,
				IsEmptyInput:     isEmptyInput,
				IsEmptyOutput:    isEmptyOutput,
				AccountingMethod: rustAccountingMethodLiteral(method, svc.GrpcName, accountingMap),
			}

			if !isEmptyInput {
				arm.FromBinaryPath = resolveFromBinaryPath(method.FullInputType, svcPkgToDir, cratePrefix)
				arm.FromJsonPath = resolveFromJsonPath(method.FullInputType, svcPkgToDir, cratePrefix)
			}
			if method.TargetNamespaceField != "" {
				arm.TargetNamespaceField = escapeRustKeyword(strutil.ToSnakeCase(method.TargetNamespaceField))
			}
			if !isEmptyOutput {
				arm.ResponseFromBinaryPath = resolveFromBinaryPath(method.FullOutputType, svcPkgToDir, cratePrefix)
				arm.ResponseToJsonPath = resolveToJsonPath(method.FullOutputType, svcPkgToDir, cratePrefix)
			}

			arms = append(arms, arm)
		}
	}

	data := proxyDispatchData{
		Header: header.GeneratedHeader(consts.LangRust, "proxy_dispatch"),
		Arms:   arms,
	}

	outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "proxy_dispatch")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create proxy_dispatch directory: %w", err)
	}
	outputPath := filepath.Join(outputDir, "mod.rs")

	zap.L().Info("Generating proxy dispatch module",
		zap.String("path", outputPath),
		zap.Int("arms", len(arms)),
	)

	tmplStr, err := loadTemplate("proxy_dispatch.rs.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load proxy_dispatch template: %w", err)
	}

	tmpl, err := template.New("proxy_dispatch").Delims("<<", ">>").Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse proxy_dispatch template: %w", err)
	}

	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, data); err != nil {
		return fmt.Errorf("failed to execute proxy_dispatch template: %w", err)
	}

	return nil
}
