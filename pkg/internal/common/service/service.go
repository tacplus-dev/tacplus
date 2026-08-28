// Copyright (c) 2026 Second Star Cavalier LLC

package service

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	protogen "gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
	spec "gitlab.com/secondstarcavalier/schematic/tacplus/target/proto/spec/sys"
)

// ServiceInfo holds information about a detected service for wrapper generation.
// This is the common struct shared by Python, TypeScript, and Rust generators.
// Go and Java have extended versions with additional fields (ReadOnly, TypesOutputDir).
type ServiceInfo struct {
	ProtoFile     string
	PackageName   string
	ServiceName   string
	GrpcName      string // service identity used for routing (defaults to package name)
	IsGrpc        bool   // true if service has a gRPC annotation (service_s2s, service_c2s, or service_c2d)
	IsS2S         bool   // true if service has service_s2s annotation
	IsC2S         bool   // true if service has service_c2s annotation
	IsC2D         bool   // true if service has service_c2d annotation
	C2DSocketPath string // service_c2d socket_path: unix socket the daemon binds (required, no default)
	IsSMB         bool   // true if service has smb annotation
	SmbRoot       spec.SmbRoot
	OutputDir     string
	ParentDir     string
	Config        *tactype.Config
	AuthDisabled  bool     // C2S: no token required
	AuthAnyUser   bool     // C2S: any authenticated user can call
	AuthServices  []string // S2S: allowed caller identities (service-level default)
	HasMethodAuth bool     // true if any per-method auth metadata must be registered
	// AuthExemptPaths are full gRPC method paths ("/pkg.Service/Method") that
	// carry auth_disabled and must therefore be reachable WITHOUT a token.
	//
	// The server-side AuthEnforcingLayer rejects every request lacking an
	// authorization header before it can reach the per-method wrapper that is
	// supposed to be the authoritative gate. That made auth_disabled a lie on
	// the server: the gateway honoured it, the service did not. Login was
	// structurally impossible as a result -- ListProviders, Authorize and
	// MintCallbackTokens must all be callable before a token exists.
	AuthExemptPaths []string
	EventLabel      string // physical service routing label from spec.event_label
	HostLabel       string // non-empty for vservices: the SelfPackage of the service that hosts this RPC surface
	Synthesized     bool   // tacplus-generated state/store/mutation/opa service (not user-defined); excluded from own-package server generation
	// SynthesizedKind is WHICH generated surface (parser.SynthesizedKind*).
	// Synthesized alone cannot answer "does this build need an S2S identity":
	// a generated STATE service says yes, a generated STORE service does not.
	SynthesizedKind string
}

// AccountingInfo holds accounting metadata for a service, derived from scanServiceRPCs.
// EnumName is the PascalCase name for the accounting enum (e.g., "Sentinel").
// RPCs is the set of valid RPC method names that have entries in the enum.
// Wrapper generators check RPCs before emitting accounting code — methods not in
// the set (e.g., synthesized state service RPCs) are silently skipped.
type AccountingInfo struct {
	EnumName    string
	RPCs        map[string]bool
	RPCOrdinals map[string]int
}

// FilterServicesByGrpcEntries filters detected services to only those listed in gRPC config.
// Matches by both proto file and service name to avoid wrapping unrelated services
// (e.g., auto-generated state services) that happen to be in the same proto file.
func FilterServicesByGrpcEntries(servicesInfo []ServiceInfo, entries []tactype.GrpcEntry) []ServiceInfo {
	type key struct{ proto, svc string }
	wanted := make(map[key]bool)
	for _, fe := range entries {
		wanted[key{fe.ProtoFile, fe.ServiceName}] = true
	}
	var result []ServiceInfo
	for _, svc := range servicesInfo {
		if wanted[key{svc.ProtoFile, svc.ServiceName}] {
			result = append(result, svc)
		}
	}
	return result
}

