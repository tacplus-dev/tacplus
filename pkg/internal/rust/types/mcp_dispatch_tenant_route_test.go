// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// Issue #170 on the MCP path: the model never sets the tenant. On a
// tenant-routed service the carrier stamps each inbound frame's tenant from
// the session's token (refusing a frame naming another) before the routing
// backstop checks it, every tool's route field is absent from its schema and
// refused as an argument, and dispatch fills it from the session tenant.

func rustMcpRouteSanitizer() *rustEgressSanitizer {
	required := map[string]model.AnnotationValue{"assert_required": {BoolValue: true}}
	return newRustEgressSanitizer([]model.ProtoFile{{Package: "crew", Messages: []model.Message{
		{Name: "SendMessageRequest", Fields: []model.Field{
			{Name: "text", Type: "string", Number: 1, Annotations: required},
			{Name: "target_tenant", Type: "string", Number: 2, Annotations: required},
		}},
		{Name: "SendMessageResponse", Fields: []model.Field{{Name: "delivered", Type: "bool", Number: 1}}},
	}}})
}

// rustMcpRouteService is a tenant-routed service with one tool and its carrier,
// both declaring their route fields as mcp_carrier.go and the parser do.
func rustMcpRouteService() (*parser.ServiceInfo, *parser.MethodInfo, *parser.MethodInfo) {
	tool := &parser.MethodInfo{
		Name:           "SendMessage",
		InputType:      "SendMessageRequest",
		OutputType:     "SendMessageResponse",
		FullInputType:  "crew.SendMessageRequest",
		FullOutputType: "crew.SendMessageResponse",
		IsAutoMcp:      true,
		McpToolName:    "send_message",
		Comment:        "SendMessage sends a message to the target tenant's crew.",
		RouteField:     &parser.RouteFieldDescriptor{Field: "target_tenant", JSONName: "targetTenant", Number: 2},
	}
	carrier := &parser.MethodInfo{
		Name:            parser.McpCarrierRpcName("CrewService"),
		InputType:       "McpRequest",
		OutputType:      "McpResponse",
		FullInputType:   "mcp.McpRequest",
		FullOutputType:  "mcp.McpResponse",
		ClientStreaming: true,
		ServerStreaming: true,
		RouteField:      &parser.RouteFieldDescriptor{Field: "tenant", JSONName: "tenant", Number: 2},
	}
	return &parser.ServiceInfo{
		Name:       "CrewService",
		IsC2S:      true,
		HasMcpRpcs: true,
		Methods:    []*parser.MethodInfo{tool, carrier},
	}, tool, carrier
}

func mustRustMcpRouteDispatch(t *testing.T, svc *parser.ServiceInfo) string {
	t.Helper()
	out, err := generateRustMcpDispatch(svc, "crew", map[string]string{}, rustMcpRouteSanitizer())
	if err != nil {
		t.Fatalf("generateRustMcpDispatch(%s): %v", svc.Name, err)
	}
	return out
}

// The route field is not a shape of tool call the model can make: it is
// absent from the schema, required or not.
func TestRustMcpRoutedToolSchemaOmitsTheRouteField(t *testing.T) {
	_, tool, _ := rustMcpRouteService()
	raw, err := rustMcpInputSchema(tool, "crew", rustMcpRouteSanitizer())
	if err != nil {
		t.Fatalf("rustMcpInputSchema: %v", err)
	}
	var schema struct {
		Required   []string                  `json:"required"`
		Properties map[string]map[string]any `json:"properties"`
	}
	if err := json.Unmarshal([]byte(raw), &schema); err != nil {
		t.Fatalf("schema is not JSON (%v): %s", err, raw)
	}
	if _, ok := schema.Properties["target_tenant"]; ok {
		t.Fatalf("the route field reached the tool schema: %s", raw)
	}
	if _, ok := schema.Properties["text"]; !ok {
		t.Fatalf("an ordinary field is missing from the tool schema: %s", raw)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "text" {
		t.Fatalf("the route field must not be a required argument either: %v", schema.Required)
	}
}

// A tool call naming the route field (either JSON spelling) is refused; the
// decoded request's route field is set from the session tenant, before
// validation and the handler.
func TestRustMcpRoutedDispatchRefusesAndStampsTheRouteField(t *testing.T) {
	svc, _, _ := rustMcpRouteService()
	got := mustRustMcpRouteDispatch(t, svc)
	assertOrdered(t, got,
		"async fn call_send_message(",
		`if arguments.contains_key("target_tenant") || arguments.contains_key("targetTenant") {`,
		`"target_tenant is set by the server from your session's tenant; it is not a tool argument"`,
		"let req: SendMessageRequest = serde_json::from_value(",
		"req.target_tenant = crew_service_mcp_session_tenant(&self.caller)?;",
		"req.validate()",
		"self.handler.send_message(caller, req).await?;")
}

// The session tenant is the tid of the caller's token namespace, and a caller
// whose namespace names none cannot open a session; a frame's tenant is
// stamped when empty and refused when it names another tenant.
func TestRustMcpRoutedSessionHelpers(t *testing.T) {
	svc, _, _ := rustMcpRouteService()
	got := mustRustMcpRouteDispatch(t, svc)
	for _, want := range []string{
		"pub fn crew_service_mcp_session_tenant(caller: &servicemanager::auth::Caller) -> Result<String, tonic::Status> {",
		"servicemanager::tenant_route::tenant_of_caller_namespace(",
		"fn crew_service_mcp_stamp_frame_tenant(frame: &mut crate::types::mcp::McpRequest, session_tenant: &str) -> Result<(), tonic::Status> {",
		"if frame.tenant.is_empty() {",
		"frame.tenant = session_tenant.to_string();",
		"} else if frame.tenant != session_tenant {",
		"tonic::Status::permission_denied(",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("routed MCP dispatch is missing %q\ngot:\n%s", want, got)
		}
	}
}

