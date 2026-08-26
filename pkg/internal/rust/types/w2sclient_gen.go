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
)

// generateW2SClients generates typed W2S clients for each selected gRPC service.
// Unary RPCs use HTTP/JSON, server-streaming RPCs use SSE, and client-streaming
// or bidirectional RPCs use the gateway WebSocket stream contract.
func generateW2SClients(servicesInfo []ServiceInfo, inputDir string, _packageToDirMap map[string]string) error {
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	for _, svc := range servicesInfo {
		protoInfo, err := descriptorParser.ConvertToProtoInfo(svc.ProtoFile)
		if err != nil {
			return fmt.Errorf("failed to parse %s: %w", svc.ProtoFile, err)
		}

		if err := generateRustW2SClient(svc, protoInfo); err != nil {
			return fmt.Errorf("failed to generate W2S client for %s: %w", svc.ServiceName, err)
		}
	}

	if len(servicesInfo) > 0 {
		if err := generateRustW2SClientAggregators(servicesInfo); err != nil {
			return fmt.Errorf("failed to generate W2S client aggregators: %w", err)
		}
	}

	return nil
}

// resolveW2STarget determines the physical service ID for W2S routing groups.
func resolveW2STarget(svc ServiceInfo) string {
	config := svc.Config
	meshTarget := svc.PackageName
	if svc.HostLabel != "" {
		meshTarget = svc.HostLabel
	}
	for _, entry := range config.GrpcW2SClients {
		matchKey := strings.TrimSuffix(filepath.Base(entry.ProtoFile), ".proto")
		if entry.ProtoPackage != "" {
			matchKey = entry.ProtoPackage
		}
		if matchKey == svc.PackageName && entry.TargetService != "" {
			meshTarget = entry.TargetService
			break
		}
	}
	return meshTarget
}

