// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"go.uber.org/zap"

	spec "gitlab.com/secondstarcavalier/schematic/tacplus/target/proto/spec/sys"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/reflect/protoregistry"
	"google.golang.org/protobuf/types/descriptorpb"
)

// DescriptorParser uses protobuf descriptors to extract complete type information
// This replaces all regex-based proto parsing with accurate descriptor-based parsing
type DescriptorParser struct {
	files        *protoregistry.Files
	protoDir     string
	descriptors  map[string]*FileDescriptor
	rawFiles     map[string]*descriptorpb.FileDescriptorProto // raw protos keyed by path, for SourceCodeInfo
	restRootPath string                                       // resolved REST_ROOT_PATH constant

	collIndex      map[string]*model.Message // cached message index for collection tier resolution (lazy)
	collIndexBuilt bool                      // true once collIndex has been computed (even if empty)
}

// FileDescriptor contains parsed information from a proto file
type FileDescriptor struct {
	Path                string
	Package             string
	Messages            []MessageDescriptor
	Enums               []EnumDescriptor
	Services            []ServiceDescriptor
	Imports             []string
	Constants           []model.Constant // File-level constants from custom options
	GenerateUserApi     bool             // Whether proto-buddy code generation is enabled via option (spec.generate_user_api)
	ServiceName         string           // File-level service identity from option (spec.service)
	ServiceID           int32            // Stable service id from option (spec.service)
	ServiceStore        bool             // Whether this service is a backing store from option (spec.service).store
	ServiceConfigStore  bool             // Whether this service is the config backing store from option (spec.service).config_store
	ServiceGateway      bool             // Whether this service is a gateway from option (spec.service).gateway
	ServiceRestDispatch bool             // Whether this service needs REST→gRPC dispatch from option (spec.service).rest_dispatch
	ServiceTenantOwned  bool             // Whether this service is a per-tenant instance from option (spec.service).tenant_owned
	StateImports        []string         // Canonical state imports from field-level state_import roots plus legacy file-level mirrors
	FileStateImports    []string         // Legacy file-level state_imports option values, e.g. "identity.Identity"
	FieldStateImports   []FieldStateImport
	GrpcS2SClients      []string         // File-level S2S client deps from option (spec.grpc_s2s_client), e.g. "identity.Mint"
	GrpcC2SClients      []string         // File-level C2S client deps from option (spec.grpc_c2s_client), e.g. "gateway.TokenMint"
	GrpcW2SClients      []string         // File-level W2S client deps from option (spec.grpc_w2s_client), e.g. "dashboard.Dashboard"
	AllGrpcS2SClients   bool             // File-level flag from option (spec.all_grpc_s2s_clients): generate S2S clients for ALL services
	OpaImports          []string         // File-level OPA eval imports from option (spec.opa_import), e.g. "userentitlements.EntitlementEvalInput"
	OpaImportTargets    []VirtualImport  // Parsed OPA imports with target service
	TimeseriesStore     bool             // Whether this service is a timeseries data store from option (spec.timeseries_store)
	RecordStore         bool             // Whether this service is an in-memory hot event store from option (spec.record_store)
	VServiceName        string           // File-level virtual-service identity name from option (spec.vservice).name or (spec.trait).name
	VServiceID          int32            // File-level virtual-service field id from option (spec.vservice).id or (spec.trait).id
	Trait               *TraitDescriptor // File-level trait declaration from option (spec.trait); nil when the file declares none
	RecordReads         []RecordRead     // File-level record read deps from option (spec.record_read)
}

// TraitDescriptor is a parsed file-level (spec.trait) declaration: the contract
// identity plus everything the generated plane needs that used to be a
// generator constant — which physical service hosts the RPCs, what the two
// emitted sibling services are called, and which peers may call the S2S half.
type TraitDescriptor struct {
	Name         string   // canonical trait name, the entitlement/audience key
	ID           int32    // stable TokenEntitlements field id
	Host         string   // routing label of the physical implementor
	S2SService   string   // name of the emitted peer-facing sibling service
	C2SService   string   // name of the emitted client-facing sibling service
	AuthServices []string // peer identities allowed to call the S2S half
}

// VirtualImport represents a dependency on a virtual service with an explicit target.
type VirtualImport struct {
	Source  string // For OPA: "package.InputMessageName". Empty for timeseries.
	Service string // Physical service that implements the virtual service (e.g. "opa").
}

// FieldStateImport describes a field-level (spec.state_import) declaration.
// Source is the full field path, e.g. "owner.OwnerState.items"; Root is the
// imported state root, e.g. "owner.OwnerState".
type FieldStateImport struct {
	Source      string
	Root        string
	MessageName string
	FieldName   string
}

// MessageDescriptor contains message information
type MessageDescriptor struct {
	Name           string
	Comment        string // Leading comment from proto source
	Fields         []FieldDescriptor
	ParamMapConfig *model.ParamMapConfig            // FromMap generation config from message options
	Annotations    map[string]model.AnnotationValue // Message-level annotations (e.g., service_state)
}

// FieldDescriptor contains field information
type FieldDescriptor struct {
	Name             string
	Comment          string // Leading comment from proto source
	Number           int32
	Type             string
	TypeKind         string // "message", "enum", "scalar"
	IsRepeated       bool
	IsOptional       bool
	IsMap            bool
	IsOneOf          bool                             // True if field is inside a oneof group
	OneOfName        string                           // Name of the oneof group (e.g., "boundary")
	MapKeyType       string                           // For map fields: key type
	MapValueType     string                           // For map fields: value type
	MapValueTypeKind string                           // For map fields: value type kind ("message", "enum", "scalar")
	Annotations      map[string]model.AnnotationValue // Field annotations from custom options
	ParamKey         string                           // Parameter key from param_key option
	JsonKey          string                           // Override JSON key for serialization (from spec.json_key)
	JsonAliases      []string                         // Additional keys accepted during deserialization (from spec.json_aliases)
}

// EnumDescriptor contains enum information
type EnumDescriptor struct {
	Name    string
	Comment string // Leading comment from proto source
	Values  []EnumValueDescriptor
}

// EnumValueDescriptor contains enum value information
type EnumValueDescriptor struct {
	Name      string
	Comment   string // Leading comment from proto source
	Number    int32
	JsonValue string // JSON serialization value from json_return_value option
}

// ServiceDescriptor contains service information
type ServiceDescriptor struct {
	Name              string
	Comment           string // Leading comment from proto source
	Methods           []MethodDescriptor
	ReadsState        []string // e.g. ["baseball.TeamRoster"]
	GrpcName          string   // service identity used for routing (defaults to package name)
	GrpcInstances     []string // service identities for generated routing
	IsS2S             bool     // service_s2s annotation present
	IsC2S             bool     // service_c2s annotation present
	IsC2D             bool     // service_c2d annotation present
	C2DSocketPath     string   // service_c2d socket_path: unix socket the daemon binds / clients dial (required, no default)
	IsSMB             bool     // smb annotation present: this service's own instances talk over a black-box peer protocol (SPIFFE plan D7)
	SmbRoot           spec.SmbRoot
	IsSMBAuthority    bool     // smb_authority annotation present: this service issues per-SMB CAs + member certs (SPIFFE plan D7)
	IsTokenMinter     bool     // token_minter annotation present: this service mints service-identity tokens
	TokenMinterProxy  bool     // token_minter {proxy: true}: the client-reachable (C2S) mint front; bare {} is the authoritative S2S minter
	Gateway           bool     // derived: IsC2S && !AuthDisabled
	EventLabel        string   // value from event_label annotation (routing label for event services)
	HostLabel         string   // value from service_s2s/service_c2s {label: "..."} — the package name of the service that hosts this vservice or trait surface
	AuthDisabled      bool     // C2S: no token required
	AuthServices      []string // S2S: service identities allowed to call (service-level default)
	AuthEntitledUsers bool     // C2S: users with matching entitlements can call
	AuthAnyUser       bool     // C2S: any authenticated user can call
	// Synthesized is true for tacplus-generated services (state/store/mutation/
	// opa_eval), identified by the `// tacplus:generated-service` sentinel comment.
	// Used to distinguish them from user-defined service_s2s with empty
	// auth_services, which must NOT be filtered out of server generation.
	Synthesized bool
	// SynthesizedKind is WHICH generated surface this is, from the word after the
	// sentinel (SynthesizedKindState/Store/RecordMap/RecordC2S/OpaEval). Empty when
	// Synthesized is false, and empty for a sentinel written without a kind.
	//
	// "Synthesized" alone is not enough to reason about: these surfaces do not all
	// imply the same thing about the process. A generated STATE service means the
	// process must reach its state host, and therefore needs an S2S identity. A
	// generated STORE service is the mesh face of spec.service_store and a c2d
	// daemon may never serve or call it at all. Collapsing the two is what made a
	// c2d-only build demand a token minter.
	SynthesizedKind string

	// HasMcpRpcs is true when at least one method is an MCP tool — either
	// because it declares principal RPC_PRINCIPAL_MCP, or because it is a user
	// RPC of a c2s service and is therefore auto-exposed (MethodDescriptor.
	// IsAutoMcp). The session-carrying MCP transport is a consequence of that
	// fact, never a separate opt-in, so this is the whole input to "does this
	// service need one".
	HasMcpRpcs bool
	// McpExposure is the derived exposure of that transport: the most-permissive
	// exposure declared across this service's MCP methods, so one EXTERNAL tool
	// makes the carrier reachable from outside to serve it. Meaningless unless
	// HasMcpRpcs.
	McpExposure spec.RpcExposure
}

// synthesizedServiceMarker is the sentinel emitted in a generated service's
// leading comment (see pkg/internal/proto state/store/mutation/opa_eval
// synthesis). It marks tacplus-generated services so they can be excluded from
// own-package server generation without the fragile "S2S + empty auth_services"
// heuristic (which also matched legitimate user services).
//
// The sentinel may be followed by a single-word KIND
// ("tacplus:generated-service store"). Readers must treat an absent kind as
// "unknown synthesized surface" rather than as a specific one: the marker
// predates the kind and a bare sentinel must keep parsing as Synthesized.
const synthesizedServiceMarker = "tacplus:generated-service"

// Synthesized service kinds, as written after the sentinel. These are the
// vocabulary shared between the emitters in pkg/internal/proto and every reader
// of ServiceDescriptor.Synthesized.
const (
	SynthesizedKindState     = "state"     // <Root>StateService / <Svc>ConfigStateService
	SynthesizedKindStore     = "store"     // <Msg>StoreService, from spec.service_store
	SynthesizedKindRecordMap = "recordmap" // RecordMap, the record working-set surface
	// SynthesizedKindRecordC2S is the CLIENT-FACING record read plane —
	// <Service>Records, emitted into the READING service's own proto from
	// (spec.record_read).c2s. Distinct from recordmap: that one is the store's
	// working-set surface and lives on the record store, this one is two scoped
	// read RPCs living on the service that chose to disclose the rows.
	SynthesizedKindRecordC2S = "recordc2s"
	SynthesizedKindOpaEval   = "opaeval" // the OPA entitlement-evaluation surface
)

// parseSynthesizedKind returns the kind word following the sentinel in a
// service's leading comment, or "" when the comment carries no sentinel or a
// bare one. Only the four known kinds are accepted, so an unrecognized word
// reads as "unknown" instead of silently becoming a new kind that no gate knows
// how to treat.
func parseSynthesizedKind(comment string) string {
	idx := strings.Index(comment, synthesizedServiceMarker)
	if idx < 0 {
		return ""
	}
	rest := comment[idx+len(synthesizedServiceMarker):]
	if nl := strings.IndexAny(rest, "\r\n"); nl >= 0 {
		rest = rest[:nl]
	}
	switch word := strings.TrimSpace(rest); word {
	case SynthesizedKindState, SynthesizedKindStore, SynthesizedKindRecordMap, SynthesizedKindRecordC2S, SynthesizedKindOpaEval:
		return word
	default:
		return ""
	}
}

// MethodDescriptor contains RPC method information
type MethodDescriptor struct {
	Name                 string
	Comment              string
	InputType            string
	OutputType           string
	ClientStreaming      bool
	ServerStreaming      bool
	InputTypePackage     string
	OutputTypePackage    string
	HttpMethod           string                // Primary HTTP method for client generation (GET if in methods, else POST)
	HttpMethods          []string              // All HTTP methods (POST always included, plus rest_method extras)
	HttpPath             string                // Convention REST path: {ROOT_PATH}/{package}/{Service}/{MethodName}
	HttpAliasPath        string                // Additional alias path from rest_path (relative to ROOT_PATH)
	HttpBody             string                // HTTP body from google.api.http annotation (e.g. "*" or field name)
	RestPath             string                // raw rest_path annotation value (relative to ROOT_PATH)
	restMethodExtra      []string              // parsed from rest_method annotation (extra methods beyond POST)
	ProofRequires        string                // method-level proof_requires annotation (formal verification precondition)
	ProofEnsures         string                // method-level proof_ensures annotation (formal verification postcondition)
	AuthServices         []string              // per-RPC S2S auth_services override (from spec.rpc)
	AuthActors           []string              // per-RPC auth_actors (from spec.rpc): delegate identities allowed to call on another principal's behalf, matched against the token's act claim
	AuthDisabled         bool                  // per-RPC auth_disabled (from spec.rpc RPC_FLAG_PUBLIC_NO_AUTH / RPC_FLAG_SELF_AUTHENTICATES)
	AuthAnyUser          bool                  // per-RPC auth_any_user (from spec.rpc RPC_FLAG_AUTH_ANY_USER)
	RequiresDevice       bool                  // per-RPC requires_device (from spec.rpc): the device-only approval check gates this RPC
	TargetNamespaceField string                // per-RPC target_namespace_field (from spec.rpc); ordinary methods name a request field, auth_any_user methods use an absolute namespace literal
	Collection           *CollectionDescriptor // method-level method_collection (collection-returning read); nil when absent
	Rpc                  *RpcDescriptor        // method-level spec.rpc (call surface); nil when absent
	MinGroupSize         int32                 // method-level (spec.min_group_size), stamped by the record-map injector onto a record's size-only RPCs; 0 when absent
	IsMcp                bool                  // derived: spec.rpc declares principal RPC_PRINCIPAL_MCP — this RPC is an MCP tool, not a gRPC call
	IsAutoMcp            bool                  // derived: this RPC is a user RPC of a c2s service, so it is an MCP tool AND keeps its gRPC call surface (mcpAutoExposed)
	McpToolName          string                // derived: the tool name this RPC publishes under (snake_case of Name, or the mcp_tool_name override); empty unless IsMcp or IsAutoMcp
}

// RpcDescriptor carries a method's spec.rpc annotation: the two independent
// call-surface axes (principal, exposure) and the non-exclusive protocol
// flags. Nil on MethodDescriptor means the method carries no spec.rpc.
type RpcDescriptor struct {
	Principal   spec.RpcPrincipal // who calls; RPC_PRINCIPAL_UNSPECIFIED when the annotation omits it
	Exposure    spec.RpcExposure  // where from; RPC_EXPOSURE_INTERNAL is the default
	Flags       []spec.RpcFlag    // non-exclusive protocol properties, as declared
	McpToolName string            // mcp_tool_name as declared (the override), empty when omitted; MethodDescriptor.McpToolName carries the resolved name
}

// CollectionDescriptor carries a method's method_collection annotation
// (collection-returning read). See docs/API.md for the model.
type CollectionDescriptor struct {
	Mode            string   // "SNAPSHOT" | "WATCH" | "PAGE" (derived from CollectionMode enum, sentinel stripped)
	OrderBy         []string // response fields defining a stable total order
	MaxItems        uint32   // SNAPSHOT item cap (required, positive); 0 on WATCH/PAGE
	ReplayDepth     uint32   // WATCH replay-ring depth; 0 = use platform default
	DefaultPageSize uint32   // PAGE per-page window when request omits page_size; 0 = fallback
	RevisionField   string   // WATCH delta's revision field name; "" = the "revision" default
}

// NewDescriptorParser creates a DescriptorParser by generating and loading a descriptor set.
func NewDescriptorParser(protoDir string) (*DescriptorParser, error) {
	protoFiles, err := discoverProtoFiles(protoDir)
	if err != nil {
		return nil, err
	}
	return newDescriptorParser(protoDir, protoFiles)
}

