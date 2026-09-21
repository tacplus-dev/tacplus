// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"bytes"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"go.uber.org/zap"
	"golang.org/x/tools/imports"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/boundaries"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/collection"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
	"gitlab.com/secondstarcavalier/schematic/tacplus/templates"
)

// wellKnownGoType maps google.protobuf type names to their Go import alias and path.
var wellKnownGoType = map[string]struct{ alias, importPath string }{
	"Empty":       {"emptypb", "google.golang.org/protobuf/types/known/emptypb"},
	"Any":         {"anypb", "google.golang.org/protobuf/types/known/anypb"},
	"Timestamp":   {"timestamppb", "google.golang.org/protobuf/types/known/timestamppb"},
	"Duration":    {"durationpb", "google.golang.org/protobuf/types/known/durationpb"},
	"Struct":      {"structpb", "google.golang.org/protobuf/types/known/structpb"},
	"Value":       {"structpb", "google.golang.org/protobuf/types/known/structpb"},
	"ListValue":   {"structpb", "google.golang.org/protobuf/types/known/structpb"},
	"FieldMask":   {"fieldmaskpb", "google.golang.org/protobuf/types/known/fieldmaskpb"},
	"BoolValue":   {"wrapperspb", "google.golang.org/protobuf/types/known/wrapperspb"},
	"BytesValue":  {"wrapperspb", "google.golang.org/protobuf/types/known/wrapperspb"},
	"DoubleValue": {"wrapperspb", "google.golang.org/protobuf/types/known/wrapperspb"},
	"FloatValue":  {"wrapperspb", "google.golang.org/protobuf/types/known/wrapperspb"},
	"Int32Value":  {"wrapperspb", "google.golang.org/protobuf/types/known/wrapperspb"},
	"Int64Value":  {"wrapperspb", "google.golang.org/protobuf/types/known/wrapperspb"},
	"StringValue": {"wrapperspb", "google.golang.org/protobuf/types/known/wrapperspb"},
	"UInt32Value": {"wrapperspb", "google.golang.org/protobuf/types/known/wrapperspb"},
	"UInt64Value": {"wrapperspb", "google.golang.org/protobuf/types/known/wrapperspb"},
}

// generatedServiceWrapper tracks info about a generated service wrapper for the aggregators.
type generatedServiceWrapper struct {
	PackageName      string // e.g. "baseball"
	ServerName       string // e.g. "BaseballService"
	ServiceName      string // proto service name: "BaseballService"
	ImportAlias      string // e.g. "baseball_fe"
	ImportPath       string // e.g. "example.com/myproj/tacplus/sm/internal/grpc_servers/services/baseball"
	TypesImportAlias string // e.g. "baseball_types"
	TypesImportPath  string // e.g. "example.com/myproj/tacplus/types/services/baseball"
	ParentDir        string // e.g. "services"
}

// generateServiceWrappers generates server.go for each service in the types directory
func generateServiceWrappers(config *tactype.Config, servicesInfo []ServiceInfo, inputDir string, accountingMap map[string]service.AccountingInfo, skippedTraceTypes proto.SkippedTraceTypes) ([]generatedServiceWrapper, error) {
	// Create descriptor parser with the proto root directory
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	// Build the egress-sanitize oracle over the (entitlement-augmented) descriptor
	// set, mirroring the helpers emitter's file set (non-infra), so the wrapper
	// emits a Sanitize<T> call exactly where the helpers emitted one.
	var allFiles []model.ProtoFile
	for relPath := range descriptorParser.GetAllFiles() {
		if proto.IsInfraProto(relPath) {
			continue
		}
		mf, err := descriptorParser.ConvertToProtoFile(relPath)
		if err != nil {
			return nil, fmt.Errorf("convert %s for egress sanitize resolution: %w", relPath, err)
		}
		allFiles = append(allFiles, mf)
	}
	san := newEgressSanitizer(allFiles)

	// Q2: resolve the accounting topic once so every wrapper can drop that
	// topic's own publish RPC from its accounting map. See
	// accounting_exclusion.go for why this exclusion is mandatory.
	accountingTopic, hasAccountingTopic, err := boundaries.LookupAccountingRecordTopic(descriptorParser)
	if err != nil {
		return nil, fmt.Errorf("resolve accounting record topic: %w", err)
	}

	var generated []generatedServiceWrapper
	// Track directories where sanitizedContext helper has been written
	sanitizedContextDirs := make(map[string]bool)

	for _, svc := range servicesInfo {
		// Skip lib services (e.g., LockService, MapService, GrpcServerService) —
		// they are internal infrastructure and don't need wrappers.
		fileDesc, err := descriptorParser.GetFileDescriptor(svc.ProtoFile)
		if err != nil {
			return nil, fmt.Errorf("failed to parse descriptor for %s: %w", svc.ProtoFile, err)
		}
		if proto.IsInfraProtoFile(svc.ProtoFile, fileDesc) {
			continue
		}
		// Parse the proto file to get method information
		protoPath := filepath.Join(inputDir, svc.ProtoFile)

		protoInfo, err := descriptorParser.ConvertToProtoInfo(protoPath)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", svc.ProtoFile, err)
		}

		// Generate service wrapper in the gRPC servers output directory
		svcAccountingMap := excludeAccountingSelfPublish(
			accountingMap, svc.GrpcName, protoInfo.Package, svc.ServiceName,
			accountingTopic, hasAccountingTopic)
		if err := generateServiceWrapper(svc, protoInfo, svcAccountingMap, skippedTraceTypes, san); err != nil {
			return nil, fmt.Errorf("failed to generate service wrapper for %s: %w", svc.ServiceName, err)
		}

		// Write sanitizedContext helper once per output directory
		wrapperOutputDir := filepath.Join(svc.Config.OutputDir, svc.Config.GetSmPrefix(), "grpc", "servers", svc.ParentDir, svc.ImportDirName())
		if !sanitizedContextDirs[wrapperOutputDir] {
			sanitizedContextDirs[wrapperOutputDir] = true
			if err := writeSanitizedContextHelper(wrapperOutputDir, svc.PackageName); err != nil {
				return nil, fmt.Errorf("failed to write sanitizedContext helper: %w", err)
			}
		}

		wrapperRelPath := svc.ParentDir + "/" + svc.ImportDirName()
		var typesImportPath string
		if svc.ParentDir == "services" {
			typesImportPath = fmt.Sprintf("%s/types/services/%s", config.ModuleBasePath, svc.ImportDirName())
		} else {
			typesImportPath = fmt.Sprintf("%s/types/%s/%s", config.ModuleBasePath, svc.ParentDir, svc.ImportDirName())
		}
		generated = append(generated, generatedServiceWrapper{
			PackageName:      svc.PackageName,
			ServerName:       svc.ServiceName,
			ServiceName:      svc.ServiceName,
			ImportAlias:      svc.PackageName + "_fe",
			ImportPath:       config.ModuleBasePath + "/" + config.GetSmPrefix() + "/grpc/servers/" + wrapperRelPath,
			TypesImportAlias: svc.PackageName + "_types",
			TypesImportPath:  typesImportPath,
			ParentDir:        svc.ParentDir,
		})
	}

	return generated, nil
}

// loadTemplate loads a template from the embedded templates filesystem.
func loadTemplate(name string) (string, error) {
	typeFS, err := fs.Sub(templates.FS, "go/types")
	if err != nil {
		return "", fmt.Errorf("failed to create sub filesystem: %w", err)
	}
	content, err := fs.ReadFile(typeFS, name)
	if err != nil {
		return "", fmt.Errorf("failed to read template %s: %w", name, err)
	}
	return string(content), nil
}

// loadChassisTemplate loads a template from the embedded go/chassis tree
// (e.g. "service_im/service_im.go.tmpl"). Chassis identity-manager templates
// (service_im, client_im) that require richer template data than the generic
// chassis walker supplies are generated by dedicated functions and read here.
func loadChassisTemplate(name string) (string, error) {
	chassisFS, err := fs.Sub(templates.FS, "go/chassis")
	if err != nil {
		return "", fmt.Errorf("failed to create sub filesystem: %w", err)
	}
	content, err := fs.ReadFile(chassisFS, name)
	if err != nil {
		return "", fmt.Errorf("failed to read template %s: %w", name, err)
	}
	return string(content), nil
}

