// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"
	"unicode"

	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/header"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/consts"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// generatedW2SClientJava tracks info about a generated W2S client wrapper.
type generatedW2SClientJava struct {
	PackageName     string
	ServiceName     string
	ClassName       string
	ParentDir       string
	FullPackage     string
	TargetServiceID string
}

// javaW2SMethodResult holds generated Java method code plus optional nested handle types.
type javaW2SMethodResult struct {
	Method    string
	ExtraType string
}

// generateW2SClients generates typed Java W2S clients. Unary RPCs use HTTP/JSON,
// server-streaming RPCs use SSE, and client-streaming/bidirectional RPCs use WebSocket frames.
func generateW2SClients(config *tactype.Config, servicesInfo []ServiceInfo, inputDir string) error {
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	var generated []generatedW2SClientJava
	for _, svc := range servicesInfo {
		protoInfo, err := descriptorParser.ConvertToProtoInfo(svc.ProtoFile)
		if err != nil {
			return fmt.Errorf("failed to parse %s: %w", svc.ProtoFile, err)
		}

		if err := generateJavaW2SClient(svc, protoInfo); err != nil {
			return fmt.Errorf("failed to generate W2S client for %s: %w", svc.ServiceName, err)
		}

		typeDir := javaTopTypeDir(svc.ProtoFile)
		generated = append(generated, generatedW2SClientJava{
			PackageName:     svc.PackageName,
			ServiceName:     svc.ServiceName,
			ClassName:       svc.ServiceName + "W2SClient",
			ParentDir:       typeDir,
			FullPackage:     javaW2SClientPackage(config, typeDir, svc.PackageName),
			TargetServiceID: resolveJavaW2STarget(svc),
		})
	}

	if err := generateJavaW2SClientAggregator(config, generated); err != nil {
		return fmt.Errorf("failed to generate W2S client aggregator: %w", err)
	}

	return nil
}

// resolveW2SClientServices returns services that should get w2s client wrappers.
func resolveW2SClientServices(config *tactype.Config, servicesInfo []ServiceInfo) []ServiceInfo {
	if len(config.GrpcW2SClients) > 0 {
		return filterServicesByGrpcEntries(servicesInfo, config.GrpcW2SClients)
	}
	return nil
}

func javaTopTypeDir(protoFile string) string {
	normalizedRelPath := strings.TrimPrefix(protoFile, "tacplus"+string(filepath.Separator))
	return strings.Split(normalizedRelPath, string(filepath.Separator))[0]
}

func javaW2SClientPackage(config *tactype.Config, typeDir, packageName string) string {
	return fmt.Sprintf("%s.%s.internal.grpc_over_rest.%s.%s", config.JavaPackageBase, config.GetSmPrefix(), typeDir, packageName)
}

// resolveJavaW2STarget determines the physical service ID for W2S routing groups.
func resolveJavaW2STarget(svc ServiceInfo) string {
	target := svc.PackageName
	if svc.HostTag != "" {
		target = svc.HostTag
	}
	for _, entry := range svc.Config.GrpcW2SClients {
		matchKey := entry.ProtoPackage
		if matchKey == "" {
			matchKey = strings.TrimSuffix(filepath.Base(entry.ProtoFile), ".proto")
		}
		if matchKey == svc.PackageName && entry.TargetService != "" {
			target = entry.TargetService
			break
		}
	}
	return target
}

