// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/header"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/consts"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
)

// generateRestClients generates typed REST client wrappers for each gRPC service.
// Methods get REST paths from google.api.http annotations or convention-based paths
// (POST /api/{package}/v1/{service}/{method}) for annotated services.
func generateRestClients(servicesInfo []ServiceInfo, inputDir string) error {
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	var generated []ServiceInfo
	for _, svc := range servicesInfo {
		protoInfo, err := descriptorParser.ConvertToProtoInfo(svc.ProtoFile)
		if err != nil {
			return fmt.Errorf("failed to parse %s: %w", svc.ProtoFile, err)
		}
		fileDesc, err := descriptorParser.GetFileDescriptor(svc.ProtoFile)
		if err != nil {
			return fmt.Errorf("failed to get descriptor for %s: %w", svc.ProtoFile, err)
		}

		// Only generate REST clients for services that have HTTP-annotated methods
		hasHttpMethods := false
		for _, s := range protoInfo.Services {
			if s.Name == svc.ServiceName {
				for _, m := range s.Methods {
					if m.HttpMethod != "" {
						hasHttpMethods = true
						break
					}
				}
				break
			}
		}

		if !hasHttpMethods {
			continue
		}

		if err := generateRestClient(svc, protoInfo, fileDesc); err != nil {
			return fmt.Errorf("failed to generate REST client for %s: %w", svc.ServiceName, err)
		}
		generated = append(generated, svc)
	}

	// Generate aggregator files
	if len(generated) > 0 {
		if err := generateTsRestClientAggregators(generated); err != nil {
			return fmt.Errorf("failed to generate REST client aggregators: %w", err)
		}
	}

	return nil
}

