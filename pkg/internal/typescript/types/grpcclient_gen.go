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
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/consts"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// generateGrpcClients generates typed gRPC client wrappers for each gRPC service.
func generateGrpcClients(servicesInfo []ServiceInfo, inputDir string) error {
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	for _, svc := range servicesInfo {
		protoInfo, err := descriptorParser.ConvertToProtoInfo(svc.ProtoFile)
		if err != nil {
			return fmt.Errorf("failed to parse %s: %w", svc.ProtoFile, err)
		}
		fileDesc, err := descriptorParser.GetFileDescriptor(svc.ProtoFile)
		if err != nil {
			return fmt.Errorf("failed to get descriptor for %s: %w", svc.ProtoFile, err)
		}

		if err := generateGrpcClient(svc, protoInfo, fileDesc); err != nil {
			return fmt.Errorf("failed to generate gRPC client for %s: %w", svc.ServiceName, err)
		}
	}

	// Generate aggregator files
	if len(servicesInfo) > 0 {
		if err := generateTsGrpcClientAggregators(servicesInfo); err != nil {
			return fmt.Errorf("failed to generate gRPC client aggregators: %w", err)
		}
	}

	return nil
}

// resolveTarget determines the physical service ID for routing.
// Priority: TargetService from the active client surface > HostTag > PackageName.
func resolveTarget(svc ServiceInfo) string {
	config := svc.Config
	target := svc.PackageName
	if svc.HostTag != "" {
		target = svc.HostTag
	}
	for _, entry := range tsGrpcTargetEntries(config) {
		if tsGrpcEntryMatchesService(entry, svc) && entry.TargetService != "" {
			target = entry.TargetService
			break
		}
	}
	return target
}

func tsGrpcTargetEntries(config *tactype.Config) []tactype.GrpcEntry {
	switch config.GetSmPrefix() {
	case "c2sm":
		return config.GrpcC2SClients
	case "c2dm":
		return config.GrpcC2DClients
	case "w2sm":
		return config.GrpcW2SClients
	default:
		return config.GrpcS2SClients
	}
}

func tsGrpcEntryMatchesService(entry tactype.GrpcEntry, svc ServiceInfo) bool {
	if entry.ProtoPackage != "" {
		return entry.ProtoPackage == svc.PackageName
	}
	if entry.ProtoFile == svc.ProtoFile {
		return true
	}
	return strings.TrimSuffix(filepath.Base(entry.ProtoFile), ".proto") == svc.PackageName
}

