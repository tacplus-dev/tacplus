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

func TestGoUnaryAccountingTreatsResponseValidationAsError(t *testing.T) {
	rendered := generateGRPCWrapper(
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
		"contract",
		map[string]service.AccountingInfo{
			"contract": {EnumName: "Contract", RPCs: map[string]bool{"PublicRead": true}},
		},
		map[string]string{},
		proto.SkippedTraceTypes{},
		nil,
	)

	// One deferred record reads the error the call returns; a response that
	// fails validation is returned as that error, at the RESPONSE stage.
	record := strings.Index(rendered, "p.emitter.Rpc(ctx, accountingMethod, __stage, __err,")
	response := strings.Index(rendered, "__stage = am.StageResponse")
	validation := strings.Index(rendered, "if validationErr := types.ValidateContractResponse(resp); validationErr != nil")
	if record < 0 || response < 0 || validation < 0 {
		t.Fatalf("rendered Go wrapper missing the record, the response stage or validation:\n%s", rendered)
	}
	if !(record < response && response < validation) {
		t.Fatalf("Go response validation must run in the RESPONSE stage, under the deferred record:\n%s", rendered)
	}
	if !strings.Contains(rendered, "(__resp *types.ContractResponse, __err error)") {
		t.Fatalf("Go unary accounting must name its results so the deferred record sees the error:\n%s", rendered)
	}
}

func TestGoUnaryTraceTreatsHandlerAndValidationErrorsAsResponseTrace(t *testing.T) {
	rendered := generateGRPCWrapper(
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
		"contract",
		map[string]service.AccountingInfo{
			"contract": {EnumName: "Contract", RPCs: map[string]bool{"PublicRead": true}},
		},
		map[string]string{},
		proto.SkippedTraceTypes{},
		nil,
	)

	for _, want := range []string{
		"Error:   err.Error(),",
		"if validationErr := types.ValidateContractResponse(resp); validationErr != nil",
		"err := status.Error(codes.Internal, fmt.Sprintf(\"response validation failed: %v\", validationErr))",
		"p.publishTrace(ctx, &trace_pb.TraceEvent{",
		"Kind: trace_pb.TraceEventKind_TRACE_EVENT_KIND_INTERNAL_RESPONSE,",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Go unary trace wrapper missing %q:\n%s", want, rendered)
		}
	}
}

func TestGoUnaryTraceNoopDoesNotSkipRequestValidation(t *testing.T) {
	for _, tc := range []struct {
		name       string
		svc        *parser.ServiceInfo
		method     *parser.MethodInfo
		noopNeedle string
	}{
		{
			name: "protected",
			svc:  &parser.ServiceInfo{Name: "ContractApi"},
			method: &parser.MethodInfo{
				Name:           "Read",
				InputType:      "ContractRequest",
				OutputType:     "ContractResponse",
				FullInputType:  "contractclient.ContractRequest",
				FullOutputType: "contractclient.ContractResponse",
			},
			noopNeedle: "if isNoop",
		},
		{
			name: "auth_disabled",
			svc:  &parser.ServiceInfo{Name: "ContractApi", AuthDisabled: true},
			method: &parser.MethodInfo{
				Name:           "PublicRead",
				InputType:      "ContractRequest",
				OutputType:     "ContractResponse",
				FullInputType:  "contractclient.ContractRequest",
				FullOutputType: "contractclient.ContractResponse",
				AuthDisabled:   true,
			},
			noopNeedle: "if req.Noop",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered := generateGRPCWrapper(
				tc.svc,
				tc.method,
				"contractclient",
				"contract",
				"contract",
				map[string]service.AccountingInfo{},
				map[string]string{},
				proto.SkippedTraceTypes{},
				nil,
			)

			validation := strings.Index(rendered, "types.ValidateContractRequest(req)")
			noop := strings.Index(rendered, tc.noopNeedle)
			if validation < 0 || noop < 0 {
				t.Fatalf("Go trace wrapper missing validation or noop branch:\n%s", rendered)
			}
			if validation > noop {
				t.Fatalf("Go trace noop must not bypass request validation:\n%s", rendered)
			}
		})
	}
}

func TestGoWatchAccountingRecordsStreamErrors(t *testing.T) {
	method := watchMethod()
	method.Name = "Watch"
	rendered := generateGRPCWrapper(
		&parser.ServiceInfo{Name: "Catalog", AuthDisabled: true, Methods: []*parser.MethodInfo{method}},
		method,
		"catalog",
		"services",
		"catalog",
		map[string]service.AccountingInfo{
			"catalog": {EnumName: "Catalog", RPCs: map[string]bool{"Watch": true}},
		},
		map[string]string{},
		proto.SkippedTraceTypes{},
		nil,
	)

	for _, want := range []string{
		"(__err error)",
		"recordErr := func(err error) error",
		"return recordErr(serr)",
		"return recordErr(cerr)",
		"p.emitter.Rpc(stream.Context(), accountingMethod, __stage, __err,",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Go WATCH accounting wrapper missing %q:\n%s", want, rendered)
		}
	}
}

