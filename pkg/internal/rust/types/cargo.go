// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/packages"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/tmplutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"github.com/tacplus-dev/tacplus/templates"
)

// cargoLibCrate describes a workspace lib crate to add as a dependency.
type cargoLibCrate struct {
	Name string
}

// cargoWorkspaceData holds data for the workspace Cargo.toml template.
type cargoWorkspaceData struct {
	Members            []string
	DepsToml           string
	CrateName          string
	HasServiceWrappers bool
	HasGrpcClients     bool
	HasW2SClients      bool
	HasSubscriptions   bool
	HasAutoTls         bool
	AutoTlsKeyType     string
	HasSmb             bool
	SkipWorkspace      bool
	HasMcpRpcs         bool
	RmcpVersion        string
	SchemarsVersion    string
	LibCrates          []cargoLibCrate
}

// typesCargoData holds data for the types-only crate Cargo.toml.
type typesCargoData struct {
	CrateName          string
	HasServiceWrappers bool
}

// createWorkspaceCargoToml creates a combined workspace + package Cargo.toml at rust/Cargo.toml.
// This is analogous to Java's parent pom.xml, TypeScript's root package.json, etc.
// It defines workspace members and shared dependency versions so that individual
// crate Cargo.toml files can use { workspace = true }, and also includes the
// [package] section for the root types crate.
func createWorkspaceCargoToml(config *tactype.Config, outputDir string, hasServiceWrappers bool, hasSubscriptions bool, hasMcpRpcs bool) error {
	// outputDir is the rust root directory (e.g., rust/)
	rustRootDir := outputDir
	cargoPath := filepath.Join(rustRootDir, "Cargo.toml")

	// Check if a parent directory defines a workspace (e.g., service root Cargo.toml).
	// If so, skip generating [workspace] — the parent owns the workspace definition.
	skipWorkspace := parentHasWorkspace(rustRootDir)
	if skipWorkspace {
		zap.L().Info("Parent Cargo.toml defines workspace, skipping [workspace] generation", zap.String("path", cargoPath))
	} else {
		zap.L().Info("Creating workspace Cargo.toml", zap.String("path", cargoPath))
	}

	// Load package registry for dependency versions
	registry, err := packages.GetPackageRegistry()
	if err != nil {
		return fmt.Errorf("failed to load package registry: %w", err)
	}

	// Build workspace members list -- "." refers to the root package itself.
	// "types" is the types-only crate that lib crates depend on (avoiding cycles).
	var members []string
	members = append(members, ".")
	members = append(members, "types")

	// Check for sys/ directory (library crates like httpclient, logger, etc.)
	// Only include subdirectories that have a Cargo.toml (i.e., actual crates).
	var libCrates []cargoLibCrate
	if entries, err := os.ReadDir(filepath.Join(rustRootDir, "chassis")); err == nil {
		for _, entry := range entries {
			if entry.IsDir() {
				cargoToml := filepath.Join(rustRootDir, "chassis", entry.Name(), "Cargo.toml")
				if _, err := os.Stat(cargoToml); err == nil {
					members = append(members, "chassis/"+entry.Name())
					libCrates = append(libCrates, cargoLibCrate{Name: entry.Name()})
				}
			}
		}
	}

	// Build workspace dependencies from the package registry
	// These are the dependencies that crates reference with { workspace = true }
	allDeps := registry.GetAllRustDependencies()

	// Core dependencies that the types crate always needs
	coreDeps := map[string]string{
		"prost":           "",
		"prost-types":     "",
		"prost-wkt-types": "",
		"pbjson":          "",
		"pbjson-types":    "",
		"bytes":           "",
		"serde":           "",
		"serde_json":      "",
		"serde_yaml":      "",
		"toml":            "",
		"regex":           "",
	}

	// Service-related dependencies
	serviceDeps := map[string]string{
		"tonic":   "",
		"axum":    "",
		"tracing": "",
		"tokio":   "",
		"tower":   "",
	}

	// Merge all into a single map with versions from the registry
	workspaceDeps := make(map[string]string)
	for dep := range coreDeps {
		if version, ok := allDeps[dep]; ok {
			workspaceDeps[dep] = version
		}
	}
	hasGrpcClients := len(config.GrpcS2SClients) > 0 ||
		config.GrpcS2SClientAll ||
		len(config.GrpcC2SClients) > 0 ||
		len(config.GrpcC2DClients) > 0
	hasSmb, _, err := detectSmbRuntimeMetadata(config)
	if err != nil {
		return err
	}
	// A state makes the service manager, and its health-only run() binds a TLS
	// listener even with no TLS gRPC service (see handler_gen.go HasAutoTls), so
	// a state alone also needs the cert crates.
	hasAutoTls := config.HasAutoTls() || hasServiceWrappers || len(config.States) > 0

	if hasServiceWrappers || hasGrpcClients {
		for dep := range serviceDeps {
			if version, ok := allDeps[dep]; ok {
				workspaceDeps[dep] = version
			}
		}
	}

	hasW2SClients := len(config.GrpcW2SClients) > 0
	if hasW2SClients {
		for _, dep := range []string{"tokio-tungstenite"} {
			if _, ok := allDeps[dep]; !ok {
				return fmt.Errorf("rust W2S clients require %s in dependencies.yaml", dep)
			}
		}
	}

	requiredDeps, err := requiredWorkspaceDependencyRefs(rustRootDir, hasServiceWrappers, hasGrpcClients, hasW2SClients, hasAutoTls || hasSmb)
	if err != nil {
		return err
	}
	var missingDeps []string
	for dep := range requiredDeps {
		if _, ok := allDeps[dep]; !ok {
			missingDeps = append(missingDeps, dep)
		}
	}
	if len(missingDeps) > 0 {
		sort.Strings(missingDeps)
		return fmt.Errorf("rust dependencies.yaml missing required workspace dependency versions: %s", strings.Join(missingDeps, ", "))
	}

	// Also add any remaining deps from the registry that crates might reference
	for dep, version := range allDeps {
		if _, exists := workspaceDeps[dep]; !exists {
			workspaceDeps[dep] = version
		}
	}

	// Sort dependency names for deterministic output
	var depNames []string
	for name := range workspaceDeps {
		depNames = append(depNames, name)
	}
	sort.Strings(depNames)

	// Build workspace dependencies TOML
	var depsToml strings.Builder

	// Special handling for crates that need features
	specDeps := map[string]string{
		"serde":              `{ version = "%s", features = ["derive"] }`,
		"tonic":              `{ version = "%s", features = ["transport"] }`,
		"tokio":              `{ version = "%s", features = ["full"] }`,
		"tower-http":         `{ version = "%s", features = ["cors", "trace"] }`,
		"tracing-subscriber": `{ version = "%s", features = ["env-filter", "json"] }`,
		"reqwest":            `{ version = "%s", features = ["json", "rustls-tls", "stream"], default-features = false }`,
		"rustls":             `{ version = "%s", default-features = false, features = ["ring", "std"] }`,
		"tokio-rustls":       `{ version = "%s", features = ["ring"], default-features = false }`,
		"tokio-tungstenite":  `{ version = "%s", features = ["connect", "rustls-tls-webpki-roots"], default-features = false }`,
		"tokio-util":         `{ version = "%s", features = ["io"] }`,
	}

	for _, name := range depNames {
		version := workspaceDeps[name]
		if tmpl, ok := specDeps[name]; ok {
			fmt.Fprintf(&depsToml, "%s = %s\n", name, fmt.Sprintf(tmpl, version))
		} else {
			fmt.Fprintf(&depsToml, "%s = \"%s\"\n", name, version)
		}
	}

	// Rust crate names must use hyphens or underscores, not dots
	crateName := strings.ReplaceAll(config.CrateBasePath, ".", "-")

	// HasAutoTls/AutoTlsKeyType policy mirrors handler_gen.go: every service
	// with gRPC service wrappers gets the auto-TLS code path (and therefore
	// needs the aws-lc-rs/ed25519 crate in Cargo.toml). Default key type to ECDSA
	// for universal compatibility when no proto-level override is set.
	// A spec.smb service reuses the shared key-generation helper for its member
	// keypair, so the cert crates (aws-lc-rs/ed25519/rand) must be in Cargo.toml even
	// when the service has no TLS gRPC of its own (SPIFFE plan D7).
	autoTlsKeyType := config.AutoTlsKeyType()
	if autoTlsKeyType == "" && (hasServiceWrappers || hasSmb || len(config.States) > 0) {
		autoTlsKeyType = "ecdsa"
	}
	data := cargoWorkspaceData{
		Members:            members,
		DepsToml:           depsToml.String(),
		CrateName:          crateName,
		HasServiceWrappers: hasServiceWrappers,
		HasGrpcClients:     hasGrpcClients,
		HasW2SClients:      hasW2SClients,
		HasSubscriptions:   hasSubscriptions,
		HasAutoTls:         hasAutoTls,
		AutoTlsKeyType:     autoTlsKeyType,
		HasSmb:             hasSmb,
		SkipWorkspace:      skipWorkspace,
		HasMcpRpcs:         hasMcpRpcs,
		RmcpVersion:        rmcpCrateVersion,
		SchemarsVersion:    schemarsCrateVersion,
		LibCrates:          libCrates,
	}

	typeFS, err := fs.Sub(templates.FS, "rust/types")
	if err != nil {
		return fmt.Errorf("failed to get template FS: %w", err)
	}

	funcMap := tmplutil.BaseFuncMap()
	result, err := tmplutil.ExecuteTemplate(typeFS, "cargo.toml.tmpl", funcMap, data)
	if err != nil {
		return fmt.Errorf("failed to execute cargo template: %w", err)
	}

	if err := os.WriteFile(cargoPath, result, 0644); err != nil {
		return fmt.Errorf("failed to create workspace Cargo.toml: %w", err)
	}

	zap.L().Info("Created workspace Cargo.toml", zap.String("path", cargoPath), zap.Int("members", len(members)))

	// Generate types/Cargo.toml — the types-only crate that lib crates depend on.
	// This breaks the dependency cycle: main crate → lib crates → types crate.
	typesCargoPath := filepath.Join(rustRootDir, "types", "Cargo.toml")
	typesData := typesCargoData{
		CrateName:          crateName,
		HasServiceWrappers: hasServiceWrappers,
	}
	typesResult, err := tmplutil.ExecuteTemplate(typeFS, "types_cargo.toml.tmpl", funcMap, typesData)
	if err != nil {
		return fmt.Errorf("failed to execute types cargo template: %w", err)
	}
	if err := os.WriteFile(typesCargoPath, typesResult, 0644); err != nil {
		return fmt.Errorf("failed to create types Cargo.toml: %w", err)
	}
	zap.L().Info("Created types Cargo.toml", zap.String("path", typesCargoPath))

	return nil
}

