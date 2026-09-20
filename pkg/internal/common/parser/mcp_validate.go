// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	spec "gitlab.com/secondstarcavalier/schematic/tacplus/target/proto/spec/sys"
)

// The generation end of principal: RPC_PRINCIPAL_MCP.
//
// An MCP-principal rpc declares a tool, not a gRPC call. tacplus generates its
// schema and a handler interface method; it maps the tool to no gRPC RPC of its
// own. Authorization is untouched — flags / auth_services /
// target_namespace_field mean the same thing against the Caller an MCP session
// captured at handshake as they do against a gRPC caller — so there is nothing
// MCP-specific to validate there. What IS new is a wire format with no
// representation for a stream, and one optional string that only means something
// on this principal.
//
// This validator is shaped like ValidateDeletedFlagAnnotations: deterministic
// file ordering, errors accumulated, one aggregated error under a header.
//
//	MCP-1  RPC_PRINCIPAL_MCP with stream on the request or response -> fail
//	       Unconditional, and deliberately not expressed in terms of
//	       CollectionMode: the tool-call wire format has no representation for a
//	       stream, so this is not a policy choice to configure. It subsumes the
//	       WATCH case (stream-shaped by construction) without tacplus needing to
//	       know about CollectionMode at all. A handler is free to drive a Watch
//	       internally and fold it into one response.
//	MCP-2  mcp_tool_name on an rpc that is not RPC_PRINCIPAL_MCP     -> fail
//	       Nothing reads it there. Same posture as residency VAL-4: an
//	       annotation no generator consults is a lie about the contract, not a
//	       harmless extra.
//	MCP-3  mcp_tool_name that is not snake_case                      -> fail
//	       The override's whole job is to be the published tool name. The default
//	       is snake_case by construction; an override that is not would put a
//	       name on the wire that the convention says cannot appear.
//	MCP-4  a service with HasMcpRpcs but none of service_c2s/_s2s/_c2d -> fail
//	       The injected session carrier's auth model is the hosting service's
//	       (mcp_carrier.go: "Its auth model is the hosting service's"). A
//	       service declaring only RPC_PRINCIPAL_MCP rpcs and no service auth
//	       annotation has no auth model at all, so the carrier is generated,
//	       mounted and routed, but CollectRpcProtos never gives it auth_model
//	       "c2s" and it mints no entitlement grant — a channel every MCP tool
//	       call arrives on that no token can ever be authorized to call.
//	       Caught only by live verification otherwise: gates and cargo check
//	       are silent, since the carrier compiles and dispatches fine right up
//	       until the PermissionDenied at the entitlement check.
//	MCP-5  a service with HasMcpRpcs and service_c2d / auth_disabled -> fail
//	       The narrowing half of MCP-4: one of the three annotations must be
//	       set, but c2d and auth_disabled are not among the ones that work. The
//	       generated carrier opens a session by extracting a Caller from the
//	       request's bearer token and scopes every server-computed tool
//	       predicate in that session from it (mcp_dispatch_gen.go), yet neither
//	       of these paths runs an authorizer over that header first — a c2d
//	       service has no authorizer at all, its trust boundary being the unix
//	       socket's file permissions. The carrier would therefore hand every
//	       tool call an identity the client asserted in a token nothing
//	       verified, for the life of the session. Unlike the unary wrapper,
//	       which answers None where nothing was verified, the carrier has no
//	       "no identity" answer available: it requires a caller. So the shape
//	       is refused here rather than generated into a session that either
//	       trusts an unverified subject or is unauthenticated by construction.

