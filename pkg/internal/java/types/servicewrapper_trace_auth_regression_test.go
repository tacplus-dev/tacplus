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

// TCG-001 (Java): a trace request with noop=true must NOT bypass token
// verification — noop skips the handler, never auth. The generated wrapper
// has to call checkAuth/checkAuthenticate unconditionally, before any isNoop
// branch (matching the Go emission order in pkg/internal/go/types).

func javaTraceAuthMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:           "Read",
		InputType:      "ContractRequest",
		OutputType:     "ContractResponse",
		FullInputType:  "contractclient.ContractRequest",
		FullOutputType: "contractclient.ContractResponse",
	}
}

func TestJavaUnaryTraceNoopDoesNotSkipAuthorize(t *testing.T) {
	rendered := generateJavaGRPCWrapper(
		javaTraceAuthMethod(),
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

	if strings.Contains(rendered, "if (!isNoop)") {
		t.Fatalf("auth must not be gated on isNoop — noop skips the handler but not auth:\n%s", rendered)
	}
	authIdx := strings.Index(rendered, `checkAuth("ContractApi/Read", request);`)
	noopIdx := strings.Index(rendered, "if (isNoop)")
	if authIdx < 0 {
		t.Fatalf("trace-aware wrapper missing checkAuth call:\n%s", rendered)
	}
	if noopIdx < 0 {
		t.Fatalf("trace-aware wrapper missing isNoop early-return branch:\n%s", rendered)
	}
	if authIdx > noopIdx {
		t.Fatalf("checkAuth must run before the isNoop early-return:\n%s", rendered)
	}
}

func TestJavaUnaryTraceNoopDoesNotSkipAuthenticate(t *testing.T) {
	rendered := generateJavaGRPCWrapper(
		javaTraceAuthMethod(),
		"ContractApi",
		"contract",
		"Contractclient.ContractRequest",
		"Contractclient.ContractResponse",
		"Helpers",
		"Helpers",
		true,
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

	if strings.Contains(rendered, "if (!isNoop)") {
		t.Fatalf("auth must not be gated on isNoop — noop skips the handler but not auth:\n%s", rendered)
	}
	authIdx := strings.Index(rendered, "checkAuthenticate();")
	noopIdx := strings.Index(rendered, "if (isNoop)")
	if authIdx < 0 {
		t.Fatalf("trace-aware auth_any_user wrapper missing checkAuthenticate call:\n%s", rendered)
	}
	if noopIdx < 0 {
		t.Fatalf("trace-aware auth_any_user wrapper missing isNoop early-return branch:\n%s", rendered)
	}
	if authIdx > noopIdx {
		t.Fatalf("checkAuthenticate must run before the isNoop early-return:\n%s", rendered)
	}
}

func TestJavaUnaryTraceNoopDoesNotSkipRequestValidation(t *testing.T) {
	for _, tc := range []struct {
		name         string
		authAnyUser  bool
		authDisabled bool
	}{
		{name: "protected"},
		{name: "auth_any_user", authAnyUser: true},
		{name: "auth_disabled", authDisabled: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered := generateJavaGRPCWrapper(
				javaTraceAuthMethod(),
				"ContractApi",
				"contract",
				"Contractclient.ContractRequest",
				"Contractclient.ContractResponse",
				"Helpers",
				"Helpers",
				tc.authAnyUser,
				tc.authDisabled,
				map[string]service.AccountingInfo{},
				"contractclient",
				"contract",
				map[string]string{},
				proto.SkippedTraceTypes{},
				nil,
				nil,
				nil,
			)

			validation := strings.Index(rendered, "Helpers.validateContractRequest(request);")
			noop := strings.Index(rendered, "if (isNoop)")
			if validation < 0 || noop < 0 {
				t.Fatalf("Java trace wrapper missing validation or noop branch:\n%s", rendered)
			}
			if validation > noop {
				t.Fatalf("Java trace noop must not bypass request validation:\n%s", rendered)
			}
		})
	}
}