// generateTsGrpcClientAggregators creates a single grpc_clients.ts with per-target
// client groups and a root client aggregator that takes per-target ChannelProviders.
func generateTsGrpcClientAggregators(servicesInfo []ServiceInfo) error {
	if len(servicesInfo) == 0 {
		return nil
	}
	config := servicesInfo[0].Config
	smDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "internal", "grpc", "clients")

	if err := os.MkdirAll(smDir, 0755); err != nil {
		return fmt.Errorf("failed to create clients directory: %w", err)
	}

	// Resolve actual service names
	type resolvedSvc struct {
		ServiceInfo
		ActualName string
		Symbol     tsClientSymbol
	}
	var resolved []resolvedSvc
	var ids []tsClientIdentity
	for _, svc := range servicesInfo {
		actual := tsActualServicePackage(svc)
		ids = append(ids, tsClientIdentity{
			ParentDir:   svc.ParentDir,
			PackageName: actual,
			ServiceName: svc.ServiceName,
		})
	}
	symbols := tsClientSymbols(ids, "GrpcClient")
	for i, svc := range servicesInfo {
		resolved = append(resolved, resolvedSvc{
			ServiceInfo: svc,
			ActualName:  ids[i].PackageName,
			Symbol:      symbols[i],
		})
	}

	// Group by target service (like Rust does)
	type targetEntry struct {
		target    string
		fieldName string
		className string
		svcs      []resolvedSvc
	}
	var targets []targetEntry
	targetSeen := make(map[string]int)
	for _, rs := range resolved {
		target := resolveTarget(rs.ServiceInfo)
		safeTarget := strings.ReplaceAll(target, "-", "_")
		if idx, ok := targetSeen[target]; ok {
			targets[idx].svcs = append(targets[idx].svcs, rs)
		} else {
			targetSeen[target] = len(targets)
			targets = append(targets, targetEntry{
				target:    target,
				fieldName: strutil.ToSnakeCase(safeTarget),
				className: strutil.ToPascalCase(safeTarget) + "Clients",
				svcs:      []resolvedSvc{rs},
			})
		}
	}

	rootFile := filepath.Join(smDir, "grpc_clients.ts")
	var buf strings.Builder
	buf.WriteString(header.GeneratedHeader(consts.LangTypeScript, "grpc_clients"))
	buf.WriteString("\n")
	buf.WriteString("import { type ChannelProvider } from '../../../../chassis/servicemanager/index.js';\n\n")

	// Import all individual client classes
	for _, rs := range resolved {
		svcSnake := strutil.StripPackagePrefix(tsGeneratedSnakeName(rs.ServiceName), rs.ActualName)
		fmt.Fprintf(&buf, "import { %s } from './%s/%s/%s.js';\n",
			tsNamedImport(rs.Symbol.ImportName, rs.Symbol.LocalName), rs.ParentDir, rs.ActualName, svcSnake)
	}

	// Re-export all client classes
	buf.WriteString("\nexport {\n")
	for _, rs := range resolved {
		fmt.Fprintf(&buf, "  %s,\n", rs.Symbol.LocalName)
	}
	buf.WriteString("};\n")

	// Per-target {Target}Clients classes
	for _, te := range targets {
		fmt.Fprintf(&buf, "\n/** Typed gRPC clients for the \"%s\" service. */\n", te.target)
		fmt.Fprintf(&buf, "export class %s {\n", te.className)
		for _, rs := range te.svcs {
			fmt.Fprintf(&buf, "  private readonly _%s: %s;\n", rs.Symbol.FieldName, rs.Symbol.LocalName)
		}
		buf.WriteString("\n")
		fmt.Fprintf(&buf, "  constructor(cp: ChannelProvider, tokenProvider?: () => Promise<string>) {\n")
		for _, rs := range te.svcs {
			fmt.Fprintf(&buf, "    this._%s = new %s(cp, tokenProvider);\n",
				rs.Symbol.FieldName, rs.Symbol.LocalName)
		}
		buf.WriteString("  }\n\n")
		for _, rs := range te.svcs {
			fmt.Fprintf(&buf, "  %s(): %s { return this._%s; }\n",
				rs.Symbol.FieldName, rs.Symbol.LocalName, rs.Symbol.FieldName)
		}
		buf.WriteString("}\n")
	}

	// TokenProviderFor type
	buf.WriteString("\n/** A factory that returns an audience-scoped token provider for a given target service ID. */\n")
	buf.WriteString("export type TokenProviderFor = (audience: string) => (() => Promise<string>) | undefined;\n\n")

	// GrpcClients: root struct holding all targets, takes per-target ChannelProviders
	buf.WriteString("/** gRPC client hierarchy: sm.grpc().{target}().{client}() */\n")
	buf.WriteString("export class GrpcClients {\n")
	for _, te := range targets {
		fmt.Fprintf(&buf, "  private readonly _%s: %s;\n", te.fieldName, te.className)
	}
	buf.WriteString("\n  constructor(\n")
	for _, te := range targets {
		fmt.Fprintf(&buf, "    %sCp: ChannelProvider,\n", strutil.ToLowerCamelCase(te.fieldName))
	}
	buf.WriteString("    tokenProviderFor: TokenProviderFor,\n")
	buf.WriteString("  ) {\n")
	for _, te := range targets {
		fmt.Fprintf(&buf, "    this._%s = new %s(%sCp, tokenProviderFor('%s'));\n",
			te.fieldName, te.className, strutil.ToLowerCamelCase(te.fieldName), te.target)
	}
	buf.WriteString("  }\n\n")
	for _, te := range targets {
		fmt.Fprintf(&buf, "  %s(): %s { return this._%s; }\n", te.fieldName, te.className, te.fieldName)
	}
	buf.WriteString("}\n")

	if err := os.WriteFile(rootFile, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write S2S clients: %w", err)
	}

	// Also write per-parentDir group files (individual client re-exports for existing import paths)
	groups := make(map[string][]resolvedSvc)
	for _, rs := range resolved {
		groups[rs.ParentDir] = append(groups[rs.ParentDir], rs)
	}
	for parentDir, svcs := range groups {
		groupDir := filepath.Join(smDir, parentDir)
		if err := os.MkdirAll(groupDir, 0755); err != nil {
			return fmt.Errorf("failed to create group directory: %w", err)
		}
		groupFile := filepath.Join(groupDir, "grpc_clients.ts")
		var gbuf strings.Builder
		gbuf.WriteString(header.GeneratedHeader(consts.LangTypeScript, "grpc_clients"))
		gbuf.WriteString("\n")
		for _, rs := range svcs {
			svcSnake := strutil.StripPackagePrefix(tsGeneratedSnakeName(rs.ServiceName), rs.ActualName)
			fmt.Fprintf(&gbuf, "import { %s } from './%s/%s.js';\n",
				tsNamedImport(rs.Symbol.ImportName, rs.Symbol.LocalName), rs.ActualName, svcSnake)
		}
		gbuf.WriteString("\nexport {\n")
		for _, rs := range svcs {
			fmt.Fprintf(&gbuf, "  %s,\n", rs.Symbol.LocalName)
		}
		gbuf.WriteString("};\n")
		if err := os.WriteFile(groupFile, []byte(gbuf.String()), 0644); err != nil {
			return fmt.Errorf("failed to write group aggregator: %w", err)
		}
	}

	return nil
}

