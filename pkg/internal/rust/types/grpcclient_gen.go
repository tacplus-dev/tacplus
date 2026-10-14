// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/header"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/consts"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
)

// generateGrpcClients generates typed gRPC client wrappers for each gRPC service.
func generateGrpcClients(servicesInfo []ServiceInfo, inputDir string, packageToDirMap map[string]string) error {
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	// (spec.rpc).requires_device: the caller-side helper hashes the request
	// through the generated canonicalization, so a CALLER of a guarded rpc needs
	// the same "can this request be canonicalized at all" oracle the host uses.
	// Without it a caller emits a call to a function the types builder declined
	// to generate, and the failure lands as a missing symbol rather than as the
	// reason.
	var allFiles []model.ProtoFile
	for relPath := range descriptorParser.GetAllFiles() {
		if proto.IsInfraProto(relPath) {
			continue
		}
		mf, err := descriptorParser.ConvertToProtoFile(relPath)
		if err != nil {
			return fmt.Errorf("convert %s for requires_device resolution: %w", relPath, err)
		}
		allFiles = append(allFiles, mf)
	}
	san := newRustEgressSanitizer(allFiles)

	for _, svc := range servicesInfo {
		protoInfo, err := descriptorParser.ConvertToProtoInfo(svc.ProtoFile)
		if err != nil {
			return fmt.Errorf("failed to parse %s: %w", svc.ProtoFile, err)
		}

		if err := generateGrpcClient(svc, protoInfo, packageToDirMap, san); err != nil {
			return fmt.Errorf("failed to generate gRPC client for %s: %w", svc.ServiceName, err)
		}
	}

	// Generate aggregator files
	if len(servicesInfo) > 0 {
		if err := generateRustGrpcClientAggregators(servicesInfo); err != nil {
			return fmt.Errorf("failed to generate gRPC client aggregators: %w", err)
		}
	}

	return nil
}

// resolveMeshTarget determines the physical service ID for routing.
// Priority: TargetService from the active client surface > HostLabel > PackageName.
func resolveMeshTarget(svc ServiceInfo) string {
	config := svc.Config
	meshTarget := svc.PackageName
	if svc.HostLabel != "" {
		meshTarget = svc.HostLabel
	}
	entries := config.GrpcS2SClients
	switch config.GetSmPrefix() {
	case "c2sm":
		entries = config.GrpcC2SClients
	case "c2dm":
		entries = config.GrpcC2DClients
	case "w2sm":
		entries = config.GrpcW2SClients
	}
	for _, entry := range entries {
		if !rustGrpcEntryMatchesService(entry, svc) || entry.TargetService == "" {
			continue
		}
		meshTarget = entry.TargetService
		break
	}
	return meshTarget
}

func rustGrpcEntryMatchesService(entry tactype.GrpcEntry, svc ServiceInfo) bool {
	if entry.ProtoPackage != "" {
		return entry.ProtoPackage == svc.PackageName
	}
	if entry.ProtoFile == svc.ProtoFile {
		return true
	}
	return strings.TrimSuffix(filepath.Base(entry.ProtoFile), ".proto") == svc.PackageName
}

// TargetServiceInfo describes a target service and its gRPC clients.
// Exported so the SM template data can reference it.
type TargetServiceInfo struct {
	ServiceID    string // e.g., "identity", "oauth"
	StructName   string // e.g., "IdentityClients", "OauthClients"
	FieldName    string // e.g., "identity_clients", "oauth_clients"
	AccessorName string // e.g., "identity", "oauth"
}

// CollectTargetServices returns deduplicated target services for SM template data.
// ModPath is derived from the first service's ParentDir for each target.
// Only includes targets that have actual client services; no service is added
// by default.
func CollectTargetServices(servicesInfo []ServiceInfo) []TargetServiceInfo {
	seen := make(map[string]bool)
	var targets []TargetServiceInfo
	for _, svc := range servicesInfo {
		target := resolveMeshTarget(svc)
		if seen[target] {
			continue
		}
		seen[target] = true
		safe := strings.ReplaceAll(target, "-", "_")
		targets = append(targets, TargetServiceInfo{
			ServiceID:    target,
			StructName:   strutil.ToPascalCase(safe) + "Clients",
			FieldName:    strutil.ToSnakeCase(safe) + "_clients",
			AccessorName: strutil.ToSnakeCase(safe),
		})
	}
	return targets
}

