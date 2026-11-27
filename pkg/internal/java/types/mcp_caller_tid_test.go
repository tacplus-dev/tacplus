// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// javaMcpCallerTidSanitizer is javaMcpTestSanitizer's tool requests with a
// (spec.caller_tid) field on the first tool.
func javaMcpCallerTidSanitizer() *javaEgressSanitizer {
	tid := map[string]model.AnnotationValue{"caller_tid": {Type: model.AnnotationTypeBool, BoolValue: true}}
	return newJavaEgressSanitizer([]model.ProtoFile{{Package: "ticket", Messages: []model.Message{
		{Name: "GetTicketBriefRequest", Fields: []model.Field{
			{Name: "tenant", Type: "string", Number: 1, Annotations: tid},
			{Name: "ticket_id", Type: "string", Number: 2},
		}},
		{Name: "GetTicketBriefResponse", Fields: []model.Field{{Name: "summary", Type: "string", Number: 1}}},
		{Name: "GetTicketRosterRequest", Fields: []model.Field{{Name: "note", Type: "string", Number: 1}}},
		{Name: "GetTicketRosterResponse", Fields: []model.Field{{Name: "note", Type: "string", Number: 1}}},
		{Name: "GetTicketRequest", Fields: []model.Field{{Name: "ticket_id", Type: "string", Number: 1}}},
		{Name: "GetTicketResponse", Fields: []model.Field{{Name: "body", Type: "string", Number: 1}}},
	}}})
}

// (spec.caller_tid) on a Java MCP tool (#475, products-layer-2026-11-08) is
// the server's, as on the gRPC wrapper: absent from the schema, refused when
// named, and bound to the session caller's tid claim (empty stamped, another
// tenant or no tid claim refused) before the handler runs.
func TestJavaMcpToolBindsCallerTid(t *testing.T) {
	tools, err := javaMcpTools(javaMcpTestService(), "ticket", "Ticket",
		map[string]string{"mcp": "Mcp"}, map[string]string{}, javaMcpCallerTidSanitizer())
	if err != nil {
		t.Fatalf("resolve Java MCP tools: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(tools[0].Schema), &schema); err != nil {
		t.Fatal(err)
	}
	if props := schema["properties"].(map[string]any); props["tenant"] != nil || props["ticket_id"] == nil {
		t.Fatalf("the (spec.caller_tid) field must be absent from the schema, the others present:\n%s", tools[0].Schema)
	}
	rendered := generateJavaMcpDispatch(javaMcpTestService(), tools, javaMcpTestFileParams())
	for _, want := range []string{
		`if (argumentNames(request).contains("tenant")) {`,
		`return toolError("tenant is set by the server from your verified tid claim; it is not a tool argument");`,
		"final String __callerTid = caller.getTid();",
		`"GetTicketBrief requires a caller in a tenant: its token carries no tid claim (products-layer-2026-11-08)"`,
		"__req = __req.toBuilder().setTenant(__callerTid).build();",
		`"tenant does not match the caller's tenant (its token's tid claim): a call keys only its own tenant's data"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("dispatch missing %q:\n%s", want, rendered)
		}
	}
	if n := strings.Count(rendered, "caller.getTid()"); n != 1 {
		t.Fatalf("only the one tool declaring (spec.caller_tid) binds it, got %d bindings:\n%s", n, rendered)
	}
	if bind, call := strings.Index(rendered, "setTenant(__callerTid)"), strings.Index(rendered, "handler.getTicketBrief("); bind < 0 || call < 0 || bind > call {
		t.Fatalf("the binding must precede the handler call:\n%s", rendered)
	}
}

// The generated gate of a service that is NOT tenant-routed but has a
// (spec.caller_tid) tool dispatches as a caller with a tid claim — else every
// call of that tool would be refused — keeps its own namespace, and asserts
// the binding, the refusal of a model-named field and of a caller with no tid.
func TestJavaMcpGateDrivesACallerTidTool(t *testing.T) {
	tools, err := javaMcpTools(javaMcpTestService(), "ticket", "Ticket",
		map[string]string{"mcp": "Mcp"}, map[string]string{}, javaMcpCallerTidSanitizer())
	if err != nil {
		t.Fatalf("resolve Java MCP tools: %v", err)
	}
	gate := generateJavaMcpDispatchTest(javaMcpTestService(), tools, javaMcpTestFileParams())
	for _, want := range []string{
		// The test caller carries a tid on a non-routed service, in its own namespace.
		`return new Caller("{\"sub\":\"spiffe://example.test/mcp-test-caller\",\"namespace\":\"/mcp-test\",\"tid\":\"mcptest1\"}", "mcp-test-request");`,
		// The handler received the caller's tid in the field.
		`check(stub.lastRequest.contains("mcptest1"), "get_ticket_brief bound tenant to the caller's tid claim");`,
		// A model-named field is refused before the handler.
		"private static void getTicketBriefRefusesAClientSuppliedTenant() {",
		`java.util.Map.of("tenant", "othertn1")`,
		"        getTicketBriefRefusesAClientSuppliedTenant();\n",
		// A caller with no tid claim is refused before the handler.
		"private static void getTicketBriefRefusesACallerWithNoTid() {",
		`new Caller("{\"sub\":\"spiffe://example.test/mcp-test-caller\",\"namespace\":\"/mcp-test\"}", "mcp-test-request")`,
		`check(resultText(result).contains("requires a caller in a tenant"),`,
		"        getTicketBriefRefusesACallerWithNoTid();\n",
		// The session case reads the binding back off the handler, too.
		`"the session bound tenant to the caller's tid claim — a value no frame above carried");`,
	} {
		if !strings.Contains(gate, want) {
			t.Fatalf("gate missing %q:\n%s", want, gate)
		}
	}
	if strings.Count(gate, "{") != strings.Count(gate, "}") {
		t.Fatalf("the gate's braces are unbalanced:\n%s", gate)
	}

	// The control: a service with no (spec.caller_tid) tool and no route keeps
	// a caller with no tid claim, and no caller_tid cases.
	plain := generateJavaMcpDispatchTest(javaMcpTestService(), javaMcpResolvedTools(t), javaMcpTestFileParams())
	if strings.Contains(plain, `\"tid\"`) || strings.Contains(plain, "WithNoTid") {
		t.Fatalf("a service with no (spec.caller_tid) tool gained a tid caller:\n%s", plain)
	}
}
