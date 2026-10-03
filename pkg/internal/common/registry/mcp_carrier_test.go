// Copyright (c) 2026 Second Star Cavalier LLC

package registry

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	spec "gitlab.com/seabeam/schematic/tacplus/target/proto/spec/sys"
)

// mcpCarrierFds is what the descriptor parser sees AFTER the proto injector has
// run (pkg/internal/proto/mcp_carrier.go): the carrier is a declared rpc on the
// hosting service, not a model-layer synthesis. Everything downstream of the
// parser therefore treats it as an ordinary rpc, which is the whole point.
func mcpCarrierFds() []*parser.FileDescriptor {
	fds := mcpSurfaceFds()
	svc := &fds[0].Services[0]
	svc.Methods = append(svc.Methods, parser.MethodDescriptor{
		Name:            parser.McpCarrierRpcName(svc.Name),
		Comment:         parser.McpCarrierDescription(svc.Name),
		InputType:       "mcp.McpRequest",
		OutputType:      "mcp.McpResponse",
		ClientStreaming: true,
		ServerStreaming: true,
	})
	return fds
}

// mcpCarrierControlFds is the paired control: an ordinary c2s service with no
// MCP rpc at all, which the injector leaves alone and which must therefore have
// no carrier anywhere in the registry.
func mcpCarrierControlFds() []*parser.FileDescriptor {
	return []*parser.FileDescriptor{
		{
			Package: "scotty",
			Services: []parser.ServiceDescriptor{
				{
					Name:  "Scotty",
					IsC2S: true,
					Methods: []parser.MethodDescriptor{
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

// The carrier is marked as the transport — not as a tool — and carries the real
// frame message types the injected declaration names.
func TestCollectRpcProtosMarksMcpSessionCarrier(t *testing.T) {
	rpcs, err := CollectRpcProtos(mcpCarrierFds())
	if err != nil {
		t.Fatalf("CollectRpcProtos returned error: %v", err)
	}

	var carriers []*spec.RpcInfo
	for _, r := range rpcs {
		if r.GetMcpSessionCarrier() {
			carriers = append(carriers, r)
		}
	}
	if len(carriers) != 1 {
		t.Fatalf("got %d session carriers, want exactly 1", len(carriers))
	}
	c := carriers[0]
	if c.GetRpc() != "BrokerScottyMcpSession" || c.GetServiceName() != "Scotty" {
		t.Errorf("carrier = %s.%s, want Scotty.BrokerScottyMcpSession", c.GetServiceName(), c.GetRpc())
	}
	if !c.GetClientStreaming() || !c.GetServerStreaming() {
		t.Errorf("client_streaming = %v, server_streaming = %v, want both true",
			c.GetClientStreaming(), c.GetServerStreaming())
	}
	if c.GetInputType() != "mcp.McpRequest" || c.GetOutputType() != "mcp.McpResponse" {
		t.Errorf("input/output type = %q/%q, want mcp.McpRequest/mcp.McpResponse",
			c.GetInputType(), c.GetOutputType())
	}
	if c.GetMcp() || c.GetMcpToolName() != "" {
		t.Errorf("carrier claims to be a tool: mcp = %v, mcp_tool_name = %q", c.GetMcp(), c.GetMcpToolName())
	}
	if !strings.Contains(c.GetDescription(), "Model Context Protocol") {
		t.Errorf("description does not describe the carrier: %q", c.GetDescription())
	}
}

// The carrier is a real gRPC method: it publishes the call surface every other
// declared rpc of its service does. This is exactly what the tools it serves do
// NOT get, and the difference is the reason the carrier exists.
func TestCollectRpcProtosMcpCarrierHasCallSurface(t *testing.T) {
	rpcs, err := CollectRpcProtos(mcpCarrierFds())
	if err != nil {
		t.Fatalf("CollectRpcProtos returned error: %v", err)
	}
	c := mcpSurfaceRpc(t, rpcs, "BrokerScottyMcpSession")

	if got, want := c.GetGrpcPath(), "/scotty.Scotty/BrokerScottyMcpSession"; got != want {
		t.Errorf("grpc_path = %q, want %q", got, want)
	}
	if got := c.GetAuthModel(); got != "c2s" {
		t.Errorf("auth_model = %q, want c2s — the carrier is the one thing an MCP client authenticates to", got)
	}
	if c.GetAuthDisabled() || c.GetAuthAnyUser() {
		t.Errorf("carrier is unauthenticated: auth_disabled = %v, auth_any_user = %v",
			c.GetAuthDisabled(), c.GetAuthAnyUser())
	}
	if len(c.GetSurfaces()) == 0 {
		t.Errorf("carrier publishes no surface at all")
	}
}

// The carrier mints an entitlement grant like any other c2s rpc. Without one
// nothing authorizes the single channel every MCP tool call arrives on.
func TestCollectEntitlementGrantsIncludesMcpCarrier(t *testing.T) {
	rpcs, err := CollectRpcProtos(mcpCarrierFds())
	if err != nil {
		t.Fatalf("CollectRpcProtos returned error: %v", err)
	}
	root := &spec.PackageNode{
		Services: []*spec.ServiceInfo{{Name: "Scotty", Package: "scotty", Rpcs: rpcs}},
	}
	var found *spec.EntitlementGrantInfo
	for _, g := range collectEntitlementGrants(root) {
		if g.GetRpc() == "BrokerScottyMcpSession" {
			found = g
		}
		if g.GetRpc() == "GetTicketBrief" {
			t.Errorf("MCP tool minted an entitlement grant: %s", g.GetToken())
		}
	}
	if found == nil {
		t.Fatal("MCP session carrier minted no entitlement grant")
	}
	if found.GetToken() == "" {
		t.Errorf("carrier grant has an empty token: %+v", found)
	}
}

// The carrier is a consequence of an RPC_PRINCIPAL_MCP rpc and nothing else: a
// service with no MCP rpc is never injected into, so no carrier reaches here.
func TestCollectRpcProtosNoCarrierWithoutMcpRpcs(t *testing.T) {
	rpcs, err := CollectRpcProtos(mcpCarrierControlFds())
	if err != nil {
		t.Fatalf("CollectRpcProtos returned error: %v", err)
	}
	if len(rpcs) != 1 || rpcs[0].GetRpc() != "GetTicket" {
		var names []string
		for _, r := range rpcs {
			names = append(names, r.GetRpc())
		}
		t.Fatalf("rpcs = %v, want exactly [GetTicket]", names)
	}
	if rpcs[0].GetMcpSessionCarrier() {
		t.Error("an ordinary rpc was marked as the MCP session carrier")
	}
}

// The carrier groups onto the service that hosts it, alongside its tools.
func TestRpcInfosForServiceIncludesCarrier(t *testing.T) {
	rpcs, err := CollectRpcProtos(mcpCarrierFds())
	if err != nil {
		t.Fatalf("CollectRpcProtos returned error: %v", err)
	}
	var got []string
	for _, r := range rpcInfosForService(rpcs, "scotty", "Scotty") {
		got = append(got, r.GetRpc())
	}
	want := map[string]bool{"GetTicketBrief": true, "GetTicket": true, "BrokerScottyMcpSession": true}
	if len(got) != len(want) {
		t.Fatalf("grouped rpcs = %v, want %d entries", got, len(want))
	}
	for _, name := range got {
		if !want[name] {
			t.Errorf("unexpected rpc %q grouped onto Scotty", name)
		}
	}
}