// The carrier resolves the session tenant at open (failing closed), then on
// every frame: validate, stamp, and only then the routing backstop.
func TestRustMcpRoutedCarrierStampsBeforeTheRouteCheck(t *testing.T) {
	svc, _, carrier := rustMcpRouteService()
	got := generateRustMcpCarrierWrapper(svc, carrier, "crew", "services", map[string]string{})
	assertOrdered(t, got,
		`tonic::Status::unauthenticated("an MCP session requires an authenticated caller")`,
		"let __mcp_session_tenant = crew_service_mcp_session_tenant(&caller)?;",
		"msg.validate()",
		"crew_service_mcp_stamp_frame_tenant(&mut msg, &__mcp_session_tenant)?;",
		`__route_wrapper.check_route_tenant("tenant", &msg.tenant)?;`,
		"serve_crew_service_mcp_session(")
}

// The generated gate carries the properties with it.
func TestRustMcpRoutedDispatchGeneratesItsOwnTests(t *testing.T) {
	svc, _, _ := rustMcpRouteService()
	got := mustRustMcpRouteDispatch(t, svc)
	for _, want := range []string{
		`const MCP_TEST_TENANT: &str = "acme0001";`,
		`.set_namespace(format!("/tenants/{MCP_TEST_TENANT}/users/alice"))`,
		"async fn send_message_refuses_a_client_supplied_target_tenant()",
		"async fn send_message_takes_target_tenant_from_the_session()",
		`"target_tenant: \"acme0001\""`,
		"fn send_message_schema_omits_target_tenant()",
		"fn the_mcp_session_tenant_comes_from_the_token()",
		"fn an_mcp_frame_with_no_tenant_is_stamped()",
		"fn an_mcp_frame_naming_another_tenant_is_refused()",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated routed MCP tests are missing %q\ngot:\n%s", want, got)
		}
	}
	// The synthesized arguments never name the route field: that call would be refused.
	if strings.Contains(got, `\"target_tenant\": `) {
		t.Fatalf("the generated gate passes the route field as a tool argument:\n%s", got)
	}
}

// A routed tool whose carrier is not routed has no session tenant to take its
// route from; generation fails rather than dispatching it unrouted.
func TestRustMcpRoutedToolNeedsARoutedCarrier(t *testing.T) {
	svc, _, carrier := rustMcpRouteService()
	carrier.RouteField = nil
	_, err := generateRustMcpDispatch(svc, "crew", map[string]string{}, rustMcpRouteSanitizer())
	if err == nil || !strings.Contains(err.Error(), "route_field") {
		t.Fatalf("a routed tool on an unrouted carrier must fail generation, got %v", err)
	}
}

// A platform service's MCP surface carries none of it.
func TestRustMcpPlatformDispatchHasNoSessionTenant(t *testing.T) {
	svc, _, _, carrier := rustMcpTestService()
	got := mustRustMcpDispatch(t, svc) + generateRustMcpCarrierWrapper(svc, carrier, "ticket", "svc", map[string]string{})
	for _, unwanted := range []string{"session_tenant", "stamp_frame_tenant", "MCP_TEST_TENANT", "set_namespace", "check_route_tenant"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("a platform MCP surface carries %q:\n%s", unwanted, got)
		}
	}
}
