// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"text/template"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/accountingpolicy"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
)

// renderJavaAmForTest renders chassis/am/AccountingManager.java.tmpl the way
// writeLibTemplates does, with the durable sink on.
func renderJavaAmForTest(t *testing.T) string {
	t.Helper()
	src := readJavaTemplate(t, "java/chassis/am/AccountingManager.java.tmpl")
	tmpl, err := template.New("am").Parse(src)
	if err != nil {
		t.Fatalf("parse am template: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, map[string]any{
		"ChassisBase":                "com.example.chassis",
		"TypesBase":                  "com.example.types",
		"HasAccountingRecord":        true,
		"AccountingRecordTypeImport": "com.example.types.vservices.accountingrecord.Accountingrecord",
		"AccountingRecordJavaType":   "Accountingrecord.AccountingRecord",
		"ServiceRPCs":                []javaAmServiceRPC{{FieldName: "Contract"}},
		"Acct":                       accountingpolicy.TemplateContext(),
	}); err != nil {
		t.Fatalf("execute am template: %v", err)
	}
	return buf.String()
}

// tacplus#273: the Java am's classification is rendered from
// accountingpolicy, row for row, so it cannot drift from Go's and Rust's.
func TestJavaAmClassificationRenderedFromPolicy(t *testing.T) {
	am := renderJavaAmForTest(t)

	want := []string{
		"public enum Stage {\n        " + strings.Join(accountingpolicy.Stages, ", ") + "\n    }",
		"return Accounting.RpcOutcome.RPC_OUTCOME_" + accountingpolicy.DefaultOutcome + ";",
		`public static final String INTERNAL_HEADER = "` + accountingpolicy.InternalHeader + `";`,
	}
	for _, c := range accountingpolicy.CodeOutcomes {
		want = append(want, fmt.Sprintf("case %d: // %s\n                return Accounting.RpcOutcome.RPC_OUTCOME_%s;", c.Code, c.Name, c.Outcome))
	}
	for _, r := range accountingpolicy.StageReasons {
		want = append(want, fmt.Sprintf("if (stage == Stage.%s && outcome == Accounting.RpcOutcome.RPC_OUTCOME_%s) {\n            return Accounting.RpcReason.RPC_REASON_%s;", r.Stage, r.Outcome, r.Reason))
	}
	for _, l := range accountingpolicy.OutcomeLevelRows() {
		want = append(want, fmt.Sprintf("case RPC_OUTCOME_%s:\n                return Accounting.EventLevel.EVENT_LEVEL_%s;", l.Outcome, l.Level))
	}
	for _, w := range want {
		if !strings.Contains(am, w) {
			t.Fatalf("Java am missing classification row %q:\n%s", w, am)
		}
	}
}

// The am's API: one emitter method for the RPC record, the handler metrics, and
// none of the removed free-text emitters.
func TestJavaAmEmitsOneTypedRpcRecord(t *testing.T) {
	am := renderJavaAmForTest(t)

	for _, w := range []string{
		"public void rpc(AccountingMethod method, Stage stage, Throwable err, long durationMs,",
		".setOutcome(outcome)",
		".setReason(reason)",
		".setCode(code)",
		"newEvent(levelFor(outcome), identity, requestId)",
		// FAILED: the error text goes to the console at DEBUG, never the sink.
		"DEBUG_LOG.debug(",
		"public static void setRecordsReturned(long n)",
		"public static void addBytes(long n)",
		"public static Call begin(Emitter emitter, AccountingMethod method, String requestId)",
		"public void authorized(Map<String, String> callerIdentity)",
		"public void finish(Throwable err)",
		// The durable sink's flattened record and its health counters.
		".setOutcome(outcome)\n                    .setReason(reason)\n                    .setRecordsReturned(recordsReturned)\n                    .setBytes(bytes)",
		"public Stats stats()",
		"ingested.incrementAndGet();",
	} {
		if !strings.Contains(am, w) {
			t.Fatalf("Java am missing %q:\n%s", w, am)
		}
	}
	for _, gone := range []string{
		"rpcError", "RpcError", "tokenRejected", "TokenRejected", "ErrorContext",
		"Diagnostic", "diagnostic(", "captureErrorContext", "shouldCaptureStack",
		"rpcCompleted(",
	} {
		if strings.Contains(am, gone) {
			t.Fatalf("Java am still carries the removed %q:\n%s", gone, am)
		}
	}
}

func javaAccountedUnaryForTest(t *testing.T, authDisabled, isC2S bool) string {
	t.Helper()
	return generateJavaGRPCWrapperSurface(
		&parser.MethodInfo{
			Name:           "Read",
			InputType:      "ContractRequest",
			OutputType:     "ContractResponse",
			FullInputType:  "contractclient.ContractRequest",
			FullOutputType: "contractclient.ContractResponse",
			AuthDisabled:   authDisabled,
		},
		"ContractApi", "contract",
		"Contractclient.ContractRequest", "Contractclient.ContractResponse",
		"Helpers", "Helpers", false, authDisabled,
		map[string]service.AccountingInfo{
			"contract": {EnumName: "Contract", RPCs: map[string]bool{"Read": true}},
		},
		"contractclient", "contract",
		map[string]string{}, proto.SkippedTraceTypes{}, nil, nil, nil,
		isC2S, false, false,
	)
}

