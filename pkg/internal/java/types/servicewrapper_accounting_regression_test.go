// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
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

	// tacplus#273: the response is validated in the RESPONSE stage, after the
	// handler returned, and before the call completes; a validation failure
	// reaches the accounted observer's onError, which records it.
	handlerStage := strings.Index(rendered, "__acct.stage(AccountingManager.Stage.HANDLER);")
	handler := strings.Index(rendered, "response = handler.publicRead(caller, request);")
	responseStage := strings.Index(rendered, "__acct.stage(AccountingManager.Stage.RESPONSE);")
	validation := strings.Index(rendered, "Helpers.validateContractResponse(response);")
	completed := strings.LastIndex(rendered, "responseObserver.onCompleted();")
	if handlerStage < 0 || handler < 0 || responseStage < 0 || validation < 0 || completed < 0 {
		t.Fatalf("rendered Java wrapper missing a stage, the validation or the completion:\n%s", rendered)
	}
	if !(handlerStage < handler && handler < responseStage && responseStage < validation && validation < completed) {
		t.Fatalf("Java unary must run HANDLER -> handler -> RESPONSE -> validation -> completion:\n%s", rendered)
	}
	for _, gone := range []string{"em.rpcError", "em.rpcCompleted", "errorContextFromError", "startTime"} {
		if strings.Contains(rendered, gone) {
			t.Fatalf("Java wrapper still emits the removed accounting shape %q:\n%s", gone, rendered)
		}
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

	// The stream's one record is emitted when the sender thread ends it: the
	// sender writes through the accounted observer the override handed in.
	if !strings.Contains(rendered, "watch__accounted(request, accountedResponseObserver(responseObserver, __acct), __acct);") {
		t.Fatalf("Java WATCH body must receive the accounted response observer:\n%s", rendered)
	}
	thread := strings.Index(rendered, `Thread __watchThread = new Thread(() -> {`)
	completed := strings.Index(rendered, "__out.onCompleted();")
	errored := strings.Index(rendered, "__out.onError(toGrpcError(t));")
	if thread < 0 || completed < thread || errored < thread {
		t.Fatalf("Java WATCH must end the stream (and so its record) inside the sender thread:\n%s", rendered)
	}
	handlerStage := strings.Index(rendered, "__acct.stage(AccountingManager.Stage.HANDLER);")
	source := strings.Index(rendered, "__source = handler.watch(caller, request);")
	responseStage := strings.Index(rendered, "__acct.stage(AccountingManager.Stage.RESPONSE);")
	if handlerStage < 0 || !(handlerStage < source && source < responseStage && responseStage < thread) {
		t.Fatalf("Java WATCH must stage HANDLER around opening the source and RESPONSE before sending:\n%s", rendered)
	}
	if strings.Contains(rendered, "em.rpc") {
		t.Fatalf("Java WATCH must not emit outside the accounted observer:\n%s", rendered)
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
		"public StreamObserver<Contractclient.ContractRequest> upload(StreamObserver<Contractclient.ContractResponse> responseObserver) {",
		"AccountingManager.Call __acct = AccountingManager.Call.begin(this.emitter,",
		"AccountingMethod.newBuilder().setContract(Services.ContractRPCs.CONTRACT_RPCS_UPLOAD).build(), Auth.requestIdFromContext());",
		"return accountedRequestObserver(upload__accounted(accountedResponseObserver(responseObserver, __acct), __acct), __acct);",
		"private StreamObserver<Contractclient.ContractRequest> upload__accounted(StreamObserver<Contractclient.ContractResponse> responseObserver, AccountingManager.Call __acct) {",
		"validatingResponseObserver(responseObserver, value -> Helpers.validateContractResponse(value))",
		"validatingRequestObserver(handler.upload(caller, validatingResponseObserver(responseObserver",
		"Helpers.validateContractRequest(value);",
		"__acct.authorized(callerIdentity);",
		"__acct.stage(AccountingManager.Stage.HANDLER);",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Java client-streaming accounting wrapper missing %q:\n%s", want, rendered)
		}
	}
	for _, gone := range []string{"__accounted = new java.util.concurrent.atomic.AtomicBoolean", "accountingResponseObserver(", "accountingRequestObserver(", "Accounting not supported"} {
		if strings.Contains(rendered, gone) {
			t.Fatalf("Java client-streaming accounting wrapper still carries %q:\n%s", gone, rendered)
		}
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
		"public StreamObserver<Contractclient.ContractRequest> chat(StreamObserver<Contractclient.ContractEvent> responseObserver) {",
		"AccountingManager.Call __acct = AccountingManager.Call.begin(this.emitter,",
		"AccountingMethod.newBuilder().setContract(Services.ContractRPCs.CONTRACT_RPCS_CHAT).build(), Auth.requestIdFromContext());",
		"return accountedRequestObserver(chat__accounted(accountedResponseObserver(responseObserver, __acct), __acct), __acct);",
		"private StreamObserver<Contractclient.ContractRequest> chat__accounted(StreamObserver<Contractclient.ContractEvent> responseObserver, AccountingManager.Call __acct) {",
		"validatingResponseObserver(responseObserver, value -> Helpers.validateContractEvent(value))",
		"validatingRequestObserver(handler.chat(caller, validatingResponseObserver(responseObserver",
		"Helpers.validateContractRequest(value);",
		"__acct.authorized(callerIdentity);",
		"__acct.stage(AccountingManager.Stage.HANDLER);",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Java bidi accounting wrapper missing %q:\n%s", want, rendered)
		}
	}
	for _, gone := range []string{"__accounted = new java.util.concurrent.atomic.AtomicBoolean", "accountingResponseObserver(", "accountingRequestObserver(", "Accounting not supported"} {
		if strings.Contains(rendered, gone) {
			t.Fatalf("Java bidi accounting wrapper still carries %q:\n%s", gone, rendered)
		}
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

// A client- or bidi-streaming method with a target_namespace_field authorizes
// every message against that message's own namespace value, downstream of the
// stream-open preflight. The preflight is therefore token-only — "is this a
// valid identity at all" — which is exactly what ServiceManager's
// setAuthenticateFunc wiring documents for these methods. checkAuthenticate("/")
// is not that: it routes to authenticateAtOrBelow("/"), which admits only a
// caller sitting exactly at "/", so every real caller was denied at stream open
// before the per-message check could ever run.
func TestJavaStreamPreflightAuthenticatesTokenOnly(t *testing.T) {
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

			if !strings.Contains(rendered, "callerIdentity = checkAuthenticate();") {
				t.Fatalf("Java stream preflight must authenticate token-only:\n%s", rendered)
			}
			if strings.Contains(rendered, `checkAuthenticate("/")`) {
				t.Fatalf("Java stream preflight must not gate on the \"/\" namespace:\n%s", rendered)
			}
			// The real namespace comparison still happens per message.
			if !strings.Contains(rendered, fmt.Sprintf(`checkAuth("ContractApi/%s", value);`, tc.method.Name)) {
				t.Fatalf("per-message target namespace authorization missing:\n%s", rendered)
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
	// The record is attributed to the identity checkAuth returned, recorded
	// on the call right after the check passes.
	check := strings.Index(authorized, `callerIdentity = checkAuth("ContractApi/Read", request);`)
	attributed := strings.Index(authorized, "__acct.authorized(callerIdentity);")
	if attributed < 0 || attributed < check {
		t.Fatalf("authorized RPC must attribute its record to the checked identity:\n%s", authorized)
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
