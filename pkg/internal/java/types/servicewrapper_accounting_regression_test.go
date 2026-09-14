// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

func TestJavaUnaryAccountingTreatsResponseValidationAsError(t *testing.T) {
	rendered := generateJavaGRPCWrapper(
		&parser.MethodInfo{
			Name:           "PublicRead",
			InputType:      "ContractRequest",
			OutputType:     "ContractResponse",
			FullInputType:  "contractclient.ContractRequest",
			FullOutputType: "contractclient.ContractResponse",
			AuthDisabled:   true,
		},
		"ContractApi",
		"contract",
		"Contractclient.ContractRequest",
		"Contractclient.ContractResponse",
		"Helpers",
		"Helpers",
		false,
		true,
		map[string]service.AccountingInfo{
			"contract": {EnumName: "Contract", RPCs: map[string]bool{"PublicRead": true}},
		},
		"contractclient",
		"contract",
		map[string]string{},
		proto.SkippedTraceTypes{},
		nil,
		nil,
		nil,
	)

	validation := strings.Index(rendered, "Helpers.validateContractResponse(response);")
	completed := strings.Index(rendered, "em.rpcCompleted")
	if validation < 0 || completed < 0 {
		t.Fatalf("rendered Java wrapper missing validation or completed accounting:\n%s", rendered)
	}
	if validation > completed {
		t.Fatalf("Java response validation must run before rpcCompleted:\n%s", rendered)
	}
	if !strings.Contains(rendered[validation:completed], "em.rpcError") {
		t.Fatalf("Java response-validation failure does not emit rpcError before completion:\n%s", rendered)
	}
}

func TestJavaUnaryTraceTreatsHandlerAndValidationErrorsAsResponseTrace(t *testing.T) {
	rendered := generateJavaGRPCWrapper(
		&parser.MethodInfo{
			Name:           "PublicRead",
			InputType:      "ContractRequest",
			OutputType:     "ContractResponse",
			FullInputType:  "contractclient.ContractRequest",
			FullOutputType: "contractclient.ContractResponse",
			AuthDisabled:   true,
		},
		"ContractApi",
		"contract",
		"Contractclient.ContractRequest",
		"Contractclient.ContractResponse",
		"Helpers",
		"Helpers",
		false,
		true,
		map[string]service.AccountingInfo{
			"contract": {EnumName: "Contract", RPCs: map[string]bool{"PublicRead": true}},
		},
		"contractclient",
		"contract",
		map[string]string{},
		proto.SkippedTraceTypes{},
		nil,
		nil,
		nil,
	)

	for _, want := range []string{
		"catch (Exception handlerEx)",
		".setError(handlerEx.getMessage() == null ? handlerEx.toString() : handlerEx.getMessage())",
		"Helpers.validateContractResponse(response);",
		"Trace.TraceEventKind.TRACE_EVENT_KIND_INTERNAL_RESPONSE",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Java unary trace wrapper missing %q:\n%s", want, rendered)
		}
	}
}

func TestJavaWatchAccountingRunsInSenderThread(t *testing.T) {
	rendered := generateJavaGRPCWrapper(
		javaWatchMethod(),
		"Catalog",
		"catalog",
		"Catalog.WatchReq",
		"Catalog.WatchEvent",
		"",
		"",
		false,
		true,
		map[string]service.AccountingInfo{
			"catalog": {EnumName: "Catalog", RPCs: map[string]bool{"Watch": true}},
		},
		"catalog",
		"services",
		map[string]string{},
		proto.SkippedTraceTypes{},
		nil,
		nil,
		nil,
	)

	thread := strings.Index(rendered, `Thread __watchThread = new Thread(() -> {`)
	completed := strings.Index(rendered, "em.rpcCompleted(accountingMethod")
	errored := strings.Index(rendered, "em.rpcError(accountingMethod")
	if thread < 0 || completed < 0 || errored < 0 {
		t.Fatalf("Java WATCH accounting must emit completed and error inside sender thread:\n%s", rendered)
	}
	if completed < thread || errored < thread {
		t.Fatalf("Java WATCH accounting must run in the sender thread lifecycle:\n%s", rendered)
	}
	if strings.Contains(rendered[:thread], "em.rpcCompleted(accountingMethod") {
		t.Fatalf("Java WATCH accounting must not complete before the sender thread runs:\n%s", rendered)
	}
}

