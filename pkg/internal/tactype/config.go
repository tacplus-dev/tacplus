// Copyright (c) 2026 Second Star Cavalier LLC

package tactype

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Config holds the configuration for code generation
type Config struct {
	// === Build Configuration ===
	ProtoDir string            // Directory containing proto files and dependencies.yaml
	Lang     LanguagesConfig   // Language-specific package configurations
	Tools    map[string]string // Tool versions from dependencies.yaml
	Verbose  bool              // Verbose output during build/installation

	// === Path Configuration ===
	Language  string // Current language being generated (internal use)
	InputDir  string // Input directory with proto files
	OutputDir string // Output directory for generated code
	// OutputName is the name of the generated-output package/dir segment — the
	// trailing segment appended to the package base (e.g. "<pkg>-model",
	// "<pkg>_model", ".model", "/model") and the conventional output subdir.
	// Defaults to DefaultOutputName ("model"); overridable via tacplus.yaml
	// output-name. The compiler is still "tacplus"; only the generated model is
	// named. Use OutputNameOrDefault() to read it.
	OutputName   string
	ProjectDir   string // Project root directory (CWD)
	DepsYAMLPath string // Path to dependencies.yaml file for dependency management
	FlatProtoDir string // De-circularized flat copy of protos for protoc-based parsing (set by builder)
	// AnnotatedProtoDir is the proto directory with tacplus-injected services and
	// spec.* annotations still present (unlike FlatProtoDir, where annotations are
	// stripped). Set by language generators that need to read annotations (e.g.
	// (spec.service_event)/(spec.topic) for EventBus publisher discovery).
	AnnotatedProtoDir string

	// === Protoc Configuration ===
	ProtocPath         string // Path to protoc compiler
	ProtocGenGo        string // Path to protoc-gen-go plugin
	ProtocGenTs        string // Path to protoc-gen-es plugin
	ProtocGenRs        string // Path to protoc-gen-prost plugin
	ProtocGenSwift     string // Path to protoc-gen-swift plugin
	ProtocGenGrpcSwift string // Path to protoc-gen-grpc-swift-2 plugin

	// === Generation Options ===
	LocalMode  bool   // Local development mode - skip require statements in go.mod
	VersionTag string // Version tag for generated artifacts (e.g., "1.0.1" or "v1.0.1")

	// === Resolved Service Surfaces ===
	// GrpcServers is populated from proto service annotations.
	GrpcServers []GrpcEntry
	// GrpcS2SClients is populated from spec.grpc_s2s_client proto options.
	GrpcS2SClients []GrpcEntry
	// GrpcC2SClients is populated from spec.grpc_c2s_client proto options.
	GrpcC2SClients []GrpcEntry
	// GrpcW2SClients is populated from spec.grpc_w2s_client proto options.
	GrpcW2SClients []GrpcEntry
	// GrpcC2DClients is populated from service_c2d annotations.
	GrpcC2DClients []GrpcEntry
	// GrpcServerAll requests all annotated server surfaces for internal generators.
	GrpcServerAll bool
	// GrpcS2SClientAll requests all annotated S2S clients from proto options.
	GrpcS2SClientAll bool
	// RestClients is populated from REST-capable proto service annotations.
	RestClients []GrpcEntry
	// RestClientAll requests all annotated REST-capable clients.
	RestClientAll bool
	// States is populated from service_state, field-level state_import, and generated runtime state.
	States []StateEntry
	// HealthStateName is the message name of the tacplus-injected service-health
	// state (set by InjectHealthState; empty when none was injected, e.g. a
	// sys-only build or when the user already declares a same-named state).
	// Emitters compare a discovered state root against this instead of the
	// "ServiceHealth" literal (OF-4).
	HealthStateName string
	// AllStates indicates internal store-service access to all discovered state roots.
	AllStates bool

	// === Service Identity ===
	// SelfPackage is the proto package name of the service being generated
	// (e.g. "gateway", "identity"). Used to identify the service's own proto
	// package for self-referencing logic.
	SelfPackage string
	// IsGateway is true when the service's proto has option (spec.service).gateway = true.
	// Changes TLS behavior: gateway watches GatewayTlsState for public certs instead of
	// using X509Mint auto-cert. Populated during proto parsing, not from tacplus.yaml.
	IsGateway bool
	// HasRestDispatch is true when the service's proto has option (spec.service).rest_dispatch = true.
	// Generates REST→gRPC dispatch code (route table, dispatch functions, axum handler).
	HasRestDispatch bool

	// === Service Manager Prefix ===
	// SmPrefix controls the output directory for service-manager code.
	// Defaults to "s2sm"; set to "c2sm" or "w2sm" when generating those variants.
	SmPrefix string

	// === Sys-Only Mode ===
	// SysOnly is true when no proto source is configured (no src in tacplus.yaml).
	// In this mode only the standard library wrappers are generated; no service
	// protos exist so identity-manager and service-manager generation are skipped.
	SysOnly bool

	// === Event-Bus Subscriptions ===
	// Subscriptions are populated from (spec.subscribe) annotations on state root messages.
	// Each entry describes an event-bus topic this service consumes.
	Subscriptions []SubscriptionEntry

	// === Registry Configuration ===
	Registry RegistryConfig // Package registry configuration (GitLab project ID, etc.)

	// === Language-Specific Package Paths ===
	ModuleBasePath    string // Base module path for Go packages (e.g., "github.com/myorg/myproject")
	CrateBasePath     string // Base crate path for Rust packages (e.g., "myorg_myproject")
	JavaPackageBase   string // Base package for Java (e.g., "com.myorg.myproject")
	PythonPackageBase string // Base package for Python (e.g., "myorg.myproject")
	TsPackageBase     string // Base package for TypeScript/npm (e.g., "@myorg/myproject")
	SwiftPackageBase  string // Swift package/module name (e.g., "MyProjectTacplus")
	CSharpPackageBase string // C# assembly/root namespace (e.g., "MyProjectTacplus")
}

