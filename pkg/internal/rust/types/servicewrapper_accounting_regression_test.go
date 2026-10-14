// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
)

func TestRustUnaryAccountingTreatsResponseValidationAsError(t *testing.T) {
	rendered := mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "ContractApi", AuthDisabled: true},
		&parser.MethodInfo{
			Name:           "PublicRead",
			InputType:      "ContractRequest",
			OutputType:     "ContractResponse",
			FullInputType:  "contractclient.ContractRequest",
			FullOutputType: "contractclient.ContractResponse",
			AuthDisabled:   true,
		},
		"contractclient",
		"contract",
		map[string]string{},
		"contract",
		map[string]service.AccountingInfo{
			"contract": {EnumName: "Contract", RPCs: map[string]bool{"PublicRead": true}},
		},
		proto.SkippedTraceTypes{},
		nil,
	)

	validation := strings.Index(rendered, "resp.validate()")
	matchResult := strings.Index(rendered, "match &result")
	completed := strings.Index(rendered, "emitter.rpc_completed")
	errored := strings.Index(rendered, "emitter.rpc_error")
	duration := strings.Index(rendered, "let duration_ms = start.elapsed()")
	if validation < 0 || matchResult < 0 || completed < 0 || errored < 0 || duration < 0 {
		t.Fatalf("rendered Rust wrapper missing validation, duration, or completed accounting:\n%s", rendered)
	}
	if validation > duration || duration > matchResult || matchResult > completed {
		t.Fatalf("Rust response validation must run before duration/accounting:\n%s", rendered)
	}
	if matchResult > errored {
		t.Fatalf("Rust accounting match does not include rpc_error:\n%s", rendered)
	}
}

func TestRustUnaryTraceTreatsHandlerAndValidationErrorsAsResponseTrace(t *testing.T) {
	rendered := mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "ContractApi", AuthDisabled: true},
		&parser.MethodInfo{
			Name:           "PublicRead",
			InputType:      "ContractRequest",
			OutputType:     "ContractResponse",
			FullInputType:  "contractclient.ContractRequest",
			FullOutputType: "contractclient.ContractResponse",
			AuthDisabled:   true,
		},
		"contractclient",
		"contract",
		map[string]string{},
		"contract",
		map[string]service.AccountingInfo{
			"contract": {EnumName: "Contract", RPCs: map[string]bool{"PublicRead": true}},
		},
		proto.SkippedTraceTypes{},
		nil,
	)

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

func TestRustStreamingAccountingCoversMethodKinds(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "ContractApi", AuthDisabled: true}
	for _, tc := range []struct {
		name   string
		method *parser.MethodInfo
	}{
		{
			name: "server-streaming",
			method: &parser.MethodInfo{
				Name:            "Stream",
				InputType:       "ContractRequest",
				OutputType:      "ContractResponse",
				FullInputType:   "contractclient.ContractRequest",
				FullOutputType:  "contractclient.ContractResponse",
				ServerStreaming: true,
				AuthDisabled:    true,
			},
		},
		{
			name: "client-streaming",
			method: &parser.MethodInfo{
				Name:            "Upload",
				InputType:       "ContractRequest",
				OutputType:      "ContractResponse",
				FullInputType:   "contractclient.ContractRequest",
				FullOutputType:  "contractclient.ContractResponse",
				ClientStreaming: true,
				AuthDisabled:    true,
			},
		},
		{
			name: "bidi",
			method: &parser.MethodInfo{
				Name:            "Chat",
				InputType:       "ContractRequest",
				OutputType:      "ContractResponse",
				FullInputType:   "contractclient.ContractRequest",
				FullOutputType:  "contractclient.ContractResponse",
				ClientStreaming: true,
				ServerStreaming: true,
				AuthDisabled:    true,
			},
		},
	} {
		rendered := mustGenerateRustGRPCWrapper(
			t,
			svc,
			tc.method,
			"contractclient",
			"contract",
			map[string]string{},
			"contract",
			map[string]service.AccountingInfo{
				"contract": {EnumName: "Contract", RPCs: map[string]bool{tc.method.Name: true}},
			},
			proto.SkippedTraceTypes{},
			nil,
		)

		for _, want := range []string{
			"let start = std::time::Instant::now();",
			"let result = async {",
			"let duration_ms = start.elapsed().as_millis() as i64;",
			"emitter.rpc_completed(method",
			"emitter.rpc_error(method",
		} {
			if !strings.Contains(rendered, want) {
				t.Fatalf("Rust %s accounting wrapper missing %q:\n%s", tc.name, want, rendered)
			}
		}
	}
}

