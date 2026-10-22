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

// TestGoRpcDebugLogDoesNotDumpClaims pins that the per-RPC DEBUG log emits
// only non-sensitive token identifiers (sub, aud) — never the full JWT claims
// payload, which carries entitlements and custom claims.
func TestGoRpcDebugLogDoesNotDumpClaims(t *testing.T) {
	rendered := generateGRPCWrapper(
		&parser.ServiceInfo{Name: "ContractApi"},
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
		map[string]service.AccountingInfo{},
		map[string]string{},
		proto.SkippedTraceTypes{},
		nil,
	)

	for _, forbidden := range []string{
		`zap.Any("claims"`,
		"authTok.Proto().Payload)",
	} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("Go wrapper debug log must not dump full JWT claims (%q):\n%s", forbidden, rendered)
		}
	}
	for _, want := range []string{
		`zap.String("sub", authTok.GetSubject())`,
		`zap.String("aud", authTok.GetAudience())`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Go wrapper debug log missing non-sensitive identifier %q:\n%s", want, rendered)
		}
	}
}