// generateRustGrpcClientAggregators creates the flat 2-level hierarchy:
//
//	sm.grpc().{target}().{client}()
//
// Level 1: GrpcClients with .identity(), .stratum() etc. accessors
// Level 2: {Target}Clients with .mint_client() etc.
//
// All targets are globally unique. If two targets from different parentDirs
// have the same name, generation fails with a clear error.
func generateRustGrpcClientAggregators(servicesInfo []ServiceInfo) error {
	if len(servicesInfo) == 0 {
		return nil
	}
	config := servicesInfo[0].Config
	smDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "internal", "grpc", "clients")

	// Global name dedup.
	allSvcNameCount := make(map[string]int)
	for _, svc := range servicesInfo {
		allSvcNameCount[svc.ServiceName]++
	}

	// Group by meshTarget (flat — no parentDir intermediary).
	type targetEntry struct {
		target     string
		safeTarget string
		structName string
		fieldName  string
		svcs       []ServiceInfo
	}
	var targets []targetEntry
	targetSeen := make(map[string]int) // target → index in targets slice
	for _, svc := range servicesInfo {
		target := resolveMeshTarget(svc)
		safeTarget := strings.ReplaceAll(target, "-", "_")
		if idx, ok := targetSeen[target]; ok {
			targets[idx].svcs = append(targets[idx].svcs, svc)
		} else {
			targetSeen[target] = len(targets)
			targets = append(targets, targetEntry{
				target:     target,
				safeTarget: safeTarget,
				structName: strutil.ToPascalCase(safeTarget) + "Clients",
				fieldName:  strutil.ToSnakeCase(safeTarget),
				svcs:       []ServiceInfo{svc},
			})
		}
	}

	// Generate single grpc_clients.rs with GrpcClients + all {Target}Clients.
	rootFile := filepath.Join(smDir, "grpc_clients.rs")
	var buf strings.Builder
	buf.WriteString(header.GeneratedHeader(consts.LangRust, "grpc_clients"))
	buf.WriteString("\nuse servicemanager::ChannelProvider;\n\n")

	// Import TokenProvider from first client.
	firstSvc := servicesInfo[0]
	firstSvcSnake := strutil.StripPackagePrefix(tonicServiceModuleName(firstSvc.ServiceName), firstSvc.PackageName)
	fmt.Fprintf(&buf, "pub use super::%s::%s::%s::TokenProvider;\n\n",
		firstSvc.ParentDir, firstSvc.PackageName, firstSvcSnake)

	buf.WriteString("/// A factory that returns an audience-scoped TokenProvider for a given target service ID.\n")
	buf.WriteString("pub type TokenProviderFor = std::sync::Arc<dyn Fn(&str) -> Option<TokenProvider> + Send + Sync>;\n\n")

	// Import all individual clients (with parentDir in path for module resolution).
	for _, svc := range servicesInfo {
		svcSnake := strutil.StripPackagePrefix(tonicServiceModuleName(svc.ServiceName), svc.PackageName)
		origClientName := svc.ServiceName + "GrpcClient"
		if allSvcNameCount[svc.ServiceName] > 1 {
			aliasName := strutil.ToPascalCase(svc.PackageName) + svc.ServiceName + "GrpcClient"
			fmt.Fprintf(&buf, "use super::%s::%s::%s::%s as %s;\n",
				svc.ParentDir, svc.PackageName, svcSnake, origClientName, aliasName)
		} else {
			fmt.Fprintf(&buf, "use super::%s::%s::%s::%s;\n",
				svc.ParentDir, svc.PackageName, svcSnake, origClientName)
		}
	}

	// Per-target {Target}Clients structs.
	for _, te := range targets {
		fmt.Fprintf(&buf, "\n/// Typed gRPC clients for the \"%s\" service.\n", te.target)
		fmt.Fprintf(&buf, "pub struct %s {\n", te.structName)
		for _, svc := range te.svcs {
			fieldName, clientName := grpcClientNames(svc, allSvcNameCount)
			fmt.Fprintf(&buf, "    %s: %s,\n", fieldName, clientName)
		}
		buf.WriteString("}\n")
		fmt.Fprintf(&buf, "\nimpl %s {\n", te.structName)
		fmt.Fprintf(&buf, "    pub fn new(cp: ChannelProvider, token_provider: Option<TokenProvider>) -> Self {\n")
		buf.WriteString("        Self {\n")
		for _, svc := range te.svcs {
			fieldName, clientName := grpcClientNames(svc, allSvcNameCount)
			fmt.Fprintf(&buf, "            %s: %s::new(cp.clone(), token_provider.clone()),\n", fieldName, clientName)
		}
		buf.WriteString("        }\n    }\n\n")
		for _, svc := range te.svcs {
			fieldName, clientName := grpcClientNames(svc, allSvcNameCount)
			fmt.Fprintf(&buf, "    pub fn %s(&self) -> &%s {\n        &self.%s\n    }\n\n", fieldName, clientName, fieldName)
		}
		buf.WriteString("}\n")
	}

	// Target service ID constants.
	buf.WriteString("\n/// Target service ID constants (from proto annotations).\n")
	for _, te := range targets {
		fmt.Fprintf(&buf, "pub const TARGET_%s: &str = \"%s\";\n", strings.ToUpper(strings.ReplaceAll(te.target, "-", "_")), te.target)
	}

	// GrpcClients: flat root struct holding all targets directly.
	buf.WriteString("\n/// gRPC client hierarchy: sm.grpc().{target}().{client}()\n")
	buf.WriteString("pub struct GrpcClients {\n")
	for _, te := range targets {
		fmt.Fprintf(&buf, "    %s: %s,\n", te.fieldName, te.structName)
	}
	buf.WriteString("}\n\nimpl GrpcClients {\n    pub fn new(\n")
	for _, te := range targets {
		fmt.Fprintf(&buf, "        %s_cp: ChannelProvider,\n", te.fieldName)
	}
	buf.WriteString("        token_provider_for: &TokenProviderFor,\n    ) -> Self {\n        Self {\n")
	for _, te := range targets {
		fmt.Fprintf(&buf, "            %s: %s::new(%s_cp, token_provider_for(\"%s\")),\n",
			te.fieldName, te.structName, te.fieldName, te.target)
	}
	buf.WriteString("        }\n    }\n\n")
	for _, te := range targets {
		fmt.Fprintf(&buf, "    pub fn %s(&self) -> &%s {\n        &self.%s\n    }\n\n", te.fieldName, te.structName, te.fieldName)
	}
	buf.WriteString("}\n")

	if err := os.WriteFile(rootFile, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write gRPC clients: %w", err)
	}

	return nil
}

