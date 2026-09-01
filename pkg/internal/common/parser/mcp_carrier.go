// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import "fmt"

// The session-carrying MCP transport.
//
// A service that declares at least one RPC_PRINCIPAL_MCP rpc (ServiceDescriptor
// .HasMcpRpcs) needs one authenticated, bidirectional channel over which an MCP
// client speaks to it — the tools themselves are not gRPC-callable, so without
// the carrier there is nothing to reach them through. That channel is never
// declared by a service author: it is a generation-time consequence of
// HasMcpRpcs, the same way the low-level dispatch machinery of an ordinary rpc
// is generated without a separate opt-in.
//
// Its exposure is not carried here — it is derived (union, most-permissive
// wins) from the constituent tools and already lives on the owning service as
// ServiceDescriptor.McpExposure / ServiceInfo.mcp_exposure, so the carrier reads
// it from the service rather than restating it.

// McpCarrierFrameType is the shape of one message on the carrier: a single
// complete JSON-RPC 2.0 frame, serialized, never fragmented across messages.
//
// It is the proto scalar `string`, not a message name, because no proto message
// is generated for the carrier in any language — naming one in registry.json
// would be a dangling reference to a type no generator emits. The deleted argus
// precedent declared its own McpRequest/McpResponse pair whose whole content was
// `string payload`; a generated carrier has no proto file of its own to declare
// that pair in, so it carries the payload type directly.
const McpCarrierFrameType = "string"

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
