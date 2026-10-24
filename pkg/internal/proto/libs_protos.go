// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	pkgproto "github.com/tacplus-dev/tacplus/pkg/proto"
	"go.uber.org/zap"
)

// copyEmbeddedProtos copies an embedded FS directory to a target subdirectory.
// Files that already exist on disk are NOT overwritten.
// embedDir is the path within the embed.FS (e.g., "sys").
// targetSubdir is the path under targetDir to write to (e.g., "sys").
func copyEmbeddedProtos(fs embed.FS, embedDir, targetDir, targetSubdir string) error {
	outDir := filepath.Join(targetDir, targetSubdir)
	if err := os.MkdirAll(outDir, 0755); err != nil {
		return fmt.Errorf("failed to create %s directory: %w", targetSubdir, err)
	}

	entries, err := fs.ReadDir(embedDir)
	if err != nil {
		return fmt.Errorf("failed to read embedded %s protos: %w", embedDir, err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".proto") {
			continue
		}

		content, err := fs.ReadFile(filepath.Join(embedDir, entry.Name()))
		if err != nil {
			return fmt.Errorf("failed to read embedded proto %s/%s: %w", embedDir, entry.Name(), err)
		}

		targetPath := filepath.Join(outDir, entry.Name())

		// Skip if file already exists - DO NOT overwrite user's version
		if _, err := os.Stat(targetPath); err == nil {
			continue
		}

		if err := os.WriteFile(targetPath, content, 0644); err != nil {
			return fmt.Errorf("failed to write proto %s/%s: %w", targetSubdir, entry.Name(), err)
		}

		zap.L().Info("created embedded proto file", zap.String("file", targetSubdir+"/"+entry.Name()))
	}

	return nil
}

// CopySysProtos copies the embedded sys proto files to targetDir/sys/.
// This includes all infrastructure protos: errors.proto, spec.proto,
// and all library protos (alpn, httpclient, etc.).
func CopySysProtos(targetDir string) error {
	return copyEmbeddedProtos(pkgproto.SysProtos, "sys", targetDir, "sys")
}

// IsSysProto returns true if the relPath refers to a sys proto file.
func IsSysProto(relPath string) bool {
	normalized := filepath.ToSlash(relPath)
	return strings.HasPrefix(normalized, "sys/")
}

// IsGeneratedProto returns true if the relPath refers to a generated proto file.
func IsGeneratedProto(relPath string) bool {
	normalized := filepath.ToSlash(relPath)
	return strings.HasPrefix(normalized, "generated/")
}

// IsEventBusSupportProto returns true for the generated EventBus wire helper
// proto. It is synthesized into the reserved sys/ namespace (sys/eventbus.proto);
// because sys/ is reserved (validateReservedNamespaces rejects user protos there),
// this path unambiguously identifies tacplus's synthesized helper — a workspace
// can no longer author a colliding EventBus service at this path.
func IsEventBusSupportProto(relPath string) bool {
	return filepath.ToSlash(relPath) == EventBusSupportProtoPath
}

// IsVirtualServiceProto is no longer used — virtual service definitions (e.g.
// OpaEval) are now appended to their source vservices proto files rather than
// written as separate generated/ files.
func IsVirtualServiceProto(_ string) bool {
	return false
}

// IsGoogleProto returns true if the relPath refers to a google well-known proto.
func IsGoogleProto(relPath string) bool {
	normalized := filepath.ToSlash(relPath)
	return strings.HasPrefix(normalized, "google/")
}

// IsInfraProto returns true if the relPath refers to any infrastructure proto
// (sys/, generated/, or google/) that is not user-authored.
func IsInfraProto(relPath string) bool {
	if IsVirtualServiceProto(relPath) {
		return false
	}
	return IsSysProto(relPath) || IsGeneratedProto(relPath) || IsGoogleProto(relPath) || IsEventBusSupportProto(relPath)
}

