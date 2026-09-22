// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"strings"
	"testing"
)

// Pins the auto-exposure predicate (decision of 2026-09-22): a user RPC of a
// c2s service is an MCP tool with no annotation asking for one. The red halves
// below are the shapes the predicate deliberately does not sweep in —
// RPC_PRINCIPAL_SERVICE, the two no-auth flags, and the streaming and
// non-PAGE-collection shapes a tool call has no frame for. PAGE is swept in:
// see TestAutoExpose_PageCollectionIsATool.

// autoExposeFile parses one c2s service body and returns its descriptor.
func autoExposeFile(t *testing.T, serviceBody string) *FileDescriptor {
	t.Helper()
	return mcpFile(t, `
service Surface {
  option (spec.service_c2s) = {auth_entitled_users: true};
`+serviceBody+`
}
`)
}

// The headline case: an rpc with NO (spec.rpc) at all, user-facing only by the
// service's auth_entitled_users. This is how most of the fleet writes a user
// RPC, and before this change it produced no tool at all.
func TestAutoExpose_UnannotatedC2SRpcIsATool(t *testing.T) {
	skipIfNoProtoc(t)

	fd := autoExposeFile(t, `
  // Return one ticket by id.
  rpc GetTicket(Ping) returns (Ping);
`)

	m := mcpMethod(t, fd, "GetTicket")
	if !m.IsAutoMcp {
		t.Error("GetTicket: IsAutoMcp = false, want true")
	}
	if !m.IsMcpTool() {
		t.Error("GetTicket: IsMcpTool() = false, want true")
	}
	// Auto-exposure is additive: the rpc keeps its gRPC and REST surface.
	if m.IsMcp {
		t.Error("GetTicket: IsMcp = true; auto-exposure must not suppress the gRPC call")
	}
	if m.HttpPath == "" {
		t.Error("GetTicket: HttpPath is empty; the auto-exposed rpc lost its REST route")
	}
	if m.McpToolName != "get_ticket" {
		t.Errorf("GetTicket: McpToolName = %q, want %q", m.McpToolName, "get_ticket")
	}
	if !fd.Services[0].HasMcpRpcs {
		t.Error("Surface: HasMcpRpcs = false; the service owning a tool needs a carrier")
	}
}

// An explicit RPC_PRINCIPAL_USER rpc is the same case, said out loud.
func TestAutoExpose_ExplicitUserPrincipalIsATool(t *testing.T) {
	skipIfNoProtoc(t)

	fd := autoExposeFile(t, `
  // Return one ticket by id.
  rpc GetTicket(Ping) returns (Ping) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER, flags: [RPC_FLAG_AUTH_ENTITLED_USERS]};
  }
`)

	if m := mcpMethod(t, fd, "GetTicket"); !m.IsAutoMcp {
		t.Error("GetTicket: IsAutoMcp = false, want true")
	}
}

// RPC_PRINCIPAL_SERVICE is the caller the predicate names as never a tool: an
// s2s call on a c2s service is not a user RPC.
func TestAutoExpose_ServicePrincipalIsNotATool(t *testing.T) {
	skipIfNoProtoc(t)

	fd := autoExposeFile(t, `
  // An internal call that happens to live on this service.
  rpc Sync(Ping) returns (Ping) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: ["*"]};
  }
`)

	m := mcpMethod(t, fd, "Sync")
	if m.IsAutoMcp {
		t.Error("Sync: IsAutoMcp = true, want false for RPC_PRINCIPAL_SERVICE")
	}
	if m.McpToolName != "" {
		t.Errorf("Sync: McpToolName = %q, want empty", m.McpToolName)
	}
	if fd.Services[0].HasMcpRpcs {
		t.Error("Surface: HasMcpRpcs = true; a service whose only rpc is s2s owns no tool")
	}
}