// generateGrpcClient creates the typed gRPC client wrapper TypeScript file for a service.
func generateGrpcClient(svc ServiceInfo, info *parser.ProtoInfo, fileDesc *parser.FileDescriptor) error {
	// Derive the actual service name
	actualServiceName := tsActualServicePackage(svc)

	// Output to sm/internal/grpc/clients/{parentDir}/{packageName}/
	outputDir := filepath.Join(svc.Config.OutputDir, svc.Config.GetSmPrefix(), "internal", "grpc", "clients", svc.ParentDir, actualServiceName)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	wrapperFile := strutil.StripPackagePrefix(tsGeneratedSnakeName(svc.ServiceName), actualServiceName) + ".ts"
	outputPath := filepath.Join(outputDir, wrapperFile)

	zap.L().Info("Generating TypeScript gRPC client", zap.String("path", outputPath))

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

	// Compute types import path
	wrapperDir := filepath.Join(svc.Config.GetSmPrefix(), "internal", "grpc", "clients", svc.ParentDir, actualServiceName)
	ownTypesDir := typescriptModuleDirForProtoDescriptor("", svc.ProtoFile, fileDesc)
	typesImportRel, _ := filepath.Rel(wrapperDir, ownTypesDir)

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
	var crossPkgImports strings.Builder
	for pkg, dir := range info.CrossPackageTypes {
		if dir == "" || !usedPkgs[pkg] {
			continue
		}
		if strings.HasPrefix(pkg, "google.protobuf") {
			continue
		}
		crossTypesDir := typescriptModuleDirForPackageOrDir(svc.Config.OutputDir, pkg, dir)
		crossRel, _ := filepath.Rel(wrapperDir, crossTypesDir)
		fmt.Fprintf(&crossPkgImports, "import * as %s_types from '%s/index.js';\n", pkg, crossRel)
	}

	// Check if google.protobuf.Empty is used
	hasGoogleEmpty := false
	for _, method := range targetService.Methods {
		if method.FullInputType == "google.protobuf.Empty" || method.FullOutputType == "google.protobuf.Empty" {
			hasGoogleEmpty = true
			break
		}
	}

	// Compute servicemanager import path (for ChannelProvider)
	smLibDir := filepath.Join("chassis", "servicemanager")
	smImportRel, _ := filepath.Rel(wrapperDir, smLibDir)

	// Build method implementations and extra types. Bidi methods produce a
	// handle class that lives outside the client class — collected into
	// extraTypes and appended to the file by the template.
	var methods strings.Builder
	var extraTypes strings.Builder
	hasBidi := false
	hasClientStreaming := false
	for _, method := range targetService.Methods {
		result, err := generateTsGrpcClientMethod(targetService, method, info.Package)
		if err != nil {
			return fmt.Errorf("failed to generate TypeScript gRPC client method %s.%s: %w", targetService.Name, method.Name, err)
		}
		methods.WriteString(result.method)
		methods.WriteString("\n")
		if result.extraType != "" {
			extraTypes.WriteString(result.extraType)
			extraTypes.WriteString("\n")
		}
		if method.ClientStreaming && method.ServerStreaming {
			hasBidi = true
		}
		if method.ClientStreaming && !method.ServerStreaming {
			hasClientStreaming = true
		}
	}
	if hasBidi {
		extraTypes.WriteString(tsBidiHelpers())
	}
	if hasClientStreaming {
		extraTypes.WriteString(tsGrpcAsyncIterableHelper())
	}

	clientName := svc.ServiceName + "GrpcClient"

	data := struct {
		Header               string
		TypesImport          string
		ServiceManagerImport string
		CrossPackageImports  string
		ServiceName          string
		ClientName           string
		Methods              string
		ExtraTypes           string
		HasGoogleEmpty       bool
	}{
		Header:               header.GeneratedHeader(consts.LangTypeScript, svc.ProtoFile),
		TypesImport:          typesImportRel + "/index.js",
		ServiceManagerImport: smImportRel + "/index.js",
		CrossPackageImports:  crossPkgImports.String(),
		ServiceName:          svc.ServiceName,
		ClientName:           clientName,
		Methods:              methods.String(),
		ExtraTypes:           extraTypes.String(),
		HasGoogleEmpty:       hasGoogleEmpty,
	}

	tmplContent, err := loadTemplate("grpcclient.ts.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load grpcclient template: %w", err)
	}

	t, err := template.New("grpcclient").Parse(tmplContent)
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

	// Create package.json and tsconfig.json for the gRPC client package
	if !proto.IsInfraProto(svc.ProtoFile) {
		if err := generateGrpcClientPackageJson(svc, outputDir); err != nil {
			return err
		}
	}

	return nil
}

