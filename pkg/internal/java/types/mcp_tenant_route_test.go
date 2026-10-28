// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// Issue #170 on the MCP seam: the model never sets the tenant. The carrier
// stamps mcp.McpRequest.tenant from the session's token before the routing
// backstop sees the frame, a routed tool's schema omits its route field, and
// its dispatch refuses the field by name and fills it from the session.

// javaMcpRoutedSanitizer is the fixture's message set with a route field on
// each tool request: GetTicketBrief's has a JSON name of its own (and is
// required, so the schema's required set is checked too), GetTicketRoster's
// does not.
func javaMcpRoutedSanitizer() *javaEgressSanitizer {
	san := javaMcpTestSanitizer()
	brief := san.msgs["ticket\x00GetTicketBriefRequest"]
	brief.Fields = append(brief.Fields, model.Field{Name: "target_tenant", Type: "string", Number: 4,
		Comment:     "The tenant whose ticket this is.",
		Annotations: map[string]model.AnnotationValue{"assert_required": {BoolValue: true}}})
	roster := san.msgs["ticket\x00GetTicketRosterRequest"]
	roster.Fields = append(roster.Fields, model.Field{Name: "tid", Type: "string", Number: 2})
	return san
}

// javaMcpRoutedService is javaMcpTestService as a tenant-routed service: every
// rpc, the carrier included, declares its route field.
func javaMcpRoutedService() *parser.ServiceInfo {
	svc := javaMcpTestService()
	svc.Methods[0].RouteField = &parser.RouteFieldDescriptor{Field: "target_tenant", JSONName: "targetTenant", Number: 4}
	svc.Methods[1].RouteField = &parser.RouteFieldDescriptor{Field: "tid", JSONName: "tid", Number: 2}
	svc.Methods[2].RouteField = &parser.RouteFieldDescriptor{Field: "ticket_id", JSONName: "ticketId", Number: 1}
	svc.Methods[3].RouteField = &parser.RouteFieldDescriptor{Field: "tenant", JSONName: "tenant", Number: 2}
	return svc
}

func javaMcpRoutedFileParams() javaMcpFileParams {
	p := javaMcpTestFileParams()
	p.RouteTenantFQN = "com.example.chassis.serviceconnection.RouteTenant"
	return p
}

func javaMcpRoutedTools(t *testing.T) []javaMcpTool {
	t.Helper()
	tools, err := javaMcpTools(javaMcpRoutedService(), "ticket", "Ticket",
		map[string]string{"mcp": "Mcp"}, map[string]string{}, javaMcpRoutedSanitizer())
	if err != nil {
		t.Fatalf("resolve Java MCP tools: %v", err)
	}
	return tools
}

func TestJavaMcpRoutedToolSchemaOmitsTheRouteField(t *testing.T) {
	tools := javaMcpRoutedTools(t)
	for i, field := range []string{"target_tenant", "tid"} {
		var schema map[string]any
		if err := json.Unmarshal([]byte(tools[i].Schema), &schema); err != nil {
			t.Fatalf("the emitted tool schema is not JSON: %v\n%s", err, tools[i].Schema)
		}
		props, _ := schema["properties"].(map[string]any)
		if props == nil {
			t.Fatalf("the tool schema has no properties:\n%s", tools[i].Schema)
		}
		if _, ok := props[field]; ok {
			t.Fatalf("the route field %s is in the tool schema; the model must not be able to name a tenant:\n%s", field, tools[i].Schema)
		}
		for _, r := range func() []any { r, _ := schema["required"].([]any); return r }() {
			if r == field {
				t.Fatalf("the route field %s is required by the tool schema:\n%s", field, tools[i].Schema)
			}
		}
	}
	// The paired control: the same request on a platform service publishes the
	// field as an ordinary argument.
	platform, err := javaMcpTools(javaMcpTestService(), "ticket", "Ticket",
		map[string]string{"mcp": "Mcp"}, map[string]string{}, javaMcpRoutedSanitizer())
	if err != nil {
		t.Fatalf("resolve Java MCP tools: %v", err)
	}
	if !strings.Contains(platform[0].Schema, `"target_tenant"`) {
		t.Fatalf("an unrouted field is missing from a platform tool's schema:\n%s", platform[0].Schema)
	}
}