func generateJavaW2SClient(svc ServiceInfo, info *parser.ProtoInfo) error {
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

	normalizedRelPath := strings.TrimPrefix(svc.ProtoFile, "tacplus"+string(filepath.Separator))
	protoDir := filepath.Join(svc.Config.OutputDir, "proto")
	typeDir := strings.Split(normalizedRelPath, string(filepath.Separator))[0]
	fullPackage := javaW2SClientPackage(svc.Config, typeDir, svc.PackageName)
	typesPackage := fmt.Sprintf("%s.types.%s.%s", svc.Config.JavaPackageBase, typeDir, svc.PackageName)

	svcName, svcFileName := proto.ServiceProtoParts(svc.ProtoFile)
	isFlatServiceProto := svcName == svcFileName

	srcDir := filepath.Join(svc.Config.OutputDir, "src", "main", "java")
	packagePath := strings.ReplaceAll(fullPackage, ".", string(filepath.Separator))
	outputDir := filepath.Join(srcDir, packagePath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	var outerClassName string
	if isFlatServiceProto {
		typesProtoPath := filepath.Join(protoDir, svc.ProtoFile)
		if name := extractJavaOuterClassName(typesProtoPath); name != "" {
			outerClassName = name
		} else {
			outerClassName = getOuterClassNameForWrapper(svc.TypesOutputDir, svc.ProtoFile)
		}
	} else {
		typesProtoPath := filepath.Join(protoDir, typeDir, svc.PackageName, svc.PackageName+".proto")
		if name := extractJavaOuterClassName(typesProtoPath); name != "" {
			outerClassName = name
		} else {
			outerClassName = getOuterClassNameForWrapper(svc.TypesOutputDir, svc.ProtoFile)
		}
	}

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
		crossPkgOuterClasses[pkg] = getOuterClassNameForWrapper(crossTypesPath, crossProtoFile)
	}

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

	neededImports := map[string]struct{}{}
	var methods strings.Builder
	var extraTypes strings.Builder
	for _, method := range targetService.Methods {
		if method.HttpPath == "" {
			return fmt.Errorf("%s.%s requires W2S REST route metadata; clients.w2s.enabled cannot emit a method without an HTTP path",
				targetService.Name, method.Name)
		}

		inputType := resolveJavaMethodType(method.FullInputType, method.InputType, info.Package, outerClassName, crossPkgOuterClasses)
		outputType := resolveJavaMethodType(method.FullOutputType, method.OutputType, info.Package, outerClassName, crossPkgOuterClasses)
		collectGrpcClientTypeImport(neededImports, method.FullInputType, info.Package, typesPackage, inputType, crossPkgJavaPackages)
		collectGrpcClientTypeImport(neededImports, method.FullOutputType, info.Package, typesPackage, outputType, crossPkgJavaPackages)

		inputValidator := javaW2SValidationRef(method.FullInputType, method.InputType, info.Package, crossPkgHelpers)
		outputValidator := javaW2SValidationRef(method.FullOutputType, method.OutputType, info.Package, crossPkgHelpers)
		if strings.HasPrefix(inputValidator, "Helpers.") || strings.HasPrefix(outputValidator, "Helpers.") {
			neededImports[fmt.Sprintf("import %s.Helpers;", typesPackage)] = struct{}{}
		}

		authRequired := !svc.AuthDisabled && !method.AuthDisabled
		result := generateJavaW2SClientMethod(targetService, method, inputType, outputType, svc.ServiceName+"W2SClient", authRequired, inputValidator, outputValidator)
		methods.WriteString(result.Method)
		methods.WriteString("\n")
		if result.ExtraType != "" {
			extraTypes.WriteString(result.ExtraType)
			extraTypes.WriteString("\n")
		}
	}

	importLines := make(map[string]struct{})
	for importLine := range neededImports {
		importLines[importLine] = struct{}{}
	}
	importLines[fmt.Sprintf("import %s.types.sys.errors.ProtoException;", svc.Config.JavaPackageBase)] = struct{}{}

	var sortedImportLines []string
	for importLine := range importLines {
		sortedImportLines = append(sortedImportLines, importLine)
	}
	sort.Strings(sortedImportLines)

	clientName := svc.ServiceName + "W2SClient"
	outputPath := filepath.Join(outputDir, clientName+".java")
	zap.L().Info("Generating Java W2S client", zap.String("path", outputPath))

	data := struct {
		Header      string
		FullPackage string
		TypesImport string
		ServiceName string
		ClientName  string
		Methods     string
		ExtraTypes  string
	}{
		Header:      header.GeneratedHeader(consts.LangJava, svc.ProtoFile),
		FullPackage: fullPackage,
		TypesImport: strings.Join(sortedImportLines, "\n"),
		ServiceName: svc.ServiceName,
		ClientName:  clientName,
		Methods:     methods.String(),
		ExtraTypes:  extraTypes.String(),
	}

	tmplStr, err := loadTemplate("w2sclient.java.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load w2sclient template: %w", err)
	}
	tmpl, err := template.New("w2sclient").Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse w2sclient template: %w", err)
	}

	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, data); err != nil {
		return fmt.Errorf("failed to execute w2sclient template: %w", err)
	}
	return nil
}

