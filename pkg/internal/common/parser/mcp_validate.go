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
// This validator is shaped like ValidateEjectionableAnnotations: deterministic
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
//	       Nothing reads it there. Same posture as ejectionable EJ-4: an
//	       annotation no generator consults is a lie about the contract, not a
//	       harmless extra.
//	MCP-3  mcp_tool_name that is not snake_case                      -> fail
//	       The override's whole job is to be the published tool name. The default
//	       is snake_case by construction; an override that is not would put a
//	       name on the wire that the convention says cannot appear.

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
