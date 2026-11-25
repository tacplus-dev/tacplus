// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/fsroot"
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
	// Workspace dependency versions come from the package registry
	allDeps := registry.GetAllRustDependencies()

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

	hasW2SClients := len(config.GrpcW2SClients) > 0
	if hasW2SClients {
		for _, dep := range []string{"tokio-tungstenite"} {
			if _, ok := allDeps[dep]; !ok {
				return fmt.Errorf("rust W2S clients require %s in dependencies.yaml", dep)
			}
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
	typesData := typesCargoData{
		CrateName:          crateName,
		HasServiceWrappers: hasServiceWrappers,
	}
	typesResult, err := tmplutil.ExecuteTemplate(typeFS, "types_cargo.toml.tmpl", funcMap, typesData)
	if err != nil {
		return fmt.Errorf("failed to execute types cargo template: %w", err)
	}
	// Rendered without [workspace.dependencies] first: the root crate's own
	// { workspace = true } references are among those the workspace declares.
	rootDeps, err := tmplutil.ExecuteTemplate(typeFS, "cargo.toml.tmpl", funcMap, data)
	if err != nil {
		return fmt.Errorf("failed to execute cargo template: %w", err)
	}

	// The workspace declares exactly the dependencies its crates reference:
	// cargo 1.100 warns on one no member uses (cargo::unused_workspace_dependencies,
	// tacplus#829), so the Rust pins in dependencies.yaml nothing generated
	// references stay out.
	requiredDeps, err := requiredWorkspaceDependencyRefs(rustRootDir, rootDeps, typesResult)
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

	// Sort dependency names for deterministic output
	var depNames []string
	for name := range requiredDeps {
		depNames = append(depNames, name)
	}
	sort.Strings(depNames)

	// Build workspace dependencies TOML
	var depsToml strings.Builder

	// Special handling for crates that need features
	specDeps := map[string]string{
		"serde":              `{ version = "%s", features = ["derive"] }`,
		"serde-saphyr":       `{ version = "%s", default-features = false, features = ["serialize", "deserialize"] }`,
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
		version := allDeps[name]
		if tmpl, ok := specDeps[name]; ok {
			fmt.Fprintf(&depsToml, "%s = %s\n", name, fmt.Sprintf(tmpl, version))
		} else {
			fmt.Fprintf(&depsToml, "%s = \"%s\"\n", name, version)
		}
	}
	data.DepsToml = depsToml.String()

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
	if err := os.WriteFile(typesCargoPath, typesResult, 0644); err != nil {
		return fmt.Errorf("failed to create types Cargo.toml: %w", err)
	}
	zap.L().Info("Created types Cargo.toml", zap.String("path", typesCargoPath))

	return nil
}

// requiredWorkspaceDependencyRefs returns the dependencies the workspace's
// crates take with { workspace = true }: those in the manifests about to be
// written (the root and types crates) and in every other Cargo.toml under
// rustRootDir (the chassis crates).
func requiredWorkspaceDependencyRefs(rustRootDir string, manifests ...[]byte) (map[string]struct{}, error) {
	required := map[string]struct{}{}
	for _, manifest := range manifests {
		for dep := range workspaceDependencyRefs(manifest) {
			required[dep] = struct{}{}
		}
	}

	if err := fsroot.Walk(rustRootDir, func(r *os.Root, rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if rel == "." {
				name = filepath.Base(rustRootDir)
			}
			switch name {
			case "target", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "Cargo.toml" || rel == "Cargo.toml" || rel == filepath.Join("types", "Cargo.toml") {
			return nil
		}
		refs, err := workspaceDependencyRefsInCargo(r, rel)
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

func workspaceDependencyRefsInCargo(r *os.Root, rel string) (map[string]struct{}, error) {
	data, err := r.ReadFile(rel)
	if err != nil {
		return nil, err
	}
	return workspaceDependencyRefs(data), nil
}

func workspaceDependencyRefs(manifest []byte) map[string]struct{} {
	refs := map[string]struct{}{}
	for _, line := range strings.Split(string(manifest), "\n") {
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
	return refs
}

// moveUnusedCrateDependencies runs once the generated Rust source is complete:
// in each crate's Cargo.toml (the root crate, types and every chassis crate) it
// moves a dependency the crate's source does not reference to
// [dev-dependencies] (moveUnusedDependencies). What a crate uses depends on the
// protos (the types crate's hmac only with redact fields, the root crate's
// chassis crates only with a service manager), and cargo 1.100 warns on a
// dependency nothing in the crate uses (cargo::unused_dependencies,
// tacplus#829). The root crate always keeps typesCrate, which lib.rs re-exports.
func moveUnusedCrateDependencies(rustRootDir, typesCrate string) error {
	crateDirs := []string{rustRootDir, filepath.Join(rustRootDir, "types")}
	chassis, err := filepath.Glob(filepath.Join(rustRootDir, "chassis", "*", "Cargo.toml"))
	if err != nil {
		return err
	}
	sort.Strings(chassis)
	for _, manifest := range chassis {
		crateDirs = append(crateDirs, filepath.Dir(manifest))
	}
	for _, crateDir := range crateDirs {
		manifestPath := filepath.Join(crateDir, "Cargo.toml")
		manifest, err := os.ReadFile(manifestPath)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		keep := ""
		if crateDir == rustRootDir {
			keep = typesCrate
		}
		moved, err := moveUnusedDependencies(crateDir, manifest, keep)
		if err != nil {
			return err
		}
		if string(moved) == string(manifest) {
			continue
		}
		if err := os.WriteFile(manifestPath, moved, 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", manifestPath, err)
		}
	}
	return nil
}

// moveUnusedDependencies moves each dependency of a crate's [dependencies]
// that the crate's source does not reference, with the comment lines above
// it, to its [dev-dependencies] (a new table after [dependencies] if it has
// none; one already there is not repeated). A dev-dependency still takes part
// in the workspace's lockfile, so a pin (tinyvec, x509-cert) still holds, and
// tests placed in the crate's tests/ still reach it. keep, if set, is never
// moved.
func moveUnusedDependencies(crateDir string, manifest []byte, keep string) ([]byte, error) {
	lines := strings.Split(string(manifest), "\n")
	deps, ok := findCargoTable(lines, "[dependencies]")
	if !ok {
		return manifest, nil
	}
	devDeps, hasDevDeps := findCargoTable(lines, "[dev-dependencies]")

	rustName := func(name string) string { return strings.ReplaceAll(name, "-", "_") }
	var names []string
	for _, dep := range deps.entries {
		names = append(names, rustName(dep.name))
	}
	refs, err := crateReferences(crateDir, names)
	if err != nil {
		return nil, err
	}
	inDevDeps := map[string]bool{}
	for _, dep := range devDeps.entries {
		inDevDeps[dep.name] = true
	}
	var kept, moved []string
	anyMoved := false
	for _, dep := range deps.entries {
		switch {
		case dep.name == keep || refs[rustName(dep.name)]:
			kept = append(kept, dep.lines...)
		case inDevDeps[dep.name]:
			anyMoved = true
		default:
			anyMoved = true
			moved = append(moved, dep.lines...)
		}
	}
	if !anyMoved {
		return manifest, nil
	}

	newDeps := append(append([]string{lines[deps.start]}, kept...), deps.trailing...)
	if !hasDevDeps {
		newDeps = append(append([]string{lines[deps.start]}, kept...),
			"",
			"# What this crate's generated code does not use: cargo warns on a dependency",
			"# nothing in the crate uses (cargo::unused_dependencies). As dev-dependencies",
			"# they still pin the workspace's lockfile and are in reach of tests in tests/.",
			"[dev-dependencies]",
		)
		newDeps = append(append(newDeps, moved...), deps.trailing...)
		return []byte(strings.Join(spliceLines(lines, deps.start, deps.end, newDeps), "\n")), nil
	}
	var newDevDeps []string
	newDevDeps = append(newDevDeps, lines[devDeps.start])
	for _, dep := range devDeps.entries {
		newDevDeps = append(newDevDeps, dep.lines...)
	}
	newDevDeps = append(append(newDevDeps, moved...), devDeps.trailing...)
	// Splice the later table first, so the earlier one's indexes still hold.
	if devDeps.start > deps.start {
		lines = spliceLines(lines, devDeps.start, devDeps.end, newDevDeps)
		lines = spliceLines(lines, deps.start, deps.end, newDeps)
	} else {
		lines = spliceLines(lines, deps.start, deps.end, newDeps)
		lines = spliceLines(lines, devDeps.start, devDeps.end, newDevDeps)
	}
	return []byte(strings.Join(lines, "\n")), nil
}

// cargoTable is a dependency table of a Cargo.toml: its header's line index,
// the index of the line ending it (the next header, or the end), its entries,
// each with the comment and blank lines above it, and the lines after the last.
type cargoTable struct {
	start, end int
	entries    []cargoTableEntry
	trailing   []string
}

type cargoTableEntry struct {
	name  string
	lines []string
}

func findCargoTable(lines []string, header string) (cargoTable, bool) {
	table := cargoTable{start: -1, end: len(lines)}
	for i, line := range lines {
		if strings.TrimSpace(line) == header {
			table.start = i
			break
		}
	}
	if table.start < 0 {
		return table, false
	}
	for i := table.start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			table.end = i
			break
		}
	}
	var pending []string
	for _, line := range lines[table.start+1 : table.end] {
		trimmed := strings.TrimSpace(line)
		name, _, ok := strings.Cut(trimmed, "=")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || !ok {
			pending = append(pending, line)
			continue
		}
		table.entries = append(table.entries, cargoTableEntry{name: strings.TrimSpace(name), lines: append(pending, line)})
		pending = nil
	}
	table.trailing = pending
	return table, true
}

func spliceLines(lines []string, start, end int, replacement []string) []string {
	out := append([]string{}, lines[:start]...)
	out = append(out, replacement...)
	return append(out, lines[end:]...)
}

// crateReferences reports which of the crates named (as Rust paths name them:
// underscores) a crate's Rust source references, by a path through it
// (`name::`), a `use name` or an `extern crate name`. It reads every .rs file
// under crateDir but those of the crates nested in it (a directory with its own
// Cargo.toml) and of the crate's tests/, which reach dev-dependencies too. A
// mention in a comment or a string counts: a dependency kept that the code does
// not use is a warning a gate shows, one moved that it does use a build that
// fails.
func crateReferences(crateDir string, names []string) (map[string]bool, error) {
	patterns := make(map[string]*regexp.Regexp, len(names))
	for _, name := range names {
		quoted := regexp.QuoteMeta(name)
		patterns[name] = regexp.MustCompile(`(?:^|[^\w:]|(?:^|[^\w:])::)` + quoted + `::|\b(?:use|extern\s+crate)\s+` + quoted + `\b`)
	}
	refs := map[string]bool{}
	err := fsroot.Walk(crateDir, func(r *os.Root, rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if rel == "." {
				return nil
			}
			switch d.Name() {
			case "target", ".git":
				return filepath.SkipDir
			}
			if rel == "tests" {
				return filepath.SkipDir
			}
			if _, err := r.Stat(filepath.Join(rel, "Cargo.toml")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".rs") {
			return nil
		}
		data, err := r.ReadFile(rel)
		if err != nil {
			return err
		}
		for name, pattern := range patterns {
			if !refs[name] && pattern.Match(data) {
				refs[name] = true
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to scan generated Rust crate %s for crate references: %w", crateDir, err)
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