// GrpcEntry specifies a gRPC service to generate wrappers for.
type GrpcEntry struct {
	ProtoFile     string // e.g., "services/baseball.proto"
	ServiceName   string // e.g., "BaseballService"
	InstanceName  string // e.g., "House" (empty = use ServiceName)
	AuthDisabled  bool   // skip authorization on the generated server wrapper
	TargetService string // physical service hosting this gRPC service (for virtual services, e.g., "opa" for OpaEval)
	ProtoPackage  string // explicit proto package when it differs from the filename (e.g. "userentitlements" for vservices/entitlements.proto)
}

// AccessMode controls which operations are available on a state state root.
type AccessMode string

const (
	AccessRW   AccessMode = "rw"   // full read-write access (get, set, lock, delete)
	AccessRO   AccessMode = "ro"   // read-only access (get, watch only)
	AccessWORM AccessMode = "worm" // write-once read-many (get, set, add, publish, offer — no lock/delete)
)

// StateEntry specifies a state root to manage with a given access mode.
type StateEntry struct {
	ProtoFile      string     // e.g., "services/baseball.proto"
	MessageName    string     // e.g., "BaseballState"
	InstanceName   string     // e.g., "House" (empty = use MessageName)
	AccessMode     AccessMode // "rw", "ro", or "worm"
	AutoTlsKeyType string     // key algorithm: "ed25519", "ecdsa", or "" (disabled)
	// IsHealthState marks the tacplus-injected service-health state (see
	// InjectHealthState). Emitters key health-specific behavior (read-only for
	// non-owners, trace-publisher gating) off this marker rather than the
	// "ServiceHealth" message name, so a user state coincidentally named
	// ServiceHealth behaves like any other user state (OF-4).
	IsHealthState bool
}