func TestJavaClientStreamingAccountingUsesAsyncLifecycle(t *testing.T) {
	rendered := generateJavaGRPCWrapper(
		&parser.MethodInfo{
			Name:            "Upload",
			InputType:       "ContractRequest",
			OutputType:      "ContractResponse",
			FullInputType:   "contractclient.ContractRequest",
			FullOutputType:  "contractclient.ContractResponse",
			ClientStreaming: true,
			AuthDisabled:    true,
		},
		"ContractApi",
		"contract",
		"Contractclient.ContractRequest",
		"Contractclient.ContractResponse",
		"Helpers",
		"Helpers",
		false,
		true,
		map[string]service.AccountingInfo{
			"contract": {EnumName: "Contract", RPCs: map[string]bool{"Upload": true}},
		},
		"contractclient",
		"contract",
		map[string]string{},
		proto.SkippedTraceTypes{},
		nil,
		nil,
		nil,
	)

	for _, want := range []string{
		"AccountingMethod accountingMethod = AccountingMethod.newBuilder().setContract(Services.ContractRPCs.CONTRACT_RPCS_UPLOAD).build();",
		"java.util.concurrent.atomic.AtomicBoolean __accounted = new java.util.concurrent.atomic.AtomicBoolean(false);",
		"accountingResponseObserver(responseObserver, accountingMethod, startTime, callerIdentity, requestId, __accounted)",
		"validatingResponseObserver(__accountingResponseObserver, value -> Helpers.validateContractResponse(value))",
		"validatingRequestObserver(handler.upload(caller, validatingResponseObserver(__accountingResponseObserver",
		"Helpers.validateContractRequest(value);",
		"}, __accountingResponseObserver);",
		"return accountingRequestObserver(__handlerObserver, accountingMethod, startTime, callerIdentity, requestId, __accounted);",
		"__accountingResponseObserver.onError(toGrpcError(e));",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Java client-streaming accounting wrapper missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "Accounting not supported") {
		t.Fatalf("Java client-streaming accounting must not retain unsupported comments:\n%s", rendered)
	}
}

func TestJavaBidiAccountingUsesAsyncLifecycle(t *testing.T) {
	rendered := generateJavaGRPCWrapper(
		&parser.MethodInfo{
			Name:            "Chat",
			InputType:       "ContractRequest",
			OutputType:      "ContractEvent",
			FullInputType:   "contractclient.ContractRequest",
			FullOutputType:  "contractclient.ContractEvent",
			ClientStreaming: true,
			ServerStreaming: true,
			AuthDisabled:    true,
		},
		"ContractApi",
		"contract",
		"Contractclient.ContractRequest",
		"Contractclient.ContractEvent",
		"Helpers",
		"Helpers",
		false,
		true,
		map[string]service.AccountingInfo{
			"contract": {EnumName: "Contract", RPCs: map[string]bool{"Chat": true}},
		},
		"contractclient",
		"contract",
		map[string]string{},
		proto.SkippedTraceTypes{},
		nil,
		nil,
		nil,
	)

	for _, want := range []string{
		"AccountingMethod accountingMethod = AccountingMethod.newBuilder().setContract(Services.ContractRPCs.CONTRACT_RPCS_CHAT).build();",
		"java.util.concurrent.atomic.AtomicBoolean __accounted = new java.util.concurrent.atomic.AtomicBoolean(false);",
		"accountingResponseObserver(responseObserver, accountingMethod, startTime, callerIdentity, requestId, __accounted)",
		"validatingResponseObserver(__accountingResponseObserver, value -> Helpers.validateContractEvent(value))",
		"validatingRequestObserver(handler.chat(caller, validatingResponseObserver(__accountingResponseObserver",
		"Helpers.validateContractRequest(value);",
		"}, __accountingResponseObserver);",
		"return accountingRequestObserver(__handlerObserver, accountingMethod, startTime, callerIdentity, requestId, __accounted);",
		"__accountingResponseObserver.onError(toGrpcError(e));",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Java bidi accounting wrapper missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "Accounting not supported") {
		t.Fatalf("Java bidi accounting must not retain unsupported comments:\n%s", rendered)
	}
}

