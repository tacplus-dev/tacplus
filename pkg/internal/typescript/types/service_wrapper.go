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
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/consts"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// ServiceInfo is a type alias for common service.ServiceInfo.
type ServiceInfo = service.ServiceInfo

// detectServicesForWrapper scans proto files to find service definitions that need wrapper generation.
func detectServicesForWrapper(config *tactype.Config, inputDir string) ([]ServiceInfo, error) {
	return service.DetectServicesForWrapper(config, inputDir, func(config *tactype.Config, relPath, parentDir, packageName string) string {
		var subdir string
		if proto.IsInfraProto(relPath) {
			subdir = "spec"
		} else {
			subdir = filepath.Join(config.GetSmPrefix(), "internal", "grpc", "servers", parentDir, packageName)
		}
		return filepath.Join(config.OutputDir, subdir)
	})
}

// generateServiceWrappers generates servicewrapper.ts for each service in the services directory.
// Uses a single DescriptorParser rooted at inputDir so protoc can resolve cross-package imports.
//
// Returns, keyed by ServiceName, whether that wrapper ended up with accounting
// wired in (i.e. defines setEmitter) — post Q2-exclusion. setEmitter is NOT
// unconditional in the TypeScript wrapper (it is gated behind
// {{if .HasAccounting}}, same bug class Java had), so the ServiceManager
// template's setEmitter(...) wiring loop must guard on this per service.
func generateServiceWrappers(servicesInfo []ServiceInfo, inputDir string, accountingMap map[string]service.AccountingInfo) (map[string]bool, error) {
	// Create one descriptor parser for the entire proto root
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	// Q2: resolve the accounting topic once so every wrapper can drop that
	// topic's own publish RPC from its accounting map. See
	// accounting_exclusion.go for why this exclusion is mandatory.
	accountingTopic, hasAccountingTopic, err := boundaries.LookupAccountingRecordTopic(descriptorParser)
	if err != nil {
		return nil, fmt.Errorf("resolve accounting record topic: %w", err)
	}

	hasAccountingByService := make(map[string]bool, len(servicesInfo))

	for _, svc := range servicesInfo {
		// Use the descriptor parser to get method information
		protoInfo, err := descriptorParser.ConvertToProtoInfo(svc.ProtoFile)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", svc.ProtoFile, err)
		}
		fileDesc, err := descriptorParser.GetFileDescriptor(svc.ProtoFile)
		if err != nil {
			return nil, fmt.Errorf("failed to get descriptor for %s: %w", svc.ProtoFile, err)
		}

		svcAccountingMap := excludeAccountingSelfPublish(
			accountingMap, svc.GrpcName, protoInfo.Package, svc.ServiceName,
			accountingTopic, hasAccountingTopic)

		// Generate servicewrapper.ts in the services output directory
		hasAccounting, err := generateServiceWrapper(svc, protoInfo, fileDesc, svcAccountingMap)
		if err != nil {
			return nil, fmt.Errorf("failed to generate servicewrapper for %s: %w", svc.ServiceName, err)
		}
		hasAccountingByService[svc.ServiceName] = hasAccounting
	}
	return hasAccountingByService, nil
}

