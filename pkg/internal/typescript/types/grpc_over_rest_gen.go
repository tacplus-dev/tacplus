// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
)

// generateGrpcOverRestClients generates TypeScript clients with the same API as gRPC clients
// but using REST transport underneath. For browser environments that can't use native gRPC.
func generateGrpcOverRestClients(servicesInfo []ServiceInfo, inputDir string) error {
	return generateGrpcOverRestClientsForSurface(servicesInfo, inputDir, false)
}

// generateW2SGrpcOverRestClients generates TypeScript W2S clients.
// Unary RPCs use HTTP/JSON, server-streaming RPCs use SSE, and
// client-streaming or bidirectional RPCs use the gateway WebSocket stream
// contract.
func generateW2SGrpcOverRestClients(servicesInfo []ServiceInfo, inputDir string) error {
	return generateGrpcOverRestClientsForSurface(servicesInfo, inputDir, true)
}

func generateGrpcOverRestClientsForSurface(servicesInfo []ServiceInfo, inputDir string, requireFullW2S bool) error {
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

		if err := generateGrpcOverRestClient(svc, protoInfo, fileDesc, requireFullW2S); err != nil {
			return fmt.Errorf("failed to generate gRPC-over-REST client for %s: %w", svc.ServiceName, err)
		}
	}

	if len(servicesInfo) > 0 {
		if err := generateTsGrpcOverRestAggregators(servicesInfo, requireFullW2S); err != nil {
			return fmt.Errorf("failed to generate gRPC-over-REST aggregators: %w", err)
		}
	}

	return nil
}

// generateTsGrpcOverRestAggregators creates group and root aggregator files.
func generateTsGrpcOverRestAggregators(servicesInfo []ServiceInfo, includeW2STypes bool) error {
	if len(servicesInfo) == 0 {
		return nil
	}
	config := servicesInfo[0].Config
	smDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "internal", "grpc_over_rest")

	type groupedSvc struct {
		ServiceInfo
		Symbol tsClientSymbol
	}
	var normalized []ServiceInfo
	var resolved []groupedSvc
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
	symbols := tsClientSymbols(ids, "Client")
	groups := make(map[string][]groupedSvc)
	for i, svc := range normalized {
		rs := groupedSvc{
			ServiceInfo: svc,
			Symbol:      symbols[i],
		}
		resolved = append(resolved, rs)
		groups[svc.ParentDir] = append(groups[svc.ParentDir], rs)
	}

	var groupNames []string
	for parentDir, svcs := range groups {
		groupNames = append(groupNames, parentDir)
		groupDir := filepath.Join(smDir, parentDir)
		groupFile := filepath.Join(groupDir, "grpc_over_rest.ts")

		var buf strings.Builder
		buf.WriteString(header.GeneratedHeader(consts.LangTypeScript, "grpc_over_rest"))
		buf.WriteString("\n")

		for _, svc := range svcs {
			svcSnake := strutil.StripPackagePrefix(tsGeneratedSnakeName(svc.ServiceName), svc.PackageName)
			fmt.Fprintf(&buf, "import { %s } from './%s/%s.js';\n",
				tsNamedImport(svc.Symbol.ImportName, svc.Symbol.LocalName), svc.PackageName, svcSnake)
		}

		buf.WriteString("\n")
		// Re-export shared transport types from the first service
		if len(svcs) > 0 {
			firstSnake := strutil.StripPackagePrefix(tsGeneratedSnakeName(svcs[0].ServiceName), svcs[0].PackageName)
			if includeW2STypes {
				fmt.Fprintf(&buf, "export type { RestTransport, W2SBidiOptions, W2SStreamHandle, W2STransportOptions } from './%s/%s.js';\n\n",
					svcs[0].PackageName, firstSnake)
			} else {
				fmt.Fprintf(&buf, "export type { RestTransport } from './%s/%s.js';\n\n",
					svcs[0].PackageName, firstSnake)
			}
		}
		buf.WriteString("export {\n")
		for _, svc := range svcs {
			fmt.Fprintf(&buf, "  %s,\n", svc.Symbol.LocalName)
		}
		buf.WriteString("};\n")

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

	// Root aggregator
	rootFile := filepath.Join(smDir, "grpc_over_rest.ts")
	var buf strings.Builder
	buf.WriteString(header.GeneratedHeader(consts.LangTypeScript, "grpc_over_rest"))
	buf.WriteString("\n")

	// Export RestTransport from first group
	if len(groupNames) > 0 {
		if includeW2STypes {
			fmt.Fprintf(&buf, "export type { RestTransport, W2SBidiOptions, W2SStreamHandle, W2STransportOptions } from './%s/grpc_over_rest.js';\n", groupNames[0])
		} else {
			fmt.Fprintf(&buf, "export type { RestTransport } from './%s/grpc_over_rest.js';\n", groupNames[0])
		}
	}
	for _, groupName := range groupNames {
		fmt.Fprintf(&buf, "export * from './%s/grpc_over_rest.js';\n", groupName)
	}
	if includeW2STypes {
		buf.WriteString("\n")
		fmt.Fprintf(&buf, "import type { RestTransport } from './%s/grpc_over_rest.js';\n", groupNames[0])
		for _, rs := range resolved {
			fmt.Fprintf(&buf, "import { %s } from './%s/grpc_over_rest.js';\n",
				tsNamedImport(rs.Symbol.ImportName, rs.Symbol.LocalName), rs.ParentDir)
		}

		type targetEntry struct {
			target    string
			fieldName string
			className string
			svcs      []groupedSvc
		}
		var targets []targetEntry
		targetSeen := make(map[string]int)
		for _, rs := range resolved {
			target := resolveTarget(rs.ServiceInfo)
			safeTarget := strings.ReplaceAll(target, "-", "_")
			if idx, ok := targetSeen[target]; ok {
				targets[idx].svcs = append(targets[idx].svcs, rs)
				continue
			}
			targetSeen[target] = len(targets)
			targets = append(targets, targetEntry{
				target:    target,
				fieldName: strutil.ToSnakeCase(safeTarget),
				className: strutil.ToPascalCase(safeTarget) + "W2SClients",
				svcs:      []groupedSvc{rs},
			})
		}

		for _, te := range targets {
			fmt.Fprintf(&buf, "\n/** Typed W2S clients for the \"%s\" service. */\n", te.target)
			fmt.Fprintf(&buf, "export class %s {\n", te.className)
			for _, rs := range te.svcs {
				fmt.Fprintf(&buf, "  private readonly _%s: %s;\n", rs.Symbol.FieldName, rs.Symbol.LocalName)
			}
			buf.WriteString("\n")
			buf.WriteString("  constructor(transport: RestTransport, baseUrl: string = '') {\n")
			for _, rs := range te.svcs {
				fmt.Fprintf(&buf, "    this._%s = new %s(transport, baseUrl);\n", rs.Symbol.FieldName, rs.Symbol.LocalName)
			}
			buf.WriteString("  }\n\n")
			for _, rs := range te.svcs {
				fmt.Fprintf(&buf, "  %s(): %s { return this._%s; }\n", rs.Symbol.FieldName, rs.Symbol.LocalName, rs.Symbol.FieldName)
			}
			buf.WriteString("}\n")
		}

		buf.WriteString("\n/** W2S client hierarchy: sm.grpc().{target}().{client}() */\n")
		buf.WriteString("export class W2SClients {\n")
		for _, te := range targets {
			fmt.Fprintf(&buf, "  private readonly _%s: %s;\n", te.fieldName, te.className)
		}
		buf.WriteString("\n  constructor(transport: RestTransport, baseUrl: string = '') {\n")
		for _, te := range targets {
			fmt.Fprintf(&buf, "    this._%s = new %s(transport, baseUrl);\n", te.fieldName, te.className)
		}
		buf.WriteString("  }\n\n")
		for _, te := range targets {
			fmt.Fprintf(&buf, "  %s(): %s { return this._%s; }\n", te.fieldName, te.className, te.fieldName)
		}
		buf.WriteString("}\n")
	}

	if err := os.MkdirAll(smDir, 0755); err != nil {
		return fmt.Errorf("failed to create grpc_over_rest directory: %w", err)
	}
	if err := os.WriteFile(rootFile, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write root aggregator: %w", err)
	}

	return nil
}