func TestJavaServerStreamingValidatesResponseItemsBeforeSend(t *testing.T) {
	rendered := generateJavaGRPCWrapper(
		&parser.MethodInfo{
			Name:            "WatchAudit",
			InputType:       "AuditRequest",
			OutputType:      "AuditEvent",
			FullInputType:   "contractclient.AuditRequest",
			FullOutputType:  "contractclient.AuditEvent",
			ServerStreaming: true,
			AuthDisabled:    true,
		},
		"ContractApi",
		"contract",
		"Contractclient.AuditRequest",
		"Contractclient.AuditEvent",
		"Helpers",
		"Helpers",
		false,
		true,
		map[string]service.AccountingInfo{},
		"contractclient",
		"contract",
		map[string]string{},
		proto.SkippedTraceTypes{},
		nil,
		nil,
		nil,
	)

	for _, want := range []string{
		"validatingResponseObserver(responseObserver, value -> Helpers.validateAuditEvent(value))",
		"handler.watchAudit(caller, request, validatingResponseObserver(responseObserver",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Java server-streaming wrapper missing %q:\n%s", want, rendered)
		}
	}
	rawHandler := strings.Index(rendered, "handler.watchAudit(caller, request, responseObserver)")
	validatingHandler := strings.Index(rendered, "handler.watchAudit(caller, request, validatingResponseObserver(responseObserver")
	if rawHandler >= 0 || validatingHandler < 0 {
		t.Fatalf("Java server-streaming handler must receive the validating response observer:\n%s", rendered)
	}
}

func TestJavaMixedAuthServiceKeepsProtectedMethodsAuthorized(t *testing.T) {
	outputDir := t.TempDir()
	wrapperDir := filepath.Join(outputDir, "src", "main", "java", "com", "example", "s2sm", "internal", "grpc", "servers", "contract", "contractclient")
	typesDir := filepath.Join(outputDir, "src", "main", "java", "com", "example", "types", "contract", "contractclient", "types")
	cfg := &tactype.Config{
		OutputDir:       outputDir,
		JavaPackageBase: "com.example",
	}
	svc := ServiceInfo{
		ProtoFile:      "contract/contract.proto",
		PackageName:    "contractclient",
		ServiceName:    "ContractApi",
		GrpcName:       "contract",
		IsGrpc:         true,
		IsC2S:          true,
		OutputDir:      wrapperDir,
		TypesOutputDir: typesDir,
		ParentDir:      "contract",
		Config:         cfg,
		AuthAnyUser:    true,
	}
	info := &parser.ProtoInfo{
		Package: "contractclient",
		Services: []*parser.ServiceInfo{{
			Name: "ContractApi",
			Methods: []*parser.MethodInfo{
				{
					Name:           "Read",
					InputType:      "ContractRequest",
					OutputType:     "ContractResponse",
					FullInputType:  "contractclient.ContractRequest",
					FullOutputType: "contractclient.ContractResponse",
				},
				{
					Name:            "Watch",
					InputType:       "ContractRequest",
					OutputType:      "ContractEvent",
					FullInputType:   "contractclient.ContractRequest",
					FullOutputType:  "contractclient.ContractEvent",
					ServerStreaming: true,
					AuthAnyUser:     true,
				},
			},
		}},
	}

	if _, err := generateServiceWrapper(svc, info, nil, proto.SkippedTraceTypes{}, nil); err != nil {
		t.Fatalf("generateServiceWrapper: %v", err)
	}
	renderedBytes, err := os.ReadFile(filepath.Join(wrapperDir, "ContractApi.java"))
	if err != nil {
		t.Fatalf("read generated wrapper: %v", err)
	}
	rendered := string(renderedBytes)
	readStart := strings.Index(rendered, "public void read(")
	watchStart := strings.Index(rendered, "public void watch(")
	if readStart < 0 || watchStart < 0 || watchStart <= readStart {
		t.Fatalf("generated wrapper missing read/watch methods:\n%s", rendered)
	}
	readBlock := rendered[readStart:watchStart]
	watchBlock := rendered[watchStart:]

	if !strings.Contains(readBlock, `checkAuth("ContractApi/Read", request);`) {
		t.Fatalf("protected Read did not use full authorization:\n%s", readBlock)
	}
	if strings.Contains(readBlock, "checkAuthenticate();") {
		t.Fatalf("protected Read used auth_any_user authentication only:\n%s", readBlock)
	}
	// Watch is auth_any_user with no target_namespace_field: any authenticated
	// caller may call it, so the gate is authentication with no namespace check.
	if !strings.Contains(watchBlock, "checkAuthenticate();") {
		t.Fatalf("auth_any_user Watch did not authenticate:\n%s", watchBlock)
	}
	if strings.Contains(watchBlock, `checkAuthenticate("`) {
		t.Fatalf("auth_any_user Watch with no target_namespace_field must not gate on a namespace:\n%s", watchBlock)
	}
	for _, want := range []string{
		"if (auth == null)",
		"ProtoException.newErrUnauthorized",
		"authenticator not configured",
		"auth.authenticate(targetNamespace);",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Java auth_any_user authenticator path must fail closed; missing %q:\n%s", want, rendered)
		}
	}
}

