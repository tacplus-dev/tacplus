// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"encoding/json"
	"go/format"
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

// The generated gate meets a (spec.caller_tid) tool (#475): on a service that
// is not tenant-routed its test caller still carries a tid claim (or every call
// of the tool would be refused for want of one), the reaching test asserts the
// handler saw the field bound to that tid, a call naming the field is refused
// before the handler, and a caller with no tid claim is refused. The rendered
// gate must still be Go.
func TestGoMcpDispatchTestsBindCallerTid(t *testing.T) {
	svc := goMcpTestService()
	tools, err := goMcpTools(svc, "ticket", goMcpCallerTidSanitizer())
	if err != nil {
		t.Fatalf("resolve MCP tools: %v", err)
	}
	rendered := generateGoMcpDispatchTests(svc, tools, goMcpTestParams())
	formatted, err := format.Source([]byte(rendered))
	if err != nil {
		t.Fatalf("the generated gate is not Go: %v\n%s", err, rendered)
	}
	rendered = string(formatted)
	for _, want := range []string{
		// The test caller is in a tenant; a non-routed service keeps its namespace.
		`ticketApiMcpTestTid       = "acme0001"`,
		"SetTid(ticketApiMcpTestTid)",
		`ticketApiMcpTestNamespace = "/example/tenants/acme"`,
		// The tool is driven, and the handler saw the field bound to the tid.
		"func TestTicketApiMcpGetTicketBriefReachesItsTypedHandler(",
		`if !strings.Contains(reported, "\"acme0001\"") {`,
		`t.Fatalf("tenant was not bound to the caller's tid claim: %s", reported)`,
		// Naming the field is refused before the handler.
		"func TestTicketApiMcpGetTicketBriefRefusesAClientSuppliedTenant(",
		`json.RawMessage("{\"tenant\": \"acme0001\"}")`,
		// A caller with no tid claim is refused.
		"func TestTicketApiMcpGetTicketBriefRefusesACallerWithNoTid(",
		`"requires a caller in a tenant"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("the generated gate is missing %s:\n%s", want, rendered)
		}
	}
}

// The paired control: a non-routed service with no (spec.caller_tid) tool keeps
// a test caller with no tid claim, and no caller_tid assertion is rendered.
func TestGoMcpDispatchTestsWithoutCallerTidCarryNoTid(t *testing.T) {
	svc := goMcpTestService()
	tools, err := goMcpTools(svc, "ticket", goMcpTestSanitizer())
	if err != nil {
		t.Fatalf("resolve MCP tools: %v", err)
	}
	rendered := generateGoMcpDispatchTests(svc, tools, goMcpTestParams())
	for _, absent := range []string{"TestTid", "SetTid(", "RefusesACallerWithNoTid", "tid claim: %s"} {
		if strings.Contains(rendered, absent) {
			t.Fatalf("a service with no caller_tid tool renders %q:\n%s", absent, rendered)
		}
	}
}