func javaW2SHttpMethod(method *parser.MethodInfo, isEmptyInput bool) string {
	if isEmptyInput && method.HttpMethod != "" {
		return method.HttpMethod
	}
	return "POST"
}

func javaRpcMethodName(name string) string {
	if name == "" {
		return name
	}
	return strings.ToLower(name[:1]) + name[1:]
}

func javaW2SHandleName(methodName string) string {
	return strutil.ToPascalCase(methodName) + "W2SHandle"
}

func javaW2SValidationRef(fullType, simpleType, servicePackage string, crossPkgHelpers map[string]string) string {
	helpers := resolveValidationHelpers(fullType, servicePackage, crossPkgHelpers)
	if helpers == "" {
		return ""
	}
	return fmt.Sprintf("%s.validate%s", helpers, simpleType)
}

func javaW2SValidationStatement(varName, validator, indent string) string {
	if validator == "" {
		return ""
	}
	return fmt.Sprintf("%s%s(%s);\n", indent, validator, varName)
}

func javaW2SValidatorLambda(validator string) string {
	if validator == "" {
		return "null"
	}
	return fmt.Sprintf("msg -> %s(msg)", validator)
}

func generateJavaW2SClientMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, inputType, outputType, clientName string, authRequired bool, inputValidator, outputValidator string) javaW2SMethodResult {
	methodName := javaRpcMethodName(method.Name)
	isEmptyInput := method.FullInputType == "google.protobuf.Empty" || method.InputType == "Empty"
	isEmptyOutput := method.FullOutputType == "google.protobuf.Empty" || method.OutputType == "Empty"
	httpMethod := javaW2SHttpMethod(method, isEmptyInput)
	authLiteral := fmt.Sprintf("%t", authRequired)
	inputValidation := javaW2SValidationStatement("request", inputValidator, "        ")
	bidiInputValidation := javaW2SValidationStatement("message", inputValidator, "            ")
	inputValidatorLambda := javaW2SValidatorLambda(inputValidator)
	outputValidatorLambda := javaW2SValidatorLambda(outputValidator)

	if method.ClientStreaming && method.ServerStreaming {
		handleType := javaW2SHandleName(method.Name)
		methodCode := fmt.Sprintf(`    /**
     * %s opens a managed W2S bidirectional stream to %s.%s.
     */
    public %s %s(java.util.function.Consumer<%s> onMessage) {
        return new %s(this, "%s", onMessage, %s, %s);
    }
`, method.Name, svc.Name, method.Name, handleType, methodName, outputType, handleType, method.HttpPath, authLiteral, outputValidatorLambda)

		extraType := fmt.Sprintf(`    /**
     * Handle for the managed %s W2S bidirectional stream.
     */
    public static final class %s implements AutoCloseable {
        private final %s client;
        private final String path;
        private final java.util.function.Consumer<%s> onMessage;
        private final boolean authRequired;
        private final java.util.function.Consumer<%s> responseValidator;
        private final java.util.concurrent.BlockingQueue<QueuedWsMessage> outbound =
            new java.util.concurrent.LinkedBlockingQueue<>(32);
        private final java.util.concurrent.atomic.AtomicBoolean closed =
            new java.util.concurrent.atomic.AtomicBoolean(false);
        private final java.util.concurrent.atomic.AtomicReference<WebSocket> currentWebSocket =
            new java.util.concurrent.atomic.AtomicReference<>();
        private final Thread worker;

        private %s(%s client, String path, java.util.function.Consumer<%s> onMessage, boolean authRequired, java.util.function.Consumer<%s> responseValidator) {
            this.client = client;
            this.path = path;
            this.onMessage = onMessage;
            this.authRequired = authRequired;
            this.responseValidator = responseValidator;
            this.worker = new Thread(this::runLoop, "%s-%s-w2s");
            this.worker.setDaemon(true);
            this.worker.start();
        }

        /**
         * Queues a message for the W2S bidirectional stream.
         * The returned future completes when the message frame has been written.
         */
        public java.util.concurrent.CompletableFuture<Void> send(%s message) {
            if (closed.get()) {
                throw new ProtoException(ProtoException.UNAVAILABLE, "W2S bidirectional stream is closed");
            }
%s            java.util.concurrent.CompletableFuture<Void> ack = new java.util.concurrent.CompletableFuture<>();
            if (!outbound.offer(new QueuedWsMessage(message, ack))) {
                ack.completeExceptionally(new ProtoException(ProtoException.RESOURCE_EXHAUSTED, "W2S send buffer full"));
            }
            return ack;
        }

        private void runLoop() {
            while (!closed.get()) {
                TextQueueListener listener = new TextQueueListener();
                WebSocket ws = null;
                try {
                    ws = client.connectWebSocket(path, listener);
                    currentWebSocket.set(ws);
                    client.sendStartFrame(ws, authRequired);

                    while (!closed.get() && !listener.isClosed()) {
                        QueuedWsMessage queued = outbound.poll(100, java.util.concurrent.TimeUnit.MILLISECONDS);
                        if (queued != null) {
                            try {
                                client.sendMessageFrame(ws, queued.message, authRequired);
                                queued.ack.complete(null);
                            } catch (RuntimeException e) {
                                queued.ack.completeExceptionally(e);
                                break;
                            }
                        }

                        String text;
                        while ((text = listener.pollText()) != null) {
                            onMessage.accept(client.decodeWsText(text, %s.class, responseValidator));
                        }
                    }
                } catch (InterruptedException e) {
                    Thread.currentThread().interrupt();
                    return;
                } catch (RuntimeException e) {
                    if (closed.get()) {
                        return;
                    }
                } finally {
                    if (ws != null) {
                        currentWebSocket.compareAndSet(ws, null);
                        client.closeWebSocket(ws);
                    }
                }
                client.sleepBeforeReconnect();
            }
        }

        @Override
        public void close() {
            if (!closed.compareAndSet(false, true)) {
                return;
            }
            WebSocket ws = currentWebSocket.get();
            if (ws != null) {
                try {
                    client.sendCompleteFrame(ws, authRequired);
                } catch (RuntimeException ignored) {
                }
                client.closeWebSocket(ws);
            }
            QueuedWsMessage queued;
            while ((queued = outbound.poll()) != null) {
                queued.ack.completeExceptionally(new ProtoException(ProtoException.UNAVAILABLE, "W2S bidirectional stream is closed"));
            }
            worker.interrupt();
        }
    }
`, method.Name, handleType, clientName, outputType, outputType, handleType, clientName, outputType, outputType,
			svc.Name, method.Name, inputType, bidiInputValidation, outputType)
		return javaW2SMethodResult{Method: methodCode, ExtraType: extraType}
	}

	if method.ClientStreaming {
		if isEmptyOutput {
			return javaW2SMethodResult{Method: fmt.Sprintf(`    /**
     * %s calls %s.%s via W2S WebSocket client streaming.
     */
    public void %s(Iterable<%s> requests) {
        clientStreamUnit("%s", requests, %s, %s);
    }
`, method.Name, svc.Name, method.Name, methodName, inputType, method.HttpPath, authLiteral, inputValidatorLambda)}
		}
		return javaW2SMethodResult{Method: fmt.Sprintf(`    /**
     * %s calls %s.%s via W2S WebSocket client streaming.
     */
    public %s %s(Iterable<%s> requests) {
        return clientStream("%s", requests, %s.class, %s, %s, %s);
    }
`, method.Name, svc.Name, method.Name, outputType, methodName, inputType, method.HttpPath, outputType, authLiteral, inputValidatorLambda, outputValidatorLambda)}
	}

	if method.ServerStreaming {
		if isEmptyInput {
			return javaW2SMethodResult{Method: fmt.Sprintf(`    /**
     * %s streams %s.%s via W2S SSE.
     */
    public java.util.Iterator<%s> %s() {
        return streamSse("%s", "%s", null, %s.class, %s, %s);
    }
`, method.Name, svc.Name, method.Name, outputType, methodName, httpMethod, method.HttpPath, outputType, authLiteral, outputValidatorLambda)}
		}
		return javaW2SMethodResult{Method: fmt.Sprintf(`    /**
     * %s streams %s.%s via W2S SSE.
     */
    public java.util.Iterator<%s> %s(%s request) {
%s        return streamSse("%s", "%s", request, %s.class, %s, %s);
    }
`, method.Name, svc.Name, method.Name, outputType, methodName, inputType, inputValidation, httpMethod, method.HttpPath, outputType, authLiteral, outputValidatorLambda)}
	}

	if isEmptyOutput {
		if isEmptyInput {
			return javaW2SMethodResult{Method: fmt.Sprintf(`    /**
     * %s calls %s.%s via W2S HTTP/JSON.
     */
    public void %s() {
        doJsonUnit("%s", "%s", null, %s);
    }
`, method.Name, svc.Name, method.Name, methodName, httpMethod, method.HttpPath, authLiteral)}
		}
		return javaW2SMethodResult{Method: fmt.Sprintf(`    /**
     * %s calls %s.%s via W2S HTTP/JSON.
     */
    public void %s(%s request) {
%s        doJsonUnit("%s", "%s", request, %s);
    }
`, method.Name, svc.Name, method.Name, methodName, inputType, inputValidation, httpMethod, method.HttpPath, authLiteral)}
	}

	if isEmptyInput {
		return javaW2SMethodResult{Method: fmt.Sprintf(`    /**
     * %s calls %s.%s via W2S HTTP/JSON.
     */
    public %s %s() {
        return doJson("%s", "%s", null, %s.class, %s, %s);
    }
`, method.Name, svc.Name, method.Name, outputType, methodName, httpMethod, method.HttpPath, outputType, authLiteral, outputValidatorLambda)}
	}
	return javaW2SMethodResult{Method: fmt.Sprintf(`    /**
     * %s calls %s.%s via W2S HTTP/JSON.
     */
    public %s %s(%s request) {
%s        return doJson("%s", "%s", request, %s.class, %s, %s);
    }
`, method.Name, svc.Name, method.Name, outputType, methodName, inputType, inputValidation, httpMethod, method.HttpPath, outputType, authLiteral, outputValidatorLambda)}
}

