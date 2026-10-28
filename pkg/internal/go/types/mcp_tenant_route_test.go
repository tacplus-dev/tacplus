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

// The MCP surface of a tenant-routed service (issue #170). The model must never
// be able to set the tenant: the session's tenant comes from its token, the
// carrier stamps it on every frame and refuses a frame naming another, and a
// tool's own route field is left out of its schema, refused as an argument and
// filled from the session tenant. The platform service of goMcpTestService is
// the paired control.

// goMcpTenantTestSanitizer is goMcpTestSanitizer with a route field on the
// tool's request.
func goMcpTenantTestSanitizer() *egressSanitizer {
	return newEgressSanitizer([]model.ProtoFile{{Package: "ticket", Messages: []model.Message{
		{Name: "GetTicketBriefRequest", Fields: []model.Field{
			{Name: "ticket_id", Type: "string", Number: 1,
				Annotations: map[string]model.AnnotationValue{"assert_required": {BoolValue: true}}},
			{Name: "target_tenant", Type: "string", Number: 2,
				Annotations: map[string]model.AnnotationValue{"assert_required": {BoolValue: true}}},
		}},
		{Name: "GetTicketBriefResponse", Fields: []model.Field{
			{Name: "summary", Type: "string", Number: 1},
		}},
		{Name: "GetTicketRequest", Fields: []model.Field{
			{Name: "ticket_id", Type: "string", Number: 1},
			{Name: "target_tenant", Type: "string", Number: 2},
		}},
		{Name: "GetTicketResponse", Fields: []model.Field{
			{Name: "body", Type: "string", Number: 1},
		}},
	}}})
}

// goMcpTenantTestService is goMcpTestService as a tenant-owned service: every
// rpc, the carrier included, declares its route field.
func goMcpTenantTestService() *parser.ServiceInfo {
	svc := goMcpTestService()
	for _, m := range svc.Methods {
		if m.Name == parser.McpCarrierRpcName(svc.Name) {
			m.RouteField = &parser.RouteFieldDescriptor{Field: "tenant", JSONName: "tenant", Number: 2}
			continue
		}
		m.RouteField = &parser.RouteFieldDescriptor{Field: "target_tenant", JSONName: "targetTenant", Number: 2}
	}
	return svc
}

func goMcpTenantRender(t *testing.T, svc *parser.ServiceInfo) string {
	t.Helper()
	rendered, err := generateGoMcpDispatch(svc, goMcpTestParams(), goMcpTenantTestSanitizer())
	if err != nil {
		t.Fatalf("generate Go MCP dispatch: %v", err)
	}
	return rendered
}

