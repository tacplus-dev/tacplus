// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
)

// tacplus#273: every accounted RPC emits exactly one RpcCompleted per call on
// every exit path. The Rust wrapper starts an am::Call before authentication,
// runs the whole method body as one async block, and finishes the call once on
// the block's result, so a `?` anywhere in the body — the auth check, request
// validation, a pin, the handler, a response guard — lands on that one finish.

func contractMethod(name string) *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:           name,
		InputType:      "ContractRequest",
		OutputType:     "ContractResponse",
		FullInputType:  "contractclient.ContractRequest",
		FullOutputType: "contractclient.ContractResponse",
	}
}

func renderAccountedContract(t *testing.T, svc *parser.ServiceInfo, method *parser.MethodInfo) string {
	t.Helper()
	return mustGenerateRustGRPCWrapper(
		t, svc, method,
		"contractclient", "contract", map[string]string{}, "contract",
		map[string]service.AccountingInfo{
			"contract": {EnumName: "Contract", RPCs: map[string]bool{method.Name: true}},
		},
		proto.SkippedTraceTypes{}, nil,
	)
}

// inOrder fails unless every want appears in rendered, in the given order.
func inOrder(t *testing.T, rendered string, wants ...string) {
	t.Helper()
	at := 0
	for _, want := range wants {
		i := strings.Index(rendered[at:], want)
		if i < 0 {
			t.Fatalf("rendered wrapper missing %q after offset %d (or out of order):\n%s", want, at, rendered)
		}
		at += i + len(want)
	}
}

// A call refused by its auth check is accounted: the Call starts before the
// authorizer runs, the authorizer's `?` is inside the accounted block, and the
// one finish follows the block. Nothing before Call::start can return.
func TestRustAccountingDeniedCallEmitsOneRecord(t *testing.T) {
	rendered := renderAccountedContract(t, &parser.ServiceInfo{Name: "ContractApi"}, contractMethod("Read"))

	inOrder(t, rendered,
		"let __tacplus_call = am::Call::start(",
		"self.emitter.read().await.clone(),",
		"contract: services_pb::ContractRpCs::ContractRpcsRead as i32,",
		"let __tacplus_result: Result<tonic::Response<ContractResponse>, tonic::Status> = async {",
		`let __tacplus_caller_identity = self.authorize_method("/contract/contractclient.ContractApi/Read", &request).await?;`,
		"__tacplus_call.authenticated(&__tacplus_caller_identity, request.metadata()",
		"__tacplus_call.stage(am::Stage::Request);",
		"req.validate()",
		"__tacplus_call.handler(self.handler.read(caller.as_ref(), req)).await?",
		"resp.validate()",
		"}.await;",
		"__tacplus_call.finish(__tacplus_result.as_ref().err()).await;",
		"__tacplus_result\n    }",
	)
	start := strings.Index(rendered, "am::Call::start(")
	if q := strings.Index(rendered, "?;"); q >= 0 && q < start {
		t.Fatalf("a `?` before am::Call::start returns without accounting:\n%s", rendered)
	}
	if n := strings.Count(rendered, "__tacplus_call.finish("); n != 1 {
		t.Fatalf("unary wrapper must finish its call exactly once, found %d:\n%s", n, rendered)
	}
	for _, gone := range []string{"rpc_error", "rpc_completed", "error_context_from_status", "emitter_guard"} {
		if strings.Contains(rendered, gone) {
			t.Fatalf("wrapper still uses the removed accounting API %q:\n%s", gone, rendered)
		}
	}
}