// ResolveServerServices returns the services that should get server wrappers generated.
// If GrpcServerAll is set, returns all services with a gRPC annotation.
// If GrpcServers is set, filters to only those services.
// Otherwise auto-infers from the service's own proto file (identified by SelfPackage),
// plus any vservices in other proto files that declare {label: "<SelfPackage>"} on their
// service_s2s annotation — meaning this service is the designated host.
func ResolveServerServices(config *tactype.Config, servicesInfo []ServiceInfo) []ServiceInfo {
	// Client targets emit NO service runtime — never any server wrappers, even
	// for own-package services (an own-package (spec.service_c2s) in a client
	// package is the contract of the remote service the client CALLS, not one it
	// serves). Without this guard, python/typescript emitted s2sm server surfaces
	// with unresolvable imports in client-only builds (taxonomy: go/rust/java
	// full-service; python/typescript full-client; swift/csharp lite C2D).
	switch config.Language {
	case "python", "typescript", "swift", "csharp":
		return nil
	}
	if config.GrpcServerAll {
		return filterGrpcAnnotated(servicesInfo)
	}
	// Auto-discover own-package and hosted-vservice services when SelfPackage
	// is set. Tacplus may also append synthesized services (e.g. OpaEval) to
	// GrpcServers. Treat those as ADDITIVE so the auto-discovery of hosted
	// vservices via filterByHostTag still runs.
	var result []ServiceInfo
	if config.SelfPackage != "" {
		result = append(result, filterByPackage(servicesInfo, config.SelfPackage)...)
		result = append(result, filterByHostTag(servicesInfo, config.SelfPackage)...)
	}
	if len(config.GrpcServers) > 0 {
		for _, extra := range FilterServicesByGrpcEntries(servicesInfo, config.GrpcServers) {
			if extra.IsC2D {
				continue
			}
			dup := false
			for _, r := range result {
				if r.ProtoFile == extra.ProtoFile && r.ServiceName == extra.ServiceName {
					dup = true
					break
				}
			}
			if !dup {
				result = append(result, extra)
			}
		}
	}
	return result
}

// filterByHostTag returns vservice entries from other packages whose HostLabel matches pkg.
// These are services defined in vservices/ protos that designate pkg as their host via
// option (spec.service_s2s) = {label: "<pkg>"}.
func filterByHostTag(servicesInfo []ServiceInfo, pkg string) []ServiceInfo {
	var result []ServiceInfo
	for _, svc := range servicesInfo {
		if svc.HostLabel == pkg && svc.PackageName != pkg {
			result = append(result, svc)
		}
	}
	return result
}

// filterByPackage returns user-defined gRPC services from proto files whose
// package matches pkg. Used for server auto-inference from proto annotations.
// tacplus-synthesized services (state/store/mutation/opa_eval, marked
// Synthesized via the `// tacplus:generated-service` sentinel) are excluded —
// they are hosted via their own generated wiring. User-defined services are
// kept even when service_s2s has no auth_services (e.g. ConnectorStatus); the
// old "S2S && empty auth_services" heuristic wrongly dropped those.
func filterByPackage(servicesInfo []ServiceInfo, pkg string) []ServiceInfo {
	var result []ServiceInfo
	for _, svc := range servicesInfo {
		if svc.PackageName != pkg || !svc.IsGrpc {
			continue
		}
		if svc.IsC2D {
			continue
		}
		// Exclude tacplus-generated state/store/mutation/opa services.
		if svc.Synthesized {
			continue
		}
		result = append(result, svc)
	}
	return result
}

func deriveServiceAuthFlags(svc parser.ServiceDescriptor) (authAnyUser bool, hasMethodAuth bool) {
	authAnyUser = svc.AuthAnyUser
	hasMethodAuth = len(svc.AuthServices) > 0
	for _, m := range svc.Methods {
		if m.AuthAnyUser {
			authAnyUser = true
		}
		if len(m.AuthServices) > 0 || m.TargetNamespaceField != "" {
			hasMethodAuth = true
		}
	}
	return authAnyUser, hasMethodAuth
}

