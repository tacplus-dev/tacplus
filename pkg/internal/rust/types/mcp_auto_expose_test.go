// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

// The dispatch end of auto-exposure: an rpc marked IsAutoMcp by the parser
// reaches the published tool set the same way a declared RPC_PRINCIPAL_MCP one
// does, and shares the handler method the gRPC wrapper already calls — which
// is what makes the two-argument dispatch call correct for it. The parser end
// is pkg/internal/common/parser/mcp_auto_expose_test.go.
func TestRustMcpAutoExposedRpcIsPublishedAsATool(t *testing.T) {
	svc, _, control, _ := rustMcpTestService()
	control.IsAutoMcp = true
	control.McpToolName = "get_ticket"
	control.Comment = "GetTicket returns one ticket by id."

	got := mustRustMcpDispatch(t, svc)

	for _, want := range []string{
		`name: "get_ticket"`,
		`description: "GetTicket returns one ticket by id."`,
		// The tool shares the gRPC handler: dispatch calls the same method the
		// service wrapper does, with the same two arguments.
		`self.handler.get_ticket(caller, req)`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("generated dispatch is missing %q", want)
		}
	}
}