// The identity on a record is the authorizer-verified claims, handed to the
// Call as soon as they are verified; an auth_disabled method verifies nothing
// and accounts with no identity.
func TestRustAccountingCarriesVerifiedCallerIdentity(t *testing.T) {
	authorized := renderAccountedContract(t, &parser.ServiceInfo{Name: "ContractApi"}, contractMethod("Read"))
	inOrder(t, authorized,
		`let __tacplus_caller_identity = self.authorize_method("/contract/contractclient.ContractApi/Read", &request).await?;`,
		"__tacplus_call.authenticated(&__tacplus_caller_identity, request.metadata()",
	)

	method := contractMethod("PublicRead")
	method.AuthDisabled = true
	noAuth := renderAccountedContract(t, &parser.ServiceInfo{Name: "ContractApi", AuthDisabled: true}, method)
	if !strings.Contains(noAuth, "let __tacplus_caller_identity: std::collections::HashMap<String, String> = std::collections::HashMap::new();") {
		t.Fatalf("auth_disabled RPC must account with an empty identity:\n%s", noAuth)
	}
	if strings.Contains(noAuth, "__tacplus_call.authenticated(") || strings.Contains(noAuth, "self.authorize_method(") {
		t.Fatalf("auth_disabled RPC verified nobody, so the Call must not be marked authenticated:\n%s", noAuth)
	}
	if !strings.Contains(noAuth, "__tacplus_call.stage(am::Stage::Request);") {
		t.Fatalf("auth_disabled RPC must still advance its Call past AUTH:\n%s", noAuth)
	}
}

// A target_namespace_field check is part of the auth stage: it runs after the
// identity is recorded and before the call moves to REQUEST, so its refusal is
// NOT_ENTITLED with the caller's identity.
func TestRustAccountingTargetNamespaceIsAuthStage(t *testing.T) {
	method := contractMethod("Read")
	method.TargetNamespaceField = "namespace"
	rendered := renderAccountedContract(t, &parser.ServiceInfo{Name: "ContractApi"}, method)
	inOrder(t, rendered,
		"__tacplus_call.authenticated(&__tacplus_caller_identity, request.metadata()",
		`self.enforce_at_or_below("/contract/contractclient.ContractApi/Read", &request, &request.get_ref().namespace).await?;`,
		"__tacplus_call.stage(am::Stage::Request);",
	)
}

// Every streaming shape is accounted once: a response stream when it ends
// (am::AccountedStream), a client stream on its single response.
func TestRustStreamingAccountingCoversMethodKinds(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "ContractApi", AuthDisabled: true}
	for _, tc := range []struct {
		name           string
		client, server bool
	}{
		{"server-streaming", false, true},
		{"client-streaming", true, false},
		{"bidi", true, true},
	} {
		method := contractMethod("Stream")
		method.ClientStreaming, method.ServerStreaming, method.AuthDisabled = tc.client, tc.server, true
		rendered := renderAccountedContract(t, svc, method)

		inOrder(t, rendered,
			"let __tacplus_call = am::Call::start(",
			"= async {",
			"__tacplus_call.stage(am::Stage::Request);",
			"__tacplus_call.handler(self.handler.stream(",
			"}.await;",
		)
		if tc.server {
			inOrder(t, rendered,
				"match __tacplus_result {",
				"Box::pin(am::AccountedStream::new(__tacplus_call, stream)) as Self::StreamStream",
				"__tacplus_call.finish(Some(&status)).await;",
			)
		} else {
			inOrder(t, rendered, "__tacplus_call.finish(__tacplus_result.as_ref().err()).await;")
			if strings.Contains(rendered, "AccountedStream") {
				t.Fatalf("%s: a unary response is not a stream:\n%s", tc.name, rendered)
			}
		}
	}
}

// WATCH: the handler runs through the Call and the producer's channel is the
// accounted stream, so the record is written when the watch ends, errors, or
// the client goes away — not when the handler returns its source.
func TestRustWatchAccountingCoversTheStream(t *testing.T) {
	rendered := mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "Catalog", AuthDisabled: true, Methods: []*parser.MethodInfo{rustWatchMethod()}},
		rustWatchMethod(),
		"catalog", "services", map[string]string{}, "catalog",
		map[string]service.AccountingInfo{
			"catalog": {EnumName: "Catalog", RPCs: map[string]bool{"Watch": true}},
		},
		proto.SkippedTraceTypes{}, nil,
	)
	inOrder(t, rendered,
		"let __tacplus_call = am::Call::start(",
		"__tacplus_call.handler(self.handler.watch(caller.as_ref(), req_msg)).await?",
		"tokio::spawn(async move",
		"tokio_stream::wrappers::ReceiverStream::new(rx)",
		"}.await;",
		"am::AccountedStream::new(__tacplus_call, stream)",
	)
	for _, gone := range []string{"macro_rules! __tacplus_watch", "rpc_error", "rpc_completed"} {
		if strings.Contains(rendered, gone) {
			t.Fatalf("WATCH wrapper still carries the removed accounting %q:\n%s", gone, rendered)
		}
	}
}