// generateGrpcClient creates the typed gRPC client wrapper Rust file for a service.
func generateGrpcClient(svc ServiceInfo, info *parser.ProtoInfo, _packageToDirMap map[string]string, san *rustEgressSanitizer) error {
	// Output to sm/internal/grpc/clients/{parentDir}/{packageName}/
	outputDir := filepath.Join(svc.Config.OutputDir, svc.Config.GetSmPrefix(), "internal", "grpc", "clients", svc.ParentDir, svc.PackageName)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	svcSnake := tonicServiceModuleName(svc.ServiceName)
	wrapperFile := strutil.StripPackagePrefix(svcSnake, svc.PackageName) + ".rs"
	outputPath := filepath.Join(outputDir, wrapperFile)

	zap.L().Info("Generating Rust gRPC client", zap.String("path", outputPath))

	// Find the target service
	var targetService *parser.ServiceInfo
	for i := range info.Services {
		if info.Services[i].Name == svc.ServiceName {
			targetService = info.Services[i]
			break
		}
	}
	if targetService == nil {
		return fmt.Errorf("service %s not found in proto info for %s", svc.ServiceName, svc.ProtoFile)
	}

	// Build service imports
	tonicName := tonicServiceTypeName(svc.ServiceName)
	tonicModuleName := tonicServiceModuleName(svc.ServiceName)
	typesCratePathStr := strings.ReplaceAll(strings.ReplaceAll(svc.ParentDir, "-", "_"), string(filepath.Separator), "::") + "::" + svc.PackageName
	var serviceImports strings.Builder
	fmt.Fprintf(&serviceImports, "use crate::types::%s::%s_client::%sClient;\n", typesCratePathStr, tonicModuleName, tonicName)
	// Import same-package types so method signatures can reference them
	fmt.Fprintf(&serviceImports, "#[allow(unused_imports)]\nuse crate::types::%s::*;\n", typesCratePathStr)

	// Build cross-package imports
	usedPkgs := make(map[string]bool)
	for _, method := range targetService.Methods {
		for _, fullType := range []string{method.FullInputType, method.FullOutputType} {
			parts := strings.Split(fullType, ".")
			if len(parts) > 1 {
				pkg := strings.Join(parts[:len(parts)-1], ".")
				if pkg != info.Package {
					usedPkgs[pkg] = true
				}
			}
		}
	}

	// Cross-package imports are NOT needed for gRPC clients.
	// Prost duplicates cross-package type definitions into each service's output file,
	// so `use crate::types::{service}::*` already provides all types used by the RPCs.
	// Adding `use crate::types::sys::*` etc. would cause ambiguous type resolution
	// (e.g., MapEmpty defined in both the service module and a sys module).
	var crossPkgImports strings.Builder

	// Build method implementations and extra types (bidi handles go outside impl block)
	var methods strings.Builder
	var extraTypes strings.Builder
	usesOutboundTimeout := false
	usesUnaryRetry := false
	hasRequiresDevice := false
	for _, method := range targetService.Methods {
		result, err := generateRustGrpcClientMethod(targetService, method, info.Package, info.CrossPackageTypes)
		if err != nil {
			return fmt.Errorf("failed to generate Rust gRPC client method %s.%s: %w", targetService.Name, method.Name, err)
		}
		methods.WriteString(result.method)
		methods.WriteString("\n")
		// (spec.rpc).requires_device: the caller-side half. Rust's generated
		// client is the seam Java uses too — neither has a separate per-rpc
		// ServiceManager helper layer the way Go does.
		if method.HasDeviceGate() {
			// The same two refusals the host makes (see servicewrapper_gen.go),
			// for the same reason and in the caller's own build: a request that
			// cannot be canonicalized identically in every language has no
			// approval hash to propose.
			if f := rustRequiresDeviceMapField(method.FullInputType, san, info.Package); f != "" {
				return fmt.Errorf("%s.%s declares (spec.rpc).%s, but its request reaches a map field (%s); map iteration order is not canonical across languages, so the approval hash could not be computed identically on both ends — remove the map or drop the annotation",
					targetService.Name, method.Name, method.DeviceGateAnnotation(), f)
			}
			if t := rustRequiresDeviceUnhashableType(method.FullInputType, san, info.Package); t != "" {
				return fmt.Errorf("%s.%s declares (spec.rpc).%s, but its request reaches %s, whose descriptor this generator cannot resolve; Rust canonicalizes by GENERATED code rather than by reflection, so the field would be silently absent from the Rust hash and present in the Go and Java one",
					targetService.Name, method.Name, method.DeviceGateAnnotation(), t)
			}
			hasRequiresDevice = true
			in, out := resolveRustTypeQualified(method, info.Package, info.CrossPackageTypes)
			methods.WriteString(rustRequiresDeviceClientHelpers(targetService, method, in, out, info.Package, svc.ParentDir, info.CrossPackageTypes))
		}
		if result.extraType != "" {
			extraTypes.WriteString(result.extraType)
			extraTypes.WriteString("\n")
		}
		usesOutboundTimeout = usesOutboundTimeout || result.usesOutboundTimeout
		usesUnaryRetry = usesUnaryRetry || result.usesUnaryRetry
	}

	clientName := svc.ServiceName + "GrpcClient"

	data := struct {
		Header              string
		ServiceName         string
		ClientName          string
		ServiceImports      string
		CrossPackageImports string
		Methods             string
		ExtraTypes          string
		UsesOutboundTimeout bool
		UsesUnaryRetry      bool
		HasRequiresDevice   bool
	}{
		Header:              header.GeneratedHeader(consts.LangRust, svc.ProtoFile),
		ServiceName:         tonicName,
		ClientName:          clientName,
		ServiceImports:      serviceImports.String(),
		CrossPackageImports: crossPkgImports.String(),
		Methods:             methods.String(),
		ExtraTypes:          extraTypes.String(),
		UsesOutboundTimeout: usesOutboundTimeout,
		UsesUnaryRetry:      usesUnaryRetry,
		HasRequiresDevice:   hasRequiresDevice,
	}

	tmplStr, err := loadTemplate("grpcclient.rs.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load grpcclient template: %w", err)
	}

	tmpl, err := template.New("grpcclient").Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, data); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	return nil
}