// generateServiceWrapper creates the service wrapper file for a service
func generateServiceWrapper(svc ServiceInfo, info *parser.ProtoInfo, accountingMap map[string]service.AccountingInfo, skippedTraceTypes proto.SkippedTraceTypes, san *egressSanitizer) error {
	// Output to sm/internal/grpc_servers/{parentDir}/{name}/
	wrapperOutputDir := filepath.Join(svc.Config.OutputDir, svc.Config.GetSmPrefix(), "grpc", "servers", svc.ParentDir, svc.ImportDirName())
	if err := os.MkdirAll(wrapperOutputDir, 0755); err != nil {
		return fmt.Errorf("failed to create wrapper output directory: %w", err)
	}
	// Determine filename, stripping redundant package prefix
	// (e.g., IdentityService in identity/ → service.go, not identity_service.go)
	serverFile := strutil.StripPackagePrefix(strutil.ToSnakeCase(svc.ServiceName), svc.ImportDirName()) + ".go"
	outputPath := filepath.Join(wrapperOutputDir, serverFile)

	zap.L().Debug("Generated service server", zap.String("path", outputPath))

	// Filter to only the service being wrapped
	var targetServices []*parser.ServiceInfo
	for _, service := range info.Services {
		if service.Name == svc.ServiceName {
			targetServices = append(targetServices, service)
		}
	}

	// Build handler interface and gRPC method wrappers with validation
	var handlerInterface strings.Builder
	var grpcWrappers strings.Builder
	var methodAuthLines []string
	for _, service := range targetServices {
		handlerInterface.WriteString(generateGoHandlerInterface(service, info.Package))
		// The peer half of a record query plane, whoever hosts it — Java's own
		// emission gate for the (spec.store_namespace)/(spec.store_principal)
		// egress seams (java/types/servicewrapper_gen.go), copied rather than
		// tightened. A plain s2s service returning record rows is legitimate:
		// Java emits no row filter there either, so refusing it here would be a
		// generation-time regression, not a boundary.
		recordPlaneS2S := !service.IsC2S && service.SynthesizedKind == parser.SynthesizedKindRecordMap
		for _, method := range service.Methods {
			// An RPC_PRINCIPAL_MCP rpc is a tool, not a gRPC call: it gets no
			// method on the wrapper and no per-method auth registration. The
			// wrapper embeds types.Unimplemented<Service>Server, so a method the
			// wrapper does not define already answers UNIMPLEMENTED — like
			// Java's ImplBase and unlike Rust's tonic trait, nothing has to be
			// emitted in its place.
			//
			// It DOES get a handler-interface method: that is what the generated
			// MCP dispatch calls (mcp_dispatch_gen.go). Only the gRPC-callable
			// surface is suppressed.
			if method.IsMcp {
				continue
			}
			// (spec.record).min_group_size is enforced in the generated Java
			// service wrapper only. A Go host of a record query plane would serve
			// the size-only RPCs with no guard at all, so refuse to generate
			// rather than ship the disclosure silently.
			if method.MinGroupSize > 0 {
				return fmt.Errorf("%s.%s carries (spec.min_group_size) = %d, but the guard is generated for Java service wrappers only; a Go host of this record query plane would serve it unguarded",
					service.Name, method.Name, method.MinGroupSize)
			}
			// (spec.record).mutability = MUTABILITY_APPEND_ONLY: a row mutation
			// over a write-once record has no guard to render in ANY language,
			// so hosting it is a generation failure here too. Same refusal the
			// Java wrapper makes (javaAppendOnlyUnenforceable), reached through
			// the same definition of "row-mutating".
			if t := goAppendOnlyRecordOnMethod(method, san, info.Package); t != "" {
				return fmt.Errorf("%s.%s must not be generated: it carries %s, which declares (spec.record).mutability = MUTABILITY_APPEND_ONLY; an append-only record's rows are written once, so no row-mutating RPC may be hosted for the type",
					service.Name, method.Name, t)
			}
			// (spec.store_epoch) is stamped in the generated Java service
			// wrapper only. A Go host would reach its handler with an unstamped
			// epoch — a row the purge measures as infinitely old — so refuse
			// to generate rather than write the wrong clock silently.
			if f := goStoreEpochField(method.FullInputType, san, info.Package); f != "" {
				return fmt.Errorf("%s.%s takes %s, whose field %q carries (spec.store_epoch), but the stamp is generated for Java service wrappers only; a Go host of this write would leave the store epoch unstamped",
					service.Name, method.Name, method.FullInputType, f)
			}
			// (spec.store_namespace) / (spec.store_principal) filter the rows
			// leaving a read down to the caller's own. That filter is generated
			// for Java and Rust service wrappers only — the Go wrapper renders
			// none on any seam — so a Go host of a record-bearing read would
			// serve every tenant's rows to every authenticated caller. Refuse
			// to generate rather than ship the leak silently. The MCP dispatch
			// is not covered here: it renders the filter itself
			// (goMcpRowFilterBlock), and an MCP rpc never reaches this loop.
			if service.IsC2S || recordPlaneS2S {
				if annotation, t := goPinnedEgressRows(method, san, info.Package); annotation != "" {
					return fmt.Errorf("%s.%s returns %s, which carries (spec.%s); the egress row filter is generated for Java and Rust service wrappers only, so a Go host of this read would return every tenant's rows",
						service.Name, method.Name, t, annotation)
				}
			}
			grpcWrappers.WriteString(generateGRPCWrapper(service, method, info.Package, svc.ParentDir, svc.GrpcName, accountingMap, info.CrossPackageTypes, skippedTraceTypes, san))
			grpcWrappers.WriteString("\n")

			// Per-method policy is keyed by the SERVICE-QUALIFIED method
			// identifier the Authorizer derives from the incoming full gRPC
			// method (qualifiedMethodKey), never by the bare RPC name: one
			// process may host several services behind one Authorizer, and a
			// bare-name key lets a second service's identically named RPC
			// silently overwrite the first's auth_services.
			methodKey := qualifiedMethodAuthKey(info.Package, service.Name, method.Name)

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
					"auth.SetMethodAuth(%q, []string{%s})",
					methodKey, strings.Join(quoted, ", ")))
			}

			// Register the per-RPC auth_actors patterns. There is no
			// service-level default to fall back to: auth_actors is declared on
			// spec.rpc only. The non-empty guard is what makes the gate fail
			// closed — an RPC that declares nothing registers nothing, and a
			// missing map entry rejects any token carrying act.
			if len(method.AuthActors) > 0 {
				var quoted []string
				for _, pattern := range method.AuthActors {
					quoted = append(quoted, fmt.Sprintf("%q", pattern))
				}
				methodAuthLines = append(methodAuthLines, fmt.Sprintf(
					"auth.SetMethodActors(%q, []string{%s})",
					methodKey, strings.Join(quoted, ", ")))
			}

			// Register request-field target_namespace_field for level-or-below enforcement.
			// auth_any_user uses target_namespace_field as a fixed namespace literal instead.
			if method.TargetNamespaceField != "" && !method.AuthAnyUser {
				methodAuthLines = append(methodAuthLines, fmt.Sprintf(
					"auth.SetMethodTargetNamespaceField(%q, %q)",
					methodKey, method.TargetNamespaceField))
			}
		}
	}

	// Build RegisterMethodAuth function.
	var registerMethodAuth string
	if len(methodAuthLines) > 0 {
		funcName := "Register" + svc.ServiceName + "MethodAuth"
		registerMethodAuth = "// " + funcName + " registers per-method auth_services patterns with the Authorizer.\n// Called by ServiceManager after creating the Authorizer.\nfunc " + funcName + "(auth grpcserver.Authorizer) {\n\t" +
			strings.Join(methodAuthLines, "\n\t") + "\n}"
	}

	// Use the specific service name this wrapper is being generated for
	serviceName := svc.ServiceName

	tmpl, err := loadTemplate("servicewrapper.go.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load service wrapper template: %w", err)
	}

	// Calculate types import path for the new structure
	var typesModulePath string
	dirName := svc.ImportDirName()
	if svc.ParentDir == "services" {
		typesModulePath = fmt.Sprintf("%s/types/services/%s", svc.Config.ModuleBasePath, dirName)
	} else {
		typesModulePath = fmt.Sprintf("%s/types/%s/%s", svc.Config.ModuleBasePath, svc.ParentDir, dirName)
	}
	errorsModulePath := svc.Config.ErrorsPath()
	grpcPkgName := svc.PackageName

	// For service protos, gRPC stubs are in the same flat package as types
	var grpcStubsImportPath string
	if strings.HasPrefix(svc.ParentDir, "services") {
		grpcStubsImportPath = fmt.Sprintf("%s/types/%s/%s", svc.Config.ModuleBasePath, svc.ParentDir, dirName)
	} else if filepath.Base(svc.ParentDir) == svc.PackageName {
		grpcStubsImportPath = fmt.Sprintf("%s/types/%s/services", svc.Config.ModuleBasePath, svc.ParentDir)
	} else {
		grpcStubsImportPath = fmt.Sprintf("%s/types/%s/%s/services", svc.Config.ModuleBasePath, svc.ParentDir, dirName)
	}

	// Build cross-package import lines (only for packages referenced by the target service)
	usedPkgs := make(map[string]bool)
	for _, service := range targetServices {
		for _, method := range service.Methods {
			for _, fullType := range []string{method.FullInputType, method.FullOutputType} {
				parts := strings.Split(fullType, ".")
				if len(parts) > 1 {
					pkg := strings.Join(parts[:len(parts)-1], ".")
					if pkg != info.Package {
						usedPkgs[pkg] = true
					}
				}
			}
			if method.Resolved != nil {
				for _, fullType := range []string{method.Resolved.PageItemType, method.Resolved.WatchItemType, method.Resolved.WatchChangeType} {
					parts := strings.Split(fullType, ".")
					if len(parts) > 1 {
						pkg := strings.Join(parts[:len(parts)-1], ".")
						if pkg != info.Package {
							usedPkgs[pkg] = true
						}
					}
				}
			}
		}
	}
	var crossPkgImports strings.Builder

	// Handle google.protobuf well-known types separately
	if usedPkgs["google.protobuf"] {
		emitted := make(map[string]bool)
		for _, service := range targetServices {
			for _, method := range service.Methods {
				for _, fullType := range []string{method.FullInputType, method.FullOutputType} {
					if strings.HasPrefix(fullType, "google.protobuf.") {
						typeName := fullType[len("google.protobuf."):]
						if wkt, ok := wellKnownGoType[typeName]; ok && !emitted[wkt.alias] {
							fmt.Fprintf(&crossPkgImports, "\n%s \"%s\"", wkt.alias, wkt.importPath)
							emitted[wkt.alias] = true
						}
					}
				}
			}
		}
	}

	for pkg, dir := range info.CrossPackageTypes {
		if dir == "" || !usedPkgs[pkg] || pkg == "google.protobuf" {
			continue
		}
		importAlias := pkg + "_pb"
		var importPath string
		if filepath.Base(dir) == pkg {
			importPath = fmt.Sprintf("%s/types/%s", svc.Config.ModuleBasePath, dir)
		} else {
			importPath = fmt.Sprintf("%s/types/%s/%s", svc.Config.ModuleBasePath, dir, pkg)
		}
		if strings.HasPrefix(dir, "services") {
			importPath = fmt.Sprintf("%s/types/%s/%s", svc.Config.ModuleBasePath, dir, pkg)
		}
		fmt.Fprintf(&crossPkgImports, "\n%s \"%s\"", importAlias, importPath)
	}

	// Server name matches the proto service name
	serverName := serviceName

	// Replace {{.ServerName}} placeholder in helper-generated strings.
	replaceWrapper := func(s string) string {
		return strings.ReplaceAll(s, "{{.ServerName}}", serverName)
	}

	serviceManagerImportPath := svc.Config.ModuleBasePath + "/chassis/servicemanager"
	amLibPath := svc.Config.ModuleBasePath + "/chassis/am"
	accountingMethodPath := svc.Config.ModuleBasePath + "/types/generated/accounting_method"
	servicesPath := svc.Config.ModuleBasePath + "/types/generated/services"
	traceTypesPath := svc.Config.ModuleBasePath + "/types/sys/trace"

	// HasHandler is true when the service has methods that should use the clean Handler interface.
	// State services (no methods in descriptor) use the raw gRPC impl pattern instead.
	hasHandler := handlerInterface.Len() > 0

	// WATCH (Hub/WatchSource) and PAGE (PageParams/Page/token codec) methods
	// reference the emitted collection runtime lib; import it explicitly since
	// goimports can't resolve the local package by name.
	collectionLibImportPath := svc.Config.ModuleBasePath + "/chassis/collection"
	hasWatch := false
	for _, service := range targetServices {
		for _, m := range service.Methods {
			if m.IsWatchCollection() || m.IsPageCollection() {
				hasWatch = true
			}
		}
	}

	data := struct {
		GrpcPackageName          string
		TypesModulePath          string
		GrpcStubsImportPath      string
		ServiceManagerImportPath string
		TokenLibPath             string
		AmLibPath                string
		AccountingMethodPath     string
		ServicesPath             string
		TraceTypesPath           string
		CrossPackageImports      string
		ErrorsModulePath         string
		ServiceName              string
		ServerName               string
		ServiceLabel             string
		HasHandler               bool
		HandlerInterface         string
		GRPCWrappers             string
		RegisterMethodAuth       string
		GrpcServerImportPath     string
		HasWatch                 bool
		CollectionLibImportPath  string
	}{
		GrpcPackageName:          grpcPkgName,
		TypesModulePath:          typesModulePath,
		GrpcStubsImportPath:      grpcStubsImportPath,
		ServiceManagerImportPath: serviceManagerImportPath,
		TokenLibPath:             svc.Config.ModuleBasePath + "/chassis/token",
		AmLibPath:                amLibPath,
		AccountingMethodPath:     accountingMethodPath,
		ServicesPath:             servicesPath,
		TraceTypesPath:           traceTypesPath,
		CrossPackageImports:      crossPkgImports.String(),
		ErrorsModulePath:         errorsModulePath,
		ServiceName:              serviceName,
		ServerName:               serverName,
		ServiceLabel:             svc.Config.ServiceLabel(),
		HasHandler:               hasHandler,
		HandlerInterface:         replaceWrapper(handlerInterface.String()),
		GRPCWrappers:             replaceWrapper(grpcWrappers.String()),
		RegisterMethodAuth:       registerMethodAuth,
		GrpcServerImportPath:     svc.Config.ModuleBasePath + "/chassis/grpcserver",
		HasWatch:                 hasWatch,
		CollectionLibImportPath:  collectionLibImportPath,
	}

	t, err := template.New("servicewrapper").Parse(tmpl)
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	// Format and remove unused imports
	formatted, err := imports.Process(outputPath, buf.Bytes(), nil)
	if err != nil {
		// If goimports fails, try plain formatting
		zap.L().Warn("failed to goimports service wrapper, trying format.Source", zap.Error(err))
		formatted, err = format.Source(buf.Bytes())
		if err != nil {
			zap.L().Warn("failed to format service wrapper", zap.Error(err))
			formatted = buf.Bytes()
		}
	}

	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write service wrapper: %w", err)
	}

	// The MCP termination lands in its own file beside the wrapper: same
	// package, so the carrier method the wrapper emits calls straight into it,
	// but its own import block (the MCP SDK, protojson, the JSON-RPC codec) does
	// not have to be threaded through the wrapper template for the services that
	// declare no tool.
	for _, service := range targetServices {
		mcpParams := goMcpFileParams{
			PackageName:              grpcPkgName,
			TypesModulePath:          typesModulePath,
			ServiceManagerImportPath: serviceManagerImportPath,
			ModuleBasePath:           svc.Config.ModuleBasePath,
			CrossPackageTypes:        info.CrossPackageTypes,
			ServicePackage:           info.Package,
			used:                     goMcpUsedPackages(service, info.Package),
		}
		if err := writeGoMcpDispatchFile(service, wrapperOutputDir, mcpParams, san); err != nil {
			return err
		}
		if err := writeGoMcpDispatchTestFile(service, wrapperOutputDir, mcpParams, san); err != nil {
			return err
		}
	}

	return nil
}