// InferGrpcClientsFromAnnotations augments config with GrpcS2SClients plus the
// package-local C2S/W2S client surfaces selected in tacplus.yaml. S2S remains
// transport-specific. C2S and W2S each resolve their proto-declared client
// dependency sets, then emit only the enabled YAML surfaces.
//
// No-op if config already has explicit entries for a given client type, or if
// SelfPackage is empty.
// protoDir must be the prepared proto directory (after CopyAndPrepareProtoDir) so that
// lib protos are overlaid and the descriptor parser can resolve all cross-file references.
func InferGrpcClientsFromAnnotations(config *tactype.Config, protoDir string) error {
	if config.SelfPackage == "" {
		return nil
	}

	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}
	allFiles := dp.GetAllFiles()

	ownPackageFound, allS2SClients, s2sRefs, c2sRefs, w2sRefs := collectOwnPackageGrpcAnnotations(config.SelfPackage, allFiles)
	if !ownPackageFound {
		return nil
	}

	// If the proto declares all_grpc_s2s_clients, set the all-flag and skip enumeration.
	if allS2SClients && !config.GrpcS2SClientAll {
		config.GrpcS2SClientAll = true
		zap.L().Info("gRPC S2S clients (all, from proto annotation)")
	}

	// Augment S2S clients if not already set from yaml or CLI.
	if len(config.GrpcS2SClients) == 0 && !config.GrpcS2SClientAll {
		entries, err := resolveGrpcRefs(s2sRefs, allFiles)
		if err != nil {
			return fmt.Errorf("grpc_s2s_client: %w", err)
		}
		config.GrpcS2SClients = entries
		for _, e := range entries {
			zap.L().Info("gRPC S2S client (from proto)", zap.String("proto", e.ProtoFile), zap.String("service", e.ServiceName))
		}
	}

	// C2S and W2S client surfaces are driven by proto declarations only. The
	// grpc_c2s_client and grpc_w2s_client refs are distinct transport surfaces
	// and are NOT merged: C2S comes from grpc_c2s_client, W2S from grpc_w2s_client.
	// No YAML clients.*.enabled flag gates this — generation is a function of the
	// proto graph.
	if len(config.GrpcC2SClients) == 0 {
		entries, err := resolveGrpcRefs(c2sRefs, allFiles)
		if err != nil {
			return fmt.Errorf("grpc_c2s_client: %w", err)
		}
		config.GrpcC2SClients = entries
		for _, e := range entries {
			zap.L().Info("gRPC C2S client (from proto declaration)",
				zap.String("proto", e.ProtoFile),
				zap.String("service", e.ServiceName))
		}
	}

	if len(config.GrpcW2SClients) == 0 {
		entries, err := resolveGrpcRefs(w2sRefs, allFiles)
		if err != nil {
			return fmt.Errorf("grpc_w2s_client: %w", err)
		}
		config.GrpcW2SClients = entries
		for _, e := range entries {
			zap.L().Info("gRPC W2S client (from proto declaration)",
				zap.String("proto", e.ProtoFile),
				zap.String("service", e.ServiceName))
		}
	}

	return nil
}

func collectOwnPackageGrpcAnnotations(selfPackage string, allFiles map[string]*parser.FileDescriptor) (found bool, allS2SClients bool, s2sRefs []string, c2sRefs []string, w2sRefs []string) {
	for _, fd := range allFiles {
		if fd.Package != selfPackage {
			continue
		}
		found = true
		allS2SClients = allS2SClients || fd.AllGrpcS2SClients
		s2sRefs = mergeGrpcRefs(s2sRefs, fd.GrpcS2SClients)
		c2sRefs = mergeGrpcRefs(c2sRefs, fd.GrpcC2SClients)
		w2sRefs = mergeGrpcRefs(w2sRefs, fd.GrpcW2SClients)
	}
	return found, allS2SClients, s2sRefs, c2sRefs, w2sRefs
}