func TestJavaStreamPreflightUsesRootNamespaceGate(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method *parser.MethodInfo
	}{
		{
			name: "client streaming target namespace field",
			method: &parser.MethodInfo{
				Name:                 "Upload",
				InputType:            "ContractRequest",
				OutputType:           "ContractResponse",
				FullInputType:        "contractclient.ContractRequest",
				FullOutputType:       "contractclient.ContractResponse",
				ClientStreaming:      true,
				TargetNamespaceField: "namespace",
			},
		},
		{
			name: "bidi target namespace field",
			method: &parser.MethodInfo{
				Name:                 "Chat",
				InputType:            "ContractRequest",
				OutputType:           "ContractResponse",
				FullInputType:        "contractclient.ContractRequest",
				FullOutputType:       "contractclient.ContractResponse",
				ClientStreaming:      true,
				ServerStreaming:      true,
				TargetNamespaceField: "namespace",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered := generateJavaGRPCWrapper(
				tc.method,
				"ContractApi",
				"contract",
				"Contractclient.ContractRequest",
				"Contractclient.ContractResponse",
				"Helpers",
				"Helpers",
				false,
				false,
				map[string]service.AccountingInfo{},
				"contractclient",
				"contract",
				map[string]string{},
				proto.SkippedTraceTypes{},
				nil,
				nil,
				nil,
			)

			if !strings.Contains(rendered, `checkAuthenticate("/");`) {
				t.Fatalf("Java stream preflight must use root namespace authentication gate:\n%s", rendered)
			}
			if strings.Contains(rendered, "checkAuthenticate();") {
				t.Fatalf("Java stream preflight must not use token-only checkAuthenticate():\n%s", rendered)
			}
		})
	}
}

// F10: an accounting record has to name the principal the Authorizer verified.
// The wrapper used to hand every emit a fresh empty map, so the only thing
// tying a record to a caller was the caller-written x-request-id header.
func TestJavaAccountingCarriesVerifiedCallerIdentity(t *testing.T) {
	accounting := map[string]service.AccountingInfo{
		"contract": {EnumName: "Contract", RPCs: map[string]bool{"Read": true, "PublicRead": true}},
	}

	authorized := generateJavaGRPCWrapper(
		&parser.MethodInfo{
			Name:           "Read",
			InputType:      "ContractRequest",
			OutputType:     "ContractResponse",
			FullInputType:  "contractclient.ContractRequest",
			FullOutputType: "contractclient.ContractResponse",
		},
		"ContractApi", "contract",
		"Contractclient.ContractRequest", "Contractclient.ContractResponse",
		"", "", false, false, accounting,
		"contractclient", "contract",
		map[string]string{}, proto.SkippedTraceTypes{}, nil, nil, nil,
	)

	if strings.Contains(authorized, "new java.util.HashMap<>(), requestId") {
		t.Fatalf("accounting must not emit an empty identity map on an authorized RPC:\n%s", authorized)
	}
	if !strings.Contains(authorized, `callerIdentity = checkAuth("ContractApi/Read", request);`) {
		t.Fatalf("authorized RPC must source its identity from checkAuth:\n%s", authorized)
	}
	for _, want := range []string{
		"em.rpcCompleted(accountingMethod, durationMs, callerIdentity, requestId);",
		"callerIdentity, requestId);",
	} {
		if !strings.Contains(authorized, want) {
			t.Fatalf("authorized RPC accounting missing %q:\n%s", want, authorized)
		}
	}

	// auth_disabled: nothing verifies a caller, so the record must carry no
	// identity at all rather than something the caller asserted.
	noAuth := generateJavaGRPCWrapper(
		&parser.MethodInfo{
			Name:           "PublicRead",
			InputType:      "ContractRequest",
			OutputType:     "ContractResponse",
			FullInputType:  "contractclient.ContractRequest",
			FullOutputType: "contractclient.ContractResponse",
			AuthDisabled:   true,
		},
		"ContractApi", "contract",
		"Contractclient.ContractRequest", "Contractclient.ContractResponse",
		"", "", false, true, accounting,
		"contractclient", "contract",
		map[string]string{}, proto.SkippedTraceTypes{}, nil, nil, nil,
	)

	if !strings.Contains(noAuth, "java.util.Map<String, String> callerIdentity = java.util.Collections.emptyMap();") {
		t.Fatalf("auth_disabled RPC must account with an empty identity:\n%s", noAuth)
	}
	if strings.Contains(noAuth, "checkAuth(") {
		t.Fatalf("auth_disabled RPC must not call checkAuth:\n%s", noAuth)
	}
}
