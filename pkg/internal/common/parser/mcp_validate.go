// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"

	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
)

// The generation end of MCP tool exposure.
//
// There are two ways an rpc becomes a tool. An MCP-principal rpc DECLARES one:
// tacplus generates its schema and a handler interface method, and maps the
// tool to no gRPC RPC of its own. A user RPC of a c2s service is AUTO-EXPOSED
// as one (MethodDescriptor.IsAutoMcp): it keeps its gRPC call surface and the
// tool shares the handler the wrapper already calls. The rules below say which
// of the two they bind on. Authorization is untouched — flags / auth_services /
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
//	MCP-2  mcp_tool_name on an rpc that publishes no tool            -> fail
//	       Nothing reads it there — on an auto-exposed rpc it IS read and
//	       names the published tool, so only a non-tool is refused. Same posture as residency VAL-4: an
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
//	MCP-6  a service owning tools, generated for a language whose server
//	       surface has no MCP backend                                -> fail
//	       Carrier injection is a proto-text operation and so is
//	       language-blind: a service hosted in a language with no MCP
//	       dispatch generator would get an injected Broker<Service>McpSession
//	       rpc, a generated handler stub for it, and no dispatch at all — an
//	       empty MCP transport handed to the author to implement by hand,
//	       with every tool the service owns silently unserved. Go, Java and
//	       Rust have the backend; Python is gated as a host without one.
//	       TypeScript, Swift and C# are client-only targets that generate no
//	       wrapper at all — they cannot host, so they are not gated, and a
//	       typescript client (the webapp) of a tool-owning service generates
//	       its client surface as usual. Under auto-exposure this stopped being
//	       a corner: every c2s service owns tools, so a Python HOST of one has
//	       to say so loudly rather than generate a hole.
//	MCP-7  an rpc publishing a tool whose leading comment cannot serve as the
//	       tool's description                                        -> fail
//	       The description IS the rpc's doc comment — there is no second
//	       string to fall back on — and it is the only thing a model reads to
//	       decide whether to call the tool. An undescribed tool is therefore
//	       not a cosmetic gap: it is a tool the model either never calls or
//	       calls blind. Under auto-exposure nobody opts an rpc in, so the doc
//	       comment is the only place its author says what it does, and the
//	       omission has to be refused at generation rather than published.
//	       Three shapes fail: no comment, a comment with no letter or digit in
//	       it, and a comment built entirely of section dividers. The last one
//	       matters because m.Comment is GetLeadingComments(), so a `// --- Jobs
//	       -----` heading sitting above an rpc IS that rpc's leading comment
//	       and would otherwise be published verbatim as its description.
//	MCP-8  RPC_PRINCIPAL_MCP with requires_device / requires_device_when -> fail
//	       No language's MCP dispatch runs the device-approval gate, so the
//	       tool would execute with no approval — the annotation a promise
//	       nothing keeps. An auto-exposed rpc carrying the gate is excluded by
//	       mcpAutoExposed rather than refused, because nobody asked for that
//	       tool; a declared one was asked for, so it fails loudly here.

// mcpToolNameRe is the MCP tool-name convention the default (snake_case of the
// RPC name) satisfies by construction, and the override must satisfy by hand.
var mcpToolNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// mcpBackendLanguages are the generation targets with an MCP dispatch backend.
// A service owning tools is generatable for these and, per MCP-6, refused for a
// server-capable language outside the set.
var mcpBackendLanguages = map[string]bool{"go": true, "java": true, "rust": true}

// mcpHostLanguages are the generation targets that emit a service wrapper and
// can therefore HOST a service rather than only call one. Only these are gated
// by MCP-6: a client-only target (typescript, swift, csharp) never serves a
// tool, so it has no hole to fall into.
var mcpHostLanguages = map[string]bool{"go": true, "java": true, "rust": true, "python": true}

// McpToolDescription is the text the model reads to decide whether to call the
// tool: the rpc's own leading comment, flattened to one paragraph. Every
// language's dispatch generator resolves the description through here, so the
// string MCP-7 validates is the string that is published.
//
// There is deliberately no fallback. Substituting the RPC name for a missing
// comment — which each generator used to do locally — would make a tool with no
// description indistinguishable from one with a description that happens to be
// its name, and would silently mask MCP-7 rather than survive it.
func McpToolDescription(comment string) string {
	return strings.Join(strings.Fields(comment), " ")
}

// mcpRuleChars are the characters the fleet builds section rules out of.
const mcpRuleChars = "-=*#~_"