// writeSanitizedContextHelper writes a shared context_sanitize.go file once per package.
// This avoids duplicate declarations when multiple service wrappers share a package.
func writeSanitizedContextHelper(dir, pkgName string) error {
	content := fmt.Sprintf(`// Copyright (c) 2026 Second Star Cavalier LLC
// Generated by service generator
//
// CODE GENERATED - DO NOT EDIT

package %s

import "context"

// sanitizedContext wraps a context to strip all values while preserving
// cancellation and deadline. Handler methods receive a clean context
// without gRPC metadata or raw token access.
type sanitizedContext struct {
	context.Context
}

func (sanitizedContext) Value(any) any { return nil }

func sanitizeContext(ctx context.Context) context.Context {
	return sanitizedContext{ctx}
}
`, pkgName)

	return os.WriteFile(filepath.Join(dir, "context_sanitize.go"), []byte(content), 0644)
}

// goValidateRef returns the Go-qualified validation function call for a type.
func goValidateRef(fullType, simpleType, servicePackage string) string {
	if isTraceRequestType(fullType) {
		return "" // TraceRequest has no user fields to validate
	}
	parts := strings.Split(fullType, ".")
	if len(parts) > 1 {
		pkg := strings.Join(parts[:len(parts)-1], ".")
		if pkg != servicePackage && pkg != "" {
			if pkg == "google.protobuf" {
				return ""
			}
			return pkg + "_pb.Validate" + simpleType
		}
	}
	return "types.Validate" + simpleType
}

func validationFuncOrNil(ref string) string {
	if ref == "" {
		return "nil"
	}
	return ref
}

// goTypeRef returns the Go-qualified type reference for a method's input or output type.
func goTypeRef(fullType, simpleType, servicePackage string) string {
	parts := strings.Split(fullType, ".")
	if len(parts) > 1 {
		pkg := strings.Join(parts[:len(parts)-1], ".")
		if pkg != servicePackage && pkg != "" {
			if pkg == "google.protobuf" {
				if wkt, ok := wellKnownGoType[simpleType]; ok {
					return wkt.alias + "." + simpleType
				}
			}
			return pkg + "_pb." + simpleType
		}
	}
	return "types." + simpleType
}

// isEmptyType returns true if the full proto type is google.protobuf.Empty.
func isEmptyType(fullType string) bool {
	return fullType == "google.protobuf.Empty"
}

// isTraceRequestType returns true if the full proto type is trace.TraceRequest.
// TraceRequest replaces google.protobuf.Empty on the gRPC wire so that every
// RPC can carry trace fields — but the handler interface treats it as empty.
func isTraceRequestType(fullType string) bool {
	return fullType == "trace.TraceRequest"
}

// goWatchType renders a resolved WATCH envelope FQN ("pkg.Name") as a Go type
// reference for this service package.
func goWatchType(fqn, servicePackage string) string {
	simple := fqn
	if i := strings.LastIndex(fqn, "."); i >= 0 {
		simple = fqn[i+1:]
	}
	return goTypeRef(fqn, simple, servicePackage)
}

// isTier2SnapshotCollection reports whether a method is a Tier-2 SNAPSHOT
// collection read: server-streaming on the wire, but the generated seam is
// collection-in/collection-out — the handler returns a materialized []*Item and
// the wrapper chunks it into the stream (WS-1). Tier-1 stays unary and Tier-3
// (cursor) is WS-2, so both fall through to the existing emit paths.
func isTier2SnapshotCollection(m *parser.MethodInfo) bool {
	return m.Resolved != nil && m.Resolved.Mode == "SNAPSHOT" && m.Resolved.Tier == collection.TierStream
}