// generateRustW2SClientAggregators creates a flat target hierarchy:
//
//	sm.w2s_clients().{target}().{client}()
func generateRustW2SClientAggregators(servicesInfo []ServiceInfo) error {
	if len(servicesInfo) == 0 {
		return nil
	}
	config := servicesInfo[0].Config
	smDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "internal", "grpc_over_rest")

	allSvcNameCount := make(map[string]int)
	for _, svc := range servicesInfo {
		allSvcNameCount[svc.ServiceName]++
	}

	type targetEntry struct {
		target     string
		safeTarget string
		structName string
		fieldName  string
		svcs       []ServiceInfo
	}
	var targets []targetEntry
	targetSeen := make(map[string]int)
	for _, svc := range servicesInfo {
		target := resolveW2STarget(svc)
		safeTarget := strings.ReplaceAll(target, "-", "_")
		if idx, ok := targetSeen[target]; ok {
			targets[idx].svcs = append(targets[idx].svcs, svc)
		} else {
			targetSeen[target] = len(targets)
			targets = append(targets, targetEntry{
				target:     target,
				safeTarget: safeTarget,
				structName: strutil.ToPascalCase(safeTarget) + "W2SClients",
				fieldName:  strutil.ToSnakeCase(safeTarget),
				svcs:       []ServiceInfo{svc},
			})
		}
	}

	rootFile := filepath.Join(smDir, "w2s_clients.rs")
	var buf strings.Builder
	buf.WriteString(header.GeneratedHeader(consts.LangRust, "w2s_clients"))
	buf.WriteString("\nuse reqwest::Client;\n\n")

	firstSvc := servicesInfo[0]
	firstSvcSnake := strutil.StripPackagePrefix(tonicServiceModuleName(firstSvc.ServiceName), firstSvc.PackageName)
	fmt.Fprintf(&buf, "pub use super::%s::%s::%s::TokenProvider;\n\n",
		firstSvc.ParentDir, firstSvc.PackageName, firstSvcSnake)

	buf.WriteString("/// A factory that returns an audience-scoped TokenProvider for a given target service ID.\n")
	buf.WriteString("pub type TokenProviderFor = std::sync::Arc<dyn Fn(&str) -> Option<TokenProvider> + Send + Sync>;\n\n")

	for _, svc := range servicesInfo {
		svcSnake := strutil.StripPackagePrefix(tonicServiceModuleName(svc.ServiceName), svc.PackageName)
		origClientName := svc.ServiceName + "W2SClient"
		if allSvcNameCount[svc.ServiceName] > 1 {
			aliasName := strutil.ToPascalCase(svc.PackageName) + svc.ServiceName + "W2SClient"
			fmt.Fprintf(&buf, "use super::%s::%s::%s::%s as %s;\n",
				svc.ParentDir, svc.PackageName, svcSnake, origClientName, aliasName)
		} else {
			fmt.Fprintf(&buf, "use super::%s::%s::%s::%s;\n",
				svc.ParentDir, svc.PackageName, svcSnake, origClientName)
		}
	}

	for _, te := range targets {
		fmt.Fprintf(&buf, "\n/// Typed W2S clients for the \"%s\" service.\n", te.target)
		fmt.Fprintf(&buf, "pub struct %s {\n", te.structName)
		for _, svc := range te.svcs {
			fieldName, clientName := w2sClientNames(svc, allSvcNameCount)
			fmt.Fprintf(&buf, "    %s: %s,\n", fieldName, clientName)
		}
		buf.WriteString("}\n")
		fmt.Fprintf(&buf, "\nimpl %s {\n", te.structName)
		buf.WriteString("    pub fn new(client: Client, base_url: String, token_provider: Option<TokenProvider>) -> Self {\n")
		buf.WriteString("        Self {\n")
		for _, svc := range te.svcs {
			fieldName, clientName := w2sClientNames(svc, allSvcNameCount)
			fmt.Fprintf(&buf, "            %s: %s::new(client.clone(), base_url.clone(), token_provider.clone()),\n", fieldName, clientName)
		}
		buf.WriteString("        }\n    }\n\n")
		for _, svc := range te.svcs {
			fieldName, clientName := w2sClientNames(svc, allSvcNameCount)
			fmt.Fprintf(&buf, "    pub fn %s(&self) -> &%s {\n        &self.%s\n    }\n\n", fieldName, clientName, fieldName)
		}
		buf.WriteString("}\n")
	}

	buf.WriteString("\n/// Target service ID constants (from proto annotations).\n")
	for _, te := range targets {
		fmt.Fprintf(&buf, "pub const TARGET_%s: &str = \"%s\";\n", strings.ToUpper(strings.ReplaceAll(te.target, "-", "_")), te.target)
	}

	buf.WriteString("\n/// W2S client hierarchy: sm.w2s_clients().{target}().{client}()\n")
	buf.WriteString("pub struct W2SClients {\n")
	for _, te := range targets {
		fmt.Fprintf(&buf, "    %s: %s,\n", te.fieldName, te.structName)
	}
	buf.WriteString("}\n\nimpl W2SClients {\n")
	buf.WriteString("    pub fn new(client: Client, base_url: String, token_provider_for: &TokenProviderFor) -> Self {\n")
	buf.WriteString("        Self {\n")
	for _, te := range targets {
		fmt.Fprintf(&buf, "            %s: %s::new(client.clone(), base_url.clone(), token_provider_for(\"%s\")),\n",
			te.fieldName, te.structName, te.target)
	}
	buf.WriteString("        }\n    }\n\n")
	for _, te := range targets {
		fmt.Fprintf(&buf, "    pub fn %s(&self) -> &%s {\n        &self.%s\n    }\n\n", te.fieldName, te.structName, te.fieldName)
	}
	buf.WriteString("}\n")

	if err := os.MkdirAll(smDir, 0755); err != nil {
		return fmt.Errorf("failed to create W2S clients directory: %w", err)
	}
	if err := os.WriteFile(rootFile, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write W2S clients: %w", err)
	}

	return nil
}

func w2sClientNames(svc ServiceInfo, svcNameCount map[string]int) (fieldName, clientName string) {
	if svcNameCount[svc.ServiceName] > 1 {
		fieldName = svc.PackageName + "_" + tonicServiceModuleName(svc.ServiceName) + "_client"
		clientName = strutil.ToPascalCase(svc.PackageName) + svc.ServiceName + "W2SClient"
	} else {
		fieldName = tonicServiceModuleName(svc.ServiceName) + "_client"
		clientName = svc.ServiceName + "W2SClient"
	}
	return
}

