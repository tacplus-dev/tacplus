// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// goMcpCallerTidSanitizer is goMcpTestSanitizer's tool request with a
// (spec.caller_tid) field.
func goMcpCallerTidSanitizer() *egressSanitizer {
	return newEgressSanitizer([]model.ProtoFile{{Package: "ticket", Messages: []model.Message{
		{Name: "GetTicketBriefRequest", Fields: []model.Field{
			{Name: "tenant", Type: "string", Number: 1,
				Annotations: map[string]model.AnnotationValue{"caller_tid": {Type: model.AnnotationTypeBool, BoolValue: true}}},
			{Name: "ticket_id", Type: "string", Number: 2},
		}},
		{Name: "GetTicketBriefResponse", Fields: []model.Field{{Name: "summary", Type: "string", Number: 1}}},
		{Name: "GetTicketRequest", Fields: []model.Field{{Name: "ticket_id", Type: "string", Number: 1}}},
		{Name: "GetTicketResponse", Fields: []model.Field{{Name: "body", Type: "string", Number: 1}}},
	}}})
}

// (spec.caller_tid) on an MCP tool (#475, products-layer-2026-11-08) is the
// server's, as on the gRPC wrapper: absent from the schema, refused when named,
// and bound to the session caller's tid claim (empty stamped, another tenant
// or no tid claim refused) before the handler runs.
func TestGoMcpToolBindsCallerTid(t *testing.T) {
	tools, err := goMcpTools(goMcpTestService(), "ticket", goMcpCallerTidSanitizer())
	if err != nil {
		t.Fatalf("resolve MCP tools: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(tools[0].Schema), &schema); err != nil {
		t.Fatal(err)
	}
	if props := schema["properties"].(map[string]any); props["tenant"] != nil || props["ticket_id"] == nil {
		t.Fatalf("the (spec.caller_tid) field must be absent from the schema, the others present:\n%s", tools[0].Schema)
	}
	rendered, err := generateGoMcpDispatch(goMcpTestService(), goMcpTestParams(), goMcpCallerTidSanitizer())
	if err != nil {
		t.Fatalf("generate Go MCP dispatch: %v", err)
	}
	for _, want := range []string{
		`if named["tenant"] {`,
		`"tenant is set by the server from your verified tid claim; it is not a tool argument"`,
		"__caller_tid = s.caller.Token().GetTid()",
		`"GetTicketBrief requires a caller in a tenant: its token carries no tid claim (products-layer-2026-11-08)"`,
		"req.Tenant = __caller_tid",
		`"tenant does not match the caller's tenant (its token's tid claim): a call keys only its own tenant's data"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("dispatch missing %q:\n%s", want, rendered)
		}
	}
	if bind, call := strings.Index(rendered, "req.Tenant = __caller_tid"), strings.Index(rendered, "s.handler.GetTicketBrief("); bind > call {
		t.Fatalf("the binding must precede the handler call:\n%s", rendered)
	}
}
