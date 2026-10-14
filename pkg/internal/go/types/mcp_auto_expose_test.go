// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

// The dispatch end of auto-exposure: an rpc marked IsAutoMcp by the parser
// reaches the published tool set the same way a declared RPC_PRINCIPAL_MCP one
// does, and the generated negative control stops claiming it is not a tool.
// The parser end is pkg/internal/common/parser/mcp_auto_expose_test.go.
func TestGoMcpAutoExposedRpcIsPublishedAsATool(t *testing.T) {
	svc := goMcpTestService()
	// svc.Methods[1] is GetTicket: an ordinary c2s call, not a declared tool.
	auto := svc.Methods[1]
	auto.IsAutoMcp = true
	auto.McpToolName = "get_ticket"
	auto.Comment = "GetTicket returns one ticket by id."

	tools, err := goMcpTools(svc, "ticket", goMcpTestSanitizer())
	if err != nil {
		t.Fatalf("resolve Go MCP tools: %v", err)
	}
	var names []string
	for _, tool := range tools {
		names = append(names, tool.ToolName)
	}
	if len(tools) != 2 {
		t.Fatalf("tools = %v, want the declared tool and the auto-exposed one", names)
	}
	var got *goMcpTool
	for i := range tools {
		if tools[i].ToolName == "get_ticket" {
			got = &tools[i]
		}
	}
	if got == nil {
		t.Fatalf("the auto-exposed rpc is not published: %v", names)
	}
	if got.Description != "GetTicket returns one ticket by id." {
		t.Errorf("description = %q; the tool description is the rpc's doc comment", got.Description)
	}

	// The auto-exposed rpc keeps its gRPC surface, so the generated dispatch
	// must not assert that it is absent from the tool table.
	rendered, err := generateGoMcpDispatch(svc, goMcpTestParams(), goMcpTestSanitizer())
	if err != nil {
		t.Fatalf("generate Go MCP dispatch: %v", err)
	}
	if strings.Contains(rendered, `"get_ticket"`) && strings.Contains(rendered, "was published as a tool") {
		idx := strings.Index(rendered, "was published as a tool")
		t.Errorf("the generated negative control still denies the auto-exposed tool:\n%s", rendered[max(0, idx-300):idx])
	}
}