func TestGoStreamingAccountingCoversMethodKinds(t *testing.T) {
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
		rendered := generateGRPCWrapper(
			svc,
			tc.method,
			"contractclient",
			"contract",
			"contract",
			map[string]service.AccountingInfo{
				"contract": {EnumName: "Contract", RPCs: map[string]bool{tc.method.Name: true}},
			},
			map[string]string{},
			proto.SkippedTraceTypes{},
			nil,
		)

		for _, want := range []string{
			"(__err error)",
			"__start := time.Now()",
			"__stage = am.StageHandler",
			"p.emitter.Rpc(stream.Context(), accountingMethod, __stage, __err, time.Since(__start).Milliseconds(), nil,",
		} {
			if !strings.Contains(rendered, want) {
				t.Fatalf("Go %s accounting wrapper missing %q:\n%s", tc.name, want, rendered)
			}
		}
	}
}

func accountedContractRead(svc *parser.ServiceInfo) string {
	return generateGRPCWrapper(
		svc,
		&parser.MethodInfo{
			Name:           "Read",
			InputType:      "ContractRequest",
			OutputType:     "ContractResponse",
			FullInputType:  "contractclient.ContractRequest",
			FullOutputType: "contractclient.ContractResponse",
		},
		"contractclient",
		"contract",
		"contract",
		map[string]service.AccountingInfo{
			"contract": {EnumName: "Contract", RPCs: map[string]bool{"Read": true}},
		},
		map[string]string{},
		proto.SkippedTraceTypes{},
		nil,
	)
}

// tacplus#273: a call refused before its handler is recorded too. The record
// is deferred ahead of the auth check, the call path advances the stage the
// reason is classified from, and the handler's metrics ride the context.
func TestGoAccountingRecordsEveryExitPath(t *testing.T) {
	rendered := accountedContractRead(&parser.ServiceInfo{Name: "ContractApi", IsS2S: true})
	order := []string{
		"__stage := am.StageAuth",
		"__mctx, __metrics := am.WithMetrics(ctx)",
		"defer func() {",
		"p.emitter.Rpc(ctx, accountingMethod, __stage, __err, time.Since(__start).Milliseconds(), am.MetricsOrNil(__metrics), identityMap,",
		"authTok, err := p.authorize(ctx,",
		"__caller = caller",
		"__stage = am.StageRequest",
		"__stage = am.StageHandler",
		"resp, err := p.handler.Read(sanitizeContext(ctx), caller, req)",
		"__stage = am.StageResponse",
	}
	at := 0
	for _, want := range order {
		i := strings.Index(rendered[at:], want)
		if i < 0 {
			t.Fatalf("Go accounted wrapper missing %q after offset %d:\n%s", want, at, rendered)
		}
		at += i + len(want)
	}
	for _, gone := range []string{"RpcError", "RpcCompleted", "ErrorContext", "err.Error(), durationMs"} {
		if strings.Contains(rendered, gone) {
			t.Fatalf("Go accounted wrapper still emits the removed %q:\n%s", gone, rendered)
		}
	}
}

// The accounting_internal marker is honored only on a call whose verified
// caller is a service: a user cannot switch its own accounting off.
func TestGoAccountingInternalMarkerOnlyForServiceCallers(t *testing.T) {
	marker := `if __caller != nil && __caller.Token().GetSubType() == "service" && am.IsAccountingInternal(ctx) {`
	r := accountedContractRead(&parser.ServiceInfo{Name: "ContractApi", IsC2S: true})
	if strings.Count(r, "IsAccountingInternal") != 1 || !strings.Contains(r, marker) {
		t.Fatalf("the marker must be honored only for a verified service caller:\n%s", r)
	}
}

// A PAGE method is accounted like any other unary call (tacplus#273): it had
// no record at all before.
func TestGoPageAccountingRecordsEveryExitPath(t *testing.T) {
	method := pageMethod()
	rendered := generateGRPCWrapper(
		&parser.ServiceInfo{Name: "Catalog", Methods: []*parser.MethodInfo{method}},
		method,
		"catalog",
		"services",
		"catalog",
		map[string]service.AccountingInfo{
			"catalog": {EnumName: "Catalog", RPCs: map[string]bool{method.Name: true}},
		},
		map[string]string{},
		proto.SkippedTraceTypes{},
		nil,
	)
	order := []string{
		"__err error)",
		"__stage := am.StageAuth",
		"p.emitter.Rpc(ctx, accountingMethod, __stage, __err,",
		"__stage = am.StageRequest",
		"__stage = am.StageHandler",
		"page, err := ",
		"__stage = am.StageResponse",
	}
	at := 0
	for _, want := range order {
		i := strings.Index(rendered[at:], want)
		if i < 0 {
			t.Fatalf("Go PAGE wrapper missing %q after offset %d:\n%s", want, at, rendered)
		}
		at += i + len(want)
	}
}