// IsInfraProtoFile is the descriptor-aware form of IsInfraProto. Since the
// EventBus support helper moved to the reserved sys/eventbus.proto path (which
// users cannot author), infra classification is now fully path-owned; the
// descriptor-shape check below is redundant defense-in-depth (a sys/eventbus.proto
// can only be tacplus's synthesized, service-free helper).
func IsInfraProtoFile(relPath string, fileDesc *parser.FileDescriptor) bool {
	if !IsInfraProto(relPath) {
		return false
	}
	if !IsEventBusSupportProto(relPath) || fileDesc == nil {
		return true
	}
	if DeclaresServiceState(fileDesc) || len(fileDesc.Services) > 0 {
		return false
	}
	for i := range fileDesc.Messages {
		if _, ok := fileDesc.Messages[i].Annotations["grpc_config"]; ok {
			return false
		}
	}
	return true
}

// ServiceProtoParts extracts (serviceName, fileName) from a proto path
// under any top-level directory (services, clients, common, etc.).
// For flat {dir}/{name}.proto, returns (name, name).
// For subdir {dir}/{name}/{file}.proto, returns (name, file).
// Returns ("", "") for sys/ and google/ (infrastructure protos), root-level
// protos, or paths deeper than one subdirectory.
func ServiceProtoParts(relPath string) (string, string) {
	normalized := filepath.ToSlash(relPath)
	parts := strings.Split(normalized, "/")
	if len(parts) < 2 || parts[0] == "sys" || parts[0] == "google" || parts[0] == "generated" {
		return "", ""
	}
	if len(parts) == 2 && strings.HasSuffix(parts[1], ".proto") {
		name := strings.TrimSuffix(parts[1], ".proto")
		return name, name
	}
	if len(parts) == 3 && strings.HasSuffix(parts[2], ".proto") {
		return parts[1], strings.TrimSuffix(parts[2], ".proto")
	}
	return "", ""
}

// IsServicesFileName returns true if the proto filename (without extension) is "services".
func IsServicesFileName(fileName string) bool {
	return fileName == "services"
}

// ValidateProtoLayout checks that proto files follow the required layout:
//   - No proto files at the root level (must be under a directory)
//   - Max one subdirectory deep (dir/file.proto or dir/sub/file.proto)
//   - A proto file's package declaration must not match its top-level directory name
func ValidateProtoLayout(protoDir string) error {
	return filepath.Walk(protoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Skip hidden directories (.claude, .git, etc.)
		if info.IsDir() && strings.HasPrefix(info.Name(), ".") && path != protoDir {
			return filepath.SkipDir
		}
		if info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}

		relPath, err := filepath.Rel(protoDir, path)
		if err != nil {
			return err
		}
		normalized := filepath.ToSlash(relPath)
		parts := strings.Split(normalized, "/")

		// Skip infrastructure protos
		if parts[0] == "sys" || parts[0] == "google" || parts[0] == "generated" {
			return nil
		}

		// No root-level protos
		if len(parts) == 1 {
			return fmt.Errorf("proto file %q is at the root level; all protos must be under a directory", relPath)
		}

		// Max one subdirectory deep
		if len(parts) > 3 {
			return fmt.Errorf("proto file %q is nested too deep; max depth is {dir}/{sub}/{file}.proto", relPath)
		}

		// Package must not match the top-level directory name
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", relPath, err)
		}
		pkg := extractPackageDecl(string(content))
		if pkg == parts[0] {
			return fmt.Errorf("proto file %q declares package %q which matches its top-level directory; use a unique package name", relPath, pkg)
		}

		return nil
	})
}

// extractPackageDecl extracts the package name from proto file content.
func extractPackageDecl(content string) string {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "package ") {
			line = strings.TrimPrefix(line, "package ")
			line = strings.TrimSuffix(line, ";")
			return strings.TrimSpace(line)
		}
	}
	return ""
}