// generateGrpcClientPackageJson creates package.json and tsconfig.json for a gRPC client package.
func generateGrpcClientPackageJson(svc ServiceInfo, outputDir string) error {
	basePackage := svc.Config.TsPackageBase
	if basePackage == "" {
		return nil
	}
	bufbuildVersion, err := typeScriptDependencyVersion("@bufbuild/protobuf")
	if err != nil {
		return err
	}

	actualServiceName := tsActualServicePackage(svc)

	npmPackageName := buildNpmPackageName(basePackage,
		strings.ReplaceAll(svc.ParentDir, "/", "-"),
		strings.ReplaceAll(actualServiceName, "/", "-"),
		svc.Config.GetSmPrefix(),
		"grpcclients")

	var b strings.Builder
	b.WriteString("{\n")
	fmt.Fprintf(&b, "\"name\": \"%s\",\n", npmPackageName)
	b.WriteString("\"version\": \"1.0.0\",\n")
	fmt.Fprintf(&b, "\"description\": \"TypeScript gRPC client wrapper for %s\",\n", actualServiceName)
	b.WriteString("\"main\": \"index.ts\",\n")
	b.WriteString("\"type\": \"module\",\n")
	b.WriteString("\"scripts\": {\n")
	b.WriteString("\"build\": \"tsc\"\n")
	b.WriteString("},\n")
	b.WriteString("\"dependencies\": {\n")
	b.WriteString("\"@grpc/grpc-js\": \"^1.9.0\",\n")
	fmt.Fprintf(&b, "\"@bufbuild/protobuf\": \"%s\"\n", bufbuildVersion)
	b.WriteString("},\n")
	b.WriteString("\"keywords\": [\"protobuf\", \"typescript\", \"grpc\", \"client\"],\n")
	b.WriteString("\"license\": \"UNLICENSED\",\n")
	b.WriteString("\"private\": true\n")
	b.WriteString("}\n")

	if err := os.WriteFile(filepath.Join(outputDir, "package.json"), []byte(b.String()), 0644); err != nil {
		return fmt.Errorf("failed to write package.json: %w", err)
	}

	tsconfig := `{
  "compilerOptions": {
    "target": "ES2020",
    "module": "ES2020",
    "lib": ["ES2020"],
    "declaration": true,
    "outDir": "dist",
    "strict": true,
    "esModuleInterop": true,
    "skipLibCheck": true,
    "forceConsistentCasingInFileNames": true,
    "moduleResolution": "bundler"
  },
  "include": ["*.ts"],
  "exclude": ["node_modules", "dist"]
}
`
	if err := os.WriteFile(filepath.Join(outputDir, "tsconfig.json"), []byte(tsconfig), 0644); err != nil {
		return fmt.Errorf("failed to write tsconfig.json: %w", err)
	}

	return nil
}

// tsGrpcMethodResult is the output of generateTsGrpcClientMethod.
// `method` is emitted inside the client class. `extraType` (when set) is
// emitted outside the class (e.g. bidi handle classes).
type tsGrpcMethodResult struct {
	method    string
	extraType string
}

func grpcClientAuthRequired(svc *parser.ServiceInfo, method *parser.MethodInfo) bool {
	return !(svc.AuthDisabled || svc.IsC2D || method.AuthDisabled)
}

func tsGrpcMetadataSnippet(authRequired bool) string {
	if !authRequired {
		return "        const metadata = await this.metadata(false);"
	}
	return "        const metadata = await this.metadata(true);"
}

func tsGrpcValidationStatement(varName, fullType, simpleType, servicePackage, indent string) string {
	validate := tsValidateRef(fullType, simpleType, servicePackage)
	if validate == "" {
		return ""
	}
	return fmt.Sprintf("%s%s(%s);\n", indent, validate, varName)
}