func generateRustW2SClient(svc ServiceInfo, info *parser.ProtoInfo) error {
	outputDir := filepath.Join(svc.Config.OutputDir, svc.Config.GetSmPrefix(), "internal", "grpc_over_rest", svc.ParentDir, svc.PackageName)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	svcSnake := tonicServiceModuleName(svc.ServiceName)
	wrapperFile := strutil.StripPackagePrefix(svcSnake, svc.PackageName) + ".rs"
	outputPath := filepath.Join(outputDir, wrapperFile)

	zap.L().Info("Generating Rust W2S client", zap.String("path", outputPath))

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

	typesCratePathStr := strings.ReplaceAll(strings.ReplaceAll(svc.ParentDir, "-", "_"), string(filepath.Separator), "::") + "::" + svc.PackageName
	var serviceImports strings.Builder
	fmt.Fprintf(&serviceImports, "#[allow(unused_imports)]\nuse crate::types::%s::*;\n", typesCratePathStr)

	var methods strings.Builder
	var extraTypes strings.Builder
	for _, method := range targetService.Methods {
		if method.HttpPath == "" {
			return fmt.Errorf("%s.%s requires W2S REST route metadata; clients.w2s.enabled cannot emit a method without an HTTP path",
				targetService.Name, method.Name)
		}
		result := generateRustW2SClientMethod(targetService, method, info.Package, info.CrossPackageTypes)
		methods.WriteString(result.method)
		methods.WriteString("\n")
		if result.extraType != "" {
			extraTypes.WriteString(result.extraType)
			extraTypes.WriteString("\n")
		}
	}

	clientName := svc.ServiceName + "W2SClient"
	data := struct {
		Header         string
		ServiceName    string
		ClientName     string
		ServiceImports string
		Methods        string
		ExtraTypes     string
	}{
		Header:         header.GeneratedHeader(consts.LangRust, svc.ProtoFile),
		ServiceName:    svc.ServiceName,
		ClientName:     clientName,
		ServiceImports: serviceImports.String(),
		Methods:        methods.String(),
		ExtraTypes:     extraTypes.String(),
	}

	tmplStr, err := loadTemplate("w2sclient.rs.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load w2sclient template: %w", err)
	}

	tmpl, err := template.New("w2sclient").Parse(tmplStr)
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

type w2sMethodResult struct {
	method    string
	extraType string
}

func rustW2SHttpMethod(method *parser.MethodInfo, isEmptyInput bool) string {
	if isEmptyInput && method.HttpMethod != "" {
		return method.HttpMethod
	}
	return "POST"
}

func rustW2SBodyExpr(requestVar string, isEmptyInput bool) string {
	if isEmptyInput {
		return "serde_json::json!({})"
	}
	return fmt.Sprintf(`serde_json::to_value(&%s)
            .map_err(|e| tonic::Status::invalid_argument(format!("serialize request: {e}")))?`, requestVar)
}

func rustW2SCanValidate(fullType string) bool {
	fullType = strings.TrimPrefix(fullType, ".")
	return fullType != "" && fullType != "google.protobuf.Empty" && !strings.HasPrefix(fullType, "google.protobuf.")
}

func rustW2SRequestValidation(varName, fullType string) string {
	if !rustW2SCanValidate(fullType) {
		return ""
	}
	return fmt.Sprintf(`        %s.validate()
            .map_err(|e| tonic::Status::invalid_argument(format!("validation failed: {e}")))?;
`, varName)
}

func rustW2SResponseValidation(varName, fullType string) string {
	if !rustW2SCanValidate(fullType) {
		return ""
	}
	return fmt.Sprintf(`        %s.validate()
            .map_err(|e| tonic::Status::internal(format!("response validation failed: {e}")))?;
`, varName)
}