func TestRustWatchAccountingRunsInProducerTask(t *testing.T) {
	rendered := mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "Catalog", AuthDisabled: true, Methods: []*parser.MethodInfo{rustWatchMethod()}},
		rustWatchMethod(),
		"catalog",
		"services",
		map[string]string{},
		"catalog",
		map[string]service.AccountingInfo{
			"catalog": {EnumName: "Catalog", RPCs: map[string]bool{"Watch": true}},
		},
		proto.SkippedTraceTypes{},
		nil,
	)

	for _, want := range []string{
		"macro_rules! __tacplus_watch_rpc_error",
		"macro_rules! __tacplus_watch_rpc_completed",
		"match self.handler.watch",
		"tokio::spawn(async move",
		"__tacplus_watch_rpc_error!(err);",
		"tonic::Status::cancelled(\"client closed Watch stream\")",
		"__tacplus_watch_rpc_completed!();",
		"emitter.rpc_completed(",
		"emitter.rpc_error(",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Rust WATCH accounting wrapper missing %q:\n%s", want, rendered)
		}
	}
}

// F10 (Rust port): an accounting record has to name the principal the
// Authorizer verified. The wrapper used to hand every emit a fresh empty map,
// so the only thing tying a record to a caller was the caller-written
// x-request-id header. Java fixed this first — see
// pkg/internal/java/types/servicewrapper_accounting_regression_test.go:437.
func TestRustAccountingCarriesVerifiedCallerIdentity(t *testing.T) {
	accounting := map[string]service.AccountingInfo{
		"contract": {EnumName: "Contract", RPCs: map[string]bool{"Read": true, "PublicRead": true}},
	}

	authorized := mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "ContractApi"},
		&parser.MethodInfo{
			Name:           "Read",
			InputType:      "ContractRequest",
			OutputType:     "ContractResponse",
			FullInputType:  "contractclient.ContractRequest",
			FullOutputType: "contractclient.ContractResponse",
		},
		"contractclient", "contract", map[string]string{}, "contract",
		accounting, proto.SkippedTraceTypes{}, nil,
	)

	if strings.Contains(authorized, "std::collections::HashMap::new(), &request_id") {
		t.Fatalf("accounting must not emit an empty identity map on an authorized RPC:\n%s", authorized)
	}
	if !strings.Contains(authorized, `let __tacplus_caller_identity = self.authorize_method("/contract/contractclient.ContractApi/Read", &request).await?;`) {
		t.Fatalf("authorized RPC must source its identity from authorize_method:\n%s", authorized)
	}
	for _, want := range []string{
		"emitter.rpc_completed(method, duration_ms, __tacplus_caller_identity.clone(), &request_id).await;",
		"Some(err_ctx), __tacplus_caller_identity.clone(), &request_id).await;",
	} {
		if !strings.Contains(authorized, want) {
			t.Fatalf("authorized RPC accounting missing %q:\n%s", want, authorized)
		}
	}

	// auth_disabled: nothing verifies a caller, so the record must carry no
	// identity at all rather than something the caller asserted.
	noAuth := mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "ContractApi", AuthDisabled: true},
		&parser.MethodInfo{
			Name:           "PublicRead",
			InputType:      "ContractRequest",
			OutputType:     "ContractResponse",
			FullInputType:  "contractclient.ContractRequest",
			FullOutputType: "contractclient.ContractResponse",
			AuthDisabled:   true,
		},
		"contractclient", "contract", map[string]string{}, "contract",
		accounting, proto.SkippedTraceTypes{}, nil,
	)

	if !strings.Contains(noAuth, "let __tacplus_caller_identity: std::collections::HashMap<String, String> = std::collections::HashMap::new();") {
		t.Fatalf("auth_disabled RPC must account with an empty identity:\n%s", noAuth)
	}
	if strings.Contains(noAuth, "self.authorize_method(") {
		t.Fatalf("auth_disabled RPC must not call authorize_method:\n%s", noAuth)
	}
}