// SubscriptionEntry holds parsed (spec.subscribe) metadata for an event-bus topic.
type SubscriptionEntry struct {
	SourcePackage   string // e.g. "aegis"
	SourceStateRoot string // e.g. "AegisState"
	SourceField     string // e.g. "event_stream"
	TargetService   string // broker service_id that hosts the EventBus typed service
	SourceTypeFqn   string // e.g. "aegis.TopologyEvent" — resolved from proto
	SourceProtoDir  string // e.g. "services", "common" — proto directory where source was found
	// SourceIsRecord is true when SourceTypeFqn carries (spec.record), i.e. the
	// topic delivers a STORED record. A record's rows land in a node-local store,
	// so that selects a per-INSTANCE consumer group for the topic — see
	// javaSubscriptionEntry.PerInstanceConsumerGroup.
	SourceIsRecord bool
	// SourceStoreEpochField is SourceTypeFqn's (spec.store_epoch) field name ("" when
	// the type declares none). A record has no write RPC, so this subscription IS
	// the write path into the store and the dispatch is where the store's clock is
	// stamped — see spec.proto's store_epoch comment.
	SourceStoreEpochField string
}

// GetSmPrefix returns the service-manager directory prefix ("s2sm", "c2sm", "c2dm", or "w2sm").
// Defaults to "s2sm" if not set.
func (c *Config) GetSmPrefix() string {
	if c.SmPrefix == "" {
		return "s2sm"
	}
	return c.SmPrefix
}

// AccessModeForProto returns the access mode for a given proto file based on resolved states.
// Returns AccessRW as default if no state entry matches.
func (c *Config) AccessModeForProto(protoFile string) AccessMode {
	for _, b := range c.States {
		if b.ProtoFile == protoFile {
			return b.AccessMode
		}
	}
	if c.AllStates {
		return AccessRO
	}
	return AccessRW
}

// AccessModeForState returns the access mode for an exact state root.
// The boolean is false when the state root should not be generated for this
// package. Store/all-state builds include unmatched roots as read-only unless
// ResolveStatesFromProto populated a more specific entry.
func (c *Config) AccessModeForState(protoFile, messageName string) (AccessMode, bool) {
	for _, b := range c.States {
		if b.ProtoFile == protoFile && b.MessageName == messageName {
			return b.AccessMode, true
		}
	}
	if c.AllStates {
		return AccessRO, true
	}
	return AccessRW, false
}

// HasAutoTls returns true if any state entry has auto_tls enabled.
func (c *Config) HasAutoTls() bool {
	for _, s := range c.States {
		if s.AutoTlsKeyType != "" {
			return true
		}
	}
	return false
}

// AutoTlsKeyType returns the key type for the first state entry with auto_tls enabled.
func (c *Config) AutoTlsKeyType() string {
	for _, s := range c.States {
		if s.AutoTlsKeyType != "" {
			return s.AutoTlsKeyType
		}
	}
	return ""
}

// ResolveProtocPaths finds the protoc compiler and plugins using priority:
// 1. CLI flag (if set via ProtocPath, ProtocGenGo, etc.)
// 2. Environment variable (PROTOC_BINARY, PROTOC_GO_BINARY, etc.)
// 3. PATH lookup
func (c *Config) ResolveProtocPaths() error {
	// Find protoc if not specified
	if c.ProtocPath == "" {
		path, err := ResolveProtocBinary()
		if err != nil {
			return err
		}
		c.ProtocPath = path
	}

	// Verify the protoc path exists
	if _, err := os.Stat(c.ProtocPath); err != nil {
		return fmt.Errorf("protoc not found at %s: %w", c.ProtocPath, err)
	}

	// Resolve language-specific plugin paths
	return c.resolveLanguagePlugin()
}

