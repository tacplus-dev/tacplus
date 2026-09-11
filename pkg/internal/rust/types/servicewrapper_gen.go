// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/boundaries"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/header"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/consts"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
)

// NOTE: Parent mod.rs files (e.g., services/foobar/mod.rs declaring "pub mod types;" and
// "pub mod services;") are created by generateModuleStructure in builder.go, which discovers
// all directories and creates appropriate mod.rs entries. Service wrapper generation must
// run BEFORE generateModuleStructure so that the services/ directories are picked up.

// generateServiceWrappers generates servicewrapper.rs for each service in the services directory.
// Uses a single DescriptorParser rooted at inputDir so protoc can resolve cross-package imports.
// The bool reports whether any wrapper got a generated MCP termination, which
// is what decides whether the model crate depends on the MCP SDK.
func generateServiceWrappers(servicesInfo []ServiceInfo, inputDir string, packageToDirMap map[string]string, accountingMap map[string]service.AccountingInfo, skippedTraceTypes proto.SkippedTraceTypes) (bool, error) {
	// Create one descriptor parser for the entire proto root (like Go does)
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return false, fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	// Build the egress-sanitize oracle over the (entitlement-augmented) descriptor
	// set, mirroring the helpers emitter's file set (non-infra), so the wrapper
	// emits a sanitize_<t> call exactly where the helpers emitted one.
	var allFiles []model.ProtoFile
	for relPath := range descriptorParser.GetAllFiles() {
		if proto.IsInfraProto(relPath) {
			continue
		}
		mf, err := descriptorParser.ConvertToProtoFile(relPath)
		if err != nil {
			return false, fmt.Errorf("convert %s for egress sanitize resolution: %w", relPath, err)
		}
		allFiles = append(allFiles, mf)
	}
	san := newRustEgressSanitizer(allFiles)

	// Q2: resolve the accounting topic once so every wrapper can drop that
	// topic's own publish RPC from its accounting map. See
	// accounting_exclusion.go for why this exclusion is mandatory.
	accountingTopic, hasAccountingTopic, err := boundaries.LookupAccountingRecordTopic(descriptorParser)
	if err != nil {
		return false, fmt.Errorf("resolve accounting record topic: %w", err)
	}

	hasMcp := false
	for _, svc := range servicesInfo {
		// Use the descriptor parser to get method information
		protoInfo, err := descriptorParser.ConvertToProtoInfo(svc.ProtoFile)
		if err != nil {
			return false, fmt.Errorf("failed to parse %s: %w", svc.ProtoFile, err)
		}
		for _, s := range protoInfo.Services {
			if s.Name == svc.ServiceName && s.HasMcpRpcs {
				hasMcp = true
			}
		}

		// Generate servicewrapper.rs in the services output directory
		svcAccountingMap := excludeAccountingSelfPublish(
			accountingMap, svc.GrpcName, protoInfo.Package, svc.ServiceName,
			accountingTopic, hasAccountingTopic)
		if err := generateServiceWrapper(svc, protoInfo, packageToDirMap, svcAccountingMap, skippedTraceTypes, san); err != nil {
			return false, fmt.Errorf("failed to generate servicewrapper for %s: %w", svc.ServiceName, err)
		}
	}
	return hasMcp, nil
}