// generateTsGrpcClientMethod generates a TypeScript method for the gRPC client.
// Uses grpc.Client.makeUnaryRequest / makeServerStreamRequest / etc. with
// protobuf-es toBinary/fromBinary serializers for proper typing.
func generateTsGrpcClientMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, servicePackage string) (tsGrpcMethodResult, error) {
	methodLower := strutil.ToLowerCamelCase(method.Name)
	fullPath := fmt.Sprintf("/%s.%s/%s", servicePackage, svc.Name, method.Name)
	inputSchema := tsSchemaRef(method.FullInputType, method.InputType, servicePackage)
	outputSchema := tsSchemaRef(method.FullOutputType, method.OutputType, servicePackage)
	inputType := tsTypeRef(method.FullInputType, method.InputType, servicePackage)
	outputType := tsTypeRef(method.FullOutputType, method.OutputType, servicePackage)
	emptyInput := method.FullInputType == "google.protobuf.Empty"
	emptyOutput := method.FullOutputType == "google.protobuf.Empty"
	authRequired := grpcClientAuthRequired(svc, method)
	authMetadata := tsGrpcMetadataSnippet(authRequired)
	authRequiredLit := "false"
	if authRequired {
		authRequiredLit = "true"
	}
	retrySafeLit := "false"
	if method.IsRetrySafeUnary() {
		retrySafeLit = "true"
	}
	requestValidation := ""
	if !emptyInput {
		requestValidation = tsGrpcValidationStatement("request", method.FullInputType, method.InputType, servicePackage, "        ")
	}
	responseValidation := ""
	if !emptyOutput {
		responseValidation = tsGrpcValidationStatement("response", method.FullOutputType, method.OutputType, servicePackage, "        ")
	}
	streamRequestValidation := ""
	if !emptyInput {
		streamRequestValidation = tsGrpcValidationStatement("request", method.FullInputType, method.InputType, servicePackage, "                    ")
	}
	// NOTE: no per-send auth in the send loops — auth is STREAM-OPEN scoped
	// (one metadata attachment at open; the pinned cross-language contract,
	// docs/API.md Client Send Contract). A per-send this.metadata(true) minted a
	// token the server never saw and destroyed legitimate streams on provider
	// hiccups (NEW-35, the TS leg of PC-W-2).

	// Serializer/deserializer lambdas
	serialize := fmt.Sprintf("(msg: %s) => Buffer.from(toBinary(%s, msg))", inputType, inputSchema)
	deserialize := fmt.Sprintf("(buf: Buffer) => fromBinary(%s, new Uint8Array(buf))", outputSchema)

	if method.ClientStreaming && method.ServerStreaming {
		// Bidirectional streaming — managed lifecycle with reconnection.
		// Uses a separate .tmpl file to avoid Go backtick escaping issues.
		//
		// Design rationale: watches (server-streaming) use callbacks for the
		// receive side because they are receive-only. Bidi streams need both
		// send and receive. Callbacks work for the receive side (consistent
		// with the watch pattern). The returned handle exposes send() for the
		// send side, with messages buffered while a reconnect is in flight.
		handleType := strutil.ToPascalCase(method.Name) + "BidiHandle"

		tmplStr, err := loadTemplate("grpcclient_bidi.ts.tmpl")
		if err != nil {
			return tsGrpcMethodResult{}, fmt.Errorf("failed to load bidi template: %w", err)
		}

		data := struct {
			MethodName         string
			MethodLower        string
			ServiceName        string
			InputType          string
			OutputType         string
			HandleType         string
			FullPath           string
			Serialize          string
			Deserialize        string
			AuthRequired       bool
			RequestValidation  string
			ResponseValidation string
		}{
			MethodName:         method.Name,
			MethodLower:        methodLower,
			ServiceName:        svc.Name,
			InputType:          inputType,
			OutputType:         outputType,
			HandleType:         handleType,
			FullPath:           fullPath,
			Serialize:          serialize,
			Deserialize:        deserialize,
			AuthRequired:       authRequired,
			RequestValidation:  tsGrpcValidationStatement("msg", method.FullInputType, method.InputType, servicePackage, "                    "),
			ResponseValidation: tsGrpcValidationStatement("msg", method.FullOutputType, method.OutputType, servicePackage, "                            "),
		}

		tmpl, err := template.New("bidi").Parse(tmplStr)
		if err != nil {
			return tsGrpcMethodResult{}, fmt.Errorf("failed to parse bidi template: %w", err)
		}

		var buf strings.Builder
		if err := tmpl.Execute(&buf, data); err != nil {
			return tsGrpcMethodResult{}, fmt.Errorf("failed to execute bidi template: %w", err)
		}

		handleClass := tsBidiHandleClass(method.Name, handleType, inputType)
		return tsGrpcMethodResult{method: buf.String(), extraType: handleClass}, nil
	}

	if method.ServerStreaming && method.IsWatchCollection() {
		// WATCH: server-streaming envelope on the wire, surfaced as a lazy async
		// iterator over {Method}WatchEvent. Breaking iteration cancels the
		// grpc-js readable stream.
		reqParam := fmt.Sprintf("request: %s", inputType)
		reqArg := "request"
		if emptyInput {
			reqParam = ""
			reqArg = "{} as Empty"
		}
		return tsGrpcMethodResult{method: fmt.Sprintf(`    /**
     * %s calls %s.%s (WATCH collection over server streaming).
     */
    async %s(%s): Promise<AsyncIterable<%s>> {
%s
%s
        const stream = this.client.makeServerStreamRequest<%s, %s>(
            '%s',
            %s,
            %s,
	            %s,
	            metadata,
	            this.callOptions()
	        );
        return stream as unknown as AsyncIterable<%s>;
    }`, method.Name, svc.Name, method.Name,
			methodLower, reqParam, outputType, authMetadata, requestValidation,
			inputType, outputType,
			fullPath, serialize, deserialize, reqArg,
			outputType)}, nil
	}

	if method.ServerStreaming && method.IsTier3SnapshotCollection() {
		// Tier-3 SNAPSHOT cursor: server-streaming on the wire, surfaced as a lazy
		// async iterator. grpc-js readable streams are async-iterable; `for await …
		// break` calls `.return()`, which cancels the call (teardown).
		reqParam := fmt.Sprintf("request: %s", inputType)
		reqArg := "request"
		if emptyInput {
			reqParam = ""
			reqArg = "{} as Empty"
		}
		return tsGrpcMethodResult{method: fmt.Sprintf(`    /**
     * %s calls %s.%s (Tier-3 SNAPSHOT cursor over server streaming).
     */
    async %s(%s): Promise<AsyncIterable<%s>> {
%s
%s
        const stream = this.client.makeServerStreamRequest<%s, %s>(
            '%s',
            %s,
            %s,
	            %s,
	            metadata,
	            this.callOptions()
	        );
        return stream as unknown as AsyncIterable<%s>;
    }`, method.Name, svc.Name, method.Name,
			methodLower, reqParam, outputType, authMetadata, requestValidation,
			inputType, outputType,
			fullPath, serialize, deserialize, reqArg,
			outputType)}, nil
	}

	if method.ServerStreaming && method.IsTier2SnapshotCollection() {
		// Tier-2 SNAPSHOT collection: server-streaming on the wire, but the client
		// seam returns a materialized array — accumulate the readable stream.
		reqParam := fmt.Sprintf("request: %s", inputType)
		reqArg := "request"
		if emptyInput {
			reqParam = ""
			reqArg = "{} as Empty"
		}
		return tsGrpcMethodResult{method: fmt.Sprintf(`    /**
     * %s calls %s.%s (Tier-2 SNAPSHOT collection over server streaming).
     */
    async %s(%s): Promise<%s[]> {
%s
%s
        const stream = this.client.makeServerStreamRequest<%s, %s>(
            '%s',
            %s,
            %s,
	            %s,
	            metadata,
	            this.callOptions()
	        );
        return await new Promise<%s[]>((resolve, reject) => {
            const items: %s[] = [];
            stream.on('data', (item: %s) => items.push(item));
            stream.on('end', () => resolve(items));
            stream.on('error', (err: any) => reject(err));
        });
    }`, method.Name, svc.Name, method.Name,
			methodLower, reqParam, outputType, authMetadata, requestValidation,
			inputType, outputType,
			fullPath, serialize, deserialize, reqArg,
			outputType, outputType, outputType)}, nil
	}

	if method.ServerStreaming {
		if emptyInput {
			return tsGrpcMethodResult{method: fmt.Sprintf(`    /**
     * %s calls %s.%s (server streaming).
     */
    async %s(): Promise<grpc.ClientReadableStream<%s>> {
%s
        return this.client.makeServerStreamRequest<%s, %s>(
            '%s',
            %s,
            %s,
	            {} as Empty,
	            metadata,
	            this.callOptions()
	        );
    }`, method.Name, svc.Name, method.Name,
				methodLower, outputType, authMetadata,
				inputType, outputType,
				fullPath, serialize, deserialize)}, nil
		}
		return tsGrpcMethodResult{method: fmt.Sprintf(`    /**
     * %s calls %s.%s (server streaming).
     */
    async %s(request: %s): Promise<grpc.ClientReadableStream<%s>> {
%s
%s
        return this.client.makeServerStreamRequest<%s, %s>(
            '%s',
            %s,
            %s,
	            request,
	            metadata,
	            this.callOptions()
	        );
    }`, method.Name, svc.Name, method.Name,
			methodLower, inputType, outputType, authMetadata, requestValidation,
			inputType, outputType,
			fullPath, serialize, deserialize)}, nil
	}

	if method.ClientStreaming {
		if emptyOutput {
			return tsGrpcMethodResult{method: fmt.Sprintf(`    /**
     * %s calls %s.%s (client streaming).
     */
    async %s(requests: AsyncIterable<%s> | Iterable<%s>): Promise<void> {
%s
        await new Promise<void>((resolve, reject) => {
            const stream = this.client.makeClientStreamRequest<%s, %s>(
                '%s',
                %s,
                %s,
                metadata,
                this.callOptions(),
                (err) => err ? reject(err) : resolve()
            );
            (async () => {
                try {
                    for await (const request of toAsyncIterable(requests)) {
%s                        if (!stream.write(request)) {
                            await new Promise<void>((resume) => stream.once('drain', resume));
                        }
                    }
                    stream.end();
                } catch (err) {
                    stream.destroy(err instanceof Error ? err : new Error(String(err)));
                    reject(err);
                }
            })().catch(reject);
        });
    }`, method.Name, svc.Name, method.Name,
				methodLower, inputType, inputType, authMetadata,
				inputType, outputType,
				fullPath, serialize, deserialize,
				streamRequestValidation)}, nil
		}
		return tsGrpcMethodResult{method: fmt.Sprintf(`    /**
     * %s calls %s.%s (client streaming).
     */
    async %s(requests: AsyncIterable<%s> | Iterable<%s>): Promise<%s> {
%s
        const response = await new Promise<%s>((resolve, reject) => {
            const stream = this.client.makeClientStreamRequest<%s, %s>(
                '%s',
                %s,
                %s,
                metadata,
                this.callOptions(),
                (err, resp) => err ? reject(err) : resolve(resp!)
            );
            (async () => {
                try {
                    for await (const request of toAsyncIterable(requests)) {
%s                        if (!stream.write(request)) {
                            await new Promise<void>((resume) => stream.once('drain', resume));
                        }
                    }
                    stream.end();
                } catch (err) {
                    stream.destroy(err instanceof Error ? err : new Error(String(err)));
                    reject(err);
                }
            })().catch(reject);
        });
%s
        return response;
    }`, method.Name, svc.Name, method.Name,
			methodLower, inputType, inputType, outputType, authMetadata,
			outputType,
			inputType, outputType,
			fullPath, serialize, deserialize,
			streamRequestValidation, responseValidation)}, nil
	}

	if method.IsPageCollection() {
		// PAGE: unary on the wire. Two client seams — an explicit single-page call
		// ({method}Page → { items, nextPageToken, total }) and an auto-pager (the
		// natural method name, an async generator) that follows next_page_token
		// until exhausted, yielding each item. The auto-pager advances the token on
		// a clone, so the caller's request is never mutated.
		itemType := tsTypeRef(method.Resolved.PageItemType, simpleTypeName(method.Resolved.PageItemType), servicePackage)
		itemsField := strutil.ToLowerCamelCase(method.Resolved.PageItemsField)
		totalExpr := "-1"
		if method.Resolved.PageHasTotal {
			totalExpr = "Number((response as any).totalCount ?? -1)"
		}
		return tsGrpcMethodResult{method: fmt.Sprintf(`    /**
     * %sPage calls %s.%s and returns a single page: items, next_page_token
     * (empty on the last page), and total (-1 when the response carries no count).
     */
    async %sPage(request: %s): Promise<{ items: %s[]; nextPageToken: string; total: number }> {
%s
        const response = await this.unaryWithRetry<%s>(%s, true, (metadata, options) => new Promise<%s>((resolve, reject) => {
            this.client.makeUnaryRequest<%s, %s>(
                '%s',
                %s,
                %s,
                request,
                metadata,
                options,
                (err, resp) => err ? reject(err) : resolve(resp!)
            );
        }));
%s
        return {
            items: ((response as any).%s ?? []) as %s[],
            nextPageToken: String((response as any).nextPageToken ?? ''),
            total: %s,
        };
    }

    /**
     * %s calls %s.%s as an auto-pager: it follows next_page_token until
     * exhausted, yielding each item across pages. Break out of the iteration to
     * stop early. The caller's request is not mutated.
     */
    async *%s(request: %s): AsyncIterable<%s> {
        const cur = { ...request } as any;
        for (;;) {
            const { items, nextPageToken } = await this.%sPage(cur);
            for (const it of items) {
                yield it;
            }
            if (nextPageToken === '') {
                return;
            }
            cur.pageToken = nextPageToken;
        }
    }`, method.Name, svc.Name, method.Name,
			methodLower, inputType, itemType,
			requestValidation, outputType, authRequiredLit, outputType,
			inputType, outputType,
			fullPath, serialize, deserialize,
			responseValidation,
			itemsField, itemType, totalExpr,
			method.Name, svc.Name, method.Name,
			methodLower, inputType, itemType,
			methodLower)}, nil
	}

	// Unary method — use makeUnaryRequest with typed serializers
	if emptyOutput && emptyInput {
		return tsGrpcMethodResult{method: fmt.Sprintf(`    /**
     * %s calls %s.%s.
     */
	    async %s(): Promise<void> {
	        return this.unaryWithRetry<void>(%s, %s, (metadata, options) => new Promise<void>((resolve, reject) => {
	            this.client.makeUnaryRequest<%s, %s>(
	                '%s',
	                %s,
	                %s,
	                {} as Empty,
	                metadata,
	                options,
	                (err, resp) => err ? reject(err) : resolve()
	            );
	        }));
	    }`, method.Name, svc.Name, method.Name,
			methodLower, authRequiredLit, retrySafeLit,
			inputType, outputType,
			fullPath, serialize, deserialize)}, nil
	}
	if emptyOutput {
		return tsGrpcMethodResult{method: fmt.Sprintf(`    /**
     * %s calls %s.%s.
     */
	    async %s(request: %s): Promise<void> {
%s
	        return this.unaryWithRetry<void>(%s, %s, (metadata, options) => new Promise<void>((resolve, reject) => {
	            this.client.makeUnaryRequest<%s, %s>(
	                '%s',
	                %s,
	                %s,
	                request,
	                metadata,
	                options,
	                (err, resp) => err ? reject(err) : resolve()
	            );
	        }));
	    }`, method.Name, svc.Name, method.Name,
			methodLower, inputType, requestValidation, authRequiredLit, retrySafeLit,
			inputType, outputType,
			fullPath, serialize, deserialize)}, nil
	}
	if emptyInput {
		return tsGrpcMethodResult{method: fmt.Sprintf(`    /**
     * %s calls %s.%s.
     */
	    async %s(): Promise<%s> {
	        const response = await this.unaryWithRetry<%s>(%s, %s, (metadata, options) => new Promise<%s>((resolve, reject) => {
	            this.client.makeUnaryRequest<%s, %s>(
	                '%s',
	                %s,
	                %s,
	                {} as Empty,
	                metadata,
	                options,
	                (err, resp) => err ? reject(err) : resolve(resp!)
	            );
	        }));
%s
	        return response;
	    }`, method.Name, svc.Name, method.Name,
			methodLower, outputType,
			outputType, authRequiredLit, retrySafeLit, outputType,
			inputType, outputType,
			fullPath, serialize, deserialize, responseValidation)}, nil
	}
	return tsGrpcMethodResult{method: fmt.Sprintf(`    /**
     * %s calls %s.%s.
     */
	    async %s(request: %s): Promise<%s> {
%s
	        const response = await this.unaryWithRetry<%s>(%s, %s, (metadata, options) => new Promise<%s>((resolve, reject) => {
	            this.client.makeUnaryRequest<%s, %s>(
	                '%s',
	                %s,
	                %s,
	                request,
	                metadata,
	                options,
	                (err, resp) => err ? reject(err) : resolve(resp!)
	            );
	        }));
%s
	        return response;
	    }`, method.Name, svc.Name, method.Name,
		methodLower, inputType, outputType,
		requestValidation, outputType, authRequiredLit, retrySafeLit, outputType,
		inputType, outputType,
		fullPath, serialize, deserialize, responseValidation)}, nil
}