// generateRestClient creates a typed REST client TypeScript file for a service.
func generateRestClient(svc ServiceInfo, info *parser.ProtoInfo, fileDesc *parser.FileDescriptor) error {
	actualServiceName := tsActualServicePackage(svc)

	outputDir := filepath.Join(svc.Config.OutputDir, svc.Config.GetSmPrefix(), "internal", "rest", svc.ParentDir, actualServiceName)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	wrapperFile := strutil.StripPackagePrefix(tsGeneratedSnakeName(svc.ServiceName), actualServiceName) + ".ts"
	outputPath := filepath.Join(outputDir, wrapperFile)

	zap.L().Info("Generating TypeScript REST client", zap.String("path", outputPath))

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
	wrapperDir := filepath.Join(svc.Config.GetSmPrefix(), "internal", "rest", svc.ParentDir, actualServiceName)
	ownTypesDir := typescriptModuleDirForProtoDescriptor("", svc.ProtoFile, fileDesc)
	typesImportRel, _ := filepath.Rel(wrapperDir, ownTypesDir)

	// Build cross-package imports
	usedPkgs := make(map[string]bool)
	for _, method := range targetService.Methods {
		if method.HttpMethod == "" {
			continue
		}
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

	// Build method implementations — for all methods with HTTP paths (convention or annotated)
	var methods strings.Builder
	hasStreamingMethods := false
	needsWktEmpty := false
	for _, method := range targetService.Methods {
		if method.HttpMethod == "" {
			continue // skip methods without HTTP paths
		}
		if method.FullInputType == "google.protobuf.Empty" || method.FullOutputType == "google.protobuf.Empty" {
			needsWktEmpty = true
		}
		if method.ClientStreaming {
			continue // skip client-streaming methods
		}
		if method.ServerStreaming {
			hasStreamingMethods = true
			methods.WriteString(generateTsRestClientStreamMethod(targetService, method, info))
			methods.WriteString("\n")
			continue
		}
		methods.WriteString(generateTsRestClientMethod(targetService, method, info.Package))
		methods.WriteString("\n")
	}

	clientName := svc.ServiceName + "RestClient"

	// Generate the file content
	var buf strings.Builder
	buf.WriteString(header.GeneratedHeader(consts.LangTypeScript, svc.ProtoFile))
	buf.WriteString("\n")

	// Import toJson/fromJson from @bufbuild/protobuf
	buf.WriteString("import { toJson, fromJson } from '@bufbuild/protobuf';\n")
	if needsWktEmpty {
		buf.WriteString("import { type Empty, EmptySchema } from '@bufbuild/protobuf/wkt';\n")
	}
	fmt.Fprintf(&buf, "import * as types from '%s/index.js';\n", typesImportRel)
	if crossPkgImports.Len() > 0 {
		buf.WriteString(crossPkgImports.String())
	}
	buf.WriteString("\n")

	// Emit the RestTransport interface
	buf.WriteString("/**\n")
	buf.WriteString(" * Transport interface for REST clients.\n")
	buf.WriteString(" * Implement this with fetch (browser) or http (Node.js).\n")
	buf.WriteString(" */\n")
	buf.WriteString("export interface RestTransport {\n")
	buf.WriteString("    getJson(url: string): Promise<unknown>;\n")
	buf.WriteString("    postJson(url: string, body: unknown): Promise<unknown>;\n")
	buf.WriteString("    putJson(url: string, body: unknown): Promise<unknown>;\n")
	buf.WriteString("    patchJson(url: string, body: unknown): Promise<unknown>;\n")
	buf.WriteString("    deleteJson(url: string, body?: unknown): Promise<unknown>;\n")
	buf.WriteString("    getStream(url: string, opts?: { signal?: AbortSignal }): Promise<Response>;\n")
	buf.WriteString("    postStream(url: string, body: unknown, opts?: { signal?: AbortSignal }): Promise<Response>;\n")
	buf.WriteString("}\n\n")

	// Emit streamNdjson helper when service has streaming methods
	if hasStreamingMethods {
		buf.WriteString("async function* streamNdjson<T>(\n")
		buf.WriteString("  response: Response,\n")
		buf.WriteString("  parse: (json: unknown) => T,\n")
		buf.WriteString("): AsyncGenerator<T> {\n")
		buf.WriteString("  const reader = response.body!.getReader();\n")
		buf.WriteString("  const decoder = new TextDecoder();\n")
		buf.WriteString("  let buffer = '';\n")
		buf.WriteString("  try {\n")
		buf.WriteString("    while (true) {\n")
		buf.WriteString("      const { done, value } = await reader.read();\n")
		buf.WriteString("      if (done) break;\n")
		buf.WriteString("      buffer += decoder.decode(value, { stream: true });\n")
		buf.WriteString("      const lines = buffer.split('\\n');\n")
		buf.WriteString("      buffer = lines.pop()!;\n")
		buf.WriteString("      for (const line of lines) {\n")
		buf.WriteString("        let trimmed = line.trim();\n")
		buf.WriteString("        if (!trimmed) continue;\n")
		buf.WriteString("        if (trimmed.startsWith('data: ')) trimmed = trimmed.slice(6);\n")
		buf.WriteString("        else if (trimmed.startsWith('event:') || trimmed.startsWith(':')) continue;\n")
		buf.WriteString("        const envelope = JSON.parse(trimmed);\n")
		buf.WriteString("        yield parse(envelope.result ?? envelope);\n")
		buf.WriteString("      }\n")
		buf.WriteString("    }\n")
		buf.WriteString("    if (buffer.trim()) {\n")
		buf.WriteString("      let last = buffer.trim();\n")
		buf.WriteString("      if (last.startsWith('data: ')) last = last.slice(6);\n")
		buf.WriteString("      if (last && !last.startsWith('event:') && !last.startsWith(':')) {\n")
		buf.WriteString("        const envelope = JSON.parse(last);\n")
		buf.WriteString("        yield parse(envelope.result ?? envelope);\n")
		buf.WriteString("      }\n")
		buf.WriteString("    }\n")
		buf.WriteString("  } finally {\n")
		buf.WriteString("    reader.releaseLock();\n")
		buf.WriteString("  }\n")
		buf.WriteString("}\n\n")
	}

	fmt.Fprintf(&buf, "/**\n * %s provides a typed REST client for %s.\n", clientName, svc.ServiceName)
	buf.WriteString(" * Each method corresponds to an RPC with a google.api.http annotation,\n")
	buf.WriteString(" * with the HTTP path baked in from the proto definition.\n")
	buf.WriteString(" *\n")
	buf.WriteString(" * Works in both browser and server contexts via the RestTransport interface.\n")
	buf.WriteString(" */\n")
	fmt.Fprintf(&buf, "export class %s {\n", clientName)
	buf.WriteString("    private readonly transport: RestTransport;\n")
	buf.WriteString("    private readonly baseUrl: string;\n")
	buf.WriteString("\n")
	buf.WriteString("    constructor(transport: RestTransport, baseUrl: string) {\n")
	buf.WriteString("        this.transport = transport;\n")
	buf.WriteString("        this.baseUrl = baseUrl;\n")
	buf.WriteString("    }\n")
	buf.WriteString("\n")
	buf.WriteString(methods.String())
	buf.WriteString("}\n")

	if err := os.WriteFile(outputPath, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	// Create package.json and tsconfig.json
	if !proto.IsInfraProto(svc.ProtoFile) {
		if err := generateRestClientPackageJson(svc, outputDir); err != nil {
			return err
		}
	}

	return nil
}

// generateTsRestClientMethod generates a TypeScript method for the REST client.
func generateTsRestClientMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, servicePackage string) string {
	methodLower := strutil.ToLowerCamelCase(method.Name)

	// Resolve type prefix for cross-package types
	resolveTypePrefix := func(fullType string) string {
		parts := strings.Split(fullType, ".")
		if len(parts) > 1 {
			pkg := strings.Join(parts[:len(parts)-1], ".")
			if strings.HasPrefix(pkg, "google.protobuf") {
				return "" // WKT types imported directly
			}
			if pkg != servicePackage {
				return pkg + "_types"
			}
		}
		return "types"
	}

	// qualType returns "prefix.Type" or just "Type" when prefix is empty (WKT)
	qualType := func(prefix, typeName string) string {
		if prefix == "" {
			return typeName
		}
		return prefix + "." + typeName
	}

	outputType := method.OutputType
	outputPrefix := resolveTypePrefix(method.FullOutputType)

	// Check if input is google.protobuf.Empty
	isEmptyInput := method.FullInputType == "google.protobuf.Empty" || method.InputType == "Empty"

	inputType := method.InputType
	inputPrefix := resolveTypePrefix(method.FullInputType)

	switch method.HttpMethod {
	case "GET":
		if isEmptyInput {
			return fmt.Sprintf(`    /**
     * %s calls %s.%s via %s %s.
     */
    async %s(): Promise<%s> {
        const json = await this.transport.getJson(`+"`${this.baseUrl}%s`"+`);
        return fromJson(%s, json as any);
    }
`, method.Name, svc.Name, method.Name, method.HttpMethod, method.HttpPath,
				methodLower, qualType(outputPrefix, outputType),
				method.HttpPath,
				qualType(outputPrefix, outputType+"Schema"))
		}
		return fmt.Sprintf(`    /**
     * %s calls %s.%s via %s %s.
     */
    async %s(request: %s): Promise<%s> {
        const json = await this.transport.getJson(`+"`${this.baseUrl}%s`"+`);
        return fromJson(%s, json as any);
    }
`, method.Name, svc.Name, method.Name, method.HttpMethod, method.HttpPath,
			methodLower, qualType(inputPrefix, inputType), qualType(outputPrefix, outputType),
			method.HttpPath,
			qualType(outputPrefix, outputType+"Schema"))

	case "POST":
		if isEmptyInput {
			return fmt.Sprintf(`    /**
     * %s calls %s.%s via %s %s.
     */
    async %s(): Promise<%s> {
        const json = await this.transport.postJson(`+"`${this.baseUrl}%s`"+`, {});
        return fromJson(%s, json as any);
    }
`, method.Name, svc.Name, method.Name, method.HttpMethod, method.HttpPath,
				methodLower, qualType(outputPrefix, outputType),
				method.HttpPath,
				qualType(outputPrefix, outputType+"Schema"))
		}
		return fmt.Sprintf(`    /**
     * %s calls %s.%s via %s %s.
     */
    async %s(request: %s): Promise<%s> {
        const json = await this.transport.postJson(
            `+"`${this.baseUrl}%s`"+`,
            toJson(%s, request, { alwaysEmitImplicit: true }),
        );
        return fromJson(%s, json as any);
    }
`, method.Name, svc.Name, method.Name, method.HttpMethod, method.HttpPath,
			methodLower, qualType(inputPrefix, inputType), qualType(outputPrefix, outputType),
			method.HttpPath,
			qualType(inputPrefix, inputType+"Schema"),
			qualType(outputPrefix, outputType+"Schema"))

	case "PUT":
		return fmt.Sprintf(`    /**
     * %s calls %s.%s via %s %s.
     */
    async %s(request: %s): Promise<%s> {
        const json = await this.transport.putJson(
            `+"`${this.baseUrl}%s`"+`,
            toJson(%s, request, { alwaysEmitImplicit: true }),
        );
        return fromJson(%s, json as any);
    }
`, method.Name, svc.Name, method.Name, method.HttpMethod, method.HttpPath,
			methodLower, qualType(inputPrefix, inputType), qualType(outputPrefix, outputType),
			method.HttpPath,
			qualType(inputPrefix, inputType+"Schema"),
			qualType(outputPrefix, outputType+"Schema"))

	case "PATCH":
		return fmt.Sprintf(`    /**
     * %s calls %s.%s via %s %s.
     */
    async %s(request: %s): Promise<%s> {
        const json = await this.transport.patchJson(
            `+"`${this.baseUrl}%s`"+`,
            toJson(%s, request, { alwaysEmitImplicit: true }),
        );
        return fromJson(%s, json as any);
    }
`, method.Name, svc.Name, method.Name, method.HttpMethod, method.HttpPath,
			methodLower, qualType(inputPrefix, inputType), qualType(outputPrefix, outputType),
			method.HttpPath,
			qualType(inputPrefix, inputType+"Schema"),
			qualType(outputPrefix, outputType+"Schema"))

	case "DELETE":
		if isEmptyInput {
			return fmt.Sprintf(`    /**
     * %s calls %s.%s via %s %s.
     */
    async %s(): Promise<unknown> {
        return this.transport.deleteJson(`+"`${this.baseUrl}%s`"+`);
    }
`, method.Name, svc.Name, method.Name, method.HttpMethod, method.HttpPath,
				methodLower,
				method.HttpPath)
		}
		return fmt.Sprintf(`    /**
     * %s calls %s.%s via %s %s.
     */
    async %s(request: %s): Promise<unknown> {
        return this.transport.deleteJson(
            `+"`${this.baseUrl}%s`"+`,
            toJson(%s, request, { alwaysEmitImplicit: true }),
        );
    }
`, method.Name, svc.Name, method.Name, method.HttpMethod, method.HttpPath,
			methodLower, qualType(inputPrefix, inputType),
			method.HttpPath,
			qualType(inputPrefix, inputType+"Schema"))
	}

	return ""
}

// generateTsRestClientStreamMethod generates a TypeScript streaming method for the REST client.
// Server-streaming RPCs are exposed over REST via grpc-gateway as NDJSON responses.
func generateTsRestClientStreamMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, info *parser.ProtoInfo) string {
	methodLower := strutil.ToLowerCamelCase(method.Name)

	// Resolve type prefix for cross-package types
	resolveTypePrefix := func(fullType string) string {
		parts := strings.Split(fullType, ".")
		if len(parts) > 1 {
			pkg := strings.Join(parts[:len(parts)-1], ".")
			if strings.HasPrefix(pkg, "google.protobuf") {
				return "" // WKT types imported directly
			}
			if pkg != info.Package {
				return pkg + "_types"
			}
		}
		return "types"
	}

	// qualType returns "prefix.Type" or just "Type" when prefix is empty (WKT)
	qualType := func(prefix, typeName string) string {
		if prefix == "" {
			return typeName
		}
		return prefix + "." + typeName
	}

	outputType := method.OutputType
	outputPrefix := resolveTypePrefix(method.FullOutputType)
	isEmptyInput := method.FullInputType == "google.protobuf.Empty" || method.InputType == "Empty"
	inputType := method.InputType
	inputPrefix := resolveTypePrefix(method.FullInputType)

	switch method.HttpMethod {
	case "GET":
		if isEmptyInput {
			return fmt.Sprintf(`    /**
     * %s streams %s.%s via %s %s (NDJSON).
     */
    async %s(opts?: { signal?: AbortSignal }): Promise<AsyncIterable<%s>> {
        const response = await this.transport.getStream(`+"`${this.baseUrl}%s`"+`, opts);
        return streamNdjson(response, (json) => fromJson(%s, json as any));
    }
`, method.Name, svc.Name, method.Name, method.HttpMethod, method.HttpPath,
				methodLower, qualType(outputPrefix, outputType),
				method.HttpPath,
				qualType(outputPrefix, outputType+"Schema"))
		}

		// Build query params from request fields
		var queryBuild strings.Builder
		queryBuild.WriteString("        const params = new URLSearchParams();\n")
		if msg := findMessage(info, method.InputType); msg != nil {
			for _, field := range msg.Fields {
				camelField := strutil.ToLowerCamelCase(field.Name)
				var guard string
				if field.Repeated {
					guard = fmt.Sprintf("request.%s !== undefined && request.%s.length > 0", camelField, camelField)
				} else {
					switch field.Type {
					case "string":
						guard = fmt.Sprintf("request.%s !== undefined && request.%s !== ''", camelField, camelField)
					case "int32", "int64", "uint32", "uint64", "sint32", "sint64",
						"fixed32", "fixed64", "sfixed32", "sfixed64", "float", "double":
						guard = fmt.Sprintf("request.%s !== undefined && request.%s !== 0", camelField, camelField)
					case "bool":
						guard = fmt.Sprintf("request.%s !== undefined", camelField)
					default:
						guard = fmt.Sprintf("request.%s !== undefined", camelField)
					}
				}
				queryBuild.WriteString(fmt.Sprintf("        if (%s) params.set('%s', String(request.%s));\n",
					guard, strutil.ToSnakeCase(field.Name), camelField))
			}
		}
		queryBuild.WriteString("        const qs = params.toString();\n")

		return fmt.Sprintf(`    /**
     * %s streams %s.%s via %s %s (NDJSON).
     */
    async %s(request: %s, opts?: { signal?: AbortSignal }): Promise<AsyncIterable<%s>> {
%s        const url = qs ? `+"`${this.baseUrl}%s?${qs}`"+` : `+"`${this.baseUrl}%s`"+`;
        const response = await this.transport.getStream(url, opts);
        return streamNdjson(response, (json) => fromJson(%s, json as any));
    }
`, method.Name, svc.Name, method.Name, method.HttpMethod, method.HttpPath,
			methodLower, qualType(inputPrefix, inputType), qualType(outputPrefix, outputType),
			queryBuild.String(),
			method.HttpPath, method.HttpPath,
			qualType(outputPrefix, outputType+"Schema"))

	case "POST":
		if isEmptyInput {
			return fmt.Sprintf(`    /**
     * %s streams %s.%s via %s %s (NDJSON).
     */
    async %s(opts?: { signal?: AbortSignal }): Promise<AsyncIterable<%s>> {
        const response = await this.transport.postStream(`+"`${this.baseUrl}%s`"+`, {}, opts);
        return streamNdjson(response, (json) => fromJson(%s, json as any));
    }
`, method.Name, svc.Name, method.Name, method.HttpMethod, method.HttpPath,
				methodLower, qualType(outputPrefix, outputType),
				method.HttpPath,
				qualType(outputPrefix, outputType+"Schema"))
		}
		return fmt.Sprintf(`    /**
     * %s streams %s.%s via %s %s (NDJSON).
     */
    async %s(request: %s, opts?: { signal?: AbortSignal }): Promise<AsyncIterable<%s>> {
        const response = await this.transport.postStream(
            `+"`${this.baseUrl}%s`"+`,
            toJson(%s, request, { alwaysEmitImplicit: true }),
            opts,
        );
        return streamNdjson(response, (json) => fromJson(%s, json as any));
    }
`, method.Name, svc.Name, method.Name, method.HttpMethod, method.HttpPath,
			methodLower, qualType(inputPrefix, inputType), qualType(outputPrefix, outputType),
			method.HttpPath,
			qualType(inputPrefix, inputType+"Schema"),
			qualType(outputPrefix, outputType+"Schema"))
	}

	return ""
}