// copyUserProtos copies user proto files from inputDir to protoDir,
// skipping embedded protos (sys/) which are overlaid separately.
func copyUserProtos(inputDir, protoDir string) error {
	return filepath.Walk(inputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip hidden directories (.claude, .git, etc.)
		if info.IsDir() && strings.HasPrefix(info.Name(), ".") && path != inputDir {
			return filepath.SkipDir
		}

		relPath, err := filepath.Rel(inputDir, path)
		if err != nil {
			return err
		}

		// Skip infrastructure protos (sys/, generated/) — sys are overlaid
		// from embedded FS, generated/ is created dynamically by tacplus.
		if IsSysProto(relPath) || IsGeneratedProto(relPath) {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		dst := filepath.Join(protoDir, relPath)
		if info.IsDir() {
			return os.MkdirAll(dst, 0755)
		}

		if !strings.HasSuffix(info.Name(), ".proto") {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, content, 0644)
	})
}

// CopyAndPrepareProtoDir copies proto files from inputDir into targetDir/proto/,
// overlays embedded sys, and prepares service proto structure. Returns the proto directory path.
// Does NOT strip spec annotations — call StripSpecOptionsFromDir separately
// after any operations that need annotations (e.g., projection generation).
func CopyAndPrepareProtoDir(inputDir, targetDir string) (string, error) {
	protoDir := filepath.Join(targetDir, "proto")
	if err := os.MkdirAll(protoDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create proto directory: %w", err)
	}

	// Validate proto layout before copying
	if err := ValidateProtoLayout(inputDir); err != nil {
		return "", err
	}

	// Copy user proto files (skip infrastructure protos — they're overlaid from embedded FS below)
	if err := copyUserProtos(inputDir, protoDir); err != nil {
		return "", fmt.Errorf("failed to copy proto files: %w", err)
	}

	// Overlay embedded protos (won't overwrite existing user files)
	if err := CopySysProtos(protoDir); err != nil {
		return "", fmt.Errorf("failed to copy sys protos: %w", err)
	}

	// Generate generated/*.proto from grpc_config annotations.
	// Must happen before any descriptor parser runs (service protos may import it).
	if _, _, _, err := ScanAndGenerateConfigHolder(inputDir, protoDir); err != nil {
		return "", fmt.Errorf("failed to generate generated/*.proto: %w", err)
	}

	// Write a stub generated/tenant_config.proto so protoc can resolve the import
	// from any service proto (e.g. config.proto) before the real file is generated.
	// GenerateTenantConfig below will overwrite this stub with the real content.
	tenantConfigStub := "// CODE GENERATED by tacplus — DO NOT EDIT\n\n" +
		"syntax = \"proto3\";\n\npackage tenant_config;\n\n" +
		"// Stub — will be replaced by GenerateTenantConfig.\n" +
		"message TenantConfig {}\n"
	genDir := filepath.Join(protoDir, "generated")
	if err := os.MkdirAll(genDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create generated dir: %w", err)
	}
	stubPath := filepath.Join(genDir, "tenant_config.proto")
	if _, err := os.Stat(stubPath); os.IsNotExist(err) {
		if err := os.WriteFile(stubPath, []byte(tenantConfigStub), 0644); err != nil {
			return "", fmt.Errorf("failed to write tenant_config.proto stub: %w", err)
		}
	}

	// Write a stub generated/project_config.proto so protoc can resolve the import
	// before the real file is generated. GenerateProjectConfig overwrites this stub.
	projectConfigStub := "// CODE GENERATED by tacplus — DO NOT EDIT\n\n" +
		"syntax = \"proto3\";\n\npackage project_config;\n\n" +
		"// Stub — will be replaced by GenerateProjectConfig.\n" +
		"message ProjectConfig {}\n"
	projectStubPath := filepath.Join(genDir, "project_config.proto")
	if _, err := os.Stat(projectStubPath); os.IsNotExist(err) {
		if err := os.WriteFile(projectStubPath, []byte(projectConfigStub), 0644); err != nil {
			return "", fmt.Errorf("failed to write project_config.proto stub: %w", err)
		}
	}

	// Replace the stubs with the real aggregates. They are GENERATED messages
	// carrying (spec.per_service_config), and this tree is the flattened view
	// every later step parses: ResolveServiceStates subscribes a service to the
	// grpc_config owner of each per_service_config message naming it, so a
	// TenantConfig left a stub here meant no service was ever subscribed to its
	// slice of the generated one, in any language. No-op (the stub stays) when
	// no service declares a slice.
	if err := GenerateTenantConfig(inputDir, protoDir, protoDir); err != nil {
		return "", fmt.Errorf("failed to generate tenant_config.proto: %w", err)
	}
	if err := GenerateProjectConfig(inputDir, protoDir, protoDir); err != nil {
		return "", fmt.Errorf("failed to generate project_config.proto: %w", err)
	}

	// Inject the MCP session carrier into every service that declares an
	// RPC_PRINCIPAL_MCP tool. Here, not in each language generator, because the
	// carrier has to be a real rpc in the tree protoc compiles for EVERY
	// language, and because this is the last point at which every import a
	// service proto can name (generated/, sys/) already resolves. A tree with no
	// MCP rpc is untouched.
	injected, err := AppendMcpCarrierRpcsToProtos(protoDir, inputDir)
	if err != nil {
		return "", fmt.Errorf("failed to inject MCP session carrier rpcs: %w", err)
	}
	if len(injected) > 0 {
		// The scan above ran over the un-injected tree, so the carrier is missing
		// from its service's <Svc>RPCs enum and TokenEntitlements mapping. Re-run
		// it over the injected tree; a c2s carrier needs an enum value exactly
		// like the hand-declared rpcs beside it.
		if _, _, _, err := ScanAndGenerateConfigHolder(inputDir, protoDir); err != nil {
			return "", fmt.Errorf("failed to regenerate generated/*.proto after MCP carrier injection: %w", err)
		}
	}

	return protoDir, nil
}

