// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
)

// TestGoAccountingCarriesVerifiedCallerIdentity guards the Go analogue of the
// Rust F10 fix (verified caller claims must reach the accounting record, not
// an always-empty map). Go's servicewrapper generator builds identityMap from
// caller.Token(), where caller derives from servicemanager.TokenFromContext,
// which only yields a non-nil token once signature/audience verification has
// passed — so this test also pins down that Go never had the Rust bug: an
// authorized RPC's identityMap is built from a verified token, and an
// auth_disabled RPC (nothing verifies a caller) carries no identity at all.
func TestGoAccountingCarriesVerifiedCallerIdentity(t *testing.T) {
	accounting := map[string]service.AccountingInfo{
		"contract": {EnumName: "Contract", RPCs: map[string]bool{"Read": true, "PublicRead": true}},
	}

	authorized := generateGRPCWrapper(
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
		accounting,
		map[string]string{},
		proto.SkippedTraceTypes{},
		nil,
	)

	if !strings.Contains(authorized, "if caller != nil {") {
		t.Fatalf("authorized RPC accounting must gate identityMap on a verified caller:\n%s", authorized)
	}
	if !strings.Contains(authorized, "payload := caller.Token().Proto().Payload") {
		t.Fatalf("authorized RPC identityMap must be sourced from the verified token's payload:\n%s", authorized)
	}
	if !strings.Contains(authorized, "if tok := servicemanager.TokenFromContext(") {
		t.Fatalf("authorized RPC caller must derive from the verified token in context:\n%s", authorized)
	}
	for _, want := range []string{
		"p.emitter.RpcCompleted(ctx, accountingMethod, durationMs, identityMap, requestID)",
	} {
		if !strings.Contains(authorized, want) {
			t.Fatalf("authorized RPC accounting missing %q:\n%s", want, authorized)
		}
	}

	// auth_disabled: nothing verifies a caller, so the emitted record must not
	// pretend to carry an identity — identityMap is declared but only
	// populated when caller != nil, and no auth-disabled RPC can produce a
	// non-nil caller (TokenFromContext still runs, but nothing forces a
	// caller to present a token, so caller is nil whenever none was sent).
	noAuth := generateGRPCWrapper(
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
		accounting,
		map[string]string{},
		proto.SkippedTraceTypes{},
		nil,
	)
	if !strings.Contains(noAuth, "var identityMap map[string]string") {
		t.Fatalf("auth_disabled RPC accounting must still declare identityMap (nil unless a caller is verified):\n%s", noAuth)
	}
	if !strings.Contains(noAuth, "if caller != nil {") {
		t.Fatalf("auth_disabled RPC accounting must not unconditionally populate identityMap:\n%s", noAuth)
	}
}
