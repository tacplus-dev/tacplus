// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"strings"
	"testing"

	spec "gitlab.com/secondstarcavalier/schematic/tacplus/target/proto/spec/sys"
)

// Pins principal: RPC_PRINCIPAL_MCP end to end through the parser: the new enum
// value lands, the tool name and description default off the rpc itself, the
// override replaces only the name, and the per-service "needs a carrier" fact is
// derived rather than declared. The refusals below are the paired red halves.

// mcpFile parses protoBody and returns its single file descriptor.
func mcpFile(t *testing.T, protoBody string) *FileDescriptor {
	t.Helper()
	fd, err := rpcParser(t, protoBody).GetFileDescriptor("rpctest.proto")
	if err != nil {
		t.Fatalf("GetFileDescriptor: %v", err)
	}
	return fd
}

// mcpMethod returns the named method of the file's first service.
func mcpMethod(t *testing.T, fd *FileDescriptor, name string) *MethodDescriptor {
	t.Helper()
	for mi := range fd.Services[0].Methods {
		if fd.Services[0].Methods[mi].Name == name {
			return &fd.Services[0].Methods[mi]
		}
	}
	t.Fatalf("method %s missing from parsed descriptor", name)
	return nil
}

// ---------------------------------------------------------------------------
// The enum value itself.
// ---------------------------------------------------------------------------

// RPC_PRINCIPAL_MCP is a third value on an existing single-valued enum, which is
// what makes "MCP tool" and "gRPC call" mutually exclusive for free: the field
// holds one value, so one rpc block cannot be both.
func TestParseMethodOptions_RpcPrincipalMcp(t *testing.T) {
	skipIfNoProtoc(t)

	fd := mcpFile(t, `
service Surface {
  // Return one ticket by id.
  rpc GetTicket(Ping) returns (Ping) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_MCP};
  }

  // An ordinary service call on the same service.
  rpc Sync(Ping) returns (Ping) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE};
  }
}
`)

	tool := mcpMethod(t, fd, "GetTicket")
	if tool.Rpc == nil || tool.Rpc.Principal != spec.RpcPrincipal_RPC_PRINCIPAL_MCP {
		t.Fatalf("GetTicket: Principal = %v, want RPC_PRINCIPAL_MCP", tool.Rpc)
	}
	if !tool.IsMcp {
		t.Error("GetTicket: IsMcp = false, want true")
	}
	if grpc := mcpMethod(t, fd, "Sync"); grpc.IsMcp {
		t.Error("Sync: IsMcp = true on an RPC_PRINCIPAL_SERVICE rpc")
	}
}

// ---------------------------------------------------------------------------
// Name and description defaults.
// ---------------------------------------------------------------------------

// The name is the RPC name in MCP naming convention and the description is the
// rpc's own leading doc comment — no second, separately-maintained string that
// can drift from the real one.
func TestMcpToolNameAndDescriptionDefaultFromTheRpc(t *testing.T) {
	skipIfNoProtoc(t)

	fd := mcpFile(t, `
service Surface {
  // Return the advice attached to one ticket.
  rpc ListTicketAdvice(Ping) returns (Ping) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_MCP};
  }
}
`)

	m := mcpMethod(t, fd, "ListTicketAdvice")
	if m.McpToolName != "list_ticket_advice" {
		t.Errorf("McpToolName = %q, want %q", m.McpToolName, "list_ticket_advice")
	}
	// Description is the same MethodDescriptor.Comment every other generated doc
	// surface already reads; the registry emits it as RpcInfo.description.
	if !strings.Contains(m.Comment, "Return the advice attached to one ticket.") {
		t.Errorf("Comment = %q, want the rpc's own doc comment", m.Comment)
	}
}

// The override exists only for the rare RPC whose name makes a bad tool name.
// It replaces the name and nothing else.
func TestMcpToolNameOverrideReplacesOnlyTheName(t *testing.T) {
	skipIfNoProtoc(t)

	fd := mcpFile(t, `
service Surface {
  // Search tickets by free text.
  rpc GetTicketV2(Ping) returns (Ping) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_MCP, mcp_tool_name: "search_tickets"};
  }
}
`)

	m := mcpMethod(t, fd, "GetTicketV2")
	if m.McpToolName != "search_tickets" {
		t.Errorf("McpToolName = %q, want the declared override %q", m.McpToolName, "search_tickets")
	}
	if !strings.Contains(m.Comment, "Search tickets by free text.") {
		t.Errorf("Comment = %q; the override must not touch the description", m.Comment)
	}
}

// ---------------------------------------------------------------------------
// The per-service fact a later task consumes (design §9): the session-carrying
// transport is a consequence of having an MCP rpc, and its exposure is the
// most-permissive one across them — neither is ever declared.
// ---------------------------------------------------------------------------