// generateServiceWrapper creates a {service_snake}.rs file for a single service.
// Each service in a proto file gets its own file.
// The file is placed in the grpcservers/ directory (matching Go convention).
func generateServiceWrapper(svc ServiceInfo, info *parser.ProtoInfo, _packageToDirMap map[string]string, accountingMap map[string]service.AccountingInfo, skippedTraceTypes proto.SkippedTraceTypes, san *rustEgressSanitizer) error {
	// Determine wrapper filename: {service_snake}.rs (matching Go convention)
	svcSnake := tonicServiceModuleName(svc.ServiceName)
	wrapperFile := strutil.StripPackagePrefix(svcSnake, svc.PackageName) + ".rs"
	wrapperName := "GrpcServer"
	if !svc.IsGrpc {
		wrapperName = "StateClient"
	}
	if err := os.MkdirAll(svc.OutputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output dir for %s: %w", svc.ServiceName, err)
	}
	outputPath := filepath.Join(svc.OutputDir, wrapperFile)

	zap.L().Info("Generating service wrapper", zap.String("service", svc.ServiceName), zap.String("path", outputPath))

	// Find the target service in the proto info
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

	serviceName := svc.ServiceName
	// tonicName/moduleName match the exact tonic/prost-generated service type
	// and module names (e.g. "ContractS2S" → ContractS2s, contract_s2s).
	tonicName := tonicServiceTypeName(serviceName)
	tonicModuleName := tonicServiceModuleName(serviceName)

	// Build service-specific imports for tonic-generated client/server types
	// These live in submodules (e.g. foo_bar_service_client::FooBarServiceClient)
	// The wrapper is in the services/ directory (sibling to the package module).
	// e.g., for baseball: wrapper is at types/services/baseball/services/
	// and proto types are at types/services/baseball/services/ (the services.rs prost output)
	// But the tonic service modules are within the services proto module itself.
	// We use super:: to reach the parent (types/services/baseball/) and then
	// go into the package-named sibling module.
	var serviceImports strings.Builder
	typesCratePathStr := strings.ReplaceAll(strings.ReplaceAll(svc.ParentDir, "-", "_"), string(filepath.Separator), "::") + "::" + svc.PackageName
	fmt.Fprintf(&serviceImports, "use crate::types::%s::%s_client::%sClient;\n", typesCratePathStr, tonicModuleName, tonicName)
	fmt.Fprintf(&serviceImports, "use crate::types::%s::%s_server::{%s, %sServer};\n", typesCratePathStr, tonicModuleName, tonicName, tonicName)

	// Build cross-package type imports (only for packages referenced by the target service)
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

	// Cross-package imports are NOT needed for service wrappers.
	// Prost duplicates cross-package type definitions into each service's output file,
	// so `use crate::types::{service}::*` already provides all types used by the RPCs.
	// Adding `use crate::types::sys::*` etc. would cause ambiguous type resolution
	// (e.g., MapEmpty defined in both the service module and a sys module).
	var crossPkgImports strings.Builder

	// Build service client field for ServiceWrapper (only the target service)
	var clientFields strings.Builder
	fmt.Fprintf(&clientFields, "pub %s_client: Option<%sClient<tonic::transport::Channel>>,\n", svcSnake, tonicName)

	// Build gRPC proxy method implementations for ServiceWrapper (only target service)
	var proxyMethods strings.Builder
	for _, method := range targetService.Methods {
		if method.IsMcp {
			proxyMethods.WriteString(generateRustMcpRefusalMethod(targetService, method, info.Package, info.CrossPackageTypes))
			proxyMethods.WriteString("\n")
			continue
		}
		proxyMethods.WriteString(generateRustProxyMethod(targetService, method, info.Package, info.CrossPackageTypes))
		proxyMethods.WriteString("\n")
	}

	// Build client initialization for ServiceWrapper (only target service)
	var clientInitialization strings.Builder
	fmt.Fprintf(&clientInitialization, "self.%s_client = Some(%sClient::new(state_channel.clone()));\n", svcSnake, tonicName)

	// The generated MCP session carrier, when this service has one. It is the
	// one rpc the ValidationWrapper serves without a handler method.
	mcpCarrier := rustMcpCarrierMethod(targetService)

	// Build Handler trait (clean interface with Caller parameter)
	var handlerTrait strings.Builder
	handlerTrait.WriteString(fmt.Sprintf("/// %sHandler defines the clean handler interface for %s.\n", serviceName, serviceName))
	handlerTrait.WriteString(fmt.Sprintf("/// Implement this trait instead of the raw tonic %s trait.\n", serviceName))
	handlerTrait.WriteString("/// The caller parameter provides the authenticated caller's identity (None when disable_auth = true).\n")
	handlerTrait.WriteString("#[tonic::async_trait]\n")
	handlerTrait.WriteString(fmt.Sprintf("pub trait %sHandler: Send + Sync + 'static {\n", serviceName))
	for _, method := range targetService.Methods {
		// The generated session carrier is NOT a handler method: carrying an
		// MCP session is nothing the service writes, and the ValidationWrapper
		// serves it from the generated dispatch below
		// (generateRustMcpCarrierWrapper). Its tools ARE handler methods —
		// tacplus generates the interface, the service implements the body, the
		// same contract as any USER/SERVICE rpc (decision doc section 8). Only
		// the registrar differs: an MCP tool reaches its handler through the
		// tool dispatcher rather than the gRPC service registrar.
		if method == mcpCarrier {
			continue
		}
		handlerTrait.WriteString(generateRustHandlerMethod(method, info.Package, info.CrossPackageTypes))
		handlerTrait.WriteString("\n")
	}
	handlerTrait.WriteString("}\n")

	// Emit a default stub impl: {Service}HandlerStub returns Unimplemented
	// for every method. Used by the SM Handler trait's default impl for
	// hosted vservices, so services can compile without overriding methods
	// for vservices they don't actually serve yet.
	handlerTrait.WriteString("\n")
	handlerTrait.WriteString(fmt.Sprintf("/// %sHandlerStub is a default implementation that returns Unimplemented for every method.\n", serviceName))
	handlerTrait.WriteString("/// Used as a placeholder by the generated Handler trait so hosted vservices can be\n")
	handlerTrait.WriteString("/// added without forcing every host service to provide an impl immediately.\n")
	handlerTrait.WriteString(fmt.Sprintf("pub struct %sHandlerStub;\n\n", serviceName))
	handlerTrait.WriteString("#[tonic::async_trait]\n")
	handlerTrait.WriteString(fmt.Sprintf("impl %sHandler for %sHandlerStub {\n", serviceName, serviceName))
	for _, method := range targetService.Methods {
		if method == mcpCarrier {
			continue
		}
		handlerTrait.WriteString(generateRustHandlerStubMethod(method, info.Package, serviceName, info.CrossPackageTypes))
		handlerTrait.WriteString("\n")
	}
	handlerTrait.WriteString("}\n")

	// Build gRPC method wrappers with validation (only target service)
	// hasAccounting is true when the service's grpcName has a matching entry
	// in the AccountingMethod proto AND at least one of this service's methods
	// is in the accounting enum.
	hasAccounting := false
	if amInfo, ok := accountingMap[svc.GrpcName]; ok {
		for _, m := range targetService.Methods {
			if amInfo.RPCs[m.Name] {
				hasAccounting = true
				break
			}
		}
	}
	var grpcWrappers, watchFields, watchFieldsInit strings.Builder
	var methodAuthLines []string
	for _, method := range targetService.Methods {
		// The session carrier is served by the generated MCP dispatch rather
		// than by a handler method; it still authorizes exactly as the ordinary
		// bidi wrapper does.
		if method == mcpCarrier {
			grpcWrappers.WriteString(generateRustMcpCarrierWrapper(targetService, method, info.Package, svc.ParentDir, info.CrossPackageTypes))
			grpcWrappers.WriteString("\n")
			// The carrier authorizes exactly as the ordinary bidi wrapper does
			// (comment above), so it needs the same set_method_auth
			// registration: method-level override if present, else the
			// service-level pattern.
			carrierAuth := svc.AuthServices
			if len(method.AuthServices) > 0 {
				carrierAuth = method.AuthServices
			}
			if len(carrierAuth) > 0 {
				var quoted []string
				for _, pattern := range carrierAuth {
					quoted = append(quoted, fmt.Sprintf("%q", pattern))
				}
				methodAuthLines = append(methodAuthLines, fmt.Sprintf(
					"    auth.set_method_auth(%q, &[%s]);",
					method.Name, strings.Join(quoted, ", ")))
			}
			continue
		}
		// An MCP tool gets no gRPC method wrapper: no authorize call, no
		// accounting emit, no handler delegation, and no per-method auth
		// registration. tonic's server trait has no default bodies, so this
		// impl must stay total; the refusal is what fills the slot.
		if method.IsMcp {
			grpcWrappers.WriteString(generateRustMcpRefusalMethod(targetService, method, info.Package, info.CrossPackageTypes))
			grpcWrappers.WriteString("\n")
			continue
		}
		// (spec.record).min_group_size is enforced in the generated Java service
		// wrapper only. A Rust host of a record query plane would serve the
		// size-only RPCs with no guard at all, so refuse to generate rather than
		// ship the disclosure silently.
		if method.MinGroupSize > 0 {
			return fmt.Errorf("%s.%s carries (spec.min_group_size) = %d, but the guard is generated for Java service wrappers only; a Rust host of this record query plane would serve it unguarded",
				targetService.Name, method.Name, method.MinGroupSize)
		}
		// (spec.record).mutability = MUTABILITY_APPEND_ONLY: a row mutation over
		// a write-once record has no guard to render in ANY language, so hosting
		// it is a generation failure here too. Same refusal the Java wrapper
		// makes (javaAppendOnlyUnenforceable), reached through the same
		// definition of "row-mutating".
		if t := rustAppendOnlyRecordOnMethod(method, san, info.Package); t != "" {
			return fmt.Errorf("%s.%s must not be generated: it carries %s, which declares (spec.record).mutability = MUTABILITY_APPEND_ONLY; an append-only record's rows are written once, so no row-mutating RPC may be hosted for the type",
				targetService.Name, method.Name, t)
		}
		// (spec.store_epoch) is stamped in the generated Java service wrapper
		// only. A Rust host would reach its handler with an unstamped epoch — a
		// row the residency window measures as infinitely old — so refuse to
		// generate rather than write the wrong clock silently.
		if f := rustStoreEpochField(method.FullInputType, san, info.Package); f != "" {
			return fmt.Errorf("%s.%s takes %s, whose field %q carries (spec.store_epoch), but the stamp is generated for Java service wrappers only; a Rust host of this write would leave the store epoch unstamped",
				targetService.Name, method.Name, method.FullInputType, f)
		}
		// (spec.store_principal) is enforced on the unary and PAGE egress seams.
		// A genuinely streaming method (WATCH) carrying such rows would return
		// them unfiltered — there is no per-item egress hook on that path — so
		// refuse to generate rather than under-enforce silently.
		if method.StreamingType() != "unary" {
			// Resolved is the computed COLLECTION view and is nil for a method
			// that declares no collection at all — a plain server-streaming RPC
			// such as oauth's Session.*. Only the WATCH item type comes from it
			// here; PAGE is excluded below because generateRustGRPCWrapper's PAGE
			// branch now generates the same egress filter the unary branch does
			// (over method.FullOutputType, which is already the first candidate).
			candidates := []string{method.FullOutputType}
			if method.Resolved != nil {
				candidates = append(candidates, method.Resolved.WatchItemType)
			}
			for _, t := range candidates {
				if t == "" {
					continue
				}
				if san.storePrincipalField(t, info.Package) != "" || len(san.principalListFields(t, info.Package)) > 0 {
					return fmt.Errorf("%s.%s returns %s, which carries (spec.store_principal), on a %s path; the principal read filter is generated for unary and page responses only",
						targetService.Name, method.Name, t, method.StreamingType())
				}
				// Same seam, same limit: (spec.store_namespace) is mandatory on
				// every record, so a Rust stream over record rows would return
				// them with no tenant boundary at all.
				if san.storeNamespaceField(t, info.Package) != "" || len(san.namespaceListFields(t, info.Package)) > 0 {
					return fmt.Errorf("%s.%s returns %s, which carries (spec.store_namespace), on a %s path; the namespace read filter is generated for unary and page responses only",
						targetService.Name, method.Name, t, method.StreamingType())
				}
			}
		}
		if method.IsWatchCollection() {
			_, changeType := rustWatchTypes(method, info.Package, info.CrossPackageTypes)
			fieldName := methodSnakeNameLower(method.Name) + "_watch_hub"
			fmt.Fprintf(&watchFields, "    %s: collection::WatchHub<%s>,\n", fieldName, changeType)
			fmt.Fprintf(&watchFieldsInit, "            %s: collection::WatchHub::new(%d, %d),\n", fieldName, method.Resolved.ReplayDepth, method.Resolved.IdleSecs)
		}
		wrapper, err := generateRustGRPCWrapper(targetService, method, info.Package, svc.ParentDir, info.CrossPackageTypes, svc.GrpcName, accountingMap, skippedTraceTypes, san)
		if err != nil {
			return err
		}
		// Replace hardcoded None caller with token extraction so handlers
		// receive the authenticated Caller from the bearer token.
		wrapper = strings.ReplaceAll(wrapper,
			"let caller: Option<servicemanager::auth::Caller> = None;",
			rustCallerExtraction)
		grpcWrappers.WriteString(wrapper)
		grpcWrappers.WriteString("\n")

		// Resolve effective auth_services: method-level overrides service-level.
		effectiveAuth := svc.AuthServices
		if len(method.AuthServices) > 0 {
			effectiveAuth = method.AuthServices
		}
		if len(effectiveAuth) > 0 {
			var quoted []string
			for _, pattern := range effectiveAuth {
				quoted = append(quoted, fmt.Sprintf("%q", pattern))
			}
			methodAuthLines = append(methodAuthLines, fmt.Sprintf(
				"    auth.set_method_auth(%q, &[%s]);",
				method.Name, strings.Join(quoted, ", ")))
		}
	}

	// Build register_method_auth function.
	var registerMethodAuth string
	if len(methodAuthLines) > 0 {
		registerMethodAuth = "/// Registers per-method auth_services patterns with the Authorizer.\n/// Called by ServiceManager after creating the Authorizer.\npub fn register_method_auth(auth: &grpcserver::authorizer::Authorizer) {\n" +
			strings.Join(methodAuthLines, "\n") + "\n}"
	}

	// Build trait implementation for ServiceWrapper (only target service)
	var traitImpls strings.Builder
	fmt.Fprintf(&traitImpls, "#[tonic::async_trait]impl %s for %s {\n", tonicName, wrapperName)
	for _, method := range targetService.Methods {
		if method.IsMcp {
			traitImpls.WriteString(generateRustMcpRefusalMethod(targetService, method, info.Package, info.CrossPackageTypes))
			traitImpls.WriteString("\n")
			continue
		}
		traitImpls.WriteString(generateRustTraitMethod(targetService, method, info.Package, info.CrossPackageTypes))
		traitImpls.WriteString("\n")
	}
	traitImpls.WriteString("}\n")

	tmpl, err := loadTemplate("servicewrapper.rs.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load template: %w", err)
	}

	// Build client fields init (only target service)
	var clientFieldsInit strings.Builder
	fmt.Fprintf(&clientFieldsInit, "%s_client: None,\n", svcSnake)

	// Compute the crate path to the types module (e.g., "services::baseball::baseball" for services/baseball)
	typesCratePath := strings.ReplaceAll(strings.ReplaceAll(svc.ParentDir, "-", "_"), string(filepath.Separator), "::") + "::" + svc.PackageName

	// `use am;` is unconditional: every wrapper carries the emitter slot and
	// set_emitter, the same way it carries set_trace_publisher, so the
	// ServiceManager can wire accounting without knowing which wrappers have
	// accounted RPCs. The enum imports stay conditional — they are only
	// referenced by an emitted accounting block.
	mcpDispatch, err := generateRustMcpDispatch(targetService, info.Package, info.CrossPackageTypes, san)
	if err != nil {
		return err
	}

	accountingImports := "use am;\n"
	if hasAccounting {
		accountingImports += `use crate::types::generated::accounting_method::accounting_method::AccountingMethod;
use crate::types::generated::services::services as services_pb;
`
	}

	data := struct {
		Header               string
		ServiceName          string
		TonicServiceName     string
		WrapperName          string
		ServiceLabel         string
		TypesCratePath       string
		ServiceImports       string
		CrossPackageImports  string
		ClientFields         string
		ClientFieldsInit     string
		ClientInitialization string
		TraitImpls           string
		HandlerTrait         string
		GRPCWrappers         string
		WatchFields          string
		WatchFieldsInit      string
		HasAccounting        bool
		AccountingImports    string
		RegisterMethodAuth   string
		McpDispatch          string
	}{
		Header:               header.GeneratedHeader(consts.LangRust, svc.ProtoFile),
		ServiceName:          serviceName,
		TonicServiceName:     tonicName,
		WrapperName:          wrapperName,
		ServiceLabel:         svc.Config.ServiceLabel(),
		TypesCratePath:       typesCratePath,
		ServiceImports:       serviceImports.String(),
		CrossPackageImports:  crossPkgImports.String(),
		ClientFields:         clientFields.String(),
		ClientFieldsInit:     clientFieldsInit.String(),
		ClientInitialization: clientInitialization.String(),
		TraitImpls:           traitImpls.String(),
		HandlerTrait:         handlerTrait.String(),
		GRPCWrappers:         grpcWrappers.String(),
		WatchFields:          watchFields.String(),
		WatchFieldsInit:      watchFieldsInit.String(),
		HasAccounting:        hasAccounting,
		AccountingImports:    accountingImports,
		RegisterMethodAuth:   registerMethodAuth,
		McpDispatch:          mcpDispatch,
	}

	t, err := template.New("servicewrapper").Parse(tmpl)
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	if err := t.Execute(file, data); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	return nil
}

// resolveRustType maps proto type names to their Rust equivalents.

// resolveRustTypeQualified maps proto type names to fully-qualified Rust paths
// when the type is from a different package than the service.
// Same-package types are returned as bare names (resolved via glob import).
// Cross-package types get crate::types:: prefix with the full module path.
// crossPkgTypes maps proto package names to their directory paths (from ProtoInfo.CrossPackageTypes).
func resolveRustTypeQualified(method *parser.MethodInfo, servicePkg string, crossPkgTypes map[string]string) (inputType, outputType string) {
	inputType = resolveOneRustTypeQualified(method.InputType, method.FullInputType, servicePkg, crossPkgTypes)
	outputType = resolveOneRustTypeQualified(method.OutputType, method.FullOutputType, servicePkg, crossPkgTypes)
	return
}