type javaW2STargetEntry struct {
	ServiceID  string
	ConstName  string
	ClassName  string
	FieldName  string
	MethodName string
	Clients    []javaW2STargetClientEntry
}

type javaW2STargetClientEntry struct {
	FieldName  string
	MethodName string
	TypeRef    string
}

func generateJavaW2SClientAggregator(config *tactype.Config, clients []generatedW2SClientJava) error {
	if len(clients) == 0 {
		return nil
	}

	serviceNameCount := make(map[string]int)
	for _, client := range clients {
		serviceNameCount[client.ServiceName]++
	}

	targetIndex := make(map[string]int)
	var targets []javaW2STargetEntry
	for _, client := range clients {
		idx, ok := targetIndex[client.TargetServiceID]
		if !ok {
			safeTarget := javaSafeIdentifier(client.TargetServiceID)
			idx = len(targets)
			targetIndex[client.TargetServiceID] = idx
			targets = append(targets, javaW2STargetEntry{
				ServiceID:  client.TargetServiceID,
				ConstName:  javaConstName(client.TargetServiceID),
				ClassName:  strutil.ToPascalCase(safeTarget) + "W2SClients",
				FieldName:  javaLowerCamelIdent(safeTarget),
				MethodName: javaLowerCamelIdent(safeTarget),
			})
		}

		fieldName := strings.ToLower(client.ClassName[:1]) + client.ClassName[1:]
		methodName := client.ServiceName
		methodName = strings.TrimSuffix(methodName, "Service")
		methodName = strings.ToLower(methodName[:1]) + methodName[1:]
		if serviceNameCount[client.ServiceName] > 1 {
			fieldName = client.PackageName + strings.ToUpper(fieldName[:1]) + fieldName[1:]
			methodName = client.PackageName + strings.ToUpper(methodName[:1]) + methodName[1:]
		}
		targets[idx].Clients = append(targets[idx].Clients, javaW2STargetClientEntry{
			FieldName:  fieldName,
			MethodName: methodName,
			TypeRef:    client.FullPackage + "." + client.ClassName,
		})
	}

	for targetIdx := range targets {
		seenMethods := make(map[string]bool)
		seenFields := make(map[string]bool)
		for clientIdx := range targets[targetIdx].Clients {
			client := &targets[targetIdx].Clients[clientIdx]
			if seenFields[client.FieldName] {
				client.FieldName = client.FieldName + "Client"
			}
			seenFields[client.FieldName] = true
			if seenMethods[client.MethodName] {
				client.MethodName = client.FieldName
			}
			seenMethods[client.MethodName] = true
		}
	}

	rootPackage := config.JavaPackageBase + "." + config.GetSmPrefix() + ".internal.grpc_over_rest"
	data := struct {
		Header  string
		Package string
		Targets []javaW2STargetEntry
	}{
		Header:  header.GeneratedHeader(consts.LangJava, "w2s_clients"),
		Package: rootPackage,
		Targets: targets,
	}

	tmplStr, err := loadTemplate("w2sclient_root.java.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load w2sclient root template: %w", err)
	}
	tmpl, err := template.New("w2sclient_root").Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse w2sclient root template: %w", err)
	}

	packagePath := strings.ReplaceAll(rootPackage, ".", string(filepath.Separator))
	outputDir := filepath.Join(config.OutputDir, "src", "main", "java", packagePath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create w2s clients directory: %w", err)
	}
	outputPath := filepath.Join(outputDir, "W2SClients.java")

	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create W2SClients.java: %w", err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, data); err != nil {
		return fmt.Errorf("failed to execute w2sclient root template: %w", err)
	}

	zap.L().Info("Generated Java W2SClients.java", zap.String("path", outputPath))
	return nil
}