func rustW2SValidationClosure(fullType, outputType string) string {
	if !rustW2SCanValidate(fullType) {
		return fmt.Sprintf("|_: &%s| Ok(())", outputType)
	}
	return fmt.Sprintf(`|msg: &%s| {
            msg.validate()
                .map_err(|e| tonic::Status::internal(format!("response validation failed: {e}")))?;
            Ok(())
        }`, outputType)
}

func rustW2SBidiResponseValidation(varName, fullType string) string {
	if !rustW2SCanValidate(fullType) {
		return ""
	}
	return fmt.Sprintf(`                                    if let Err(e) = %s.validate() {
                                        errors += 1;
                                        tracing::warn!(error = %%e, errors, "W2S response validation failed");
                                        break;
                                    }
`, varName)
}

func generateRustW2SClientMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, servicePkg string, crossPkgTypes map[string]string) w2sMethodResult {
	methodSnake := prostSnakeName(method.Name)
	inputType, outputType := resolveRustTypeQualified(method, servicePkg, crossPkgTypes)
	isEmptyInput := method.FullInputType == "google.protobuf.Empty" || inputType == "()"
	isEmptyOutput := method.FullOutputType == "google.protobuf.Empty" || outputType == "()"
	httpMethod := rustW2SHttpMethod(method, isEmptyInput)
	bodyExpr := rustW2SBodyExpr("req", isEmptyInput)
	authRequired := !(svc.AuthDisabled || method.AuthDisabled)
	authRequiredRust := fmt.Sprintf("%t", authRequired)
	requestValidation := rustW2SRequestValidation("req", method.FullInputType)
	responseValidation := rustW2SResponseValidation("response.get_ref()", method.FullOutputType)
	sseValidation := rustW2SValidationClosure(method.FullOutputType, outputType)
	bidiRequestValidation := rustW2SRequestValidation("msg", method.FullInputType)
	bidiResponseValidation := rustW2SBidiResponseValidation("resp", method.FullOutputType)
	streamResponseValidation := rustW2SResponseValidation("resp", method.FullOutputType)

	if method.ClientStreaming && method.ServerStreaming {
		handleType := fmt.Sprintf("%sW2SHandle", strutil.ToPascalCase(method.Name))
		clientName := svc.Name + "W2SClient"
		methodCode := fmt.Sprintf(`    /// %s opens a managed W2S bidirectional stream to %s.%s.
    pub fn %s<F, Fut>(
        &self,
        on_connect: F,
        on_message: Arc<dyn Fn(%s) + Send + Sync>,
    ) -> %s
    where
        F: Fn(%s) -> Fut + Send + Sync + 'static,
        Fut: Future<Output = ()> + Send + 'static,
    {
        let (user_tx, mut user_rx) = tokio::sync::mpsc::channel::<W2SQueuedMessage>(32);
        let client = self.clone();
        let handle = %s {
            client: client.clone(),
            tx: user_tx,
            auth_required: %s,
        };
        let task_handle = handle.clone();
        let on_connect = Arc::new(on_connect);

        tokio::spawn(async move {
            let mut errors: u64 = 0;
            loop {
                let ws = match client.connect_websocket("%s").await {
                    Ok(ws) => ws,
                    Err(e) => {
                        errors += 1;
                        tracing::error!(error = %%e, errors, "%s: WebSocket connect failed");
                        tokio::time::sleep(Duration::from_secs(5)).await;
                        continue;
                    }
                };
                let (mut write, mut read) = ws.split();
                if let Err(e) = client.send_start_frame(&mut write, %s).await {
                    errors += 1;
                    tracing::error!(error = %%e, errors, "%s: WebSocket start failed");
                    tokio::time::sleep(Duration::from_secs(5)).await;
                    continue;
                }

                let connect_handle = task_handle.clone();
                let connect_callback = on_connect.clone();
                tokio::spawn(async move {
                    (connect_callback)(connect_handle).await;
                });

                loop {
                    tokio::select! {
                        msg = user_rx.recv() => {
                            match msg {
                                Some(queued) => {
                                    if queued.complete {
                                        let result = client.send_complete_frame(&mut write).await;
                                        let _ = queued.ack.send(result);
                                        return;
                                    }
                                    if queued.ack.is_closed() {
                                        continue;
                                    }
                                    match client.send_message_frame_with_auth(&mut write, queued.body, queued.authorization).await {
                                        Ok(()) => {
                                            let _ = queued.ack.send(Ok(()));
                                        }
                                        Err(e) => {
                                            errors += 1;
                                            tracing::warn!(error = %%e, errors, "%s: outbound send failed");
                                            let _ = queued.ack.send(Err(e));
                                            break;
                                        }
                                    }
                                }
                                None => {
                                    let _ = client.send_complete_frame(&mut write).await;
                                    return;
                                }
                            }
                        }
                        msg = read.next() => {
                            match client.decode_ws_message::<%s>(msg).await {
                                Ok(DecodedWsMessage::Message(resp)) => {
%s
                                    on_message(resp)
                                },
                                Ok(DecodedWsMessage::Continue) => continue,
                                Ok(DecodedWsMessage::Closed) => break,
                                Err(e) => {
                                    errors += 1;
                                    tracing::warn!(error = %%e, errors, "%s: inbound receive failed");
                                    break;
                                }
                            }
                        }
                    }
                }

                tokio::time::sleep(Duration::from_secs(5)).await;
            }
        });

        handle
    }`, method.Name, svc.Name, method.Name, methodSnake, outputType, handleType,
			handleType, handleType,
			authRequiredRust, method.HttpPath, method.Name, authRequiredRust, method.Name,
			method.Name, outputType, bidiResponseValidation, method.Name)

		handleStruct := fmt.Sprintf(`/// Handle for the managed %s W2S bidirectional stream.
#[derive(Clone)]
pub struct %s {
    client: %s,
    tx: tokio::sync::mpsc::Sender<W2SQueuedMessage>,
    auth_required: bool,
}

impl %s {
    /// Send a message on the W2S bidirectional stream.
    pub async fn send(&self, msg: %s) -> Result<(), tonic::Status> {
        let send_timeout = self.client.connect_timeout;
        let permit = timeout(send_timeout, self.tx.reserve())
            .await
            .map_err(|_| tonic::Status::deadline_exceeded("W2S bidirectional send queue timed out"))?
            .map_err(|_| tonic::Status::unavailable("W2S bidirectional stream closed"))?;
%s
        let body = %s;
        let authorization = self.client.auth_header(self.auth_required).await?;
        let (ack, sent) = tokio::sync::oneshot::channel();
        permit.send(W2SQueuedMessage {
            body,
            authorization,
            ack,
            complete: false,
        });
        timeout(self.client.connect_timeout, sent)
            .await
            .map_err(|_| tonic::Status::deadline_exceeded("W2S bidirectional send timed out"))?
            .map_err(|_| tonic::Status::unavailable("W2S bidirectional stream closed"))?
    }

    /// Sends a complete frame and closes the W2S bidirectional stream task.
    pub async fn finish(&self) -> Result<(), tonic::Status> {
        let send_timeout = self.client.connect_timeout;
        let permit = timeout(send_timeout, self.tx.reserve())
            .await
            .map_err(|_| tonic::Status::deadline_exceeded("W2S bidirectional finish queue timed out"))?
            .map_err(|_| tonic::Status::unavailable("W2S bidirectional stream closed"))?;
        let (ack, sent) = tokio::sync::oneshot::channel();
        permit.send(W2SQueuedMessage {
            body: Value::Null,
            authorization: None,
            ack,
            complete: true,
        });
        timeout(self.client.connect_timeout, sent)
            .await
            .map_err(|_| tonic::Status::deadline_exceeded("W2S bidirectional finish timed out"))?
            .map_err(|_| tonic::Status::unavailable("W2S bidirectional stream closed"))?
    }
}
`, method.Name, handleType, clientName, handleType, inputType, bidiRequestValidation, rustW2SBodyExpr("msg", isEmptyInput))
		return w2sMethodResult{method: methodCode, extraType: handleStruct}
	}

	if method.ClientStreaming {
		if isEmptyOutput {
			return w2sMethodResult{method: fmt.Sprintf(`    /// %s calls %s.%s via W2S WebSocket client streaming.
    pub async fn %s(
        &self,
        request: impl tonic::IntoStreamingRequest<Message = %s>,
    ) -> Result<tonic::Response<()>, tonic::Status> {
        let mut outbound = Box::pin(request.into_streaming_request().into_inner());
        let mut ws = self.connect_websocket("%s").await?;
        self.send_start_frame(&mut ws, %s).await?;
        while let Some(req) = outbound.next().await {
%s
            let body = %s;
            self.send_message_frame(&mut ws, body, %s).await?;
        }
        self.send_complete_frame(&mut ws).await?;
        self.recv_ws_unit(&mut ws).await
    }`, method.Name, svc.Name, method.Name, methodSnake, inputType, method.HttpPath, authRequiredRust, requestValidation, rustW2SBodyExpr("req", isEmptyInput), authRequiredRust)}
		}
		return w2sMethodResult{method: fmt.Sprintf(`    /// %s calls %s.%s via W2S WebSocket client streaming.
    pub async fn %s(
        &self,
        request: impl tonic::IntoStreamingRequest<Message = %s>,
    ) -> Result<tonic::Response<%s>, tonic::Status> {
        let mut outbound = Box::pin(request.into_streaming_request().into_inner());
        let mut ws = self.connect_websocket("%s").await?;
        self.send_start_frame(&mut ws, %s).await?;
        while let Some(req) = outbound.next().await {
%s
            let body = %s;
            self.send_message_frame(&mut ws, body, %s).await?;
        }
        self.send_complete_frame(&mut ws).await?;
        match self.recv_ws_json::<%s, _>(&mut ws).await? {
            Some(resp) => {
%s
                Ok(tonic::Response::new(resp))
            },
            None => Err(tonic::Status::unavailable("W2S stream closed before response")),
        }
    }`, method.Name, svc.Name, method.Name, methodSnake, inputType, outputType, method.HttpPath, authRequiredRust, requestValidation, rustW2SBodyExpr("req", isEmptyInput), authRequiredRust, outputType, streamResponseValidation)}
	}

	if method.ServerStreaming {
		return w2sMethodResult{method: fmt.Sprintf(`    /// %s calls %s.%s via W2S SSE server streaming.
    pub async fn %s(
        &self,
        request: impl tonic::IntoRequest<%s>,
    ) -> Result<Pin<Box<dyn Stream<Item = Result<%s, tonic::Status>> + Send>>, tonic::Status> {
        let req = request.into_request().into_inner();
%s
        let body = %s;
        self.stream_sse("%s", "%s", body, %s, %s).await
    }`, method.Name, svc.Name, method.Name, methodSnake, inputType, outputType, requestValidation, bodyExpr, httpMethod, method.HttpPath, authRequiredRust, sseValidation)}
	}

	if isEmptyOutput {
		return w2sMethodResult{method: fmt.Sprintf(`    /// %s calls %s.%s via W2S HTTP/JSON.
    pub async fn %s(
        &self,
        request: impl tonic::IntoRequest<%s>,
    ) -> Result<tonic::Response<()>, tonic::Status> {
        let req = request.into_request().into_inner();
%s
        let body = %s;
        self.do_json_unit("%s", "%s", body, %s).await
    }`, method.Name, svc.Name, method.Name, methodSnake, inputType, requestValidation, bodyExpr, httpMethod, method.HttpPath, authRequiredRust)}
	}

	return w2sMethodResult{method: fmt.Sprintf(`    /// %s calls %s.%s via W2S HTTP/JSON.
    pub async fn %s(
        &self,
        request: impl tonic::IntoRequest<%s>,
    ) -> Result<tonic::Response<%s>, tonic::Status> {
        let req = request.into_request().into_inner();
%s
        let body = %s;
        let response = self.do_json_value::<%s>("%s", "%s", body, %s).await?;
%s
        Ok(response)
    }`, method.Name, svc.Name, method.Name, methodSnake, inputType, outputType, requestValidation, bodyExpr, outputType, httpMethod, method.HttpPath, authRequiredRust, responseValidation)}
}
