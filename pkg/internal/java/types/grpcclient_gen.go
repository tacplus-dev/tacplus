// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

type javaResolvedGrpcMethod struct {
	method     *parser.MethodInfo
	inputType  string
	outputType string
}

// generatedGrpcClientJava tracks info about a generated gRPC client wrapper for aggregators.
type generatedGrpcClientJava struct {
	PackageName string // e.g. "baseball"
	ServiceName string // proto service name: "BaseballService"
	ClassName   string // e.g. "BaseballServiceGrpcClient"
	ParentDir   string // e.g. "services"
	FullPackage string // e.g. "com.example.wrappers.grpcclients.services.baseball"
	ProtoFile   string
	HostLabel     string
}

// generateGrpcClients generates typed gRPC client wrappers for each gRPC service.
func generateGrpcClients(config *tactype.Config, servicesInfo []ServiceInfo, inputDir string) error {
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	var generated []generatedGrpcClientJava

	for _, svc := range servicesInfo {
		protoInfo, err := descriptorParser.ConvertToProtoInfo(svc.ProtoFile)
		if err != nil {
			return fmt.Errorf("failed to parse %s: %w", svc.ProtoFile, err)
		}

		if err := generateGrpcClient(svc, protoInfo); err != nil {
			return fmt.Errorf("failed to generate gRPC client for %s: %w", svc.ServiceName, err)
		}

		// Determine full package for aggregator — must match the actual client file path
		normalizedRelPath := strings.TrimPrefix(svc.ProtoFile, "tacplus"+string(filepath.Separator))
		typeDir := strings.Split(normalizedRelPath, string(filepath.Separator))[0]
		fullPackage := fmt.Sprintf("%s."+config.GetSmPrefix()+".internal.grpc.clients.%s.%s", config.JavaPackageBase, typeDir, svc.PackageName)

		className := svc.ServiceName + "GrpcClient"
		generated = append(generated, generatedGrpcClientJava{
			PackageName: svc.PackageName,
			ServiceName: svc.ServiceName,
			ClassName:   className,
			ParentDir:   strings.Split(normalizedRelPath, string(filepath.Separator))[0],
			FullPackage: fullPackage,
			ProtoFile:   svc.ProtoFile,
			HostLabel:     svc.HostLabel,
		})
	}

	// Generate group aggregators
	if err := generateGrpcClientGroupAggregators(config, generated); err != nil {
		return fmt.Errorf("failed to generate gRPC client group aggregators: %w", err)
	}

	// Generate root aggregator
	if err := generateGrpcClientRootAggregator(config, generated); err != nil {
		return fmt.Errorf("failed to generate root gRPC client aggregator: %w", err)
	}

	return nil
}

