// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/proto"
)

// auth_any_user means "any authenticated user can call this RPC". The optional
// target_namespace_field on such a method is an authored FIXED namespace
// literal, not a request field name. With no literal there is no namespace to
// compare against, so the gate is plain authentication; defaulting the target to
// "/" instead would make IsAtOrBelow(callerNamespace, "/") the gate, which is
// true only for a caller sitting exactly at "/" — every real caller denied.
func TestGoAuthAnyUserUnaryGate(t *testing.T) {
	for _, tc := range []struct {
		name     string
		method   *parser.MethodInfo
		wantCall string
		notWant  string
	}{
		{
			name: "no target namespace field authenticates only",
			method: &parser.MethodInfo{
				Name:           "Read",
				InputType:      "PlainRequest",
				OutputType:     "PlainResponse",
				FullInputType:  "contractclient.PlainRequest",
				FullOutputType: "contractclient.PlainResponse",
				AuthAnyUser:    true,
			},
			wantCall: `p.authenticate(ctx)`,
			notWant:  `p.authenticateAtOrBelow(`,
		},
		{
			name: "declared literal keeps the fixed namespace gate",
			method: &parser.MethodInfo{
				Name:                 "TraceRead",
				InputType:            "ContractRequest",
				OutputType:           "ContractResponse",
				FullInputType:        "contractclient.ContractRequest",
				FullOutputType:       "contractclient.ContractResponse",
				AuthAnyUser:          true,
				TargetNamespaceField: "/tenants/acme",
			},
			wantCall: `p.authenticateAtOrBelow(ctx, "/tenants/acme")`,
			notWant:  `p.authenticate(ctx)`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered := generateGRPCWrapper(
				&parser.ServiceInfo{Name: "ContractApi"},
				tc.method,
				"contractclient",
				"contract",
				"contract",
				map[string]service.AccountingInfo{},
				map[string]string{},
				proto.SkippedTraceTypes{},
				nil,
			)

			if !strings.Contains(rendered, tc.wantCall) {
				t.Fatalf("Go auth_any_user unary wrapper missing %q:\n%s", tc.wantCall, rendered)
			}
			if strings.Contains(rendered, tc.notWant) {
				t.Fatalf("Go auth_any_user unary wrapper must not contain %q:\n%s", tc.notWant, rendered)
			}
		})
	}
}

// The PAGE surface builds its auth block independently of the unary one; pin the
// same rule there.
func TestGoAuthAnyUserPageWithoutTargetAuthenticatesOnly(t *testing.T) {
	method := pageMethod()
	method.AuthAnyUser = true

	rendered := generateGRPCWrapper(
		&parser.ServiceInfo{Name: "Catalog", IsS2S: true, Methods: []*parser.MethodInfo{method}},
		method,
		"catalog",
		"services",
		"",
		map[string]service.AccountingInfo{},
		map[string]string{},
		proto.SkippedTraceTypes{},
		nil,
	)

	if !strings.Contains(rendered, "p.authenticate(ctx)") {
		t.Fatalf("Go auth_any_user PAGE wrapper with no target_namespace_field must authenticate only:\n%s", rendered)
	}
	if strings.Contains(rendered, "p.authenticateAtOrBelow(") {
		t.Fatalf("Go auth_any_user PAGE wrapper with no target_namespace_field must not gate on a namespace:\n%s", rendered)
	}
}

func TestGoAuthAnyUserPageUsesFixedNamespaceGate(t *testing.T) {
	method := pageMethod()
	method.AuthAnyUser = true
	method.TargetNamespaceField = "/catalog"

	rendered := generateGRPCWrapper(
		&parser.ServiceInfo{Name: "Catalog", IsS2S: true, Methods: []*parser.MethodInfo{method}},
		method,
		"catalog",
		"services",
		"",
		map[string]service.AccountingInfo{},
		map[string]string{},
		proto.SkippedTraceTypes{},
		nil,
	)

	if !strings.Contains(rendered, `p.authenticateAtOrBelow(ctx, "/catalog")`) {
		t.Fatalf("Go auth_any_user PAGE wrapper missing fixed namespace gate:\n%s", rendered)
	}
	if strings.Contains(rendered, "p.authenticate(ctx)") {
		t.Fatalf("Go auth_any_user PAGE wrapper must not use token-only authentication:\n%s", rendered)
	}
}

// TestGoUnaryAuthorizePrecedesHandler (LED-1): the auth blocks are emitted via
// nested positional Sprintf, so a swapped-but-valid arg would still compile.
// Pin the two invariants an artifact/compile gate cannot catch on its own for a
// normal (non-auth_any_user) authenticated method: the auth call is p.authorize
// (not authenticateAtOrBelow / authenticate), and it TEXTUALLY PRECEDES the
// handler call — auth can never be emitted after the handler runs.
func TestGoUnaryAuthorizePrecedesHandler(t *testing.T) {
	m := &parser.MethodInfo{
		Name:           "GetThing",
		InputType:      "GetThingRequest",
		OutputType:     "GetThingResponse",
		FullInputType:  "contractclient.GetThingRequest",
		FullOutputType: "contractclient.GetThingResponse",
	}
	rendered := generateGRPCWrapper(
		&parser.ServiceInfo{Name: "ContractApi"},
		m, "contractclient", "contract", "contract",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, nil,
	)

	authIdx := strings.Index(rendered, "p.authorize(ctx,")
	handlerIdx := strings.Index(rendered, "p.handler.GetThing(")
	if authIdx < 0 {
		t.Fatalf("normal authenticated unary must gate with p.authorize:\n%s", rendered)
	}
	if handlerIdx < 0 {
		t.Fatalf("could not find the handler call:\n%s", rendered)
	}
	if authIdx > handlerIdx {
		t.Fatalf("p.authorize must precede the handler call (auth-before-handler), got auth@%d handler@%d:\n%s", authIdx, handlerIdx, rendered)
	}
	// A normal method must NOT use the auth_any_user fixed-namespace gate.
	if strings.Contains(rendered, "p.authenticateAtOrBelow(") {
		t.Fatalf("normal authenticated method must not use the auth_any_user gate:\n%s", rendered)
	}
}