func resolveOneRustTypeQualified(shortName, fullType, servicePkg string, crossPkgTypes map[string]string) string {
	if fullType == "google.protobuf.Empty" {
		return "()"
	}
	// Normalize to the prost/heck struct name so references match generated
	// structs. Idempotent for prost-stable names; only changes digit-acronym
	// sequences (e.g. "MintP2STokenRequest" -> "MintP2sTokenRequest").
	shortName = toProstTypeName(shortName)
	// Extract package from full type
	parts := strings.Split(fullType, ".")
	if len(parts) <= 1 {
		return shortName
	}
	pkg := strings.Join(parts[:len(parts)-1], ".")
	if pkg == servicePkg {
		// Same package — bare name works via glob import
		return shortName
	}
	// Cross-package — use fully-qualified path
	// CrossPackageTypes maps proto package → parent directory
	// (e.g., "eventbus" → "common", "x509" → "sys", "tokenrequest" → "sys")
	// The Rust module path is: crate::types::{dir}::{pkg}::{TypeName}
	if dir, ok := crossPkgTypes[pkg]; ok {
		var modulePath string
		if dir == "" {
			// Package at root level
			modulePath = pkg
		} else {
			dirPath := strings.ReplaceAll(dir, string(filepath.Separator), "::")
			modulePath = dirPath + "::" + pkg
		}
		return fmt.Sprintf("crate::types::%s::%s", modulePath, shortName)
	}
	// Fallback: convert dots to double-colons
	rustPkg := strings.ReplaceAll(pkg, ".", "::")
	return fmt.Sprintf("crate::types::%s::%s", rustPkg, shortName)
}

func methodSnakeNameLower(name string) string {
	return prostSnakeName(name)
}

func simpleTypeName(fullType string) string {
	if i := strings.LastIndex(fullType, "."); i >= 0 {
		return fullType[i+1:]
	}
	return fullType
}

// rustPageItemType resolves the qualified Rust type of a PAGE method's item
// (the element of the response's single repeated items field).
func rustPageItemType(method *parser.MethodInfo, servicePkg string, crossPkgTypes map[string]string) string {
	return resolveOneRustTypeQualified(simpleTypeName(method.Resolved.PageItemType), method.Resolved.PageItemType, servicePkg, crossPkgTypes)
}

func rustWatchTypes(method *parser.MethodInfo, servicePkg string, crossPkgTypes map[string]string) (itemType, changeType string) {
	itemSimple := simpleTypeName(method.Resolved.WatchItemType)
	changeSimple := simpleTypeName(method.Resolved.WatchChangeType)
	return resolveOneRustTypeQualified(itemSimple, method.Resolved.WatchItemType, servicePkg, crossPkgTypes),
		resolveOneRustTypeQualified(changeSimple, method.Resolved.WatchChangeType, servicePkg, crossPkgTypes)
}

func rustOneofEnumPath(outputType, outputProtoType, oneofName string) string {
	module := prostSnakeName(outputProtoType)
	enumName := strutil.ToCamelCase(oneofName)
	if i := strings.LastIndex(outputType, "::"); i >= 0 {
		return outputType[:i+2] + module + "::" + enumName
	}
	return module + "::" + enumName
}

// ResponseStream is the boxed stream type used for server-side streaming responses.
// tonic::Streaming<T> is an inbound-only decoder and cannot be constructed on the server side.
const responseStreamFmt = "std::pin::Pin<Box<dyn tokio_stream::Stream<Item = Result<%s, tonic::Status>> + Send>>"
const requestStreamFmt = "std::pin::Pin<Box<dyn tokio_stream::Stream<Item = Result<%s, tonic::Status>> + Send>>"

// generateRustHandlerMethod generates a single method signature for the Handler trait.
// Unary methods return the response directly. Streaming methods use a boxed stream.
// Empty request types (mapped to "()" in Rust) are omitted from the signature.
func generateRustHandlerMethod(method *parser.MethodInfo, servicePkg string, crossPkgTypes map[string]string) string {
	methodSnake := prostSnakeName(method.Name)
	inputType, outputType := resolveRustTypeQualified(method, servicePkg, crossPkgTypes)
	handlerEmpty := method.FullInputType == "google.protobuf.Empty" || method.FullInputType == "trace.TraceRequest"
	responseStream := fmt.Sprintf(responseStreamFmt, outputType)

	switch {
	case !method.ClientStreaming && !method.ServerStreaming && method.IsPageCollection():
		// PAGE — a unary, client-driven window. The wrapper owns the page_token
		// codec and the page_size bound; the handler is a keyset producer: given
		// PageParams { limit, after } (plus req for the filter fields) it returns
		// one collection::Page (items + the cursor to resume after + an optional
		// total).
		itemType := rustPageItemType(method, servicePkg, crossPkgTypes)
		if handlerEmpty {
			return fmt.Sprintf("    async fn %s(&self, caller: Option<&servicemanager::auth::Caller>, page: collection::PageParams) -> Result<collection::Page<%s>, tonic::Status>;",
				methodSnake, itemType)
		}
		return fmt.Sprintf("    async fn %s(&self, caller: Option<&servicemanager::auth::Caller>, req: %s, page: collection::PageParams) -> Result<collection::Page<%s>, tonic::Status>;",
			methodSnake, inputType, itemType)
	case !method.ClientStreaming && !method.ServerStreaming:
		// Unary — takes Caller, returns Result
		if handlerEmpty {
			return fmt.Sprintf("    async fn %s(&self, caller: Option<&servicemanager::auth::Caller>) -> Result<%s, tonic::Status>;",
				methodSnake, outputType)
		}
		return fmt.Sprintf("    async fn %s(&self, caller: Option<&servicemanager::auth::Caller>, req: %s) -> Result<%s, tonic::Status>;",
			methodSnake, inputType, outputType)
	case !method.ClientStreaming && method.ServerStreaming && method.IsTier2SnapshotCollection():
		// Tier-2 SNAPSHOT collection — handler returns a materialized collection;
		// the wrapper chunks it into the server-stream.
		if handlerEmpty {
			return fmt.Sprintf("    async fn %s(&self, caller: Option<&servicemanager::auth::Caller>) -> Result<Vec<%s>, tonic::Status>;",
				methodSnake, outputType)
		}
		return fmt.Sprintf("    async fn %s(&self, caller: Option<&servicemanager::auth::Caller>, req: %s) -> Result<Vec<%s>, tonic::Status>;",
			methodSnake, inputType, outputType)
	case !method.ClientStreaming && method.ServerStreaming && method.IsWatchCollection():
		itemType, changeType := rustWatchTypes(method, servicePkg, crossPkgTypes)
		if handlerEmpty {
			return fmt.Sprintf("    async fn %s(&self, caller: Option<&servicemanager::auth::Caller>) -> Result<collection::WatchSource<%s, %s>, tonic::Status>;",
				methodSnake, itemType, changeType)
		}
		return fmt.Sprintf("    async fn %s(&self, caller: Option<&servicemanager::auth::Caller>, req: %s) -> Result<collection::WatchSource<%s, %s>, tonic::Status>;",
			methodSnake, inputType, itemType, changeType)
	case !method.ClientStreaming && method.ServerStreaming:
		// Server streaming — takes Caller, returns boxed stream
		if handlerEmpty {
			return fmt.Sprintf("    async fn %s(&self, caller: Option<&servicemanager::auth::Caller>) -> Result<tonic::Response<%s>, tonic::Status>;",
				methodSnake, responseStream)
		}
		return fmt.Sprintf("    async fn %s(&self, caller: Option<&servicemanager::auth::Caller>, req: %s) -> Result<tonic::Response<%s>, tonic::Status>;",
			methodSnake, inputType, responseStream)
	case method.ClientStreaming && !method.ServerStreaming:
		// Client streaming
		requestStream := fmt.Sprintf(requestStreamFmt, inputType)
		return fmt.Sprintf("    async fn %s(&self, caller: Option<&servicemanager::auth::Caller>, req: tonic::Request<%s>) -> Result<tonic::Response<%s>, tonic::Status>;",
			methodSnake, requestStream, outputType)
	case method.ClientStreaming && method.ServerStreaming:
		// Bidirectional streaming
		requestStream := fmt.Sprintf(requestStreamFmt, inputType)
		return fmt.Sprintf("    async fn %s(&self, caller: Option<&servicemanager::auth::Caller>, req: tonic::Request<%s>) -> Result<tonic::Response<%s>, tonic::Status>;",
			methodSnake, requestStream, responseStream)
	}
	return ""
}

// generateRustHandlerStubMethod generates a single method body for the
// {Service}HandlerStub. Returns Unimplemented for every call. Used by
// services hosting a vservice that hasn't been overridden yet, so the
// crate compiles via the default impl in the SM Handler trait.
func generateRustHandlerStubMethod(method *parser.MethodInfo, servicePkg, serviceName string, crossPkgTypes map[string]string) string {
	methodSnake := prostSnakeName(method.Name)
	inputType, outputType := resolveRustTypeQualified(method, servicePkg, crossPkgTypes)
	handlerEmpty := method.FullInputType == "google.protobuf.Empty" || method.FullInputType == "trace.TraceRequest"
	responseStream := fmt.Sprintf(responseStreamFmt, outputType)
	msg := fmt.Sprintf("%s.%s: handler not implemented", serviceName, method.Name)

	switch {
	case !method.ClientStreaming && !method.ServerStreaming && method.IsPageCollection():
		itemType := rustPageItemType(method, servicePkg, crossPkgTypes)
		if handlerEmpty {
			return fmt.Sprintf("    async fn %s(&self, _caller: Option<&servicemanager::auth::Caller>, _page: collection::PageParams) -> Result<collection::Page<%s>, tonic::Status> {\n        Err(tonic::Status::unimplemented(%q))\n    }",
				methodSnake, itemType, msg)
		}
		return fmt.Sprintf("    async fn %s(&self, _caller: Option<&servicemanager::auth::Caller>, _req: %s, _page: collection::PageParams) -> Result<collection::Page<%s>, tonic::Status> {\n        Err(tonic::Status::unimplemented(%q))\n    }",
			methodSnake, inputType, itemType, msg)
	case !method.ClientStreaming && !method.ServerStreaming:
		if handlerEmpty {
			return fmt.Sprintf("    async fn %s(&self, _caller: Option<&servicemanager::auth::Caller>) -> Result<%s, tonic::Status> {\n        Err(tonic::Status::unimplemented(%q))\n    }",
				methodSnake, outputType, msg)
		}
		return fmt.Sprintf("    async fn %s(&self, _caller: Option<&servicemanager::auth::Caller>, _req: %s) -> Result<%s, tonic::Status> {\n        Err(tonic::Status::unimplemented(%q))\n    }",
			methodSnake, inputType, outputType, msg)
	case !method.ClientStreaming && method.ServerStreaming && method.IsTier2SnapshotCollection():
		if handlerEmpty {
			return fmt.Sprintf("    async fn %s(&self, _caller: Option<&servicemanager::auth::Caller>) -> Result<Vec<%s>, tonic::Status> {\n        Err(tonic::Status::unimplemented(%q))\n    }",
				methodSnake, outputType, msg)
		}
		return fmt.Sprintf("    async fn %s(&self, _caller: Option<&servicemanager::auth::Caller>, _req: %s) -> Result<Vec<%s>, tonic::Status> {\n        Err(tonic::Status::unimplemented(%q))\n    }",
			methodSnake, inputType, outputType, msg)
	case !method.ClientStreaming && method.ServerStreaming && method.IsWatchCollection():
		itemType, changeType := rustWatchTypes(method, servicePkg, crossPkgTypes)
		if handlerEmpty {
			return fmt.Sprintf("    async fn %s(&self, _caller: Option<&servicemanager::auth::Caller>) -> Result<collection::WatchSource<%s, %s>, tonic::Status> {\n        Err(tonic::Status::unimplemented(%q))\n    }",
				methodSnake, itemType, changeType, msg)
		}
		return fmt.Sprintf("    async fn %s(&self, _caller: Option<&servicemanager::auth::Caller>, _req: %s) -> Result<collection::WatchSource<%s, %s>, tonic::Status> {\n        Err(tonic::Status::unimplemented(%q))\n    }",
			methodSnake, inputType, itemType, changeType, msg)
	case !method.ClientStreaming && method.ServerStreaming:
		if handlerEmpty {
			return fmt.Sprintf("    async fn %s(&self, _caller: Option<&servicemanager::auth::Caller>) -> Result<tonic::Response<%s>, tonic::Status> {\n        Err(tonic::Status::unimplemented(%q))\n    }",
				methodSnake, responseStream, msg)
		}
		return fmt.Sprintf("    async fn %s(&self, _caller: Option<&servicemanager::auth::Caller>, _req: %s) -> Result<tonic::Response<%s>, tonic::Status> {\n        Err(tonic::Status::unimplemented(%q))\n    }",
			methodSnake, inputType, responseStream, msg)
	case method.ClientStreaming && !method.ServerStreaming:
		requestStream := fmt.Sprintf(requestStreamFmt, inputType)
		return fmt.Sprintf("    async fn %s(&self, _caller: Option<&servicemanager::auth::Caller>, _req: tonic::Request<%s>) -> Result<tonic::Response<%s>, tonic::Status> {\n        Err(tonic::Status::unimplemented(%q))\n    }",
			methodSnake, requestStream, outputType, msg)
	case method.ClientStreaming && method.ServerStreaming:
		requestStream := fmt.Sprintf(requestStreamFmt, inputType)
		return fmt.Sprintf("    async fn %s(&self, _caller: Option<&servicemanager::auth::Caller>, _req: tonic::Request<%s>) -> Result<tonic::Response<%s>, tonic::Status> {\n        Err(tonic::Status::unimplemented(%q))\n    }",
			methodSnake, requestStream, responseStream, msg)
	}
	return ""
}