// ResolveProtocBinary finds the protoc compiler using priority:
// 1. PROTOC_BINARY environment variable
// 2. PATH lookup
//
// This is the single resolution mechanism callers should use to locate protoc,
// so that pinning it via PROTOC_BINARY (e.g. to match a service's pinned
// protobuf.version) takes effect everywhere protoc is invoked, not just in
// per-language plugin generation.
func ResolveProtocBinary() (string, error) {
	if envPath := os.Getenv("PROTOC_BINARY"); envPath != "" {
		return envPath, nil
	}
	path, err := exec.LookPath("protoc")
	if err != nil {
		return "", fmt.Errorf("protoc compiler not found in PATH. Please install protoc, set PROTOC_BINARY env var, or specify --protoc-binary flag")
	}
	return path, nil
}

// ServiceLabel returns a short human-readable identifier for this service,
// derived from the last path component of the module base path (minus "/tacplus").
// e.g. "gitlab.com/org/services/identity/tacplus" → "identity"
// Falls back to Pkg when ModuleBasePath is not set (e.g. Rust services).
func (c *Config) ServiceLabel() string {
	base := strings.TrimSuffix(c.ModuleBasePath, "/"+c.OutputNameOrDefault())
	label := filepath.Base(base)
	if label == "." || label == "" {
		return c.SelfPackage
	}
	return label
}

// DefaultOutputName is the default generated-output package/dir segment. The
// compiler is "tacplus"; the generated model it emits is named "model".
const DefaultOutputName = "model"

// OutputNameOrDefault returns the configured generated-output segment name,
// defaulting to DefaultOutputName ("model") when unset.
func (c *Config) OutputNameOrDefault() string {
	if c.OutputName != "" {
		return c.OutputName
	}
	return DefaultOutputName
}

// This is just the ModuleBasePath with no assumptions or modifications
func (c *Config) TypesModulePath() string {
	return c.ModuleBasePath
}

// FormattedVersion returns the version tag formatted for use in go.mod files
// Version tag is required, so this function just ensures it has a 'v' prefix
// (e.g., "1.0.1" -> "v1.0.1")
func (c *Config) FormattedVersion() string {
	// Ensure version starts with 'v'
	if !strings.HasPrefix(c.VersionTag, "v") {
		return "v" + c.VersionTag
	}
	return c.VersionTag
}

// PEP440Version returns the version formatted for Python PEP 440 compliance.
// Converts "0.0.0-SNAPSHOT" to "0.0.0.dev0" since PEP 440 doesn't allow hyphens.
func (c *Config) PEP440Version() string {
	v := strings.TrimPrefix(c.VersionTag, "v")
	v = strings.Replace(v, "-SNAPSHOT", ".dev0", 1)
	return v
}

// UtilsPath returns the utils module path (validation helpers, DecodeBase64, etc.)
// With single-module layout, utils package is at {base}/utils (sibling to types/)
func (c *Config) UtilsPath() string {
	return c.TypesModulePath() + "/utils"
}

// ErrorsPath returns the errors module path (error constructors, Error proto type)
// With single-module layout, errors package is at {base}/types/sys/errors
func (c *Config) ErrorsPath() string {
	return c.TypesModulePath() + "/types/sys/errors"
}

// TypesImportPath returns the import path for a generated types package.
// With the single-module layout, types are at {base}/types/{parentDir}/{packageName}
func (c *Config) TypesImportPath(parentDir, packageName string) string {
	if filepath.Base(parentDir) == packageName {
		return fmt.Sprintf("%s/types/%s", c.ModuleBasePath, parentDir)
	}
	return fmt.Sprintf("%s/types/%s/%s", c.ModuleBasePath, parentDir, packageName)
}

// LangDir returns the language root directory.
// OutputDir is the language root (e.g., {configDir}/{lang}), so LangDir returns it directly.
func (c *Config) LangDir() string {
	return c.OutputDir
}