// findMessage looks up a MessageInfo by name in the ProtoInfo.
func findMessage(info *parser.ProtoInfo, name string) *parser.MessageInfo {
	for _, msg := range info.Messages {
		if msg.Name == name {
			return msg
		}
	}
	return nil
}

// generateRestClientPackageJson creates package.json and tsconfig.json for a REST client package.
func generateRestClientPackageJson(svc ServiceInfo, outputDir string) error {
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
		"restclients")

	var b strings.Builder
	b.WriteString("{\n")
	fmt.Fprintf(&b, "\"name\": \"%s\",\n", npmPackageName)
	b.WriteString("\"version\": \"1.0.0\",\n")
	fmt.Fprintf(&b, "\"description\": \"TypeScript REST client wrapper for %s\",\n", actualServiceName)
	b.WriteString("\"main\": \"index.ts\",\n")
	b.WriteString("\"type\": \"module\",\n")
	b.WriteString("\"scripts\": {\n")
	b.WriteString("\"build\": \"tsc\"\n")
	b.WriteString("},\n")
	b.WriteString("\"dependencies\": {\n")
	fmt.Fprintf(&b, "\"@bufbuild/protobuf\": \"%s\"\n", bufbuildVersion)
	b.WriteString("},\n")
	b.WriteString("\"keywords\": [\"protobuf\", \"typescript\", \"rest\", \"client\"],\n")
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