// The two no-auth flags. RPC_FLAG_PUBLIC_NO_AUTH has no caller identity to
// scope a tool call by; RPC_FLAG_SELF_AUTHENTICATES is every token-minting rpc
// in the fleet, and a credential minter is never a model-callable tool.
func TestAutoExpose_NoAuthFlagsAreNotTools(t *testing.T) {
	skipIfNoProtoc(t)

	for _, tc := range []struct{ name, flag string }{
		{"ListJwks", "RPC_FLAG_PUBLIC_NO_AUTH"},
		{"MintToken", "RPC_FLAG_SELF_AUTHENTICATES"},
	} {
		t.Run(tc.flag, func(t *testing.T) {
			fd := autoExposeFile(t, `
  // A public endpoint.
  rpc `+tc.name+`(Ping) returns (Ping) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER, flags: [`+tc.flag+`]};
  }
`)
			if m := mcpMethod(t, fd, tc.name); m.IsAutoMcp {
				t.Errorf("%s: IsAutoMcp = true, want false for %s", tc.name, tc.flag)
			}
			if fd.Services[0].HasMcpRpcs {
				t.Errorf("Surface: HasMcpRpcs = true; %s owns no tool", tc.flag)
			}
		})
	}
}

// A streaming rpc is excluded rather than refused: auto-exposure is not a
// request the author made, and the generated session carrier is itself a bidi
// rpc on its own service — refusing here would make every c2s service that has
// one ungeneratable. MCP-1 still refuses a DECLARED streaming tool.
func TestAutoExpose_StreamingIsExcludedNotRefused(t *testing.T) {
	skipIfNoProtoc(t)

	fd := autoExposeFile(t, `
  // Stream events to the caller.
  rpc WatchTickets(Ping) returns (stream Ping);
`)

	if m := mcpMethod(t, fd, "WatchTickets"); m.IsAutoMcp {
		t.Error("WatchTickets: IsAutoMcp = true; a streaming rpc has no tool-call frame")
	}
	if err := validateMcpAnnotations([]*FileDescriptor{fd}, "rust"); err != nil {
		t.Errorf("a c2s streaming rpc must not fail validation: %v", err)
	}
}

// PAGE IS auto-exposed (phase 2): page_size and page_token are ordinary fields
// of a PAGE request, so they are ordinary tool arguments, and the dispatch calls
// the same three-argument handler the gRPC PAGE wrapper calls.
func TestAutoExpose_PageCollectionIsATool(t *testing.T) {
	skipIfNoProtoc(t)

	fd := autoExposeFile(t, `
  // List tickets, a page at a time.
  rpc ListTickets(Ping) returns (Ping) {
    option (spec.method_collection) = {mode: COLLECTION_MODE_PAGE, order_by: ["m"], default_page_size: 50};
  }
`)

	m := mcpMethod(t, fd, "ListTickets")
	if !m.IsAutoMcp {
		t.Error("ListTickets: IsAutoMcp = false; a PAGE read is auto-exposed as a paged tool")
	}
	if m.McpToolName != "list_tickets" {
		t.Errorf("ListTickets: McpToolName = %q, want list_tickets", m.McpToolName)
	}
	if !fd.Services[0].HasMcpRpcs {
		t.Error("Surface: HasMcpRpcs = false; a PAGE tool is still a tool")
	}
}

// SNAPSHOT is NOT widened with PAGE. Its handler returns a materialized
// collection (or a cursor, or a stream), none of which this dispatch can call,
// and no decision asked for it — so it stays excluded and the per-language
// dispatch guards keep refusing a DECLARED tool that carries one.
func TestAutoExpose_SnapshotCollectionIsExcluded(t *testing.T) {
	skipIfNoProtoc(t)

	fd := autoExposeFile(t, `
  // List every ticket at once.
  rpc ListAllTickets(Ping) returns (Ping) {
    option (spec.method_collection) = {mode: COLLECTION_MODE_SNAPSHOT, order_by: ["m"], max_items: 100};
  }
`)

	if m := mcpMethod(t, fd, "ListAllTickets"); m.IsAutoMcp {
		t.Error("ListAllTickets: IsAutoMcp = true; only PAGE collections are auto-exposed")
	}
}

