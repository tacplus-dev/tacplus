// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

// The dispatch end of auto-exposure: an rpc marked IsAutoMcp by the parser
// reaches the published tool set the same way a declared RPC_PRINCIPAL_MCP one
// does. The parser end is pkg/internal/common/parser/mcp_auto_expose_test.go.
func TestJavaMcpAutoExposedRpcIsPublishedAsATool(t *testing.T) {
	svc := javaMcpTestService()
	var control *parser.MethodInfo
	for _, m := range svc.Methods {
		if m.Name == "GetTicket" {
			control = m
		}
	}
	if control == nil {
		t.Fatal("the java MCP test service no longer carries a non-tool control rpc")
	}
	control.IsAutoMcp = true
	control.McpToolName = "get_ticket"
	control.Comment = "GetTicket returns one ticket by id."

	tools, err := javaMcpTools(svc, "ticket", "Ticket",
		map[string]string{"mcp": "Mcp"}, map[string]string{}, javaMcpTestSanitizer())
	if err != nil {
		t.Fatalf("resolve Java MCP tools: %v", err)
	}
	var got *javaMcpTool
	for i := range tools {
		if tools[i].ToolName == "get_ticket" {
			got = &tools[i]
		}
	}
	if got == nil {
		t.Fatalf("the auto-exposed rpc is not published; tools = %d", len(tools))
	}
	if got.Description != "GetTicket returns one ticket by id." {
		t.Errorf("description = %q; the tool description is the rpc's doc comment", got.Description)
	}
}
