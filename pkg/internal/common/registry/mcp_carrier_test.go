// Copyright (c) 2026 Second Star Cavalier LLC

package registry

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	spec "gitlab.com/secondstarcavalier/schematic/tacplus/target/proto/spec/sys"
)

// mcpCarrierControlFds is the paired control: an ordinary c2s service with no
// MCP rpc at all, which must get no carrier.
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
							HttpPath:    "/api/v1/scotty/Scotty/GetTicket",
							HttpMethods: []string{"POST"},
						},
					},
				},
			},
		},
	}
}

// A service that hosts MCP tools gets exactly one generated session carrier,
// bidirectional, framed as JSON-RPC text — and it is not itself a tool.
func TestCollectRpcProtosSynthesizesMcpSessionCarrier(t *testing.T) {
	rpcs, err := CollectRpcProtos(mcpSurfaceFds())
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
	if c.GetRpc() != "BrokerScottyMcpSession" {
		t.Errorf("rpc = %q, want BrokerScottyMcpSession", c.GetRpc())
	}
	if c.GetServiceName() != "Scotty" {
		t.Errorf("service_name = %q, want Scotty", c.GetServiceName())
	}
	if !c.GetClientStreaming() || !c.GetServerStreaming() {
		t.Errorf("client_streaming = %v, server_streaming = %v, want both true",
			c.GetClientStreaming(), c.GetServerStreaming())
	}
	if c.GetInputType() != "string" || c.GetOutputType() != "string" {
		t.Errorf("input/output type = %q/%q, want string/string — one JSON-RPC frame per message",
			c.GetInputType(), c.GetOutputType())
	}
	if c.GetMcp() || c.GetMcpToolName() != "" {
		t.Errorf("carrier claims to be a tool: mcp = %v, mcp_tool_name = %q", c.GetMcp(), c.GetMcpToolName())
	}
	if !strings.Contains(c.GetDescription(), "Model Context Protocol") {
		t.Errorf("description does not describe the carrier: %q", c.GetDescription())
	}
	// It is not a declared rpc, so it publishes no call surface — same as the
	// tools it serves.
	if c.GetGrpcPath() != "" || c.GetHttpPath() != "" || len(c.GetSurfaces()) != 0 || c.GetAuthModel() != "" {
		t.Errorf("carrier leaked a call surface: grpc_path=%q http_path=%q surfaces=%d auth_model=%q",
			c.GetGrpcPath(), c.GetHttpPath(), len(c.GetSurfaces()), c.GetAuthModel())
	}
}

// The carrier is a consequence of HasMcpRpcs and nothing else: a service with no
// MCP rpc gets none.
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
}

// The carrier has no grpc_path, so the by-prefix grouping that assembles a
// ServiceInfo's rpcs must still place it on the service that hosts it.
func TestRpcInfosForServiceIncludesCarrier(t *testing.T) {
	rpcs, err := CollectRpcProtos(mcpSurfaceFds())
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

// The carrier mints no entitlement grant: it carries no auth_model, the same
// gate the tools rely on.
func TestCollectEntitlementGrantsSkipsMcpCarrier(t *testing.T) {
	rpcs, err := CollectRpcProtos(mcpSurfaceFds())
	if err != nil {
		t.Fatalf("CollectRpcProtos returned error: %v", err)
	}
	root := &spec.PackageNode{
		Services: []*spec.ServiceInfo{{Name: "Scotty", Package: "scotty", Rpcs: rpcs}},
	}
	for _, g := range collectEntitlementGrants(root) {
		if g.GetRpc() == "BrokerScottyMcpSession" {
			t.Errorf("MCP session carrier minted an entitlement grant: %s", g.GetToken())
		}
	}
}