// generateGoHandlerInterface generates the Handler interface for a service.
// The interface has clean method signatures with a *servicemanager.Caller parameter
// instead of raw gRPC types. Empty request types are omitted — the gRPC contract
// requires them, but the handler doesn't need them. Empty output types return just
// error — the wrapper constructs the Empty response.
func generateGoHandlerInterface(svc *parser.ServiceInfo, servicePackage string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "type %sHandler interface {\n", svc.Name)
	mcpCarrier := goMcpCarrierMethod(svc)
	for _, method := range svc.Methods {
		// The generated session carrier is served by the wrapper itself
		// (generateGoMcpDispatch), not by the service: nothing about carrying an
		// MCP session is the service's to write, and a Go interface has no
		// default body to give it one. Its TOOLS are on this interface; the
		// carriage is not.
		if mcpCarrier != nil && method.Name == mcpCarrier.Name {
			continue
		}
		goInput := goTypeRef(method.FullInputType, method.InputType, servicePackage)
		goOutput := goTypeRef(method.FullOutputType, method.OutputType, servicePackage)
		handlerEmpty := isEmptyType(method.FullInputType) || isTraceRequestType(method.FullInputType)
		emptyOutput := isEmptyType(method.FullOutputType)

		if method.ClientStreaming && method.ServerStreaming {
			fmt.Fprintf(&b, "\t%s(caller *servicemanager.Caller, stream grpc.BidiStreamingServer[%s, %s]) error\n",
				method.Name, goInput, goOutput)
		} else if isTier2SnapshotCollection(method) {
			// Tier-2 SNAPSHOT collection: the handler returns a materialized
			// collection; the generated wrapper chunks it into the server-stream.
			if handlerEmpty {
				fmt.Fprintf(&b, "\t%s(caller *servicemanager.Caller) ([]*%s, error)\n",
					method.Name, goOutput)
			} else {
				fmt.Fprintf(&b, "\t%s(caller *servicemanager.Caller, req *%s) ([]*%s, error)\n",
					method.Name, goInput, goOutput)
			}
		} else if method.IsTier3SnapshotCollection() {
			// Tier-3 SNAPSHOT cursor: the handler returns a lazy pull producer
			// (iter.Seq2). The wrapper drives it and the range-func cleanup runs on
			// client cancel / early exit (deterministic teardown).
			if handlerEmpty {
				fmt.Fprintf(&b, "\t%s(caller *servicemanager.Caller) (iter.Seq2[*%s, error], error)\n",
					method.Name, goOutput)
			} else {
				fmt.Fprintf(&b, "\t%s(caller *servicemanager.Caller, req *%s) (iter.Seq2[*%s, error], error)\n",
					method.Name, goInput, goOutput)
			}
		} else if method.IsWatchCollection() {
			// WATCH: the handler returns a WatchSource (snapshot + change feed); the
			// generated wrapper owns the {Method}WatchEvent envelope, replay ring,
			// OUT_OF_RANGE relist, and teardown.
			item := goWatchType(method.Resolved.WatchItemType, servicePackage)
			change := goWatchType(method.Resolved.WatchChangeType, servicePackage)
			if handlerEmpty {
				fmt.Fprintf(&b, "\t%s(caller *servicemanager.Caller) (*collection.WatchSource[%s, %s], error)\n",
					method.Name, item, change)
			} else {
				fmt.Fprintf(&b, "\t%s(caller *servicemanager.Caller, req *%s) (*collection.WatchSource[%s, %s], error)\n",
					method.Name, goInput, item, change)
			}
		} else if method.IsPageCollection() {
			// PAGE: a unary, client-driven window. The generated wrapper owns the
			// page_token codec and the trust-tier page_size bound; the handler is a
			// keyset producer — given the decoded PageParams{Limit, After} (plus req
			// for the non-pagination filter fields) it returns one collection.Page
			// (items + the cursor to resume after + an optional Total).
			item := goWatchType(method.Resolved.PageItemType, servicePackage)
			if handlerEmpty {
				fmt.Fprintf(&b, "\t%s(ctx context.Context, caller *servicemanager.Caller, page collection.PageParams) (collection.Page[*%s], error)\n",
					method.Name, item)
			} else {
				fmt.Fprintf(&b, "\t%s(ctx context.Context, caller *servicemanager.Caller, req *%s, page collection.PageParams) (collection.Page[*%s], error)\n",
					method.Name, goInput, item)
			}
		} else if method.ServerStreaming {
			if handlerEmpty {
				fmt.Fprintf(&b, "\t%s(caller *servicemanager.Caller, stream grpc.ServerStreamingServer[%s]) error\n",
					method.Name, goOutput)
			} else {
				fmt.Fprintf(&b, "\t%s(caller *servicemanager.Caller, req *%s, stream grpc.ServerStreamingServer[%s]) error\n",
					method.Name, goInput, goOutput)
			}
		} else if method.ClientStreaming {
			fmt.Fprintf(&b, "\t%s(caller *servicemanager.Caller, stream grpc.ClientStreamingServer[%s, %s]) error\n",
				method.Name, goInput, goOutput)
		} else {
			// Unary: omit Empty/TraceRequest input param, return error-only for Empty output
			if handlerEmpty && emptyOutput {
				fmt.Fprintf(&b, "\t%s(ctx context.Context, caller *servicemanager.Caller) error\n",
					method.Name)
			} else if handlerEmpty {
				fmt.Fprintf(&b, "\t%s(ctx context.Context, caller *servicemanager.Caller) (*%s, error)\n",
					method.Name, goOutput)
			} else if emptyOutput {
				fmt.Fprintf(&b, "\t%s(ctx context.Context, caller *servicemanager.Caller, req *%s) error\n",
					method.Name, goInput)
			} else {
				fmt.Fprintf(&b, "\t%s(ctx context.Context, caller *servicemanager.Caller, req *%s) (*%s, error)\n",
					method.Name, goInput, goOutput)
			}
		}
	}
	b.WriteString("}")
	return b.String()
}