// rustAuthSnippet returns a Rust code block that injects a Bearer token into
// outgoing gRPC metadata for methods that require authentication.
// The reqVar parameter is the variable name holding the tonic::Request.
func grpcClientAuthRequired(svc *parser.ServiceInfo, method *parser.MethodInfo) bool {
	return !(svc.AuthDisabled || svc.IsC2D || method.AuthDisabled)
}

func rustAuthSnippet(reqVar string, authRequired bool) string {
	if !authRequired {
		return ""
	}
	return fmt.Sprintf(`let tp = self
            .token_provider
            .as_ref()
            .ok_or_else(|| tonic::Status::unauthenticated("auth token provider is required"))?;
        let token = (tp)().await.map_err(|e| tonic::Status::unauthenticated(format!("auth token: {e}")))?;
        if token.is_empty() {
            return Err(tonic::Status::unauthenticated("auth token is empty"));
        }
        %s.metadata_mut().insert(
            "authorization",
            format!("Bearer {}", token).parse().map_err(|_| tonic::Status::unauthenticated("invalid token"))?,
        );`, reqVar)
}

func rustGrpcRequestMut(authRequired bool) string {
	if authRequired {
		return "mut "
	}
	return ""
}

// grpcClientNames returns the field name and client type name for a service,
// prefixing with the package name when the service name collides with another.
func grpcClientNames(svc ServiceInfo, svcNameCount map[string]int) (fieldName, clientName string) {
	if svcNameCount[svc.ServiceName] > 1 {
		fieldName = svc.PackageName + "_" + tonicServiceModuleName(svc.ServiceName) + "_client"
		clientName = strutil.ToPascalCase(svc.PackageName) + svc.ServiceName + "GrpcClient"
	} else {
		fieldName = tonicServiceModuleName(svc.ServiceName) + "_client"
		clientName = svc.ServiceName + "GrpcClient"
	}
	return
}