// NewDescriptorParserForFiles creates a DescriptorParser for a scoped list of
// proto files. Imports are still included in the descriptor set, but unrelated
// files under protoDir are not compiled. This is useful during pre-generation
// phases where the whole tree cannot be parsed yet because generated/*.proto
// has not been written.
func NewDescriptorParserForFiles(protoDir string, protoFiles []string) (*DescriptorParser, error) {
	if len(protoFiles) == 0 {
		return nil, fmt.Errorf("no proto files provided for %s", protoDir)
	}

	cleaned := make([]string, 0, len(protoFiles))
	seen := make(map[string]bool, len(protoFiles))
	for _, path := range protoFiles {
		relPath := filepath.ToSlash(filepath.Clean(path))
		if filepath.IsAbs(path) || strings.Contains(relPath, "..") {
			return nil, fmt.Errorf("proto file path contains traversal: %s", path)
		}
		if filepath.Ext(relPath) != ".proto" {
			return nil, fmt.Errorf("proto file path must end in .proto: %s", path)
		}
		if seen[relPath] {
			continue
		}
		seen[relPath] = true
		cleaned = append(cleaned, relPath)
	}
	sort.Strings(cleaned)
	return newDescriptorParser(protoDir, cleaned)
}

func discoverProtoFiles(protoDir string) ([]string, error) {
	rootProtoDir := protoDir
	var protoFiles []string
	err := filepath.Walk(rootProtoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() && info.Name() == ".git" {
			return filepath.SkipDir
		}
		if !info.IsDir() && filepath.Ext(path) == ".proto" {
			relPath, err := filepath.Rel(rootProtoDir, path)
			if err != nil {
				return err
			}
			relPath = filepath.ToSlash(relPath)
			if strings.Contains(relPath, "..") {
				return fmt.Errorf("proto file path contains traversal: %s", relPath)
			}
			protoFiles = append(protoFiles, relPath)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to find proto files: %w", err)
	}
	sort.Strings(protoFiles)
	return protoFiles, nil
}

func newDescriptorParser(protoDir string, protoFiles []string) (*DescriptorParser, error) {
	// Create temp file for descriptor set
	descFile, err := os.CreateTemp("", "proto-desc-*.pb")
	if err != nil {
		return nil, fmt.Errorf("failed to create temp descriptor file: %w", err)
	}
	descPath := descFile.Name()
	descFile.Close()
	defer func() {
		if err := os.Remove(descPath); err != nil {
			zap.L().Warn("Failed to remove temp descriptor file", zap.String("path", descPath), zap.Error(err))
		}
	}()

	// Use the proto directory provided by the caller
	rootProtoDir := protoDir

	if len(protoFiles) == 0 {
		return nil, fmt.Errorf("no proto files found in %s", rootProtoDir)
	}

	// Generate descriptor set using protoc with root proto dir
	args := []string{
		"--descriptor_set_out=" + descPath,
		"--include_imports",
		"--include_source_info",
		"--proto_path=" + rootProtoDir,
	}
	args = append(args, protoFiles...)

	protocPath, err := tactype.ResolveProtocBinary()
	if err != nil {
		return nil, err
	}

	cmd := exec.Command(protocPath, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("protoc failed: %w\n%s", err, string(output))
	}

	// Load descriptor set
	descBytes, err := os.ReadFile(descPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read descriptor set: %w", err)
	}

	var fileDescSet descriptorpb.FileDescriptorSet
	if err := proto.Unmarshal(descBytes, &fileDescSet); err != nil {
		return nil, fmt.Errorf("failed to unmarshal descriptor set: %w", err)
	}

	// Create file descriptors registry and retain raw protos for SourceCodeInfo
	files := new(protoregistry.Files)
	rawFiles := make(map[string]*descriptorpb.FileDescriptorProto, len(fileDescSet.File))
	for _, fdProto := range fileDescSet.File {
		fd, err := protodesc.NewFile(fdProto, files)
		if err != nil {
			return nil, fmt.Errorf("failed to create file descriptor: %w", err)
		}
		if err := files.RegisterFile(fd); err != nil {
			return nil, fmt.Errorf("failed to register file descriptor: %w", err)
		}
		rawFiles[fdProto.GetName()] = fdProto
	}

	parser := &DescriptorParser{
		files:       files,
		protoDir:    rootProtoDir,
		descriptors: make(map[string]*FileDescriptor),
		rawFiles:    rawFiles,
	}

	// Pre-resolve REST_ROOT_PATH from file-level constants before parsing services.
	// This avoids ordering issues since all proto descriptors are already loaded.
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		opts, ok := fd.Options().(*descriptorpb.FileOptions)
		if !ok || opts == nil {
			return true
		}
		if proto.HasExtension(opts, spec.E_StringConst) {
			for _, sc := range proto.GetExtension(opts, spec.E_StringConst).([]*spec.StringConst) {
				if sc.GetName() == "REST_ROOT_PATH" {
					parser.restRootPath = sc.GetValue()
					return false // found it, stop
				}
			}
		}
		return true
	})

	// Parse all files into our descriptor format
	files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		parser.parseFileDescriptor(fd)
		return true
	})

	return parser, nil
}

// commentMap indexes SourceCodeInfo.Location entries by their path for O(1) lookup.
// The key is the path serialized as comma-separated integers (e.g. "4,0,2,1").
type commentMap map[string]string

// buildCommentMap builds a lookup from SourceCodeInfo path → leading comment.
func buildCommentMap(raw *descriptorpb.FileDescriptorProto) commentMap {
	cm := make(commentMap)
	if raw == nil || raw.SourceCodeInfo == nil {
		return cm
	}
	for _, loc := range raw.SourceCodeInfo.GetLocation() {
		comment := strings.TrimSpace(loc.GetLeadingComments())
		if comment == "" {
			continue
		}
		// Build path key
		parts := make([]string, len(loc.Path))
		for i, v := range loc.Path {
			parts[i] = fmt.Sprintf("%d", v)
		}
		cm[strings.Join(parts, ",")] = comment
	}
	return cm
}

// pathKey builds the lookup key for a source code info path.
func pathKey(parts ...int) string {
	s := make([]string, len(parts))
	for i, v := range parts {
		s[i] = fmt.Sprintf("%d", v)
	}
	return strings.Join(s, ",")
}

// parseFileDescriptor converts protoreflect.FileDescriptor to our FileDescriptor
func (p *DescriptorParser) parseFileDescriptor(fd protoreflect.FileDescriptor) {
	fileDesc := &FileDescriptor{
		Path:     fd.Path(),
		Package:  string(fd.Package()),
		Messages: []MessageDescriptor{},
		Enums:    []EnumDescriptor{},
		Services: []ServiceDescriptor{},
		Imports:  []string{},
	}

	// Build comment map from raw proto's SourceCodeInfo
	cm := buildCommentMap(p.rawFiles[fd.Path()])

	// Parse imports
	for i := 0; i < fd.Imports().Len(); i++ {
		imp := fd.Imports().Get(i)
		fileDesc.Imports = append(fileDesc.Imports, imp.Path())
	}

	// Parse messages (path tag 4 = FileDescriptorProto.message_type)
	for i := 0; i < fd.Messages().Len(); i++ {
		msg := fd.Messages().Get(i)
		fileDesc.Messages = append(fileDesc.Messages, p.parseMessageDescriptor(msg, cm, 4, i))
	}

	// Parse enums (path tag 5 = FileDescriptorProto.enum_type)
	for i := 0; i < fd.Enums().Len(); i++ {
		enum := fd.Enums().Get(i)
		fileDesc.Enums = append(fileDesc.Enums, p.parseEnumDescriptor(enum, cm, 5, i))
	}

	// Parse services (path tag 6 = FileDescriptorProto.service)
	for i := 0; i < fd.Services().Len(); i++ {
		svc := fd.Services().Get(i)
		fileDesc.Services = append(fileDesc.Services, p.parseServiceDescriptor(svc, cm, 6, i, fileDesc.Package))
	}

	// Parse file-level constants from custom options
	fileDesc.Constants = p.parseFileConstants(fd)

	// Parse generate_user_api option
	fileDesc.GenerateUserApi = p.parseGenerateUserApi(fd)
	fileDesc.ServiceName, fileDesc.ServiceID, fileDesc.ServiceStore, fileDesc.ServiceConfigStore, fileDesc.ServiceGateway, fileDesc.ServiceRestDispatch, fileDesc.ServiceTenantOwned = p.parseServiceIdentity(fd, fileDesc.Package)
	fileDesc.FileStateImports = p.parseStateImports(fd)
	fileDesc.FieldStateImports = collectFieldStateImports(fileDesc.Messages)
	fileDesc.StateImports = mergeStateImportRoots(fileDesc.FileStateImports, fileDesc.FieldStateImports)
	fileDesc.GrpcS2SClients = p.parseGrpcS2SClients(fd)
	fileDesc.RecordReads = p.parseRecordReads(fd)
	fileDesc.GrpcC2SClients = p.parseGrpcC2SClients(fd)
	fileDesc.GrpcW2SClients = p.parseGrpcW2SClients(fd)
	fileDesc.AllGrpcS2SClients = p.parseBoolFileOption(fd, spec.E_AllGrpcS2SClients)
	fileDesc.OpaImports, fileDesc.OpaImportTargets = p.parseOpaImports(fd)
	fileDesc.TimeseriesStore = p.parseTimeseriesStore(fd)
	fileDesc.RecordStore = p.parseRecordStore(fd)
	fileDesc.VServiceName, fileDesc.VServiceID = p.parseVService(fd)
	fileDesc.Trait = p.parseTrait(fd)
	if fileDesc.Trait != nil && fileDesc.Trait.Name != "" {
		fileDesc.VServiceName, fileDesc.VServiceID = fileDesc.Trait.Name, fileDesc.Trait.ID
	}

	p.descriptors[fd.Path()] = fileDesc
}

// ValidateNoNestedRedact fails loud when (spec.redact) is declared on a field of
// a NESTED message declaration. tacplus applies egress redaction only to
// top-level record messages, and parseMessageDescriptor does NOT recurse into
// nested type declarations — so a redact annotation inside one is invisible to
// the validator and every language's emitter, silently shipping the data the
// annotation exists to protect (NEW-4). Reject it: the type must be hoisted to
// top level (or the annotation removed). Top-level redact fields are unaffected —
// they flow through the normal pipeline (ValidateRedactAnnotations et al.).
func (p *DescriptorParser) ValidateNoNestedRedact() error {
	var offenders []string
	p.files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		msgs := fd.Messages()
		for i := 0; i < msgs.Len(); i++ {
			collectNestedRedactFields(string(fd.Path()), msgs.Get(i), false, &offenders)
		}
		return true
	})
	if len(offenders) == 0 {
		return nil
	}
	sort.Strings(offenders)
	return fmt.Errorf("(spec.redact) is declared on a field of a NESTED message declaration (%s); "+
		"tacplus applies egress redaction only to top-level record messages, so a nested redact is silently dropped — "+
		"hoist the type to top level or remove the annotation", strings.Join(offenders, ", "))
}

// collectNestedRedactFields recurses a message's nested type declarations,
// recording any field carrying (spec.redact). isNested is false for the top-level
// message (whose own redact fields are the normal pipeline's job) and true once
// inside a nested declaration. Synthetic map-entry messages are skipped.
func collectNestedRedactFields(filePath string, md protoreflect.MessageDescriptor, isNested bool, offenders *[]string) {
	if md.IsMapEntry() {
		return
	}
	if isNested {
		fields := md.Fields()
		for i := 0; i < fields.Len(); i++ {
			f := fields.Get(i)
			if opts := f.Options(); opts != nil && proto.HasExtension(opts, spec.E_Redact) {
				*offenders = append(*offenders, fmt.Sprintf("%s: %s.%s", filePath, md.Name(), f.Name()))
			}
		}
	}
	nested := md.Messages()
	for i := 0; i < nested.Len(); i++ {
		collectNestedRedactFields(filePath, nested.Get(i), true, offenders)
	}
}

// parseMessageDescriptor converts protoreflect.MessageDescriptor to MessageDescriptor.
// parentPath is the SourceCodeInfo path to this message (e.g. [4, msgIndex]).
func (p *DescriptorParser) parseMessageDescriptor(md protoreflect.MessageDescriptor, cm commentMap, parentTag, msgIndex int) MessageDescriptor {
	msg := MessageDescriptor{
		Name:           string(md.Name()),
		Comment:        cm[pathKey(parentTag, msgIndex)],
		Fields:         []FieldDescriptor{},
		ParamMapConfig: p.parseParamMapConfig(md),
		Annotations:    p.parseMessageAnnotations(md),
	}

	// Field path tag 2 = DescriptorProto.field
	for i := 0; i < md.Fields().Len(); i++ {
		field := md.Fields().Get(i)
		fieldComment := cm[pathKey(parentTag, msgIndex, 2, i)]
		msg.Fields = append(msg.Fields, p.parseFieldDescriptorWithComment(field, fieldComment))
	}

	return msg
}

// parseFieldDescriptorWithComment converts protoreflect.FieldDescriptor to FieldDescriptor with a comment.
func (p *DescriptorParser) parseFieldDescriptorWithComment(fd protoreflect.FieldDescriptor, comment string) FieldDescriptor {
	field := FieldDescriptor{
		Name:        string(fd.Name()),
		Comment:     comment,
		Number:      int32(fd.Number()),
		IsRepeated:  fd.Cardinality() == protoreflect.Repeated,
		IsOptional:  fd.HasOptionalKeyword(),
		IsMap:       fd.IsMap(),
		Annotations: p.parseFieldAnnotations(fd),
		ParamKey:    p.parseParamKey(fd),
	}

	// Promote json_key/json_aliases from annotations to first-class fields
	if v, ok := field.Annotations["json_key"]; ok {
		field.JsonKey = v.StringValue
	}
	if v, ok := field.Annotations["json_aliases"]; ok {
		field.JsonAliases = v.StringSliceValue
	}

	// Check if field is inside a oneof group
	if od := fd.ContainingOneof(); od != nil && !fd.HasOptionalKeyword() {
		field.IsOneOf = true
		field.OneOfName = string(od.Name())
	}

	// Handle map fields specially
	if fd.IsMap() {
		// For map fields, the field is actually a repeated message with map entry
		// The map entry message has two fields: "key" (field 1) and "value" (field 2)
		mapEntry := fd.Message()
		keyField := mapEntry.Fields().ByNumber(1)
		valueField := mapEntry.Fields().ByNumber(2)

		// Get key type (always scalar for proto maps)
		field.MapKeyType = keyField.Kind().String()

		// Get value type
		switch valueField.Kind() {
		case protoreflect.MessageKind:
			field.MapValueType = string(valueField.Message().FullName())
			field.MapValueTypeKind = "message"
		case protoreflect.EnumKind:
			field.MapValueType = string(valueField.Enum().FullName())
			field.MapValueTypeKind = "enum"
		default:
			field.MapValueType = valueField.Kind().String()
			field.MapValueTypeKind = "scalar"
		}

		// For map fields, set the type to the map entry message name
		field.Type = string(fd.Message().FullName())
		field.TypeKind = "message"
	} else {
		// Determine type and type kind for non-map fields
		switch fd.Kind() {
		case protoreflect.MessageKind:
			field.Type = string(fd.Message().FullName())
			field.TypeKind = "message"
		case protoreflect.EnumKind:
			field.Type = string(fd.Enum().FullName())
			field.TypeKind = "enum"
		default:
			field.Type = fd.Kind().String()
			field.TypeKind = "scalar"
		}
	}

	return field
}

// parseEnumDescriptor converts protoreflect.EnumDescriptor to EnumDescriptor.
// parentTag and enumIndex locate this enum in SourceCodeInfo (e.g. [5, enumIndex]).
func (p *DescriptorParser) parseEnumDescriptor(ed protoreflect.EnumDescriptor, cm commentMap, parentTag, enumIndex int) EnumDescriptor {
	enum := EnumDescriptor{
		Name:    string(ed.Name()),
		Comment: cm[pathKey(parentTag, enumIndex)],
		Values:  []EnumValueDescriptor{},
	}

	// Enum value path tag 2 = EnumDescriptorProto.value
	for i := 0; i < ed.Values().Len(); i++ {
		val := ed.Values().Get(i)
		enum.Values = append(enum.Values, EnumValueDescriptor{
			Name:      string(val.Name()),
			Comment:   cm[pathKey(parentTag, enumIndex, 2, i)],
			Number:    int32(val.Number()),
			JsonValue: p.parseJsonReturnValue(val),
		})
	}

	return enum
}

