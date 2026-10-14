// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
)

// mcpSurfaceFds is one c2s service carrying an MCP tool and, as the paired
// control, an ordinary rpc of the same verb shape.
func mcpSurfaceFds() []*parser.FileDescriptor {
	return []*parser.FileDescriptor{
		{
			Package: "scotty",
			Services: []parser.ServiceDescriptor{
				{
					Name:       "Scotty",
					IsC2S:      true,
					HasMcpRpcs: true,
					Methods: []parser.MethodDescriptor{
						{
							Name:        "GetTicketBrief",
							InputType:   "GetTicketRequest",
							OutputType:  "GetTicketResponse",
							IsMcp:       true,
							McpToolName: "get_ticket_brief",
						},
						{
							Name:        "GetTicket",
							InputType:   "GetTicketRequest",
							OutputType:  "GetTicketResponse",
							HttpPath:    "/scotty/Scotty/GetTicket",
							HttpMethods: []string{"POST"},
						},
					},
				},
			},
		},
	}
}

func mcpSurfaceRpc(t *testing.T, rpcs []*spec.RpcInfo, name string) *spec.RpcInfo {
	t.Helper()
	for _, r := range rpcs {
		if r.GetRpc() == name {
			return r
		}
	}
	t.Fatalf("rpc %s missing from collected registry entries", name)
	return nil
}

// The registry entry for an MCP tool keeps the tool's identity and drops every
// field that would publish it as a gRPC or REST call. The design is explicit
// that tacplus maps an MCP tool to no gRPC RPC at all.
func TestCollectRpcProtosMcpRpcHasNoCallSurface(t *testing.T) {
	rpcs, err := CollectRpcProtos(mcpSurfaceFds())
	if err != nil {
		t.Fatalf("CollectRpcProtos returned error: %v", err)
	}

	tool := mcpSurfaceRpc(t, rpcs, "GetTicketBrief")
	if !tool.GetMcp() || tool.GetMcpToolName() != "get_ticket_brief" {
		t.Fatalf("mcp = %v, mcp_tool_name = %q, want true / get_ticket_brief", tool.GetMcp(), tool.GetMcpToolName())
	}
	if tool.GetGrpcPath() != "" {
		t.Errorf("grpc_path = %q, want empty", tool.GetGrpcPath())
	}
	if tool.GetHttpPath() != "" || len(tool.GetHttpMethods()) != 0 {
		t.Errorf("http_path = %q, http_methods = %v, want none", tool.GetHttpPath(), tool.GetHttpMethods())
	}
	if len(tool.GetSurfaces()) != 0 {
		t.Errorf("surfaces = %v, want none", tool.GetSurfaces())
	}
	if tool.GetAuthModel() != "" {
		t.Errorf("auth_model = %q, want empty — it is what gates the entitlement grant", tool.GetAuthModel())
	}
	if tool.GetRpcVerb() != "" || tool.GetCapabilityScope() != "" || len(tool.GetEntitlementScopes()) != 0 {
		t.Errorf("verb classification leaked: verb=%q capability=%q scopes=%v",
			tool.GetRpcVerb(), tool.GetCapabilityScope(), tool.GetEntitlementScopes())
	}

	// Scoped to IsMcp only: the ordinary rpc on the same service still gets the
	// whole call surface.
	ctl := mcpSurfaceRpc(t, rpcs, "GetTicket")
	if ctl.GetGrpcPath() != "/scotty.Scotty/GetTicket" {
		t.Errorf("control grpc_path = %q, want /scotty.Scotty/GetTicket", ctl.GetGrpcPath())
	}
	if ctl.GetAuthModel() != "c2s" || ctl.GetCapabilityScope() != "READ" {
		t.Errorf("control auth_model = %q, capability_scope = %q, want c2s / READ", ctl.GetAuthModel(), ctl.GetCapabilityScope())
	}
	if len(ctl.GetSurfaces()) == 0 {
		t.Error("control has no surfaces")
	}
}

// The grant catalog is built from the RpcInfos above, so an MCP tool must not
// mint an entitlement grant — an external grant for something that is not
// gRPC-callable is the leak this suppression closes.
func TestCollectEntitlementGrantsSkipsMcpRpc(t *testing.T) {
	rpcs, err := CollectRpcProtos(mcpSurfaceFds())
	if err != nil {
		t.Fatalf("CollectRpcProtos returned error: %v", err)
	}
	root := &spec.PackageNode{
		Services: []*spec.ServiceInfo{{Name: "Scotty", Package: "scotty", Rpcs: rpcs}},
	}

	var tokens []string
	for _, g := range collectEntitlementGrants(root) {
		tokens = append(tokens, g.GetToken())
		if g.GetRpc() == "GetTicketBrief" {
			t.Errorf("MCP tool minted an entitlement grant: %s", g.GetToken())
		}
	}
	var sawControl bool
	for _, tok := range tokens {
		if tok == "SCOTTY_SCOTTY_GET_TICKET" {
			sawControl = true
		}
	}
	if !sawControl {
		t.Errorf("control rpc lost its grant; tokens = %v", tokens)
	}
}