func mergeGrpcRefs(groups ...[]string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, group := range groups {
		for _, ref := range group {
			if seen[ref] {
				continue
			}
			seen[ref] = true
			result = append(result, ref)
		}
	}
	return result
}

// resolveGrpcRefs converts "package.ServiceName" annotation strings to GrpcEntry values
// by looking up the exact package/service pair in descriptors.
func resolveGrpcRefs(refs []string, allFiles map[string]*parser.FileDescriptor) ([]tactype.GrpcEntry, error) {
	var entries []tactype.GrpcEntry
	for _, ref := range refs {
		dot := strings.LastIndex(ref, ".")
		if dot < 0 {
			return nil, fmt.Errorf("invalid ref %q: expected package.ServiceName", ref)
		}
		pkg := ref[:dot]
		svcName := ref[dot+1:]
		if svcName == "" {
			return nil, fmt.Errorf("invalid ref %q: empty service name", ref)
		}

		var matches []string
		packageSeen := false
		traitPlanePath := ""
		var eventC2SStatePaths []string
		for path, fd := range allFiles {
			if fd.Package != pkg {
				continue
			}
			packageSeen = true
			if t := fd.Trait; t != nil && (svcName == t.S2SService || svcName == t.C2SService) {
				traitPlanePath = path
			}
			for _, msg := range fd.Messages {
				if !msg.Annotations["event_c2s"].BoolValue || strings.HasSuffix(msg.Name, "Bus") {
					continue
				}
				if svcName == protogen.StateServiceName(msg.Name+"Bus") {
					eventC2SStatePaths = append(eventC2SStatePaths, path)
				}
			}
			for _, svc := range fd.Services {
				if svc.Name == svcName {
					matches = append(matches, path)
					break
				}
			}
		}
		// A trait's plane services are injected into the trait's proto later
		// (AppendRecordMapServiceToProtos), so a client referencing
		// <trait_pkg>.<declared service name> is valid even though the service is
		// not yet materialized during inference. The names come off the
		// declaration, so nothing here has to know what any plane is called.
		if len(matches) == 0 && traitPlanePath != "" {
			matches = append(matches, traitPlanePath)
		}
		// EventBus wrappers inject their grpc_c2s_client reference before the
		// wrapper's state service is materialized. Resolve only the canonical
		// per-event service name derived from a parsed event_c2s declaration.
		if len(matches) == 0 {
			matches = append(matches, eventC2SStatePaths...)
		}
		if !packageSeen {
			return nil, fmt.Errorf("no proto file found for package %q (from %q)", pkg, ref)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("service %q not found in package %q (from %q)", svcName, pkg, ref)
		}
		if len(matches) > 1 {
			return nil, fmt.Errorf("service ref %q is ambiguous; matched %d proto files", ref, len(matches))
		}
		entries = append(entries, tactype.GrpcEntry{
			ProtoFile:   matches[0],
			ServiceName: svcName,
		})
	}
	return entries, nil
}

// ResolveClientServices returns the services that should get s2s gRPC client wrappers generated.
// If GrpcS2SClientAll is set, returns all services with a gRPC annotation.
// If GrpcS2SClients is set, filters to only those services.
// Otherwise returns nil.
func ResolveClientServices(config *tactype.Config, servicesInfo []ServiceInfo) []ServiceInfo {
	if config.GrpcS2SClientAll {
		return filterGrpcAnnotated(servicesInfo)
	}
	if len(config.GrpcS2SClients) > 0 {
		return FilterServicesByGrpcEntries(servicesInfo, config.GrpcS2SClients)
	}
	return nil
}

// ResolveC2SClientServices returns the services that should get c2s (client-to-service) gRPC client wrappers generated.
func ResolveC2SClientServices(config *tactype.Config, servicesInfo []ServiceInfo) []ServiceInfo {
	if len(config.GrpcC2SClients) > 0 {
		return FilterServicesByGrpcEntries(servicesInfo, config.GrpcC2SClients)
	}
	return nil
}