// generateRustProxyMethod generates a proxy method implementation for ServiceWrapper trait impl
func generateRustProxyMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, servicePkg string, crossPkgTypes map[string]string) string {
	svcSnake := tonicServiceModuleName(svc.Name)
	methodSnake := prostSnakeName(method.Name)
	inputType, outputType := resolveRustTypeQualified(method, servicePkg, crossPkgTypes)

	switch {
	case !method.ClientStreaming && !method.ServerStreaming:
		// Unary
		return fmt.Sprintf(`    async fn %s(
        &self,
        request: tonic::Request<%s>,
    ) -> Result<tonic::Response<%s>, tonic::Status> {
        tracing::info!(
            service = "%s",
            method = "%s",
            "Service method called"
        );

        let client = self.%s_client.as_ref()
            .ok_or_else(|| tonic::Status::internal("state client not initialized"))?;
        client.clone().%s(request).await
    }`, methodSnake, inputType, outputType, svc.Name, method.Name, svcSnake, methodSnake)

	case !method.ClientStreaming && method.ServerStreaming:
		// Server streaming — wrap client Streaming<T> in Pin<Box<dyn Stream>>
		return fmt.Sprintf(`    async fn %s(
        &self,
        request: tonic::Request<%s>,
    ) -> Result<tonic::Response<Self::%sStream>, tonic::Status> {
        tracing::info!(
            service = "%s",
            method = "%s",
            stream_type = "server-streaming",
            "Service method called"
        );

        let client = self.%s_client.as_ref()
            .ok_or_else(|| tonic::Status::internal("state client not initialized"))?;
        client.clone().%s(request).await
            .map(|resp| resp.map(|s| Box::pin(s) as Self::%sStream))
    }`, methodSnake, inputType, method.Name, svc.Name, method.Name, svcSnake, methodSnake, method.Name)

	case method.ClientStreaming && !method.ServerStreaming:
		// Client streaming - cannot directly proxy server-side stream to typed client
		return fmt.Sprintf(`    async fn %s(
        &self,
        _request: tonic::Request<tonic::Streaming<%s>>,
    ) -> Result<tonic::Response<%s>, tonic::Status> {
        tracing::info!(
            service = "%s",
            method = "%s",
            stream_type = "client-streaming",
            "Service method called"
        );

        // Client streaming proxy requires stream bridging - implement in service layer
        Err(tonic::Status::unimplemented("client streaming proxy not yet implemented"))
    }`, methodSnake, inputType, outputType, svc.Name, method.Name)

	case method.ClientStreaming && method.ServerStreaming:
		// Bidirectional streaming - cannot directly proxy server-side stream to typed client
		return fmt.Sprintf(`    async fn %s(
        &self,
        _request: tonic::Request<tonic::Streaming<%s>>,
    ) -> Result<tonic::Response<Self::%sStream>, tonic::Status> {
        tracing::info!(
            service = "%s",
            method = "%s",
            stream_type = "bidirectional-streaming",
            "Service method called"
        );

        // Bidirectional streaming proxy requires stream bridging - implement in service layer
        Err(tonic::Status::unimplemented("bidirectional streaming proxy not yet implemented"))
    // inputType, not method.InputType: every other branch here uses the
    // resolved path, and a bare simple name does not resolve for a
    // cross-package request type. Latent until the generated MCP session
    // carrier made a cross-package bidi rpc reachable (its request is
    // mcp.McpRequest).
    }`, methodSnake, inputType, method.Name, svc.Name, method.Name)
	}

	return ""
}

// generateRustTraitMethod generates a trait method signature for ServiceWrapper
// generateRustMcpRefusalMethod renders the tonic trait method for an
// RPC_PRINCIPAL_MCP rpc.
//
// The rpc is an MCP tool, not a gRPC call, so nothing else is generated for it:
// no handler-trait method, no gRPC wrapper, no REST route, no entitlement
// grant. tonic's generated server trait has no default method bodies, though,
// so the wrapper must still supply one to implement the trait at all — and the
// only body that keeps the suppression honest is an outright refusal. It takes
// no caller, reads no request, and reaches no handler. MCP-1 rejects a
// streaming MCP rpc at parse time, so the unary shape is the only one reachable
// here.
func generateRustMcpRefusalMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, servicePkg string, crossPkgTypes map[string]string) string {
	methodSnake := prostSnakeName(method.Name)
	inputType, outputType := resolveRustTypeQualified(method, servicePkg, crossPkgTypes)
	return fmt.Sprintf(`    async fn %s(
        &self,
        _request: tonic::Request<%s>,
    ) -> Result<tonic::Response<%s>, tonic::Status> {
        Err(tonic::Status::unimplemented("%s.%s is an MCP tool, not a gRPC method"))
    }`, methodSnake, inputType, outputType, svc.Name, method.Name)
}

func generateRustTraitMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, servicePkg string, crossPkgTypes map[string]string) string {
	_, outputType := resolveRustTypeQualified(method, servicePkg, crossPkgTypes)
	switch {
	case !method.ClientStreaming && !method.ServerStreaming:
		// Unary
		return generateRustProxyMethod(svc, method, servicePkg, crossPkgTypes)

	case !method.ClientStreaming && method.ServerStreaming:
		// Server streaming - need to define associated type (boxed stream for outbound)
		streamType := fmt.Sprintf(responseStreamFmt, outputType)
		return fmt.Sprintf(`    type %sStream = %s;

%s`, method.Name, streamType, generateRustProxyMethod(svc, method, servicePkg, crossPkgTypes))

	case method.ClientStreaming && !method.ServerStreaming:
		// Client streaming
		return generateRustProxyMethod(svc, method, servicePkg, crossPkgTypes)

	case method.ClientStreaming && method.ServerStreaming:
		// Bidirectional streaming - need to define associated type (boxed stream for outbound)
		streamType := fmt.Sprintf(responseStreamFmt, outputType)
		return fmt.Sprintf(`    type %sStream = %s;

%s`, method.Name, streamType, generateRustProxyMethod(svc, method, servicePkg, crossPkgTypes))
	}

	return ""
}

// rustProstEnumVariant converts a proto UPPER_SNAKE_CASE enum value name to the
// PascalCase variant name that prost generates.
// e.g. "SENTINEL_SERVICE_RPCS_GET_CLUSTER_INFO" → "SentinelServiceRpcsGetClusterInfo"
func rustProstEnumVariant(upperSnake string) string {
	return strutil.UpperSnakeCaseToPascalCase(upperSnake)
}

// buildRustAccountingBlock generates the Rust accounting emit code for a single RPC method.
// Returns empty string if the service has no matching AccountingMethod entry.
// accountingMap maps grpcName → AccountingInfo (EnumName + valid RPCs).
func buildRustAccountingBlock(method *parser.MethodInfo, grpcName, svcProtoName string, accountingMap map[string]service.AccountingInfo) string {
	methodLiteral := rustAccountingMethodLiteral(method, grpcName, accountingMap)
	if methodLiteral == "" {
		return ""
	}

	return fmt.Sprintf(`
        // Emit accounting event
        {
            let emitter_guard = self.emitter.read().await;
            if let Some(ref emitter) = *emitter_guard {
                let method = %s;
                match &result {
                    Ok(_) => {
                        emitter.rpc_completed(method, duration_ms, __tacplus_caller_identity.clone(), &request_id).await;
                    }
                    Err(status) => {
                        let err_ctx = am::error_context_from_status(status);
                        emitter.rpc_error(method, status.code() as i32, status.message(), duration_ms, Some(err_ctx), __tacplus_caller_identity.clone(), &request_id).await;
                    }
                }
            }
        }`, methodLiteral)
}

func rustAccountingMethodLiteral(method *parser.MethodInfo, grpcName string, accountingMap map[string]service.AccountingInfo) string {
	info, ok := accountingMap[grpcName]
	if !ok || !info.RPCs[method.Name] {
		return ""
	}

	// Construct prost enum names using the AccountingMethod's EnumName:
	// Proto enum type: SentinelRPCs → prost: SentinelRpCs
	// Use ToTonicCamelCase to match prost's heck-based naming.
	rustEnumType := strutil.ToTonicCamelCase(info.EnumName) + "RpCs"

	// Proto enum value: SENTINEL_RPCS_GET_CLUSTER_INFO → prost: SentinelRpcsGetClusterInfo
	upperSnakeValue := strings.ToUpper(strutil.ToSnakeCase(info.EnumName)) + "_RPCS_" + strings.ToUpper(strutil.ToSnakeCase(method.Name))
	rustEnumVariant := rustProstEnumVariant(upperSnakeValue)

	// Field name on AccountingMethod is the grpcName (already snake_case)
	fieldName := grpcName

	return fmt.Sprintf(`
AccountingMethod {
            %s: services_pb::%s::%s as i32,
            ..Default::default()
        }`, fieldName, rustEnumType, rustEnumVariant)
}

// rustCallerIdentityVar is the local the generated wrapper binds the
// AUTHORIZER-VERIFIED caller identity to. Every accounting emit in the wrapper
// reads it. It is __-prefixed so a wrapper with no accounting does not warn on
// an unused binding, and so it cannot collide with a proto field name.
//
// Before this existed the Rust wrapper handed every emit a fresh empty map, so
// the only thing tying an accounting record to a caller was the caller-written
// x-request-id header. Java fixed this first (F10); this is the Rust port.
const rustCallerIdentityVar = "__tacplus_caller_identity"