// A call refused before its handler (here: the method's auth check) is
// accounted: the override begins the call before the body runs and hands the
// body the accounted observer, so the refusal's onError is the call's record,
// with the stage still AUTH and no identity.
func TestJavaAccountedRpcRecordsPreHandlerDenial(t *testing.T) {
	rendered := javaAccountedUnaryForTest(t, false, false)

	begin := strings.Index(rendered, "AccountingManager.Call __acct = AccountingManager.Call.begin(this.emitter,")
	body := strings.Index(rendered, "read__accounted(request, accountedResponseObserver(responseObserver, __acct), __acct);")
	decl := strings.Index(rendered, "private void read__accounted(Contractclient.ContractRequest request, StreamObserver<Contractclient.ContractResponse> responseObserver, AccountingManager.Call __acct) {")
	check := strings.Index(rendered, `callerIdentity = checkAuth("ContractApi/Read", request);`)
	refused := strings.Index(rendered[check:], "responseObserver.onError(toGrpcError(e));")
	if refused >= 0 {
		refused += check
	}
	authorized := strings.Index(rendered, "__acct.authorized(callerIdentity);")
	validate := strings.Index(rendered, "Helpers.validateContractRequest(request);")
	handlerStage := strings.Index(rendered, "__acct.stage(AccountingManager.Stage.HANDLER);")
	for name, i := range map[string]int{"begin": begin, "body": body, "decl": decl, "check": check, "refused": refused, "authorized": authorized, "validate": validate, "handlerStage": handlerStage} {
		if i < 0 {
			t.Fatalf("accounted wrapper missing %s:\n%s", name, rendered)
		}
	}
	if !(begin < body && body < decl && decl < check && check < refused && refused < authorized && authorized < validate && validate < handlerStage) {
		t.Fatalf("accounted wrapper must begin the call, then refuse at AUTH, then attribute, validate at REQUEST, then call the handler:\n%s", rendered)
	}

	// The observer the refusal goes through ends the call: onError records it.
	tmpl := readJavaTemplate(t, "java/types/servicewrapper.java.tmpl")
	for _, w := range []string{
		"@Override public void onError(Throwable t) { acct.finish(t); sco.onError(t); }",
		"@Override public void onCompleted() { acct.finish(null); sco.onCompleted(); }",
		"@Override public void onError(Throwable t) { acct.finish(t); delegate.onError(t); }",
	} {
		if !strings.Contains(tmpl, w) {
			t.Fatalf("servicewrapper template's accountedResponseObserver missing %q", w)
		}
	}
	// A call ends once: the first finish wins.
	if am := renderJavaAmForTest(t); !strings.Contains(am, "if (!done.compareAndSet(false, true) || suppressed || emitter == null) {") {
		t.Fatalf("Call.finish must emit only on the call's first end:\n%s", am)
	}
}

// The call is bound into the Context around the body (and so the handler), and
// around each request-stream callback, which is what Metrics reach.
func TestJavaAccountedRpcInstallsMetricsAroundHandler(t *testing.T) {
	rendered := javaAccountedUnaryForTest(t, false, false)
	bind := strings.Index(rendered, "io.grpc.Context __acctPrev = __acct.bind();")
	body := strings.Index(rendered, "read__accounted(request,")
	unbind := strings.Index(rendered, "} finally {\n            __acct.unbind(__acctPrev);")
	if bind < 0 || body < bind || unbind < body {
		t.Fatalf("accounted override must bind the call around the body:\n%s", rendered)
	}

	tmpl := readJavaTemplate(t, "java/types/servicewrapper.java.tmpl")
	reqObs := tmpl[strings.Index(tmpl, "private static <T> StreamObserver<T> accountedRequestObserver("):]
	if !strings.Contains(reqObs, "io.grpc.Context prev = acct.bind();\n                try {\n                    delegate.onNext(value);") {
		t.Fatalf("accountedRequestObserver must bind the call around the handler's onNext:\n%s", reqObs)
	}

	am := renderJavaAmForTest(t)
	for _, w := range []string{
		"return Context.current().withValue(CALL_CTX, this).attach();",
		"Call call = CALL_CTX.get();\n            if (call != null) {\n                call.recordsReturned.set(n);",
		"Call call = CALL_CTX.get();\n            if (call != null) {\n                call.bytes.addAndGet(n);",
		".setRecordsReturned(recordsReturned.get())",
		".setBytes(bytes.get())",
	} {
		if !strings.Contains(am, w) {
			t.Fatalf("Java am metrics missing %q:\n%s", w, am)
		}
	}
}

// The accounting_internal marker suppresses the record on any accounted
// wrapper, but only for a call whose verified caller is a service principal
// (sub_type "service"): a user or workload token with the header is recorded.
func TestJavaAccountingInternalMarkerHonoredOnlyForServiceCallers(t *testing.T) {
	for _, tc := range []struct {
		name         string
		authDisabled bool
		isC2S        bool
	}{
		{"s2s", false, false},
		{"c2s", false, true},
		{"auth_disabled or c2d", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered := javaAccountedUnaryForTest(t, tc.authDisabled, tc.isC2S)
			// No per-surface switch: the decision is the call's, made from its
			// verified token when the caller is attributed.
			if !strings.Contains(rendered, "Auth.requestIdFromContext());") {
				t.Fatalf("accounted override must begin the call with no surface switch:\n%s", rendered)
			}
			if !strings.Contains(rendered, "__acct.authorized(callerIdentity);") {
				t.Fatalf("accounted body must attribute the call:\n%s", rendered)
			}
		})
	}

	am := renderJavaAmForTest(t)
	if !strings.Contains(am, `TokenWrapper verified = Auth.verifiedTokenFromContext();
            if (verified != null && "service".equals(verified.getSubType()) && internalFromContext()) {
                suppressed = true;`) {
		t.Fatalf("Call.authorized must suppress only a service caller's marked call:\n%s", am)
	}
	authz := readJavaTemplate(t, "java/chassis/grpcserver/Authorizer.java.tmpl")
	if !strings.Contains(authz, "Context ctx = AccountingManager.captureInternal(Approval.capture(") {
		t.Fatal("headerCaptureInterceptor must copy the accounting_internal marker onto the Context")
	}
}