// ResolveW2SClientServices returns the services that should get w2s (web-to-service, gRPC-over-REST) client wrappers generated.
func ResolveW2SClientServices(config *tactype.Config, servicesInfo []ServiceInfo) []ServiceInfo {
	if len(config.GrpcW2SClients) > 0 {
		return FilterServicesByGrpcEntries(servicesInfo, config.GrpcW2SClients)
	}
	return nil
}

// ResolveC2DServerServices returns own-package service_c2d services that should
// get a generated daemon SERVER surface (Handler trait + ValidationWrapper),
// served on a local unix socket — not on the s2s gRPC port.
//
// ResolveServerServices/filterByPackage deliberately exclude c2d from the normal
// server set (c2d must never be registered on the gRPC listener, and most
// language ServiceManagers don't host a c2d socket server). This is the additive
// set for backends whose ServiceManager DOES host a c2d socket server (currently
// Rust: HasC2DGrpc → C2dServer). Without it, an own-package daemon (e.g. the
// client daemon) gets a c2d client + raw tonic server traits but no generated
// ValidationWrapper, which regressed daemon hosting.
func ResolveC2DServerServices(config *tactype.Config, servicesInfo []ServiceInfo) []ServiceInfo {
	var result []ServiceInfo
	for _, svc := range servicesInfo {
		if !svc.IsC2D || !svc.IsGrpc {
			continue
		}
		// Own-package only: a daemon hosts its OWN c2d services. (SelfPackage
		// empty ⇒ single-package generation, include all c2d.)
		if config.SelfPackage != "" && svc.PackageName != config.SelfPackage {
			continue
		}
		result = append(result, svc)
	}
	return result
}

// ResolveC2DClientServices returns the services that should get c2d
// (client-to-daemon) gRPC client helpers generated.
func ResolveC2DClientServices(config *tactype.Config, servicesInfo []ServiceInfo) []ServiceInfo {
	// C2D is driven by proto: an explicit GrpcC2DClients allowlist narrows the
	// service_c2d services that may be emitted; otherwise all in-scope
	// own-package service_c2d services are emitted. No YAML flag gates this.
	if len(config.GrpcC2DClients) > 0 {
		return filterC2DAnnotated(FilterServicesByGrpcEntries(servicesInfo, config.GrpcC2DClients))
	}

	var result []ServiceInfo
	for _, svc := range servicesInfo {
		if !svc.IsC2D {
			continue
		}
		if config.SelfPackage == "" || svc.PackageName == config.SelfPackage {
			result = append(result, svc)
		}
	}
	return result
}

// ResolveRestClientServices returns the services that should get REST client wrappers generated.
func ResolveRestClientServices(config *tactype.Config, servicesInfo []ServiceInfo) []ServiceInfo {
	if config.RestClientAll {
		return filterGrpcAnnotated(servicesInfo)
	}
	if len(config.RestClients) > 0 {
		return FilterServicesByGrpcEntries(servicesInfo, config.RestClients)
	}
	return nil
}

// filterGrpcAnnotated returns non-C2D services from proto files that contain
// at least one non-C2D service with a gRPC annotation.
func filterGrpcAnnotated(servicesInfo []ServiceInfo) []ServiceInfo {
	// Find proto files that have gRPC-annotated services
	grpcProtos := make(map[string]bool)
	for _, svc := range servicesInfo {
		if svc.IsGrpc && !svc.IsC2D {
			grpcProtos[svc.ProtoFile] = true
		}
	}
	// Return all services from those proto files
	var result []ServiceInfo
	for _, svc := range servicesInfo {
		if !svc.IsC2D && grpcProtos[svc.ProtoFile] {
			result = append(result, svc)
		}
	}
	return result
}