// rustCallerExtraction is the Rust code that extracts a Caller from the
// authorization header. Replaces the placeholder `let caller = None` so that
// handlers receive the authenticated identity. Requires `request_id` to
// already be bound and `request` (a tonic::Request) to still be in scope.
const rustCallerExtraction = `let caller: Option<servicemanager::auth::Caller> = request.metadata()
            .get("authorization")
            .and_then(|v| v.to_str().ok())
            .and_then(|v| v.strip_prefix("Bearer "))
            .and_then(|t| token::Token::parse(t).ok())
            .map(|t| servicemanager::auth::Caller::new(t, request_id.clone()));`

// rustAuthLine returns the Rust authorization call for a gRPC method.
// auth_disabled → no auth check; auth_any_user → fixed namespace at-or-below; default → full authorize.
// When target_namespace_field is set on a non-client-streaming method, also emit a
// level-or-below check using the named request field.
func rustAuthLine(svc *parser.ServiceInfo, method *parser.MethodInfo, fullMethod string) string {
	var base string
	// c2d (client-to-daemon) services are local IPC over a unix-domain
	// socket whose filesystem permissions (0600, owner-only) are the access
	// boundary. They carry no token-authority-issued token, so the inalienable token-auth
	// gate must not apply — a c2d wrapper has no authorizer and would otherwise
	// fail closed ("authorizer not configured — auth is mandatory"), breaking the
	// daemon's own control plane (e.g. a client daemon's DaemonControl/HookReceiver).
	if method.AuthDisabled || svc.AuthDisabled || svc.IsC2D {
		// Nothing verified a caller here, so accounting records carry no identity —
		// an empty map, never something the caller asserted in a header.
		base = "// c2d/auth_disabled: no authorization check\n        let " + rustCallerIdentityVar + ": std::collections::HashMap<String, String> = std::collections::HashMap::new();"
	} else if method.AuthAnyUser {
		targetNamespace := strings.TrimSpace(method.TargetNamespaceField)
		if targetNamespace == "" {
			targetNamespace = "/"
		}
		base = fmt.Sprintf("let %s = self.enforce_at_or_below(&request, %q).await?;", rustCallerIdentityVar, targetNamespace)
	} else {
		base = fmt.Sprintf("let %s = self.authorize_method(\"%s\", &request).await?;", rustCallerIdentityVar, fullMethod)
	}

	// target_namespace_field: emit level-or-below enforcement using direct field access.
	// Only valid for non-client-streaming methods (request.get_ref() yields the message).
	if method.TargetNamespaceField != "" && !method.ClientStreaming && !method.AuthAnyUser {
		fieldSnake := strutil.ToSnakeCase(method.TargetNamespaceField)
		base += fmt.Sprintf("\n        self.enforce_at_or_below(&request, &request.get_ref().%s).await?;", fieldSnake)
	}
	return base
}

func rustServerRequestValidationBlock(varName, fullType string) string {
	if !rustW2SCanValidate(fullType) {
		return ""
	}
	return fmt.Sprintf(`
        %s.validate()
            .map_err(|e| tonic::Status::invalid_argument(format!("validation failed: {}", e)))?;
`, varName)
}

func rustServerResponseValidationBlock(varName, fullType string) string {
	if !rustW2SCanValidate(fullType) {
		return ""
	}
	return fmt.Sprintf(`
        %s.validate()
            .map_err(|e| tonic::Status::internal(format!("response validation failed: {}", e)))?;
`, varName)
}

func rustValidatedRequestStreamBlock(inputType, fullInputType, targetNamespaceField string) string {
	requestStream := fmt.Sprintf(requestStreamFmt, inputType)
	fieldSnake := ""
	if targetNamespaceField != "" {
		fieldSnake = strutil.ToSnakeCase(targetNamespaceField)
	}
	authPreamble := ""
	if fieldSnake != "" {
		authPreamble = `
        let authorization = metadata
            .get("authorization")
            .and_then(|v| v.to_str().ok())
            .map(|v| v.to_string());
        let wrapper = self.clone();`
	}
	if !rustW2SCanValidate(fullInputType) {
		if fieldSnake != "" {
			return fmt.Sprintf(`
        let (metadata, extensions, stream) = request.into_parts();%s
        let stream = stream.then(move |item| {
            let authorization = authorization.clone();
            let wrapper = wrapper.clone();
            async move {
                let msg = item?;
                wrapper.enforce_at_or_below_authorization(authorization.as_deref(), &msg.%s).await?;
                Ok(msg)
            }
        });
        let request = tonic::Request::from_parts(metadata, extensions, Box::pin(stream) as %s);
`, authPreamble, fieldSnake, requestStream)
		}
		return fmt.Sprintf(`
        let (metadata, extensions, stream) = request.into_parts();
        let request = tonic::Request::from_parts(metadata, extensions, Box::pin(stream) as %s);
`, requestStream)
	}
	if fieldSnake != "" {
		return fmt.Sprintf(`
        let (metadata, extensions, stream) = request.into_parts();%s
        let stream = stream.then(move |item| {
            let authorization = authorization.clone();
            let wrapper = wrapper.clone();
            async move {
                let msg = item?;
                wrapper.enforce_at_or_below_authorization(authorization.as_deref(), &msg.%s).await?;
                msg.validate()
                    .map_err(|e| tonic::Status::invalid_argument(format!("validation failed: {}", e)))?;
                Ok(msg)
            }
        });
        let request = tonic::Request::from_parts(metadata, extensions, Box::pin(stream) as %s);
`, authPreamble, fieldSnake, requestStream)
	}
	return fmt.Sprintf(`
        let (metadata, extensions, stream) = request.into_parts();
        let stream = stream.map(|item| {
            item.and_then(|msg| {
                msg.validate()
                    .map_err(|e| tonic::Status::invalid_argument(format!("validation failed: {}", e)))?;
                Ok(msg)
            })
        });
        let request = tonic::Request::from_parts(metadata, extensions, Box::pin(stream) as %s);
`, requestStream)
}

func rustValidatedResponseStreamReturn(methodName, fullOutputType string) string {
	if !rustW2SCanValidate(fullOutputType) {
		return `        Ok(response)
`
	}
	return fmt.Sprintf(`        Ok(response.map(|stream| {
            let stream = stream.map(|item| {
                item.and_then(|msg| {
                    msg.validate()
                        .map_err(|e| tonic::Status::internal(format!("response validation failed: {}", e)))?;
                    Ok(msg)
                })
            });
            Box::pin(stream) as Self::%sStream
        }))
`, methodName)
}

// rustTypeHasTraceFields returns true if a proto type has the injected trace_id
// field. True for user-defined types and trace.TraceRequest. Returns false for
// google.protobuf.*, sys/* types, and google.protobuf.Empty.
func rustTypeHasTraceFields(fullType, servicePackage, parentDir string, crossPkgTypes map[string]string, skippedTraceTypes proto.SkippedTraceTypes) bool {
	if fullType == "trace.TraceRequest" {
		return true
	}
	if fullType == "" || fullType == "google.protobuf.Empty" {
		return false
	}
	// Types that were skipped during trace injection (they define their own
	// trace_id or noop at non-standard field numbers) don't have the injected
	// trace_id/noop pair — don't emit trace handling code for them.
	if skippedTraceTypes[fullType] {
		return false
	}
	parts := strings.Split(fullType, ".")
	if len(parts) <= 1 {
		return !strings.HasPrefix(parentDir, "sys")
	}
	pkg := strings.Join(parts[:len(parts)-1], ".")
	if pkg == servicePackage {
		return !strings.HasPrefix(parentDir, "sys")
	}
	if pkg == "google.protobuf" {
		return false
	}
	if dir, ok := crossPkgTypes[pkg]; ok {
		return !strings.HasPrefix(dir, "sys")
	}
	return !strings.HasPrefix(parentDir, "sys")
}