// generateTsRestClientAggregators creates group and root aggregator files for REST clients.
func generateTsRestClientAggregators(servicesInfo []ServiceInfo) error {
	if len(servicesInfo) == 0 {
		return nil
	}
	config := servicesInfo[0].Config
	smDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "internal", "rest")

	type groupedSvc struct {
		ServiceInfo
		Symbol tsClientSymbol
	}
	var normalized []ServiceInfo
	var ids []tsClientIdentity
	for _, svc := range servicesInfo {
		svcCopy := svc
		svcCopy.PackageName = tsActualServicePackage(svc)
		normalized = append(normalized, svcCopy)
		ids = append(ids, tsClientIdentity{
			ParentDir:   svc.ParentDir,
			PackageName: svcCopy.PackageName,
			ServiceName: svc.ServiceName,
		})
	}
	symbols := tsClientSymbols(ids, "RestClient")
	groups := make(map[string][]groupedSvc)
	for i, svc := range normalized {
		groups[svc.ParentDir] = append(groups[svc.ParentDir], groupedSvc{
			ServiceInfo: svc,
			Symbol:      symbols[i],
		})
	}

	// Generate group aggregators
	var groupNames []string
	for parentDir, svcs := range groups {
		groupNames = append(groupNames, parentDir)
		groupDir := filepath.Join(smDir, parentDir)
		groupFile := filepath.Join(groupDir, "rests.ts")

		var buf strings.Builder
		buf.WriteString(header.GeneratedHeader(consts.LangTypeScript, "rest"))
		buf.WriteString("\n")

		// Re-export only the client classes (not RestTransport, which would conflict)
		for _, svc := range svcs {
			svcSnake := strutil.StripPackagePrefix(tsGeneratedSnakeName(svc.ServiceName), svc.PackageName)
			fmt.Fprintf(&buf, "export { %s } from './%s/%s.js';\n",
				tsNamedExport(svc.Symbol.ImportName, svc.Symbol.LocalName), svc.PackageName, svcSnake)
		}

		if err := os.MkdirAll(groupDir, 0755); err != nil {
			return fmt.Errorf("failed to create group directory: %w", err)
		}
		if err := os.WriteFile(groupFile, []byte(buf.String()), 0644); err != nil {
			return fmt.Errorf("failed to write group aggregator: %w", err)
		}
	}

	// Sort the group names before emitting the root aggregator: `groups` is a Go
	// map, so ranging it leaks Go's randomized map order into generated output
	// and byte-diffs across otherwise-identical runs (GEN-DET-1).
	sort.Strings(groupNames)

	// Generate root aggregator — re-export RestTransport once, then all clients
	rootFile := filepath.Join(smDir, "rest.ts")
	var buf strings.Builder
	buf.WriteString(header.GeneratedHeader(consts.LangTypeScript, "rest"))
	buf.WriteString("\n")

	// Export RestTransport from the first service
	firstGroup := groups[groupNames[0]]
	firstSvc := firstGroup[0]
	firstSnake := strutil.StripPackagePrefix(tsGeneratedSnakeName(firstSvc.ServiceName), firstSvc.PackageName)
	fmt.Fprintf(&buf, "export type { RestTransport } from './%s/%s/%s.js';\n",
		groupNames[0], firstSvc.PackageName, firstSnake)

	// Re-export all client classes
	for _, groupName := range groupNames {
		fmt.Fprintf(&buf, "export * from './%s/rests.js';\n", groupName)
	}

	if err := os.MkdirAll(smDir, 0755); err != nil {
		return fmt.Errorf("failed to create rest directory: %w", err)
	}
	if err := os.WriteFile(rootFile, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write root aggregator: %w", err)
	}

	return nil
}
