// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/restdispatch"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// dispatchArm holds resolved data for a single dispatch match arm.
type dispatchArm struct {
	GrpcPath       string // e.g., "/identity.IdentityService/GetUser"
	ServiceID      string // e.g., "identity" — target service for channel/auth resolution
	MethodSnake    string // e.g., "get_user"
	IsEmptyInput   bool
	IsEmptyOutput  bool
	ValidateInput  bool
	ValidateOutput bool
	ClientPath     string // fully-qualified Rust tonic client path
	FromJsonPath   string // fully-qualified Rust from_json function path
	ToJsonPath     string // fully-qualified Rust to_json function path
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
	FormBody           bool   // (spec.rest_browser).form_body
	RedirectField      string // Rust Option<&str> literal: "None" or the redirect field's JSON key
}

// restDispatchData is the template data for rest_dispatch.rs.tmpl.
type restDispatchData struct {
	Header       string
	HasStreaming bool
	UnaryArms    []dispatchArm
	StreamArms   []dispatchArm
	Routes       []routeEntry
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
// Which routes and arms exist is decided by the shared planner
// (pkg/internal/common/restdispatch); this renders them as Rust.
func generateRestDispatch(config *tactype.Config, servicesInfo []ServiceInfo, inputDir string) error {
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	services := make([]restdispatch.Service, 0, len(servicesInfo))
	for _, svc := range servicesInfo {
		services = append(services, planService(svc))
	}
	plan, err := restdispatch.Collect(descriptorParser, config, services, inputDir)
	if err != nil {
		return err
	}

	// The REST dispatch module lives inside the tacplus crate, so all type
	// references use `crate::` rather than the external crate name.
	cratePrefix := "crate"

	var unaryArms []dispatchArm
	var streamArms []dispatchArm
	for _, a := range plan.Arms {
		pkgToDir := plan.PkgToDirFor(a)
		arm := dispatchArm{
			GrpcPath:       a.GrpcPath,
			ServiceID:      a.ServiceID,
			MethodSnake:    prostSnakeName(a.MethodName),
			IsEmptyInput:   a.IsEmptyInput,
			IsEmptyOutput:  a.IsEmptyOutput,
			ValidateInput:  a.ValidateInput,
			ValidateOutput: a.ValidateOutput,
			ClientPath:     resolveClientPath(a.ServiceName, a.PackageName, pkgToDir, cratePrefix),
			FromJsonPath:   resolveFromJsonPath(a.InputType, pkgToDir, cratePrefix),
			ToJsonPath:     resolveToJsonPath(a.OutputType, pkgToDir, cratePrefix),
		}
		if a.ServerStreaming {
			streamArms = append(streamArms, arm)
		} else {
			unaryArms = append(unaryArms, arm)
		}
	}

	routes := make([]routeEntry, 0, len(plan.Routes))
	for _, r := range plan.Routes {
		routes = append(routes, rustRouteEntry(r))
	}

	data := restDispatchData{
		Header:       header.GeneratedHeader(consts.LangRust, "rest_dispatch"),
		HasStreaming: len(streamArms) > 0,
		UnaryArms:    unaryArms,
		StreamArms:   streamArms,
		Routes:       routes,
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

	tmpl, err := parseRestDispatchTemplate()
	if err != nil {
		return err
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

// planService is the slice of a Rust ServiceInfo the shared planner reads.
func planService(svc ServiceInfo) restdispatch.Service {
	return restdispatch.Service{
		ProtoFile:   svc.ProtoFile,
		PackageName: svc.PackageName,
		ServiceName: svc.ServiceName,
		ParentDir:   svc.ParentDir,
		IsS2S:       svc.IsS2S,
		EventLabel:  svc.EventLabel,
		HostLabel:   svc.HostLabel,
	}
}

// rustRouteEntry renders one planned route as the ROUTES table literals.
func rustRouteEntry(r restdispatch.Route) routeEntry {
	return routeEntry{
		HttpPath:           r.HttpPath,
		GrpcPath:           r.GrpcPath,
		ServiceID:          r.ServiceID,
		EntitlementService: rustOptionString(r.EntitlementService),
		HttpMethodsLiteral: formatHttpMethods(r.HttpMethods),
		AuthPolicy:         r.AuthPolicy,
		EntitlementScopes:  rustStrSlice(r.EntitlementScopes),
		CapabilityScope:    rustOptionString(r.CapabilityScope),
		ServerStreaming:    r.ServerStreaming,
		ClientStreaming:    r.ClientStreaming,
		TargetNamespace:    rustOptionString(r.TargetNamespaceField),
		FormBody:           r.FormBody,
		RedirectField:      rustOptionString(r.RedirectJSONName),
	}
}

// restFormBody reports whether the method's REST route accepts a form body.
func restFormBody(method *parser.MethodInfo) bool {
	return restdispatch.FormBody(method)
}

// restRedirectField is the Rust Option<&str> literal for the JSON key the
// method's REST route redirects to; "None" when it answers JSON.
func restRedirectField(method *parser.MethodInfo) string {
	return rustOptionString(restdispatch.RedirectJSONName(method))
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

// rustStrSlice formats strings as a Rust static slice literal, e.g. `&["ANY", "READ"]`.
func rustStrSlice(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, v := range values {
		quoted = append(quoted, rustQuote(v))
	}
	return "&[" + strings.Join(quoted, ", ") + "]"
}

// formatHttpMethods formats HTTP methods as a Rust static array literal.
// e.g., ["POST", "GET"] → `"POST", "GET"`
func formatHttpMethods(methods []string) string {
	quoted := make([]string, len(methods))
	for i, m := range methods {
		quoted[i] = rustQuote(m)
	}
	return strings.Join(quoted, ", ")
}

// parseRestDispatchTemplate loads and parses rest_dispatch.rs.tmpl.
func parseRestDispatchTemplate() (*template.Template, error) {
	tmplStr, err := loadTemplate("rest_dispatch.rs.tmpl")
	if err != nil {
		return nil, fmt.Errorf("failed to load rest_dispatch template: %w", err)
	}

	// Use custom delimiters to avoid conflicts with Rust's {{ }} brace escaping.
	tmpl, err := template.New("rest_dispatch").
		Delims("<<", ">>").
		Funcs(template.FuncMap{"rustStr": rustStringEscape}).
		Parse(tmplStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse rest_dispatch template: %w", err)
	}
	return tmpl, nil
}

// rustStringEscape escapes a value for interpolation between the quotes of a
// Rust string literal. The REST paths, gRPC paths and service IDs it guards are
// proto-annotation-derived, so an author-supplied quote or backslash would
// otherwise terminate or corrupt the emitted literal.
func rustStringEscape(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case 0:
			b.WriteString(`\0`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u{%x}`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	return b.String()
}

func rustOptionString(value string) string {
	if value == "" {
		return "None"
	}
	return fmt.Sprintf("Some(%s)", rustQuote(value))
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

// routeIdentityForService resolves the two identities a ROUTES entry carries:
// ServiceID, the physical service the gateway dials, and EntitlementService,
// the audience it checks the caller's token against, as a Rust Option literal.
// The rules live in the shared planner (restdispatch.RouteIdentity).
func routeIdentityForService(svc ServiceInfo, traitName string) (serviceID, entitlementService string, err error) {
	serviceID, audience, err := restdispatch.RouteIdentity(planService(svc), traitName)
	if err != nil {
		return "", "", err
	}
	return serviceID, rustOptionString(audience), nil
}
