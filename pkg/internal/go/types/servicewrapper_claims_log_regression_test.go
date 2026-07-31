// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
)

// TestGoRpcDebugLogDoesNotDumpClaims pins that the per-RPC DEBUG log emits
// only non-sensitive token identifiers (sub, aud) — never the full JWT claims
// payload, which carries entitlements and custom claims.
func TestGoRpcDebugLogDoesNotDumpClaims(t *testing.T) {
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
		map[string]service.AccountingInfo{},
		map[string]string{},
		proto.SkippedTraceTypes{},
		nil,
	)

	for _, forbidden := range []string{
		`zap.Any("claims"`,
		"tok.Proto().Payload)",
	} {
		if strings.Contains(rendered, forbidden) {
			t.Fatalf("Go wrapper debug log must not dump full JWT claims (%q):\n%s", forbidden, rendered)
		}
	}
	for _, want := range []string{
		`zap.String("sub", tok.GetSubject())`,
		`zap.String("aud", tok.GetAudience())`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Go wrapper debug log missing non-sensitive identifier %q:\n%s", want, rendered)
		}
	}
}