// A declared RPC_PRINCIPAL_MCP tool with no gRPC twin keeps working exactly as
// before: IsMcp, no REST route, the tool name off the rpc, and NOT IsAutoMcp.
func TestAutoExpose_DeclaredMcpToolIsUnchanged(t *testing.T) {
	skipIfNoProtoc(t)

	fd := autoExposeFile(t, `
  // Summarize the ticket queue for a model.
  rpc SummarizeQueue(Ping) returns (Ping) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_MCP};
  }
`)

	m := mcpMethod(t, fd, "SummarizeQueue")
	if !m.IsMcp {
		t.Error("SummarizeQueue: IsMcp = false, want true")
	}
	if m.IsAutoMcp {
		t.Error("SummarizeQueue: IsAutoMcp = true; a declared tool is not also auto-exposed")
	}
	if m.HttpPath != "" {
		t.Errorf("SummarizeQueue: HttpPath = %q; a declared tool maps to no gRPC/REST call", m.HttpPath)
	}
	if m.McpToolName != "summarize_queue" {
		t.Errorf("SummarizeQueue: McpToolName = %q", m.McpToolName)
	}
}

// An s2s service auto-exposes nothing: the predicate is c2s-only.
func TestAutoExpose_S2SServiceOwnsNoTools(t *testing.T) {
	skipIfNoProtoc(t)

	fd := mcpFile(t, `
service Surface {
  option (spec.service_s2s) = {auth_services: ["*"]};

  // An internal call.
  rpc Sync(Ping) returns (Ping);
}
`)

	if m := mcpMethod(t, fd, "Sync"); m.IsAutoMcp {
		t.Error("Sync: IsAutoMcp = true on an s2s service")
	}
}

// MCP-2 used to refuse mcp_tool_name on anything but RPC_PRINCIPAL_MCP. On an
// auto-exposed rpc the override is now read, so it is accepted there and still
// refused on an rpc that publishes no tool.
func TestAutoExpose_McpToolNameOverride(t *testing.T) {
	skipIfNoProtoc(t)

	fd := autoExposeFile(t, `
  // Return one ticket by id.
  rpc GetTicket(Ping) returns (Ping) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER, mcp_tool_name: "fetch_ticket"};
  }

  // An internal call that may not name a tool.
  rpc Sync(Ping) returns (Ping) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: ["*"], mcp_tool_name: "sync_it"};
  }
`)

	if m := mcpMethod(t, fd, "GetTicket"); m.McpToolName != "fetch_ticket" {
		t.Errorf("GetTicket: McpToolName = %q, want %q", m.McpToolName, "fetch_ticket")
	}
	err := validateMcpAnnotations([]*FileDescriptor{fd}, "go")
	if err == nil {
		t.Fatal("mcp_tool_name on an RPC_PRINCIPAL_SERVICE rpc was accepted")
	}
	if !strings.Contains(err.Error(), "Sync") || !strings.Contains(err.Error(), "publishes no tool") {
		t.Errorf("MCP-2 error does not name the offender: %v", err)
	}
	if strings.Contains(err.Error(), "GetTicket") {
		t.Errorf("MCP-2 refused the override on an auto-exposed rpc: %v", err)
	}
}

// MCP-6: a language that generates a service wrapper but has no MCP dispatch
// backend must not silently host a service that owns tools.
func TestAutoExpose_Mcp6LanguageGate(t *testing.T) {
	skipIfNoProtoc(t)

	fd := autoExposeFile(t, `
  // Return one ticket by id.
  rpc GetTicket(Ping) returns (Ping);
`)

	for _, lang := range []string{"go", "java", "rust", "swift", "csharp", ""} {
		if err := validateMcpAnnotations([]*FileDescriptor{fd}, lang); err != nil {
			t.Errorf("language %q: unexpected MCP-6 failure: %v", lang, err)
		}
	}
	for _, lang := range []string{"python", "typescript"} {
		err := validateMcpAnnotations([]*FileDescriptor{fd}, lang)
		if err == nil {
			t.Errorf("language %q: a host with no MCP backend was accepted", lang)
			continue
		}
		if !strings.Contains(err.Error(), "no MCP dispatch backend") || !strings.Contains(err.Error(), "Surface") {
			t.Errorf("language %q: unhelpful MCP-6 error: %v", lang, err)
		}
	}
}