// parseServiceDescriptor converts protoreflect.ServiceDescriptor to ServiceDescriptor
func (p *DescriptorParser) parseServiceDescriptor(sd protoreflect.ServiceDescriptor, cm commentMap, parentTag, svcIndex int, _pkg string) ServiceDescriptor {
	svcComment := cm[pathKey(parentTag, svcIndex)]
	svc := ServiceDescriptor{
		Name:            string(sd.Name()),
		Comment:         svcComment,
		Methods:         []MethodDescriptor{},
		Synthesized:     strings.Contains(svcComment, synthesizedServiceMarker),
		SynthesizedKind: parseSynthesizedKind(svcComment),
	}

	// Parse service options (service_s2s, service_c2s)
	p.parseServiceOptions(sd, &svc, _pkg)

	for i := 0; i < sd.Methods().Len(); i++ {
		method := sd.Methods().Get(i)

		inputType := string(method.Input().FullName())
		outputType := string(method.Output().FullName())

		// Extract package from full type name
		inputParts := strings.Split(inputType, ".")
		outputParts := strings.Split(outputType, ".")

		inputPackage := ""
		if len(inputParts) > 1 {
			inputPackage = strings.Join(inputParts[:len(inputParts)-1], ".")
		}

		outputPackage := ""
		if len(outputParts) > 1 {
			outputPackage = strings.Join(outputParts[:len(outputParts)-1], ".")
		}

		md := MethodDescriptor{
			Name:              string(method.Name()),
			Comment:           cm[pathKey(parentTag, svcIndex, 2, i)],
			InputType:         inputType,
			OutputType:        outputType,
			ClientStreaming:   method.IsStreamingClient(),
			ServerStreaming:   method.IsStreamingServer(),
			InputTypePackage:  inputPackage,
			OutputTypePackage: outputPackage,
		}
		p.parseMethodOptions(method, &md)

		// Convention-based REST paths for gateway-exposed gRPC services.
		// Every RPC on an annotated service gets a convention path:
		//   {REST_ROOT_PATH}/{package}/{MethodName}
		// POST is always present. Additional methods come from rest_method.
		// rest_path creates an additive alias (relative to REST_ROOT_PATH).
		//
		// An RPC_PRINCIPAL_MCP rpc is deliberately excluded: it declares a tool,
		// not a call, and the MCP design (§5) is that tacplus maps it to no gRPC
		// RPC at all. Leaving HttpPath/HttpMethods empty is what suppresses its
		// REST route and w2s surface — every consumer of those two fields
		// (rpcSurfaceInfos, the w2s/rest client generators) already treats an
		// empty path as "no REST surface".
		if svc.GrpcName != "" && len(md.HttpMethods) == 0 && !md.IsMcp {
			// Build method list: POST always present
			methods := []string{"POST"}
			// Server-streaming defaults to GET
			if md.ServerStreaming {
				methods = append(methods, "GET")
			}
			// Add methods from rest_method annotation
			for _, m := range md.restMethodExtra {
				if m != "POST" { // POST already included
					methods = append(methods, m)
				}
			}
			md.HttpMethods = methods

			// Primary method for client generation: prefer GET if available, else POST
			md.HttpMethod = "POST"
			for _, m := range methods {
				if m == "GET" {
					md.HttpMethod = "GET"
					break
				}
			}

			// Convention path is always present. It mirrors the gRPC + entitlement
			// grant identity ({package}/{service}/{RPC}) so REST routing is
			// collision-free when one package hosts >=2 gateway/C2S services that
			// share a method name (T10). svc.GrpcName is the package; svc.Name is
			// the service.
			md.HttpPath = fmt.Sprintf("%s/%s/%s/%s", p.restRootPath, svc.GrpcName, svc.Name, md.Name)

			// rest_path adds an alias route (relative to REST_ROOT_PATH)
			if md.RestPath != "" {
				md.HttpAliasPath = fmt.Sprintf("%s/%s", p.restRootPath, md.RestPath)
			}

			md.HttpBody = "*"
		}

		// Gateway-exposed event service Publish RPCs.
		// When a state service has event_label set, generate convention REST paths
		// for Publish* RPCs using the label as the route prefix.
		if svc.EventLabel != "" && svc.Gateway && strings.HasPrefix(md.Name, "Publish") && len(md.HttpMethods) == 0 {
			md.HttpMethods = []string{"POST"}
			md.HttpMethod = "POST"
			md.HttpPath = fmt.Sprintf("%s/%s/%s", p.restRootPath, svc.EventLabel, md.Name)
			md.HttpBody = "*"
		}

		// Auto-exposure: a user RPC of a c2s service is an MCP tool without any
		// annotation asking for one. Resolved here, next to IsMcp, so exactly one
		// place answers "is this rpc a tool" and every generator and the registry
		// read the same bit. Unlike IsMcp this is additive — the rpc keeps its
		// gRPC call surface, its REST route and its entitlement grant, and the
		// tool shares the handler method the gRPC wrapper already calls.
		if !md.IsMcp && mcpAutoExposed(&svc, &md) {
			md.IsAutoMcp = true
			md.McpToolName = md.Rpc.GetMcpToolNameOverride()
			if md.McpToolName == "" {
				md.McpToolName = strutil.ToSnakeCase(md.Name)
			}
		}

		svc.Methods = append(svc.Methods, md)

		// Derived, not declared: §9 of the MCP design. EXTERNAL wins because the
		// carrier has to be reachable from wherever its most-exposed tool is
		// called.
		if md.IsMcpTool() {
			svc.HasMcpRpcs = true
			if md.Rpc != nil && md.Rpc.Exposure == spec.RpcExposure_RPC_EXPOSURE_EXTERNAL {
				svc.McpExposure = spec.RpcExposure_RPC_EXPOSURE_EXTERNAL
			}
		}
	}

	return svc
}

// IsMcpTool reports whether this rpc is published as an MCP tool — either
// declared (RPC_PRINCIPAL_MCP) or auto-exposed as a user RPC of a c2s service.
// The two differ in what ELSE the rpc is: a declared tool maps to no gRPC call
// at all, an auto-exposed one keeps its full gRPC surface. Every site that asks
// "does this rpc produce a tool" must use this; every site that asks "is this
// rpc suppressed from the gRPC/REST surface" must keep using IsMcp.
func (m *MethodDescriptor) IsMcpTool() bool { return m.IsMcp || m.IsAutoMcp }

// GetMcpToolNameOverride returns the declared mcp_tool_name, or "" when the rpc
// carries no spec.rpc at all — which is the common case for an auto-exposed
// user RPC.
func (r *RpcDescriptor) GetMcpToolNameOverride() string {
	if r == nil {
		return ""
	}
	return r.McpToolName
}

// mcpAutoExposed reports whether m is a "user RPC" of svc, and therefore an MCP
// tool with no annotation asking for one.
//
// The predicate (decision of 2026-09-22): an rpc on a (spec.service_c2s)
// service that is not RPC_PRINCIPAL_SERVICE, minus RPC_FLAG_PUBLIC_NO_AUTH and
// RPC_FLAG_SELF_AUTHENTICATES. There is deliberately no opt-out annotation —
// catalogue size is the MCP proxy's progressive-disclosure problem, not a
// filter to configure here. A token-minting or no-auth endpoint is never a
// tool: the first mints credentials, the second has no caller identity to scope
// a tool call by.
//
// Two shapes are excluded rather than refused, because auto-exposure is not a
// request the author made:
//
//   - streaming, on either side. A tool call is one request and one response,
//     and the generated session carrier is itself a bidi rpc on its own service
//     — so refusing here would make every c2s service with a streaming rpc
//     (the carrier included) ungeneratable. An author who writes
//     RPC_PRINCIPAL_MCP on a streaming rpc still gets MCP-1's hard refusal,
//     because there the tool WAS asked for.
//   - method_collection in any mode but PAGE. WATCH is server-streaming, so
//     the line above already has it; SNAPSHOT is excluded here. A PAGE rpc IS
//     auto-exposed (phase 2 of this decision): page_size and page_token are
//     ordinary fields of its request message, so they are ordinary tool
//     arguments, and the dispatch calls the same three-argument handler the
//     gRPC PAGE wrapper calls. See the per-language *McpToolGeneratable
//     guards, which keep refusing a collection tool in the modes this seam
//     still cannot call.
//
// A service that is auth_disabled is excluded for the reason MCP-5 gives: the
// carrier scopes every tool call by a Caller no authorizer verified.
func mcpAutoExposed(svc *ServiceDescriptor, m *MethodDescriptor) bool {
	if !svc.IsC2S || svc.AuthDisabled {
		return false
	}
	if m.ClientStreaming || m.ServerStreaming {
		return false
	}
	if m.Collection != nil && m.Collection.Mode != "PAGE" {
		return false
	}
	if m.Rpc == nil {
		// The common case fleet-wide: no (spec.rpc) at all, user-facing purely
		// by the service's auth_entitled_users.
		return true
	}
	if m.Rpc.Principal == spec.RpcPrincipal_RPC_PRINCIPAL_SERVICE {
		return false
	}
	// md.AuthDisabled is set from exactly RPC_FLAG_PUBLIC_NO_AUTH and
	// RPC_FLAG_SELF_AUTHENTICATES (parseMethodOptions), which is the pair the
	// decision names.
	return !m.AuthDisabled
}

// parseServiceOptions extracts service-level options (service_s2s, service_c2s, etc.)
// pkg is the proto package name, used as the default GrpcName.
func (p *DescriptorParser) parseServiceOptions(sd protoreflect.ServiceDescriptor, svc *ServiceDescriptor, pkg string) {
	opts := sd.Options().(*descriptorpb.ServiceOptions)
	if opts == nil {
		return
	}

	// service_s2s (ServiceS2SAnnotation) — service-to-service gRPC service.
	if proto.HasExtension(opts, spec.E_ServiceS2S) {
		val := proto.GetExtension(opts, spec.E_ServiceS2S).(*spec.ServiceS2SAnnotation)
		if val != nil {
			svc.IsS2S = true
			svc.GrpcName = pkg
			svc.GrpcInstances = []string{pkg}
			svc.AuthServices = val.GetAuthServices()
			svc.HostLabel = val.GetLabel() // non-empty for vservices: names the package of the service that hosts this RPC surface
		}
	}

	// service_c2s (ServiceC2SAnnotation) — client-to-service gRPC service.
	// Per-RPC auth policy is declared on spec.rpc.
	if proto.HasExtension(opts, spec.E_ServiceC2S) {
		val := proto.GetExtension(opts, spec.E_ServiceC2S).(*spec.ServiceC2SAnnotation)
		if val != nil {
			svc.IsC2S = true
			svc.GrpcName = pkg
			svc.GrpcInstances = []string{pkg}
			svc.AuthEntitledUsers = val.GetAuthEntitledUsers()
			// non-empty for a trait-hosted C2S surface: names the physical
			// service that serves these RPCs. The audience the gateway checks
			// is the file's trait name, not this label — see
			// ValidateC2SHostLabels.
			svc.HostLabel = val.GetLabel()
			// Gateway: all C2S services are gateway-exposed.
			svc.Gateway = true
		}
	}

	// service_c2d (ServiceC2DAnnotation) — client-to-daemon gRPC service.
	// No auth, no gateway routing. Unix socket transport.
	if proto.HasExtension(opts, spec.E_ServiceC2D) {
		svc.IsC2D = true
		svc.GrpcName = pkg
		svc.GrpcInstances = []string{pkg}
		if ann, ok := proto.GetExtension(opts, spec.E_ServiceC2D).(*spec.ServiceC2DAnnotation); ok && ann != nil {
			svc.C2DSocketPath = ann.GetSocketPath()
		}
	}

	// event_label (string) — routing label for gateway-exposed event services
	if proto.HasExtension(opts, spec.E_EventLabel) {
		svc.EventLabel = proto.GetExtension(opts, spec.E_EventLabel).(string)
	}

	// smb (SmbAnnotation) — Service Mesh Blackbox: this service's own instances
	// talk over a black-box peer protocol within a single
	// cluster (SPIFFE plan D7). The generator surfaces/soundness checks key off
	// this flag; SmbRoot records whether the per-SMB root is isolated or rooted
	// in the cluster CA.
	if proto.HasExtension(opts, spec.E_Smb) {
		if val := proto.GetExtension(opts, spec.E_Smb).(*spec.SmbAnnotation); val != nil {
			svc.IsSMB = true
			svc.SmbRoot = val.GetRoot()
		}
	}

	// smb_authority (SmbAuthorityAnnotation) — this consumer service issues
	// per-SMB CAs + member certs (SPIFFE plan D7). tacplus generates the contract
	// and the SMB service client seam; the designated service implements it.
	if proto.HasExtension(opts, spec.E_SmbAuthority) {
		if val := proto.GetExtension(opts, spec.E_SmbAuthority).(*spec.SmbAuthorityAnnotation); val != nil {
			svc.IsSMBAuthority = true
		}
	}

	// token_minter (TokenMinterAnnotation) — this service mints service-identity
	// tokens. {proxy: true} is the client-reachable C2S front; bare {} is the
	// authoritative S2S minter. Read from the descriptor (not an RPC-name or
	// single-line text scan) so multiline options classify correctly (OF-17a).
	if proto.HasExtension(opts, spec.E_TokenMinter) {
		if val := proto.GetExtension(opts, spec.E_TokenMinter).(*spec.TokenMinterAnnotation); val != nil {
			svc.IsTokenMinter = true
			svc.TokenMinterProxy = val.GetProxy()
		}
	}
}

// parseMethodOptions extracts method-level options.
func (p *DescriptorParser) parseMethodOptions(method protoreflect.MethodDescriptor, md *MethodDescriptor) {
	opts := method.Options().(*descriptorpb.MethodOptions)
	if opts == nil {
		return
	}

	// rest_method (RestMethods) — additional HTTP methods for this RPC
	if proto.HasExtension(opts, spec.E_RestMethod) {
		rm := proto.GetExtension(opts, spec.E_RestMethod).(*spec.RestMethods)
		if rm != nil {
			for _, m := range rm.GetMethods() {
				switch m {
				case spec.RestMethod_REST_METHOD_GET:
					md.restMethodExtra = append(md.restMethodExtra, "GET")
				case spec.RestMethod_REST_METHOD_PATCH:
					md.restMethodExtra = append(md.restMethodExtra, "PATCH")
				case spec.RestMethod_REST_METHOD_PUT:
					md.restMethodExtra = append(md.restMethodExtra, "PUT")
				case spec.RestMethod_REST_METHOD_DELETE:
					md.restMethodExtra = append(md.restMethodExtra, "DELETE")
				}
			}
		}
	}

	// rest_path (string) — additive alias path relative to ROOT_PATH
	if proto.HasExtension(opts, spec.E_RestPath) {
		md.RestPath = proto.GetExtension(opts, spec.E_RestPath).(string)
	}

	// proof_requires (string) — formal verification precondition
	if proto.HasExtension(opts, spec.E_ProofRequires) {
		md.ProofRequires = proto.GetExtension(opts, spec.E_ProofRequires).(string)
	}

	// proof_ensures (string) — formal verification postcondition
	if proto.HasExtension(opts, spec.E_ProofEnsures) {
		md.ProofEnsures = proto.GetExtension(opts, spec.E_ProofEnsures).(string)
	}

	// rpc (RpcAnnotation) — per-RPC call surface (principal / exposure / flags)
	// plus the auth facts that legitimately differ between two RPCs of one
	// service: the allowed caller identities and the target namespace field.
	if proto.HasExtension(opts, spec.E_Rpc) {
		val := proto.GetExtension(opts, spec.E_Rpc).(*spec.RpcAnnotation)
		if val != nil {
			md.Rpc = &RpcDescriptor{
				Principal:   val.GetPrincipal(),
				Exposure:    val.GetExposure(),
				Flags:       val.GetFlags(),
				McpToolName: val.GetMcpToolName(),
			}
			// An MCP tool's name and description are the RPC's own name and doc
			// comment, resolved here so no consumer re-derives the convention.
			// The override is the rare escape hatch; ValidateMcpAnnotations
			// rejects it on a non-MCP RPC rather than dropping it silently.
			if val.GetPrincipal() == spec.RpcPrincipal_RPC_PRINCIPAL_MCP {
				md.IsMcp = true
				md.McpToolName = val.GetMcpToolName()
				if md.McpToolName == "" {
					md.McpToolName = strutil.ToSnakeCase(md.Name)
				}
			}
			for _, f := range val.GetFlags() {
				switch f {
				// Both flags mean "no bearer token on this RPC, so the caller
				// interceptor skips it"; they differ in why (public endpoint vs
				// authenticates from the request body), not in the wiring.
				case spec.RpcFlag_RPC_FLAG_PUBLIC_NO_AUTH, spec.RpcFlag_RPC_FLAG_SELF_AUTHENTICATES:
					md.AuthDisabled = true
				case spec.RpcFlag_RPC_FLAG_AUTH_ANY_USER:
					md.AuthAnyUser = true
				}
			}
			if svcs := val.GetAuthServices(); len(svcs) > 0 {
				md.AuthServices = svcs
			}
			if actors := val.GetAuthActors(); len(actors) > 0 {
				md.AuthActors = actors
			}
			md.RequiresDevice = val.GetRequiresDevice()
			if f := val.GetTargetNamespaceField(); f != "" {
				md.TargetNamespaceField = f
			}
		}
	}

	// min_group_size (int32) — the smallest result size this size-only RPC will
	// disclose. Stamped by the record-map injector from the record's
	// (spec.record).min_group_size; never hand-written on an RPC.
	if proto.HasExtension(opts, spec.E_MinGroupSize) {
		md.MinGroupSize = proto.GetExtension(opts, spec.E_MinGroupSize).(int32)
	}

	// method_collection (MethodCollection) — collection-returning read shape
	if proto.HasExtension(opts, spec.E_MethodCollection) {
		val := proto.GetExtension(opts, spec.E_MethodCollection).(*spec.MethodCollection)
		if val != nil {
			cd := &CollectionDescriptor{
				Mode:            collectionModeName(val.GetMode()),
				OrderBy:         val.GetOrderBy(),
				MaxItems:        val.GetMaxItems(),
				ReplayDepth:     val.GetReplayDepth(),
				DefaultPageSize: val.GetDefaultPageSize(),
				RevisionField:   val.GetRevisionField(),
			}
			md.Collection = cd
		}
	}
}