// mcpDividerLine reports whether line is a section divider rather than prose:
// it opens or closes with a run of three or more of the same rule character,
// which is the shape of `// --- Jobs -----`. Rule characters are all ASCII, so
// indexing bytes is safe here.
func mcpDividerLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	first := line[0]
	if strings.IndexByte(mcpRuleChars, first) >= 0 &&
		len(line)-len(strings.TrimLeft(line, string(first))) >= 3 {
		return true
	}
	last := line[len(line)-1]
	return strings.IndexByte(mcpRuleChars, last) >= 0 &&
		len(line)-len(strings.TrimRight(line, string(last))) >= 3
}

// mcpDescriptionUnusable reports whether comment cannot serve as a tool
// description. Three shapes cannot: empty or whitespace-only, carrying no
// letter or digit at all, and built entirely of section-divider lines. A
// comment mixing a divider line with real prose is usable — only a heading
// masquerading as a doc comment is refused.
func mcpDescriptionUnusable(comment string) bool {
	if strings.TrimSpace(comment) == "" {
		return true
	}
	hasWord := strings.IndexFunc(comment, func(r rune) bool {
		return unicode.IsLetter(r) || unicode.IsDigit(r)
	}) >= 0
	if !hasWord {
		return true
	}
	for _, line := range strings.Split(comment, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if !mcpDividerLine(line) {
			return false
		}
	}
	return true
}

// ValidateMcpAnnotations enforces the invariants MCP tool exposure needs in
// order to be generatable. language is the tacplus.yaml language of the
// generation being validated; it reaches here for MCP-6 and may be "" when the
// caller has no language in hand, which skips that rule alone.
func ValidateMcpAnnotations(dp *DescriptorParser, language string) error {
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
	return validateMcpAnnotations(fds, language)
}

// validateMcpAnnotations is the file-list form of ValidateMcpAnnotations; fds
// must already be in a deterministic order so the error text is stable.
func validateMcpAnnotations(fds []*FileDescriptor, language string) error {
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

			// MCP-6.
			if svc.HasMcpRpcs && language != "" && mcpHostLanguages[language] && !mcpBackendLanguages[language] {
				errs = append(errs, fmt.Sprintf(
					"%s.%s: owns MCP tools but is being generated for language %q, which has no MCP "+
						"dispatch backend; the session carrier would be injected into the proto and "+
						"stubbed on the handler with nothing to serve the tools it carries. Generate "+
						"this service's server surface in go, java or rust, or take the tools off it "+
						"(a service_c2s service auto-exposes its user RPCs, so that means moving the "+
						"rpcs, not annotating them)",
					fd.Package, svc.Name, language))
			}

			for mi := range svc.Methods {
				m := &svc.Methods[mi]

				// MCP-7. Ahead of the nil check below: the common
				// auto-exposed rpc carries no spec.rpc at all, and it is
				// exactly the rpc whose only description is its comment.
				if m.IsMcpTool() && mcpDescriptionUnusable(m.Comment) {
					errs = append(errs, fmt.Sprintf(
						"%s.%s/%s: publishes MCP tool %q with no usable description; a tool's "+
							"description is its leading doc comment and nothing else, and it is all "+
							"a model has to decide whether to call the tool. Write a comment above "+
							"the rpc saying what it does — a section divider such as "+
							"`// --- Jobs -----` is the rpc's leading comment too, and does not "+
							"count as one",
						fd.Package, svc.Name, m.Name, m.McpToolName))
				}

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

				// MCP-8.
				if isMcp && m.HasDeviceGate() {
					annotation := "requires_device"
					if m.RequiresDeviceWhen != nil {
						annotation = "requires_device_when"
					}
					errs = append(errs, fmt.Sprintf(
						"%s.%s/%s: declares principal RPC_PRINCIPAL_MCP and %s; the MCP tool dispatch "+
							"runs no device-approval gate, so the tool would execute without the approval "+
							"the annotation asks for. Declare the rpc under RPC_PRINCIPAL_USER, where the "+
							"gRPC wrapper runs the gate, or drop the annotation",
						fd.Package, svc.Name, m.Name, annotation))
				}

				override := m.Rpc.McpToolName
				if override == "" {
					continue
				}

				// MCP-2.
				if !m.IsMcpTool() {
					errs = append(errs, fmt.Sprintf(
						"%s.%s/%s: declares mcp_tool_name %q but the rpc publishes no tool (principal "+
							"is %s and it is not an auto-exposed user RPC of a c2s service); nothing "+
							"reads a tool name there. Drop it, or declare the rpc as an MCP tool",
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