// WriteAnnotatedProtos copies the annotated proto files (with spec.* annotations
// intact) from protoDir to targetDir/proto-annotated/. This preserves the original
// annotations (private, service_state, etc.) and includes sys/spec.proto
// so that downstream tools (e.g., a Maven codegen plugin) can parse annotations
// without needing the tacplus compiler.
//
// Call this BEFORE StripSpecOptionsFromDir to capture the annotated state.
func WriteAnnotatedProtos(protoDir, targetDir string) error {
	annotatedDir := filepath.Join(targetDir, "proto-annotated")
	if err := os.RemoveAll(annotatedDir); err != nil {
		return fmt.Errorf("failed to clean annotated proto directory: %w", err)
	}

	return filepath.Walk(protoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(protoDir, path)
		if err != nil {
			return err
		}

		dst := filepath.Join(annotatedDir, relPath)
		if info.IsDir() {
			return os.MkdirAll(dst, 0755)
		}

		if !strings.HasSuffix(info.Name(), ".proto") {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return err
		}
		return os.WriteFile(dst, content, 0644)
	})
}

// SecurityPolicy describes the computed access policy for a single state root.
// Derived from proto annotations at build time. Consumed by the Java codegen plugin.
type SecurityPolicy struct {
	Name           string          `json:"name"`
	Package        string          `json:"package"`
	RW             []string        `json:"rw"`
	RO             []string        `json:"ro"`
	DisableAuth    bool            `json:"disableAuth,omitempty"`
	Internal       bool            `json:"internal,omitempty"`
	BoundaryGrants []BoundaryGrant `json:"boundaryGrants,omitempty"`
	// TenantPinnedReads are the field-scoped, tenant-pinned read grants (#130):
	// see ResolveTenantPinnedBoundaries for the rule the serving side enforces.
	// Deliberately NOT a BoundaryGrant: a consumer that reads boundaryGrants
	// grants the callers the whole boundary (read and write) whatever the type,
	// so a pinned read carried there would fail OPEN on a consumer that predates
	// it. In a field of its own, a consumer that does not know it grants nothing.
	TenantPinnedReads []TenantPinnedRead `json:"tenantPinnedReads,omitempty"`
}

// TenantPinnedRead is one tenant-pinned collection of a state root and the
// callers pinned on it. Callers are bare, location-agnostic service paths.
type TenantPinnedRead struct {
	Boundary    string                  `json:"boundary"`    // collection field, e.g. "tenant_configs"
	KeyField    string                  `json:"keyField"`    // item's (spec.indexed) string key, e.g. "tenant_name"
	ConfigField string                  `json:"configField"` // item's per_service_config field, e.g. "config"
	Slices      []TenantPinnedReadSlice `json:"slices"`
}

