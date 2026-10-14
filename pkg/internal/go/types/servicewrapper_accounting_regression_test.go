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

	validation := strings.Index(rendered, "if validationErr := types.ValidateContractResponse(resp); validationErr != nil")
	completed := strings.Index(rendered, "p.emitter.RpcCompleted")
	if validation < 0 || completed < 0 {
		t.Fatalf("rendered Go wrapper missing validation or completed accounting:\n%s", rendered)
	}
	if validation > completed {
		t.Fatalf("Go response validation must run before rpcCompleted:\n%s", rendered)
	}
	if !strings.Contains(rendered[validation:completed], "p.emitter.RpcError") {
		t.Fatalf("Go response-validation failure does not emit rpcError before completion:\n%s", rendered)
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
		"var streamErr error",
		"recordErr := func(err error) error",
		"return recordErr(serr)",
		"return recordErr(cerr)",
		"p.emitter.RpcError(stream.Context(), accountingMethod",
		"p.emitter.RpcCompleted(stream.Context(), accountingMethod",
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
			"start := time.Now()",
			"durationMs := time.Since(start).Milliseconds()",
			"p.emitter.RpcError(stream.Context(), accountingMethod",
			"p.emitter.RpcCompleted(stream.Context(), accountingMethod",
		} {
			if !strings.Contains(rendered, want) {
				t.Fatalf("Go %s accounting wrapper missing %q:\n%s", tc.name, want, rendered)
			}
		}
	}
}
