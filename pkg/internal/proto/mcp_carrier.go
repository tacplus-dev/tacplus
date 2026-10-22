// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
)

// The session-carrying MCP transport, synthesized as a real proto rpc.
//
// An RPC_PRINCIPAL_MCP rpc declares a tool, not a gRPC call, so a service that
// hosts tools has nothing an MCP client can dial. The carrier is that one
// channel, and it is never declared by a service author: it is a generation-time
// consequence of ServiceDescriptor.HasMcpRpcs.
//
// It is injected into the service's OWN proto file, inside the service block,
// before protoc runs — not synthesized into the model afterwards. That is the
// whole point: an injected rpc is an ordinary rpc from there on, so it reaches
// tonic's generated trait, grpc-java's ImplBase, the auth wrapper, the REST
// dispatcher and the entitlement grant through the same machinery every
// hand-declared rpc uses, with no MCP-specific branch in any of them. Its auth
// model is the hosting service's (a c2s service's carrier is c2s, and mints the
// grant that follows), which is exactly how the hand-declared precedent
// (platform/services/argus's BrokerMcpSession) was authorized.
//
// Its frames are the shared sys types mcp.McpRequest / mcp.McpResponse — one
// complete JSON-RPC 2.0 frame per stream message. See pkg/proto/sys/mcp.proto.

const (
	// mcpFrameProtoImport is the sys proto declaring the carrier's frame types.
	mcpFrameProtoImport = "sys/mcp.proto"
	// mcpRequestFrameType / mcpResponseFrameType are those types, fully
	// qualified, as an injected rpc in another package must name them.
	mcpRequestFrameType  = "mcp.McpRequest"
	mcpResponseFrameType = "mcp.McpResponse"
)

// McpResponseFrameMaxBytes is the (spec.assert_max_size) bound on
// mcp.McpResponse.payload.
//
// A generated MCP transport has to know it: past this value the generated
// response validation fails the whole STREAM, killing the session over one
// large tool result, so the transport turns an over-size frame into a JSON-RPC
// error on that one request instead. sys/mcp.proto is embedded rather than
// parsed from the consumer's proto tree — its messages are stripped from the
// per-language message index as infra — so the value is stated here and pinned
// to the proto by TestMcpResponseFrameBoundMatchesProto.
const McpResponseFrameMaxBytes = 4000000

// AppendMcpCarrierRpcsToProtos injects, into every service that declares at
// least one RPC_PRINCIPAL_MCP rpc, the bidirectional session carrier
//
//	rpc Broker<Service>McpSession(stream mcp.McpRequest) returns (stream mcp.McpResponse)
//
// plus the sys/mcp.proto import that resolves its frames. Idempotent: a service
// that already declares an rpc of that name (hand-declared, or a second pass
// over an already-injected tree) is left alone.
//
// Returns the proto files it modified, so the caller can re-run the scans that
// enumerate a service's rpcs (the <Svc>RPCs enum) over the injected tree.
//
// inputDir is unused; the signature matches the other Append*ToProtos injectors
// so the call sites read alike.
func AppendMcpCarrierRpcsToProtos(tempProtoDir, inputDir string) ([]string, error) {
	_ = inputDir
	// Cheap text pre-check before the descriptor parse. A tree with no rpc that
	// could be a tool has no carrier to inject, and this injector must not be
	// the first thing to insist such a tree parses — every proto set that
	// generated before MCP existed still has to reach protoc exactly as it did.
	if hasMcp, err := protoDirMayDeclareMcpTools(tempProtoDir); err != nil {
		return nil, err
	} else if !hasMcp {
		return nil, nil
	}

	dp, err := parser.NewDescriptorParser(tempProtoDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create descriptor parser for MCP carrier injection: %w", err)
	}

	all := dp.GetAllFiles()
	relPaths := make([]string, 0, len(all))
	for relPath := range all {
		relPaths = append(relPaths, relPath)
	}
	sort.Strings(relPaths)

	var modified []string
	for _, relPath := range relPaths {
		if IsInfraProto(relPath) {
			continue
		}
		fd := all[relPath]
		if fd == nil {
			continue
		}

		// Services needing a carrier, in reverse order of their closing brace so
		// that inserting into a later service does not shift an earlier offset.
		type target struct {
			name     string
			exposure spec.RpcExposure
		}
		var targets []target
		for i := range fd.Services {
			svc := &fd.Services[i]
			if !svc.HasMcpRpcs {
				continue
			}
			carrier := parser.McpCarrierRpcName(svc.Name)
			declared := false
			for j := range svc.Methods {
				if svc.Methods[j].Name == carrier {
					declared = true
					break
				}
			}
			if declared {
				continue
			}
			targets = append(targets, target{name: svc.Name, exposure: svc.McpExposure})
		}
		if len(targets) == 0 {
			continue
		}

		filePath := filepath.Join(tempProtoDir, relPath)
		content, err := os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("read %s for MCP carrier injection: %w", relPath, err)
		}
		text := ensureImport(string(content), mcpFrameProtoImport)
		text = ensureImport(text, "sys/spec.proto")

		// Later services first: each insertion invalidates every offset after it.
		type placed struct {
			target
			at int
		}
		var placements []placed
		for _, t := range targets {
			at := findDeclClosingBrace(text, "service", t.name)
			if at < 0 {
				return nil, fmt.Errorf("MCP carrier injection: could not locate `service %s { ... }` in %s", t.name, relPath)
			}
			placements = append(placements, placed{target: t, at: at})
		}
		sort.Slice(placements, func(i, j int) bool { return placements[i].at > placements[j].at })
		for _, p := range placements {
			text = text[:p.at] + mcpCarrierRpcBlock(p.name, p.exposure) + text[p.at:]
		}

		if err := os.WriteFile(filePath, []byte(text), 0644); err != nil {
			return nil, fmt.Errorf("write %s after MCP carrier injection: %w", relPath, err)
		}
		modified = append(modified, relPath)
	}
	return modified, nil
}

