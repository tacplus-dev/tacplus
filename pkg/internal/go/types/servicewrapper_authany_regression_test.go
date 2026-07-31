// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
)

func TestGoAuthAnyUserUnaryUsesFixedNamespaceGate(t *testing.T) {
	for _, tc := range []struct {
		name       string
		method     *parser.MethodInfo
		wantTarget string
	}{
		{
			name: "plain unary defaults root",
			method: &parser.MethodInfo{
				Name:           "Read",
				InputType:      "PlainRequest",
				OutputType:     "PlainResponse",
				FullInputType:  "contractclient.PlainRequest",
				FullOutputType: "contractclient.PlainResponse",
				AuthAnyUser:    true,
			},
			wantTarget: "/",
		},
		{
			name: "trace unary uses declared literal",
			method: &parser.MethodInfo{
				Name:                 "TraceRead",
				InputType:            "ContractRequest",
				OutputType:           "ContractResponse",
				FullInputType:        "contractclient.ContractRequest",
				FullOutputType:       "contractclient.ContractResponse",
				AuthAnyUser:          true,
				TargetNamespaceField: "/tenants/acme",
			},
			wantTarget: "/tenants/acme",
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

			want := `p.authenticateAtOrBelow(ctx, "` + tc.wantTarget + `")`
			if !strings.Contains(rendered, want) {
				t.Fatalf("Go auth_any_user unary wrapper missing fixed namespace gate %q:\n%s", want, rendered)
			}
			if strings.Contains(rendered, "p.authenticate(ctx)") {
				t.Fatalf("Go auth_any_user unary wrapper must not use token-only authentication:\n%s", rendered)
			}
		})
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