func TestJavaMcpRoutedToolRefusesTheRouteFieldAndTakesItFromTheSession(t *testing.T) {
	rendered := generateJavaMcpDispatch(javaMcpRoutedService(), javaMcpRoutedTools(t), javaMcpRoutedFileParams())
	brief := rendered[strings.Index(rendered, "callGetTicketBrief(McpSchema.CallToolRequest request)"):]
	brief = brief[:strings.Index(brief, "callGetTicketRoster")]

	// Refused by BOTH names: JsonFormat's parser accepts either, so refusing
	// only the proto name would let the JSON name through to be overwritten.
	order := []string{
		`if (argumentNames(request).contains("target_tenant") || argumentNames(request).contains("targetTenant")) {`,
		`return toolError("target_tenant is set by the server from your session's tenant; it is not a tool argument");`,
		"final String __routeTenant = sessionTenant(caller);",
		"decode(request, __b);",
		"__b.setTargetTenant(__routeTenant);",
		"Ticket.GetTicketBriefRequest __req = __b.build();",
		"Helpers.validateGetTicketBriefRequest(__req);",
		"handler.getTicketBrief(caller, __req)",
	}
	at := 0
	for _, step := range order {
		i := strings.Index(brief[at:], step)
		if i < 0 {
			t.Fatalf("the routed dispatch is missing step %q, or has it out of order:\n%s", step, brief)
		}
		at += i
	}
	// A route field whose JSON name is its proto name is checked once.
	if !strings.Contains(rendered, `if (argumentNames(request).contains("tid")) {`) {
		t.Fatalf("the routed roster tool does not refuse its route field:\n%s", rendered)
	}

	// The session's tenant is the verified caller's, from its namespace claim,
	// and a caller with none cannot use a routed tool at all.
	for _, want := range []string{
		"public static String sessionTenant(Caller caller) {",
		"com.example.chassis.serviceconnection.RouteTenant.tenantOfCaller(caller.getNamespace())",
		"throw ProtoException.newErrPermissionDenied(null, \"%s\",",
		"public static Mcp.McpRequest stampTenant(String sessionTenant, Mcp.McpRequest frame) {",
		"return frame.toBuilder().setTenant(sessionTenant).build();",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("the session tenant helpers are missing %q:\n%s", want, rendered)
		}
	}
	stamp := rendered[strings.Index(rendered, "public static Mcp.McpRequest stampTenant("):]
	if !strings.Contains(stamp[:strings.Index(stamp, "\n    }\n")], "if (!frame.getTenant().equals(sessionTenant)) {\n            throw ProtoException.newErrPermissionDenied(") {
		t.Fatalf("a frame naming another tenant than the session's is not refused:\n%s", stamp)
	}
}

// A platform service's MCP termination carries none of it.
func TestJavaMcpPlatformServiceHasNoSessionTenant(t *testing.T) {
	rendered := generateJavaMcpDispatch(javaMcpTestService(), javaMcpResolvedTools(t), javaMcpRoutedFileParams())
	gate := generateJavaMcpDispatchTest(javaMcpTestService(), javaMcpResolvedTools(t), javaMcpRoutedFileParams())
	for _, notWant := range []string{"sessionTenant", "stampTenant", "RouteTenant", "__routeTenant", "/tenants/"} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("a platform service's MCP dispatch carries %q:\n%s", notWant, rendered)
		}
		if strings.Contains(gate, notWant) {
			t.Fatalf("a platform service's MCP gate carries %q:\n%s", notWant, gate)
		}
	}
	if !strings.Contains(gate, `\"namespace\":\"/mcp-test\"`) {
		t.Fatalf("a platform service's MCP gate changed its test caller:\n%s", gate)
	}
}