// pluginConfig holds configuration for a language-specific protoc plugin
type pluginConfig struct {
	currentPath *string // pointer to the config field to update
	envVar      string  // environment variable name
	binaryName  string  // binary name to look up in PATH
	installMsg  string  // installation instructions
}

// resolveLanguagePlugin resolves language-specific protoc plugin paths
func (c *Config) resolveLanguagePlugin() error {
	// Map of language to plugin configuration
	plugins := map[string]pluginConfig{
		"go": {
			currentPath: &c.ProtocGenGo,
			envVar:      "PROTOC_GO_BINARY",
			binaryName:  "protoc-gen-go",
			installMsg:  "Install with: go install google.golang.org/protobuf/cmd/protoc-gen-go@latest, or set PROTOC_GO_BINARY env var",
		},
		"rust": {
			currentPath: &c.ProtocGenRs,
			envVar:      "PROTOC_RUST_BINARY",
			binaryName:  "protoc-gen-prost",
			installMsg:  "Install with: cargo install protoc-gen-prost, or set PROTOC_RUST_BINARY env var",
		},
	}

	// Special handling for TypeScript (has fallback to npx)
	if c.Language == "typescript" {
		if c.ProtocGenTs == "" {
			if envPath := os.Getenv("PROTOC_TS_BINARY"); envPath != "" {
				c.ProtocGenTs = envPath
			} else if path, err := exec.LookPath("protoc-gen-es"); err == nil {
				c.ProtocGenTs = path
			} else if path, err := exec.LookPath("npx"); err == nil {
				c.ProtocGenTs = filepath.Join(filepath.Dir(path), "protoc-gen-es")
			} else {
				return fmt.Errorf("protoc-gen-es not found. Install with: npm install -g @bufbuild/protoc-gen-es, or set PROTOC_TS_BINARY env var")
			}
		}
		return nil
	}

	// Swift needs both message and gRPC plugins.
	if c.Language == "swift" {
		if c.ProtocGenSwift == "" {
			if envPath := os.Getenv("PROTOC_SWIFT_BINARY"); envPath != "" {
				c.ProtocGenSwift = envPath
			} else if path, err := exec.LookPath("protoc-gen-swift"); err == nil {
				c.ProtocGenSwift = path
			} else {
				return fmt.Errorf("protoc-gen-swift not found in PATH. Install with: swift build --product protoc-gen-swift, or set PROTOC_SWIFT_BINARY env var")
			}
		}
		if c.ProtocGenGrpcSwift == "" {
			if envPath := os.Getenv("PROTOC_GRPC_SWIFT_BINARY"); envPath != "" {
				c.ProtocGenGrpcSwift = envPath
			} else if path, err := exec.LookPath("protoc-gen-grpc-swift-2"); err == nil {
				c.ProtocGenGrpcSwift = path
			} else {
				return fmt.Errorf("protoc-gen-grpc-swift-2 not found in PATH. Install with: swift build --product protoc-gen-grpc-swift-2, or set PROTOC_GRPC_SWIFT_BINARY env var")
			}
		}
		return nil
	}

	// C# uses Grpc.Tools at dotnet build time, so tacplus does not resolve
	// or invoke a C# protoc plugin during generation.
	if c.Language == "csharp" {
		return nil
	}

	// Java and Python are built into protoc
	if c.Language == "java" || c.Language == "python" {
		return nil
	}

	// Handle standard plugins (Go, Rust)
	if plugin, ok := plugins[c.Language]; ok {
		if *plugin.currentPath == "" {
			// Try environment variable first
			if envPath := os.Getenv(plugin.envVar); envPath != "" {
				*plugin.currentPath = envPath
			} else {
				// Try to find in PATH
				path, err := exec.LookPath(plugin.binaryName)
				if err != nil {
					return fmt.Errorf("%s not found in PATH. %s", plugin.binaryName, plugin.installMsg)
				}
				*plugin.currentPath = path
			}
		}
	}

	return nil
}