func TestMcpServiceCarrierFactIsDerived(t *testing.T) {
	skipIfNoProtoc(t)

	fd := mcpFile(t, `
service Surface {
  // Internal tool.
  rpc GetTicket(Ping) returns (Ping) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_MCP};
  }

  // One externally-reachable tool is enough to pull the carrier outside.
  rpc ListTickets(Ping) returns (Ping) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_MCP, exposure: RPC_EXPOSURE_EXTERNAL};
  }
}

service Plain {
  rpc Sync(Ping) returns (Ping) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, exposure: RPC_EXPOSURE_EXTERNAL};
  }
}
`)

	byName := map[string]*ServiceDescriptor{}
	for si := range fd.Services {
		byName[fd.Services[si].Name] = &fd.Services[si]
	}
	surface, plain := byName["Surface"], byName["Plain"]
	if surface == nil || plain == nil {
		t.Fatalf("services = %v, want Surface and Plain", byName)
	}
	if !surface.HasMcpRpcs {
		t.Error("Surface: HasMcpRpcs = false, want true")
	}
	if surface.McpExposure != spec.RpcExposure_RPC_EXPOSURE_EXTERNAL {
		t.Errorf("Surface: McpExposure = %v, want RPC_EXPOSURE_EXTERNAL (the union)", surface.McpExposure)
	}
	if plain.HasMcpRpcs {
		t.Error("Plain: HasMcpRpcs = true on a service with no MCP rpc")
	}
	// An EXTERNAL non-MCP rpc must not widen a carrier that does not exist.
	if plain.McpExposure != spec.RpcExposure_RPC_EXPOSURE_INTERNAL {
		t.Errorf("Plain: McpExposure = %v, want the zero value", plain.McpExposure)
	}
}

// ---------------------------------------------------------------------------
// Positive control for the validator: the shape the annotation exists for must
// generate, or the refusals below would pass for the wrong reason.
// ---------------------------------------------------------------------------

func TestValidateMcpAcceptsUnaryTool(t *testing.T) {
	skipIfNoProtoc(t)

	p := rpcParser(t, `
service Surface {
  // Return one ticket by id.
  rpc GetTicket(Ping) returns (Ping) {
    option (spec.rpc) = {
      principal: RPC_PRINCIPAL_MCP,
      exposure: RPC_EXPOSURE_EXTERNAL,
      flags: [RPC_FLAG_AUTH_ENTITLED_USERS],
      mcp_tool_name: "get_ticket_by_id"
    };
  }

  // Streaming is only refused on the MCP principal; it stays legal everywhere else.
  rpc WatchTickets(Ping) returns (stream Ping) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE};
  }
}
`)
	if err := ValidateMcpAnnotations(p); err != nil {
		t.Fatalf("a unary MCP tool must generate: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Refusals.
// ---------------------------------------------------------------------------

// MCP-1: the tool-call wire format has no representation for a stream. This is
// unconditional — not a CollectionMode special case.
func TestValidateMcpRefusesStreaming(t *testing.T) {
	skipIfNoProtoc(t)

	for _, tc := range []struct {
		name string
		sig  string
		side string
	}{
		{"ServerStreaming", "rpc WatchTickets(Ping) returns (stream Ping)", "response"},
		{"ClientStreaming", "rpc WatchTickets(stream Ping) returns (Ping)", "request"},
		{"Bidi", "rpc WatchTickets(stream Ping) returns (stream Ping)", "request and response"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := rpcParser(t, `
service Surface {
  // Follow tickets as they change.
  `+tc.sig+` {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_MCP};
  }
}
`)
			err := ValidateMcpAnnotations(p)
			if err == nil {
				t.Fatal("a streaming RPC_PRINCIPAL_MCP rpc must fail generation")
			}
			if !strings.Contains(err.Error(), "no wire representation for a stream") {
				t.Fatalf("refusal must say why, got: %v", err)
			}
			if !strings.Contains(err.Error(), "its "+tc.side) {
				t.Fatalf("refusal must name the streaming side %q, got: %v", tc.side, err)
			}
		})
	}
}

// MCP-2: an annotation no generator consults is a lie about the contract.
func TestValidateMcpRefusesToolNameOnNonMcpRpc(t *testing.T) {
	skipIfNoProtoc(t)

	p := rpcParser(t, `
service Surface {
  // An ordinary service call carrying a tool name nothing reads.
  rpc Sync(Ping) returns (Ping) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, mcp_tool_name: "sync"};
  }
}
`)
	err := ValidateMcpAnnotations(p)
	if err == nil {
		t.Fatal("mcp_tool_name on a non-MCP rpc must fail generation")
	}
	if !strings.Contains(err.Error(), "nothing reads a tool name") {
		t.Fatalf("refusal must say nothing reads it, got: %v", err)
	}
}

// MCP-3: the override's whole job is to be the published tool name, and the
// default is snake_case by construction.
func TestValidateMcpRefusesNonSnakeCaseOverride(t *testing.T) {
	skipIfNoProtoc(t)

	p := rpcParser(t, `
service Surface {
  // Return one ticket by id.
  rpc GetTicket(Ping) returns (Ping) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_MCP, mcp_tool_name: "GetTicket"};
  }
}
`)
	err := ValidateMcpAnnotations(p)
	if err == nil {
		t.Fatal("a non-snake_case mcp_tool_name must fail generation")
	}
	if !strings.Contains(err.Error(), "is not an MCP tool name") {
		t.Fatalf("refusal must say the name is not a tool name, got: %v", err)
	}
}