func goMcpToolSchemaProps(t *testing.T, svc *parser.ServiceInfo) map[string]any {
	t.Helper()
	tools, err := goMcpTools(svc, "ticket", goMcpTenantTestSanitizer())
	if err != nil {
		t.Fatalf("resolve MCP tools: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal([]byte(tools[0].Schema), &schema); err != nil {
		t.Fatalf("the generated schema is not JSON: %v\n%s", err, tools[0].Schema)
	}
	props, _ := schema["properties"].(map[string]any)
	return props
}

// The route field is not a shape of call the model can make: it is absent from
// the schema of a tenant-routed tool, and present in the platform control.
func TestGoMcpToolSchemaOmitsTheRouteField(t *testing.T) {
	if _, ok := goMcpToolSchemaProps(t, goMcpTenantTestService())["target_tenant"]; ok {
		t.Fatal("the route field is a tool argument of a tenant-routed tool")
	}
	tools, err := goMcpTools(goMcpTenantTestService(), "ticket", goMcpTenantTestSanitizer())
	if err != nil {
		t.Fatalf("resolve MCP tools: %v", err)
	}
	if strings.Contains(tools[0].Schema, "target_tenant") {
		t.Fatalf("the schema still names the route field (required?):\n%s", tools[0].Schema)
	}
	props := goMcpToolSchemaProps(t, goMcpTestService())
	if _, ok := props["target_tenant"]; !ok {
		t.Fatal("control: an ordinary field named target_tenant on a platform tool must stay in its schema")
	}
}

// A tool call naming the route field, by either of its names, is refused; the
// route field is filled from the session's tenant, before validation, so the
// request validated is the one dispatched.
func TestGoMcpDispatchFillsTheRouteFieldFromTheSessionTenant(t *testing.T) {
	rendered := goMcpTenantRender(t, goMcpTenantTestService())
	for _, want := range []string{
		`if named["target_tenant"] || named["targetTenant"] {`,
		"target_tenant is set by the server from your session's tenant; it is not a tool argument",
		"__tenant, err := ticketApiMcpSessionTenant(s.caller)",
		"req.TargetTenant = __tenant",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("tenant-routed tool dispatch lacks %q:\n%s", want, rendered)
		}
	}
	stamp := strings.Index(rendered, "req.TargetTenant = __tenant")
	decode := strings.Index(rendered, "protojson.Unmarshal(arguments, req)")
	if decode < 0 || stamp < decode {
		t.Fatalf("the route field is stamped before the arguments are decoded, where the decode would overwrite it:\n%s", rendered)
	}

	control := goMcpTenantRender(t, goMcpTestService())
	for _, absent := range []string{"SessionTenant", "StampFrameTenant", "req.TargetTenant ="} {
		if strings.Contains(control, absent) {
			t.Fatalf("control: a platform service's MCP dispatch carries %q:\n%s", absent, control)
		}
	}
}

// The session tenant is the tenant of the token's namespace claim; a caller
// with none cannot reach a tenant-routed tool, and a frame naming any other
// tenant is refused while an empty one is stamped.
func TestGoMcpSessionTenantComesFromTheToken(t *testing.T) {
	rendered := goMcpTenantRender(t, goMcpTenantTestService())
	for _, want := range []string{
		"func ticketApiMcpSessionTenant(caller *servicemanager.Caller) (string, error) {",
		"serviceconnection.NamespaceTenant(ns)",
		"codes.PermissionDenied",
		"func ticketApiMcpStampFrameTenant(caller *servicemanager.Caller, frame *mcp_pb.McpRequest) error {",
		"if frame.Tenant == \"\" {",
		"frame.Tenant = tid",
		"} else if frame.Tenant != tid {",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("tenant-routed MCP dispatch lacks %q:\n%s", want, rendered)
		}
	}
}

// The carrier's per-frame routing backstop stamps the frame from the token
// first: the generic check alone would refuse an empty tenant, and must never
// see one the platform did not set.
func TestGoMcpCarrierStampsEachFrameBeforeTheRoutingBackstop(t *testing.T) {
	svc := goMcpTenantTestService()
	var carrier *parser.MethodInfo
	for _, m := range svc.Methods {
		if m.Name == parser.McpCarrierRpcName(svc.Name) {
			carrier = m
		}
	}
	got := goStreamMessageRoute(svc, carrier, "mcp_pb.McpRequest", "TicketApi_BrokerTicketApiMcpSession_FullMethodName")
	want := "func(req *mcp_pb.McpRequest) error { if err := ticketApiMcpStampFrameTenant(caller, req); err != nil { return err }; return p.checkRoute(TicketApi_BrokerTicketApiMcpSession_FullMethodName, req) }"
	if got != want {
		t.Fatalf("carrier frame route = %s\nwant %s", got, want)
	}
	// Any other routed stream: the generic backstop alone.
	other := &parser.MethodInfo{Name: "Watch", ClientStreaming: true, RouteField: carrier.RouteField}
	if got := goStreamMessageRoute(svc, other, "X", "FM"); got != "func(req *X) error { return p.checkRoute(FM, req) }" {
		t.Fatalf("routed stream route = %s", got)
	}
}
