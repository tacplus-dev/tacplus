// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"path/filepath"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/collection"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/verbs"
	spec "gitlab.com/seabeam/schematic/tacplus/target/proto/spec/sys"
)

// ProtoInfo holds parsed information from a proto file
type ProtoInfo struct {
	Package           string
	Services          []*ServiceInfo
	Messages          []*MessageInfo
	FilePath          string
	CrossPackageTypes map[string]string // proto package -> directory for cross-package method types
	CrossPackageFiles map[string]string // proto package -> proto file for cross-package method types
}

// ServiceInfo represents a gRPC service definition
type ServiceInfo struct {
	Name              string
	Methods           []*MethodInfo
	AuthDisabled      bool     // service-level or method-derived auth_disabled
	AuthServices      []string // service-level S2S auth_services
	AuthEntitledUsers bool     // service-level C2S auth_entitled_users
	AuthAnyUser       bool     // method-derived C2S auth_any_user
	IsC2D             bool     // service_c2d: client-to-daemon local IPC
	C2DSocketPath     string   // service_c2d socket_path (required, no default)
	IsC2S             bool     // service_c2s: client-to-server (external) traffic
	IsS2S             bool     // service_s2s: service-to-service (internal) traffic
	IsSMB             bool     // smb: service instances use a black-box peer protocol
	SmbRoot           spec.SmbRoot
	Synthesized       bool   // tacplus-generated service surface
	SynthesizedKind   string // which generated surface: SynthesizedKindState/Store/RecordMap/OpaEval; "" when not synthesized or kind absent
	HasMcpRpcs        bool   // mirrors ServiceDescriptor.HasMcpRpcs: at least one rpc is an MCP tool, so this service gets the generated session carrier (see mcp_carrier.go)
}

// MethodInfo represents a gRPC method
type MethodInfo struct {
	Name                 string
	InputType            string
	OutputType           string
	ClientStreaming      bool
	ServerStreaming      bool
	FullInputType        string                        // Fully qualified input type
	FullOutputType       string                        // Fully qualified output type
	HttpMethod           string                        // Primary HTTP method for client generation (first GET if present, else POST)
	HttpMethods          []string                      // All HTTP methods (POST always included, plus rest_method extras)
	HttpPath             string                        // Convention REST path: {ROOT_PATH}/{package}/{MethodName}
	HttpAliasPath        string                        // Additional alias path from rest_path
	HttpBody             string                        // HTTP body ("*" or field name)
	AuthDisabled         bool                          // per-RPC auth_disabled (from spec.rpc RPC_FLAG_PUBLIC_NO_AUTH / RPC_FLAG_SELF_AUTHENTICATES)
	AuthAnyUser          bool                          // per-RPC auth_any_user (from spec.rpc RPC_FLAG_AUTH_ANY_USER)
	AuthServices         []string                      // per-RPC S2S auth_services override (from spec.rpc)
	AuthActors           []string                      // per-RPC auth_actors (from spec.rpc): delegate identities allowed to call on another principal's behalf, matched against the token's act claim
	RequiresDevice       bool                          // per-RPC requires_device (from spec.rpc): the device-only approval check gates this RPC
	RequiresDeviceWhen   *RequiresDeviceWhenDescriptor // per-RPC requires_device_when (from spec.rpc): the same check, gated on one request field's value; nil when absent
	TargetNamespaceField string                        // per-RPC target_namespace_field (from spec.rpc); ordinary methods name a request field, auth_any_user methods use an absolute namespace literal
	RestBrowser          *RestBrowserDescriptor        // method-level rest_browser (browser form target: form body / redirect); nil when absent
	Collection           *CollectionDescriptor         // method-level method_collection (collection-returning read); nil when absent
	Resolved             *collection.Resolved          // computed codegen view of Collection (mode/tier/bounds); nil when Collection is nil. Set by ResolveCollections.
	Class                *verbs.Classification         // resolved verb/pattern/access (archive/RPC_PATTERNS_AND_VERBS_PLAN.md §2). Set by ClassifyMethods; nil if classification failed (see ClassifyMethods for the reported error).
	MinGroupSize         int32                         // method-level (spec.min_group_size): the smallest result size this size-only RPC discloses; 0 when the record declares no threshold
	IsMcp                bool                          // mirrors MethodDescriptor.IsMcp: the rpc declares principal RPC_PRINCIPAL_MCP, so it is an MCP tool and gets no generated gRPC surface
	IsAutoMcp            bool                          // mirrors MethodDescriptor.IsAutoMcp: the rpc is a user RPC of a c2s service, so it is an MCP tool AND keeps its gRPC surface
	McpToolName          string                        // mirrors MethodDescriptor.McpToolName: the resolved tool name this rpc publishes under; empty unless IsMcp or IsAutoMcp
	Comment              string                        // leading comment from proto source; the MCP tool description (decision doc section 8: no second hand-authored description)
}

// MessageInfo represents a protobuf message
type MessageInfo struct {
	Name   string
	Fields []*FieldInfo
}

// FieldInfo represents a message field
type FieldInfo struct {
	Name     string
	Type     string
	Number   int
	Repeated bool
}

// ParseProtoFile parses a proto file and extracts service definitions.
//
// IMPLEMENTATION NOTE: This function now uses DescriptorParser internally for robust,
// accurate parsing via protobuf descriptors. The regex-based implementation has been
// replaced to eliminate parsing fragility and ensure correctness.
//
// Regex usage rationale:
//   - NONE: All proto file parsing is now handled by DescriptorParser which uses protoc's
//     official descriptor API. This ensures accurate parsing of all proto syntax including
//     services, methods, streaming modes, and message types.
func ParseProtoFile(filePath string) (*ProtoInfo, error) {
	// Determine the proto directory (parent directory of the file)
	protoDir := filepath.Dir(filePath)

	// Create descriptor parser
	parser, err := NewDescriptorParser(protoDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	// Use descriptor parser to extract service information
	return parser.ConvertToProtoInfo(filePath)
}

// IsMcpTool reports whether this rpc is published as an MCP tool — declared
// (RPC_PRINCIPAL_MCP) or auto-exposed. See MethodDescriptor.IsMcpTool: a site
// asking "does this rpc produce a tool" uses this; a site asking "is this rpc
// suppressed from the gRPC/REST surface" keeps using IsMcp.
func (m *MethodInfo) IsMcpTool() bool { return m.IsMcp || m.IsAutoMcp }

// StreamingType returns a string describing the streaming type
func (m *MethodInfo) StreamingType() string {
	if m.ClientStreaming && m.ServerStreaming {
		return "bidirectional"
	} else if m.ClientStreaming {
		return "client-streaming"
	} else if m.ServerStreaming {
		return "server-streaming"
	}
	return "unary"
}
