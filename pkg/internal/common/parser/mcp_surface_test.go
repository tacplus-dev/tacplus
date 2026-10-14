// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import "testing"

// An RPC_PRINCIPAL_MCP rpc declares a tool, not a call, so it must not pick up
// the convention REST route every other rpc on an annotated service gets.
// HttpPath/HttpMethods staying empty is the whole suppression: every consumer
// of those two fields already reads "empty" as "no REST surface".
func TestParseMethodOptions_McpRpcGetsNoConventionRestPath(t *testing.T) {
	skipIfNoProtoc(t)

	fd := mcpFile(t, `
service Surface {
  option (spec.service_c2s) = {};

  // An MCP tool.
  rpc GetTicketBrief(Ping) returns (Ping) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_MCP};
  }

  // The paired control: an ordinary user call on the same service.
  rpc GetTicket(Ping) returns (Ping) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER};
  }
}
`)

	tool := mcpMethod(t, fd, "GetTicketBrief")
	if tool.HttpPath != "" {
		t.Errorf("GetTicketBrief: HttpPath = %q, want empty", tool.HttpPath)
	}
	if len(tool.HttpMethods) != 0 {
		t.Errorf("GetTicketBrief: HttpMethods = %v, want none", tool.HttpMethods)
	}
	if tool.HttpMethod != "" {
		t.Errorf("GetTicketBrief: HttpMethod = %q, want empty", tool.HttpMethod)
	}

	// Scoped to IsMcp only: the ordinary rpc on the same service is untouched.
	ctl := mcpMethod(t, fd, "GetTicket")
	if want := "/rpctest/Surface/GetTicket"; ctl.HttpPath != want {
		t.Errorf("GetTicket: HttpPath = %q, want %q", ctl.HttpPath, want)
	}
	if len(ctl.HttpMethods) == 0 {
		t.Error("GetTicket: HttpMethods is empty, want the convention POST route")
	}
}