// generateRustGRPCWrapper generates a gRPC method wrapper with validation for ValidationWrapper.
// grpcName is the service's grpc_config name (e.g. "sentinel") used to construct the AccountingMethod.
func generateRustGRPCWrapper(svc *parser.ServiceInfo, method *parser.MethodInfo, servicePackage, parentDir string, crossPkgTypes map[string]string, grpcName string, accountingMap map[string]service.AccountingInfo, skippedTraceTypes proto.SkippedTraceTypes, san *rustEgressSanitizer) (string, error) {
	methodSnake := prostSnakeName(method.Name)
	fullMethod := fmt.Sprintf("/%s/%s.%s/%s", parentDir, servicePackage, svc.Name, method.Name)
	inputType, outputType := resolveRustTypeQualified(method, servicePackage, crossPkgTypes)
	authLine := rustAuthLine(svc, method, fullMethod)

	emptyInput := method.FullInputType == "google.protobuf.Empty"
	handlerEmpty := emptyInput || method.FullInputType == "trace.TraceRequest"

	// (spec.owner_default) rides the REQUEST-VALIDATION seam, immediately after
	// the (spec.principal) stamp, because that is the only point where both
	// halves of the comparison exist: the authenticated caller and the request's
	// bound principal. The snippet is empty unless a type this method touches
	// contradicts its access. A method that would be refused but has no such
	// seam — streaming, PAGE, or an empty request — cannot make the comparison,
	// so refuse to generate rather than under-enforce silently, exactly as the
	// (spec.store_principal) read filter does for the same shapes.
	ownerDefaultGuard, err := rustOwnerDefaultGuard(method, san, servicePackage)
	if err != nil {
		return "", err
	}
	if ownerDefaultGuard != "" && (handlerEmpty || method.StreamingType() != "unary" || method.IsPageCollection()) {
		return "", fmt.Errorf("%s touches a type declaring (spec.owner_default) that contradicts its access, but its %s request shape has no validation seam at which the caller can be compared to the row's principal", method.Name, method.StreamingType())
	}
	hasAccounting := false
	if amInfo, ok := accountingMap[grpcName]; ok {
		hasAccounting = amInfo.RPCs[method.Name]
	}

	// Trace v2: event-based tracing with Herald-issued trace_id.
	// A non-empty trace_id publishes a TraceEvent at this hop. If noop=true,
	// the handler is skipped and a default response is returned. If noop=false
	// (default), auth and handler run normally — trace is just observability.
	inputHasTrace := rustTypeHasTraceFields(method.FullInputType, servicePackage, parentDir, crossPkgTypes, skippedTraceTypes)

	// Build trace-aware blocks for unary methods
	authBlock := authLine
	reqMut := ""
	traceAppend := ""
	respMut := ""
	traceCopy := ""
	traceErrorCopy := ""
	traceErrorOnResult := ""

	if inputHasTrace && !emptyInput {
		// Trace v2 contract (TCG-001): noop skips the handler but NOT auth.
		// Auth runs unconditionally before the trace/noop early return,
		// matching the Go generator's ordering.
		authBlock = fmt.Sprintf(`let is_tracing = !request.get_ref().trace_id.is_empty();
        let is_noop = is_tracing && request.get_ref().noop;
        %s`, authLine)

		// Build typed rpc field for the AccountingMethod if this service has an entry.
		var traceRpcRequest, traceRpcResponse string
		if info, ok := accountingMap[grpcName]; ok && info.RPCs[method.Name] {
			rustEnumType := strutil.ToTonicCamelCase(info.EnumName) + "RpCs"
			upperSnakeValue := strings.ToUpper(strutil.ToSnakeCase(info.EnumName)) + "_RPCS_" + strings.ToUpper(strutil.ToSnakeCase(method.Name))
			rustEnumVariant := rustProstEnumVariant(upperSnakeValue)
			rpcBlock := fmt.Sprintf("rpc: Some(AccountingMethod { %s: services_pb::%s::%s as i32, ..Default::default() }),", grpcName, rustEnumType, rustEnumVariant)
			traceRpcRequest = rpcBlock + "\n                kind: crate::types::sys::trace::TraceEventKind::InternalRequest.into(),"
			traceRpcResponse = rpcBlock + "\n                kind: crate::types::sys::trace::TraceEventKind::InternalResponse.into(),"
		} else {
			traceRpcRequest = "kind: crate::types::sys::trace::TraceEventKind::InternalRequest.into(),"
			traceRpcResponse = "kind: crate::types::sys::trace::TraceEventKind::InternalResponse.into(),"
		}

		// Publish request trace event, save trace_id for response trace.
		traceAppend = fmt.Sprintf(`
        let trace_id_saved = req.trace_id.clone();
        if is_tracing {
            let round = self.trace_round().await;
            self.publish_trace(crate::types::sys::trace::TraceEvent {
                trace_id: req.trace_id.clone(),
                round: Some(round),
                %s
                ..Default::default()
            }).await;
            if is_noop {
                return Ok(tonic::Response::new(<%s>::default()));
            }
        }`, traceRpcRequest, outputType)

		// Publish response trace after handler returns.
		traceCopy = fmt.Sprintf(`
        if is_tracing {
            let round = self.trace_round().await;
            self.publish_trace(crate::types::sys::trace::TraceEvent {
                trace_id: trace_id_saved.clone(),
                round: Some(round),
                %s
                ..Default::default()
            }).await;
        }`, traceRpcResponse)

		traceErrorCopy = fmt.Sprintf(`
            if is_tracing {
                let round = self.trace_round().await;
                self.publish_trace(crate::types::sys::trace::TraceEvent {
                    trace_id: trace_id_saved.clone(),
                    round: Some(round),
                    error: err.to_string(),
                    %s
                    ..Default::default()
                }).await;
            }`, traceRpcResponse)
		traceErrorOnResult = fmt.Sprintf(`
        if let Err(ref err) = result {%s
        }`, traceErrorCopy)
	}

	if method.ClientStreaming || method.ServerStreaming {
		streamType := fmt.Sprintf(responseStreamFmt, outputType)

		// Build streaming accounting block
		var streamAccountingBlock string
		if hasAccounting {
			streamAccountingBlock = buildRustAccountingBlock(method, grpcName, svc.Name, accountingMap)
		}
		serverStreamInputValidation := rustServerRequestValidationBlock("request.get_ref()", method.FullInputType)
		streamTargetNamespaceField := ""
		if method.ClientStreaming && method.TargetNamespaceField != "" &&
			!method.AuthAnyUser &&
			!(method.AuthDisabled || svc.AuthDisabled || svc.IsC2D) {
			streamTargetNamespaceField = method.TargetNamespaceField
		}
		requestStreamBlock := rustValidatedRequestStreamBlock(inputType, method.FullInputType, streamTargetNamespaceField)
		streamResponseReturn := rustValidatedResponseStreamReturn(method.Name, method.FullOutputType)
		unaryStreamResponseValidation := rustServerResponseValidationBlock("response.get_ref()", method.FullOutputType)

		// Tier-2 SNAPSHOT collection: server-streaming on the wire, but the handler
		// returns a materialized Vec; chunk it into the stream (tonic/tokio apply
		// backpressure). Output validation, if any, runs per item.
		if !method.ClientStreaming && method.ServerStreaming && method.IsTier2SnapshotCollection() {
			tier2HandlerCall := fmt.Sprintf("self.handler.%s(caller.as_ref(), request.into_inner()).await", methodSnake)
			if handlerEmpty {
				tier2HandlerCall = fmt.Sprintf("self.handler.%s(caller.as_ref()).await", methodSnake)
			}
			streamExpr := "tokio_stream::iter(items.into_iter().map(Ok::<_, tonic::Status>))"
			if rustW2SCanValidate(method.FullOutputType) {
				streamExpr = `tokio_stream::iter(items.into_iter().map(|msg| {
                msg.validate().map_err(|e| tonic::Status::internal(format!("response validation failed: {}", e)))?;
                Ok::<_, tonic::Status>(msg)
            }))`
			}
			// Egress data entitlement: sanitize each materialized item after the
			// handler returns, before the stream (and its validation) sees it.
			tier2Pre := serverStreamInputValidation
			if t, ok := san.sanitizableType(method.FullOutputType, servicePackage, crossPkgTypes); ok {
				tier2Pre += rustEgressSanitizePreamble([]rustSanTarget{t})
				streamExpr = "{\n            let items: Vec<_> = items.into_iter().map(|m| " + t.sanitizeExpr("&m") + ").collect();\n            " + streamExpr + "\n            }"
			}
			serverStreamInputValidation := tier2Pre
			if hasAccounting {
				return fmt.Sprintf(`    type %sStream = %s;

    async fn %s(
        &self,
        request: tonic::Request<%s>,
    ) -> Result<tonic::Response<Self::%sStream>, tonic::Status> {
        %s
        let request_id = servicemanager::auth::extract_request_id(&request);
        let caller: Option<servicemanager::auth::Caller> = None;
        tracing::debug!(method = "%s", request_id = %%request_id, "rpc");
        let start = std::time::Instant::now();
        let result = async {
%s
            let items = %s?;
            let stream = %s;
            Ok(tonic::Response::new(Box::pin(stream) as Self::%sStream))
        }.await;
        let duration_ms = start.elapsed().as_millis() as i64;
%s
        result
    }`, method.Name, streamType, methodSnake, inputType, method.Name, authLine, fullMethod, serverStreamInputValidation, tier2HandlerCall, streamExpr, method.Name, streamAccountingBlock), nil
			}
			return fmt.Sprintf(`    type %sStream = %s;

    async fn %s(
        &self,
        request: tonic::Request<%s>,
    ) -> Result<tonic::Response<Self::%sStream>, tonic::Status> {
        %s
        let request_id = servicemanager::auth::extract_request_id(&request);
        let caller: Option<servicemanager::auth::Caller> = None;
        tracing::debug!(method = "%s", request_id = %%request_id, "rpc");
%s
        let items = %s?;
        let stream = %s;
        Ok(tonic::Response::new(Box::pin(stream) as Self::%sStream))
    }`, method.Name, streamType, methodSnake, inputType, method.Name, authLine, fullMethod, serverStreamInputValidation, tier2HandlerCall, streamExpr, method.Name), nil
		}

		if !method.ClientStreaming && method.ServerStreaming && method.IsWatchCollection() {
			// WATCH collection: handler returns WatchSource, wrapper emits the
			// {Method}WatchEvent envelope and drives the shared replay hub.
			itemType, changeType := rustWatchTypes(method, servicePackage, crossPkgTypes)
			oneofPath := rustOneofEnumPath(outputType, method.OutputType, method.Resolved.WatchOneOf)
			fieldName := methodSnakeNameLower(method.Name) + "_watch_hub"
			handlerCall := fmt.Sprintf("self.handler.%s(caller.as_ref(), req_msg).await", methodSnake)
			if handlerEmpty {
				handlerCall = fmt.Sprintf("self.handler.%s(caller.as_ref()).await", methodSnake)
			}
			validateEnvelope := ""
			if rustW2SCanValidate(method.FullOutputType) {
				validateEnvelope = `
                if let Err(e) = __event.validate() {
                    let _ = tx.send(Err(tonic::Status::internal(format!("response validation failed: {}", e)))).await;
                    return;
                }`
			}
			sourceBinding := fmt.Sprintf("let source: collection::WatchSource<%s, %s> = %s?;", itemType, changeType, handlerCall)
			// Egress data entitlement: sanitize each snapshot item AND each delta
			// before send. A sanitizable item/change is sanitized whole; an injected
			// envelope (e.g. <T>WatchChange) has its record-bearing fields sanitized
			// (the hub stream yields owned values, so in-place mutation is safe).
			var watchTargets []rustSanTarget
			var itemSan, changeSan string
			if t, ok := san.sanitizableType(method.Resolved.WatchItemType, servicePackage, crossPkgTypes); ok {
				watchTargets = append(watchTargets, t)
				itemSan = "\n                            let item = " + t.sanitizeExpr("&item") + ";"
			} else if fs := san.envelopeSanFields(method.Resolved.WatchItemType, servicePackage, crossPkgTypes); len(fs) > 0 {
				for _, f := range fs {
					watchTargets = append(watchTargets, f.Target)
				}
				itemSan = "\n                            let mut item = item;\n" + rustEgressEnvelopeStmts(fs, "item", "                            ")
			}
			if t, ok := san.sanitizableType(method.Resolved.WatchChangeType, servicePackage, crossPkgTypes); ok {
				watchTargets = append(watchTargets, t)
				changeSan = "\n                        let change = " + t.sanitizeExpr("&change") + ";"
			} else if fs := san.envelopeSanFields(method.Resolved.WatchChangeType, servicePackage, crossPkgTypes); len(fs) > 0 {
				for _, f := range fs {
					watchTargets = append(watchTargets, f.Target)
				}
				changeSan = "\n                        let mut change = change;\n" + rustEgressEnvelopeStmts(fs, "change", "                        ")
			}
			watchPre := rustEgressSanitizePreamble(watchTargets)
			accountingSetup := ""
			sendClosed := "return;"
			sendErr := `let _ = tx.send(Err(err)).await;
                            return;`
			completed := ""
			if hasAccounting {
				methodLiteral := rustAccountingMethodLiteral(method, grpcName, accountingMap)
				accountingSetup = fmt.Sprintf(`
        let __tacplus_accounting_start = std::time::Instant::now();
        let __tacplus_accounting_method = %s;
        let __tacplus_emitter = self.emitter.clone();
        let __tacplus_request_id = request_id.clone();
        let __tacplus_accounting_identity = __tacplus_caller_identity.clone();
        macro_rules! __tacplus_watch_rpc_error {
            ($status:expr) => {{
                let duration_ms = __tacplus_accounting_start.elapsed().as_millis() as i64;
                let emitter_guard = __tacplus_emitter.read().await;
                if let Some(ref emitter) = *emitter_guard {
                    let err_ctx = am::error_context_from_status(&$status);
                    emitter.rpc_error(
                        __tacplus_accounting_method.clone(),
                        $status.code() as i32,
                        $status.message(),
                        duration_ms,
                        Some(err_ctx),
                        __tacplus_accounting_identity.clone(),
                        &__tacplus_request_id,
                    ).await;
                }
            }};
        }
        macro_rules! __tacplus_watch_rpc_completed {
            () => {{
                let duration_ms = __tacplus_accounting_start.elapsed().as_millis() as i64;
                let emitter_guard = __tacplus_emitter.read().await;
                if let Some(ref emitter) = *emitter_guard {
                    emitter.rpc_completed(
                        __tacplus_accounting_method.clone(),
                        duration_ms,
                        __tacplus_accounting_identity.clone(),
                        &__tacplus_request_id,
                    ).await;
                }
            }};
        }
`, methodLiteral)
				sourceBinding = fmt.Sprintf(`let source: collection::WatchSource<%s, %s> = match %s {
            Ok(source) => source,
            Err(status) => {
                __tacplus_watch_rpc_error!(status);
                return Err(status);
            }
        };`, itemType, changeType, handlerCall)
				sendClosed = `let status = tonic::Status::cancelled("client closed Watch stream");
                                __tacplus_watch_rpc_error!(status);
                                return;`
				sendErr = `__tacplus_watch_rpc_error!(err);
                            let _ = tx.send(Err(err)).await;
                            return;`
				completed = `
            __tacplus_watch_rpc_completed!();`
			}
			return fmt.Sprintf(`    type %sStream = %s;

    async fn %s(
        &self,
        request: tonic::Request<%s>,
    ) -> Result<tonic::Response<Self::%sStream>, tonic::Status> {
        %s
        let request_id = servicemanager::auth::extract_request_id(&request);
        let caller: Option<servicemanager::auth::Caller> = None;
        tracing::debug!(method = "%s", request_id = %%request_id, "rpc");
%s
        let req_msg = request.into_inner();
        let from_revision = req_msg.from_revision;
%s
        let resume_revision = if from_revision == 0 { source.revision } else { from_revision };
        let mut snapshot = source.snapshot;
        let mut changes = self.%s.watch(
            source.scope.clone(),
            resume_revision,
            source.changes,
            |change: &%s| change.revision,
        ).await;
        let source_revision = source.revision;
        let (tx, rx) = tokio::sync::mpsc::channel(32);
        tokio::spawn(async move {
            if from_revision == 0 {
                while let Some(item) = snapshot.next().await {
                    match item {
                        Ok(item) => {%s
                            let __event = %s { %s: Some(%s::SnapshotItem(item)), ..Default::default() };%s
                            if tx.send(Ok(__event)).await.is_err() { %s }
                        }
                        Err(err) => {
                            %s
                        }
                    }
                }
                let __event = %s { %s: Some(%s::SnapshotDone(source_revision)), ..Default::default() };%s
                if tx.send(Ok(__event)).await.is_err() { %s }
            }
            while let Some(change) = changes.next().await {
                match change {
                    Ok(change) => {%s
                        let __event = %s { %s: Some(%s::Delta(change)), ..Default::default() };%s
                        if tx.send(Ok(__event)).await.is_err() { %s }
                    }
                    Err(err) => {
                        %s
                    }
                }
            }
%s
        });
        let stream = tokio_stream::wrappers::ReceiverStream::new(rx);
        Ok(tonic::Response::new(Box::pin(stream) as Self::%sStream))
    }`, method.Name, streamType, methodSnake, inputType, method.Name, authLine, fullMethod, serverStreamInputValidation+watchPre+accountingSetup,
				sourceBinding, fieldName, changeType,
				itemSan, outputType, method.Resolved.WatchOneOf, oneofPath, validateEnvelope, sendClosed,
				sendErr,
				outputType, method.Resolved.WatchOneOf, oneofPath, validateEnvelope, sendClosed,
				changeSan, outputType, method.Resolved.WatchOneOf, oneofPath, validateEnvelope, sendClosed,
				sendErr, completed,
				method.Name), nil
		}

		// Streaming methods - extract caller, delegate to handler
		if !method.ClientStreaming && method.ServerStreaming {
			handlerCall := fmt.Sprintf("self.handler.%s(caller.as_ref(), request.into_inner()).await", methodSnake)
			if handlerEmpty {
				handlerCall = fmt.Sprintf("self.handler.%s(caller.as_ref()).await", methodSnake)
			}
			// Egress data entitlement: the handler returns the stream itself (this is
			// also the Tier-3 SNAPSHOT cursor route in Rust) — wrap it so every
			// lazily-pulled item is sanitized before validation/send.
			var lazySan string
			if t, ok := san.sanitizableType(method.FullOutputType, servicePackage, crossPkgTypes); ok {
				serverStreamInputValidation += rustEgressSanitizePreamble([]rustSanTarget{t})
				lazySan = fmt.Sprintf(`        let response = response.map(|stream| {
            let stream = stream.map(move |item| item.map(|msg| %s));
            Box::pin(stream) as Self::%sStream
        });
`, t.sanitizeExpr("&msg"), method.Name)
			} else if fs := san.envelopeSanFields(method.FullOutputType, servicePackage, crossPkgTypes); len(fs) > 0 {
				var targets []rustSanTarget
				for _, f := range fs {
					targets = append(targets, f.Target)
				}
				serverStreamInputValidation += rustEgressSanitizePreamble(targets)
				lazySan = fmt.Sprintf(`        let response = response.map(|stream| {
            let stream = stream.map(move |item| item.map(|msg| {
                let mut msg = msg;
%s                msg
            }));
            Box::pin(stream) as Self::%sStream
        });
`, rustEgressEnvelopeStmts(fs, "msg", "                "), method.Name)
			}
			streamResponseReturn = lazySan + streamResponseReturn
			if hasAccounting {
				return fmt.Sprintf(`    type %sStream = %s;

    async fn %s(
        &self,
        request: tonic::Request<%s>,
    ) -> Result<tonic::Response<Self::%sStream>, tonic::Status> {
        %s
        let request_id = servicemanager::auth::extract_request_id(&request);
	        let caller: Option<servicemanager::auth::Caller> = None;
	        tracing::debug!(method = "%s", request_id = %%request_id, "rpc");
	        let start = std::time::Instant::now();
	        let result = async {
	%s
	            let response = %s?;
	%s
	        }.await;
	        let duration_ms = start.elapsed().as_millis() as i64;
	%s
	        result
	    }`, method.Name, streamType, methodSnake, inputType, method.Name, authLine, fullMethod, serverStreamInputValidation, handlerCall, streamResponseReturn, streamAccountingBlock), nil
			}
			return fmt.Sprintf(`    type %sStream = %s;

    async fn %s(
        &self,
        request: tonic::Request<%s>,
    ) -> Result<tonic::Response<Self::%sStream>, tonic::Status> {
        %s
	        let request_id = servicemanager::auth::extract_request_id(&request);
	        let caller: Option<servicemanager::auth::Caller> = None;
	        tracing::debug!(method = "%s", request_id = %%request_id, "rpc");
	%s
	        let response = %s?;
	%s
	    }`, method.Name, streamType, methodSnake, inputType, method.Name, authLine, fullMethod, serverStreamInputValidation, handlerCall, streamResponseReturn), nil
		} else if method.ClientStreaming && method.ServerStreaming {
			if hasAccounting {
				return fmt.Sprintf(`    type %sStream = %s;

    async fn %s(
        &self,
        request: tonic::Request<tonic::Streaming<%s>>,
    ) -> Result<tonic::Response<Self::%sStream>, tonic::Status> {
        %s
        let request_id = servicemanager::auth::extract_request_id(&request);
	        let caller: Option<servicemanager::auth::Caller> = None;
	        tracing::debug!(method = "%s", request_id = %%request_id, "rpc");
	        let start = std::time::Instant::now();
	%s
	        let result = async {
	            let response = self.handler.%s(caller.as_ref(), request).await?;
	%s
	        }.await;
	        let duration_ms = start.elapsed().as_millis() as i64;
	%s
	        result
	    }`, method.Name, streamType, methodSnake, inputType, method.Name, authLine, fullMethod, requestStreamBlock, methodSnake, streamResponseReturn, streamAccountingBlock), nil
			}
			return fmt.Sprintf(`    type %sStream = %s;

    async fn %s(
        &self,
        request: tonic::Request<tonic::Streaming<%s>>,
    ) -> Result<tonic::Response<Self::%sStream>, tonic::Status> {
        %s
	        let request_id = servicemanager::auth::extract_request_id(&request);
	        let caller: Option<servicemanager::auth::Caller> = None;
	        tracing::debug!(method = "%s", request_id = %%request_id, "rpc");
	%s
	        let response = self.handler.%s(caller.as_ref(), request).await?;
	%s
	    }`, method.Name, streamType, methodSnake, inputType, method.Name, authLine, fullMethod, requestStreamBlock, methodSnake, streamResponseReturn), nil
		} else {
			// Client streaming only
			if hasAccounting {
				return fmt.Sprintf(`    async fn %s(
        &self,
        request: tonic::Request<tonic::Streaming<%s>>,
    ) -> Result<tonic::Response<%s>, tonic::Status> {
        %s
        let request_id = servicemanager::auth::extract_request_id(&request);
	        let caller: Option<servicemanager::auth::Caller> = None;
	        tracing::debug!(method = "%s", request_id = %%request_id, "rpc");
	        let start = std::time::Instant::now();
	%s
	        let result = async {
	            let response = self.handler.%s(caller.as_ref(), request).await?;
	%s
	            Ok(response)
	        }.await;
	        let duration_ms = start.elapsed().as_millis() as i64;
	%s
	        result
	    }`, methodSnake, inputType, outputType, authLine, fullMethod, requestStreamBlock, methodSnake, unaryStreamResponseValidation, streamAccountingBlock), nil
			}
			return fmt.Sprintf(`    async fn %s(
	        &self,
        request: tonic::Request<tonic::Streaming<%s>>,
    ) -> Result<tonic::Response<%s>, tonic::Status> {
        %s
	        let request_id = servicemanager::auth::extract_request_id(&request);
	        let caller: Option<servicemanager::auth::Caller> = None;
	        tracing::debug!(method = "%s", request_id = %%request_id, "rpc");
	%s
	        let response = self.handler.%s(caller.as_ref(), request).await?;
	%s
	        Ok(response)
	    }`, methodSnake, inputType, outputType, authLine, fullMethod, requestStreamBlock, methodSnake, unaryStreamResponseValidation), nil
		}
	}

	// Build accounting emit block for unary methods
	accountingBlock := buildRustAccountingBlock(method, grpcName, svc.Name, accountingMap)

	// Handler call args — TraceRequest has no user fields, handler takes no req
	handlerArgs := "caller.as_ref(), req"
	if handlerEmpty {
		handlerArgs = "caller.as_ref()"
	}

	// Build validation block for unary methods (only for non-empty input types).
	var validationBlock string
	if !handlerEmpty {
		validationBlock = `
        // Validate request before invoking the handler.
        req.validate().map_err(|e| tonic::Status::invalid_argument(format!("validation failed: {}", e)))?;
`
		// (spec.principal): stamp or refuse before the handler sees the request.
		if pf := san.requestPrincipalField(method.FullInputType, servicePackage); pf != "" {
			validationBlock += rustRequestPrincipalBlock(pf, method.Name)
		}
		// (spec.namespace): the same stamp or refuse, bound to the namespace claim.
		if nf := san.requestNamespaceField(method.FullInputType, servicePackage); nf != "" {
			validationBlock += rustRequestNamespaceBlock(nf, method.Name)
		}
		// (spec.owner_default): refuse the owner's own contradicted access,
		// after the stamp has bound the request principal to the caller.
		validationBlock += ownerDefaultGuard
	}
	responseValidationBlock := rustServerResponseValidationBlock("resp", method.FullOutputType)

	if method.IsPageCollection() {
		// PAGE: unary on the wire. The wrapper owns the page_token codec and the
		// trust-tier page_size bound; the handler is a keyset producer returning a
		// collection::Page. A malformed page_token -> INVALID_ARGUMENT (decode_page_token
		// returns the Status), never a silent reset to page 1. Like the other
		// collection wrappers, PAGE skips the unary trace-v2 dance.
		itemsField := prostSnakeName(method.Resolved.PageItemsField)
		pageValidation := `
        // Validate request before decoding the page token or invoking the handler.
        req.validate().map_err(|e| tonic::Status::invalid_argument(format!("validation failed: {}", e)))?;
`
		totalBlock := ""
		if method.Resolved.PageHasTotal {
			totalBlock = `
        // total_count is reported only when the handler computes it (>= 0); a
        // negative total leaves the field at 0 = "unknown", not "empty".
        if page.total >= 0 {
            resp.total_count = page.total;
        }`
		}
		// Egress data entitlement: sanitize every page item after the handler
		// returns, before the response is assembled/validated.
		var pageSan string
		if t, ok := san.sanitizableType(method.Resolved.PageItemType, servicePackage, crossPkgTypes); ok {
			pageValidation += rustEgressSanitizePreamble([]rustSanTarget{t})
			pageSan = fmt.Sprintf(`
        let mut page = page;
        page.items = page.items.iter().map(|m| %s).collect();`, t.sanitizeExpr("m"))
		}
		// (spec.store_principal)/(spec.store_namespace): the same egress
		// backstop the unary branch below splices into responseValidationBlock.
		// PAGE returns before reaching that code, so without this a page/list
		// response's rows were never checked against the caller's principal or
		// tenant at all, unlike a unary response over the same item type.
		// Computed over the assembled response envelope (method.FullOutputType),
		// exactly like the unary leg — the page item type is a repeated field
		// of that envelope, which is what these helpers look for.
		var pageEgressFilter string
		if lf := san.principalListFields(method.FullOutputType, servicePackage); len(lf) > 0 {
			pageEgressFilter += rustResponsePrincipalFilterBlock(lf, method.Name)
		}
		if lf := san.namespaceListFields(method.FullOutputType, servicePackage); len(lf) > 0 {
			pageEgressFilter += rustResponseNamespaceFilterBlock(lf, method.Name)
		}
		responseValidationBlock = pageEgressFilter + responseValidationBlock
		pageBody := fmt.Sprintf(`    async fn %s(
        &self,
        request: tonic::Request<%s>,
    ) -> Result<tonic::Response<%s>, tonic::Status> {
        %s

        let request_id = servicemanager::auth::extract_request_id(&request);
        let caller: Option<servicemanager::auth::Caller> = None;
        let req = request.into_inner();
        tracing::debug!(method = "%s", request_id = %%request_id, "rpc");
%s
        // Decode the opaque page_token into a keyset cursor (bad token -> INVALID_ARGUMENT).
        let after = collection::decode_page_token(&req.page_token)?;
        // Resolve the effective page size: default when unset, clamped to the trust ceiling.
        let limit = collection::effective_page_size(req.page_size as i64, %d, %d);
        let page = self.handler.%s(caller.as_ref(), req, collection::PageParams { limit, after }).await?;%s
        let mut resp = <%s>::default();
        resp.%s = page.items;
        resp.next_page_token = collection::encode_page_token(page.next_after.as_ref());%s
%s
        Ok(tonic::Response::new(resp))
    }`, methodSnake, inputType, outputType, authLine,
			fullMethod, pageValidation,
			method.Resolved.DefaultPageSize, method.Resolved.MaxPageSize, methodSnake, pageSan,
			outputType, itemsField, totalBlock, responseValidationBlock)
		return pageBody, nil
	}

	// Egress data entitlement for unary responses (e.g. RecordMap Sample<T>): a
	// sanitizable response type is sanitized whole; an injected envelope
	// (Sample<T>Response) has each record-bearing field sanitized instead.
	// The grant/entitlement setup runs after the handler returns, before
	// response validation — prepended to the response-validation block so every
	// unary variant picks it up.
	{
		var unarySan string
		if t, ok := san.sanitizableType(method.FullOutputType, servicePackage, crossPkgTypes); ok {
			unarySan = rustEgressSanitizePreamble([]rustSanTarget{t}) + fmt.Sprintf("        let resp = %s;\n", t.sanitizeExpr("&resp"))
		} else if fs := san.envelopeSanFields(method.FullOutputType, servicePackage, crossPkgTypes); len(fs) > 0 {
			var targets []rustSanTarget
			for _, f := range fs {
				targets = append(targets, f.Target)
			}
			unarySan = rustEgressSanitizePreamble(targets) + "        let mut resp = resp;\n" + rustEgressEnvelopeStmts(fs, "resp", "        ")
		}
		responseValidationBlock = unarySan + responseValidationBlock
	}

	// (spec.store_principal): filter rows the caller does not own out of the
	// response, before it is validated and returned. Egress-side, like sanitize.
	if lf := san.principalListFields(method.FullOutputType, servicePackage); len(lf) > 0 {
		responseValidationBlock = rustResponsePrincipalFilterBlock(lf, method.Name) + responseValidationBlock
	}

	// (spec.store_namespace): hold the rows to the caller's tenant on the same
	// egress seam. Runs after the principal filter is spliced in, so it is the
	// OUTERMOST of the two — the tenant boundary is checked over exactly the
	// rows that would otherwise be returned.
	if lf := san.namespaceListFields(method.FullOutputType, servicePackage); len(lf) > 0 {
		responseValidationBlock = rustResponseNamespaceFilterBlock(lf, method.Name) + responseValidationBlock
	}

	// Unary methods - authorize, extract caller, validate, delegate to handler
	if hasAccounting {
		if emptyInput {
			return fmt.Sprintf(`    async fn %s(
        &self,
        request: tonic::Request<%s>,
    ) -> Result<tonic::Response<%s>, tonic::Status> {
        %s

        let request_id = servicemanager::auth::extract_request_id(&request);
        let caller: Option<servicemanager::auth::Caller> = None;
        tracing::debug!(method = "%s", request_id = %%request_id, "rpc");

	        // Call the handler
	        let start = std::time::Instant::now();
	        let result = async {
	            let resp = self.handler.%s(caller.as_ref()).await?;
		%s
	            Ok::<%s, tonic::Status>(resp)
	        }.await;
	        let duration_ms = start.elapsed().as_millis() as i64;
		%s
		        let resp = result?;
		        Ok(tonic::Response::new(resp))
		    }`, methodSnake, inputType, outputType, authLine, fullMethod, methodSnake, responseValidationBlock, outputType, accountingBlock), nil
		}

		return fmt.Sprintf(`    async fn %s(
        &self,
        request: tonic::Request<%s>,
    ) -> Result<tonic::Response<%s>, tonic::Status> {
        %s

        let request_id = servicemanager::auth::extract_request_id(&request);
        let caller: Option<servicemanager::auth::Caller> = None;
        let %sreq = request.into_inner();
        tracing::debug!(method = "%s", request_id = %%request_id, "rpc");
%s%s
	        // Call the handler
	        let start = std::time::Instant::now();
	        let result = async {
	            let %sresp = self.handler.%s(%s).await?;
		%s%s
	            Ok::<%s, tonic::Status>(resp)
	        }.await;
	        let duration_ms = start.elapsed().as_millis() as i64;
	%s
	%s
		        let resp = result?;
		        Ok(tonic::Response::new(resp))
		    }`, methodSnake, inputType, outputType, authBlock, reqMut, fullMethod, validationBlock, traceAppend, respMut, methodSnake, handlerArgs, responseValidationBlock, traceCopy, outputType, traceErrorOnResult, accountingBlock), nil
	}

	// No accounting — original behavior
	if emptyInput {
		return fmt.Sprintf(`    async fn %s(
        &self,
        request: tonic::Request<%s>,
    ) -> Result<tonic::Response<%s>, tonic::Status> {
        %s

        let request_id = servicemanager::auth::extract_request_id(&request);
        let caller: Option<servicemanager::auth::Caller> = None;
        tracing::debug!(method = "%s", request_id = %%request_id, "rpc");

	        // Call the handler
	        let resp = self.handler.%s(caller.as_ref()).await?;
	%s

	        Ok(tonic::Response::new(resp))
	    }`, methodSnake, inputType, outputType, authLine, fullMethod, methodSnake, responseValidationBlock), nil
	}

	return fmt.Sprintf(`    async fn %s(
        &self,
        request: tonic::Request<%s>,
    ) -> Result<tonic::Response<%s>, tonic::Status> {
        %s

        let request_id = servicemanager::auth::extract_request_id(&request);
        let caller: Option<servicemanager::auth::Caller> = None;
        let %sreq = request.into_inner();
        tracing::debug!(method = "%s", request_id = %%request_id, "rpc");
%s%s
        // Call the handler
        let result = async {
	        let %sresp = self.handler.%s(%s).await?;
	%s
            Ok::<%s, tonic::Status>(resp)
        }.await;
%s
        let resp = result?;
%s
	        Ok(tonic::Response::new(resp))
	    }`, methodSnake, inputType, outputType, authBlock, reqMut, fullMethod, validationBlock, traceAppend, respMut, methodSnake, handlerArgs, responseValidationBlock, outputType, traceErrorOnResult, traceCopy), nil
}