func generateW2SServiceManager(config *tactype.Config) error {
	tmplContent, err := loadTemplate("W2SServiceManager.java.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load w2sm service_manager template: %w", err)
	}

	tmpl, err := template.New("w2sservicemanager").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse w2sm service_manager template: %w", err)
	}

	data := struct {
		PackageBase string
	}{
		PackageBase: config.JavaPackageBase,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute w2sm service_manager template: %w", err)
	}

	packagePath := strings.ReplaceAll(config.JavaPackageBase, ".", string(filepath.Separator))
	outputDir := filepath.Join(config.LangDir(), "src", "main", "java", packagePath, "w2sm")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create w2sm service_manager dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "ServiceManager.java")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write w2sm ServiceManager.java: %w", err)
	}

	zap.L().Info("Generated Java w2sm service_manager", zap.String("path", outputPath))
	return nil
}

func javaSafeIdentifier(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "target"
	}
	if unicode.IsDigit([]rune(out)[0]) {
		return "_" + out
	}
	return out
}

func javaLowerCamelIdent(s string) string {
	pascal := strutil.ToPascalCase(s)
	if pascal == "" {
		return "target"
	}
	return strings.ToLower(pascal[:1]) + pascal[1:]
}

func javaConstName(s string) string {
	var b strings.Builder
	lastUnderscore := false
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToUpper(r))
			lastUnderscore = false
		} else if !lastUnderscore {
			b.WriteRune('_')
			lastUnderscore = true
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "TARGET"
	}
	return out
}