// protoDirMayDeclareMcpTools reports whether any proto under dir mentions
// RPC_PRINCIPAL_MCP or spec.service_c2s — a declared tool, or a service whose
// user RPCs are auto-exposed as tools. Textual on purpose: it is a gate on
// doing the real work, not the decision about which service gets a carrier
// (that is the descriptor's).
func protoDirMayDeclareMcpTools(dir string) (bool, error) {
	found := false
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || found || !strings.HasSuffix(path, ".proto") {
			return err
		}
		relPath, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		if IsInfraProto(filepath.ToSlash(relPath)) {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(content, []byte("RPC_PRINCIPAL_MCP")) || bytes.Contains(content, []byte("spec.service_c2s")) {
			found = true
		}
		return nil
	})
	if err != nil {
		return false, fmt.Errorf("scan %s for MCP rpcs: %w", dir, err)
	}
	return found, nil
}

// mcpCarrierRpcBlock renders the carrier's proto declaration for serviceName,
// indented to sit inside a service block.
//
// The (spec.rpc) annotation is declared rather than inherited: the carrier is
// called by a real user session, so RPC_PRINCIPAL_USER with
// RPC_FLAG_AUTH_ENTITLED_USERS states the posture on the rpc instead of leaving
// it to be read off the service. exposure mirrors the service's derived
// McpExposure — the union of its tools' — because the carrier has to be
// reachable from wherever its most-exposed tool is called; it is derived here
// from the same union rather than declared twice, so it cannot drift.
func mcpCarrierRpcBlock(serviceName string, exposure spec.RpcExposure) string {
	var b strings.Builder
	b.WriteString("\n")
	for _, line := range strings.Split(parser.McpCarrierDescription(serviceName), "\n") {
		if line == "" {
			b.WriteString("  //\n")
			continue
		}
		b.WriteString("  // " + line + "\n")
	}
	fmt.Fprintf(&b, "  rpc %s(stream %s) returns (stream %s) {\n",
		parser.McpCarrierRpcName(serviceName), mcpRequestFrameType, mcpResponseFrameType)
	b.WriteString("    option (spec.rpc) = {\n")
	b.WriteString("      principal: RPC_PRINCIPAL_USER,\n")
	fmt.Fprintf(&b, "      exposure: %s,\n", exposure.String())
	b.WriteString("      flags: [RPC_FLAG_AUTH_ENTITLED_USERS]\n")
	b.WriteString("    };\n")
	b.WriteString("  }\n")
	return b.String()
}