// generateRustGrpcClientMethod generates a Rust method for the gRPC client wrapper.
// Each method resolves the active channel from the ChannelProvider on each call.
type grpcMethodResult struct {
	method    string
	extraType string // types that go outside the impl block (e.g. bidi handles)
	// usesOutboundTimeout is true when the generated method body calls
	// self.apply_outbound_timeout.
	usesOutboundTimeout bool
	// usesUnaryRetry is true when the generated method body calls
	// self.unary_with_retry (which also pulls in should_retry_outbound and
	// outbound_backoff).
	usesUnaryRetry bool
}

func rustGrpcRequestValidation(method *parser.MethodInfo, reqVar string) string {
	if method.FullInputType == "google.protobuf.Empty" {
		return ""
	}
	return fmt.Sprintf(`        %s.get_ref()
            .validate()
            .map_err(|e| tonic::Status::invalid_argument(format!("validation failed: {e}")))?;
`, reqVar)
}

func rustGrpcResponseValidation(method *parser.MethodInfo, respVar string) string {
	if method.FullOutputType == "google.protobuf.Empty" {
		return ""
	}
	return fmt.Sprintf(`        %s
            .get_ref()
            .validate()
            .map_err(|e| tonic::Status::internal(format!("response validation failed: {e}")))?;
`, respVar)
}

func rustGrpcStreamValueValidation(varName, fullType, errExpr string) string {
	if fullType == "google.protobuf.Empty" {
		return ""
	}
	return fmt.Sprintf(`        %s
            .validate()
            .map_err(|e| %s)?;
`, varName, errExpr)
}

func rustGrpcBidiResponseValidation(varName, fullType string) string {
	if fullType == "google.protobuf.Empty" {
		return ""
	}
	return fmt.Sprintf(`                                    if let Err(e) = %s.validate() {
                                        errors += 1;
                                        tracing::warn!(error = %%e, sent, received, errors, "response validation failed");
                                        break;
                                    }
`, varName)
}

func rustGrpcInitialStreamAuthBlock(authRequired bool) string {
	if !authRequired {
		return ""
	}
	return `                let tp = token_provider
                    .as_ref()
                    .ok_or_else(|| tonic::Status::unauthenticated("auth token provider is required"))?;
                let token = (tp)().await.map_err(|e| tonic::Status::unauthenticated(format!("auth token: {e}")))?;
                if token.is_empty() {
                    return Err(tonic::Status::unauthenticated("auth token is empty"));
                }
                req.metadata_mut().insert(
                    "authorization",
                    format!("Bearer {}", token).parse().map_err(|_| tonic::Status::unauthenticated("invalid token"))?,
                );
`
}

func generateRustGrpcClientMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, servicePkg string, crossPkgTypes map[string]string) (grpcMethodResult, error) {
	methodSnake := prostSnakeName(method.Name)
	inputType, outputType := resolveRustTypeQualified(method, servicePkg, crossPkgTypes)
	tonicClientName := tonicServiceTypeName(svc.Name) + "Client"
	authRequired := grpcClientAuthRequired(svc, method)

	// channelResolve creates a tonic client from the channel provider on each call.
	channelResolve := fmt.Sprintf("let channel = self.channel_provider.channel().await?;\n        let mut client = %s::new(channel);", tonicClientName)

	if method.ClientStreaming && method.ServerStreaming {
		// Bidirectional streaming — managed lifecycle with reconnection.
		// Uses a separate .tmpl file to avoid Go backtick escaping issues.
		//
		// Design rationale: watches (server-streaming) use callbacks for the
		// receive side because they are receive-only. Bidi streams need both
		// send and receive. Callbacks work for the receive side (consistent
		// with the watch pattern). A send channel is needed for the send side
		// because bidi introduces a direction that watches don't have.
		handleType := fmt.Sprintf("%sBidiHandle", strutil.ToPascalCase(method.Name))

		tmplStr, err := loadTemplate("grpcclient_bidi.rs.tmpl")
		if err != nil {
			return grpcMethodResult{}, fmt.Errorf("failed to load bidi template: %w", err)
		}

		data := struct {
			MethodName         string
			MethodSnake        string
			ServiceName        string
			InputType          string
			OutputType         string
			HandleType         string
			TonicClientName    string
			AuthRequired       bool
			RequestValidation  string
			ResponseValidation string
		}{
			MethodName:         method.Name,
			MethodSnake:        methodSnake,
			ServiceName:        svc.Name,
			InputType:          inputType,
			OutputType:         outputType,
			HandleType:         handleType,
			TonicClientName:    tonicClientName,
			AuthRequired:       authRequired,
			RequestValidation:  rustGrpcStreamValueValidation("msg", method.FullInputType, "tonic::Status::invalid_argument(format!(\"validation failed: {e}\"))"),
			ResponseValidation: rustGrpcBidiResponseValidation("resp", method.FullOutputType),
		}

		tmpl, err := template.New("bidi").Parse(tmplStr)
		if err != nil {
			return grpcMethodResult{}, fmt.Errorf("failed to parse bidi template: %w", err)
		}

		var buf strings.Builder
		if err := tmpl.Execute(&buf, data); err != nil {
			return grpcMethodResult{}, fmt.Errorf("failed to execute bidi template: %w", err)
		}

		// Append the handle struct (generated outside the impl block by the caller)
		handleStruct := fmt.Sprintf("\n/// Handle for the managed %s bidirectional stream.\n"+
			"/// Send messages via send(). The stream reconnects automatically.\n"+
			"#[derive(Clone)]\n"+
			"pub struct %s {\n"+
			"    tx: tokio::sync::mpsc::Sender<%s>,\n"+
			"}\n\n"+
			"impl %s {\n"+
			"    /// Send a message on the bidi stream.\n"+
			"    pub async fn send(&self, msg: %s) -> Result<(), tonic::Status> {\n"+
			"%s"+
			"        self.tx.send(msg).await.map_err(|_| tonic::Status::unavailable(\"bidi stream closed\"))\n"+
			"    }\n"+
			"}\n",
			method.Name, handleType, inputType, handleType, inputType, data.RequestValidation)

		return grpcMethodResult{method: buf.String(), extraType: handleStruct}, nil
	}

	if method.ServerStreaming && method.IsTier2SnapshotCollection() {
		// Tier-2 SNAPSHOT collection: server-streaming on the wire, but the client
		// seam returns a materialized collection — drain the stream into a Vec.
		auth := rustAuthSnippet("req", authRequired)
		reqMut := "mut "
		return grpcMethodResult{method: fmt.Sprintf(`    /// %s calls %s.%s (Tier-2 SNAPSHOT collection over server streaming).
    pub async fn %s(
        &self,
        request: impl tonic::IntoRequest<%s>,
    ) -> Result<Vec<%s>, tonic::Status> {
	        let %sreq = request.into_request();
%s
	        %s
	        self.apply_outbound_timeout(&mut req);
	        %s
	        let mut stream = client.%s(req).await?.into_inner();
        let mut items = Vec::new();
        while let Some(item) = stream.message().await? {
            items.push(item);
        }
        Ok(items)
    }`, method.Name, svc.Name, method.Name, methodSnake, inputType, outputType, reqMut, rustGrpcRequestValidation(method, "req"), auth, channelResolve, methodSnake), usesOutboundTimeout: true}, nil
	}

	if method.ServerStreaming && method.IsWatchCollection() {
		// WATCH collection: server-streaming envelope on the wire, surfaced as the
		// tonic Streaming<{Method}WatchEvent>; dropping it cancels the call.
		auth := rustAuthSnippet("req", authRequired)
		reqMut := rustGrpcRequestMut(authRequired)
		return grpcMethodResult{method: fmt.Sprintf(`    /// %s calls %s.%s (WATCH collection over server streaming).
    pub async fn %s(
        &self,
        request: impl tonic::IntoRequest<%s>,
    ) -> Result<tonic::Response<tonic::Streaming<%s>>, tonic::Status> {
	        let %sreq = request.into_request();
%s
	        %s
	        %s
	        client.%s(req).await
    }`, method.Name, svc.Name, method.Name, methodSnake, inputType, outputType, reqMut, rustGrpcRequestValidation(method, "req"), auth, channelResolve, methodSnake)}, nil
	}

	if method.ServerStreaming {

		// Server streaming
		auth := rustAuthSnippet("req", authRequired)
		reqMut := rustGrpcRequestMut(authRequired)
		return grpcMethodResult{method: fmt.Sprintf(`    /// %s calls %s.%s (server streaming).
    pub async fn %s(
        &self,
        request: impl tonic::IntoRequest<%s>,
    ) -> Result<tonic::Response<tonic::Streaming<%s>>, tonic::Status> {
	        let %sreq = request.into_request();
%s
	        %s
	        %s
	        client.%s(req).await
    }`, method.Name, svc.Name, method.Name, methodSnake, inputType, outputType, reqMut, rustGrpcRequestValidation(method, "req"), auth, channelResolve, methodSnake)}, nil
	}

	if method.ClientStreaming {
		// Client streaming
		handleType := fmt.Sprintf("%sClientStream", strutil.ToPascalCase(method.Name))
		methodCode := fmt.Sprintf(`    /// %s opens a managed client-streaming call to %s.%s.
    pub fn %s(&self) -> %s {
        let (tx, rx) = tokio::sync::mpsc::channel::<%s>(32);
        let (response_tx, response_rx) = tokio::sync::oneshot::channel::<Result<tonic::Response<%s>, tonic::Status>>();
        let cp = self.channel_provider.clone();
        let stream_token_provider = self.token_provider.clone();
        let handle_token_provider = self.token_provider.clone();

        tokio::spawn(async move {
            let token_provider = stream_token_provider;
            let result = async {
                let outbound = tokio_stream::wrappers::ReceiverStream::new(rx);
                let mut req = tonic::Request::new(outbound);
%s
                let channel = cp.channel().await?;
                let mut client = %s::new(channel);
                let response = client.%s(req).await?;
%s
                Ok(response)
            }.await;
            let _ = response_tx.send(result);
        });

        %s {
            tx,
            response: Some(response_rx),
            token_provider: handle_token_provider,
            auth_required: %t,
        }
    }`, method.Name, svc.Name, method.Name, methodSnake, handleType, inputType, outputType,
			rustGrpcInitialStreamAuthBlock(authRequired), tonicClientName, methodSnake, rustGrpcResponseValidation(method, "response"), handleType, authRequired)

		handleStruct := fmt.Sprintf(`
/// Handle for the managed %s client-streaming call.
pub struct %s {
    tx: tokio::sync::mpsc::Sender<%s>,
    response: Option<tokio::sync::oneshot::Receiver<Result<tonic::Response<%s>, tonic::Status>>>,
    token_provider: Option<TokenProvider>,
    auth_required: bool,
}

impl %s {
    /// Sends one request message on the client stream.
    pub async fn send(&self, msg: %s) -> Result<(), tonic::Status> {
%s        if self.auth_required {
            let tp = self
                .token_provider
                .as_ref()
                .ok_or_else(|| tonic::Status::unauthenticated("auth token provider is required"))?;
            let token = (tp)().await.map_err(|e| tonic::Status::unauthenticated(format!("auth token: {e}")))?;
            if token.is_empty() {
                return Err(tonic::Status::unauthenticated("auth token is empty"));
            }
        }
        self.tx
            .send(msg)
            .await
            .map_err(|_| tonic::Status::unavailable("client stream closed"))
    }

    /// Closes the send side and waits for the unary response.
    pub async fn finish(mut self) -> Result<tonic::Response<%s>, tonic::Status> {
        let response = self
            .response
            .take()
            .ok_or_else(|| tonic::Status::internal("client stream response already awaited"))?;
        drop(self.tx);
        response
            .await
            .map_err(|_| tonic::Status::unavailable("client stream response task stopped"))?
    }
}
`, method.Name, handleType, inputType, outputType, handleType, inputType,
			rustGrpcStreamValueValidation("msg", method.FullInputType, "tonic::Status::invalid_argument(format!(\"validation failed: {e}\"))"),
			outputType)
		return grpcMethodResult{method: methodCode, extraType: handleStruct}, nil
	}

	if method.IsPageCollection() {
		// PAGE: unary on the wire. Two client seams — an explicit single-page call
		// ({method}_page → (items, next_page_token, total)) and an auto-pager (the
		// natural method name) returning a futures::Stream that follows
		// next_page_token until exhausted, yielding each item. The auto-pager
		// advances the token on a request clone, so the caller's request is unchanged.
		itemType := rustPageItemType(method, servicePkg, crossPkgTypes)
		itemsField := prostSnakeName(method.Resolved.PageItemsField)
		auth := rustAuthSnippet("req", authRequired)
		totalExpr := "-1i64"
		if method.Resolved.PageHasTotal {
			totalExpr = "resp.total_count"
		}
		return grpcMethodResult{method: fmt.Sprintf(`    /// %s calls %s.%s and returns a single page: (items, next_page_token, total).
    ///
    /// next_page_token is empty on the last page; total is -1 when the response
    /// carries no count.
    pub async fn %s_page(
        &self,
        request: impl tonic::IntoRequest<%s>,
    ) -> Result<(Vec<%s>, String, i64), tonic::Status> {
        let mut req = request.into_request();
%s
        %s
        self.apply_outbound_timeout(&mut req);
        %s
        let resp = client.%s(req).await?.into_inner();
        let total: i64 = %s;
        Ok((resp.%s, resp.next_page_token, total))
    }

    /// %s calls %s.%s as an auto-pager: it follows next_page_token until
    /// exhausted, yielding each item across pages. The caller's request is not
    /// mutated (the page token is advanced on a clone).
    pub fn %s(
        &self,
        request: impl tonic::IntoRequest<%s>,
    ) -> impl futures::Stream<Item = Result<%s, tonic::Status>> + '_ {
        struct PagerState { req: %s, token: String, buf: std::collections::VecDeque<%s>, done: bool }
        let base = request.into_request().into_inner();
        let seed = PagerState { req: base, token: String::new(), buf: std::collections::VecDeque::new(), done: false };
        futures::stream::try_unfold(seed, move |mut st| async move {
            loop {
                if let Some(item) = st.buf.pop_front() {
                    return Ok(Some((item, st)));
                }
                if st.done {
                    return Ok(None);
                }
                let mut page_req = st.req.clone();
                page_req.page_token = st.token.clone();
                let (items, next, _total) = self.%s_page(page_req).await?;
                st.buf.extend(items);
                if next.is_empty() { st.done = true; } else { st.token = next; }
            }
        })
    }`, method.Name, svc.Name, method.Name,
			methodSnake, inputType, itemType,
			rustGrpcRequestValidation(method, "req"), auth, channelResolve, methodSnake,
			totalExpr, itemsField,
			method.Name, svc.Name, method.Name,
			methodSnake, inputType, itemType,
			inputType, itemType,
			methodSnake), usesOutboundTimeout: true}, nil
	}

	// Unary method
	auth := rustAuthSnippet("req", authRequired)
	reqMut := "mut "
	retrySafe := method.IsRetrySafeUnary()
	return grpcMethodResult{method: fmt.Sprintf(`    /// %s calls %s.%s with optional validation.
	    pub async fn %s(
	        &self,
	        request: impl tonic::IntoRequest<%s>,
	    ) -> Result<tonic::Response<%s>, tonic::Status> {
	        let req_base = request.into_request();
%s
	        let req_message = req_base.get_ref().clone();
	        let req_metadata = req_base.metadata().clone();
	        self.unary_with_retry(%t, || {
	            let %sreq = {
	                let mut req = tonic::Request::new(req_message.clone());
	                *req.metadata_mut() = req_metadata.clone();
	                req
	            };
	            async move {
	                %s
	                self.apply_outbound_timeout(&mut req);
	                %s
	                client.%s(req).await
	            }
	        }).await
            .and_then(|response| {
%s
                Ok(response)
            })
	    }`, method.Name, svc.Name, method.Name, methodSnake, inputType, outputType, rustGrpcRequestValidation(method, "req_base"), retrySafe, reqMut, auth, channelResolve, methodSnake, rustGrpcResponseValidation(method, "response")), usesOutboundTimeout: true, usesUnaryRetry: true}, nil
}