// mcpToolNameRe is the MCP tool-name convention the default (snake_case of the
// RPC name) satisfies by construction, and the override must satisfy by hand.
var mcpToolNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ValidateMcpAnnotations enforces the invariants principal: RPC_PRINCIPAL_MCP
// needs in order to be generatable.
func ValidateMcpAnnotations(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	all := dp.GetAllFiles()
	paths := make([]string, 0, len(all))
	for path := range all {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	fds := make([]*FileDescriptor, 0, len(all))
	for _, path := range paths {
		fds = append(fds, all[path])
	}
	return validateMcpAnnotations(fds)
}

// validateMcpAnnotations is the file-list form of ValidateMcpAnnotations; fds
// must already be in a deterministic order so the error text is stable.
func validateMcpAnnotations(fds []*FileDescriptor) error {
	var errs []string
	for _, fd := range fds {
		for si := range fd.Services {
			svc := &fd.Services[si]

			// MCP-4.
			if svc.HasMcpRpcs && !svc.IsC2S && !svc.IsS2S && !svc.IsC2D {
				errs = append(errs, fmt.Sprintf(
					"%s.%s: declares an RPC_PRINCIPAL_MCP rpc but no service_c2s/service_s2s/"+
						"service_c2d annotation; the generated MCP session carrier's auth model "+
						"follows the hosting service's, so with none set it mints no entitlement "+
						"grant and no token can ever call it. Add option (spec.service_c2s) = {} "+
						"(or service_s2s) to the service; service_c2d is rejected separately by "+
						"MCP-5 below",
					fd.Package, svc.Name))
			}

			// MCP-5.
			if svc.HasMcpRpcs && (svc.IsC2D || svc.AuthDisabled) {
				errs = append(errs, fmt.Sprintf(
					"%s.%s: declares an RPC_PRINCIPAL_MCP rpc on a service_c2d/auth_disabled "+
						"service; the generated MCP session carrier extracts the session's Caller "+
						"from the request's bearer token and scopes every tool call in that session "+
						"from it, but neither path runs an authorizer over that header first (a c2d "+
						"service has no authorizer at all — its boundary is the socket's file "+
						"permissions), so the caller would be whatever subject the client asserted. "+
						"Host the MCP rpcs on a service_c2s or service_s2s service instead",
					fd.Package, svc.Name))
			}

			for mi := range svc.Methods {
				m := &svc.Methods[mi]
				if m.Rpc == nil {
					continue
				}
				isMcp := m.Rpc.Principal == spec.RpcPrincipal_RPC_PRINCIPAL_MCP

				// MCP-1.
				if isMcp && (m.ClientStreaming || m.ServerStreaming) {
					var sides []string
					if m.ClientStreaming {
						sides = append(sides, "request")
					}
					if m.ServerStreaming {
						sides = append(sides, "response")
					}
					errs = append(errs, fmt.Sprintf(
						"%s.%s/%s: declares principal RPC_PRINCIPAL_MCP and `stream` on its %s; a tool "+
							"call has no wire representation for a stream. Declare the rpc unary — a "+
							"handler may drive a Watch internally and fold it into one response — or "+
							"declare it under RPC_PRINCIPAL_USER/RPC_PRINCIPAL_SERVICE instead",
						fd.Package, svc.Name, m.Name, strings.Join(sides, " and ")))
				}

				override := m.Rpc.McpToolName
				if override == "" {
					continue
				}

				// MCP-2.
				if !isMcp {
					errs = append(errs, fmt.Sprintf(
						"%s.%s/%s: declares mcp_tool_name %q but principal is %s; nothing reads a tool "+
							"name on an rpc that is not RPC_PRINCIPAL_MCP. Drop it, or declare the rpc "+
							"as an MCP tool",
						fd.Package, svc.Name, m.Name, override, m.Rpc.Principal))
					continue
				}

				// MCP-3.
				if !mcpToolNameRe.MatchString(override) {
					errs = append(errs, fmt.Sprintf(
						"%s.%s/%s: mcp_tool_name %q is not an MCP tool name; it must match "+
							"[a-z][a-z0-9_]*, which is what the default (the RPC name in snake_case) "+
							"always is. Leave it unset unless the RPC name makes a bad tool name",
						fd.Package, svc.Name, m.Name, override))
				}
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("mcp annotation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