// tsBidiHandleClass returns the TypeScript handle class definition for a
// managed bidi stream. The handle exposes send() / close() / isClosed()
// while the lifecycle loop attaches/detaches the live stream's writer.
// Messages sent before the first connect (or during a reconnect window)
// are buffered up to a small bound and flushed on attach.
func tsBidiHandleClass(methodName, handleType, inputType string) string {
	return fmt.Sprintf(`
/**
 * Handle for the managed %s bidirectional stream.
 *
 * Send messages via send(). The stream reconnects automatically on
 * transport errors. close() aborts the lifecycle loop and cancels any
 * in-flight stream.
 *
 * Messages sent while a reconnect is in progress are buffered (bounded)
 * and flushed once the new stream is attached.
 */
export class %s {
    private static readonly MAX_BUFFER = 1024;
    private _send?: (msg: %s) => Promise<void>;
    private _cancel?: () => void;
    private _buffer: Array<{ msg: %s; resolve: () => void; reject: (err: unknown) => void }> = [];
    private _closed = false;
    private _sendChain: Promise<void> = Promise.resolve();

    /** send queues a message. If a stream is attached it writes immediately;
     *  otherwise the message is buffered until the next reconnect. */
    send(msg: %s): Promise<void> {
        if (this._closed) return Promise.reject(new Error('stream is closed'));
        if (this._send) {
            return this._send(msg);
        }
        if (this._buffer.length >= %s.MAX_BUFFER) {
            const dropped = this._buffer.shift();
            dropped?.reject(new Error('bidi send buffer full'));
        }
        return new Promise<void>((resolve, reject) => {
            this._buffer.push({ msg, resolve, reject });
        });
    }

    /** close stops the lifecycle loop and cancels the in-flight stream. */
    close(): void {
        this._closed = true;
        const pending = this._buffer;
        this._buffer = [];
        for (const item of pending) {
            item.reject(new Error('stream is closed'));
        }
        if (this._cancel) {
            try { this._cancel(); } catch { /* no-op */ }
        }
    }

    isClosed(): boolean { return this._closed; }

    /** Internal: attach a live stream writer and flush buffered messages. */
    _attach(send: (msg: %s) => Promise<void>): void {
        this._sendChain = Promise.resolve();
        const chainedSend = (msg: %s): Promise<void> => {
            const next = this._sendChain.then(() => send(msg));
            this._sendChain = next.catch(() => {});
            return next;
        };
        this._send = chainedSend;
        const pending = this._buffer;
        this._buffer = [];
        for (const item of pending) {
            chainedSend(item.msg).then(item.resolve, item.reject);
        }
    }

    /** Internal: detach the writer when the stream ends or errors. */
    _detach(): void {
        this._send = undefined;
        this._cancel = undefined;
        this._sendChain = Promise.resolve();
    }

    /** Internal: register a cancel callback for the live stream. */
    _setCancel(cancel: () => void): void {
        this._cancel = cancel;
    }
}
`, methodName, handleType, inputType, inputType, inputType, handleType, inputType, inputType)
}