func requiredWorkspaceDependencyRefs(rustRootDir string, hasServiceWrappers, hasGrpcClients, hasW2SClients, hasCertStreams bool) (map[string]struct{}, error) {
	required := map[string]struct{}{}
	add := func(names ...string) {
		for _, name := range names {
			required[name] = struct{}{}
		}
	}

	add(
		"base64",
		"bytes",
		"pbjson",
		"pbjson-types",
		"prost",
		"prost-types",
		"prost-wkt-types",
		"regex",
		"rustls",
		"serde",
		"serde_json",
		"serde_yaml",
		"toml",
		"tonic-health",
	)
	if hasServiceWrappers {
		add("tonic", "tonic-prost")
	}
	if hasServiceWrappers || hasGrpcClients || hasW2SClients {
		// tinyvec is pinned as a direct dep (not just transitively pulled in via
		// reqwest -> quinn) to force the resolver to 1.12.0: tinyvec 1.13.0 has a
		// still-open upstream bug ("cannot find macro `vec`") on newer rustc, and
		// no newer tinyvec has been published to fix it.
		// See decisions/2026-09-03-tinyvec-current-generation-root-cause.md.
		add("prost", "tonic", "futures", "tokio-stream", "tokio", "serde", "serde_json", "reqwest", "tinyvec", "tokio-util", "tonic-prost", "tracing")
	}
	if hasW2SClients {
		add("tokio-tungstenite")
	}
	if hasServiceWrappers || hasGrpcClients {
		add("async-trait", "axum", "base64", "regex")
	}
	if hasCertStreams {
		add("rustls-pemfile")
		if hasServiceWrappers {
			add("tokio-rustls")
		}
	}

	rootCargo := filepath.Join(rustRootDir, "Cargo.toml")
	if err := filepath.WalkDir(rustRootDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case "target", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "Cargo.toml" || path == rootCargo {
			return nil
		}
		refs, err := workspaceDependencyRefsInCargo(path)
		if err != nil {
			return err
		}
		for dep := range refs {
			required[dep] = struct{}{}
		}
		return nil
	}); err != nil {
		return nil, fmt.Errorf("failed to scan generated Rust Cargo.toml dependencies: %w", err)
	}

	return required, nil
}

func workspaceDependencyRefsInCargo(path string) (map[string]struct{}, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	refs := map[string]struct{}{}
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.Contains(trimmed, "workspace = true") {
			continue
		}
		eq := strings.Index(trimmed, "=")
		if eq <= 0 {
			continue
		}
		name := strings.TrimSpace(trimmed[:eq])
		if name != "" {
			refs[name] = struct{}{}
		}
	}
	return refs, nil
}

// parentHasWorkspace checks if a parent directory contains a Cargo.toml with [workspace].
// This detects when the generated code will be embedded in a service that owns the workspace.
func parentHasWorkspace(rustRootDir string) bool {
	parentDir := filepath.Dir(rustRootDir)
	parentCargo := filepath.Join(parentDir, "Cargo.toml")
	data, err := os.ReadFile(parentCargo)
	if err != nil {
		return false
	}
	return strings.Contains(string(data), "[workspace]")
}