// generateGrpcClient creates the typed gRPC client wrapper Java file for a service.
func generateGrpcClient(svc ServiceInfo, info *parser.ProtoInfo) error {
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

	// Compute output directory and Java package
	normalizedRelPath := strings.TrimPrefix(svc.ProtoFile, "tacplus"+string(filepath.Separator))
	protoDir := filepath.Join(svc.Config.OutputDir, "proto")
	typesProtoFileName := svc.PackageName

	typeDir := strings.Split(normalizedRelPath, string(filepath.Separator))[0]
	var fullPackage, typesPackage, grpcStubsPackage string
	fullPackage = fmt.Sprintf("%s."+svc.Config.GetSmPrefix()+".internal.grpc.clients.%s.%s", svc.Config.JavaPackageBase, typeDir, svc.PackageName)
	typesPackage = fmt.Sprintf("%s.types.%s.%s", svc.Config.JavaPackageBase, typeDir, svc.PackageName)
	grpcStubsPackage = typesPackage

	svcName, svcFileName := proto.ServiceProtoParts(svc.ProtoFile)
	isFlatServiceProto := svcName == svcFileName

	srcDir := filepath.Join(svc.Config.OutputDir, "src", "main", "java")
	packagePath := strings.ReplaceAll(fullPackage, ".", string(filepath.Separator))
	outputDir := filepath.Join(srcDir, packagePath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Detect outer class name
	var outerClassName string
	if isFlatServiceProto {
		typesProtoPath := filepath.Join(protoDir, svc.ProtoFile)
		if name := extractJavaOuterClassName(typesProtoPath); name != "" {
			outerClassName = name
		} else {
			outerClassName = getOuterClassNameForWrapper(svc.TypesOutputDir, svc.ProtoFile)
		}
	} else if typesProtoFileName != "" {
		typesProtoPath := filepath.Join(protoDir, typeDir, svc.PackageName, typesProtoFileName+".proto")
		if name := extractJavaOuterClassName(typesProtoPath); name != "" {
			outerClassName = name
		} else {
			outerClassName = getOuterClassNameForWrapper(svc.TypesOutputDir, svc.ProtoFile)
		}
	} else {
		outerClassName = getOuterClassNameForWrapper(svc.TypesOutputDir, svc.ProtoFile)
	}

	// Build cross-package outer class name and Java package maps.
	crossPkgOuterClasses := make(map[string]string)
	crossPkgJavaPackages := make(map[string]string)
	for pkg, dir := range info.CrossPackageTypes {
		if dir == "" {
			continue
		}
		dirParts := strings.Split(dir, string(filepath.Separator))
		crossJavaPackage := javaPackageForProtoPackage(svc.Config.JavaPackageBase, pkg, dir)
		crossPkgJavaPackages[pkg] = crossJavaPackage
		var typeSubPkg string
		if len(dirParts) >= 2 {
			typeSubPkg = pkg
		}
		crossTypesSrcDir := filepath.Join(svc.Config.OutputDir, "src", "main", "java")
		var crossTypesPath string
		if typeSubPkg != "" {
			crossTypesPath = filepath.Join(crossTypesSrcDir, strings.ReplaceAll(crossJavaPackage+"."+typeSubPkg, ".", string(filepath.Separator)))
		} else {
			crossTypesPath = filepath.Join(crossTypesSrcDir, strings.ReplaceAll(crossJavaPackage, ".", string(filepath.Separator)))
		}
		crossProtoFile := filepath.Join(dir, pkg+".proto")
		crossOuterClass := getOuterClassNameForWrapper(crossTypesPath, crossProtoFile)
		crossPkgOuterClasses[pkg] = crossOuterClass
	}

	// Build methods and collect the exact outer classes referenced by signatures.
	var resolvedMethods []javaResolvedGrpcMethod
	neededImports := map[string]struct{}{}
	hasUnary := false
	hasAuth := false
	hasCursor := false
	hasPage := false
	crossPkgHelpers := make(map[string]string)
	for pkg, dir := range info.CrossPackageTypes {
		if dir == "" {
			continue
		}
		crossDirParts := strings.Split(dir, string(filepath.Separator))
		crossJavaPackage := javaPackageForProtoPackage(svc.Config.JavaPackageBase, pkg, dir)
		if len(crossDirParts) >= 2 {
			crossPkgHelpers[pkg] = crossJavaPackage + "." + pkg + ".Helpers"
		} else {
			crossPkgHelpers[pkg] = crossJavaPackage + ".Helpers"
		}
	}
	var methods strings.Builder
	for _, method := range targetService.Methods {
		inputType := resolveJavaMethodType(method.FullInputType, method.InputType, info.Package, outerClassName, crossPkgOuterClasses)
		outputType := resolveJavaMethodType(method.FullOutputType, method.OutputType, info.Package, outerClassName, crossPkgOuterClasses)
		resolvedMethods = append(resolvedMethods, javaResolvedGrpcMethod{
			method:     method,
			inputType:  inputType,
			outputType: outputType,
		})
		collectGrpcClientTypeImport(neededImports, method.FullInputType, info.Package, typesPackage, inputType, crossPkgJavaPackages)
		collectGrpcClientTypeImport(neededImports, method.FullOutputType, info.Package, typesPackage, outputType, crossPkgJavaPackages)
		if !method.ClientStreaming && !method.ServerStreaming {
			hasUnary = true
		}
		// Collection clients (Tier-2 materialize, Tier-3 cursor, WATCH cursor) use the blocking stub.
		if method.IsTier2SnapshotCollection() || method.IsTier3SnapshotCollection() || method.IsWatchCollection() {
			hasUnary = true
		}
		if method.IsTier3SnapshotCollection() {
			hasCursor = true
		}
		if method.IsPageCollection() {
			// PAGE explicit-page seam returns CollectionRuntime.PageResult; the
			// auto-pager is unary on the wire (blocking stub).
			hasPage = true
			hasUnary = true
		}
		if javaGrpcClientAuthRequired(targetService, method) {
			hasAuth = true
		}
		inputValidator := javaW2SValidationRef(method.FullInputType, method.InputType, info.Package, crossPkgHelpers)
		outputValidator := javaW2SValidationRef(method.FullOutputType, method.OutputType, info.Package, crossPkgHelpers)
		if strings.HasPrefix(inputValidator, "Helpers.") || strings.HasPrefix(outputValidator, "Helpers.") {
			neededImports[fmt.Sprintf("import %s.Helpers;", typesPackage)] = struct{}{}
		}
	}
	for _, resolvedMethod := range resolvedMethods {
		method := resolvedMethod.method
		inputType := resolvedMethod.inputType
		outputType := resolvedMethod.outputType
		inputValidator := javaW2SValidationRef(method.FullInputType, method.InputType, info.Package, crossPkgHelpers)
		outputValidator := javaW2SValidationRef(method.FullOutputType, method.OutputType, info.Package, crossPkgHelpers)
		methods.WriteString(generateJavaGrpcClientMethod(targetService, method, inputType, outputType, inputValidator, outputValidator, crossPkgOuterClasses))
		methods.WriteString("\n")
	}

	// Build exact imports for the types referenced by method signatures.
	importLines := make(map[string]struct{})
	if grpcStubsPackage != "" {
		importLines[fmt.Sprintf("import %s.%sGrpc;", grpcStubsPackage, svc.ServiceName)] = struct{}{}
	}
	for importLine := range neededImports {
		importLines[importLine] = struct{}{}
	}
	importLines[fmt.Sprintf("import %s.types.sys.errors.ProtoException;", svc.Config.JavaPackageBase)] = struct{}{}
	if hasPage {
		importLines[fmt.Sprintf("import %s.chassis.collection.CollectionRuntime;", svc.Config.JavaPackageBase)] = struct{}{}
	}

	var sortedImportLines []string
	for importLine := range importLines {
		sortedImportLines = append(sortedImportLines, importLine)
	}
	sort.Strings(sortedImportLines)
	typesImport := strings.Join(sortedImportLines, "\n")

	className := svc.ServiceName + "GrpcClient"
	outputPath := filepath.Join(outputDir, className+".java")

	zap.L().Info("Generating Java gRPC client", zap.String("path", outputPath))

	// Determine if any methods use streaming
	hasStreaming := false
	for _, method := range targetService.Methods {
		if method.ClientStreaming || method.ServerStreaming {
			hasStreaming = true
			break
		}
	}

	data := struct {
		Header       string
		FullPackage  string
		TypesImport  string
		ServiceName  string
		ClientName   string
		Methods      string
		HasStreaming bool
		HasUnary     bool
		HasAuth      bool
		HasCursor    bool
	}{
		Header:       header.GeneratedHeader(consts.LangJava, svc.ProtoFile),
		FullPackage:  fullPackage,
		TypesImport:  typesImport,
		ServiceName:  svc.ServiceName,
		ClientName:   className,
		Methods:      methods.String(),
		HasStreaming: hasStreaming,
		HasUnary:     hasUnary,
		HasAuth:      hasAuth,
		HasCursor:    hasCursor,
	}

	tmplStr, err := loadTemplate("grpcclient.java.tmpl")
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

func collectGrpcClientTypeImport(imports map[string]struct{}, fullType, servicePackage, localTypesPackage, resolvedType string, crossPkgJavaPackages map[string]string) {
	if strings.HasPrefix(resolvedType, "com.") {
		return
	}
	outerClass, _, found := strings.Cut(resolvedType, ".")
	if !found || outerClass == "" {
		return
	}
	javaPackage := localTypesPackage
	parts := strings.Split(fullType, ".")
	if len(parts) > 1 {
		pkg := strings.Join(parts[:len(parts)-1], ".")
		if pkg != "" && pkg != servicePackage {
			if crossJavaPackage, ok := crossPkgJavaPackages[pkg]; ok {
				javaPackage = crossJavaPackage
			}
		}
	}
	imports[fmt.Sprintf("import %s.%s;", javaPackage, outerClass)] = struct{}{}
}

func javaGrpcClientAuthRequired(svc *parser.ServiceInfo, method *parser.MethodInfo) bool {
	return !(svc.AuthDisabled || svc.IsC2D || method.AuthDisabled)
}

func javaGrpcAuthBlock(authRequired bool, indent string) string {
	if !authRequired {
		return ""
	}
	return fmt.Sprintf(`%[1]sif (tokenProvider != null) {
%[1]s    Metadata headers = new Metadata();
%[1]s    String token;
%[1]s    try {
%[1]s        token = tokenProvider.get();
%[1]s    } catch (RuntimeException e) {
%[1]s        throw ProtoException.newErrUnauthorized(e, "auth token: %%s", e.getMessage());
%[1]s    }
%[1]s    if (token == null || token.isBlank()) {
%[1]s        throw ProtoException.newErrUnauthorized(null, "auth token is empty");
%[1]s    }
%[1]s    headers.put(AUTH_KEY, "Bearer " + token);
%[1]s    stub = stub.withInterceptors(MetadataUtils.newAttachHeadersInterceptor(headers));
%[1]s} else {
%[1]s    throw ProtoException.newErrUnauthorized(null, "auth token provider is required");
%[1]s}`, indent)
}

func javaGrpcResponseObserverWrapper(outputType, outputValidator string) (string, string) {
	if outputValidator == "" {
		return "", "responseObserver"
	}
	outputValidation := javaW2SValidationStatement("response", outputValidator, "                ")
	return fmt.Sprintf(`        StreamObserver<%[1]s> validatingResponseObserver = new StreamObserver<%[1]s>() {
            @Override public void onNext(%[1]s response) {
%[2]s                responseObserver.onNext(response);
            }
            @Override public void onError(Throwable t) {
                responseObserver.onError(t);
            }
            @Override public void onCompleted() {
                responseObserver.onCompleted();
            }
        };
`, outputType, outputValidation), "validatingResponseObserver"
}

// generateJavaGrpcClientMethod generates a Java method for the gRPC client wrapper.
func generateJavaGrpcClientMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, inputType, outputType, inputValidator, outputValidator string, crossPkgOuterClasses map[string]string) string {
	// Use protoc's naming convention: lowercase first letter only.
	// e.g. "ExecuteDQL" → "executeDQL" (not "executeDql").
	// A snake_case round-trip would mangle all-caps acronyms.
	methodName := method.Name
	if len(methodName) > 0 {
		methodName = strings.ToLower(methodName[:1]) + methodName[1:]
	}
	authRequired := javaGrpcClientAuthRequired(svc, method)
	emptyInput := method.FullInputType == "google.protobuf.Empty"
	emptyOutput := method.FullOutputType == "google.protobuf.Empty"
	inputValidation := ""
	if !emptyInput {
		inputValidation = javaW2SValidationStatement("request", inputValidator, "        ")
	}
	outputValidation := ""
	if !emptyOutput {
		outputValidation = javaW2SValidationStatement("response", outputValidator, "            ")
	}
	streamInputValidation := ""
	if !emptyInput {
		streamInputValidation = javaW2SValidationStatement("request", inputValidator, "            ")
	}
	streamOutputValidation := ""
	if !emptyOutput {
		streamOutputValidation = javaW2SValidationStatement("response", outputValidator, "                ")
	}

	if method.ClientStreaming && method.ServerStreaming {
		// Bidirectional streaming authenticates once on stream open. Message sends
		// can only validate payloads because no new auth metadata reaches the server
		// mid-stream.
		responseObserverSetup, responseObserverArg := javaGrpcResponseObserverWrapper(outputType, outputValidator)
		return fmt.Sprintf(`    /**
     * %s calls bidirectional streaming RPC.
     */
    public StreamObserver<%s> %s(StreamObserver<%s> responseObserver) {
	        var stub = asyncStub;
%s
%s
        StreamObserver<%s> delegate = stub.%s(%s);
        return new StreamObserver<%s>() {
            @Override public void onNext(%s request) {
%s                delegate.onNext(request);
            }
            @Override public void onError(Throwable t) {
                delegate.onError(t);
            }
            @Override public void onCompleted() {
                delegate.onCompleted();
            }
        };
    }
`, method.Name, inputType, methodName, outputType, javaGrpcAuthBlock(authRequired, "        "), responseObserverSetup, inputType, methodName, responseObserverArg, inputType, inputType, streamInputValidation)
	}

	if method.ServerStreaming && method.IsTier2SnapshotCollection() {
		// Tier-2 SNAPSHOT collection: server-streaming on the wire, but the client
		// seam returns a materialized List — drain the blocking-stub iterator.
		return fmt.Sprintf(`    /**
     * %s calls Tier-2 SNAPSHOT collection RPC (server streaming), returning a materialized list.
     */
    public java.util.List<%s> %s(%s request) {
        try {
%s
	            var stub = blockingStubWithDeadline();
%s
            java.util.Iterator<%s> it = stub.%s(request);
            java.util.List<%s> items = new java.util.ArrayList<>();
            while (it.hasNext()) {
                var response = it.next();
%s                items.add(response);
            }
            return items;
        } catch (StatusRuntimeException e) {
            throw ProtoException.fromGrpcError(e);
        }
    }
`, method.Name, outputType, methodName, inputType, inputValidation, javaGrpcAuthBlock(authRequired, "            "), outputType, methodName, outputType, streamOutputValidation)
	}

	if method.ServerStreaming && method.IsWatchCollection() {
		// WATCH collection: server-streaming envelope on the wire, surfaced as a
		// lazy AutoCloseable iterator. close() cancels the stream.
		return fmt.Sprintf(`    /**
     * %s calls WATCH collection RPC (server streaming), returning a lazy closeable iterator.
     */
    public CloseableIterator<%s> %s(%s request) {
%s
	        var stub = blockingStub;
%s
        final io.grpc.Context.CancellableContext __ctx = io.grpc.Context.current().withCancellation();
        final io.grpc.Context __prev = __ctx.attach();
        final java.util.Iterator<%s> __it;
        try {
            __it = stub.%s(request);
        } finally {
            __ctx.detach(__prev);
        }
        return new CloseableIterator<%s>() {
            @Override public boolean hasNext() {
                try {
                    return __it.hasNext();
                } catch (StatusRuntimeException e) {
                    throw ProtoException.fromGrpcError(e);
                }
            }
            @Override public %s next() {
                try {
                    var response = __it.next();
%s                    return response;
                } catch (StatusRuntimeException e) {
                    throw ProtoException.fromGrpcError(e);
                }
            }
            @Override public void close() {
                __ctx.cancel(null);
            }
        };
    }
`, method.Name, outputType, methodName, inputType, inputValidation, javaGrpcAuthBlock(authRequired, "        "), outputType, methodName, outputType, outputType, strings.ReplaceAll(streamOutputValidation, "                ", "                    "))
	}

	if method.ServerStreaming && method.IsTier3SnapshotCollection() {
		// Tier-3 SNAPSHOT cursor: server-streaming on the wire, surfaced as a lazy
		// AutoCloseable iterator. The call is bound to a CancellableContext; close()
		// cancels it (teardown on early exit / try-with-resources).
		return fmt.Sprintf(`    /**
     * %s calls Tier-3 SNAPSHOT cursor RPC (server streaming), returning a lazy closeable iterator.
     */
    public CloseableIterator<%s> %s(%s request) {
%s
	        var stub = blockingStub;
%s
        final io.grpc.Context.CancellableContext __ctx = io.grpc.Context.current().withCancellation();
        final io.grpc.Context __prev = __ctx.attach();
        final java.util.Iterator<%s> __it;
        try {
            __it = stub.%s(request);
        } finally {
            __ctx.detach(__prev);
        }
        return new CloseableIterator<%s>() {
            @Override public boolean hasNext() {
                try {
                    return __it.hasNext();
                } catch (StatusRuntimeException e) {
                    throw ProtoException.fromGrpcError(e);
                }
            }
            @Override public %s next() {
                try {
                    var response = __it.next();
%s                    return response;
                } catch (StatusRuntimeException e) {
                    throw ProtoException.fromGrpcError(e);
                }
            }
            @Override public void close() {
                __ctx.cancel(null);
            }
        };
    }
`, method.Name, outputType, methodName, inputType, inputValidation, javaGrpcAuthBlock(authRequired, "        "), outputType, methodName, outputType, outputType, strings.ReplaceAll(streamOutputValidation, "                ", "                    "))
	}

	if method.ServerStreaming {
		// Server streaming — inject auth on the async stub
		responseObserverSetup, responseObserverArg := javaGrpcResponseObserverWrapper(outputType, outputValidator)
		return fmt.Sprintf(`    /**
     * %s calls server streaming RPC.
     */
    public void %s(%s request, StreamObserver<%s> responseObserver) {
%s
	        var stub = asyncStub;
%s
%s
        stub.%s(request, %s);
    }
`, method.Name, methodName, inputType, outputType, inputValidation, javaGrpcAuthBlock(authRequired, "        "), responseObserverSetup, methodName, responseObserverArg)
	}

	if method.ClientStreaming {
		// Client streaming — inject auth on the async stub. Auth is STREAM-OPEN
		// scoped (one token in the headers interceptor, same as bidi — the pinned
		// contract): gRPC metadata cannot change mid-stream, so a per-send token
		// preflight would mint a token the server never sees while being able to
		// fail a legitimate send (PC-W-2).
		responseObserverSetup, responseObserverArg := javaGrpcResponseObserverWrapper(outputType, outputValidator)
		return fmt.Sprintf(`    /**
     * %s calls client streaming RPC.
     */
    public StreamObserver<%s> %s(StreamObserver<%s> responseObserver) {
	        var stub = asyncStub;
%s
%s
        StreamObserver<%s> delegate = stub.%s(%s);
        return new StreamObserver<%s>() {
            @Override public void onNext(%s request) {
%s                delegate.onNext(request);
            }
            @Override public void onError(Throwable t) {
                delegate.onError(t);
            }
            @Override public void onCompleted() {
                delegate.onCompleted();
            }
        };
    }
`, method.Name, inputType, methodName, outputType, javaGrpcAuthBlock(authRequired, "        "), responseObserverSetup, inputType, methodName, responseObserverArg, inputType, inputType, streamInputValidation)
	}

	if method.IsPageCollection() {
		// PAGE: unary on the wire. Two client seams — an explicit single-page call
		// ({method}Page -> PageResult{items, nextPageToken, total}) and an
		// auto-pager (the natural method name) returning a lazy Iterator that
		// follows next_page_token until exhausted. The auto-pager advances the
		// token on a request clone, so the caller's request is not mutated.
		itemType := javaWatchTypeRef(method.Resolved.PageItemType, outputType, crossPkgOuterClasses)
		itemsListGetter := "get" + strutil.ToPascalCase(method.Resolved.PageItemsField) + "List"
		totalExpr := "-1L"
		if method.Resolved.PageHasTotal {
			totalExpr = "response.getTotalCount()"
		}
		return fmt.Sprintf(`    /**
     * %sPage calls %s.%s and returns a single page: items, next_page_token
     * (empty on the last page), and total (-1 when the response carries no count).
     */
    public CollectionRuntime.PageResult<%s> %sPage(%s request) {
        try {
%s
            var response = unaryWithRetry(true, () -> {
                var stub = blockingStubWithDeadline();
%s
                return stub.%s(request);
            });
%s
            return new CollectionRuntime.PageResult<>(response.%s(), response.getNextPageToken(), %s);
        } catch (StatusRuntimeException e) {
            throw ProtoException.fromGrpcError(e);
        }
    }

    /**
     * %s calls %s.%s as an auto-pager: a lazy Iterator that follows
     * next_page_token until exhausted, yielding each item across pages. The
     * caller's request is not mutated (the token is advanced on a clone).
     */
    public java.util.Iterator<%s> %s(%s request) {
        return new java.util.Iterator<%s>() {
            private java.util.Iterator<%s> __buf = java.util.Collections.emptyIterator();
            private String __token = "";
            private boolean __done = false;
            private void __fill() {
                while (!__buf.hasNext() && !__done) {
                    %s __req = request.toBuilder().setPageToken(__token).build();
                    CollectionRuntime.PageResult<%s> __p = %sPage(__req);
                    __buf = __p.items.iterator();
                    if (__p.nextPageToken.isEmpty()) { __done = true; } else { __token = __p.nextPageToken; }
                }
            }
            @Override public boolean hasNext() { __fill(); return __buf.hasNext(); }
            @Override public %s next() { __fill(); return __buf.next(); }
        };
    }
`, method.Name, svc.Name, method.Name,
			itemType, methodName, inputType,
			inputValidation,
			javaGrpcAuthBlock(authRequired, "                "),
			methodName,
			outputValidation,
			itemsListGetter, totalExpr,
			method.Name, svc.Name, method.Name,
			itemType, methodName, inputType,
			itemType, itemType,
			inputType, itemType, methodName,
			itemType)
	}

	// Unary method — validate input/output, inject auth on the blocking stub.
	retrySafe := method.IsRetrySafeUnary()

	// When output is Empty, return void. When input is Empty, omit req param.
	if emptyOutput && emptyInput {
		return fmt.Sprintf(`    /**
     * %s calls unary RPC with error mapping.
     */
    public void %s() {
        try {
	            unaryWithRetryVoid(%t, () -> {
	                var stub = blockingStubWithDeadline();
	%s
	                stub.%s(com.google.protobuf.Empty.getDefaultInstance());
	            });
	        } catch (StatusRuntimeException e) {
            throw ProtoException.fromGrpcError(e);
        }
    }
`, method.Name, methodName, retrySafe, javaGrpcAuthBlock(authRequired, "            "), methodName)
	}
	if emptyOutput {
		return fmt.Sprintf(`    /**
     * %s calls unary RPC with error mapping.
     */
    public void %s(%s request) {
        try {
%s
	            unaryWithRetryVoid(%t, () -> {
	                var stub = blockingStubWithDeadline();
	%s
	                stub.%s(request);
	            });
	        } catch (StatusRuntimeException e) {
            throw ProtoException.fromGrpcError(e);
        }
    }
`, method.Name, methodName, inputType, inputValidation, retrySafe, javaGrpcAuthBlock(authRequired, "            "), methodName)
	}
	if emptyInput {
		return fmt.Sprintf(`    /**
     * %s calls unary RPC with error mapping.
     */
    public %s %s() {
        try {
	            var response = unaryWithRetry(%t, () -> {
	                var stub = blockingStubWithDeadline();
	%s
	                return stub.%s(com.google.protobuf.Empty.getDefaultInstance());
	            });
%s
	            return response;
	        } catch (StatusRuntimeException e) {
            throw ProtoException.fromGrpcError(e);
        }
    }
`, method.Name, outputType, methodName, retrySafe, javaGrpcAuthBlock(authRequired, "            "), methodName, outputValidation)
	}
	return fmt.Sprintf(`    /**
     * %s calls unary RPC with error mapping.
     */
    public %s %s(%s request) {
        try {
%s
	            var response = unaryWithRetry(%t, () -> {
	                var stub = blockingStubWithDeadline();
	%s
	                return stub.%s(request);
	            });
%s
	            return response;
	        } catch (StatusRuntimeException e) {
            throw ProtoException.fromGrpcError(e);
        }
    }
`, method.Name, outputType, methodName, inputType, inputValidation, retrySafe, javaGrpcAuthBlock(authRequired, "            "), methodName, outputValidation)
}

// grpcClientGroupEntryJava tracks a service for the group aggregator.
type grpcClientGroupEntryJava struct {
	FieldName       string
	MethodName      string
	ClassName       string
	TypeRef         string // simple name or FQN when there's a class-name collision
	ImportLine      string // "pkg.ClassName" to import, or "" when using FQN
	FullPackage     string
	TargetServiceID string // service_id for ServiceConnection routing (e.g. "opa")
	TargetParamName string // camelCase param name for the ChannelProvider (e.g. "opaChannel")
}

// grpcClientTargetJava tracks a unique target service for constructor params.
type grpcClientTargetJava struct {
	ServiceID string // e.g. "opa"
	ParamName string // camelCase constructor param name (e.g. "opaChannel")
	ConstName string // UPPER_SNAKE constant name (e.g. "OPA")
}

// targetConstName converts a service ID to an UPPER_SNAKE constant name.
func targetConstName(serviceID string) string {
	return strings.ToUpper(strings.ReplaceAll(serviceID, "-", "_"))
}

// targetParamName converts a service ID to a camelCase constructor param name.
func targetParamName(serviceID string) string {
	safe := strings.ReplaceAll(serviceID, "-", "_")
	parts := strings.Split(safe, "_")
	result := parts[0]
	for _, p := range parts[1:] {
		if len(p) > 0 {
			result += strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return result + "Channel"
}

func javaGrpcTargetEntries(config *tactype.Config) []tactype.GrpcEntry {
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

func javaGrpcEntryMatches(entry tactype.GrpcEntry, protoFile, packageName string) bool {
	if entry.ProtoPackage != "" {
		return entry.ProtoPackage == packageName
	}
	if entry.ProtoFile == protoFile {
		return true
	}
	return strings.TrimSuffix(filepath.Base(entry.ProtoFile), ".proto") == packageName
}

func javaResolveGrpcTarget(config *tactype.Config, protoFile, packageName, hostTag string) string {
	target := packageName
	if hostTag != "" {
		target = hostTag
	}
	for _, entry := range javaGrpcTargetEntries(config) {
		if javaGrpcEntryMatches(entry, protoFile, packageName) && entry.TargetService != "" {
			target = entry.TargetService
			break
		}
	}
	return target
}

func javaClientTargets(config *tactype.Config, servicesInfo []ServiceInfo) []grpcClientTargetJava {
	targetSeen := make(map[string]bool)
	var targets []grpcClientTargetJava
	for _, svc := range servicesInfo {
		targetServiceID := javaResolveGrpcTarget(config, svc.ProtoFile, svc.PackageName, svc.HostLabel)
		if targetSeen[targetServiceID] {
			continue
		}
		targetSeen[targetServiceID] = true
		targets = append(targets, grpcClientTargetJava{
			ServiceID: targetServiceID,
			ParamName: targetParamName(targetServiceID),
			ConstName: targetConstName(targetServiceID),
		})
	}
	return targets
}

// generateGrpcClientGroupAggregators creates GrpcClients.java at each group level.
func generateGrpcClientGroupAggregators(config *tactype.Config, wrappers []generatedGrpcClientJava) error {
	if len(wrappers) == 0 {
		return nil
	}

	groups := make(map[string][]generatedGrpcClientJava)
	for _, w := range wrappers {
		groups[w.ParentDir] = append(groups[w.ParentDir], w)
	}

	for group, groupWrappers := range groups {
		groupPackage := fmt.Sprintf("%s."+config.GetSmPrefix()+".internal.grpc.clients.%s", config.JavaPackageBase, group)

		// Count occurrences of each ClassName so we can detect collisions.
		classNameCount := make(map[string]int)
		for _, w := range groupWrappers {
			classNameCount[w.ClassName]++
		}

		var entries []grpcClientGroupEntryJava
		seen := make(map[string]bool)
		seenFields := make(map[string]bool)
		for _, w := range groupWrappers {
			fieldName := strings.ToLower(w.ClassName[:1]) + w.ClassName[1:]
			// When multiple services share the same ClassName (e.g., both hotdb and colddb
			// expose a "Query" service → both generate "QueryGrpcClient"), prefix the field
			// name with the package name to avoid duplicate field declarations.
			if classNameCount[w.ClassName] > 1 || seenFields[fieldName] {
				fieldName = w.PackageName + strings.ToUpper(fieldName[:1]) + fieldName[1:]
			}
			seenFields[fieldName] = true
			// Derive method name from service name (strip "Service" suffix, lowercase first letter)
			// to avoid duplicates when multiple services share the same package name
			// (e.g., BaseballService → baseball(), BaseballStateService → baseballState())
			methodName := w.ServiceName
			methodName = strings.TrimSuffix(methodName, "Service")
			methodName = strings.ToLower(methodName[:1]) + methodName[1:]
			// When there's a class-name collision, prefix method name with package too.
			if classNameCount[w.ClassName] > 1 {
				methodName = w.PackageName + strings.ToUpper(methodName[:1]) + methodName[1:]
			}
			// Fall back to field name if there's still a collision
			if seen[methodName] {
				methodName = fieldName
			}
			seen[methodName] = true
			// Mesh routing target: use TargetService from the active client surface
			// when set. Virtual services route to their physical host.
			targetServiceID := javaResolveGrpcTarget(config, w.ProtoFile, w.PackageName, w.HostLabel)
			// When two services share the same simple class name we must use the fully-qualified
			// name in the generated Java to avoid import conflicts and ambiguity.
			typeRef := w.ClassName
			importLine := w.FullPackage + "." + w.ClassName
			if classNameCount[w.ClassName] > 1 {
				typeRef = w.FullPackage + "." + w.ClassName
				importLine = ""
			}
			entries = append(entries, grpcClientGroupEntryJava{
				FieldName:       fieldName,
				MethodName:      methodName,
				ClassName:       w.ClassName,
				TypeRef:         typeRef,
				ImportLine:      importLine,
				FullPackage:     w.FullPackage,
				TargetServiceID: targetServiceID,
				TargetParamName: targetParamName(targetServiceID),
			})
		}

		// Collect unique target services for this group's constructor params.
		targetSeen := make(map[string]bool)
		var uniqueTargets []grpcClientTargetJava
		for _, e := range entries {
			if !targetSeen[e.TargetServiceID] {
				targetSeen[e.TargetServiceID] = true
				uniqueTargets = append(uniqueTargets, grpcClientTargetJava{
					ServiceID: e.TargetServiceID,
					ParamName: e.TargetParamName,
					ConstName: targetConstName(e.TargetServiceID),
				})
			}
		}

		data := struct {
			Header                  string
			Package                 string
			ServiceConnectionImport string
			Entries                 []grpcClientGroupEntryJava
			UniqueTargets           []grpcClientTargetJava
		}{
			Header:                  header.GeneratedHeader(consts.LangJava, "grpcclients/"+group),
			Package:                 groupPackage,
			ServiceConnectionImport: config.JavaPackageBase + ".chassis",
			Entries:                 entries,
			UniqueTargets:           uniqueTargets,
		}

		tmplStr, err := loadTemplate("grpcclient_group.java.tmpl")
		if err != nil {
			return fmt.Errorf("failed to load grpcclient group template: %w", err)
		}
		tmpl, err := template.New("gcgroup").Parse(tmplStr)
		if err != nil {
			return fmt.Errorf("failed to parse grpcclient group template: %w", err)
		}

		packagePath := strings.ReplaceAll(groupPackage, ".", string(filepath.Separator))
		outputDir := filepath.Join(config.OutputDir, "src", "main", "java", packagePath)
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return fmt.Errorf("failed to create group directory: %w", err)
		}

		outputPath := filepath.Join(outputDir, "GrpcClients.java")
		file, err := os.Create(outputPath)
		if err != nil {
			return fmt.Errorf("failed to create GrpcClients.java: %w", err)
		}
		defer file.Close()

		if err := tmpl.Execute(file, data); err != nil {
			return fmt.Errorf("failed to execute grpcclient group template: %w", err)
		}

		zap.L().Info("Generated group GrpcClients.java", zap.String("group", group), zap.String("path", outputPath))
	}

	return nil
}

// grpcClientRootGroupJava tracks a group for the root aggregator.
type grpcClientRootGroupJava struct {
	Name          string
	MethodName    string
	GroupPackage  string
	UniqueTargets []grpcClientTargetJava // targets this group needs forwarded
}

// generateGrpcClientRootAggregator creates the root GrpcClients.java aggregator.
func generateGrpcClientRootAggregator(config *tactype.Config, wrappers []generatedGrpcClientJava) error {
	if len(wrappers) == 0 {
		return nil
	}

	rootPackage := config.JavaPackageBase + "." + config.GetSmPrefix() + ".internal.grpc.clients"

	// Collect all unique targets across all groups, and per-group targets.
	allTargetSeen := make(map[string]bool)
	var allUniqueTargets []grpcClientTargetJava

	// Group wrappers by ParentDir to collect per-group targets.
	groupWrapperMap := make(map[string][]generatedGrpcClientJava)
	for _, w := range wrappers {
		groupWrapperMap[w.ParentDir] = append(groupWrapperMap[w.ParentDir], w)
	}

	seen := make(map[string]bool)
	var groups []grpcClientRootGroupJava
	for _, w := range wrappers {
		if seen[w.ParentDir] {
			continue
		}
		seen[w.ParentDir] = true

		// Compute unique targets for this group.
		targetSeen := make(map[string]bool)
		var groupTargets []grpcClientTargetJava
		for _, gw := range groupWrapperMap[w.ParentDir] {
			targetServiceID := javaResolveGrpcTarget(config, gw.ProtoFile, gw.PackageName, gw.HostLabel)
			if !targetSeen[targetServiceID] {
				targetSeen[targetServiceID] = true
				t := grpcClientTargetJava{
					ServiceID: targetServiceID,
					ParamName: targetParamName(targetServiceID),
					ConstName: targetConstName(targetServiceID),
				}
				groupTargets = append(groupTargets, t)
				if !allTargetSeen[targetServiceID] {
					allTargetSeen[targetServiceID] = true
					allUniqueTargets = append(allUniqueTargets, t)
				}
			}
		}

		groups = append(groups, grpcClientRootGroupJava{
			Name:          w.ParentDir,
			MethodName:    strutil.ToLowerCamelCase(strings.ReplaceAll(w.ParentDir, "-", "_")),
			GroupPackage:  fmt.Sprintf("%s."+config.GetSmPrefix()+".internal.grpc.clients.%s", config.JavaPackageBase, w.ParentDir),
			UniqueTargets: groupTargets,
		})
	}

	data := struct {
		Header                  string
		Package                 string
		ServiceConnectionImport string
		Groups                  []grpcClientRootGroupJava
		UniqueTargets           []grpcClientTargetJava
	}{
		Header:                  header.GeneratedHeader(consts.LangJava, "grpcclients"),
		Package:                 rootPackage,
		ServiceConnectionImport: config.JavaPackageBase + ".chassis",
		Groups:                  groups,
		UniqueTargets:           allUniqueTargets,
	}

	tmplStr, err := loadTemplate("grpcclient_root.java.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load grpcclient root template: %w", err)
	}
	tmpl, err := template.New("gcroot").Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse grpcclient root template: %w", err)
	}

	packagePath := strings.ReplaceAll(rootPackage, ".", string(filepath.Separator))
	outputDir := filepath.Join(config.OutputDir, "src", "main", "java", packagePath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create grpcclients directory: %w", err)
	}

	outputPath := filepath.Join(outputDir, "GrpcClients.java")
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create root GrpcClients.java: %w", err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, data); err != nil {
		return fmt.Errorf("failed to execute grpcclient root template: %w", err)
	}

	zap.L().Info("Generated root GrpcClients.java", zap.String("path", outputPath))
	return nil
}