// TenantPinnedReadSlice is one pinned caller and the config field it may see.
type TenantPinnedReadSlice struct {
	Caller     string `json:"caller"`
	SliceField string `json:"sliceField"`
}

// BoundaryGrant records a cross-namespace call grant on a specific boundary field.
type BoundaryGrant struct {
	Boundary string   `json:"boundary"`
	Callers  []string `json:"callers"`
	Type     string   `json:"type"` // "queue", "topic", "command"
}

// ServiceRPCMapping describes a service and its RPCs for entitlements enforcement.
// Written to codegen-config.json so downstream generators can
// build a static service→RPCs map for token entitlements.
type ServiceRPCMapping struct {
	ServiceName string   `json:"serviceName"`
	RPCs        []string `json:"rpcs"`
}

// codegenConfig is the JSON structure written to codegen-config.json.
type codegenConfig struct {
	JavaPackageBase    string              `json:"javaPackageBase"`
	InterfacesPackage  string              `json:"interfacesPackage"`
	AcceptedAudiences  []string            `json:"acceptedAudiences,omitempty"`
	SecurityPolicies   []SecurityPolicy    `json:"securityPolicies,omitempty"`
	ServiceRPCMappings []ServiceRPCMapping `json:"serviceRPCMappings,omitempty"`
}

// WriteCodegenConfig writes a codegen-config.json file into targetDir/proto-annotated/
// containing package configuration that downstream codegen plugins (e.g., the Maven
// StateServerMojo) can read instead of requiring hardcoded Maven parameters.
func WriteCodegenConfig(targetDir, javaPackageBase, interfacesPackage string, acceptedAudiences []string, policies []SecurityPolicy, svcRPCs []ServiceRPCEntry) error {
	annotatedDir := filepath.Join(targetDir, "proto-annotated")
	if err := os.MkdirAll(annotatedDir, 0755); err != nil {
		return fmt.Errorf("failed to create annotated proto directory: %w", err)
	}

	// Convert ServiceRPCEntry to ServiceRPCMapping for JSON output.
	var mappings []ServiceRPCMapping
	for _, e := range svcRPCs {
		mappings = append(mappings, ServiceRPCMapping{
			ServiceName: e.GrpcName,
			RPCs:        e.RPCs,
		})
	}

	cfg := codegenConfig{
		JavaPackageBase:    javaPackageBase,
		InterfacesPackage:  interfacesPackage,
		AcceptedAudiences:  acceptedAudiences,
		SecurityPolicies:   policies,
		ServiceRPCMappings: mappings,
	}

	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal codegen config: %w", err)
	}

	configPath := filepath.Join(annotatedDir, "codegen-config.json")
	if err := os.WriteFile(configPath, append(data, '\n'), 0644); err != nil {
		return fmt.Errorf("failed to write codegen config: %w", err)
	}

	zap.L().Info("wrote codegen-config.json", zap.String("path", configPath),
		zap.Strings("acceptedAudiences", acceptedAudiences),
		zap.Int("policies", len(policies)),
		zap.Int("serviceRPCMappings", len(mappings)))
	return nil
}

