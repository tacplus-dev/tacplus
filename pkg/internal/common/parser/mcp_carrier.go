// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import "fmt"

// The session-carrying MCP transport — its NAME and DOC TEXT, which the proto
// injector and every consumer of the generated model have to agree on.
//
// A service that declares at least one RPC_PRINCIPAL_MCP rpc (ServiceDescriptor
// .HasMcpRpcs) needs one authenticated, bidirectional channel over which an MCP
// client speaks to it — the tools themselves are not gRPC-callable, so without
// the carrier there is nothing to reach them through. That channel is never
// declared by a service author: it is a generation-time consequence of
// HasMcpRpcs, the same way the low-level dispatch machinery of an ordinary rpc
// is generated without a separate opt-in.
//
// The carrier itself is not synthesized in the model. tacplus injects a real
// rpc declaration into the service's own proto before protoc runs
// (pkg/internal/proto/mcp_carrier.go), so from parse onward it is an ordinary
// rpc with an ordinary grpc_path, auth model, and entitlement grant. Its frames
// are the sys types mcp.McpRequest / mcp.McpResponse.
//
// Its exposure is not carried here — it is derived (union, most-permissive
// wins) from the constituent tools and already lives on the owning service as
// ServiceDescriptor.McpExposure / ServiceInfo.mcp_exposure, which is also what
// the injector stamps on the declaration.

// McpCarrierRpcName is the name of the carrier generated for serviceName.
func McpCarrierRpcName(serviceName string) string {
	return "Broker" + serviceName + "McpSession"
}

// McpCarrierDescription is the carrier's doc text, used as the registry entry's
// description and as the generated handler comment.
func McpCarrierDescription(serviceName string) string {
	return fmt.Sprintf(`%s carries one Model Context Protocol session for %s over a
single bidirectional stream: one complete JSON-RPC frame per stream message,
request frames in and response frames out, for the life of the session.

Generated, never declared: %s has at least one RPC_PRINCIPAL_MCP rpc, and this
is the only channel those tools are reachable through. Bidirectional because an
MCP session is stateful — it opens with an initialize handshake and holds the
negotiated protocol version after it — so a unary call would repeat that
handshake per tool call.`, McpCarrierRpcName(serviceName), serviceName, serviceName)
}