// rustStoreEpochField returns the name of fullType's (spec.store_epoch) field, or
// "" when it declares none. The Rust wrapper never STAMPS it — the stamp is
// generated for Java service wrappers only — so this exists purely so a Rust host
// of an epoch-bearing write fails generation instead of writing an unstamped row.
func rustStoreEpochField(fullType string, san *rustEgressSanitizer, servicePackage string) string {
	if san == nil || fullType == "" {
		return ""
	}
	pkg, name := splitRustEgressFQN(fullType, servicePackage)
	msg, ok := san.msgs[pkg+"\x00"+name]
	if !ok {
		return ""
	}
	for i := range msg.Fields {
		if msg.Fields[i].Annotations["store_epoch"].BoolValue {
			return msg.Fields[i].Name
		}
	}
	return ""
}

// rustAppendOnlyRecordOnMethod returns the append-only record type a row-mutating
// method carries, or "" when the method is not one. Same definition of
// "row-mutating" the parser and the Java wrapper use (parser.IsRowMutatingPattern),
// so the three ends cannot drift apart.
func rustAppendOnlyRecordOnMethod(method *parser.MethodInfo, san *rustEgressSanitizer, servicePackage string) string {
	if san == nil || !parser.IsRowMutatingPattern(method.Class) {
		return ""
	}
	for _, t := range []string{method.FullInputType, method.FullOutputType} {
		if t == "" {
			continue
		}
		pkg, name := splitRustEgressFQN(t, servicePackage)
		msg, ok := san.msgs[pkg+"\x00"+name]
		if !ok {
			continue
		}
		if ann, ok := msg.Annotations["record"]; !ok || !ann.BoolValue {
			continue
		}
		if msg.Annotations["record_mutability"].StringValue == "MUTABILITY_APPEND_ONLY" {
			return t
		}
	}
	return ""
}