// typeHasTraceFields returns true if a proto type has the injected trace_id
// field. True for user-defined types and trace.TraceRequest. Returns false for
// google.protobuf.*, sys/* types, and google.protobuf.Empty.
func typeHasTraceFields(fullType, servicePackage, parentDir string, crossPkgTypes map[string]string, skippedTraceTypes proto.SkippedTraceTypes) bool {
	if isTraceRequestType(fullType) {
		return true
	}
	if fullType == "" || isEmptyType(fullType) {
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

// generateGRPCWrapper generates a gRPC method wrapper with validation for ServiceServer.
// The wrapper extracts the caller identity from context, sanitizes the context to strip
// gRPC metadata and raw token values, and delegates to the Handler interface.
// Auth is skipped when disabled by proto annotations or for c2d local-IPC services.
// grpcName is the service's grpc_config name (e.g. "sentinel") used to construct the AccountingMethod.
func generateGRPCWrapper(svc *parser.ServiceInfo, method *parser.MethodInfo, servicePackage, parentDir, grpcName string, accountingMap map[string]service.AccountingInfo, crossPkgTypes map[string]string, skippedTraceTypes proto.SkippedTraceTypes, san *egressSanitizer) string {
	goInput := goTypeRef(method.FullInputType, method.InputType, servicePackage)
	goOutput := goTypeRef(method.FullOutputType, method.OutputType, servicePackage)
	emptyInput := isEmptyType(method.FullInputType)
	emptyOutput := isEmptyType(method.FullOutputType)
	handlerEmpty := emptyInput || isTraceRequestType(method.FullInputType)
	inputHasTrace := typeHasTraceFields(method.FullInputType, servicePackage, parentDir, crossPkgTypes, skippedTraceTypes)
	validateInput := goValidateRef(method.FullInputType, method.InputType, servicePackage)
	validateOutput := goValidateRef(method.FullOutputType, method.OutputType, servicePackage)

	// svc.IsC2D is intentionally inert here: Go never resolves a C2D service as a
	// server, so no C2D service reaches this function. Both resolveServerServices
	// and filterGrpcAnnotated in generator.go skip IsC2D services by design.
	// Rust is the only target that hosts a generated C2D server wrapper -- see
	// docs/API.md "C2D server auth-skip scope". The disjunct is kept so it stays
	// correct if that exclusion ever changes; it is not dead code to be cleaned.
	skipAuth := method.AuthDisabled || svc.AuthDisabled || svc.IsC2D
	authAnyUser := method.AuthAnyUser

	fullMethodConstant := fmt.Sprintf(`"/%s" + types.%s_%s_FullMethodName`, parentDir, svc.Name, method.Name)
	var inputValidation string
	if validateInput != "" {
		inputValidation = fmt.Sprintf(`
	// Validate request before invoking the handler.
	if err := %s(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("validation failed: %%v", err))
	}
`, validateInput)
	}

	// Build the AccountingMethod field assignment.
	// e.g. grpcName="sentinel", svc.Name="Dashboard", method.Name="GetClusterInfo"
	// → field: "Sentinel" (PascalCase of grpcName)
	// → enum: services_pb.SentinelRPCs_SENTINEL_RPCS_GET_CLUSTER_INFO
	// Skip accounting if grpcName is empty or this method isn't in the enum
	// (e.g., synthesized state service RPCs that share a grpcName).
	hasAccounting := false
	if grpcName != "" {
		if info, ok := accountingMap[grpcName]; ok {
			hasAccounting = info.RPCs[method.Name]
		}
	}

	var accountingMethodBlock, identityBlock string
	if hasAccounting {
		accountingField := strutil.ToPascalCase(grpcName)
		enumTypeName := accountingField + "RPCs"
		enumValueName := strings.ToUpper(strutil.ToSnakeCase(accountingField)) + "_RPCS_" + strings.ToUpper(strutil.ToSnakeCase(method.Name))

		accountingMethodBlock = fmt.Sprintf(`
	accountingMethod := &accounting_method_pb.AccountingMethod{%s: services_pb.%s_%s}`,
			accountingField, enumTypeName, enumValueName)

		identityBlock = `
	var identityMap map[string]string
	if caller != nil {
		payload := caller.Token().Proto().Payload
		identityMap = map[string]string{}
		for _, k := range []string{"sub", "aud", "iss", "azp", "act"} {
			if v, ok := payload[k]; ok && v != "" { identityMap[k] = v }
		}
	}`
	}

	// On an auth_any_user method target_namespace_field is not a request field
	// name but an authored FIXED namespace literal. Unset means no namespace
	// check at all — "any authenticated user can call this RPC". Defaulting it
	// to "/" would gate on IsAtOrBelow(callerNamespace, "/"), which is true only
	// for a caller whose own namespace is exactly "/" — i.e. it would deny every
	// real caller, the inverse of the annotation.
	authAnyUserTarget := strings.TrimSpace(method.TargetNamespaceField)
	authAnyUserCall := func(ctxExpr string) string {
		if authAnyUserTarget == "" {
			return fmt.Sprintf("p.authenticate(%s)", ctxExpr)
		}
		return fmt.Sprintf("p.authenticateAtOrBelow(%s, %q)", ctxExpr, authAnyUserTarget)
	}
	authAnyUserComment := "// Authenticate (auth_any_user: any authenticated caller)"
	if authAnyUserTarget != "" {
		authAnyUserComment = "// Authenticate (auth_any_user: fixed namespace target)"
	}

	// Build optional authorize block
	// Every auth check binds the token it verified to authTok; callerExtractFor
	// below builds the caller from that same token. Reading the identity back out
	// of the context instead would read a key nothing on this path ever writes.
	var authBlock string
	authenticateOnlyBlock := `
authTok, err := p.authenticate(%s)
if err != nil {
	return %s
}`
	if !skipAuth {
		if authAnyUser {
			// auth_any_user: verify token signature + audience, and — only when a
			// fixed target namespace was authored — that the caller is at-or-above it.
			authBlock = fmt.Sprintf(`
authTok, err := %s
if err != nil {
	return %%s
}`, authAnyUserCall("%s"))
		} else {
			authBlock = fmt.Sprintf(`
authTok, err := p.authorize(%%s, %s, %%s)
if err != nil {
	return %%s
}`, fullMethodConstant)
		}
	}

	// Caller extraction block — the caller is the token the auth check that just
	// ran verified (authTok). On a skipAuth method nothing was verified, so there
	// is no caller at all, which is the documented contract ("nil only when auth
	// is disabled").
	// Logs only non-sensitive token identifiers (sub, aud) at DEBUG — never the
	// full claims payload, which carries entitlements and custom claims.
	callerExtractFor := func(ctxExpr string) string {
		if skipAuth {
			return fmt.Sprintf(`
// No caller identity: auth is disabled for this method, so no token was verified.
requestID := servicemanager.RequestIDFromContext(%s)
var caller *servicemanager.Caller
zap.L().Debug("rpc (no token)",
	zap.String("method", %s),
	zap.String("request_id", requestID))`, ctxExpr, fullMethodConstant)
		}
		return fmt.Sprintf(`
// Caller identity is the token the auth check above verified.
requestID := servicemanager.RequestIDFromContext(%s)
var caller *servicemanager.Caller
if authTok != nil {
	caller = servicemanager.NewCaller(authTok, requestID)
	zap.L().Debug("rpc",
		zap.String("method", %s),
		zap.String("request_id", requestID),
		zap.String("sub", authTok.GetSubject()),
		zap.String("aud", authTok.GetAudience()))
} else {
	zap.L().Debug("rpc (no token)",
		zap.String("method", %s),
		zap.String("request_id", requestID))
}`, ctxExpr, fullMethodConstant, fullMethodConstant)
	}

	// Streaming accounting block — emits after handler completes
	var streamAccountingBlock string
	if hasAccounting {
		streamAccountingBlock = `
	if p.emitter != nil {` + accountingMethodBlock + identityBlock + `
		if streamErr != nil {
			code := am.GrpcCodeFromError(streamErr)
			errCtx := am.ErrorContextFromError(streamErr, am.ShouldCaptureStack(code))
			p.emitter.RpcError(stream.Context(), accountingMethod, int32(code), streamErr.Error(), durationMs, errCtx, identityMap, requestID)
		} else {
			p.emitter.RpcCompleted(stream.Context(), accountingMethod, durationMs, identityMap, requestID)
		}
		}`
	}

	var streamInputValidation string
	if validateInput != "" && !handlerEmpty {
		streamInputValidation = fmt.Sprintf(`
	// Validate request before invoking the handler.
	if err := %s(req); err != nil {
		return status.Error(codes.InvalidArgument, fmt.Sprintf("validation failed: %%v", err))
	}`, validateInput)
	}

	serverStreamArg := "stream"
	if validateOutput != "" {
		serverStreamArg = fmt.Sprintf("{{.ServerName}}ValidatingServerStreamingServer[%s]{ServerStreamingServer: stream, validate: %s}", goOutput, validateOutput)
	}

	needsStreamMessageAuth := method.ClientStreaming && method.TargetNamespaceField != "" && !skipAuth && !authAnyUser
	streamMessageAuthorize := "nil"
	if needsStreamMessageAuth {
		streamMessageAuthorize = fmt.Sprintf("func(req *%s) error { _, err := p.authorize(stream.Context(), %s, req); return err }", goInput, fullMethodConstant)
	}

	// (spec.principal)/(spec.namespace) on a request STREAM. The claim is a
	// property of the call and cannot change mid-stream, but the check runs on
	// EVERY inbound message: a request stream is not one request, and message N
	// naming a principal message N-1 did not is the same forgery the unary
	// check refuses. Recv() applies it before the handler ever sees the message.
	streamMessageScope := "nil"
	if method.ClientStreaming && !handlerEmpty {
		if b := goRequestScopeBlocks(san, method, servicePackage, "req", "\t\t", "return"); b != "" {
			streamMessageScope = fmt.Sprintf("func(req *%s) error {\n%s\t\treturn nil\n\t}", goInput, b)
		}
	}

	clientStreamArg := "stream"
	if validateInput != "" || (validateOutput != "" && !emptyOutput) || needsStreamMessageAuth || streamMessageScope != "nil" {
		clientStreamArg = fmt.Sprintf("{{.ServerName}}ValidatingClientStreamingServer[%s, %s]{ClientStreamingServer: stream, authorizeReq: %s, scopeReq: %s, validateReq: %s, validateRes: %s}", goInput, goOutput, streamMessageAuthorize, streamMessageScope, validationFuncOrNil(validateInput), validationFuncOrNil(validateOutput))
	}

	bidiStreamArg := "stream"
	if validateInput != "" || validateOutput != "" || needsStreamMessageAuth || streamMessageScope != "nil" {
		bidiStreamArg = fmt.Sprintf("{{.ServerName}}ValidatingBidiStreamingServer[%s, %s]{BidiStreamingServer: stream, authorizeReq: %s, scopeReq: %s, validateReq: %s, validateRes: %s}", goInput, goOutput, streamMessageAuthorize, streamMessageScope, validationFuncOrNil(validateInput), validationFuncOrNil(validateOutput))
	}

	if method.ClientStreaming && method.ServerStreaming {
		var checks string
		if !skipAuth {
			if authAnyUser {
				checks += fmt.Sprintf(authBlock, "stream.Context()", "err")
			} else if needsStreamMessageAuth {
				checks += fmt.Sprintf(authenticateOnlyBlock, "stream.Context()", "err")
			} else {
				checks += fmt.Sprintf(authBlock, "stream.Context()", "nil", "err")
			}
		}
		callerExtract := callerExtractFor("stream.Context()")

		// The generated MCP session carrier has no handler method to delegate to:
		// the whole session is generated (mcp_dispatch_gen.go). It authorizes and
		// extracts its caller exactly as any other bidi rpc — the carrier IS an
		// ordinary rpc with an ordinary auth model — and then hands the validated
		// stream to the generated session server.
		handlerCall := fmt.Sprintf("p.handler.%s(caller, %s)", method.Name, bidiStreamArg)
		if goIsMcpCarrier(svc, method) {
			handlerCall = fmt.Sprintf("serve%sMcpSession(stream.Context(), p.handler, caller, %s)", svc.Name, bidiStreamArg)
			// Fail closed: every server-computed predicate in the tool surface
			// comes from this caller, so a session with no verified identity
			// would run its tools unscoped.
			callerExtract += `
	if caller == nil {
		return status.Error(codes.Unauthenticated, "an MCP session requires an authenticated caller")
	}
`
		}

		if hasAccounting {
			return fmt.Sprintf(`// %s implements %s.%s (bidirectional streaming)
	func (p *{{.ServerName}}) %s(stream grpc.BidiStreamingServer[%s, %s]) error {%s%s
		start := time.Now()
		streamErr := %s
		durationMs := time.Since(start).Milliseconds()
	%s
		return streamErr
	}`, method.Name, svc.Name, method.Name, method.Name, goInput, goOutput, checks, callerExtract, handlerCall, streamAccountingBlock)
		}
		return fmt.Sprintf(`// %s implements %s.%s (bidirectional streaming)
	func (p *{{.ServerName}}) %s(stream grpc.BidiStreamingServer[%s, %s]) error {%s%s
		return %s
	}`, method.Name, svc.Name, method.Name, method.Name, goInput, goOutput, checks, callerExtract, handlerCall)
	} else if isTier2SnapshotCollection(method) {
		// Tier-2 SNAPSHOT collection: the wire is still server-streaming, but the
		// handler returns a materialized []*Item; the wrapper chunks it into
		// bounded stream sends (gRPC-go's Send applies flow control). Output
		// validation is preserved by sending through serverStreamArg.
		var checks string
		if !skipAuth {
			if authAnyUser {
				checks += fmt.Sprintf(authBlock, "stream.Context()", "err")
			} else {
				checks += fmt.Sprintf(authBlock, "stream.Context()", "req", "err")
			}
		}
		callerExtract := callerExtractFor("stream.Context()")
		callerExtract += goRequestScopeBlocks(san, method, servicePackage, "req", "\t", "return")
		handlerSlice := fmt.Sprintf("p.handler.%s(caller, req)", method.Name)
		if handlerEmpty {
			handlerSlice = fmt.Sprintf("p.handler.%s(caller)", method.Name)
		}
		// Egress data entitlement: sanitize each materialized item after the handler
		// returns (fresh slice — never mutate the handler's backing array).
		var tier2San string
		if t, ok := san.sanitizableType(method.FullOutputType, servicePackage); ok {
			callerExtract += egressSanitizePreamble([]sanTarget{t})
			tier2San = "\n" + egressSanitizeSliceStmt(t, "items", goOutput, "\t\t")
		}
		if hasAccounting {
			return fmt.Sprintf(`// %s implements %s.%s (Tier-2 SNAPSHOT collection over server streaming)
	func (p *{{.ServerName}}) %s(req *%s, stream grpc.ServerStreamingServer[%s]) error {%s%s%s
		start := time.Now()
		streamErr := func() error {
			items, err := %s
			if err != nil {
				return err
			}%s
			sendStream := %s
			for _, item := range items {
				if err := sendStream.Send(item); err != nil {
					return err
				}
			}
			return nil
		}()
		durationMs := time.Since(start).Milliseconds()
	%s
		return streamErr
	}`, method.Name, svc.Name, method.Name, method.Name, goInput, goOutput, checks, callerExtract, streamInputValidation, handlerSlice, tier2San, serverStreamArg, streamAccountingBlock)
		}
		return fmt.Sprintf(`// %s implements %s.%s (Tier-2 SNAPSHOT collection over server streaming)
	func (p *{{.ServerName}}) %s(req *%s, stream grpc.ServerStreamingServer[%s]) error {%s%s%s
		items, err := %s
		if err != nil {
			return err
		}%s
		sendStream := %s
		for _, item := range items {
			if err := sendStream.Send(item); err != nil {
				return err
			}
		}
		return nil
	}`, method.Name, svc.Name, method.Name, method.Name, goInput, goOutput, checks, callerExtract, streamInputValidation, handlerSlice, tier2San, serverStreamArg)
	} else if method.IsWatchCollection() {
		// WATCH: handler returns a WatchSource (snapshot + change feed). The wrapper
		// owns the {Method}WatchEvent envelope: send snapshot_item* then
		// snapshot_done=R (when from_revision==0), then deltas after R via the shared
		// per-scope replay ring (Hub). A resume point behind the ring yields
		// collection.ErrRelist (OUT_OF_RANGE). Teardown via stream-context cancel.
		var checks string
		if !skipAuth {
			if authAnyUser {
				checks += fmt.Sprintf(authBlock, "stream.Context()", "err")
			} else {
				checks += fmt.Sprintf(authBlock, "stream.Context()", "req", "err")
			}
		}
		callerExtract := callerExtractFor("stream.Context()")
		callerExtract += goRequestScopeBlocks(san, method, servicePackage, "req", "\t", "return")
		oneof := strutil.ToPascalCase(method.Resolved.WatchOneOf)
		change := goWatchType(method.Resolved.WatchChangeType, servicePackage)
		hubVar := "{{.ServerName}}WatchHub" + method.Name
		sendWatchEvent := fmt.Sprintf(`
	sendWatchEvent := func(event *%s) error {
		return stream.Send(event)
	}`, goOutput)
		if validateOutput != "" {
			sendWatchEvent = fmt.Sprintf(`
	sendWatchEvent := func(event *%s) error {
		if err := %s(event); err != nil {
			return status.Error(codes.Internal, fmt.Sprintf("response validation failed: %%v", err))
		}
		return stream.Send(event)
	}`, goOutput, validateOutput)
		}
		snapshotSend := fmt.Sprintf("sendWatchEvent(&%s{%s: &%s_SnapshotItem{SnapshotItem: item}})", goOutput, oneof, goOutput)
		doneSend := fmt.Sprintf("sendWatchEvent(&%s{%s: &%s_SnapshotDone{SnapshotDone: src.Revision}})", goOutput, oneof, goOutput)
		deltaSend := fmt.Sprintf("sendWatchEvent(&%s{%s: &%s_Delta{Delta: change}})", goOutput, oneof, goOutput)
		watchCall := fmt.Sprintf("%s.Watch(src.Scope, resumeRev, src.Changes, func(c *%s) int64 { return c.Get%s() })",
			hubVar, change, toGoFieldName(method.Resolved.WatchRevisionField))
		// Egress data entitlement: sanitize each snapshot item AND each delta before
		// send. A sanitizable item/change is sanitized whole (Sanitize<T> clones); an
		// injected envelope (e.g. <T>WatchChange) is cloned first — delta envelopes
		// come from the shared replay ring and must never be mutated in place — then
		// its record-bearing fields are sanitized.
		itemGoType := goWatchType(method.Resolved.WatchItemType, servicePackage)
		var watchTargets []sanTarget
		var itemSan, changeSan string
		if t, ok := san.sanitizableType(method.Resolved.WatchItemType, servicePackage); ok {
			watchTargets = append(watchTargets, t)
			itemSan = "\t\t\titem = " + t.sanitizeExpr("item") + "\n"
		} else if fs := san.envelopeSanFields(method.Resolved.WatchItemType, servicePackage); len(fs) > 0 {
			for _, f := range fs {
				watchTargets = append(watchTargets, f.Target)
			}
			itemSan = "\t\t\titem = proto.Clone(item).(*" + itemGoType + ")\n" + egressSanitizeEnvelopeStmts(fs, "item", "\t\t\t")
		}
		if t, ok := san.sanitizableType(method.Resolved.WatchChangeType, servicePackage); ok {
			watchTargets = append(watchTargets, t)
			changeSan = "\t\tchange = " + t.sanitizeExpr("change") + "\n"
		} else if fs := san.envelopeSanFields(method.Resolved.WatchChangeType, servicePackage); len(fs) > 0 {
			for _, f := range fs {
				watchTargets = append(watchTargets, f.Target)
			}
			changeSan = "\t\tchange = proto.Clone(change).(*" + change + ")\n" + egressSanitizeEnvelopeStmts(fs, "change", "\t\t")
		}
		if len(watchTargets) > 0 {
			callerExtract += egressSanitizePreamble(watchTargets)
		}
		if hasAccounting {
			return fmt.Sprintf(`var %s = collection.NewHub[%s](%d, %d)

// %s implements %s.%s (WATCH collection over server streaming)
func (p *{{.ServerName}}) %s(req *%s, stream grpc.ServerStreamingServer[%s]) error {%s%s%s
	start := time.Now()
	var streamErr error
	recordErr := func(err error) error {
		streamErr = err
		return err
	}
	defer func() {
		durationMs := time.Since(start).Milliseconds()
	%s
	}()
	src, err := p.handler.%s(caller, req)
	if err != nil {
		return recordErr(err)
	}
%s
	ctx := stream.Context()
	fromRev := req.GetFromRevision()
	resumeRev := fromRev
	if fromRev == 0 {
		for item, serr := range src.Snapshot {
			if serr != nil {
				return recordErr(serr)
			}
			if ctx.Err() != nil {
				return recordErr(ctx.Err())
			}
%s			if e := %s; e != nil {
				return recordErr(e)
			}
		}
		if e := %s; e != nil {
			return recordErr(e)
		}
		resumeRev = src.Revision
	}
	for change, cerr := range %s {
		if cerr != nil {
			return recordErr(cerr)
		}
		if ctx.Err() != nil {
			return recordErr(ctx.Err())
		}
%s		if e := %s; e != nil {
			return recordErr(e)
		}
	}
	return nil
}`, hubVar, change, method.Resolved.ReplayDepth, method.Resolved.IdleSecs, method.Name, svc.Name, method.Name, method.Name, goInput, goOutput, checks, callerExtract, streamInputValidation, streamAccountingBlock, method.Name, sendWatchEvent, itemSan, snapshotSend, doneSend, watchCall, changeSan, deltaSend)
		}
		return fmt.Sprintf(`var %s = collection.NewHub[%s](%d, %d)

// %s implements %s.%s (WATCH collection over server streaming)
func (p *{{.ServerName}}) %s(req *%s, stream grpc.ServerStreamingServer[%s]) error {%s%s%s
	src, err := p.handler.%s(caller, req)
	if err != nil {
		return err
	}
%s
	ctx := stream.Context()
	fromRev := req.GetFromRevision()
	resumeRev := fromRev
	if fromRev == 0 {
		for item, serr := range src.Snapshot {
			if serr != nil {
				return serr
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
%s			if e := %s; e != nil {
				return e
			}
		}
		if e := %s; e != nil {
			return e
		}
		resumeRev = src.Revision
	}
	for change, cerr := range %s {
		if cerr != nil {
			return cerr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
%s		if e := %s; e != nil {
			return e
		}
	}
	return nil
}`, hubVar, change, method.Resolved.ReplayDepth, method.Resolved.IdleSecs, method.Name, svc.Name, method.Name, method.Name, goInput, goOutput, checks, callerExtract, streamInputValidation, method.Name, sendWatchEvent, itemSan, snapshotSend, doneSend, watchCall, changeSan, deltaSend)
	} else if method.IsTier3SnapshotCollection() {
		// Tier-3 SNAPSHOT cursor: handler returns a lazy iter.Seq2 producer; drive
		// it, sending each item (gRPC-go Send applies flow control). On client
		// cancel or send error the range-func returns false → the producer's
		// cleanup runs (deterministic teardown).
		var checks string
		if !skipAuth {
			if authAnyUser {
				checks += fmt.Sprintf(authBlock, "stream.Context()", "err")
			} else {
				checks += fmt.Sprintf(authBlock, "stream.Context()", "req", "err")
			}
		}
		callerExtract := callerExtractFor("stream.Context()")
		callerExtract += goRequestScopeBlocks(san, method, servicePackage, "req", "\t", "return")
		handlerSeq := fmt.Sprintf("p.handler.%s(caller, req)", method.Name)
		if handlerEmpty {
			handlerSeq = fmt.Sprintf("p.handler.%s(caller)", method.Name)
		}
		// Egress data entitlement: sanitize each lazily-pulled item before send
		// (Sanitize<T> clones — the producer's records are never mutated).
		var tier3San string
		if t, ok := san.sanitizableType(method.FullOutputType, servicePackage); ok {
			callerExtract += egressSanitizePreamble([]sanTarget{t})
			tier3San = "\n\t\t\titem = " + t.sanitizeExpr("item")
		} else if fs := san.envelopeSanFields(method.FullOutputType, servicePackage); len(fs) > 0 {
			var targets []sanTarget
			for _, f := range fs {
				targets = append(targets, f.Target)
			}
			callerExtract += egressSanitizePreamble(targets)
			tier3San = "\n\t\t\titem = proto.Clone(item).(*" + goOutput + ")\n" + egressSanitizeEnvelopeStmts(fs, "item", "\t\t\t")
		}
		drive := fmt.Sprintf(`seq(func(item *%s, err error) bool {
			if err != nil {
				sendErr = err
				return false
			}
			if ctx.Err() != nil {
				sendErr = ctx.Err()
				return false
			}%s
			if e := sendStream.Send(item); e != nil {
				sendErr = e
				return false
			}
			return true
		})`, goOutput, tier3San)
		if hasAccounting {
			return fmt.Sprintf(`// %s implements %s.%s (Tier-3 SNAPSHOT cursor over server streaming)
	func (p *{{.ServerName}}) %s(req *%s, stream grpc.ServerStreamingServer[%s]) error {%s%s%s
		start := time.Now()
		streamErr := func() error {
			seq, err := %s
			if err != nil {
				return err
			}
			ctx := stream.Context()
			sendStream := %s
			var sendErr error
			%s
			return sendErr
		}()
		durationMs := time.Since(start).Milliseconds()
	%s
		return streamErr
	}`, method.Name, svc.Name, method.Name, method.Name, goInput, goOutput, checks, callerExtract, streamInputValidation, handlerSeq, serverStreamArg, drive, streamAccountingBlock)
		}
		return fmt.Sprintf(`// %s implements %s.%s (Tier-3 SNAPSHOT cursor over server streaming)
	func (p *{{.ServerName}}) %s(req *%s, stream grpc.ServerStreamingServer[%s]) error {%s%s%s
		seq, err := %s
		if err != nil {
			return err
		}
		ctx := stream.Context()
		sendStream := %s
		var sendErr error
		%s
		return sendErr
	}`, method.Name, svc.Name, method.Name, method.Name, goInput, goOutput, checks, callerExtract, streamInputValidation, handlerSeq, serverStreamArg, drive)
	} else if method.ServerStreaming {
		var checks string
		if !skipAuth {
			if authAnyUser {
				checks += fmt.Sprintf(authBlock, "stream.Context()", "err")
			} else {
				checks += fmt.Sprintf(authBlock, "stream.Context()", "req", "err")
			}
		}
		callerExtract := callerExtractFor("stream.Context()")
		callerExtract += goRequestScopeBlocks(san, method, servicePackage, "req", "\t", "return")
		handlerCall := fmt.Sprintf("p.handler.%s(caller, req, %s)", method.Name, serverStreamArg)
		if handlerEmpty {
			handlerCall = fmt.Sprintf("p.handler.%s(caller, %s)", method.Name, serverStreamArg)
		}
		if hasAccounting {
			return fmt.Sprintf(`// %s implements %s.%s (server streaming)
	func (p *{{.ServerName}}) %s(req *%s, stream grpc.ServerStreamingServer[%s]) error {%s%s%s
		start := time.Now()
		streamErr := %s
		durationMs := time.Since(start).Milliseconds()
	%s
		return streamErr
	}`, method.Name, svc.Name, method.Name, method.Name, goInput, goOutput, checks, callerExtract, streamInputValidation, handlerCall, streamAccountingBlock)
		}
		return fmt.Sprintf(`// %s implements %s.%s (server streaming)
	func (p *{{.ServerName}}) %s(req *%s, stream grpc.ServerStreamingServer[%s]) error {%s%s%s
		return %s
	}`, method.Name, svc.Name, method.Name, method.Name, goInput, goOutput, checks, callerExtract, streamInputValidation, handlerCall)
	} else if method.ClientStreaming {
		var checks string
		if !skipAuth {
			if authAnyUser {
				checks += fmt.Sprintf(authBlock, "stream.Context()", "err")
			} else if needsStreamMessageAuth {
				checks += fmt.Sprintf(authenticateOnlyBlock, "stream.Context()", "err")
			} else {
				checks += fmt.Sprintf(authBlock, "stream.Context()", "nil", "err")
			}
		}
		callerExtract := callerExtractFor("stream.Context()")
		if hasAccounting {
			return fmt.Sprintf(`// %s implements %s.%s (client streaming)
	func (p *{{.ServerName}}) %s(stream grpc.ClientStreamingServer[%s, %s]) error {%s%s
		start := time.Now()
		streamErr := p.handler.%s(caller, %s)
		durationMs := time.Since(start).Milliseconds()
	%s
		return streamErr
	}`, method.Name, svc.Name, method.Name, method.Name, goInput, goOutput, checks, callerExtract, method.Name, clientStreamArg, streamAccountingBlock)
		}
		return fmt.Sprintf(`// %s implements %s.%s (client streaming)
	func (p *{{.ServerName}}) %s(stream grpc.ClientStreamingServer[%s, %s]) error {%s%s
		return p.handler.%s(caller, %s)
	}`, method.Name, svc.Name, method.Name, method.Name, goInput, goOutput, checks, callerExtract, method.Name, clientStreamArg)
	} else if method.IsPageCollection() {
		// PAGE: a unary RPC carrying a client-driven window. The wrapper owns the
		// page_token codec and the trust-tier page_size bound (the runtime row cap
		// SNAPSHOT lacks); the handler is a keyset producer returning a
		// collection.Page. A malformed page_token is INVALID_ARGUMENT — never a
		// silent reset to page 1 (that would skip rows and read as success).
		itemsField := strutil.ToPascalCase(method.Resolved.PageItemsField)

		var pageAuth string
		if !skipAuth {
			if authAnyUser {
				pageAuth = fmt.Sprintf(`
	%s
	authTok, err := %s
	if err != nil {
		return nil, err
	}
`, authAnyUserComment, authAnyUserCall("ctx"))
			} else {
				pageAuth = fmt.Sprintf(`
	// Authorize
	authTok, err := p.authorize(ctx, %s, req)
	if err != nil {
		return nil, err
	}
`, fullMethodConstant)
			}
		}

		callerExtract := callerExtractFor("ctx")
		callerExtract += goRequestScopeBlocks(san, method, servicePackage, "req", "\t", "return nil,")

		var inputValidation string
		if validateInput != "" {
			inputValidation = fmt.Sprintf(`
	// Validate request before decoding the page token or invoking the handler.
	if err := %s(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("validation failed: %%v", err))
	}
`, validateInput)
		}

		handlerCall := fmt.Sprintf("p.handler.%s(sanitizeContext(ctx), caller, req, collection.PageParams{Limit: limit, After: after})", method.Name)
		if handlerEmpty {
			handlerCall = fmt.Sprintf("p.handler.%s(sanitizeContext(ctx), caller, collection.PageParams{Limit: limit, After: after})", method.Name)
		}

		// Egress data entitlement: sanitize every page item after the handler
		// returns (fresh slice — never mutate the handler's backing array).
		var pageSan string
		if t, ok := san.sanitizableType(method.Resolved.PageItemType, servicePackage); ok {
			pageItemGoType := goWatchType(method.Resolved.PageItemType, servicePackage)
			callerExtract += egressSanitizePreamble([]sanTarget{t})
			pageSan = "\n" + egressSanitizeSliceStmt(t, "page.Items", pageItemGoType, "\t")
		}

		totalBlock := ""
		if method.Resolved.PageHasTotal {
			totalBlock = `
	// total_count is reported only when the handler computes it (Total >= 0);
	// a negative Total leaves total_count at 0 = "unknown", not "empty".
	if page.Total >= 0 {
		resp.TotalCount = page.Total
	}`
		}

		var outputValidation string
		if validateOutput != "" {
			outputValidation = fmt.Sprintf(`
	// Validate response before returning it to gRPC.
	if err := %s(resp); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("response validation failed: %%v", err))
	}
`, validateOutput)
		}

		return fmt.Sprintf(`// %s implements %s.%s (PAGE collection — keyset pagination)
func (p *{{.ServerName}}) %s(ctx context.Context, req *%s) (*%s, error) {%s%s%s
	// Decode the opaque page_token into a keyset cursor (bad token -> INVALID_ARGUMENT).
	after, ptErr := collection.DecodePageToken(req.GetPageToken())
	if ptErr != nil {
		return nil, ptErr
	}
	// Resolve the effective page size: default when unset, clamped to the trust ceiling.
	limit := collection.EffectivePageSize(int(req.GetPageSize()), %d, %d)
	page, err := %s
	if err != nil {
		return nil, err
	}%s
	// Encode the next cursor; an empty token signals the last page to the auto-pager.
	nextPageToken, encErr := collection.EncodePageToken(page.NextAfter)
	if encErr != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("page token encode failed: %%v", encErr))
	}
	resp := &%s{
		%s:           page.Items,
		NextPageToken: nextPageToken,
	}%s
%s
	return resp, nil
}`, method.Name, svc.Name, method.Name,
			method.Name, goInput, goOutput, pageAuth, callerExtract, inputValidation,
			method.Resolved.DefaultPageSize, method.Resolved.MaxPageSize, handlerCall, pageSan,
			goOutput, itemsField, totalBlock, outputValidation)
	}

	// Unary method with validation

	// Trace v2: event-based tracing with Herald-issued trace_id.
	// A non-empty trace_id publishes a TraceEvent at this hop. If noop=true,
	// the handler is skipped. If noop=false (default), auth and handler run normally.
	var unaryAuth string
	noopEarlyReturn := fmt.Sprintf(`return &%s{}, nil`, goOutput)
	if emptyOutput {
		noopEarlyReturn = `return &emptypb.Empty{}, nil`
	}

	// Build typed trace rpc block using AccountingMethod if available.
	var traceRpcBlock, traceRpcResponseBlock string
	if hasAccounting {
		accountingField := strutil.ToPascalCase(grpcName)
		enumTypeName := accountingField + "RPCs"
		enumValueName := strings.ToUpper(strutil.ToSnakeCase(accountingField)) + "_RPCS_" + strings.ToUpper(strutil.ToSnakeCase(method.Name))
		traceRpcBlock = fmt.Sprintf("Rpc: &accounting_method_pb.AccountingMethod{%s: services_pb.%s_%s},\n\t\t\tKind: trace_pb.TraceEventKind_TRACE_EVENT_KIND_INTERNAL_REQUEST,", accountingField, enumTypeName, enumValueName)
		traceRpcResponseBlock = fmt.Sprintf("Rpc: &accounting_method_pb.AccountingMethod{%s: services_pb.%s_%s},\n\t\t\tKind: trace_pb.TraceEventKind_TRACE_EVENT_KIND_INTERNAL_RESPONSE,", accountingField, enumTypeName, enumValueName)
	} else {
		traceRpcBlock = "Kind: trace_pb.TraceEventKind_TRACE_EVENT_KIND_INTERNAL_REQUEST,"
		traceRpcResponseBlock = "Kind: trace_pb.TraceEventKind_TRACE_EVENT_KIND_INTERNAL_RESPONSE,"
	}

	if inputHasTrace && !skipAuth {
		traceInputValidation := inputValidation
		inputValidation = ""
		var innerAuth string
		if authAnyUser {
			innerAuth = fmt.Sprintf(`
		authTok, err := %s
		if err != nil {
			return nil, err
		}`, authAnyUserCall("ctx"))
		} else {
			innerAuth = fmt.Sprintf(`
		authTok, err := p.authorize(ctx, %s, req)
		if err != nil {
			return nil, err
		}`, fullMethodConstant)
		}
		unaryAuth = fmt.Sprintf(`
	// Trace v2: publish trace event, noop skips handler but not auth/validation
	isTracing := req.TraceId != ""
	isNoop := isTracing && req.Noop
%s%s
	if isTracing {
		round := p.traceRound()
		p.publishTrace(ctx, &trace_pb.TraceEvent{
			TraceId: req.TraceId,
			Round:   round,
			%s
		})
		if isNoop {
			%s
		}
	}
`, innerAuth, traceInputValidation, traceRpcBlock, noopEarlyReturn)
	} else if inputHasTrace {
		traceInputValidation := inputValidation
		inputValidation = ""
		// Auth already disabled by annotation, but still handle trace
		unaryAuth = fmt.Sprintf(`
	// Trace v2: publish trace event, noop skips handler but not validation
	isTracing := req.TraceId != ""
%s
	if isTracing {
		round := p.traceRound()
		p.publishTrace(ctx, &trace_pb.TraceEvent{
			TraceId: req.TraceId,
			Round:   round,
			%s
		})
		if req.Noop {
			%s
		}
	}
`, traceInputValidation, traceRpcBlock, noopEarlyReturn)
	} else if !skipAuth {
		if authAnyUser {
			unaryAuth = fmt.Sprintf(`
	%s
	authTok, err := %s
	if err != nil {
		return nil, err
	}
`, authAnyUserComment, authAnyUserCall("ctx"))
		} else {
			unaryAuth = fmt.Sprintf(`
	// Authorize
	authTok, err := p.authorize(ctx, %s, req)
	if err != nil {
		return nil, err
	}
`, fullMethodConstant)
		}
	}

	callerExtract := callerExtractFor("ctx")
	callerExtract += goRequestScopeBlocks(san, method, servicePackage, "req", "\t", "return nil,")

	// Egress data entitlement for unary responses (e.g. RecordMap Sample<T>): a
	// sanitizable response type is sanitized whole; an injected envelope
	// (Sample<T>Response) has each record-bearing field sanitized instead
	// (repeated fields via a fresh slice — the handler's items may alias stored
	// records). Applied after the handler returns, before response validation.
	var unarySan string
	if !emptyOutput {
		if t, ok := san.sanitizableType(method.FullOutputType, servicePackage); ok {
			callerExtract += egressSanitizePreamble([]sanTarget{t})
			unarySan = "\tresp = " + t.sanitizeExpr("resp") + "\n"
		} else if fs := san.envelopeSanFields(method.FullOutputType, servicePackage); len(fs) > 0 {
			var targets []sanTarget
			for _, f := range fs {
				targets = append(targets, f.Target)
			}
			callerExtract += egressSanitizePreamble(targets)
			unarySan = egressSanitizeEnvelopeStmts(fs, "resp", "\t")
		}
	}

	// Handler call — sanitize the context and omit Empty/TraceRequest request
	handlerCall := fmt.Sprintf("p.handler.%s(sanitizeContext(ctx), caller, req)", method.Name)
	if handlerEmpty {
		handlerCall = fmt.Sprintf("p.handler.%s(sanitizeContext(ctx), caller)", method.Name)
	}

	// Publish INTERNAL_RESPONSE after the handler returns (when tracing).
	var traceResponseCopy string
	if inputHasTrace && !emptyInput {
		traceResponseCopy = fmt.Sprintf(`
	if isTracing {
		round := p.traceRound()
		p.publishTrace(ctx, &trace_pb.TraceEvent{
			TraceId: req.TraceId,
			Round:   round,
			%s
		})
	}
`, traceRpcResponseBlock)
	}

	var traceErrorCopy string
	if inputHasTrace && !emptyInput {
		traceErrorCopy = fmt.Sprintf(`
		if isTracing {
			round := p.traceRound()
			p.publishTrace(ctx, &trace_pb.TraceEvent{
				TraceId: req.TraceId,
				Round:   round,
				Error:   err.Error(),
				%s
			})
		}`, traceRpcResponseBlock)
	}

	var outputValidation string
	if validateOutput != "" && !emptyOutput {
		outputValidation = fmt.Sprintf(`
	// Validate response before returning it to gRPC.
	if validationErr := %s(resp); validationErr != nil {
		err := status.Error(codes.Internal, fmt.Sprintf("response validation failed: %%v", validationErr))%s
		return nil, err
	}
`, validateOutput, traceErrorCopy)
	}

	// When output is Empty, handler returns error only — wrapper constructs the Empty response
	if emptyOutput {
		if hasAccounting {
			errorAccountingBlock := `
		if p.emitter != nil {` + accountingMethodBlock + identityBlock + `
			code := am.GrpcCodeFromError(err)
			errCtx := am.ErrorContextFromError(err, am.ShouldCaptureStack(code))
			p.emitter.RpcError(ctx, accountingMethod, int32(code), err.Error(), durationMs, errCtx, identityMap, requestID)
		}`
			successAccountingBlock := `
	if p.emitter != nil {` + accountingMethodBlock + identityBlock + `
		p.emitter.RpcCompleted(ctx, accountingMethod, durationMs, identityMap, requestID)
	}`

			return fmt.Sprintf(`// %s implements %s.%s with authorization and optional validation
func (p *{{.ServerName}}) %s(ctx context.Context, req *%s) (*%s, error) {%s%s%s
	// Call the handler
	start := time.Now()
	err := %s
	durationMs := time.Since(start).Milliseconds()
	if err != nil {%s
%s
		return nil, err
	}
%s%s
	return &emptypb.Empty{}, nil
}`, method.Name, svc.Name, method.Name, method.Name, goInput, goOutput, unaryAuth, callerExtract, inputValidation, handlerCall, errorAccountingBlock, traceErrorCopy, traceResponseCopy, successAccountingBlock)
		}

		return fmt.Sprintf(`// %s implements %s.%s with authorization and optional validation
func (p *{{.ServerName}}) %s(ctx context.Context, req *%s) (*%s, error) {%s%s%s
	// Call the handler
	if err := %s; err != nil {%s
		return nil, err
	}
%s
	return &emptypb.Empty{}, nil
}`, method.Name, svc.Name, method.Name, method.Name, goInput, goOutput, unaryAuth, callerExtract, inputValidation, handlerCall, traceErrorCopy, traceResponseCopy)
	}

	if hasAccounting {
		errorAccountingBlock := `
			if p.emitter != nil {` + accountingMethodBlock + identityBlock + `
				code := am.GrpcCodeFromError(err)
				errCtx := am.ErrorContextFromError(err, am.ShouldCaptureStack(code))
				p.emitter.RpcError(ctx, accountingMethod, int32(code), err.Error(), durationMs, errCtx, identityMap, requestID)
			}`
		successAccountingBlock := `
		if p.emitter != nil {` + accountingMethodBlock + identityBlock + `
			p.emitter.RpcCompleted(ctx, accountingMethod, durationMs, identityMap, requestID)
		}`
		outputValidationAccounting := ""
		if validateOutput != "" && !emptyOutput {
			outputValidationAccounting = fmt.Sprintf(`
		// Validate response before returning it to gRPC.
		if validationErr := %s(resp); validationErr != nil {
			err := status.Error(codes.Internal, fmt.Sprintf("response validation failed: %%v", validationErr))
			durationMs := time.Since(start).Milliseconds()%s
%s
			return nil, err
		}
`, validateOutput, errorAccountingBlock, traceErrorCopy)
		}

		return fmt.Sprintf(`// %s implements %s.%s with authorization and optional validation
func (p *{{.ServerName}}) %s(ctx context.Context, req *%s) (*%s, error) {%s%s%s
	// Call the handler
	start := time.Now()
	resp, err := %s
	if err != nil {
		durationMs := time.Since(start).Milliseconds()%s
%s
		return nil, err
	}
%s	%s
	durationMs := time.Since(start).Milliseconds()
	%s%s
	return resp, nil
}`, method.Name, svc.Name, method.Name, method.Name, goInput, goOutput, unaryAuth, callerExtract, inputValidation, handlerCall, errorAccountingBlock, traceErrorCopy, unarySan, outputValidationAccounting, traceResponseCopy, successAccountingBlock)
	}

	return fmt.Sprintf(`// %s implements %s.%s with authorization and optional validation
func (p *{{.ServerName}}) %s(ctx context.Context, req *%s) (*%s, error) {%s%s%s
	// Call the handler
	resp, err := %s
	if err != nil {%s
		return nil, err
	}
%s%s%s
	return resp, nil
}`, method.Name, svc.Name, method.Name, method.Name, goInput, goOutput, unaryAuth, callerExtract, inputValidation, handlerCall, traceErrorCopy, unarySan, outputValidation, traceResponseCopy)
}

// goStoreEpochField returns the name of fullType's (spec.store_epoch) field, or
// "" when it declares none. The Go wrapper never STAMPS it — the stamp is
// generated for Java service wrappers only — so this exists purely so a Go host
// of an epoch-bearing write fails generation instead of writing an unstamped row.
func goStoreEpochField(fullType string, san *egressSanitizer, servicePackage string) string {
	if san == nil || fullType == "" {
		return ""
	}
	pkg, name := splitEgressFQN(fullType, servicePackage)
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

// goAppendOnlyRecordOnMethod returns the append-only record type a row-mutating
// method carries, or "" when the method is not one. Same definition of
// "row-mutating" the parser and the Java wrapper use (parser.IsRowMutatingPattern),
// so the three ends cannot drift apart.
func goAppendOnlyRecordOnMethod(method *parser.MethodInfo, san *egressSanitizer, servicePackage string) string {
	if san == nil || !parser.IsRowMutatingPattern(method.Class) {
		return ""
	}
	for _, t := range []string{method.FullInputType, method.FullOutputType} {
		if t == "" {
			continue
		}
		pkg, name := splitEgressFQN(t, servicePackage)
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

// goPinnedEgressRows names the caller-pinned rows a method returns: the
// annotation ("store_namespace" or "store_principal") and the egress type
// carrying it, or "", "" when the method returns none. The Go wrapper renders
// NO egress row filter on any of its seams — the filter is generated for Java
// and Rust service wrappers only — so this exists purely so a Go host of a
// record-bearing read fails generation instead of returning every tenant's
// rows. It answers off the same index helpers the generated MCP dispatch
// filters with: mcpRowFields for repeated rows, annotatedStringField for a
// row-shaped type.
func goPinnedEgressRows(method *parser.MethodInfo, san *egressSanitizer, servicePackage string) (string, string) {
	if san == nil {
		return "", ""
	}
	// Resolved is the computed COLLECTION view and is nil for a method that
	// declares no collection at all, so the page/watch types are reachable only
	// through it.
	egressTypes := []string{method.FullOutputType}
	if method.Resolved != nil {
		egressTypes = append(egressTypes, method.Resolved.PageItemType, method.Resolved.WatchItemType, method.Resolved.WatchChangeType)
	}
	for _, t := range egressTypes {
		if t == "" {
			continue
		}
		for _, annotation := range []string{"store_namespace", "store_principal"} {
			if len(san.mcpRowFields(t, servicePackage, annotation)) > 0 || san.annotatedStringField(t, servicePackage, annotation) != "" {
				return annotation, t
			}
		}
	}
	return "", ""
}

// qualifiedMethodAuthKey builds the SERVICE-QUALIFIED method identifier that
// keys the Authorizer's per-method policy maps: "pkg.Service/Method", the last
// two segments of the full gRPC method path the wrapper passes to Authorize.
// It must stay in lockstep with qualifiedMethodKey in the grpcserver chassis
// (pkg/templates/go/chassis/grpcserver/authorizer.go.tmpl).
func qualifiedMethodAuthKey(protoPackage, serviceName, methodName string) string {
	if protoPackage == "" {
		return serviceName + "/" + methodName
	}
	return protoPackage + "." + serviceName + "/" + methodName
}