// ComputeSecurityPolicies derives access policies from proto annotations.
// For each message with service_state, it computes:
//   - rw: ["service:<package>"] (owning service, implicit)
//   - ro: ["service:<owner>", "service:<importer>", ...] — the owning service
//     plus every service that declares a (spec.state_import) of the root. A
//     service may read another service's state only if it has declared a
//     dependency on it, so there is no blanket "*" read grant.
//   - disableAuth: from disable_auth_message annotation
//   - boundaryGrants: from boundary_callers on queue/topic/command fields
//
// The RO set is resolved by the same stateImportersByRoot/stateServiceAuthServices
// pair that AppendStateServiceToProtos uses for the generated state service's
// auth_services, differing only in the "service:" prefix the kernel-side
// SecurityPolicyParser expects; ValidateStatePolicyAuthParity is the
// post-condition that keeps the two from drifting apart.
//
// inputDir is the flat proto directory for descriptor parsing.
func ComputeSecurityPolicies(inputDir string) ([]SecurityPolicy, error) {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create descriptor parser for security policies: %w", err)
	}

	importersByRoot := stateImportersByRoot(dp.GetAllFiles())

	pinned, err := ResolveTenantPinnedBoundaries(dp.GetAllFiles())
	if err != nil {
		return nil, err
	}
	pinnedByRoot := make(map[string][]TenantPinnedRead)
	for _, b := range pinned {
		read := TenantPinnedRead{Boundary: b.Field, KeyField: b.KeyField, ConfigField: b.ConfigField}
		for _, sl := range b.Slices {
			read.Slices = append(read.Slices, TenantPinnedReadSlice{Caller: sl.Caller, SliceField: sl.SliceField})
		}
		key := b.Package + "." + b.StateRoot
		pinnedByRoot[key] = append(pinnedByRoot[key], read)
	}

	var policies []SecurityPolicy

	for relPath, fileDesc := range dp.GetAllFiles() {
		if IsInfraProto(relPath) {
			continue
		}

		for _, msg := range fileDesc.Messages {
			ann, hasState := msg.Annotations["service_state"]
			if !hasState || ann.StringValue == "" {
				continue
			}

			stateName := ann.StringValue
			pkg := fileDesc.Package

			_, disableAuth := msg.Annotations["disable_auth_message"]

			// Owning service always has RW
			rw := []string{"service:" + pkg}
			// Cross-namespace read: owner plus declared state_import importers.
			ro := prefixServicePackages(stateServiceAuthServices(pkg, importersByRoot[pkg+"."+stateName]))

			// Check for instance_keyed fields → any authenticated service can write
			for _, field := range msg.Fields {
				ikAnn, hasIK := field.Annotations["instance_keyed"]
				if hasIK && ikAnn.BoolValue {
					rw = []string{"*"}
					break
				}
			}

			// Collect boundary grants from boundary_callers on queue/topic/command fields
			var grants []BoundaryGrant
			for _, field := range msg.Fields {
				callersAnn, hasCallers := field.Annotations["boundary_callers"]
				if !hasCallers || len(callersAnn.StringSliceValue) == 0 {
					continue
				}

				var boundaryType string
				if _, isQ := field.Annotations["queue"]; isQ {
					boundaryType = "queue"
				} else if _, isPQ := field.Annotations["priority_queue"]; isPQ {
					boundaryType = "queue"
				} else if _, isT := field.Annotations["topic"]; isT {
					boundaryType = "topic"
				} else if cmdAnn, isCmd := field.Annotations["command"]; isCmd && cmdAnn.BoolValue {
					boundaryType = "command"
				}

				if boundaryType != "" {
					grants = append(grants, BoundaryGrant{
						Boundary: field.Name,
						Callers:  callersAnn.StringSliceValue,
						Type:     boundaryType,
					})
				}
			}

			policy := SecurityPolicy{
				Name:              stateName,
				Package:           pkg,
				RW:                rw,
				RO:                ro,
				DisableAuth:       disableAuth,
				BoundaryGrants:    grants,
				TenantPinnedReads: pinnedByRoot[pkg+"."+stateName],
			}
			policies = append(policies, policy)

			zap.L().Debug("computed security policy",
				zap.String("name", stateName),
				zap.String("package", pkg),
				zap.Bool("disableAuth", disableAuth),
				zap.Int("boundaryGrants", len(grants)))
		}
	}

	// Also compute policies for service_store messages.
	for relPath, fileDesc := range dp.GetAllFiles() {
		if IsInfraProto(relPath) {
			continue
		}

		for _, msg := range fileDesc.Messages {
			ann, hasStore := msg.Annotations["service_store"]
			if !hasStore || ann.StringValue == "" {
				continue
			}

			storeName := ann.StringValue
			pkg := fileDesc.Package

			policy := SecurityPolicy{
				Name:    storeName,
				Package: pkg,
				RW:      []string{"service:" + pkg},
				RO:      []string{"*"},
			}
			policies = append(policies, policy)

			zap.L().Debug("computed store security policy",
				zap.String("name", storeName),
				zap.String("package", pkg))
		}
	}

	return policies, nil
}

