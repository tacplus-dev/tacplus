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

// TestGoAccountingCarriesVerifiedCallerIdentity guards the Go analogue of the
// Rust F10 fix (verified caller claims must reach the accounting record, not
// an always-empty map). Go's servicewrapper generator builds identityMap from
// caller.Token(), and the caller is the token the per-RPC authorize/
// authenticate call returned — so an authorized RPC's identityMap carries
// verified claims, and an auth_disabled RPC (nothing verifies a caller)
// carries no identity at all.
//
// Go DID have its own version of the Rust bug: the caller used to come from
// servicemanager.TokenFromContext, a context key only the never-installed
// AuthUnaryInterceptor/AuthStreamInterceptor wrote, so identityMap was in fact
// always empty. Note what this test could not see: every assertion below still
// held while the caller was unconditionally nil, because they all describe the
// SHAPE of the identity block, not where the caller comes from. That question
// is pinned in servicewrapper_caller_from_auth_regression_test.go.
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
	if !strings.Contains(authorized, "servicemanager.NewCaller(authTok, requestID)") {
		t.Fatalf("authorized RPC caller must derive from the token the auth check verified:\n%s", authorized)
	}
	for _, want := range []string{
		"p.emitter.RpcCompleted(ctx, accountingMethod, durationMs, identityMap, requestID)",
	} {
		if !strings.Contains(authorized, want) {
			t.Fatalf("authorized RPC accounting missing %q:\n%s", want, authorized)
		}
	}

	// auth_disabled: nothing verifies a caller, so the emitted record must not
	// pretend to carry an identity — identityMap is declared but only populated
	// when caller != nil, and an auth-disabled RPC never builds a caller at all.
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