// generateGrpcOverRestClient creates a TypeScript gRPC-over-REST client for a service.
// Same API as gRPC clients but using REST transport for browser compatibility.
func generateGrpcOverRestClient(svc ServiceInfo, info *parser.ProtoInfo, fileDesc *parser.FileDescriptor, requireFullW2S bool) error {
	actualServiceName := tsActualServicePackage(svc)

	outputDir := filepath.Join(svc.Config.OutputDir, svc.Config.GetSmPrefix(), "internal", "grpc_over_rest", svc.ParentDir, actualServiceName)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	wrapperFile := strutil.StripPackagePrefix(tsGeneratedSnakeName(svc.ServiceName), actualServiceName) + ".ts"
	outputPath := filepath.Join(outputDir, wrapperFile)

	zap.L().Info("Generating TypeScript gRPC-over-REST client", zap.String("path", outputPath))

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
	if requireFullW2S {
		for _, method := range targetService.Methods {
			// An RPC_PRINCIPAL_MCP rpc carries no REST route by construction —
			// the parser leaves HttpPath/HttpMethod empty for it — so there is
			// no w2s client method to emit and its absence is not a defect.
			// This mirrors the same skip the Go backend already carries
			// (go/types/w2sclient_gen.go). Without it, a service with one MCP
			// tool cannot have a w2s client AT ALL: scotty.Scotty declares
			// GetMyTicketAudit as RPC_PRINCIPAL_MCP, and the refusal below
			// fired on it even though the gateway's own REST route table
			// deliberately omits that rpc.
			if method.IsMcp {
				continue
			}
			if method.HttpPath == "" {
				return fmt.Errorf("%s.%s requires W2S REST route metadata; clients.w2s.enabled cannot emit a method without an HTTP path",
					targetService.Name, method.Name)
			}
		}
	}

	// Compute types import path
	wrapperDir := filepath.Join(svc.Config.GetSmPrefix(), "internal", "grpc_over_rest", svc.ParentDir, actualServiceName)
	ownTypesDir := typescriptModuleDirForProtoDescriptor("", svc.ProtoFile, fileDesc)
	typesImportRel, _ := filepath.Rel(wrapperDir, ownTypesDir)

	// Build cross-package imports
	usedPkgs := make(map[string]bool)
	for _, method := range targetService.Methods {
		if !shouldEmitGrpcOverRestMethod(method, requireFullW2S) {
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

	// Build method implementations
	var methods strings.Builder
	var extraTypes strings.Builder
	hasStreamingMethods := false
	hasWebSocketMethods := false
	needsWktEmpty := false
	for _, method := range targetService.Methods {
		if !shouldEmitGrpcOverRestMethod(method, requireFullW2S) {
			continue
		}
		if method.FullInputType == "google.protobuf.Empty" || method.FullOutputType == "google.protobuf.Empty" {
			needsWktEmpty = true
		}
		authRequired := false
		if requireFullW2S {
			authRequired = tsW2SAuthRequired(targetService, method)
		}
		if method.ClientStreaming && method.ServerStreaming {
			hasWebSocketMethods = true
			result := generateTsGrpcOverRestBidiMethod(targetService, method, info.Package, authRequired)
			methods.WriteString(result.method)
			methods.WriteString("\n")
			if result.extraType != "" {
				extraTypes.WriteString(result.extraType)
				extraTypes.WriteString("\n")
			}
			continue
		}
		if method.ClientStreaming {
			hasWebSocketMethods = true
			methods.WriteString(generateTsGrpcOverRestClientStreamMethod(targetService, method, info.Package, authRequired))
			methods.WriteString("\n")
			continue
		}
		if method.ServerStreaming {
			hasStreamingMethods = true
			if requireFullW2S {
				methods.WriteString(generateTsGrpcOverRestStreamMethod(targetService, method, info, authRequired))
			} else {
				methods.WriteString(generateTsGrpcOverRestLegacyStreamMethod(targetService, method, info))
			}
			methods.WriteString("\n")
			continue
		}
		if requireFullW2S {
			methods.WriteString(generateTsGrpcOverRestMethod(targetService, method, info.Package, authRequired))
		} else {
			methods.WriteString(generateTsGrpcOverRestLegacyMethod(targetService, method, info.Package))
		}
		methods.WriteString("\n")
	}

	clientName := svc.ServiceName + "Client"

	var buf strings.Builder
	buf.WriteString(header.GeneratedHeader(consts.LangTypeScript, svc.ProtoFile))
	buf.WriteString("\n")

	// Import toJson/fromJson
	buf.WriteString("import { toJson, fromJson } from '@bufbuild/protobuf';\n")
	if needsWktEmpty {
		buf.WriteString("import { type Empty, EmptySchema } from '@bufbuild/protobuf/wkt';\n")
	}
	fmt.Fprintf(&buf, "import * as types from '%s/index.js';\n", typesImportRel)
	if crossPkgImports.Len() > 0 {
		buf.WriteString(crossPkgImports.String())
	}
	buf.WriteString("\n")

	// RestTransport interface (same as REST client)
	buf.WriteString("/**\n")
	buf.WriteString(" * Transport interface for gRPC-over-REST clients.\n")
	buf.WriteString(" * Implement with fetch (browser) or http (Node.js).\n")
	buf.WriteString(" */\n")
	buf.WriteString("export interface RestTransport {\n")
	if requireFullW2S {
		buf.WriteString("    requestJson?(method: string, url: string, body: unknown, opts?: W2STransportOptions): Promise<unknown>;\n")
		buf.WriteString("    postJson(url: string, body: unknown, opts?: W2STransportOptions): Promise<unknown>;\n")
		buf.WriteString("    getStream(url: string, opts?: W2STransportOptions): Promise<Response>;\n")
		buf.WriteString("    postStream(url: string, body: unknown, opts?: W2STransportOptions): Promise<Response>;\n")
		buf.WriteString("    openWebSocket?(url: string, opts?: W2STransportOptions): Promise<WebSocket>;\n")
		buf.WriteString("    authHeader?(service?: string): Promise<string | undefined>;\n")
		buf.WriteString("}\n\n")
		buf.WriteString("export interface W2STransportOptions {\n")
		buf.WriteString("    signal?: AbortSignal;\n")
		buf.WriteString("    service?: string;\n")
		buf.WriteString("    auth?: boolean;\n")
		buf.WriteString("}\n\n")
		buf.WriteString("export interface W2SStreamHandle<T> {\n")
		buf.WriteString("    send(message: T): Promise<void>;\n")
		buf.WriteString("    close(): void;\n")
		buf.WriteString("    isClosed(): boolean;\n")
		buf.WriteString("}\n\n")
		buf.WriteString("export interface W2SBidiOptions<T> {\n")
		buf.WriteString("    signal?: AbortSignal;\n")
		buf.WriteString("    reconnect?: boolean;\n")
		buf.WriteString("    reconnectDelayMs?: number;\n")
		buf.WriteString("    onOpen?: (handle: W2SStreamHandle<T>) => void | Promise<void>;\n")
		buf.WriteString("    onError?: (error: unknown) => void;\n")
		buf.WriteString("}\n\n")
	} else {
		buf.WriteString("    postJson(url: string, body: unknown, opts?: { service?: string }): Promise<unknown>;\n")
		buf.WriteString("    getStream(url: string, opts?: { signal?: AbortSignal; service?: string }): Promise<Response>;\n")
		buf.WriteString("    postStream(url: string, body: unknown, opts?: { signal?: AbortSignal; service?: string }): Promise<Response>;\n")
		buf.WriteString("}\n\n")
	}

	if requireFullW2S {
		buf.WriteString(tsW2SEnvelopeHelper())
	}
	if hasStreamingMethods {
		if requireFullW2S {
			buf.WriteString(tsStreamHelper("streamSse"))
		} else {
			buf.WriteString(tsStreamHelper("streamNdjson"))
		}
	}
	if hasWebSocketMethods {
		buf.WriteString(tsW2SWebSocketHelpers())
	}

	// Client class — same name as gRPC client so consumers can swap transport
	fmt.Fprintf(&buf, "/**\n * %s provides gRPC-compatible API over REST transport.\n", clientName)
	buf.WriteString(" * Drop-in replacement for the gRPC client in browser environments.\n")
	buf.WriteString(" * Uses convention-based paths: POST /api/{package}/v1/{service}/{method}\n")
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
	if requireFullW2S {
		buf.WriteString(tsW2SClientHelpers(info.Package, hasWebSocketMethods))
	}
	buf.WriteString(methods.String())
	buf.WriteString("}\n")
	if extraTypes.Len() > 0 {
		buf.WriteString("\n")
		buf.WriteString(extraTypes.String())
	}

	if err := os.WriteFile(outputPath, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}

	// Create package.json
	if !proto.IsInfraProto(svc.ProtoFile) {
		if err := generateGrpcOverRestPackageJson(svc, outputDir); err != nil {
			return err
		}
	}

	return nil
}

func shouldEmitGrpcOverRestMethod(method *parser.MethodInfo, requireFullW2S bool) bool {
	// An MCP tool is not a gRPC-over-REST call. It has no route, so it emits no
	// method and — because this predicate also gates the cross-package import
	// block — no import of its request/response types either.
	if method.IsMcp {
		return false
	}
	if requireFullW2S {
		return method.HttpPath != ""
	}
	return method.HttpMethod != "" && !method.ClientStreaming
}

func tsW2SAuthRequired(svc *parser.ServiceInfo, method *parser.MethodInfo) bool {
	return !(svc.AuthDisabled || method.AuthDisabled)
}

func tsBoolLiteral(v bool) string {
	if v {
		return "true"
	}
	return "false"
}

func tsW2SHttpMethod(method *parser.MethodInfo, isEmptyInput bool) string {
	if isEmptyInput && method.HttpMethod != "" {
		return method.HttpMethod
	}
	return "POST"
}

func tsW2SBodyExpr(requestVar, inputSchema string, isEmptyInput bool) string {
	if isEmptyInput {
		return "{}"
	}
	return fmt.Sprintf("toJson(%s, %s, { alwaysEmitImplicit: true })", inputSchema, requestVar)
}

func tsW2SValidationStatement(varName, fullType, simpleType, servicePackage, indent string) string {
	validate := tsValidateRef(fullType, simpleType, servicePackage)
	if validate == "" {
		return ""
	}
	return fmt.Sprintf("%s%s(%s);\n", indent, validate, varName)
}

func tsW2SParseClosure(schema, fullType, simpleType, servicePackage string) string {
	validate := tsValidateRef(fullType, simpleType, servicePackage)
	if validate == "" {
		return fmt.Sprintf("(json) => fromJson(%s, json as any)", schema)
	}
	return fmt.Sprintf(`(json) => {
	            const message = fromJson(%s, json as any);
	            %s(message);
	            return message;
	        }`, schema, validate)
}

type tsW2SMethodResult struct {
	method    string
	extraType string
}

func tsW2SEnvelopeHelper() string {
	return `function unwrapW2SEnvelope(value: unknown): unknown {
    const envelope = value as any;
    if (envelope?.error) {
        throw new Error(String(envelope.error));
    }
    if (Object.prototype.hasOwnProperty.call(envelope ?? {}, 'result')) {
        return envelope.result;
    }
    if (envelope?.type === 'message' && Object.prototype.hasOwnProperty.call(envelope, 'message')) {
        return envelope.message;
    }
    return value ?? {};
}

`
}

func tsStreamHelper(name string) string {
	return fmt.Sprintf(`async function* %s<T>(
  response: Response,
  parse: (json: unknown) => T,
): AsyncGenerator<T> {
  const reader = response.body!.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer += decoder.decode(value, { stream: true });
      const lines = buffer.split('\n');
      buffer = lines.pop()!;
      for (const line of lines) {
        let trimmed = line.trim();
        if (!trimmed) continue;
        if (trimmed.startsWith('data: ')) trimmed = trimmed.slice(6);
        else if (trimmed.startsWith('event:') || trimmed.startsWith(':')) continue;
        const envelope = JSON.parse(trimmed);
        yield parse(unwrapW2SEnvelope(envelope));
      }
    }
    if (buffer.trim()) {
      let last = buffer.trim();
      if (last.startsWith('data: ')) last = last.slice(6);
      if (last && !last.startsWith('event:') && !last.startsWith(':')) {
        const envelope = JSON.parse(last);
        yield parse(unwrapW2SEnvelope(envelope));
      }
    }
  } finally {
    reader.releaseLock();
  }
}

`, name)
}

// generateTsGrpcOverRestLegacyMethod preserves the existing non-W2S gRPC-over-REST
// unary surface while W2S uses the stricter gateway contract below.
func generateTsGrpcOverRestLegacyMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, servicePackage string) string {
	methodLower := strutil.ToLowerCamelCase(method.Name)

	resolveTypePrefix := func(fullType string) string {
		parts := strings.Split(fullType, ".")
		if len(parts) > 1 {
			pkg := strings.Join(parts[:len(parts)-1], ".")
			if strings.HasPrefix(pkg, "google.protobuf") {
				return ""
			}
			if pkg != servicePackage {
				return pkg + "_types"
			}
		}
		return "types"
	}

	outputType := method.OutputType
	outputPrefix := resolveTypePrefix(method.FullOutputType)
	inputType := method.InputType
	inputPrefix := resolveTypePrefix(method.FullInputType)
	isEmptyInput := method.FullInputType == "google.protobuf.Empty" || method.InputType == "Empty"

	qualType := func(prefix, typeName string) string {
		if prefix == "" {
			return typeName
		}
		return prefix + "." + typeName
	}

	if isEmptyInput {
		return fmt.Sprintf(`    /**
	     * %s calls %s.%s via POST %s.
	     */
	    async %s(): Promise<%s> {
	        const json = await this.transport.postJson(`+"`${this.baseUrl}%s`"+`, {}, { service: '%s' });
	        return fromJson(%s, json as any);
	    }
	`, method.Name, svc.Name, method.Name, method.HttpPath,
			methodLower, qualType(outputPrefix, outputType),
			method.HttpPath,
			servicePackage,
			qualType(outputPrefix, outputType+"Schema"))
	}

	return fmt.Sprintf(`    /**
	     * %s calls %s.%s via POST %s.
	     */
	    async %s(request: %s): Promise<%s> {
	        const json = await this.transport.postJson(
	            `+"`${this.baseUrl}%s`"+`,
	            toJson(%s, request, { alwaysEmitImplicit: true }),
	            { service: '%s' },
	        );
	        return fromJson(%s, json as any);
	    }
	`, method.Name, svc.Name, method.Name, method.HttpPath,
		methodLower, qualType(inputPrefix, inputType), qualType(outputPrefix, outputType),
		method.HttpPath,
		qualType(inputPrefix, inputType+"Schema"),
		servicePackage,
		qualType(outputPrefix, outputType+"Schema"))
}

// generateTsGrpcOverRestLegacyStreamMethod preserves the existing non-W2S
// server-streaming surface, which consumes NDJSON-ish REST responses.
func generateTsGrpcOverRestLegacyStreamMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, info *parser.ProtoInfo) string {
	methodLower := strutil.ToLowerCamelCase(method.Name)

	resolveTypePrefix := func(fullType string) string {
		parts := strings.Split(fullType, ".")
		if len(parts) > 1 {
			pkg := strings.Join(parts[:len(parts)-1], ".")
			if pkg != info.Package && !strings.HasPrefix(pkg, "google.protobuf") {
				return pkg + "_types"
			}
		}
		return "types"
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
	     * %s streams %s.%s via GET %s (NDJSON).
	     */
	    async %s(opts?: { signal?: AbortSignal }): Promise<AsyncIterable<%s.%s>> {
	        const response = await this.transport.getStream(`+"`${this.baseUrl}%s`"+`, { ...opts, service: '%s' });
	        return streamNdjson(response, (json) => fromJson(%s.%sSchema, json as any));
	    }
	`, method.Name, svc.Name, method.Name, method.HttpPath,
				methodLower, outputPrefix, outputType,
				method.HttpPath,
				info.Package,
				outputPrefix, outputType)
		}
		return fmt.Sprintf(`    /**
	     * %s streams %s.%s via GET %s (NDJSON).
	     */
	    async %s(request: %s.%s, opts?: { signal?: AbortSignal }): Promise<AsyncIterable<%s.%s>> {
	        const response = await this.transport.getStream(`+"`${this.baseUrl}%s`"+`, { ...opts, service: '%s' });
	        return streamNdjson(response, (json) => fromJson(%s.%sSchema, json as any));
	    }
	`, method.Name, svc.Name, method.Name, method.HttpPath,
			methodLower, inputPrefix, inputType, outputPrefix, outputType,
			method.HttpPath,
			info.Package,
			outputPrefix, outputType)

	default:
		if isEmptyInput {
			return fmt.Sprintf(`    /**
	     * %s streams %s.%s via POST %s (NDJSON).
	     */
	    async %s(opts?: { signal?: AbortSignal }): Promise<AsyncIterable<%s.%s>> {
	        const response = await this.transport.postStream(`+"`${this.baseUrl}%s`"+`, {}, { ...opts, service: '%s' });
	        return streamNdjson(response, (json) => fromJson(%s.%sSchema, json as any));
	    }
	`, method.Name, svc.Name, method.Name, method.HttpPath,
				methodLower, outputPrefix, outputType,
				method.HttpPath,
				info.Package,
				outputPrefix, outputType)
		}
		return fmt.Sprintf(`    /**
	     * %s streams %s.%s via POST %s (NDJSON).
	     */
	    async %s(request: %s.%s, opts?: { signal?: AbortSignal }): Promise<AsyncIterable<%s.%s>> {
	        const response = await this.transport.postStream(
	            `+"`${this.baseUrl}%s`"+`,
	            toJson(%s.%sSchema, request, { alwaysEmitImplicit: true }),
	            { ...opts, service: '%s' },
	        );
	        return streamNdjson(response, (json) => fromJson(%s.%sSchema, json as any));
	    }
	`, method.Name, svc.Name, method.Name, method.HttpPath,
			methodLower, inputPrefix, inputType, outputPrefix, outputType,
			method.HttpPath,
			inputPrefix, inputType,
			info.Package,
			outputPrefix, outputType)
	}
}

// generateTsGrpcOverRestMethod generates a unary method that uses REST transport.
// Same signature as gRPC client method but sends JSON to the convention path.
func generateTsGrpcOverRestMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, servicePackage string, authRequired bool) string {
	methodLower := strutil.ToLowerCamelCase(method.Name)
	outputType := tsTypeRef(method.FullOutputType, method.OutputType, servicePackage)
	outputSchema := tsSchemaRef(method.FullOutputType, method.OutputType, servicePackage)
	inputType := tsTypeRef(method.FullInputType, method.InputType, servicePackage)
	inputSchema := tsSchemaRef(method.FullInputType, method.InputType, servicePackage)
	isEmptyInput := method.FullInputType == "google.protobuf.Empty" || method.InputType == "Empty"
	isEmptyOutput := method.FullOutputType == "google.protobuf.Empty" || method.OutputType == "Empty"
	httpMethod := tsW2SHttpMethod(method, isEmptyInput)
	bodyExpr := tsW2SBodyExpr("request", inputSchema, isEmptyInput)
	auth := tsBoolLiteral(authRequired)
	requestValidation := tsW2SValidationStatement("request", method.FullInputType, method.InputType, servicePackage, "        ")
	responseValidation := tsW2SValidationStatement("response", method.FullOutputType, method.OutputType, servicePackage, "        ")

	if isEmptyInput && isEmptyOutput {
		return fmt.Sprintf(`    /**
	     * %s calls %s.%s via W2S HTTP/JSON.
	     */
	    async %s(): Promise<void> {
	        await this.requestJson('%s', '%s', {}, %s);
	    }
	`, method.Name, svc.Name, method.Name,
			methodLower, httpMethod, method.HttpPath, auth)
	}

	if isEmptyInput {
		return fmt.Sprintf(`    /**
	     * %s calls %s.%s via W2S HTTP/JSON.
	     */
	    async %s(): Promise<%s> {
	        const json = await this.requestJson('%s', '%s', {}, %s);
	        const response = fromJson(%s, json as any);
%s	        return response;
	    }
	`, method.Name, svc.Name, method.Name,
			methodLower, outputType,
			httpMethod, method.HttpPath, auth,
			outputSchema, responseValidation)
	}

	if isEmptyOutput {
		return fmt.Sprintf(`    /**
	     * %s calls %s.%s via W2S HTTP/JSON.
	     */
	    async %s(request: %s): Promise<void> {
%s	        await this.requestJson('%s', '%s', %s, %s);
	    }
	`, method.Name, svc.Name, method.Name,
			methodLower, inputType, requestValidation,
			httpMethod, method.HttpPath, bodyExpr, auth)
	}

	return fmt.Sprintf(`    /**
	     * %s calls %s.%s via W2S HTTP/JSON.
	     */
	    async %s(request: %s): Promise<%s> {
%s	        const json = await this.requestJson('%s', '%s', %s, %s);
	        const response = fromJson(%s, json as any);
%s	        return response;
	    }
	`, method.Name, svc.Name, method.Name,
		methodLower, inputType, outputType, requestValidation,
		httpMethod, method.HttpPath, bodyExpr, auth,
		outputSchema, responseValidation)
}

// generateTsGrpcOverRestStreamMethod generates a server-streaming method using SSE.
func generateTsGrpcOverRestStreamMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, info *parser.ProtoInfo, authRequired bool) string {
	methodLower := strutil.ToLowerCamelCase(method.Name)
	outputType := tsTypeRef(method.FullOutputType, method.OutputType, info.Package)
	outputSchema := tsSchemaRef(method.FullOutputType, method.OutputType, info.Package)
	isEmptyInput := method.FullInputType == "google.protobuf.Empty" || method.InputType == "Empty"
	inputType := tsTypeRef(method.FullInputType, method.InputType, info.Package)
	inputSchema := tsSchemaRef(method.FullInputType, method.InputType, info.Package)
	httpMethod := tsW2SHttpMethod(method, isEmptyInput)
	auth := tsBoolLiteral(authRequired)
	requestValidation := tsW2SValidationStatement("request", method.FullInputType, method.InputType, info.Package, "        ")
	parseClosure := tsW2SParseClosure(outputSchema, method.FullOutputType, method.OutputType, info.Package)

	// Server-streaming uses GET for convention, POST with body
	switch httpMethod {
	case "GET":
		if isEmptyInput {
			return fmt.Sprintf(`    /**
	     * %s streams %s.%s via W2S SSE.
	     */
	    async %s(opts?: { signal?: AbortSignal }): Promise<AsyncIterable<%s>> {
	        const response = await this.transport.getStream(`+"`${this.baseUrl}%s`"+`, { ...opts, service: '%s', auth: %s });
	        return streamSse(response, %s);
	    }
		`, method.Name, svc.Name, method.Name,
				methodLower, outputType,
				method.HttpPath,
				info.Package,
				auth,
				parseClosure)
		}
		return fmt.Sprintf(`    /**
	     * %s streams %s.%s via W2S SSE.
	     */
	    async %s(request: %s, opts?: { signal?: AbortSignal }): Promise<AsyncIterable<%s>> {
%s	        const response = await this.transport.getStream(`+"`${this.baseUrl}%s`"+`, { ...opts, service: '%s', auth: %s });
	        return streamSse(response, %s);
	    }
		`, method.Name, svc.Name, method.Name,
			methodLower, inputType, outputType, requestValidation,
			method.HttpPath,
			info.Package,
			auth,
			parseClosure)

	default: // POST
		if isEmptyInput {
			return fmt.Sprintf(`    /**
	     * %s streams %s.%s via W2S SSE.
	     */
	    async %s(opts?: { signal?: AbortSignal }): Promise<AsyncIterable<%s>> {
	        const response = await this.transport.postStream(`+"`${this.baseUrl}%s`"+`, {}, { ...opts, service: '%s', auth: %s });
	        return streamSse(response, %s);
	    }
		`, method.Name, svc.Name, method.Name,
				methodLower, outputType,
				method.HttpPath,
				info.Package,
				auth,
				parseClosure)
		}
		return fmt.Sprintf(`    /**
	     * %s streams %s.%s via W2S SSE.
	     */
	    async %s(request: %s, opts?: { signal?: AbortSignal }): Promise<AsyncIterable<%s>> {
%s	        const response = await this.transport.postStream(
	            `+"`${this.baseUrl}%s`"+`,
	            toJson(%s, request, { alwaysEmitImplicit: true }),
	            { ...opts, service: '%s', auth: %s },
	        );
	        return streamSse(response, %s);
	    }
		`, method.Name, svc.Name, method.Name,
			methodLower, inputType, outputType, requestValidation,
			method.HttpPath,
			inputSchema,
			info.Package,
			auth,
			parseClosure)
	}
}

func generateTsGrpcOverRestClientStreamMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, servicePackage string, authRequired bool) string {
	methodLower := strutil.ToLowerCamelCase(method.Name)
	inputType := tsTypeRef(method.FullInputType, method.InputType, servicePackage)
	inputSchema := tsSchemaRef(method.FullInputType, method.InputType, servicePackage)
	outputType := tsTypeRef(method.FullOutputType, method.OutputType, servicePackage)
	outputSchema := tsSchemaRef(method.FullOutputType, method.OutputType, servicePackage)
	isEmptyOutput := method.FullOutputType == "google.protobuf.Empty" || method.OutputType == "Empty"
	auth := tsBoolLiteral(authRequired)
	requestValidation := tsW2SValidationStatement("request", method.FullInputType, method.InputType, servicePackage, "                ")
	responseValidation := tsW2SValidationStatement("response", method.FullOutputType, method.OutputType, servicePackage, "            ")

	if isEmptyOutput {
		return fmt.Sprintf(`    /**
	     * %s calls %s.%s via W2S WebSocket client streaming.
	     */
	    async %s(requests: AsyncIterable<%s> | Iterable<%s>, opts?: { signal?: AbortSignal }): Promise<void> {
	        const ws = await this.openWebSocket('%s', %s, opts?.signal);
	        try {
	            await this.sendWsStart(ws, %s);
	            for await (const request of toAsyncIterable(requests)) {
	                this.throwIfAborted(opts?.signal);
%s	                await this.sendWsMessage(ws, toJson(%s, request, { alwaysEmitImplicit: true }), %s);
	            }
	            await this.sendWsComplete(ws);
	            await this.waitWsComplete(ws, opts?.signal);
	        } finally {
	            this.closeWebSocket(ws);
	        }
	    }
	`, method.Name, svc.Name, method.Name,
			methodLower, inputType, inputType,
			method.HttpPath, auth,
			auth,
			requestValidation, inputSchema, auth)
	}

	return fmt.Sprintf(`    /**
	     * %s calls %s.%s via W2S WebSocket client streaming.
	     */
	    async %s(requests: AsyncIterable<%s> | Iterable<%s>, opts?: { signal?: AbortSignal }): Promise<%s> {
	        const ws = await this.openWebSocket('%s', %s, opts?.signal);
	        try {
	            await this.sendWsStart(ws, %s);
	            for await (const request of toAsyncIterable(requests)) {
	                this.throwIfAborted(opts?.signal);
%s	                await this.sendWsMessage(ws, toJson(%s, request, { alwaysEmitImplicit: true }), %s);
	            }
	            await this.sendWsComplete(ws);
	            const json = await this.recvWsJson(ws, opts?.signal);
	            const response = fromJson(%s, json as any);
%s	            return response;
	        } finally {
	            this.closeWebSocket(ws);
	        }
	    }
	`, method.Name, svc.Name, method.Name,
		methodLower, inputType, inputType, outputType,
		method.HttpPath, auth,
		auth,
		requestValidation, inputSchema, auth,
		outputSchema, responseValidation)
}

func generateTsGrpcOverRestBidiMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, servicePackage string, authRequired bool) tsW2SMethodResult {
	methodLower := strutil.ToLowerCamelCase(method.Name)
	inputType := tsTypeRef(method.FullInputType, method.InputType, servicePackage)
	inputSchema := tsSchemaRef(method.FullInputType, method.InputType, servicePackage)
	outputType := tsTypeRef(method.FullOutputType, method.OutputType, servicePackage)
	outputSchema := tsSchemaRef(method.FullOutputType, method.OutputType, servicePackage)
	auth := tsBoolLiteral(authRequired)
	serializeClosure := fmt.Sprintf("(message) => toJson(%s, message, { alwaysEmitImplicit: true })", inputSchema)
	if inputValidation := tsW2SValidationStatement("message", method.FullInputType, method.InputType, servicePackage, "	                "); inputValidation != "" {
		serializeClosure = fmt.Sprintf(`(message) => {
%s	                return toJson(%s, message, { alwaysEmitImplicit: true });
	            }`, inputValidation, inputSchema)
	}
	parseClosure := tsW2SParseClosure(outputSchema, method.FullOutputType, method.OutputType, servicePackage)

	methodCode := fmt.Sprintf(`    /**
	     * %s opens a managed W2S bidirectional stream to %s.%s.
	     */
	    %s(
	        onMessage: (message: %s) => void,
	        opts?: W2SBidiOptions<%s>,
	    ): W2SStreamHandle<%s> {
	        const handle = new W2SStreamHandleImpl<%s>();
	        void this.runWsBidi(
	            handle,
	            '%s',
	            %s,
	            %s,
	            %s,
	            onMessage,
	            opts,
	        );
	        return handle;
	    }
	`, method.Name, svc.Name, method.Name,
		methodLower,
		outputType,
		inputType,
		inputType,
		inputType,
		method.HttpPath,
		auth,
		serializeClosure,
		parseClosure)

	return tsW2SMethodResult{method: methodCode}
}

func tsW2SWebSocketHelpers() string {
	return `class W2SStreamHandleImpl<T> implements W2SStreamHandle<T> {
    private static readonly MAX_BUFFER = 1024;
    private sender?: (message: T) => Promise<void>;
    private cancel?: () => void;
    private buffer: Array<{ message: T; resolve: () => void; reject: (error: unknown) => void }> = [];
    private closed = false;
    private sendChain: Promise<void> = Promise.resolve();

    send(message: T): Promise<void> {
        if (this.closed) {
            return Promise.reject(new Error('W2S stream is closed'));
        }
        if (this.sender) {
            return this.sender(message);
        }
        if (this.buffer.length >= W2SStreamHandleImpl.MAX_BUFFER) {
            const dropped = this.buffer.shift();
            dropped?.reject(new Error('W2S stream send buffer full'));
        }
        return new Promise<void>((resolve, reject) => {
            this.buffer.push({ message, resolve, reject });
        });
    }

    close(): void {
        this.closed = true;
        const pending = this.buffer;
        this.buffer = [];
        for (const item of pending) {
            item.reject(new Error('W2S stream is closed'));
        }
        if (this.cancel) {
            try { this.cancel(); } catch { /* no-op */ }
        }
    }

    isClosed(): boolean {
        return this.closed;
    }

    _attach(sender: (message: T) => Promise<void>, cancel: () => void): void {
        this.sendChain = Promise.resolve();
        const chainedSender = (message: T) => {
            const next = this.sendChain.then(() => sender(message));
            this.sendChain = next.catch(() => {});
            return next;
        };
        this.sender = chainedSender;
        this.cancel = cancel;
        const pending = this.buffer;
        this.buffer = [];
        for (const item of pending) {
            chainedSender(item.message).then(item.resolve, item.reject);
        }
    }

    _detach(): void {
        this.sender = undefined;
        this.cancel = undefined;
        this.sendChain = Promise.resolve();
    }

    _fail(error: unknown): void {
        const pending = this.buffer;
        this.buffer = [];
        for (const item of pending) {
            item.reject(error);
        }
    }
}

async function* toAsyncIterable<T>(input: AsyncIterable<T> | Iterable<T>): AsyncIterable<T> {
    if (Symbol.asyncIterator in Object(input)) {
        for await (const item of input as AsyncIterable<T>) {
            yield item;
        }
        return;
    }
    for (const item of input as Iterable<T>) {
        yield item;
    }
}

function w2sSleep(ms: number, signal?: AbortSignal): Promise<void> {
    return new Promise((resolve, reject) => {
        if (signal?.aborted) {
            reject(new Error('W2S operation aborted'));
            return;
        }
        const timer = setTimeout(resolve, ms);
        const abort = () => {
            clearTimeout(timer);
            reject(new Error('W2S operation aborted'));
        };
        signal?.addEventListener('abort', abort, { once: true });
    });
}

`
}

func tsW2SClientHelpers(serviceName string, hasWebSocketMethods bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, `    private async requestJson(method: string, path: string, body: unknown, auth: boolean): Promise<unknown> {
        const url = `+"`${this.baseUrl}${path}`"+`;
        const opts: W2STransportOptions = { service: '%s', auth };
        if (this.transport.requestJson) {
            return unwrapW2SEnvelope(await this.transport.requestJson(method, url, body, opts));
        }
        if (method === 'POST') {
            return unwrapW2SEnvelope(await this.transport.postJson(url, body, opts));
        }
        throw new Error(`+"`"+`W2S transport cannot issue ${method} JSON requests without requestJson()`+"`"+`);
    }

`, serviceName)
	if !hasWebSocketMethods {
		return b.String()
	}
	b.WriteString(`    private async authHeader(authRequired: boolean): Promise<string | undefined> {
        if (!authRequired) return undefined;
        if (!this.transport.authHeader) {
            throw new Error('W2S authenticated WebSocket frames require RestTransport.authHeader()');
        }
        const header = await this.transport.authHeader('` + serviceName + `');
        if (!header) {
            throw new Error('W2S authenticated WebSocket frames require a bearer token');
        }
        return header;
    }

    private throwIfAborted(signal?: AbortSignal): void {
        if (signal?.aborted) {
            throw new Error('W2S operation aborted');
        }
    }

    private async openWebSocket(path: string, authRequired: boolean, signal?: AbortSignal): Promise<WebSocket> {
        this.throwIfAborted(signal);
        if (!this.transport.openWebSocket) {
            throw new Error('W2S WebSocket methods require RestTransport.openWebSocket()');
        }
        const ws = await this.transport.openWebSocket(this.baseUrl + path, { service: '` + serviceName + `', auth: authRequired, signal });
        this.throwIfAborted(signal);
        return ws;
    }

    private closeWebSocket(ws: WebSocket): void {
        if (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING) {
            ws.close();
        }
    }

    private async sendWsFrame(ws: WebSocket, frame: unknown): Promise<void> {
        if (ws.readyState !== WebSocket.OPEN) {
            throw new Error('W2S WebSocket is not open');
        }
        ws.send(JSON.stringify(frame));
    }

    private async sendWsStart(ws: WebSocket, authRequired: boolean): Promise<void> {
        const authorization = await this.authHeader(authRequired);
        await this.sendWsFrame(ws, authorization ? { type: 'start', authorization } : { type: 'start' });
    }

    private async sendWsMessage(ws: WebSocket, message: unknown, authRequired: boolean): Promise<void> {
        const authorization = await this.authHeader(authRequired);
        await this.sendWsFrame(ws, authorization ? { type: 'message', message, authorization } : { type: 'message', message });
    }

    private async sendWsComplete(ws: WebSocket): Promise<void> {
        await this.sendWsFrame(ws, { type: 'complete' });
    }

	    private decodeWsPayload(data: unknown): unknown {
	        if (typeof data !== 'string') {
	            throw new Error('W2S WebSocket binary responses are not supported');
	        }
        const envelope = JSON.parse(data) as any;
        if (envelope?.type === 'complete') {
            return undefined;
        }
        if (Object.prototype.hasOwnProperty.call(envelope ?? {}, 'message')) {
            return envelope.message;
        }
	        return unwrapW2SEnvelope(envelope);
	    }

	    private isWsCompleteFrame(data: unknown): boolean {
	        if (typeof data !== 'string') return false;
	        try {
	            return JSON.parse(data)?.type === 'complete';
	        } catch {
	            return false;
	        }
	    }

	    private recvWsJson(ws: WebSocket, signal?: AbortSignal): Promise<unknown> {
	        return new Promise((resolve, reject) => {
            const cleanup = () => {
                ws.removeEventListener('message', onMessage);
                ws.removeEventListener('error', onError);
                ws.removeEventListener('close', onClose);
                signal?.removeEventListener('abort', onAbort);
	            };
	            const onMessage = (event: MessageEvent) => {
	                try {
	                    if (this.isWsCompleteFrame(event.data)) {
	                        cleanup();
	                        reject(new Error('W2S stream completed before response'));
	                        return;
	                    }
	                    const payload = this.decodeWsPayload(event.data);
	                    if (payload === undefined) return;
	                    cleanup();
                    resolve(payload);
                } catch (err) {
                    cleanup();
                    reject(err);
                }
            };
            const onError = () => {
                cleanup();
                reject(new Error('W2S WebSocket receive failed'));
            };
            const onClose = () => {
                cleanup();
                reject(new Error('W2S stream closed before response'));
            };
            const onAbort = () => {
                cleanup();
                this.closeWebSocket(ws);
                reject(new Error('W2S operation aborted'));
            };
            ws.addEventListener('message', onMessage);
            ws.addEventListener('error', onError);
            ws.addEventListener('close', onClose);
	            signal?.addEventListener('abort', onAbort, { once: true });
	        });
	    }

	    private waitWsComplete(ws: WebSocket, signal?: AbortSignal): Promise<void> {
	        return new Promise((resolve, reject) => {
	            const cleanup = () => {
	                ws.removeEventListener('message', onMessage);
	                ws.removeEventListener('error', onError);
	                ws.removeEventListener('close', onClose);
	                signal?.removeEventListener('abort', onAbort);
	            };
	            const onMessage = (event: MessageEvent) => {
	                try {
	                    if (this.isWsCompleteFrame(event.data)) {
	                        cleanup();
	                        resolve();
	                        return;
	                    }
	                    const payload = this.decodeWsPayload(event.data);
	                    if (payload !== undefined) {
	                        cleanup();
	                        resolve();
	                    }
	                } catch (err) {
	                    cleanup();
	                    reject(err);
	                }
	            };
	            const onError = () => {
	                cleanup();
	                reject(new Error('W2S WebSocket receive failed'));
	            };
	            const onClose = () => {
	                cleanup();
	                resolve();
	            };
	            const onAbort = () => {
	                cleanup();
	                this.closeWebSocket(ws);
	                reject(new Error('W2S operation aborted'));
	            };
	            ws.addEventListener('message', onMessage);
	            ws.addEventListener('error', onError);
	            ws.addEventListener('close', onClose);
	            signal?.addEventListener('abort', onAbort, { once: true });
	        });
	    }

    private readWsMessages<Output>(
        ws: WebSocket,
        parse: (json: unknown) => Output,
        onMessage: (message: Output) => void,
        signal?: AbortSignal,
    ): Promise<void> {
        return new Promise((resolve, reject) => {
            const cleanup = () => {
                ws.removeEventListener('message', onMessageEvent);
                ws.removeEventListener('error', onError);
                ws.removeEventListener('close', onClose);
                signal?.removeEventListener('abort', onAbort);
            };
	            const onMessageEvent = (event: MessageEvent) => {
	                try {
	                    if (this.isWsCompleteFrame(event.data)) {
	                        cleanup();
	                        resolve();
	                        return;
	                    }
	                    const payload = this.decodeWsPayload(event.data);
	                    if (payload === undefined) return;
	                    onMessage(parse(payload));
                } catch (err) {
                    cleanup();
                    reject(err);
                }
            };
            const onError = () => {
                cleanup();
                reject(new Error('W2S WebSocket receive failed'));
            };
            const onClose = () => {
                cleanup();
                resolve();
            };
            const onAbort = () => {
                cleanup();
                this.closeWebSocket(ws);
                reject(new Error('W2S operation aborted'));
            };
            ws.addEventListener('message', onMessageEvent);
            ws.addEventListener('error', onError);
            ws.addEventListener('close', onClose);
            signal?.addEventListener('abort', onAbort, { once: true });
        });
    }

    private async runWsBidi<Input, Output>(
        handle: W2SStreamHandleImpl<Input>,
        path: string,
        authRequired: boolean,
        serialize: (message: Input) => unknown,
        parse: (json: unknown) => Output,
        onMessage: (message: Output) => void,
        opts?: W2SBidiOptions<Input>,
    ): Promise<void> {
        const reconnect = opts?.reconnect ?? true;
        const reconnectDelayMs = opts?.reconnectDelayMs ?? 5000;
        while (!handle.isClosed()) {
            let ws: WebSocket | undefined;
	            try {
	                ws = await this.openWebSocket(path, authRequired, opts?.signal);
	                await this.sendWsStart(ws, authRequired);
	                handle._attach(
	                    (message) => this.sendWsMessage(ws!, serialize(message), authRequired),
	                    () => { if (ws) this.closeWebSocket(ws); },
	                );
	                await opts?.onOpen?.(handle);
	                await this.readWsMessages(ws, parse, onMessage, opts?.signal);
            } catch (err) {
                opts?.onError?.(err);
                if (!reconnect || opts?.signal?.aborted || handle.isClosed()) {
                    handle._fail(err);
                    break;
                }
            } finally {
                handle._detach();
                if (ws) this.closeWebSocket(ws);
            }
            if (!reconnect || opts?.signal?.aborted || handle.isClosed()) {
                break;
            }
            try {
                await w2sSleep(reconnectDelayMs, opts?.signal);
            } catch (err) {
                handle._fail(err);
                break;
            }
        }
    }

`)
	return b.String()
}

// generateGrpcOverRestPackageJson creates package.json and tsconfig.json for a gRPC-over-REST package.
func generateGrpcOverRestPackageJson(svc ServiceInfo, outputDir string) error {
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
		"grpcoverrest")

	var b strings.Builder
	b.WriteString("{\n")
	fmt.Fprintf(&b, "\"name\": \"%s\",\n", npmPackageName)
	b.WriteString("\"version\": \"1.0.0\",\n")
	fmt.Fprintf(&b, "\"description\": \"TypeScript gRPC-over-REST client for %s\",\n", actualServiceName)
	b.WriteString("\"main\": \"index.ts\",\n")
	b.WriteString("\"type\": \"module\",\n")
	b.WriteString("\"scripts\": {\n")
	b.WriteString("\"build\": \"tsc\"\n")
	b.WriteString("},\n")
	b.WriteString("\"dependencies\": {\n")
	fmt.Fprintf(&b, "\"@bufbuild/protobuf\": \"%s\"\n", bufbuildVersion)
	b.WriteString("},\n")
	b.WriteString("\"keywords\": [\"protobuf\", \"typescript\", \"grpc\", \"rest\", \"client\"],\n")
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
    "lib": ["ES2020", "DOM"],
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