// PAGE was hosted with no accounting at all; it is accounted like any unary.
func TestRustPageAccounted(t *testing.T) {
	rendered := mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "Catalog"},
		rustPageMethod(),
		"catalog", "services", map[string]string{}, "catalog",
		map[string]service.AccountingInfo{
			"catalog": {EnumName: "Catalog", RPCs: map[string]bool{"ListEvents": true}},
		},
		proto.SkippedTraceTypes{}, nil,
	)
	inOrder(t, rendered,
		"let __tacplus_call = am::Call::start(",
		"__tacplus_call.stage(am::Stage::Request);",
		"collection::decode_page_token(&req.page_token)?",
		"__tacplus_call.handler(self.handler.list_events(caller.as_ref(), req, collection::PageParams { limit, after })).await?",
		"__tacplus_call.finish(__tacplus_result.as_ref().err()).await;",
	)
}

// The accounting_internal marker is noted by every accounted wrapper, C2S,
// S2S or C2D alike, but honored only for a verified SERVICE caller: the Call
// learns whether the verified token's sub_type is exactly "service" when auth
// passes. A user token carrying the header is still recorded, and an
// auth_disabled call verified nobody, so it can never be a service caller.
func TestRustAccountingInternalMarkerOnlyForServiceCaller(t *testing.T) {
	for name, svc := range map[string]*parser.ServiceInfo{
		"s2s": {Name: "ContractApi"},
		"c2s": {Name: "ContractApi", IsC2S: true},
		"c2d": {Name: "ContractApi", IsC2D: true},
	} {
		rendered := renderAccountedContract(t, svc, contractMethod("Read"))
		inOrder(t, rendered,
			"&servicemanager::auth::extract_request_id(&request),\n            am::accounting_internal(request.metadata()),\n        );",
		)
		if name == "c2d" {
			// c2d verifies no token, so no caller can be a service.
			if strings.Contains(rendered, "__tacplus_call.authenticated(") {
				t.Fatalf("c2d wrapper verified nobody and must not mark a caller:\n%s", rendered)
			}
			continue
		}
		inOrder(t, rendered,
			`self.authorize_method("/contract/contractclient.ContractApi/Read", &request).await?;`,
			"__tacplus_call.authenticated(&__tacplus_caller_identity, "+rustServiceCallerExpr+");",
		)
	}
	if !strings.Contains(rustServiceCallerExpr, `t.sub_type() == Some("service")`) {
		t.Fatalf("only a token whose sub_type is exactly \"service\" is a service caller: %s", rustServiceCallerExpr)
	}
}

// A method outside the accounting map renders no accounting at all.
func TestRustUnaccountedMethodHasNoCall(t *testing.T) {
	rendered := mustGenerateRustGRPCWrapper(
		t, &parser.ServiceInfo{Name: "ContractApi"}, contractMethod("Read"),
		"contractclient", "contract", map[string]string{}, "contract",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, nil,
	)
	if strings.Contains(rendered, "__tacplus_call.") || strings.Contains(rendered, "__tacplus_call =") {
		t.Fatalf("unaccounted method must not start an accounting Call:\n%s", rendered)
	}
	if !strings.Contains(rendered, "self.handler.read(caller.as_ref(), req).await?") {
		t.Fatalf("unaccounted method must call its handler directly:\n%s", rendered)
	}
}

func TestRustUnaryTraceTreatsHandlerAndValidationErrorsAsResponseTrace(t *testing.T) {
	method := contractMethod("PublicRead")
	method.AuthDisabled = true
	rendered := renderAccountedContract(t, &parser.ServiceInfo{Name: "ContractApi", AuthDisabled: true}, method)

	for _, want := range []string{
		"if let Err(ref err) = result",
		"error: err.to_string(),",
		"resp.validate()",
		"kind: crate::types::sys::trace::TraceEventKind::InternalResponse.into(),",
		"let resp = result?;",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Rust unary trace wrapper missing %q:\n%s", want, rendered)
		}
	}
}