// generateServiceWrapper creates the servicewrapper.ts file for a service
func generateServiceWrapper(svc ServiceInfo, info *parser.ProtoInfo, fileDesc *parser.FileDescriptor, accountingMap map[string]service.AccountingInfo) (bool, error) {
	// Ensure output directory exists (created here, not in detectServicesForWrapper,
	// so directories are only created for services that pass gRPC filtering)
	if err := os.MkdirAll(svc.OutputDir, 0755); err != nil {
		return false, fmt.Errorf("failed to create output directory: %w", err)
	}

	// Determine wrapper filename, stripping redundant package prefix
	wrapperFile := strutil.StripPackagePrefix(tsGeneratedSnakeName(svc.ServiceName), svc.PackageName) + ".ts"
	outputPath := filepath.Join(svc.OutputDir, wrapperFile)

	zap.L().Info("Generating TypeScript service wrapper", zap.String("path", outputPath))

	// Find the target service in the proto info
	var targetService *parser.ServiceInfo
	for i := range info.Services {
		if info.Services[i].Name == svc.ServiceName {
			targetService = info.Services[i]
			break
		}
	}

	serviceName := svc.ServiceName
	serviceLabel := svc.GrpcName
	if serviceLabel == "" {
		serviceLabel = svc.ServiceName
	}

	// Derive the actual service name and compute correct types import path.
	actualServiceName := tsActualServicePackage(svc)
	wrapperDir := filepath.Join(svc.Config.GetSmPrefix(), "internal", "grpc", "servers", svc.ParentDir, actualServiceName)
	ownTypesDir := typescriptModuleDirForProtoDescriptor("", svc.ProtoFile, fileDesc)
	typesImportRel, _ := filepath.Rel(wrapperDir, ownTypesDir)
	collectionRel, _ := filepath.Rel(wrapperDir, filepath.Join("chassis", "collection"))
	traceRel, _ := filepath.Rel(wrapperDir, filepath.Join("types", "sys", "trace"))
	accountingRel, _ := filepath.Rel(wrapperDir, filepath.Join("chassis", "am"))
	accountingMethodRel, _ := filepath.Rel(wrapperDir, filepath.Join("types", "generated", "accounting_method"))

	// Build cross-package imports
	usedPkgs := make(map[string]bool)
	if targetService != nil {
		for _, method := range targetService.Methods {
			fullTypes := []string{method.FullInputType, method.FullOutputType}
			if method.IsWatchCollection() {
				fullTypes = append(fullTypes, method.Resolved.WatchItemType, method.Resolved.WatchChangeType)
			}
			for _, fullType := range fullTypes {
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

	// Build handler interface, service definition, and method implementations
	var handlerInterface, serviceDefinition, grpcMethods, watchFields strings.Builder
	var methodAuthLines []string
	hasGoogleEmpty := false

	if targetService != nil && len(targetService.Methods) > 0 {
		// Check if google.protobuf.Empty is used
		for _, method := range targetService.Methods {
			if method.FullInputType == "google.protobuf.Empty" || method.FullOutputType == "google.protobuf.Empty" {
				hasGoogleEmpty = true
				break
			}
		}

		// Generate handler interface
		fmt.Fprintf(&handlerInterface, "/**\n * %sHandler defines the handler interface for %s.\n * Implement this interface to provide business logic for each RPC method.\n */\nexport interface %sHandler {\n", serviceName, serviceName, serviceName)
		for _, method := range targetService.Methods {
			handlerInterface.WriteString(generateTsHandlerMethodSignature(method, info.Package))
		}
		handlerInterface.WriteString("}\n")

		// Generate service definition entries and method implementations
		for i, method := range targetService.Methods {
			if i > 0 {
				serviceDefinition.WriteString("\n")
				grpcMethods.WriteString("\n")
			}
			if method.IsWatchCollection() {
				changeType := tsTypeRef(method.Resolved.WatchChangeType, simpleTypeName(method.Resolved.WatchChangeType), info.Package)
				fmt.Fprintf(&watchFields, "    private readonly %sWatchHub = new WatchHub<%s>(%d, %d);\n",
					strutil.ToLowerCamelCase(method.Name), changeType, method.Resolved.ReplayDepth, method.Resolved.IdleSecs)
			}
			serviceDefinition.WriteString(generateTsServiceDefinitionEntry(targetService, method, info.Package))
			grpcMethods.WriteString(generateTsGRPCWrapperMethod(targetService, method, info.Package, svc.ParentDir, svc.GrpcName, accountingMap))

			// Per-method policy is keyed by the SERVICE-QUALIFIED method
			// identifier the Authorizer derives from the incoming full gRPC
			// method (qualifiedMethodKey), never by the bare rpc name: one
			// process may host several services behind one Authorizer, and a
			// bare-name key lets a second service's identically named rpc
			// silently overwrite the first's auth_services.
			methodAuthKey := tsQualifiedMethodAuthKey(info.Package, targetService.Name, method.Name)

			// Resolve effective auth_services: method-level overrides service-level.
			effectiveAuth := svc.AuthServices
			if len(method.AuthServices) > 0 {
				effectiveAuth = method.AuthServices
			}
			if len(effectiveAuth) > 0 && !svc.AuthDisabled &&
				!method.AuthDisabled && !method.AuthAnyUser {
				var quoted []string
				for _, pattern := range effectiveAuth {
					quoted = append(quoted, fmt.Sprintf("%q", pattern))
				}
				methodAuthLines = append(methodAuthLines, fmt.Sprintf(
					"        authorizer.setMethodAuth(%q, [%s]);",
					methodAuthKey, strings.Join(quoted, ", ")))
			}

			if method.TargetNamespaceField != "" && !method.AuthAnyUser {
				methodAuthLines = append(methodAuthLines, fmt.Sprintf(
					"        authorizer.setMethodTargetNamespaceField(%q, %q);",
					methodAuthKey, method.TargetNamespaceField))
			}
		}
	}

	// Load template from embedded filesystem
	tmplContent, err := loadTemplate("service_wrapper.ts.tmpl")
	if err != nil {
		return false, fmt.Errorf("failed to load service_wrapper template: %w", err)
	}

	// Determine wrapper name from annotation
	wrapperName := "GrpcServer"
	if !svc.IsGrpc {
		wrapperName = "StateClient"
	}

	hasHandler := targetService != nil && len(targetService.Methods) > 0
	hasAccounting := serviceHasAccounting(targetService, svc.GrpcName, accountingMap)

	data := struct {
		Header                 string
		TypesImport            string
		TraceImport            string
		AccountingImport       string
		AccountingMethodImport string
		CollectionImport       string
		CrossPackageImports    string
		WatchFields            string
		ServiceName            string
		ServiceLabel           string
		WrapperName            string
		HasAccounting          bool
		HasHandler             bool
		HasGoogleEmpty         bool
		HandlerInterface       string
		ServiceDefinition      string
		GRPCMethods            string
		RegisterMethodAuth     string
		ProtoPackage           string
	}{
		Header:                 header.GeneratedHeader(consts.LangTypeScript, svc.ProtoFile),
		TypesImport:            typesImportRel + "/index.js",
		TraceImport:            filepath.ToSlash(traceRel) + "/index.js",
		AccountingImport:       filepath.ToSlash(accountingRel) + "/index.js",
		AccountingMethodImport: filepath.ToSlash(accountingMethodRel) + "/index.js",
		CollectionImport:       filepath.ToSlash(collectionRel) + "/index.js",
		CrossPackageImports:    crossPkgImports.String(),
		WatchFields:            watchFields.String(),
		ServiceName:            serviceName,
		ServiceLabel:           serviceLabel,
		WrapperName:            wrapperName,
		HasAccounting:          hasAccounting,
		HasHandler:             hasHandler,
		HasGoogleEmpty:         hasGoogleEmpty,
		HandlerInterface:       handlerInterface.String(),
		ServiceDefinition:      serviceDefinition.String(),
		GRPCMethods:            grpcMethods.String(),
		RegisterMethodAuth:     strings.Join(methodAuthLines, "\n"),
		ProtoPackage:           info.Package,
	}

	t, err := template.New("servicewrapper").Parse(tmplContent)
	if err != nil {
		return false, fmt.Errorf("failed to parse template: %w", err)
	}

	file, err := os.Create(outputPath)
	if err != nil {
		return false, fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	if err := t.Execute(file, data); err != nil {
		return false, fmt.Errorf("failed to execute template: %w", err)
	}

	return hasAccounting, nil
}

// tsTypeRef returns the TypeScript type reference for a method's input/output type.
// Same-package types use "types.TypeName", cross-package use "{pkg}_types.TypeName",
// and google.protobuf.Empty uses "Empty".
func tsTypeRef(fullType, simpleType, servicePackage string) string {
	if fullType == "google.protobuf.Empty" {
		return "Empty"
	}
	parts := strings.Split(fullType, ".")
	if len(parts) > 1 {
		pkg := strings.Join(parts[:len(parts)-1], ".")
		if pkg != servicePackage && pkg != "" && pkg != "google.protobuf" {
			return pkg + "_types." + simpleType
		}
	}
	return "types." + simpleType
}

func simpleTypeName(fullType string) string {
	if i := strings.LastIndex(fullType, "."); i >= 0 {
		return fullType[i+1:]
	}
	return fullType
}

// tsSchemaRef returns the protobuf-es Schema reference for serialization.
// Same-package types use "types.TypeNameSchema", cross-package use "{pkg}_types.TypeNameSchema",
// and google.protobuf.Empty uses "EmptySchema".
func tsSchemaRef(fullType, simpleType, servicePackage string) string {
	if fullType == "google.protobuf.Empty" {
		return "EmptySchema"
	}
	parts := strings.Split(fullType, ".")
	if len(parts) > 1 {
		pkg := strings.Join(parts[:len(parts)-1], ".")
		if pkg != servicePackage && pkg != "" && pkg != "google.protobuf" {
			return pkg + "_types." + simpleType + "Schema"
		}
	}
	return "types." + simpleType + "Schema"
}

// tsValidateRef returns the validation function call for a type.
// Same-package types use "types.validateTypeName(msg)", cross-package use "{pkg}_types.validateTypeName(msg)".
// Returns empty string for google.protobuf.Empty (no validation needed).
func tsValidateRef(fullType, simpleType, servicePackage string) string {
	if strings.HasPrefix(fullType, "google.protobuf.") {
		return ""
	}
	parts := strings.Split(fullType, ".")
	if len(parts) > 1 {
		pkg := strings.Join(parts[:len(parts)-1], ".")
		if pkg != servicePackage && pkg != "" && pkg != "google.protobuf" {
			return pkg + "_types.validate" + simpleType
		}
	}
	return "types.validate" + simpleType
}

func inputValidationBlockForTS(validateRef, valueRef string) string {
	if validateRef == "" {
		return ""
	}
	return fmt.Sprintf("%s(%s);", validateRef, valueRef)
}

func serviceHasAccounting(svc *parser.ServiceInfo, grpcName string, accountingMap map[string]service.AccountingInfo) bool {
	if svc == nil || grpcName == "" {
		return false
	}
	info, ok := accountingMap[grpcName]
	if !ok {
		return false
	}
	for _, method := range svc.Methods {
		if info.RPCs[method.Name] {
			return true
		}
	}
	return false
}

func accountingRPCOrdinal(info service.AccountingInfo, methodName string) int {
	if info.RPCOrdinals == nil {
		return 0
	}
	return info.RPCOrdinals[methodName]
}

func tsAccountingMethodExpression(grpcName, methodName string, accountingMap map[string]service.AccountingInfo) string {
	if grpcName == "" {
		return ""
	}
	info, ok := accountingMap[grpcName]
	if !ok || !info.RPCs[methodName] {
		return ""
	}
	ordinal := accountingRPCOrdinal(info, methodName)
	if ordinal == 0 {
		return ""
	}
	fieldName := strutil.ToLowerCamelCase(grpcName)
	return fmt.Sprintf("create(accountingTypes.AccountingMethodSchema, { %s: %d })", fieldName, ordinal)
}

func tsAccountingSuccessBlock(methodExpr, metadataExpr, startExpr string) string {
	if methodExpr == "" {
		return ""
	}
	return fmt.Sprintf("this.emitAccounting(%s, %s, null, %s);", methodExpr, startExpr, metadataExpr)
}

func tsAccountingErrorBlock(methodExpr, metadataExpr, startExpr, errExpr string) string {
	if methodExpr == "" {
		return ""
	}
	return fmt.Sprintf("this.emitAccounting(%s, %s, %s, %s);", methodExpr, startExpr, errExpr, metadataExpr)
}

func tsAccountingTryPreamble(methodExpr string) string {
	if methodExpr == "" {
		return ""
	}
	return `                    const __tacplusAccountingStart = Date.now();
                    let __tacplusAccountingError: unknown = null;
`
}

func tsAccountingCatchMark(methodExpr, errExpr string) string {
	if methodExpr == "" {
		return ""
	}
	return fmt.Sprintf(`
	                        __tacplusAccountingError = %s;`, errExpr)
}

func tsAccountingFinally(methodExpr, metadataExpr string) string {
	if methodExpr == "" {
		return ""
	}
	return fmt.Sprintf(`
	                    } finally {
	                        this.emitAccounting(%s, __tacplusAccountingStart, __tacplusAccountingError, %s);`, methodExpr, metadataExpr)
}

// generateTsHandlerMethodSignature generates a single handler interface method signature.
func generateTsHandlerMethodSignature(method *parser.MethodInfo, servicePackage string) string {
	methodName := strutil.ToLowerCamelCase(method.Name)
	inputType := tsTypeRef(method.FullInputType, method.InputType, servicePackage)
	outputType := tsTypeRef(method.FullOutputType, method.OutputType, servicePackage)
	emptyInput := method.FullInputType == "google.protobuf.Empty"
	emptyOutput := method.FullOutputType == "google.protobuf.Empty"

	switch {
	case method.ClientStreaming && method.ServerStreaming:
		// Bidi streaming — pass the duplex stream directly
		return fmt.Sprintf("    %s(stream: grpc.ServerDuplexStream<any, any>): Promise<void>;\n", methodName)
	case method.IsTier2SnapshotCollection():
		// Tier-2 SNAPSHOT collection — handler returns a materialized array; the
		// wrapper chunks it into the server-stream with flow control.
		if emptyInput {
			return fmt.Sprintf("    %s(): Promise<%s[]>;\n", methodName, outputType)
		}
		return fmt.Sprintf("    %s(req: %s): Promise<%s[]>;\n", methodName, inputType, outputType)
	case method.IsTier3SnapshotCollection():
		// Tier-3 SNAPSHOT cursor — handler returns a lazy async producer; the
		// wrapper drives it with flow control and `for await … break` runs the
		// producer's `.return()` cleanup on teardown.
		if emptyInput {
			return fmt.Sprintf("    %s(): AsyncIterable<%s>;\n", methodName, outputType)
		}
		return fmt.Sprintf("    %s(req: %s): AsyncIterable<%s>;\n", methodName, inputType, outputType)
	case method.IsWatchCollection():
		itemType := tsTypeRef(method.Resolved.WatchItemType, simpleTypeName(method.Resolved.WatchItemType), servicePackage)
		changeType := tsTypeRef(method.Resolved.WatchChangeType, simpleTypeName(method.Resolved.WatchChangeType), servicePackage)
		if emptyInput {
			return fmt.Sprintf("    %s(): Promise<WatchSource<%s, %s>>;\n", methodName, itemType, changeType)
		}
		return fmt.Sprintf("    %s(req: %s): Promise<WatchSource<%s, %s>>;\n", methodName, inputType, itemType, changeType)
	case method.IsPageCollection():
		// PAGE — a unary, client-driven window. The wrapper owns the page_token
		// codec and the page_size bound; the handler is a keyset producer: given
		// PageParams{limit, after} (plus req for the filter fields) it returns one
		// Page (items + the cursor to resume after + an optional total).
		itemType := tsTypeRef(method.Resolved.PageItemType, simpleTypeName(method.Resolved.PageItemType), servicePackage)
		if emptyInput {
			return fmt.Sprintf("    %s(page: PageParams): Promise<Page<%s>>;\n", methodName, itemType)
		}
		return fmt.Sprintf("    %s(req: %s, page: PageParams): Promise<Page<%s>>;\n", methodName, inputType, itemType)
	case method.ServerStreaming:
		// Server streaming — pass request (or nothing for Empty) and writable stream
		if emptyInput {
			return fmt.Sprintf("    %s(stream: grpc.ServerWritableStream<any, any>): Promise<void>;\n", methodName)
		}
		return fmt.Sprintf("    %s(req: %s, stream: grpc.ServerWritableStream<any, any>): Promise<void>;\n", methodName, inputType)
	case method.ClientStreaming:
		// Client streaming — pass the readable stream, return response
		if emptyOutput {
			return fmt.Sprintf("    %s(stream: grpc.ServerReadableStream<any, any>): Promise<void>;\n", methodName)
		}
		return fmt.Sprintf("    %s(stream: grpc.ServerReadableStream<any, any>): Promise<%s>;\n", methodName, outputType)
	default:
		// Unary
		if emptyInput && emptyOutput {
			return fmt.Sprintf("    %s(): Promise<void>;\n", methodName)
		} else if emptyInput {
			return fmt.Sprintf("    %s(): Promise<%s>;\n", methodName, outputType)
		} else if emptyOutput {
			return fmt.Sprintf("    %s(req: %s): Promise<void>;\n", methodName, inputType)
		}
		return fmt.Sprintf("    %s(req: %s): Promise<%s>;\n", methodName, inputType, outputType)
	}
}

// generateTsServiceDefinitionEntry generates a single entry for the gRPC service definition object.
func generateTsServiceDefinitionEntry(svc *parser.ServiceInfo, method *parser.MethodInfo, servicePackage string) string {
	inputSchema := tsSchemaRef(method.FullInputType, method.InputType, servicePackage)
	outputSchema := tsSchemaRef(method.FullOutputType, method.OutputType, servicePackage)

	requestStream := method.ClientStreaming
	responseStream := method.ServerStreaming

	return fmt.Sprintf(`            %s: {
                path: '/%s.%s/%s',
                requestStream: %v,
                responseStream: %v,
                requestSerialize: (msg: any) => Buffer.from(toBinary(%s, msg)),
                requestDeserialize: (buf: Buffer) => fromBinary(%s, new Uint8Array(buf)),
                responseSerialize: (msg: any) => Buffer.from(toBinary(%s, msg)),
                responseDeserialize: (buf: Buffer) => fromBinary(%s, new Uint8Array(buf)),
            },
`, method.Name, servicePackage, svc.Name, method.Name, requestStream, responseStream, inputSchema, inputSchema, outputSchema, outputSchema)
}

// generateTsGRPCWrapperMethod generates a single gRPC method implementation for the wrapper.
func generateTsGRPCWrapperMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, servicePackage, parentDir, grpcName string, accountingMap map[string]service.AccountingInfo) string {
	methodName := strutil.ToLowerCamelCase(method.Name)
	emptyInput := method.FullInputType == "google.protobuf.Empty"
	emptyOutput := method.FullOutputType == "google.protobuf.Empty"
	fullMethodPath := fmt.Sprintf("/%s.%s/%s", servicePackage, svc.Name, method.Name)

	authDisabled := method.AuthDisabled || svc.AuthDisabled || svc.IsC2D
	authAnyUser := method.AuthAnyUser

	validateInput := tsValidateRef(method.FullInputType, method.InputType, servicePackage)
	validateOutput := tsValidateRef(method.FullOutputType, method.OutputType, servicePackage)
	outputType := tsTypeRef(method.FullOutputType, method.OutputType, servicePackage)
	outputSchema := tsSchemaRef(method.FullOutputType, method.OutputType, servicePackage)
	inputValidationRef := "null"
	if validateInput != "" {
		inputValidationRef = validateInput
	}
	outputValidationRef := "null"
	if validateOutput != "" {
		outputValidationRef = validateOutput
	}
	accountingMethodExpr := tsAccountingMethodExpression(grpcName, method.Name, accountingMap)
	traceNoErrorArgs := ""
	traceErrorArgs := "err?.message ?? String(err)"
	if accountingMethodExpr != "" {
		traceNoErrorArgs = fmt.Sprintf(", '', %s", accountingMethodExpr)
		traceErrorArgs = fmt.Sprintf("err?.message ?? String(err), %s", accountingMethodExpr)
	}

	// Build auth call based on auth mode.
	authLineFor := func(metadataExpr, reqExpr string) string {
		if authDisabled {
			return ""
		}
		if authAnyUser {
			targetNamespace := strings.TrimSpace(method.TargetNamespaceField)
			if targetNamespace == "" {
				targetNamespace = "/"
			}
			return fmt.Sprintf("await this.authenticate(%s, %q);", metadataExpr, targetNamespace)
		}
		return fmt.Sprintf("await this.authorize('%s', %s, %s);", fullMethodPath, metadataExpr, reqExpr)
	}
	needsStreamMessageAuth := method.ClientStreaming && method.TargetNamespaceField != "" && !authDisabled && !authAnyUser

	// Streaming methods — delegate directly to handler with auth check
	if method.ClientStreaming && method.ServerStreaming {
		authLine := authLineFor("stream.metadata", "null")
		streamAuthorize := "undefined"
		if needsStreamMessageAuth {
			authLine = "await this.authenticate(stream.metadata);"
			streamAuthorize = fmt.Sprintf("async (msg: any) => { await this.authorize('%s', stream.metadata, msg); }", fullMethodPath)
		}
		return fmt.Sprintf(`            %s: (stream: grpc.ServerDuplexStream<any, any>) => {
	                (async () => {
%s
	                    try {
	                        %s
	                        const validatedStream = this.wrapReadableForRequestValidation(
	                            this.wrapWritableForResponseValidation(stream, %s),
	                            %s,
	                            %s,
	                        );
	                        await this.handler.%s(validatedStream);
	                    } catch (err: any) {
%s
	                        stream.emit('error', this.toGrpcError(err));
%s
	                    }
	                })();
	            },
	`, method.Name, tsAccountingTryPreamble(accountingMethodExpr), authLine, outputValidationRef, inputValidationRef, streamAuthorize, methodName, tsAccountingCatchMark(accountingMethodExpr, "err"), tsAccountingFinally(accountingMethodExpr, "stream.metadata"))
	}

	if method.IsTier3SnapshotCollection() {
		// Tier-3 SNAPSHOT cursor: handler returns a lazy async producer; drive it
		// with WS-4 flow control (call.write() bool + 'drain'). `for await` over the
		// producer calls its `.return()` on loop exit/throw → deterministic teardown.
		tier3HandlerCall := fmt.Sprintf("this.handler.%s(call.request)", methodName)
		if emptyInput {
			tier3HandlerCall = fmt.Sprintf("this.handler.%s()", methodName)
		}
		authLine := authLineFor("call.metadata", "call.request")
		return fmt.Sprintf(`            %s: (call: grpc.ServerWritableStream<any, any>) => {
	                (async () => {
%s
	                    try {
	                        %s
	                        %s
	                        const validatedCall = this.wrapWritableForResponseValidation(call, %s);
	                        for await (const item of %s) {
	                            if (!validatedCall.write(item)) {
	                                await new Promise<void>((resolve) => call.once('drain', resolve));
	                            }
	                        }
	                        call.end();
	                    } catch (err: any) {
%s
	                        call.emit('error', this.toGrpcError(err));
%s
	                    }
	                })();
	            },
	`, method.Name, tsAccountingTryPreamble(accountingMethodExpr), authLine, inputValidationBlockForTS(validateInput, "call.request"), outputValidationRef, tier3HandlerCall, tsAccountingCatchMark(accountingMethodExpr, "err"), tsAccountingFinally(accountingMethodExpr, "call.metadata"))
	}

	if method.IsTier2SnapshotCollection() {
		// Tier-2 SNAPSHOT collection: handler returns a materialized array; chunk it
		// into the server-stream with WS-4 flow control — honor the call.write()
		// boolean and await 'drain' so a slow client throttles production.
		tier2HandlerCall := fmt.Sprintf("await this.handler.%s(call.request)", methodName)
		if emptyInput {
			tier2HandlerCall = fmt.Sprintf("await this.handler.%s()", methodName)
		}
		authLine := authLineFor("call.metadata", "call.request")
		return fmt.Sprintf(`            %s: (call: grpc.ServerWritableStream<any, any>) => {
	                (async () => {
%s
	                    try {
	                        %s
	                        %s
	                        const validatedCall = this.wrapWritableForResponseValidation(call, %s);
	                        const items = %s;
	                        for (const item of items) {
	                            if (!validatedCall.write(item)) {
	                                await new Promise<void>((resolve) => call.once('drain', resolve));
	                            }
	                        }
	                        call.end();
	                    } catch (err: any) {
%s
	                        call.emit('error', this.toGrpcError(err));
%s
	                    }
	                })();
	            },
	`, method.Name, tsAccountingTryPreamble(accountingMethodExpr), authLine, inputValidationBlockForTS(validateInput, "call.request"), outputValidationRef, tier2HandlerCall, tsAccountingCatchMark(accountingMethodExpr, "err"), tsAccountingFinally(accountingMethodExpr, "call.metadata"))
	}

	if method.IsWatchCollection() {
		authLine := authLineFor("call.metadata", "call.request")
		outputSchema := tsSchemaRef(method.FullOutputType, method.OutputType, servicePackage)
		fromRevisionExpr := "Number((call.request as any).fromRevision ?? (call.request as any).from_revision ?? 0)"
		sourceCall := fmt.Sprintf("await this.handler.%s(call.request)", methodName)
		if emptyInput {
			sourceCall = fmt.Sprintf("await this.handler.%s()", methodName)
		}
		return fmt.Sprintf(`            %s: (call: grpc.ServerWritableStream<any, any>) => {
	                (async () => {
%s
	                    try {
	                        %s
	                        %s
	                        const validatedCall = this.wrapWritableForResponseValidation(call, %s);
	                        const source = %s;
	                        const fromRevision = %s;
	                        const resumeRevision = fromRevision === 0 ? Number(source.revision) : fromRevision;
	                        const writeEvent = async (event: %s): Promise<void> => {
	                            if (!validatedCall.write(event)) {
	                                await new Promise<void>((resolve) => call.once('drain', resolve));
	                            }
	                        };
	                        if (fromRevision === 0) {
	                            for await (const item of this.toAsyncIterable(source.snapshot)) {
	                                await writeEvent(create(%s, { %s: { case: 'snapshotItem', value: item } }));
	                            }
	                            await writeEvent(create(%s, { %s: { case: 'snapshotDone', value: BigInt(source.revision) } }));
	                        }
	                        for await (const change of this.%sWatchHub.watch(source.scope, resumeRevision, source.changes, (c: any) => c.revision)) {
	                            await writeEvent(create(%s, { %s: { case: 'delta', value: change } }));
	                        }
	                        call.end();
	                    } catch (err: any) {
%s
	                        call.emit('error', this.toGrpcError(err));
%s
	                    }
	                })();
	            },
	`, method.Name, tsAccountingTryPreamble(accountingMethodExpr), authLine, inputValidationBlockForTS(validateInput, "call.request"), outputValidationRef,
			sourceCall, fromRevisionExpr, outputType,
			outputSchema, method.Resolved.WatchOneOf,
			outputSchema, method.Resolved.WatchOneOf,
			methodName,
			outputSchema, method.Resolved.WatchOneOf, tsAccountingCatchMark(accountingMethodExpr, "err"), tsAccountingFinally(accountingMethodExpr, "call.metadata"))
	}

	if method.ServerStreaming {
		handlerCall := fmt.Sprintf("this.handler.%s(call.request, call)", methodName)
		if emptyInput {
			handlerCall = fmt.Sprintf("this.handler.%s(call)", methodName)
		}
		authLine := authLineFor("call.metadata", "call.request")
		return fmt.Sprintf(`            %s: (call: grpc.ServerWritableStream<any, any>) => {
	                (async () => {
%s
	                    try {
	                        %s
	                        %s
	                        const validatedCall = this.wrapWritableForResponseValidation(call, %s);
	                        await %s;
	                    } catch (err: any) {
%s
	                        call.emit('error', this.toGrpcError(err));
%s
	                    }
	                })();
	            },
	`, method.Name, tsAccountingTryPreamble(accountingMethodExpr), authLine, inputValidationBlockForTS(validateInput, "call.request"), outputValidationRef, strings.ReplaceAll(handlerCall, "call)", "validatedCall)"), tsAccountingCatchMark(accountingMethodExpr, "err"), tsAccountingFinally(accountingMethodExpr, "call.metadata"))
	}

	if method.ClientStreaming {
		clientAuthLine := authLineFor("call.metadata", "null")
		streamAuthorize := "undefined"
		if needsStreamMessageAuth {
			clientAuthLine = "await this.authenticate(call.metadata);"
			streamAuthorize = fmt.Sprintf("async (msg: any) => { await this.authorize('%s', call.metadata, msg); }", fullMethodPath)
		}
		return fmt.Sprintf(`            %s: (call: grpc.ServerReadableStream<any, any>, callback: grpc.sendUnaryData<any>) => {
	                (async () => {
%s
	                    try {
	                        %s
	                        const validatedCall = this.wrapReadableForRequestValidation(call, %s, %s);
	                        const resp = await this.handler.%s(validatedCall);
	                        this.validateResponse(%s, resp);
	                        callback(null, resp);
	                    } catch (err: any) {
%s
	                        callback(this.toGrpcError(err));
%s
	                    }
	                })();
	            },
	`, method.Name, tsAccountingTryPreamble(accountingMethodExpr), clientAuthLine, inputValidationRef, streamAuthorize, methodName, outputValidationRef, tsAccountingCatchMark(accountingMethodExpr, "err"), tsAccountingFinally(accountingMethodExpr, "call.metadata"))
	}

	if method.IsPageCollection() {
		// PAGE: unary on the wire. The wrapper owns the page_token codec and the
		// trust-tier page_size bound; the handler is a keyset producer returning a
		// Page. A malformed page_token throws BadPageTokenError → INVALID_ARGUMENT
		// (never a silent reset to page 1).
		itemsKey := strutil.ToLowerCamelCase(method.Resolved.PageItemsField)
		outputSchema := tsSchemaRef(method.FullOutputType, method.OutputType, servicePackage)
		pageAuthLine := authLineFor("call.metadata", "call.request")
		var pageInputValidation string
		if validateInput != "" && !emptyInput {
			pageInputValidation = fmt.Sprintf(`
                        %s(call.request);`, validateInput)
		}
		handlerCall := fmt.Sprintf("this.handler.%s(call.request, { limit, after })", methodName)
		if emptyInput {
			handlerCall = fmt.Sprintf("this.handler.%s({ limit, after })", methodName)
		}
		totalField := ""
		if method.Resolved.PageHasTotal {
			// total_count is reported only when the handler computes it (>= 0); a
			// negative total leaves the field at 0 = "unknown", not "empty".
			// total_count is int64 -> protobuf-es bigint, so coerce the number.
			totalField = `
                            totalCount: page.total >= 0 ? BigInt(page.total) : 0n,`
		}
		return fmt.Sprintf(`            %s: (call: grpc.ServerUnaryCall<any, any>, callback: grpc.sendUnaryData<any>) => {
	                (async () => {
%s
	                    try {
	                        %s%s
	                        const after = decodePageToken(String((call.request as any).pageToken ?? (call.request as any).page_token ?? ''));
	                        const limit = effectivePageSize(Number((call.request as any).pageSize ?? (call.request as any).page_size ?? 0), %d, %d);
	                        const page = await %s;
	                        const resp = create(%s, {
	                            %s: page.items,
	                            nextPageToken: encodePageToken(page.nextAfter),%s
	                        });
	                        this.validateResponse(%s, resp);
	                        callback(null, resp);
	                    } catch (err: any) {
%s
	                        callback(this.toGrpcError(err));
%s
	                    }
	                })();
	            },
	`, method.Name, tsAccountingTryPreamble(accountingMethodExpr), pageAuthLine, pageInputValidation,
			method.Resolved.DefaultPageSize, method.Resolved.MaxPageSize, handlerCall,
			outputSchema, itemsKey, totalField, outputValidationRef,
			tsAccountingCatchMark(accountingMethodExpr, "err"), tsAccountingFinally(accountingMethodExpr, "call.metadata"))
	}

	// Unary method with validation
	var inputValidationBlock, outputValidationBlock string
	if validateInput != "" && !emptyInput {
		inputValidationBlock = fmt.Sprintf(`
                        %s(call.request);`, validateInput)
	}
	if validateOutput != "" && !emptyOutput {
		outputValidationBlock = fmt.Sprintf(`
	                        this.validateResponse(%s, resp);`, outputValidationRef)
	}

	// Build handler call
	handlerCall := fmt.Sprintf("this.handler.%s(call.request)", methodName)
	if emptyInput {
		handlerCall = fmt.Sprintf("this.handler.%s()", methodName)
	}

	unaryAuthLine := authLineFor("call.metadata", "call.request")

	if emptyOutput {
		return fmt.Sprintf(`            %s: (call: grpc.ServerUnaryCall<any, any>, callback: grpc.sendUnaryData<any>) => {
	                (async () => {
	                    let traceId = '';
%s
	                    try {
	                        %s%s
	                        traceId = this.traceIdFromRequest(call.request);
	                        const isNoop = traceId !== '' && this.noopFromRequest(call.request);
	                        if (traceId !== '') {
	                            this.publishTrace(traceId, 3%s);
	                            if (isNoop) {
	                                callback(null, {});
	                                return;
	                            }
	                        }
	                        await %s;
	                        if (traceId !== '') {
	                            this.publishTrace(traceId, 4%s);
	                        }
	                        callback(null, {});
	                    } catch (err: any) {
%s
	                        if (traceId !== '') {
	                            this.publishTrace(traceId, 4, %s);
	                        }
	                        callback(this.toGrpcError(err));
%s
	                    }
	                })();
	            },
	`, method.Name, tsAccountingTryPreamble(accountingMethodExpr), unaryAuthLine, inputValidationBlock, traceNoErrorArgs, handlerCall, traceNoErrorArgs, tsAccountingCatchMark(accountingMethodExpr, "err"), traceErrorArgs, tsAccountingFinally(accountingMethodExpr, "call.metadata"))
	}

	return fmt.Sprintf(`            %s: (call: grpc.ServerUnaryCall<any, any>, callback: grpc.sendUnaryData<any>) => {
	                (async () => {
	                    let traceId = '';
%s
	                    try {
	                        %s%s
	                        traceId = this.traceIdFromRequest(call.request);
	                        const isNoop = traceId !== '' && this.noopFromRequest(call.request);
	                        if (traceId !== '') {
	                            this.publishTrace(traceId, 3%s);
	                            if (isNoop) {
	                                callback(null, create(%s, {}));
	                                return;
	                            }
	                        }
	                        const resp = await %s;%s
	                        if (traceId !== '') {
	                            this.publishTrace(traceId, 4%s);
	                        }
	                        callback(null, resp);
	                    } catch (err: any) {
%s
	                        if (traceId !== '') {
	                            this.publishTrace(traceId, 4, %s);
	                        }
	                        callback(this.toGrpcError(err));
%s
	                    }
	                })();
	            },
	`, method.Name, tsAccountingTryPreamble(accountingMethodExpr), unaryAuthLine, inputValidationBlock, traceNoErrorArgs, outputSchema, handlerCall, outputValidationBlock, traceNoErrorArgs, tsAccountingCatchMark(accountingMethodExpr, "err"), traceErrorArgs, tsAccountingFinally(accountingMethodExpr, "call.metadata"))
}

// generateServicesPackageJson creates package.json and tsconfig.json for each services/ directory.
// This is called after service wrappers are generated so that the services/ directory has its own
// npm package with dependencies and a dependency on the sibling types package.
func generateServicesPackageJson(servicesInfo []ServiceInfo) error {
	// Track which directories we've already created package.json for (multiple services may share a dir)
	created := make(map[string]bool)

	for _, svc := range servicesInfo {
		if created[svc.OutputDir] {
			continue
		}
		created[svc.OutputDir] = true

		if proto.IsInfraProto(svc.ProtoFile) {
			continue // Sys directory is flat, handled separately
		}

		basePackage := svc.Config.TsPackageBase
		if basePackage == "" {
			return fmt.Errorf("TsPackageBase is required for TypeScript generation")
		}

		// Package name: e.g., @myclient-proto/services-baseball-services
		npmPackageName := buildNpmPackageName(basePackage,
			strings.ReplaceAll(svc.ParentDir, "/", "-"),
			"services")

		var b strings.Builder
		b.WriteString("{\n")
		fmt.Fprintf(&b, "\"name\": \"%s\",\n", npmPackageName)
		b.WriteString("\"version\": \"1.0.0\",\n")
		fmt.Fprintf(&b, "\"description\": \"TypeScript service wrapper for %s proto package\",\n", svc.PackageName)
		b.WriteString("\"main\": \"index.ts\",\n")
		b.WriteString("\"exports\": {\n")
		b.WriteString("\".\": \"./index.ts\",\n")
		b.WriteString("\"./*\": \"./*\"\n")
		b.WriteString("},\n")
		b.WriteString("\"type\": \"module\",\n")
		b.WriteString("\"scripts\": {\n")
		b.WriteString("\"build\": \"tsc\"\n")
		b.WriteString("},\n")
		b.WriteString("\"keywords\": [\"protobuf\", \"typescript\", \"services\"],\n")
		b.WriteString("\"license\": \"UNLICENSED\",\n")
		b.WriteString("\"private\": true\n")
		b.WriteString("}\n")

		packageJsonPath := filepath.Join(svc.OutputDir, "package.json")
		if err := os.WriteFile(packageJsonPath, []byte(b.String()), 0644); err != nil {
			return fmt.Errorf("failed to write services package.json: %w", err)
		}

		zap.L().Debug("Created services package.json", zap.String("package", npmPackageName))

		// Also create tsconfig.json
		tsconfigPath := filepath.Join(svc.OutputDir, "tsconfig.json")
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
  "exclude": ["node_modules", "dist", "*.test.ts", "*_test.ts"]
}
`
		if err := os.WriteFile(tsconfigPath, []byte(tsconfig), 0644); err != nil {
			return fmt.Errorf("failed to write services tsconfig.json: %w", err)
		}
	}

	return nil
}

// tsQualifiedMethodAuthKey builds the SERVICE-QUALIFIED method identifier that
// keys the Authorizer's per-method policy maps: "pkg.Service/Method", the last
// two segments of the full gRPC method path the wrapper passes to authorize().
// Must stay in lockstep with qualifiedMethodKey in the grpcserver chassis
// (pkg/templates/typescript/chassis/grpcserver/Authorizer.ts.tmpl).
func tsQualifiedMethodAuthKey(protoPackage, serviceName, methodName string) string {
	if protoPackage == "" {
		return serviceName + "/" + methodName
	}
	return protoPackage + "." + serviceName + "/" + methodName
}