// collectionModeName maps the CollectionMode enum to a bare mode name
// ("SNAPSHOT" | "WATCH"), stripping the COLLECTION_MODE_ prefix. The
// unspecified sentinel maps to "" so downstream validation can flag it.
func collectionModeName(m spec.CollectionMode) string {
	switch m {
	case spec.CollectionMode_COLLECTION_MODE_SNAPSHOT:
		return "SNAPSHOT"
	case spec.CollectionMode_COLLECTION_MODE_WATCH:
		return "WATCH"
	case spec.CollectionMode_COLLECTION_MODE_PAGE:
		return "PAGE"
	default:
		return ""
	}
}

// GetFileDescriptor returns the parsed descriptor for a proto file
// The protoPath can be either absolute or relative to the proto root directory
func (p *DescriptorParser) GetFileDescriptor(protoPath string) (*FileDescriptor, error) {
	// Try direct lookup first
	desc, ok := p.descriptors[protoPath]
	if ok {
		return desc, nil
	}

	// If not found and path is absolute, try converting to relative
	if filepath.IsAbs(protoPath) {
		relPath, err := filepath.Rel(p.protoDir, protoPath)
		if err == nil {
			desc, ok = p.descriptors[relPath]
			if ok {
				return desc, nil
			}
		}
	}

	// Try matching by base name as last resort
	baseName := filepath.Base(protoPath)
	for key, desc := range p.descriptors {
		if filepath.Base(key) == baseName {
			return desc, nil
		}
	}

	return nil, fmt.Errorf("descriptor not found for %s (checked %d descriptors)", protoPath, len(p.descriptors))
}

// GetAllFiles returns all parsed file descriptors
func (p *DescriptorParser) GetAllFiles() map[string]*FileDescriptor {
	return p.descriptors
}