// prefixServicePackages renders resolved package names in the "service:<pkg>"
// form the kernel's SecurityPolicyParser matches subjects against. The
// resolution itself lives in stateServiceAuthServices, which yields plain
// package names for the generated state service's auth_services option; this
// is the only difference between the two formats.
func prefixServicePackages(pkgs []string) []string {
	out := make([]string, 0, len(pkgs))
	for _, pkg := range pkgs {
		out = append(out, "service:"+pkg)
	}
	return out
}

// ValidateStatePolicyAuthParity is the cross-plane post-condition on state
// access control: for every service_state root, the caller set gating the
// generated gRPC state service (auth_services, enforced by the chassis
// interceptor) must equal the caller set gating boundary reads
// (SecurityPolicy.RO, enforced by the kernel's SecurityInterceptor via
// codegen-config.json), modulo the "service:" prefix.
//
// The two planes are layered — a caller must pass both — so a root whose RO is
// wider than its auth_services is not immediately exploitable. It is still a
// hard error: the RO set is what codegen-config.json publishes as the read
// policy for that graph, and a silent divergence between the two derivations is
// exactly how ro: ["*"] outlived the rule that a service may read another
// service's state only if it declares a dependency on it. Both sets now come
// from the same stateImportersByRoot/stateServiceAuthServices pair, so this
// check should be structurally unreachable; it exists so that editing one
// derivation without the other fails generation instead of drifting.
//
// Roots declaring (spec.disable_auth_message) are skipped: they carry no
// auth_services by design (see ValidateGeneratedStateServiceAuth) and their
// graph is registered as no-auth on the kernel side, so there is nothing to
// compare.
func ValidateStatePolicyAuthParity(inputDir string, policies []SecurityPolicy) error {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser for state policy parity: %w", err)
	}

	byRoot := make(map[string]SecurityPolicy, len(policies))
	for _, p := range policies {
		byRoot[p.Package+"."+p.Name] = p
	}
	importersByRoot := stateImportersByRoot(dp.GetAllFiles())

	var violations []string
	for relPath, fileDesc := range dp.GetAllFiles() {
		if IsInfraProto(relPath) {
			continue
		}
		for i := range fileDesc.Messages {
			msg := &fileDesc.Messages[i]
			ann, hasState := msg.Annotations["service_state"]
			if !hasState || ann.StringValue == "" {
				continue
			}
			if _, authDisabled := msg.Annotations["disable_auth_message"]; authDisabled {
				continue
			}

			key := fileDesc.Package + "." + ann.StringValue
			policy, ok := byRoot[key]
			if !ok {
				violations = append(violations, fmt.Sprintf(
					"%s: state root %s has no computed SecurityPolicy", relPath, key))
				continue
			}

			want := prefixServicePackages(stateServiceAuthServices(fileDesc.Package, importersByRoot[key]))
			if !sameStringSet(want, policy.RO) {
				violations = append(violations, fmt.Sprintf(
					"%s: state root %s auth_services %v disagree with SecurityPolicy.RO %v",
					relPath, key, want, policy.RO))
			}
		}
	}

	if len(violations) > 0 {
		sort.Strings(violations)
		return fmt.Errorf("state service auth_services and SecurityPolicy.RO disagree:\n  %s",
			strings.Join(violations, "\n  "))
	}
	return nil
}

// sameStringSet reports whether two lists hold the same set of values,
// ignoring order and duplicates.
func sameStringSet(a, b []string) bool {
	seen := make(map[string]bool, len(a))
	for _, v := range a {
		seen[v] = true
	}
	other := make(map[string]bool, len(b))
	for _, v := range b {
		other[v] = true
	}
	if len(seen) != len(other) {
		return false
	}
	for v := range seen {
		if !other[v] {
			return false
		}
	}
	return true
}