// tsBidiHelpers returns the small helpers used by generated bidi lifecycle
// loops: a cancellable backoff sleep, and the auth-failure error constructor.
//
// bidiUnauthenticated stamps grpc.status.UNAUTHENTICATED the same way the
// unary/streaming seams in grpcclient.ts.tmpl (metadata()) already do. The bidi
// send path throws to the CALLER, so an uncoded Error left a caller unable to
// tell "no credential" from any other send failure (same class as PAR-9).
func tsBidiHelpers() string {
	return `
/** bidiUnauthenticated builds the error thrown when a bidi stream cannot obtain
 *  a credential, carrying the same grpc.status.UNAUTHENTICATED code the unary
 *  client seam uses. */
function bidiUnauthenticated(message: string): Error & { code?: number } {
    const err = new Error(message) as Error & { code?: number };
    err.code = grpc.status.UNAUTHENTICATED;
    return err;
}

/** bidiSleep waits for the given duration in milliseconds, but returns early
 *  if the handle is closed. */
function bidiSleep(ms: number, handle: { isClosed(): boolean }): Promise<void> {
    return new Promise<void>((resolve) => {
        if (handle.isClosed()) { resolve(); return; }
        const start = Date.now();
        const tick = (): void => {
            if (handle.isClosed() || Date.now() - start >= ms) { resolve(); return; }
            setTimeout(tick, Math.min(100, Math.max(1, ms - (Date.now() - start))));
        };
        tick();
    });
}
`
}

func tsGrpcAsyncIterableHelper() string {
	return `
async function* toAsyncIterable<T>(values: AsyncIterable<T> | Iterable<T>): AsyncIterable<T> {
    if (Symbol.asyncIterator in Object(values)) {
        for await (const value of values as AsyncIterable<T>) {
            yield value;
        }
        return;
    }
    for (const value of values as Iterable<T>) {
        yield value;
    }
}
`
}