// TokenMinters returns every service carrying the (spec.token_minter)
// annotation, resolved from the parsed descriptor model (not RPC names or a
// text scan). Results are sorted by Dir then ServiceName for deterministic
// output. The Dir is the proto file path minus its .proto extension (e.g.
// "services/mint"), matching the historical TokenExchangerInfo.Dir contract.
func (p *DescriptorParser) TokenMinters() []TokenExchangerInfo {
	var out []TokenExchangerInfo
	for path, fd := range p.descriptors {
		if fd == nil {
			continue
		}
		dir := strings.TrimSuffix(path, ".proto")
		for _, svc := range fd.Services {
			if !svc.IsTokenMinter {
				continue
			}
			tokenMintMethod, mintServiceTokenMethod, x509MintMethod := tokenMinterRPCMethods(svc.Methods)
			out = append(out, TokenExchangerInfo{
				Package:                fd.Package,
				Dir:                    dir,
				ServiceName:            svc.Name,
				TokenMintMethod:        tokenMintMethod,
				MintServiceTokenMethod: mintServiceTokenMethod,
				X509MintMethod:         x509MintMethod,
				IsProxy:                svc.TokenMinterProxy,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Dir != out[j].Dir {
			return out[i].Dir < out[j].Dir
		}
		return out[i].ServiceName < out[j].ServiceName
	})
	return out
}

func tokenMinterRPCMethods(methods []MethodDescriptor) (tokenMint string, mintServiceToken string, x509Mint string) {
	for _, m := range methods {
		switch {
		case methodSignatureMatches(m, "TokenMintRequest", "TokenMintResponse"):
			tokenMint = m.Name
		case methodSignatureMatches(m, "MintServiceTokenRequest", "TokenMintResponse"):
			mintServiceToken = m.Name
		case methodSignatureMatches(m, "X509MintRequest", "X509CertChain"):
			x509Mint = m.Name
		}
	}
	return tokenMint, mintServiceToken, x509Mint
}

func methodSignatureMatches(m MethodDescriptor, inputBase string, outputBase string) bool {
	return typeNameBase(m.InputType) == inputBase && typeNameBase(m.OutputType) == outputBase
}

func typeNameBase(name string) string {
	name = strings.TrimPrefix(name, ".")
	if name == "" {
		return ""
	}
	parts := strings.Split(name, ".")
	return parts[len(parts)-1]
}

// RestRootPath returns the resolved REST_ROOT_PATH constant from the proto tree.
func (p *DescriptorParser) RestRootPath() string {
	return p.restRootPath
}

// parseFileConstants extracts file-level constants from custom options
func (p *DescriptorParser) parseFileConstants(fd protoreflect.FileDescriptor) []model.Constant {
	var constants []model.Constant

	opts := fd.Options().(*descriptorpb.FileOptions)
	if opts == nil {
		return constants
	}

	// String constants
	if proto.HasExtension(opts, spec.E_StringConst) {
		stringConsts := proto.GetExtension(opts, spec.E_StringConst).([]*spec.StringConst)
		for _, sc := range stringConsts {
			constants = append(constants, model.Constant{
				Name: sc.GetName(),
				// strconv.Quote, not a hand-rolled `"%s"`: the value is
				// proto-author-controlled and lands verbatim in generated
				// source, so a quote or backslash in it would otherwise
				// terminate or corrupt the emitted string literal.
				Value: strconv.Quote(sc.GetValue()),
				Type:  "string",
			})
		}
	}

	// Integer constants
	if proto.HasExtension(opts, spec.E_IntConst) {
		intConsts := proto.GetExtension(opts, spec.E_IntConst).([]*spec.IntConst)
		for _, ic := range intConsts {
			constants = append(constants, model.Constant{
				Name:  ic.GetName(),
				Value: fmt.Sprintf("%d", ic.GetValue()),
				Type:  "int64",
			})
		}
	}

	// Float constants
	if proto.HasExtension(opts, spec.E_FloatConst) {
		floatConsts := proto.GetExtension(opts, spec.E_FloatConst).([]*spec.FloatConst)
		for _, fc := range floatConsts {
			constants = append(constants, model.Constant{
				Name:  fc.GetName(),
				Value: fmt.Sprintf("%f", fc.GetValue()),
				Type:  "float64",
			})
		}
	}

	// Boolean constants
	if proto.HasExtension(opts, spec.E_BoolConst) {
		boolConsts := proto.GetExtension(opts, spec.E_BoolConst).([]*spec.BoolConst)
		for _, bc := range boolConsts {
			constants = append(constants, model.Constant{
				Name:  bc.GetName(),
				Value: fmt.Sprintf("%t", bc.GetValue()),
				Type:  "bool",
			})
		}
	}

	return constants
}

// resolveStringConstant looks up a named string constant across all parsed files.
func (p *DescriptorParser) resolveStringConstant(name string) string {
	for _, fd := range p.descriptors {
		for _, c := range fd.Constants {
			if c.Name == name {
				return c.RawValue()
			}
		}
	}
	return ""
}

// parseGenerateUserApi extracts the generate_user_api file-level option
func (p *DescriptorParser) parseGenerateUserApi(fd protoreflect.FileDescriptor) bool {
	opts := fd.Options().(*descriptorpb.FileOptions)
	if opts == nil {
		return false
	}

	if !proto.HasExtension(opts, spec.E_GenerateUserApi) {
		return false
	}

	return proto.GetExtension(opts, spec.E_GenerateUserApi).(bool)
}

// parseStateImports extracts file-level state_imports options.
func (p *DescriptorParser) parseStateImports(fd protoreflect.FileDescriptor) []string {
	opts := fd.Options().(*descriptorpb.FileOptions)
	if opts == nil {
		return nil
	}

	if !proto.HasExtension(opts, spec.E_StateImports) {
		return nil
	}

	vals := proto.GetExtension(opts, spec.E_StateImports).([]*spec.StateImport)
	var imports []string
	for _, v := range vals {
		if src := v.GetSource(); src != "" {
			imports = append(imports, src)
		}
	}
	return imports
}

func collectFieldStateImports(messages []MessageDescriptor) []FieldStateImport {
	var imports []FieldStateImport
	for _, msg := range messages {
		stateAnn, ok := msg.Annotations["service_state"]
		if !ok || stateAnn.StringValue == "" {
			continue
		}
		for _, field := range msg.Fields {
			importAnn, ok := field.Annotations["state_import"]
			if !ok || importAnn.StringValue == "" {
				continue
			}
			imports = append(imports, FieldStateImport{
				Source:      importAnn.StringValue,
				Root:        StateImportRoot(importAnn.StringValue),
				MessageName: msg.Name,
				FieldName:   field.Name,
			})
		}
	}
	return imports
}

func mergeStateImportRoots(fileImports []string, fieldImports []FieldStateImport) []string {
	seen := make(map[string]bool)
	var roots []string
	for _, root := range fileImports {
		if root == "" || seen[root] {
			continue
		}
		seen[root] = true
		roots = append(roots, root)
	}
	for _, imp := range fieldImports {
		if imp.Root == "" || seen[imp.Root] {
			continue
		}
		seen[imp.Root] = true
		roots = append(roots, imp.Root)
	}
	sort.Strings(roots)
	return roots
}

// StateImportRoot converts a field-level state import source
// "package.StateRoot.field" to its state root "package.StateRoot". An empty
// return means the source is not a field projection.
func StateImportRoot(source string) string {
	if strings.Count(source, ".") < 2 {
		return ""
	}
	idx := strings.LastIndex(source, ".")
	if idx <= 0 || idx == len(source)-1 {
		return ""
	}
	return source[:idx]
}

// parseBoolFileOption extracts a boolean file-level option.
func (p *DescriptorParser) parseBoolFileOption(fd protoreflect.FileDescriptor, ext protoreflect.ExtensionType) bool {
	opts := fd.Options().(*descriptorpb.FileOptions)
	if opts == nil {
		return false
	}
	if !proto.HasExtension(opts, ext) {
		return false
	}
	return proto.GetExtension(opts, ext).(bool)
}

// parseGrpcS2SClients extracts file-level grpc_s2s_client options.
func (p *DescriptorParser) parseGrpcS2SClients(fd protoreflect.FileDescriptor) []string {
	opts := fd.Options().(*descriptorpb.FileOptions)
	if opts == nil {
		return nil
	}
	if !proto.HasExtension(opts, spec.E_GrpcS2SClient) {
		return nil
	}
	vals := proto.GetExtension(opts, spec.E_GrpcS2SClient).([]*spec.GrpcS2SClient)
	var clients []string
	for _, v := range vals {
		if svc := v.GetService(); svc != "" {
			clients = append(clients, svc)
		}
	}
	return clients
}

// RecordRead is one parsed file-level (spec.record_read) declaration: the record
// whose stored rows this service reads, and — when the reader asked for one —
// the client-facing surface it wants emitted over them.
//
// The GRANT halves live on the record itself as (spec.record).s2s_read and
// (spec.record).c2s_read; ValidateRecordReadAnnotations refuses a build where
// either pair disagrees.
type RecordRead struct {
	// Record is the fully-qualified record name, e.g. "messaging.Message".
	Record string
	// C2S is the (spec.record_read).c2s block, or nil when the reader declared
	// none — which is every declaration that predates the field, and the case
	// that must emit exactly what it emitted before.
	C2S *RecordReadC2S
}

// RecordReadC2S is a parsed (spec.record_read).c2s block: which rows the emitted
// client-facing surface may return, and how far outside the cluster it reaches.
type RecordReadC2S struct {
	// Scope is the C2SScope enum value NAME, e.g. "C2S_SCOPE_PRINCIPAL". The
	// name rather than the number so every downstream error message can quote
	// what the author wrote. C2S_SCOPE_UNSPECIFIED is normalized to
	// C2S_SCOPE_PRINCIPAL here — the narrowest value is what absence means.
	Scope string
	// Exposure is the RpcExposure enum value NAME stamped onto the emitted RPCs,
	// e.g. "RPC_EXPOSURE_EXTERNAL".
	Exposure string
}

// parseRecordReads extracts file-level (spec.record_read) options.
func (p *DescriptorParser) parseRecordReads(fd protoreflect.FileDescriptor) []RecordRead {
	opts := fd.Options().(*descriptorpb.FileOptions)
	if opts == nil {
		return nil
	}
	if !proto.HasExtension(opts, spec.E_RecordRead) {
		return nil
	}
	vals := proto.GetExtension(opts, spec.E_RecordRead).([]*spec.RecordRead)
	var reads []RecordRead
	for _, v := range vals {
		rr := RecordRead{Record: v.GetRecord()}
		if c := v.GetC2S(); c != nil {
			scope := c.GetScope()
			if scope == spec.C2SScope_C2S_SCOPE_UNSPECIFIED {
				scope = spec.C2SScope_C2S_SCOPE_PRINCIPAL
			}
			rr.C2S = &RecordReadC2S{
				Scope:    scope.String(),
				Exposure: c.GetExposure().String(),
			}
		}
		reads = append(reads, rr)
	}
	return reads
}

// parseGrpcC2SClients extracts file-level grpc_c2s_client options.
func (p *DescriptorParser) parseGrpcC2SClients(fd protoreflect.FileDescriptor) []string {
	opts := fd.Options().(*descriptorpb.FileOptions)
	if opts == nil {
		return nil
	}
	if !proto.HasExtension(opts, spec.E_GrpcC2SClient) {
		return nil
	}
	vals := proto.GetExtension(opts, spec.E_GrpcC2SClient).([]*spec.GrpcC2SClient)
	var clients []string
	for _, v := range vals {
		if svc := v.GetService(); svc != "" {
			clients = append(clients, svc)
		}
	}
	return clients
}

// parseGrpcW2SClients extracts file-level grpc_w2s_client options.
func (p *DescriptorParser) parseGrpcW2SClients(fd protoreflect.FileDescriptor) []string {
	opts := fd.Options().(*descriptorpb.FileOptions)
	if opts == nil {
		return nil
	}
	if !proto.HasExtension(opts, spec.E_GrpcW2SClient) {
		return nil
	}
	vals := proto.GetExtension(opts, spec.E_GrpcW2SClient).([]*spec.GrpcW2SClient)
	var clients []string
	for _, v := range vals {
		if svc := v.GetService(); svc != "" {
			clients = append(clients, svc)
		}
	}
	return clients
}

// parseOpaImports extracts file-level opa_import options.
// Returns both the source list and the full VirtualImport list.
func (p *DescriptorParser) parseOpaImports(fd protoreflect.FileDescriptor) ([]string, []VirtualImport) {
	opts := fd.Options().(*descriptorpb.FileOptions)
	if opts == nil {
		return nil, nil
	}

	if !proto.HasExtension(opts, spec.E_OpaImport) {
		return nil, nil
	}

	vals := proto.GetExtension(opts, spec.E_OpaImport).([]*spec.OpaImport)
	var imports []string
	var targets []VirtualImport
	for _, v := range vals {
		if src := v.GetSource(); src != "" {
			imports = append(imports, src)
			targets = append(targets, VirtualImport{
				Source:  src,
				Service: v.GetService(),
			})
		}
	}
	return imports, targets
}

// parseTimeseriesStore checks for the file-level spec.timeseries_store option.
func (p *DescriptorParser) parseTimeseriesStore(fd protoreflect.FileDescriptor) bool {
	opts := fd.Options().(*descriptorpb.FileOptions)
	if opts == nil {
		return false
	}
	return proto.HasExtension(opts, spec.E_TimeseriesStore)
}

// parseRecordStore checks for the file-level spec.record_store option.
func (p *DescriptorParser) parseRecordStore(fd protoreflect.FileDescriptor) bool {
	opts := fd.Options().(*descriptorpb.FileOptions)
	if opts == nil {
		return false
	}
	return proto.HasExtension(opts, spec.E_RecordStore)
}

// parseVService extracts the file-level (spec.vservice) virtual-service identity
// (name + field id). Empty name means the file declares no vservice.
func (p *DescriptorParser) parseVService(fd protoreflect.FileDescriptor) (string, int32) {
	opts := fd.Options().(*descriptorpb.FileOptions)
	if opts == nil || !proto.HasExtension(opts, spec.E_Vservice) {
		return "", 0
	}
	val := proto.GetExtension(opts, spec.E_Vservice).(*spec.VServiceAnnotation)
	if val == nil {
		return "", 0
	}
	return val.GetName(), val.GetId()
}

// parseTrait extracts the file-level (spec.trait) declaration. nil means the
// file declares no trait. Twin of parseVService for the identity half: a trait
// carries the same identity shape and lands in the same descriptor fields; the
// remaining fields carry what a trait-driven plane emits (host, the two sibling
// service names, and the S2S audience).
func (p *DescriptorParser) parseTrait(fd protoreflect.FileDescriptor) *TraitDescriptor {
	opts := fd.Options().(*descriptorpb.FileOptions)
	if opts == nil || !proto.HasExtension(opts, spec.E_Trait) {
		return nil
	}
	val := proto.GetExtension(opts, spec.E_Trait).(*spec.TraitAnnotation)
	if val == nil {
		return nil
	}
	return &TraitDescriptor{
		Name:         val.GetName(),
		ID:           val.GetId(),
		Host:         val.GetHost(),
		S2SService:   val.GetS2SService(),
		C2SService:   val.GetC2SService(),
		AuthServices: val.GetAuthServices(),
	}
}

// parseServiceIdentity extracts the file-level service identity option.
// pkg is the proto package name, used as the default service name when
// spec.service has no explicit name because identity comes from the package.
func (p *DescriptorParser) parseServiceIdentity(fd protoreflect.FileDescriptor, pkg string) (string, int32, bool, bool, bool, bool, bool) {
	opts := fd.Options().(*descriptorpb.FileOptions)
	if opts == nil {
		return "", 0, false, false, false, false, false
	}

	if !proto.HasExtension(opts, spec.E_Service) {
		return "", 0, false, false, false, false, false
	}

	val := proto.GetExtension(opts, spec.E_Service).(*spec.ServiceAnnotation)
	if val == nil {
		return "", 0, false, false, false, false, false
	}

	name := val.GetName()
	if name == "" {
		name = pkg
	}
	return name, 0, val.GetStore(), val.GetConfigStore(), val.GetGateway(), val.GetRestDispatch(), val.GetTenantOwned()
}

// parsemodel.ParamMapConfig extracts the generate_from_map message option
func (p *DescriptorParser) parseParamMapConfig(md protoreflect.MessageDescriptor) *model.ParamMapConfig {
	opts := md.Options().(*descriptorpb.MessageOptions)
	if opts == nil {
		return nil
	}

	if !proto.HasExtension(opts, spec.E_GenerateFromMap) {
		return nil
	}

	config := proto.GetExtension(opts, spec.E_GenerateFromMap).(*spec.ParamMapConfig)
	if config == nil {
		return nil
	}

	return &model.ParamMapConfig{
		KeyEnum:          config.GetKeyEnum(),
		Strict:           config.GetStrict(),
		ValidateRequired: config.GetValidateRequired(),
	}
}

func eventPublishBindingEntry(ep *spec.EventPublishAnnotation) model.EventBusBindingEntry {
	if ep == nil {
		return model.EventBusBindingEntry{}
	}
	return model.EventBusBindingEntry{
		TopicRef:  ep.GetTopicRef(),
		MethodS2S: methodS2SEntry(ep.GetMethodS2S()),
		MethodC2S: methodC2SEntry(ep.GetMethodC2S()),
	}
}

func eventWatchBindingEntry(ew *spec.EventWatchAnnotation) model.EventBusBindingEntry {
	if ew == nil {
		return model.EventBusBindingEntry{}
	}
	return model.EventBusBindingEntry{
		TopicRef:  ew.GetTopicRef(),
		MethodS2S: methodS2SEntry(ew.GetMethodS2S()),
		MethodC2S: methodC2SEntry(ew.GetMethodC2S()),
	}
}

func methodS2SEntry(m *spec.MethodS2SAnnotation) *model.EventBusMethodS2S {
	if m == nil {
		return nil
	}
	return &model.EventBusMethodS2S{
		AuthServices:        append([]string(nil), m.GetAuthServices()...),
		AuthDisabled:        m.GetAuthDisabled(),
		RoutingOnlyServices: append([]string(nil), m.GetRoutingOnlyServices()...),
	}
}

func methodC2SEntry(m *spec.MethodC2SAnnotation) *model.EventBusMethodC2S {
	if m == nil {
		return nil
	}
	return &model.EventBusMethodC2S{
		AuthDisabled:         m.GetAuthDisabled(),
		AuthAnyUser:          m.GetAuthAnyUser(),
		TargetNamespaceField: m.GetTargetNamespaceField(),
	}
}

// parseMessageAnnotations extracts message-level annotations from custom options
// redactModeString maps a spec.RedactMode to the canonical data-entitlement mode
// string stamped on the model annotation. REDACT_MODE_UNSPECIFIED (and any
// unknown value) maps to "" so ValidateDataEntitlementAnnotations rejects a
// present-but-modeless annotation loudly instead of silently dropping it.
func redactModeString(m spec.RedactMode) string {
	switch m {
	case spec.RedactMode_REDACT_MODE_DROP:
		return "drop"
	case spec.RedactMode_REDACT_MODE_PSEUDONYMIZE:
		return "pseudonymize"
	case spec.RedactMode_REDACT_MODE_MAY_REDACT:
		return "may_redact"
	case spec.RedactMode_REDACT_MODE_MAY_ANONYMIZE:
		return "may_anonymize"
	default:
		return ""
	}
}

func (p *DescriptorParser) parseMessageAnnotations(md protoreflect.MessageDescriptor) map[string]model.AnnotationValue {
	annotations := make(map[string]model.AnnotationValue)

	opts := md.Options().(*descriptorpb.MessageOptions)
	if opts == nil {
		return annotations
	}

	// service_state: marks a message as a state root.
	if proto.HasExtension(opts, spec.E_ServiceState) {
		val := proto.GetExtension(opts, spec.E_ServiceState).(*spec.StateAnnotation)
		if val != nil {
			// name defaults to the message name when omitted.
			stateName := val.GetName()
			if stateName == "" {
				stateName = string(md.Name())
			}
			annotations["service_state"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: stateName}
			labels := val.GetLabel()
			if len(labels) > 0 {
				annotations["service_state_label"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringSliceValue: labels}
			}
			if val.GetAutoTls() != spec.TlsKeyType_TLS_KEY_TYPE_NONE {
				keyType := strings.ToLower(strings.TrimPrefix(val.GetAutoTls().String(), "TLS_KEY_TYPE_"))
				annotations["service_state_auto_tls"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: keyType}
			}
			if val.GetExport() {
				annotations["service_state_export"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
			}
			if val.GetRequiresArtifactOnWrite() {
				annotations["service_state_requires_artifact_on_write"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
			}
		}
	}

	// State-root defaults for the statedb tuning family no generator consumes.
	// Presence markers only, for the same loud rejection as their field-level
	// counterparts (GEN-ANN-1).
	for name, xt := range unimplementedStatedbMessageExtensions() {
		if proto.HasExtension(opts, xt) {
			annotations[name] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
		}
	}

	// service_event: marks a message as an event source backed by EventBus.
	// When present without service_state, synthesizes service_state from the
	// message name and uses the label as the backing store.
	//
	// A topic is published to exactly one broker, so service_event's label stays
	// singular while service_state's is repeated. The historical fallback (omit
	// the event label, inherit the state one) therefore only resolves when the
	// state root declares exactly one label; more than one has no non-arbitrary
	// answer and is rejected in validateMessageAnnotations.
	if proto.HasExtension(opts, spec.E_ServiceEvent) {
		val := proto.GetExtension(opts, spec.E_ServiceEvent).(*spec.ServiceEventAnnotation)
		if val != nil {
			label := val.GetLabel()
			if label == "" {
				if stateAnn, ok := annotations["service_state_label"]; ok && len(stateAnn.StringSliceValue) == 1 {
					label = stateAnn.StringSliceValue[0]
				}
			}
			if label != "" {
				annotations["service_event_label"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: label}
				if val.GetExport() {
					annotations["service_state_export"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
				}
				// Synthesize service_state when absent — makes service_event standalone.
				if _, hasState := annotations["service_state"]; !hasState {
					annotations["service_state"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: string(md.Name())}
					annotations["service_state_label"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringSliceValue: []string{label}}
				}
			}
		}
	}

	// service_store: marks a message as a durable key-value store.
	if proto.HasExtension(opts, spec.E_ServiceStore) {
		val := proto.GetExtension(opts, spec.E_ServiceStore).(*spec.ServiceStoreAnnotation)
		if val != nil {
			storeName := string(md.Name())
			annotations["service_store"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: storeName}
			labels := val.GetLabel()
			if len(labels) > 0 {
				annotations["service_store_label"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringSliceValue: labels}
			}
		}
	}

	// service_health: declares a typed health message for a service
	if proto.HasExtension(opts, spec.E_ServiceHealth) {
		val := proto.GetExtension(opts, spec.E_ServiceHealth).(*spec.ServiceHealthAnnotation)
		if val != nil {
			annotations["service_health"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
		}
	}

	// per_service_config: marks a message whose fields are per-service config slices.
	if proto.HasExtension(opts, spec.E_PerServiceConfig) {
		if proto.GetExtension(opts, spec.E_PerServiceConfig).(bool) {
			annotations["per_service_config"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
		}
	}

	// owner_default: what the type's OWNER may do with its own rows. Recorded as
	// a presence marker plus the two booleans, so an explicit {read:true,write:true}
	// is distinguishable from no declaration at all.
	if proto.HasExtension(opts, spec.E_OwnerDefault) {
		if od, ok := proto.GetExtension(opts, spec.E_OwnerDefault).(*spec.OwnerDefault); ok && od != nil {
			annotations["owner_default"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
			annotations["owner_default_read"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: od.GetRead()}
			annotations["owner_default_write"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: od.GetWrite()}
		}
	}

	// data_entitlement: message(type)-level default data-entitlement transform for
	// the message's eligible fields. Recorded whenever present (even an empty {})
	// so ValidateDataEntitlementAnnotations can reject an invalid mode loudly.
	if proto.HasExtension(opts, spec.E_DataEntitlement) {
		if de, ok := proto.GetExtension(opts, spec.E_DataEntitlement).(*spec.RedactOption); ok && de != nil {
			annotations["data_entitlement"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: redactModeString(de.GetMode())}
		}
	}

	// grpc_config: marks this message as a gRPC service's configuration
	if proto.HasExtension(opts, spec.E_GrpcConfig) {
		val := proto.GetExtension(opts, spec.E_GrpcConfig).(*spec.GrpcConfigAnnotation)
		if val != nil && val.ServiceName != "" {
			annotations["grpc_config"] = model.AnnotationValue{
				Type:        model.AnnotationTypeString,
				StringValue: val.ServiceName,
			}
		}
	}

	// config_slice: declared-owner per-service config slice annotation.
	if proto.HasExtension(opts, spec.E_ConfigSlice) {
		val := proto.GetExtension(opts, spec.E_ConfigSlice).(*spec.ConfigSliceAnnotation)
		if val != nil && val.Scope != spec.ConfigSliceScope_CONFIG_SLICE_SCOPE_UNSPECIFIED {
			annotations["config_slice_scope"] = model.AnnotationValue{Type: model.AnnotationTypeEnum, EnumValue: val.Scope.String()}
			if val.ServiceName != "" {
				annotations["config_slice_service"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: val.ServiceName}
			}
		}
	}

	// disable_auth_message: no authentication required for this state root's RPCs
	if proto.HasExtension(opts, spec.E_DisableAuthMessage) && proto.GetExtension(opts, spec.E_DisableAuthMessage).(bool) {
		annotations["disable_auth_message"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}

	// subscribe: event-bus topic subscriptions for this state service
	if proto.HasExtension(opts, spec.E_Subscribe) {
		val := proto.GetExtension(opts, spec.E_Subscribe).([]*spec.SubscriptionAnnotation)
		if len(val) > 0 {
			entries := make([]model.SubscriptionEntry, 0, len(val))
			for _, sa := range val {
				entries = append(entries, model.SubscriptionEntry{Source: sa.GetSource()})
			}
			annotations["subscribe"] = model.AnnotationValue{
				Type:                model.AnnotationTypeBool,
				BoolValue:           true,
				SubscriptionEntries: entries,
			}
		}
	}

	// event_publish: first-class EventBus publish bindings for this actor.
	if proto.HasExtension(opts, spec.E_EventPublish) {
		val := proto.GetExtension(opts, spec.E_EventPublish).([]*spec.EventPublishAnnotation)
		if len(val) > 0 {
			entries := make([]model.EventBusBindingEntry, 0, len(val))
			for _, ep := range val {
				entries = append(entries, eventPublishBindingEntry(ep))
			}
			annotations["event_publish"] = model.AnnotationValue{
				Type:                model.AnnotationTypeBool,
				BoolValue:           true,
				EventPublishEntries: entries,
			}
		}
	}

	// event_watch: first-class EventBus watch bindings for this actor.
	if proto.HasExtension(opts, spec.E_EventWatch) {
		val := proto.GetExtension(opts, spec.E_EventWatch).([]*spec.EventWatchAnnotation)
		if len(val) > 0 {
			entries := make([]model.EventBusBindingEntry, 0, len(val))
			for _, ew := range val {
				entries = append(entries, eventWatchBindingEntry(ew))
			}
			annotations["event_watch"] = model.AnnotationValue{
				Type:              model.AnnotationTypeBool,
				BoolValue:         true,
				EventWatchEntries: entries,
			}
		}
	}

	// record: opts this message into EventDB
	if proto.HasExtension(opts, spec.E_Record) {
		val := proto.GetExtension(opts, spec.E_Record).(*spec.RecordAnnotation)
		if val != nil {
			annotations["record"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
			if val.GetKey() != "" {
				annotations["record_key"] = model.AnnotationValue{
					Type:        model.AnnotationTypeString,
					StringValue: val.GetKey(),
				}
			}
			if val.GetOwner() != "" {
				annotations["record_owner"] = model.AnnotationValue{
					Type:        model.AnnotationTypeString,
					StringValue: val.GetOwner(),
				}
			}
			// min_group_size is the smallest result size this record's size-only
			// query surfaces disclose. Carried unconditionally when declared —
			// including a negative value, which ValidateMinGroupSizeAnnotations
			// rejects at generation time rather than silently dropping here.
			if val.GetMinGroupSize() != 0 {
				annotations["record_min_group_size"] = model.AnnotationValue{
					Type:     model.AnnotationTypeInt,
					IntValue: int64(val.GetMinGroupSize()),
				}
			}
			// residency is the record type's two lifecycle clocks (idle ->
			// evicted from memory, deleted-and-aged -> purged). Carried whenever
			// the block is PRESENT, values and all — including a negative one,
			// which ValidateResidencyAnnotations rejects at generation time
			// rather than being silently dropped here.
			if r := residencyPolicyEntry(val.GetResidency()); r != nil {
				annotations["record_residency"] = model.AnnotationValue{
					Type:            model.AnnotationTypeBool,
					BoolValue:       true,
					RecordResidency: r,
				}
			}
			// mutability is an ATTRIBUTE orthogonal to residency. Unset =
			// MUTABILITY_CRUD (today's behaviour), so only a declared value is
			// carried; MUTABILITY_APPEND_ONLY is what suppresses row mutation.
			if m := val.GetMutability(); m != spec.Mutability_MUTABILITY_UNSPECIFIED {
				annotations["record_mutability"] = model.AnnotationValue{
					Type:        model.AnnotationTypeString,
					StringValue: m.String(),
				}
			}
			// s2s_read is the record's STORED-ROW read grant: which services may
			// query this record type's rows through the record query plane's S2S
			// half. Carried whenever the BLOCK is present, including with an empty
			// auth_services list, which ValidateRecordReadAnnotations rejects at
			// generation time rather than being silently dropped here into the
			// trait-wide fallback — a record that meant to name readers and named
			// none must not quietly inherit the whole fleet's list.
			if sr := val.GetS2SRead(); sr != nil {
				annotations["record_s2s_read"] = model.AnnotationValue{
					Type:             model.AnnotationTypeBool,
					BoolValue:        true,
					StringSliceValue: append([]string(nil), sr.GetAuthServices()...),
				}
			}
			// c2s_read is the record's DISCLOSURE grant: which services may
			// re-expose these rows to their own end users. Carried on the same
			// terms as s2s_read above — the BLOCK's presence is the signal, and
			// an empty auth_services list is refused by
			// ValidateRecordReadAnnotations rather than being dropped here.
			if cr := val.GetC2SRead(); cr != nil {
				annotations["record_c2s_read"] = model.AnnotationValue{
					Type:             model.AnnotationTypeBool,
					BoolValue:        true,
					StringSliceValue: append([]string(nil), cr.GetAuthServices()...),
				}
			}
			// private: this record type belongs to exactly one principal. Carried
			// as "record_private" — deliberately NOT "private", which already
			// names the unrelated per-FIELD statedb marker (spec.private = 50013)
			// in a field's own Annotations map. Only carried when true; unset
			// stays today's tenant-boundary-only behaviour.
			if val.GetPrivate() {
				annotations["record_private"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
			}
		}
	}

	// event: marks this message as an EventBus event. spec.record implies
	// spec.event (a record IS an event). The producer surface (c2s/s2s) is read
	// from whichever annotation is present and captured as "event_surface"; the
	// synthesized topic wrapper (AppendEventBusWrappersToProtos) provides the topic
	// and event_publish bindings INHERIT this surface (see eventbus.ValidateBinding).
	hasRecordExt := proto.HasExtension(opts, spec.E_Record)
	if proto.HasExtension(opts, spec.E_Event) || hasRecordExt {
		annotations["event"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
		var c2s *model.EventBusMethodC2S
		var s2s *model.EventBusMethodS2S
		var s2sSubscribe *model.EventBusMethodS2S
		if proto.HasExtension(opts, spec.E_Event) {
			ev := proto.GetExtension(opts, spec.E_Event).(*spec.EventAnnotation)
			if ev != nil {
				c2s = methodC2SEntry(ev.GetC2S())
				s2s = methodS2SEntry(ev.GetS2S())
				s2sSubscribe = methodS2SEntry(ev.GetS2SSubscribe())
			}
		}
		if hasRecordExt {
			rec := proto.GetExtension(opts, spec.E_Record).(*spec.RecordAnnotation)
			if rec != nil {
				if c2s == nil {
					c2s = methodC2SEntry(rec.GetC2S())
				}
				if s2s == nil {
					s2s = methodS2SEntry(rec.GetS2S())
				}
				if s2sSubscribe == nil {
					s2sSubscribe = methodS2SEntry(rec.GetS2SSubscribe())
				}
			}
		}
		if c2s != nil {
			annotations["event_c2s"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
		}
		if s2s != nil {
			annotations["event_s2s"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
		}
		// event_surface carries the declared producer surface (one binding entry,
		// auth only) so the topic registry can attach it to the topic and
		// auth-less event_publish bindings can inherit it.
		annotations["event_surface"] = model.AnnotationValue{
			Type:      model.AnnotationTypeBool,
			BoolValue: true,
			EventPublishEntries: []model.EventBusBindingEntry{{
				MethodC2S:          c2s,
				MethodS2S:          s2s,
				MethodS2SSubscribe: s2sSubscribe,
			}},
		}
	}

	// transformation: declares pure typed conversions of this message into derived
	// row types. Dedup/cardinality_hint are semantic intent; a downstream
	// materializer renders the concrete backend schema. No lifetime here — the
	// record's lifecycle lives on (spec.record).residency.
	if proto.HasExtension(opts, spec.E_Transformation) {
		vals := proto.GetExtension(opts, spec.E_Transformation).([]*spec.Transformation)
		if len(vals) > 0 {
			entries := make([]model.TransformationEntry, 0, len(vals))
			for _, tp := range vals {
				entry := model.TransformationEntry{
					Name:  tp.GetName(),
					When:  tp.GetWhen(),
					Dedup: dedupPolicyEntry(tp.GetDedup()),
				}
				for _, f := range tp.GetField() {
					entry.Fields = append(entry.Fields, model.TransformationFieldEntry{
						Source:          f.GetSource(),
						As:              f.GetAs(),
						OrderKey:        f.GetOrderKey(),
						PartitionKey:    f.GetPartitionKey(),
						CardinalityHint: model.TimeseriesCardinalityHint(f.GetCardinalityHint()),
					})
				}
				entries = append(entries, entry)
			}
			annotations["transformation"] = model.AnnotationValue{
				Type:                  model.AnnotationTypeBool,
				BoolValue:             true,
				TransformationEntries: entries,
			}
		}
	}

	// timeseries_type: opts this message into timeseries table generation
	if proto.HasExtension(opts, spec.E_TimeseriesType) {
		val := proto.GetExtension(opts, spec.E_TimeseriesType).(*spec.TimeseriesTypeAnnotation)
		if val != nil {
			// Mark presence even if name is empty (will default to snake_case of message name)
			annotations["timeseries_type"] = model.AnnotationValue{
				Type:      model.AnnotationTypeBool,
				BoolValue: true,
			}
			if val.GetName() != "" {
				annotations["timeseries_table_name"] = model.AnnotationValue{
					Type:        model.AnnotationTypeString,
					StringValue: val.GetName(),
				}
			}
			if d := dedupPolicyEntry(val.GetDedup()); d != nil {
				annotations["timeseries_dedup"] = model.AnnotationValue{
					Type:            model.AnnotationTypeBool,
					BoolValue:       true,
					TimeseriesDedup: d,
				}
			}
			if val.GetSource() != "" {
				annotations["timeseries_table_source"] = model.AnnotationValue{
					Type:        model.AnnotationTypeString,
					StringValue: val.GetSource(),
				}
			}
		}
	}

	// opa_eval: marks this message as an OPA policy evaluation input
	if proto.HasExtension(opts, spec.E_OpaEval) {
		val := proto.GetExtension(opts, spec.E_OpaEval).(*spec.OpaEvalAnnotation)
		if val != nil && val.GetEntryPoint() != "" {
			annotations["opa_eval"] = model.AnnotationValue{
				Type:        model.AnnotationTypeString,
				StringValue: val.GetEntryPoint(),
			}
			if val.GetResult() != "" {
				annotations["opa_eval_result"] = model.AnnotationValue{
					Type:        model.AnnotationTypeString,
					StringValue: val.GetResult(),
				}
			}
		}
	}

	return annotations
}

// parseFieldAnnotations extracts field-level annotations from custom options
func (p *DescriptorParser) parseFieldAnnotations(fd protoreflect.FieldDescriptor) map[string]model.AnnotationValue {
	annotations := make(map[string]model.AnnotationValue)

	opts := fd.Options().(*descriptorpb.FieldOptions)
	if opts == nil {
		return annotations
	}

	// Boolean annotations
	if proto.HasExtension(opts, spec.E_AssertRequired) && proto.GetExtension(opts, spec.E_AssertRequired).(bool) {
		annotations["assert_required"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
	if proto.HasExtension(opts, spec.E_AssertNetPort) && proto.GetExtension(opts, spec.E_AssertNetPort).(bool) {
		annotations["assert_net_port"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
	if proto.HasExtension(opts, spec.E_AssertEmail) && proto.GetExtension(opts, spec.E_AssertEmail).(bool) {
		annotations["assert_email"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
	if proto.HasExtension(opts, spec.E_AssertFqdn) && proto.GetExtension(opts, spec.E_AssertFqdn).(bool) {
		annotations["assert_fqdn"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
	if proto.HasExtension(opts, spec.E_AssertListenAddr) && proto.GetExtension(opts, spec.E_AssertListenAddr).(bool) {
		annotations["assert_listen_addr"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
	if proto.HasExtension(opts, spec.E_AssertBase64) && proto.GetExtension(opts, spec.E_AssertBase64).(bool) {
		annotations["assert_base64"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
	if proto.HasExtension(opts, spec.E_AssertEnumDefined) && proto.GetExtension(opts, spec.E_AssertEnumDefined).(bool) {
		annotations["assert_enum_defined"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
	if proto.HasExtension(opts, spec.E_AssertSemver) && proto.GetExtension(opts, spec.E_AssertSemver).(bool) {
		annotations["assert_semver"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
	if proto.HasExtension(opts, spec.E_AssertHost) && proto.GetExtension(opts, spec.E_AssertHost).(bool) {
		annotations["assert_host"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
	// Principal annotations: where the caller's principal lives on the type.
	if proto.HasExtension(opts, spec.E_Principal) && proto.GetExtension(opts, spec.E_Principal).(bool) {
		annotations["principal"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
	if proto.HasExtension(opts, spec.E_StorePrincipal) && proto.GetExtension(opts, spec.E_StorePrincipal).(bool) {
		annotations["store_principal"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
	// Namespace annotations: the same two seams bound to the caller's namespace
	// claim instead of its subject — the tenant-isolation backstop.
	if proto.HasExtension(opts, spec.E_Namespace) && proto.GetExtension(opts, spec.E_Namespace).(bool) {
		annotations["namespace"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
	if proto.HasExtension(opts, spec.E_StoreNamespace) && proto.GetExtension(opts, spec.E_StoreNamespace).(bool) {
		annotations["store_namespace"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
	// store_epoch: where the row's last-write clock lives on the type. Same
	// doctrine as the two above — the annotation owns the LOCATION, the generated
	// wrapper owns the stamp.
	if proto.HasExtension(opts, spec.E_StoreEpoch) && proto.GetExtension(opts, spec.E_StoreEpoch).(bool) {
		annotations["store_epoch"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
	// deleted_flag: where the row's soft-delete state lives. Same doctrine again —
	// the annotation owns the LOCATION, the row's own value owns the answer, and a
	// record that declares no marker has no rows to exclude from its queries.
	if proto.HasExtension(opts, spec.E_DeletedFlag) && proto.GetExtension(opts, spec.E_DeletedFlag).(bool) {
		annotations["deleted_flag"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
	// SPIFFE identity annotations (plan D5)
	if proto.HasExtension(opts, spec.E_IdentityNamespace) && proto.GetExtension(opts, spec.E_IdentityNamespace).(bool) {
		annotations["identity_namespace"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
	if proto.HasExtension(opts, spec.E_IdentitySubject) && proto.GetExtension(opts, spec.E_IdentitySubject).(bool) {
		annotations["identity_subject"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
	if proto.HasExtension(opts, spec.E_IdentitySpiffeSubject) && proto.GetExtension(opts, spec.E_IdentitySpiffeSubject).(bool) {
		annotations["identity_spiffe_subject"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
	if proto.HasExtension(opts, spec.E_IdentitySpiffeResource) {
		if val := proto.GetExtension(opts, spec.E_IdentitySpiffeResource).(*spec.IdentitySpiffeResource); val != nil {
			annotations["identity_spiffe_resource"] = model.AnnotationValue{
				Type: model.AnnotationTypeIdentityResource,
				IdentitySpiffeResource: &model.IdentitySpiffeResourceValue{
					NamespaceField: val.GetNamespaceField(),
					Kind:           val.GetKind(),
					IdField:        val.GetIdField(),
				},
			}
		}
	}
	if proto.HasExtension(opts, spec.E_IdentitySpiffeActor) && proto.GetExtension(opts, spec.E_IdentitySpiffeActor).(bool) {
		annotations["identity_spiffe_actor"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
	// Field-level default value annotations
	if proto.HasExtension(opts, spec.E_DefaultString) {
		val := proto.GetExtension(opts, spec.E_DefaultString).(string)
		if val != "" {
			annotations["default_string"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: val}
		}
	}
	if proto.HasExtension(opts, spec.E_DefaultInt) {
		val := proto.GetExtension(opts, spec.E_DefaultInt).(int64)
		annotations["default_int"] = model.AnnotationValue{Type: model.AnnotationTypeInt, IntValue: val}
	}
	if proto.HasExtension(opts, spec.E_DefaultFloat) {
		val := proto.GetExtension(opts, spec.E_DefaultFloat).(float64)
		annotations["default_float"] = model.AnnotationValue{Type: model.AnnotationTypeFloat, FloatValue: val}
	}
	if proto.HasExtension(opts, spec.E_DefaultBool) {
		val := proto.GetExtension(opts, spec.E_DefaultBool).(bool)
		annotations["default_bool"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: val}
	}
	if proto.HasExtension(opts, spec.E_DefaultConst) {
		val := proto.GetExtension(opts, spec.E_DefaultConst).(string)
		if val != "" {
			annotations["default_const"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: val}
		}
	}
	if proto.HasExtension(opts, spec.E_DefaultHost) {
		val := proto.GetExtension(opts, spec.E_DefaultHost).(*spec.DefaultHost)
		if val != nil {
			annotations["default_host"] = model.AnnotationValue{
				Type: model.AnnotationTypeHost,
				HostValue: &model.HostValue{
					Host: val.GetHost(),
					Port: val.GetPort(),
				},
			}
		}
	}

	// Enum annotations
	if proto.HasExtension(opts, spec.E_AssertIpaddrType) {
		val := proto.GetExtension(opts, spec.E_AssertIpaddrType).(spec.IpAddrTypes)
		annotations["assert_ipaddr_type"] = model.AnnotationValue{Type: model.AnnotationTypeEnum, EnumValue: val.String()}
	}
	if proto.HasExtension(opts, spec.E_AssertEncType) {
		val := proto.GetExtension(opts, spec.E_AssertEncType).(spec.EncType)
		annotations["assert_enc_type"] = model.AnnotationValue{Type: model.AnnotationTypeEnum, EnumValue: val.String()}
	}
	if proto.HasExtension(opts, spec.E_AssertUrl) {
		val := proto.GetExtension(opts, spec.E_AssertUrl).(spec.UrlScheme)
		annotations["assert_url"] = model.AnnotationValue{Type: model.AnnotationTypeEnum, EnumValue: val.String()}
	}
	if proto.HasExtension(opts, spec.E_AssertDuration) {
		val := proto.GetExtension(opts, spec.E_AssertDuration).(spec.DurationType)
		annotations["assert_duration"] = model.AnnotationValue{Type: model.AnnotationTypeEnum, EnumValue: val.String()}
	}
	if proto.HasExtension(opts, spec.E_AssertFilePath) {
		val := proto.GetExtension(opts, spec.E_AssertFilePath).(spec.PathType)
		annotations["assert_file_path"] = model.AnnotationValue{Type: model.AnnotationTypeEnum, EnumValue: val.String()}
	}
	if proto.HasExtension(opts, spec.E_AssertSql) {
		val := proto.GetExtension(opts, spec.E_AssertSql).(spec.SqlPermit)
		annotations["assert_sql"] = model.AnnotationValue{Type: model.AnnotationTypeEnum, EnumValue: val.String()}
	}

	// Integer annotations
	if proto.HasExtension(opts, spec.E_AssertDurationMin) {
		val := proto.GetExtension(opts, spec.E_AssertDurationMin).(int64)
		annotations["assert_duration_min"] = model.AnnotationValue{Type: model.AnnotationTypeInt, IntValue: val}
	}
	if proto.HasExtension(opts, spec.E_AssertDurationMax) {
		val := proto.GetExtension(opts, spec.E_AssertDurationMax).(int64)
		annotations["assert_duration_max"] = model.AnnotationValue{Type: model.AnnotationTypeInt, IntValue: val}
	}
	if proto.HasExtension(opts, spec.E_AssertMaxSize) {
		val := proto.GetExtension(opts, spec.E_AssertMaxSize).(uint32)
		annotations["assert_max_size"] = model.AnnotationValue{Type: model.AnnotationTypeInt, IntValue: int64(val)}
	}
	if proto.HasExtension(opts, spec.E_AssertMinSize) {
		val := proto.GetExtension(opts, spec.E_AssertMinSize).(uint32)
		annotations["assert_min_size"] = model.AnnotationValue{Type: model.AnnotationTypeInt, IntValue: int64(val)}
	}
	if proto.HasExtension(opts, spec.E_AssertElementMaxSize) {
		val := proto.GetExtension(opts, spec.E_AssertElementMaxSize).(uint32)
		annotations["assert_element_max_size"] = model.AnnotationValue{Type: model.AnnotationTypeInt, IntValue: int64(val)}
	}
	if proto.HasExtension(opts, spec.E_AssertGt) {
		val := proto.GetExtension(opts, spec.E_AssertGt).(int64)
		annotations["assert_gt"] = model.AnnotationValue{Type: model.AnnotationTypeInt, IntValue: val}
	}
	if proto.HasExtension(opts, spec.E_AssertGe) {
		val := proto.GetExtension(opts, spec.E_AssertGe).(int64)
		annotations["assert_ge"] = model.AnnotationValue{Type: model.AnnotationTypeInt, IntValue: val}
	}
	if proto.HasExtension(opts, spec.E_AssertLt) {
		val := proto.GetExtension(opts, spec.E_AssertLt).(int64)
		annotations["assert_lt"] = model.AnnotationValue{Type: model.AnnotationTypeInt, IntValue: val}
	}
	if proto.HasExtension(opts, spec.E_AssertLe) {
		val := proto.GetExtension(opts, spec.E_AssertLe).(int64)
		annotations["assert_le"] = model.AnnotationValue{Type: model.AnnotationTypeInt, IntValue: val}
	}

	// NumericConstraint
	if proto.HasExtension(opts, spec.E_AssertNumeric) {
		val := proto.GetExtension(opts, spec.E_AssertNumeric).(*spec.NumericConstraint)
		if val != nil {
			constraint := &model.NumericConstraint{}
			if val.Gt != nil {
				v := val.GetGt()
				constraint.Gt = &v
			}
			if val.Ge != nil {
				v := val.GetGe()
				constraint.Ge = &v
			}
			if val.Lt != nil {
				v := val.GetLt()
				constraint.Lt = &v
			}
			if val.Le != nil {
				v := val.GetLe()
				constraint.Le = &v
			}
			if val.Eq != nil {
				v := val.GetEq()
				constraint.Eq = &v
			}
			if val.Ne != nil {
				v := val.GetNe()
				constraint.Ne = &v
			}
			if val.MultipleOf != nil {
				v := val.GetMultipleOf()
				constraint.MultipleOf = &v
			}
			constraint.In = val.GetIn()
			constraint.NotIn = val.GetNotIn()

			annotations["assert_numeric"] = model.AnnotationValue{
				Type:              model.AnnotationTypeNumericConstraint,
				NumericConstraint: constraint,
			}
		}
	}

	// private (bool) - statedb field never shared with other services
	if proto.HasExtension(opts, spec.E_Private) && proto.GetExtension(opts, spec.E_Private).(bool) {
		annotations["private"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}

	// state_import (string) - declares that this field's value comes from another service's state
	if proto.HasExtension(opts, spec.E_StateImport) {
		val := proto.GetExtension(opts, spec.E_StateImport).(string)
		if val != "" {
			annotations["state_import"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: val}
		}
	}

	// key (string) - maps a config field to a service_state or service
	if proto.HasExtension(opts, spec.E_Key) {
		val := proto.GetExtension(opts, spec.E_Key).(string)
		if val != "" {
			annotations["key"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: val}
		}
	}

	// indexed (bool) - marks field as the entity key for state wrappers
	if proto.HasExtension(opts, spec.E_Indexed) && proto.GetExtension(opts, spec.E_Indexed).(bool) {
		annotations["indexed"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}

	// queue (bool) - FIFO queue boundary
	if proto.HasExtension(opts, spec.E_Queue) && proto.GetExtension(opts, spec.E_Queue).(bool) {
		annotations["queue"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}

	// priority_queue (string) - priority queue with named priority field
	if proto.HasExtension(opts, spec.E_PriorityQueue) {
		val := proto.GetExtension(opts, spec.E_PriorityQueue).(string)
		if val != "" {
			annotations["priority_queue"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: val}
		}
	}

	// topic (bool) - pub/sub topic boundary
	if proto.HasExtension(opts, spec.E_Topic) && proto.GetExtension(opts, spec.E_Topic).(bool) {
		annotations["topic"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}

	// query_field - marks a record field as a RecordMap typed filter criterion.
	// Stamps the match op ("eq" or "range") so the record map codegen can emit the
	// typed <T>Filter.
	if proto.HasExtension(opts, spec.E_QueryField) {
		if qf, ok := proto.GetExtension(opts, spec.E_QueryField).(*spec.QueryFieldOption); ok && qf != nil {
			op := "eq"
			if qf.GetOp() == spec.QueryOp_QUERY_OP_RANGE {
				op = "range"
			}
			annotations["query_field"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: op}
		}
	}

	// redact - marks a record field as egress-redactable. Stamps the mode ("drop"/
	// "pseudonymize"/"may_redact"/"may_anonymize") so the record codegen can emit the
	// typed Sanitize<T> transform.
	// Recorded whenever the extension is PRESENT (even when the mode is unspecified,
	// with an empty StringValue) so ValidateRedactAnnotations can reject an invalid
	// mode loudly instead of silently dropping the annotation.
	if proto.HasExtension(opts, spec.E_Redact) {
		if r, ok := proto.GetExtension(opts, spec.E_Redact).(*spec.RedactOption); ok && r != nil {
			annotations["redact"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: redactModeString(r.GetMode())}
		}
	}

	// leader (bool) - per-graph leader election (valid on queue/topic/priority_queue fields)
	if proto.HasExtension(opts, spec.E_Leader) && proto.GetExtension(opts, spec.E_Leader).(bool) {
		annotations["leader"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}

	// epoch_expiration (bool) - marks int64 field as epoch expiration timestamp
	if proto.HasExtension(opts, spec.E_EpochExpiration) && proto.GetExtension(opts, spec.E_EpochExpiration).(bool) {
		annotations["epoch_expiration"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}

	// persistence (bool) - mark boundary for durable storage
	if proto.HasExtension(opts, spec.E_Persistence) && proto.GetExtension(opts, spec.E_Persistence).(bool) {
		annotations["persistence"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}

	// instance_keyed (bool) - server extracts instance_id from caller's token for keying
	if proto.HasExtension(opts, spec.E_InstanceKeyed) && proto.GetExtension(opts, spec.E_InstanceKeyed).(bool) {
		annotations["instance_keyed"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}

	// store_key (bool) - marks field as the entry key for service_store
	if proto.HasExtension(opts, spec.E_StoreKey) && proto.GetExtension(opts, spec.E_StoreKey).(bool) {
		annotations["store_key"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}

	// quorum_write (bool) - requires write quorum (replication to 2+ nodes)
	if proto.HasExtension(opts, spec.E_QuorumWrite) && proto.GetExtension(opts, spec.E_QuorumWrite).(bool) {
		annotations["quorum_write"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}

	// min_durable_copies (int32) - how many distinct nodes must hold the value
	// durably before a quorum_write acks. Carried whenever DECLARED, including a
	// value below 1 and a value on a field with no quorum_write: both are
	// rejected by ValidateMinDurableCopiesAnnotations at generation time rather
	// than silently dropped here into the default.
	if proto.HasExtension(opts, spec.E_MinDurableCopies) {
		annotations["min_durable_copies"] = model.AnnotationValue{
			Type:     model.AnnotationTypeInt,
			IntValue: int64(proto.GetExtension(opts, spec.E_MinDurableCopies).(int32)),
		}
	}

	// collected_health (bool) - marks boundary for Sentinel health collection
	if proto.HasExtension(opts, spec.E_CollectedHealth) && proto.GetExtension(opts, spec.E_CollectedHealth).(bool) {
		annotations["collected_health"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}

	// eviction_max_size (int32) - max entries before eviction; enforced server-side and as client-side safety limit
	if proto.HasExtension(opts, spec.E_EvictionMaxSize) {
		val := proto.GetExtension(opts, spec.E_EvictionMaxSize).(int32)
		if val > 0 {
			annotations["eviction_max_size"] = model.AnnotationValue{Type: model.AnnotationTypeInt, IntValue: int64(val)}
		}
	}

	// Statedb tuning annotations that spec.proto DECLARES (with examples) but no
	// generator consumes. Recorded as presence markers only, so
	// ValidateStateConsistency can reject them loudly instead of the author's
	// durability/eviction intent silently generating nothing (GEN-ANN-1).
	// eviction_max_size, just above, is the one member of this family that IS
	// implemented, so it is not in this list.
	for name, xt := range unimplementedStatedbFieldExtensions() {
		if proto.HasExtension(opts, xt) {
			annotations[name] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
		}
	}

	// command (bool) - mark field as a command RPC
	if proto.HasExtension(opts, spec.E_Command) && proto.GetExtension(opts, spec.E_Command).(bool) {
		annotations["command"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}

	// refresh_on_miss (string) - names the lookup request message type for cache-miss refresh
	if proto.HasExtension(opts, spec.E_RefreshOnMiss) {
		val := proto.GetExtension(opts, spec.E_RefreshOnMiss).(string)
		if val != "" {
			annotations["refresh_on_miss"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: val}
		}
	}

	// FloatConstraint
	if proto.HasExtension(opts, spec.E_AssertFloat) {
		val := proto.GetExtension(opts, spec.E_AssertFloat).(*spec.FloatConstraint)
		if val != nil {
			constraint := &model.FloatConstraint{}
			if val.Gt != nil {
				v := val.GetGt()
				constraint.Gt = &v
			}
			if val.Ge != nil {
				v := val.GetGe()
				constraint.Ge = &v
			}
			if val.Lt != nil {
				v := val.GetLt()
				constraint.Lt = &v
			}
			if val.Le != nil {
				v := val.GetLe()
				constraint.Le = &v
			}
			if val.Epsilon != nil {
				v := val.GetEpsilon()
				constraint.Epsilon = &v
			}
			constraint.In = val.GetIn()
			constraint.NotIn = val.GetNotIn()

			annotations["assert_float"] = model.AnnotationValue{
				Type:            model.AnnotationTypeFloatConstraint,
				FloatConstraint: constraint,
			}
		}
	}

	// required_if_true (string) - field required when named boolean sibling is true
	if proto.HasExtension(opts, spec.E_RequiredIfTrue) {
		val := proto.GetExtension(opts, spec.E_RequiredIfTrue).(string)
		if val != "" {
			annotations["required_if_true"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: val}
		}
	}

	// required_if_false (string) - field required when named boolean sibling is false
	if proto.HasExtension(opts, spec.E_RequiredIfFalse) {
		val := proto.GetExtension(opts, spec.E_RequiredIfFalse).(string)
		if val != "" {
			annotations["required_if_false"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: val}
		}
	}

	// required_if_eq (ConditionalRequirement) - field required when sibling equals value
	if proto.HasExtension(opts, spec.E_RequiredIfEq) {
		val := proto.GetExtension(opts, spec.E_RequiredIfEq).(*spec.ConditionalRequirement)
		if val != nil && val.GetField() != "" && val.GetValue() != "" {
			annotations["required_if_eq"] = model.AnnotationValue{
				Type:        model.AnnotationTypeString,
				StringValue: val.GetField() + "=" + val.GetValue(),
			}
		}
	}

	// view_sources (repeated string) - names the source collection fields for a read-only view
	if proto.HasExtension(opts, spec.E_ViewSources) {
		val := proto.GetExtension(opts, spec.E_ViewSources).([]string)
		if len(val) > 0 {
			annotations["view_sources"] = model.AnnotationValue{
				Type:             model.AnnotationTypeString,
				StringSliceValue: val,
			}
		}
	}

	// view_member (repeated string) — registers collection as a source for one or
	// more cross-boundary views. StringSliceValue holds all view names; StringValue
	// mirrors the first for backward-compatible single-view consumers.
	if proto.HasExtension(opts, spec.E_ViewMember) {
		val := proto.GetExtension(opts, spec.E_ViewMember).([]string)
		if len(val) > 0 {
			annotations["view_member"] = model.AnnotationValue{
				Type:             model.AnnotationTypeString,
				StringValue:      val[0],
				StringSliceValue: val,
			}
		}
	}

	// boundary_callers (repeated string) — services allowed to call this boundary cross-namespace
	if proto.HasExtension(opts, spec.E_BoundaryCallers) {
		val := proto.GetExtension(opts, spec.E_BoundaryCallers).([]string)
		if len(val) > 0 {
			annotations["boundary_callers"] = model.AnnotationValue{
				Type:             model.AnnotationTypeString,
				StringSliceValue: val,
			}
		}
	}

	// projection_source (string) — maps a projection field to its source path on the event message
	if proto.HasExtension(opts, spec.E_ProjectionSource) {
		val := proto.GetExtension(opts, spec.E_ProjectionSource).(string)
		if val != "" {
			annotations["projection_source"] = model.AnnotationValue{
				Type:        model.AnnotationTypeString,
				StringValue: val,
			}
		}
	}

	// projection_record_key (bool) — marks the projection field that carries the
	// source record's key, so a projection query can return record keys and stratum
	// can federate + hydrate the full records from the record_store.
	if proto.HasExtension(opts, spec.E_ProjectionRecordKey) {
		if proto.GetExtension(opts, spec.E_ProjectionRecordKey).(bool) {
			annotations["projection_record_key"] = model.AnnotationValue{
				Type:      model.AnnotationTypeBool,
				BoolValue: true,
			}
		}
	}

	// assert_invariant (string) — formal verification invariant for this field
	if proto.HasExtension(opts, spec.E_AssertInvariant) {
		val := proto.GetExtension(opts, spec.E_AssertInvariant).(string)
		if val != "" {
			annotations["assert_invariant"] = model.AnnotationValue{
				Type:        model.AnnotationTypeString,
				StringValue: val,
			}
		}
	}

	// json_key (string) — override the output JSON key for serialization
	if proto.HasExtension(opts, spec.E_JsonKey) {
		val := proto.GetExtension(opts, spec.E_JsonKey).(string)
		if val != "" {
			annotations["json_key"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: val}
		}
	}

	// json_aliases (repeated string) — additional keys accepted during JSON deserialization
	if proto.HasExtension(opts, spec.E_JsonAliases) {
		val := proto.GetExtension(opts, spec.E_JsonAliases).([]string)
		if len(val) > 0 {
			annotations["json_aliases"] = model.AnnotationValue{
				Type:             model.AnnotationTypeString,
				StringSliceValue: val,
			}
		}
	}

	// timeseries_column (TimeseriesFieldOption) — timeseries column options for this field
	if proto.HasExtension(opts, spec.E_TimeseriesColumn) {
		val := proto.GetExtension(opts, spec.E_TimeseriesColumn).(*spec.TimeseriesFieldOption)
		if val != nil {
			// Mark presence — even an empty {} means "include as column"
			annotations["timeseries_column"] = model.AnnotationValue{
				Type:      model.AnnotationTypeBool,
				BoolValue: true,
			}
			if val.GetOrderKey() > 0 {
				annotations["timeseries_order_key"] = model.AnnotationValue{
					Type:     model.AnnotationTypeInt,
					IntValue: int64(val.GetOrderKey()),
				}
			}
			if val.GetPartitionKey() {
				annotations["timeseries_partition_key"] = model.AnnotationValue{
					Type:      model.AnnotationTypeBool,
					BoolValue: true,
				}
			}
			if val.GetCardinalityHint() != spec.CardinalityHint_CARDINALITY_UNSPECIFIED {
				annotations["timeseries_cardinality_hint"] = model.AnnotationValue{
					Type:     model.AnnotationTypeInt,
					IntValue: int64(val.GetCardinalityHint()),
				}
			}
		}
	}

	return annotations
}

// residencyPolicyEntry converts a spec.ResidencyPolicy into its model form,
// returning nil when the block is absent (both clocks take the platform
// default). A PRESENT block is always carried, values and all: an author who
// wrote the block meant something, and deciding here that a negative number is
// "really" the default is the fail-silent class ValidateResidencyAnnotations
// exists to close.
func residencyPolicyEntry(r *spec.ResidencyPolicy) *model.ResidencyPolicyEntry {
	if r == nil {
		return nil
	}
	return &model.ResidencyPolicyEntry{
		IdleSeconds:          r.GetIdleSeconds(),
		DeletedMaxAgeSeconds: r.GetDeletedMaxAgeSeconds(),
	}
}

// dedupPolicyEntry converts a spec.DedupPolicy into its model form, returning nil
// when the policy is absent.
func dedupPolicyEntry(d *spec.DedupPolicy) *model.DedupPolicyEntry {
	if d == nil {
		return nil
	}
	return &model.DedupPolicyEntry{
		Strategy:     model.TimeseriesDedupStrategy(d.GetStrategy()),
		KeyFields:    append([]string(nil), d.GetKeyFields()...),
		VersionField: d.GetVersionField(),
	}
}

// parseParamKey extracts the param_key field option
func (p *DescriptorParser) parseParamKey(fd protoreflect.FieldDescriptor) string {
	opts := fd.Options().(*descriptorpb.FieldOptions)
	if opts == nil {
		return ""
	}

	if !proto.HasExtension(opts, spec.E_ParamKey) {
		return ""
	}

	return proto.GetExtension(opts, spec.E_ParamKey).(string)
}

// parseJsonReturnValue extracts the json_return_value enum value option
func (p *DescriptorParser) parseJsonReturnValue(evd protoreflect.EnumValueDescriptor) string {
	opts := evd.Options().(*descriptorpb.EnumValueOptions)
	if opts == nil {
		return ""
	}

	if !proto.HasExtension(opts, spec.E_JsonReturnValue) {
		return ""
	}

	return proto.GetExtension(opts, spec.E_JsonReturnValue).(string)
}

// ConvertToProtoFile converts a FileDescriptor to the ProtoFile format
// This allows gradual migration from regex-based parsing to descriptor-based parsing
func (p *DescriptorParser) ConvertToProtoFile(path string) (model.ProtoFile, error) {
	desc, err := p.GetFileDescriptor(path)
	if err != nil {
		return model.ProtoFile{}, err
	}

	protoFile := model.ProtoFile{
		Package:    desc.Package,
		SourcePath: path,
		RelPath:    strings.TrimPrefix(desc.Path, p.protoDir+"/"),
		Enums:      make([]model.Enum, 0, len(desc.Enums)),
		Constants:  desc.Constants,
		Messages:   make([]model.Message, 0, len(desc.Messages)),
		Imports:    make([]model.Import, 0, len(desc.Imports)),
	}

	// Convert imports
	for _, imp := range desc.Imports {
		if strings.HasPrefix(imp, "google/") {
			continue // Skip google imports (protobuf well-known types and google/api)
		}
		normalizedImp := imp
		// Resolve the actual proto package name from the imported file's descriptor.
		// This is important when the file name differs from the package name
		// (e.g., iron-maiden.proto has package ironmaiden).
		baseName := filepath.Base(normalizedImp)
		packageName := strings.TrimSuffix(baseName, ".proto")
		if impDesc, err := p.GetFileDescriptor(normalizedImp); err == nil && impDesc.Package != "" {
			packageName = impDesc.Package
		}
		protoFile.Imports = append(protoFile.Imports, model.Import{
			Path:    normalizedImp,
			Package: packageName,
		})
	}

	// Convert enums
	for _, enumDesc := range desc.Enums {
		enum := model.Enum{
			Name:    enumDesc.Name,
			Comment: enumDesc.Comment,
			Values:  make([]model.EnumValue, 0, len(enumDesc.Values)),
		}
		for _, valDesc := range enumDesc.Values {
			enum.Values = append(enum.Values, model.EnumValue{
				Name:      valDesc.Name,
				Number:    int(valDesc.Number),
				Comment:   valDesc.Comment,
				JsonValue: valDesc.JsonValue,
			})
		}
		protoFile.Enums = append(protoFile.Enums, enum)
	}

	// Convert messages
	for _, msgDesc := range desc.Messages {
		message := model.Message{
			Name:           msgDesc.Name,
			Comment:        msgDesc.Comment,
			Fields:         make([]model.Field, 0, len(msgDesc.Fields)),
			ParamMapConfig: msgDesc.ParamMapConfig,
			Annotations:    msgDesc.Annotations,
		}

		for _, fieldDesc := range msgDesc.Fields {
			field := model.Field{
				Name:        fieldDesc.Name,
				Number:      int(fieldDesc.Number),
				Comment:     fieldDesc.Comment,
				IsRepeated:  fieldDesc.IsRepeated,
				IsOptional:  fieldDesc.IsOptional,
				IsMap:       fieldDesc.IsMap,
				IsOneOf:     fieldDesc.IsOneOf,
				OneOfName:   fieldDesc.OneOfName,
				Annotations: fieldDesc.Annotations,
				ParamKey:    fieldDesc.ParamKey,
				JsonKey:     fieldDesc.JsonKey,
				JsonAliases: fieldDesc.JsonAliases,
			}

			// Handle map fields
			if fieldDesc.IsMap {
				field.IsMessage = false
				field.IsEnum = false
				field.MapKeyType = mapScalarType(fieldDesc.MapKeyType)

				// Handle map value type
				switch fieldDesc.MapValueTypeKind {
				case "message":
					field.MapValueIsMessage = true
					// Parse package and type name for message values
					parts := strings.Split(fieldDesc.MapValueType, ".")
					if len(parts) > 1 {
						pkg := strings.Join(parts[:len(parts)-1], ".")
						// Only set package if it's a cross-package reference
						if pkg != desc.Package {
							field.MapValueTypePackage = parts[len(parts)-2]
						}
						field.MapValueType = parts[len(parts)-1]
					} else {
						field.MapValueType = fieldDesc.MapValueType
					}
				case "enum":
					field.MapValueIsMessage = false
					field.MapValueIsEnum = true
					// Parse package and type name for enum values
					parts := strings.Split(fieldDesc.MapValueType, ".")
					if len(parts) > 1 {
						pkg := strings.Join(parts[:len(parts)-1], ".")
						// Only set package if it's a cross-package reference
						if pkg != desc.Package {
							field.MapValueTypePackage = parts[len(parts)-2]
						}
						field.MapValueType = parts[len(parts)-1]
					} else {
						field.MapValueType = fieldDesc.MapValueType
					}
				case "scalar":
					field.MapValueIsMessage = false
					field.MapValueType = mapScalarType(fieldDesc.MapValueType)
				}

				field.Type = "map"
				field.ProtoType = "map"
				message.Fields = append(message.Fields, field)
				continue
			}

			// Parse type information for non-map fields
			switch fieldDesc.TypeKind {
			case "message":
				field.IsMessage = true
				field.IsEnum = false
				// Parse package and type name from full name (e.g., "key.KeyType" -> package="key", type="KeyType")
				parts := strings.Split(fieldDesc.Type, ".")
				if len(parts) > 1 {
					// Check if this is a same-package reference (possibly nested).
					// For "pkg.Parent.Child", parts[0]=="pkg" matches desc.Package,
					// so it's same-package with Go type "Parent_Child".
					if parts[0] == desc.Package {
						// Same package — join middle parts + last part with "_" for Go nested type
						field.Type = strings.Join(parts[1:], "_")
					} else {
						// Cross-package reference
						pkg := strings.Join(parts[:len(parts)-1], ".")
						// Special handling for google.protobuf well-known types
						// Map them to their pb package names (e.g., google.protobuf.Struct -> structpb)
						if strings.HasPrefix(pkg, "google.protobuf") {
							typeName := parts[len(parts)-1]
							field.TypePackage = strings.ToLower(typeName) + "pb" // e.g., Struct -> structpb
						} else {
							field.TypePackage = parts[0] // Use first part as package for cross-package refs
						}
						field.Type = parts[len(parts)-1] // Last part is type name
					}
				} else {
					field.Type = fieldDesc.Type
				}
				field.ProtoType = field.Type

			case "enum":
				field.IsMessage = false
				field.IsEnum = true
				// Parse package and type name
				parts := strings.Split(fieldDesc.Type, ".")
				if len(parts) > 1 {
					// Check if this is a same-package reference (possibly nested).
					// For "pkg.Parent.Enum", parts[0]=="pkg" matches desc.Package,
					// so it's same-package with Go type "Parent_Enum".
					if parts[0] == desc.Package {
						// Same package — join middle parts + last part with "_" for Go nested type
						field.Type = strings.Join(parts[1:], "_")
					} else {
						pkg := strings.Join(parts[:len(parts)-1], ".")
						if pkg != desc.Package {
							field.TypePackage = parts[0]
						}
						field.Type = parts[len(parts)-1]
					}
				} else {
					field.Type = fieldDesc.Type
				}
				field.ProtoType = field.Type

			case "scalar":
				field.IsMessage = false
				field.IsEnum = false
				// Map protoreflect scalar names to proto type names
				field.Type = mapScalarType(fieldDesc.Type)
				field.ProtoType = field.Type
			}

			message.Fields = append(message.Fields, field)
		}

		protoFile.Messages = append(protoFile.Messages, message)
	}

	return protoFile, nil
}

// mapScalarType maps protoreflect.Kind strings to proto type names
func mapScalarType(reflectType string) string {
	switch reflectType {
	case "double":
		return "double"
	case "float":
		return "float"
	case "int64":
		return "int64"
	case "uint64":
		return "uint64"
	case "int32":
		return "int32"
	case "fixed64":
		return "fixed64"
	case "fixed32":
		return "fixed32"
	case "bool":
		return "bool"
	case "string":
		return "string"
	case "bytes":
		return "bytes"
	case "uint32":
		return "uint32"
	case "sfixed32":
		return "sfixed32"
	case "sfixed64":
		return "sfixed64"
	case "sint32":
		return "sint32"
	case "sint64":
		return "sint64"
	default:
		return reflectType
	}
}

// ConvertToProtoInfo converts a FileDescriptor to ProtoInfo format for service generation.
// This bridges the descriptor-based parser with service generator requirements.
func (p *DescriptorParser) ConvertToProtoInfo(filePath string) (*ProtoInfo, error) {
	// Get the file descriptor (this will parse if needed)
	fileDesc, err := p.GetFileDescriptor(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to get file descriptor: %w", err)
	}

	// Convert to ProtoInfo
	info := &ProtoInfo{
		Package:  fileDesc.Package,
		Services: []*ServiceInfo{},
		Messages: []*MessageInfo{},
		FilePath: filePath,
	}

	// Convert services
	for _, svc := range fileDesc.Services {
		serviceInfo := &ServiceInfo{
			Name:              svc.Name,
			Methods:           []*MethodInfo{},
			AuthDisabled:      svc.AuthDisabled,
			AuthServices:      svc.AuthServices,
			AuthEntitledUsers: svc.AuthEntitledUsers,
			AuthAnyUser:       svc.AuthAnyUser,
			IsC2D:             svc.IsC2D,
			C2DSocketPath:     svc.C2DSocketPath,
			IsC2S:             svc.IsC2S,
			IsS2S:             svc.IsS2S,
			IsSMB:             svc.IsSMB,
			SmbRoot:           svc.SmbRoot,
			Synthesized:       svc.Synthesized,
			SynthesizedKind:   svc.SynthesizedKind,
			HasMcpRpcs:        svc.HasMcpRpcs,
		}

		// Convert methods
		for _, method := range svc.Methods {
			// Extract simple type name from fully qualified name
			inputParts := strings.Split(method.InputType, ".")
			inputSimpleName := inputParts[len(inputParts)-1]

			outputParts := strings.Split(method.OutputType, ".")
			outputSimpleName := outputParts[len(outputParts)-1]

			methodInfo := &MethodInfo{
				Name:                 method.Name,
				InputType:            inputSimpleName,
				OutputType:           outputSimpleName,
				ClientStreaming:      method.ClientStreaming,
				ServerStreaming:      method.ServerStreaming,
				FullInputType:        method.InputType,
				FullOutputType:       method.OutputType,
				HttpMethod:           method.HttpMethod,
				HttpMethods:          method.HttpMethods,
				HttpPath:             method.HttpPath,
				HttpAliasPath:        method.HttpAliasPath,
				HttpBody:             method.HttpBody,
				AuthDisabled:         method.AuthDisabled,
				AuthAnyUser:          method.AuthAnyUser,
				AuthServices:         method.AuthServices,
				AuthActors:           method.AuthActors,
				RequiresDevice:       method.RequiresDevice,
				TargetNamespaceField: method.TargetNamespaceField,
				Collection:           method.Collection,
				MinGroupSize:         method.MinGroupSize,
				IsMcp:                method.IsMcp,
				IsAutoMcp:            method.IsAutoMcp,
				McpToolName:          method.McpToolName,
				Comment:              method.Comment,
			}
			serviceInfo.Methods = append(serviceInfo.Methods, methodInfo)
		}

		info.Services = append(info.Services, serviceInfo)
	}

	// Attach the codegen view (mode/tier/bounds) to every method_collection
	// method before import discovery so resolved PAGE/WATCH item types can add
	// their cross-package imports to generated wrappers.
	ResolveCollections(info.Services, p.collectionMessageIndex())

	// Detect cross-package type references in service methods
	crossPackageTypes := make(map[string]string)
	crossPackageFiles := make(map[string]string)
	// p.descriptors is a MAP and a proto package legitimately spans several
	// files (and several directories — e.g. services/ and vservices/ sharing one
	// package). The scan below records ONE file per package as that package's
	// import location, and CrossPackageFiles feeds the emitted Python import
	// line verbatim (python/types/proto_imports.go). Picking the first raw-map
	// match made that line differ between identical builds, so fix the choice:
	// the lowest descriptor path for the package.
	descPaths := make([]string, 0, len(p.descriptors))
	for descPath := range p.descriptors {
		descPaths = append(descPaths, descPath)
	}
	sort.Strings(descPaths)
	for _, svc := range info.Services {
		for _, method := range svc.Methods {
			fullTypes := []string{method.FullInputType, method.FullOutputType}
			if method.Resolved != nil {
				fullTypes = append(fullTypes, method.Resolved.PageItemType, method.Resolved.WatchItemType, method.Resolved.WatchChangeType)
			}
			for _, fullType := range fullTypes {
				parts := strings.Split(fullType, ".")
				if len(parts) > 1 {
					pkg := strings.Join(parts[:len(parts)-1], ".")
					if pkg != info.Package && pkg != "" && !strings.HasPrefix(pkg, "google.") {
						if _, exists := crossPackageTypes[pkg]; !exists {
							// Look up the directory for this package from parsed descriptors
							for _, descPath := range descPaths {
								if desc := p.descriptors[descPath]; desc.Package == pkg {
									dir := filepath.Dir(descPath)
									if dir == "." {
										dir = ""
									}
									crossPackageTypes[pkg] = dir
									crossPackageFiles[pkg] = descPath
									break
								}
							}
						}
					}
				}
			}
		}
	}
	info.CrossPackageTypes = crossPackageTypes
	info.CrossPackageFiles = crossPackageFiles

	// Classify every RPC into a verb/pattern/access (archive/RPC_PATTERNS_AND_VERBS_PLAN.md
	// §2-§3) and attach it for entitlements. This is the EnforceVerbs gate:
	// unclassified generated RPC surfaces would otherwise omit entitlement
	// metadata or fall back to broad ANY grants.
	var classificationFailures []string
	ClassifyMethods(info.Services, func(svc, method string, err error) {
		classificationFailures = append(classificationFailures, fmt.Sprintf("%s.%s: %v", svc, method, err))
	})
	if len(classificationFailures) > 0 {
		return nil, fmt.Errorf("rpc verb/pattern classification failed for %d method(s): %s",
			len(classificationFailures), strings.Join(classificationFailures, "; "))
	}

	// (spec.record).mutability = MUTABILITY_APPEND_ONLY, generation end: no
	// service may declare a row-mutating RPC over an append-only record. Runs
	// here, immediately after classification, because the refusal is defined on
	// the resolved verb PATTERN and not on an RPC name.
	if err := validateAppendOnlyMethods(info.Services, appendOnlyRecordTypes(p)); err != nil {
		return nil, err
	}

	// Convert messages
	for _, msg := range fileDesc.Messages {
		messageInfo := &MessageInfo{
			Name:   msg.Name,
			Fields: []*FieldInfo{},
		}

		// Convert fields
		for _, field := range msg.Fields {
			fieldInfo := &FieldInfo{
				Name:     field.Name,
				Type:     field.Type,
				Number:   int(field.Number),
				Repeated: field.IsRepeated,
			}
			messageInfo.Fields = append(messageInfo.Fields, fieldInfo)
		}

		info.Messages = append(info.Messages, messageInfo)
	}

	return info, nil
}