func filterC2DAnnotated(servicesInfo []ServiceInfo) []ServiceInfo {
	var result []ServiceInfo
	for _, svc := range servicesInfo {
		if svc.IsC2D {
			result = append(result, svc)
		}
	}
	return result
}

// DetectServicesForWrapper scans proto files to find service definitions that need wrapper generation.
// Uses DescriptorParser rooted at inputDir so protoc can resolve cross-package imports.
// Only generates wrappers for proto files with services that have a gRPC annotation.
//
// outputDirFn computes the language-specific output directory for each service.
// It receives the config, the relative path of the proto file, the parent directory, and the package name.
func DetectServicesForWrapper(config *tactype.Config, inputDir string, outputDirFn func(config *tactype.Config, relPath, parentDir, packageName string) string) ([]ServiceInfo, error) {
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	var servicesInfo []ServiceInfo

	err = filepath.Walk(inputDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}

		relPath, _ := filepath.Rel(inputDir, path)
		fileDesc, err := descriptorParser.GetFileDescriptor(relPath)
		if err != nil {
			zap.L().Debug("Skipping proto file (no descriptor)", zap.String("path", relPath), zap.Error(err))
			return nil
		}

		// Only include files that have services with a gRPC annotation
		hasGrpc := false
		for _, svc := range fileDesc.Services {
			if svc.GrpcName != "" {
				hasGrpc = true
				break
			}
		}
		if len(fileDesc.Services) == 0 || !hasGrpc {
			return nil
		}

		parentDir := filepath.Dir(relPath)
		packageName := fileDesc.Package

		outputDir := outputDirFn(config, relPath, parentDir, packageName)

		// Include ALL services from proto files that have at least one gRPC service.
		// This matches Go's behavior where all services (including injected state
		// services) get wrappers generated if they are in a gRPC proto file.
		for _, svc := range fileDesc.Services {
			// Derive service-level auth flags from method-level annotations.
			anyUser, hasMethodAuth := deriveServiceAuthFlags(svc)
			authExempt := authExemptMethodPaths(packageName, svc)
			servicesInfo = append(servicesInfo, ServiceInfo{
				ProtoFile:       relPath,
				PackageName:     packageName,
				ServiceName:     svc.Name,
				GrpcName:        svc.GrpcName,
				IsGrpc:          svc.GrpcName != "",
				IsS2S:           svc.IsS2S,
				IsC2S:           svc.IsC2S,
				IsC2D:           svc.IsC2D,
				C2DSocketPath:   svc.C2DSocketPath,
				IsSMB:           svc.IsSMB,
				SmbRoot:         svc.SmbRoot,
				OutputDir:       outputDir,
				ParentDir:       parentDir,
				Config:          config,
				AuthDisabled:    svc.AuthDisabled,
				AuthAnyUser:     anyUser,
				AuthServices:    svc.AuthServices,
				HasMethodAuth:   hasMethodAuth,
				AuthExemptPaths: authExempt,
				EventLabel:      svc.EventLabel,
				HostLabel:       svc.HostLabel,
				Synthesized:     svc.Synthesized,
				SynthesizedKind: svc.SynthesizedKind,
			})
		}

		return nil
	})

	return servicesInfo, err
}

// authExemptMethodPaths returns the full gRPC paths of methods that must be
// reachable WITHOUT a token, i.e. those carrying auth_disabled (or every method
// when the whole service is auth_disabled).
//
// These feed the server's AuthEnforcingLayer. That layer runs before the
// per-method wrappers and rejects any request without an authorization header,
// so without this list an auth_disabled method is unreachable on the server even
// though the gateway correctly forwards it unauthenticated.
func authExemptMethodPaths(packageName string, svc parser.ServiceDescriptor) []string {
	var out []string
	for _, m := range svc.Methods {
		if svc.AuthDisabled || m.AuthDisabled {
			out = append(out, fmt.Sprintf("/%s.%s/%s", packageName, svc.Name, m.Name))
		}
	}
	return out
}