// The generated gate proves each property on the generated code itself.
func TestJavaMcpRoutedGateCoversTheTenant(t *testing.T) {
	gate := generateJavaMcpDispatchTest(javaMcpRoutedService(), javaMcpRoutedTools(t), javaMcpRoutedFileParams())
	for _, want := range []string{
		// The test caller belongs to a tenant, or no routed tool call could pass.
		`\"namespace\":\"/tenants/mcptest1\"`,
		// The schema has no route field.
		`check(__propsTargetTenant != null && !__propsTargetTenant.containsKey("target_tenant"),`,
		// The route field is taken from the token.
		`check(stub.lastRequest.contains("mcptest1"), "get_ticket_brief took target_tenant from the session's token");`,
		// A model-supplied tenant is refused, by either name.
		"getTicketBriefRefusesAModelSuppliedTargetTenant();",
		`java.util.Map.of("ticket_id", "mcp-test-ticket_id", "target_tenant", "othertn1")`,
		"getTicketBriefRefusesAModelSuppliedTargetTenantByItsJsonName();",
		`java.util.Map.of("ticket_id", "mcp-test-ticket_id", "targetTenant", "othertn1")`,
		"getTicketRosterRefusesAModelSuppliedTid();",
		// The session's tenant comes from the token, and a token naming none
		// opens no session.
		"sessionTenantIsTheCallersTenant();",
		`TicketApiMcp.sessionTenant(new Caller("{\"sub\":\"` + javaMcpTestSubject + `\",\"namespace\":\"/tenants/mcptest1/team\"}", "mcp-test-request"))`,
		`\"namespace\":\"/system\"}", "mcp-test-request")),`,
		// Frame ingress: empty is stamped, the same is kept, another is refused.
		"aFrameWithNoTenantIsStamped();",
		"aFrameWithAnotherTenantIsRefused();",
		`TicketApiMcp.stampTenant("mcptest1", Mcp.McpRequest.newBuilder().setPayload("{}").setTenant("othertn1").build())`,
		"ProtoException.PERMISSION_DENIED",
	} {
		if !strings.Contains(gate, want) {
			t.Fatalf("the routed MCP gate is missing %q:\n%s", want, gate)
		}
	}
}

// The carrier: the session tenant is resolved at stream open, and every inbound
// frame is stamped (or refused) BEFORE the generic route backstop checks it
// against the instance's tenant — stamp, then check.
func TestJavaMcpCarrierStampsTheFrameTenantBeforeTheRouteBackstop(t *testing.T) {
	render := func(t *testing.T, routed bool) string {
		outputDir := t.TempDir()
		wrapperDir := filepath.Join(outputDir, "wrapper")
		svc := ServiceInfo{
			ProtoFile: "svc/ticket.proto", PackageName: "ticket", ServiceName: "TicketApi", GrpcName: "ticket",
			IsGrpc: true, IsC2S: true, OutputDir: wrapperDir, TypesOutputDir: filepath.Join(outputDir, "types"),
			ParentDir: "svc",
			Config:    &tactype.Config{OutputDir: outputDir, JavaPackageBase: "com.example"},
		}
		target := javaMcpTestService()
		san := javaMcpTestSanitizer()
		if routed {
			target = javaMcpRoutedService()
			san = javaMcpRoutedSanitizer()
		}
		info := &parser.ProtoInfo{Package: "ticket", Services: []*parser.ServiceInfo{target}}
		if _, err := generateServiceWrapper(svc, info, nil, proto.SkippedTraceTypes{}, san); err != nil {
			t.Fatalf("generateServiceWrapper: %v", err)
		}
		raw, err := os.ReadFile(filepath.Join(wrapperDir, "TicketApi.java"))
		if err != nil {
			t.Fatalf("read generated wrapper: %v", err)
		}
		mcp, err := os.ReadFile(filepath.Join(wrapperDir, "TicketApiMcp.java"))
		if err != nil {
			t.Fatalf("read generated MCP dispatch: %v", err)
		}
		return string(raw) + "\n" + string(mcp)
	}

	got := render(t, true)
	carrier := got[strings.Index(got, "brokerTicketApiMcpSession(StreamObserver<"):]
	carrier = carrier[:strings.Index(carrier, "\n    }\n")]
	order := []string{
		"__sessionTenant = TicketApiMcp.sessionTenant(caller);",
		"scopingRequestObserver(validatingRequestObserver(TicketApiMcp.serve(handler, caller, ",
		`checkRouteTenant("tenant", value.getTenant());`,
		"value -> TicketApiMcp.stampTenant(__sessionTenant, value), responseObserver)",
	}
	at := 0
	for _, step := range order {
		i := strings.Index(carrier[at:], step)
		if i < 0 {
			t.Fatalf("the routed carrier is missing %q, or has it out of order:\n%s", step, carrier)
		}
		at += i
	}
	// The generated tool dispatch reaches RouteTenant through the chassis.
	if !strings.Contains(got, "com.example.chassis.serviceconnection.RouteTenant.tenantOfCaller(") {
		t.Fatalf("the MCP dispatch does not read the session tenant through RouteTenant:\n%s", got)
	}

	platform := render(t, false)
	for _, notWant := range []string{"sessionTenant", "stampTenant", "checkRouteTenant"} {
		if strings.Contains(platform, notWant) {
			t.Fatalf("a platform carrier carries %q:\n%s", notWant, platform)
		}
	}
}
