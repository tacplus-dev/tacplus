// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/boundaries"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
)

// ServiceInfo represents a service that needs a wrapper generated.
type ServiceInfo struct {
	ProtoFile   string
	PackageName string
	ServiceName string
	GrpcName    string // service identity for routing (package name)
	IsGrpc      bool
	// HasMcp is true when the service publishes MCP tools. An MCP-only service
	// (no service_s2s/service_c2s/service_c2d, so IsGrpc is false) still
	// carries a synthesized MCP session carrier RPC and needs a wrapper, a
	// Handler entry and a ServiceManager mount like any other gRPC service.
	HasMcp         bool
	IsS2S          bool   // true if service has service_s2s annotation
	IsC2S          bool   // true if service has service_c2s annotation
	IsC2D          bool   // true if service has service_c2d annotation
	C2DSocketPath  string // service_c2d socket_path: unix socket the daemon binds (required, no default)
	IsSMB          bool   // true if service has smb annotation
	SmbRoot        spec.SmbRoot
	OutputDir      string
	TypesOutputDir string // Output dir for the types package (for outer class name detection)
	ParentDir      string
	Config         *tactype.Config
	AuthDisabled   bool     // C2S: no token required
	AuthAnyUser    bool     // C2S: any authenticated user can call
	AuthServices   []string // S2S: allowed caller identities
	HasMethodAuth  bool     // true if any per-method auth metadata must be registered
	HostLabel      string   // non-empty for vservices: the SelfPackage of the service that hosts this RPC surface
	EventLabel     string   // physical service routing label from spec.event_label
	// HasRequiresDevice is true when any gRPC-callable method declares
	// (spec.rpc).requires_device, so this service's wrapper carries the
	// device-approval gate and the ServiceManager must wire the check into it.
	HasRequiresDevice bool
	// SynthesizedKind is WHICH tacplus-generated surface this is
	// (parser.SynthesizedKind*), empty for hand-authored services. See the go
	// generator's field for why the kind, not just the fact, is what matters.
	SynthesizedKind string
}

// detectServicesForWrapper scans proto files to find service definitions that need wrapper generation.
// Includes annotated gRPC services and state services (service_state on messages).
// Handles both flat (services/X.proto) and directory-based (services/X/services.proto) layouts.
func detectServicesForWrapper(config *tactype.Config, inputDir string) ([]ServiceInfo, error) {
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	var servicesInfo []ServiceInfo

	// Check if inputDir has directory-based service protos ({dir}/{name}/{file}.proto).
	useDirectoryMode := false
	if topEntries, topErr := os.ReadDir(inputDir); topErr == nil {
		for _, topEntry := range topEntries {
			if !topEntry.IsDir() || topEntry.Name() == "sys" || topEntry.Name() == "google" {
				continue
			}
			subDir := filepath.Join(inputDir, topEntry.Name())
			if subEntries, subErr := os.ReadDir(subDir); subErr == nil {
				for _, entry := range subEntries {
					if entry.IsDir() {
						useDirectoryMode = true
						break
					}
				}
			}
			if useDirectoryMode {
				break
			}
		}
	}

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

		if len(fileDesc.Services) == 0 {
			return nil
		}

		// Check if any service has a gRPC service annotation,
		// or any message has service_state
		hasGrpc := false
		for _, svc := range fileDesc.Services {
			if svc.GrpcName != "" || svc.HasMcpRpcs {
				hasGrpc = true
			}
		}
		hasState := false
		for _, msg := range fileDesc.Messages {
			if ann, ok := msg.Annotations["service_state"]; ok && ann.StringValue != "" {
				hasState = true
				break
			}
		}
		if !hasGrpc && !hasState {
			return nil
		}

		// Strip tacplus/ prefix — import namespace doesn't affect output structure
		normalizedRelPath := strings.TrimPrefix(relPath, "tacplus"+string(filepath.Separator))
		parentDir := filepath.Dir(normalizedRelPath)
		packageName := fileDesc.Package
		// Strip dotted sub-packages (e.g., "baseball.services" → "baseball")
		if idx := strings.Index(packageName, "."); idx > 0 {
			packageName = packageName[:idx]
		}

		// In directory mode, derive package name from directory structure
		// since split service protos may have generic "services" package
		if useDirectoryMode {
			parts := strings.Split(normalizedRelPath, string(filepath.Separator))
			if len(parts) >= 2 && parts[0] != "sys" && parts[0] != "google" {
				packageName = parts[1]
			}
		}

		// All generated code lives in a shared source tree: config.OutputDir/src/main/java/
		srcDir := filepath.Join(config.OutputDir, "src", "main", "java")

		for _, service := range fileDesc.Services {
			// gRPC services carry a transport service annotation.
			// State services (StateService, WatchService) are in files with service_state messages.
			isGrpc := service.GrpcName != ""
			if !isGrpc && !hasState && !service.HasMcpRpcs {
				continue
			}

			var outputDir, typesOutputDir, javaPackage string

			typeDir := strings.Split(normalizedRelPath, string(filepath.Separator))[0]
			if useDirectoryMode {
				// Directory-based: {dir}/{name}/services.proto, {dir}/{name}/state.proto
				// Wrapper goes in types/{parentDir}/{name}/wrappers/ (separate from gRPC stubs)
				serviceBasePkg := fmt.Sprintf("%s.types.%s.%s", config.JavaPackageBase, typeDir, packageName)
				javaPackage = fmt.Sprintf("%s."+config.GetSmPrefix()+".internal.grpc.servers.%s.%s", config.JavaPackageBase, typeDir, packageName)

				packagePath := strings.ReplaceAll(javaPackage, ".", string(filepath.Separator))
				outputDir = filepath.Join(srcDir, packagePath)
				typesOutputDir = filepath.Join(srcDir, strings.ReplaceAll(serviceBasePkg+"."+packageName, ".", string(filepath.Separator)))
			} else {
				// Flat mode: wrapper goes in types/{typeDir}/{name}/wrappers/
				javaPackage = fmt.Sprintf("%s."+config.GetSmPrefix()+".internal.grpc.servers.%s.%s", config.JavaPackageBase, typeDir, packageName)

				packagePath := strings.ReplaceAll(javaPackage, ".", string(filepath.Separator))
				outputDir = filepath.Join(srcDir, packagePath)

				filenameStem := strings.TrimSuffix(filepath.Base(relPath), ".proto")
				typesJavaPackage := fmt.Sprintf("%s.types.%s.%s", config.JavaPackageBase, typeDir, filenameStem)
				typesOutputDir = filepath.Join(srcDir, strings.ReplaceAll(typesJavaPackage, ".", string(filepath.Separator)), "types")
			}

			// Derive service-level auth flags from method-level annotations.
			anyUser := service.AuthAnyUser
			// A service with no RPCs registers nothing, and its wrapper emits no
			// register-method-auth function; see the Go generator for the
			// ServiceManager call this would otherwise dangle.
			hasMethodAuth := len(service.Methods) > 0 && len(service.AuthServices) > 0
			hasRequiresDevice := false
			for _, m := range service.Methods {
				// An MCP tool has no generated gRPC wrapper, so it contributes
				// nothing to the wrapper's auth surface — no per-method auth
				// registration, no service-level any-user widening.
				if m.IsMcp {
					continue
				}
				if m.AuthAnyUser {
					anyUser = true
				}
				if len(m.AuthServices) > 0 {
					hasMethodAuth = true
				}
				if len(m.AuthActors) > 0 {
					hasMethodAuth = true
				}
				if m.TargetNamespaceField != "" && !m.AuthAnyUser {
					hasMethodAuth = true
				}
				if m.HasDeviceGate() {
					hasRequiresDevice = true
				}
			}
			servicesInfo = append(servicesInfo, ServiceInfo{
				ProtoFile:       relPath,
				PackageName:     packageName,
				ServiceName:     service.Name,
				GrpcName:        service.GrpcName,
				IsGrpc:          isGrpc,
				HasMcp:          service.HasMcpRpcs,
				IsS2S:           service.IsS2S,
				SynthesizedKind: service.SynthesizedKind,
				IsC2S:           service.IsC2S,
				IsC2D:           service.IsC2D,
				C2DSocketPath:   service.C2DSocketPath,
				IsSMB:           service.IsSMB,
				SmbRoot:         service.SmbRoot,
				OutputDir:       outputDir,
				TypesOutputDir:  typesOutputDir,
				ParentDir:       parentDir,
				Config:          config,
				AuthDisabled:    service.AuthDisabled,
				AuthAnyUser:     anyUser,
				AuthServices:    service.AuthServices,
				HasMethodAuth:   hasMethodAuth,
				HostLabel:       service.HostLabel,
				EventLabel:      service.EventLabel,

				HasRequiresDevice: hasRequiresDevice,
			})
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	return servicesInfo, nil
}

// filterServicesByGrpcEntries returns services whose ProtoFile matches any gRPC entry's ProtoFile.
func filterServicesByGrpcEntries(servicesInfo []ServiceInfo, entries []tactype.GrpcEntry) []ServiceInfo {
	protoSet := make(map[string]bool)
	for _, entry := range entries {
		protoSet[entry.ProtoFile] = true
	}
	var result []ServiceInfo
	for _, svc := range servicesInfo {
		if protoSet[svc.ProtoFile] {
			result = append(result, svc)
		}
	}
	return result
}

// resolveServerServices returns services that should get server wrappers.
// If GrpcServerAll is set, returns all services with a gRPC annotation.
// If GrpcServers is set, filters to only those services.
// Otherwise auto-infers:
//   - Own-package S2S/C2S services (e.g. services/identity.proto with service_s2s when SelfPackage="identity")
//   - Hosted vservices from other packages that declare {label: "<SelfPackage>"} on their service_s2s annotation
func resolveServerServices(config *tactype.Config, servicesInfo []ServiceInfo) []ServiceInfo {
	if config.GrpcServerAll {
		return filterGrpcAnnotated(servicesInfo)
	}
	if len(config.GrpcServers) > 0 {
		var result []ServiceInfo
		for _, svc := range filterServicesByGrpcEntries(servicesInfo, config.GrpcServers) {
			if !svc.IsC2D {
				result = append(result, svc)
			}
		}
		return result
	}
	if config.SelfPackage != "" {
		var result []ServiceInfo
		// Include own-package S2S/C2S gRPC services (the service's own hosted endpoints)
		for _, svc := range servicesInfo {
			// An MCP-only service (IsGrpc false) still carries a synthesized
			// session carrier RPC to host; mount it like any other gRPC service.
			if svc.PackageName == config.SelfPackage && ((svc.IsGrpc && (svc.IsS2S || svc.IsC2S)) || svc.HasMcp) {
				result = append(result, svc)
			}
		}
		// Include hosted vservices from other packages that designate this package as host
		result = append(result, filterByHostTag(servicesInfo, config.SelfPackage)...)
		if len(result) > 0 {
			return result
		}
	}
	return nil
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

// resolveClientServices returns services that should get gRPC client wrappers.
// Mirrors Go behavior: only explicit gRPC client config enables generation.
func resolveClientServices(config *tactype.Config, servicesInfo []ServiceInfo) []ServiceInfo {
	if config.GrpcS2SClientAll {
		return filterGrpcAnnotated(servicesInfo)
	}
	if len(config.GrpcS2SClients) > 0 {
		return filterServicesByGrpcEntries(servicesInfo, config.GrpcS2SClients)
	}
	return nil
}

// resolveC2SClientServices returns the services that should get c2s gRPC client wrappers.
func resolveC2SClientServices(config *tactype.Config, servicesInfo []ServiceInfo) []ServiceInfo {
	if len(config.GrpcC2SClients) > 0 {
		return filterServicesByGrpcEntries(servicesInfo, config.GrpcC2SClients)
	}
	return nil
}

// resolveC2DClientServices returns the services that should get c2d gRPC client wrappers.
func resolveC2DClientServices(config *tactype.Config, servicesInfo []ServiceInfo) []ServiceInfo {
	if len(config.GrpcC2DClients) > 0 {
		return filterC2DAnnotated(filterServicesByGrpcEntries(servicesInfo, config.GrpcC2DClients))
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

// resolveRestClientServices returns the services that should get REST client wrappers.
func resolveRestClientServices(config *tactype.Config, servicesInfo []ServiceInfo) []ServiceInfo {
	if config.RestClientAll {
		return filterGrpcAnnotated(servicesInfo)
	}
	if len(config.RestClients) > 0 {
		return filterServicesByGrpcEntries(servicesInfo, config.RestClients)
	}
	return nil
}

// filterGrpcAnnotated returns all services from proto files that have gRPC annotations.
func filterGrpcAnnotated(servicesInfo []ServiceInfo) []ServiceInfo {
	grpcProtos := make(map[string]bool)
	for _, svc := range servicesInfo {
		if (svc.IsGrpc || svc.HasMcp) && !svc.IsC2D {
			grpcProtos[svc.ProtoFile] = true
		}
	}
	var result []ServiceInfo
	for _, svc := range servicesInfo {
		if !svc.IsC2D && grpcProtos[svc.ProtoFile] {
			result = append(result, svc)
		}
	}
	return result
}

// resolveC2DServerServices returns the own-package service_c2d services the
// ServiceManager hosts on its c2d socket server (see
// service.ResolveC2DServerServices, which Rust resolves through).
func resolveC2DServerServices(config *tactype.Config, servicesInfo []ServiceInfo) []ServiceInfo {
	var result []ServiceInfo
	for _, svc := range servicesInfo {
		if service.IsC2DServer(svc.IsC2D, svc.IsGrpc, svc.PackageName, config.SelfPackage) {
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

// generatedGrpcWrapper tracks info about a generated service wrapper for aggregators.
type generatedGrpcWrapper struct {
	PackageName string // e.g. "baseball"
	ServiceName string // proto service name: "BaseballService"
	ClassName   string // e.g. "BaseballGrpcWrapper"
	ParentDir   string // e.g. "services"
	FullPackage string // e.g. "com.example.s2sm.internal.grpc.servers.services.baseball"
}

// generateServiceWrappers generates ServiceWrapper.java for each service in the services directory.
// Uses a single DescriptorParser rooted at inputDir so protoc can resolve cross-package imports.
// generateServiceWrappers generates every service wrapper and returns, keyed
// by ServiceName, whether that wrapper ended up with accounting wired in
// (i.e. defines setEmitter) — post Q2-exclusion. The ServiceManager template
// uses this to guard its setEmitter(...) wiring loop, since not every wrapper
// necessarily has that method (services with no accounted RPCs don't).
func generateServiceWrappers(config *tactype.Config, servicesInfo []ServiceInfo, inputDir string, accountingMap map[string]service.AccountingInfo, skippedTraceTypes proto.SkippedTraceTypes) (map[string]bool, error) {
	// Create one descriptor parser for the entire proto root (like Go does)
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	// Build the egress-sanitize oracle over the (entitlement-augmented) descriptor
	// set, mirroring the helpers emitter's file set (non-infra), so the wrapper
	// emits a sanitize<T> call exactly where the helpers emitted one.
	var allFiles []model.ProtoFile
	for relPath := range descriptorParser.GetAllFiles() {
		if proto.IsInfraProto(relPath) {
			continue
		}
		mf, err := descriptorParser.ConvertToProtoFile(relPath)
		if err != nil {
			return nil, fmt.Errorf("convert %s for egress sanitize resolution: %w", relPath, err)
		}
		allFiles = append(allFiles, mf)
	}
	san := newJavaEgressSanitizer(allFiles)

	// Q2: resolve the accounting topic once so every wrapper can drop that
	// topic's own publish RPC from its accounting map. See
	// accounting_exclusion.go for why this exclusion is mandatory.
	accountingTopic, hasAccountingTopic, err := boundaries.LookupAccountingRecordTopic(descriptorParser)
	if err != nil {
		return nil, fmt.Errorf("resolve accounting record topic: %w", err)
	}

	var generated []generatedGrpcWrapper
	hasAccountingByService := make(map[string]bool, len(servicesInfo))

	for _, svc := range servicesInfo {
		// Use the descriptor parser to get method information
		protoInfo, err := descriptorParser.ConvertToProtoInfo(svc.ProtoFile)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", svc.ProtoFile, err)
		}

		// Generate ServiceWrapper.java in the services output directory
		svcAccountingMap := excludeAccountingSelfPublish(
			accountingMap, svc.GrpcName, protoInfo.Package, svc.ServiceName,
			accountingTopic, hasAccountingTopic)
		hasAccounting, err := generateServiceWrapper(svc, protoInfo, svcAccountingMap, skippedTraceTypes, san)
		if err != nil {
			return nil, fmt.Errorf("failed to generate service wrapper for %s: %w", svc.ServiceName, err)
		}
		hasAccountingByService[svc.ServiceName] = hasAccounting

		// Track for aggregator generation (only gRPC services, including an
		// MCP-only service whose single gRPC surface is the session carrier)
		if svc.IsGrpc || svc.HasMcp {
			className := serverClassName(svc.ServiceName)
			// Determine parentDir group
			normalizedRelPath := strings.TrimPrefix(svc.ProtoFile, "tacplus"+string(filepath.Separator))
			parentDir := strings.Split(normalizedRelPath, string(filepath.Separator))[0]

			fullPackage := fmt.Sprintf("%s."+config.GetSmPrefix()+".internal.grpc.servers.%s.%s", config.JavaPackageBase, parentDir, svc.PackageName)

			generated = append(generated, generatedGrpcWrapper{
				PackageName: svc.PackageName,
				ServiceName: svc.ServiceName,
				ClassName:   className,
				ParentDir:   parentDir,
				FullPackage: fullPackage,
			})
		}
	}

	// Generate per-group gRPC aggregators
	if err := generateGrpcGroupAggregators(config, generated); err != nil {
		return nil, fmt.Errorf("failed to generate gRPC group aggregators: %w", err)
	}

	// Generate root gRPC aggregator
	if err := generateGrpcRootAggregator(config, generated); err != nil {
		return nil, fmt.Errorf("failed to generate gRPC root aggregator: %w", err)
	}

	return hasAccountingByService, nil
}

// getOuterClassNameForWrapper detects the Java outer class name for a proto file.
// Protobuf-Java nests all message types inside an outer class derived from the proto filename.
// If there's a naming conflict (message with same name as file), protoc adds "OuterClass" suffix.
// typesOutputDir is the directory where protoc generated the types (for checking OuterClass variant).
func getOuterClassNameForWrapper(typesOutputDir, protoFile string) string {
	// Default: capitalize proto filename
	protoFileName := filepath.Base(protoFile)
	defaultName := strings.TrimSuffix(protoFileName, ".proto")
	if len(defaultName) > 0 {
		defaultName = strings.ToUpper(defaultName[:1]) + defaultName[1:]
	}

	// Check if the OuterClass variant exists for the simple default name.
	outerClassPath := filepath.Join(typesOutputDir, defaultName+"OuterClass.java")
	if _, err := os.Stat(outerClassPath); err == nil {
		return defaultName + "OuterClass"
	}

	// Snake-case proto filenames (e.g. tenant_config.proto) become PascalCase
	// outer classes (TenantConfig / TenantConfigOuterClass) in protoc's output,
	// not Tenant_config. Check the PascalCase variants too.
	if strings.Contains(defaultName, "_") {
		pascal := strutil.ToPascalCase(strings.TrimSuffix(protoFileName, ".proto"))
		if pascal != "" {
			if _, err := os.Stat(filepath.Join(typesOutputDir, pascal+"OuterClass.java")); err == nil {
				return pascal + "OuterClass"
			}
			if _, err := os.Stat(filepath.Join(typesOutputDir, pascal+".java")); err == nil {
				return pascal
			}
		}
	}

	return defaultName
}

// serverClassName returns the Java class name for a service wrapper.
func serverClassName(serviceName string) string {
	return serviceName
}

// generateServiceWrapper creates a named wrapper Java file for a single service.
// gRPC services → {Name}Service.java (e.g., BaseballService.java)
func generateServiceWrapper(svc ServiceInfo, info *parser.ProtoInfo, accountingMap map[string]service.AccountingInfo, skippedTraceTypes proto.SkippedTraceTypes, san *javaEgressSanitizer) (bool, error) {
	className := serverClassName(svc.ServiceName)
	outputPath := filepath.Join(svc.OutputDir, className+".java")

	if err := os.MkdirAll(svc.OutputDir, 0755); err != nil {
		return false, fmt.Errorf("failed to create service wrapper output dir: %w", err)
	}

	zap.L().Info("Generating service wrapper", zap.String("service", svc.ServiceName), zap.String("path", outputPath))

	// Find the target service in the proto info
	var targetService *parser.ServiceInfo
	for i := range info.Services {
		if info.Services[i].Name == svc.ServiceName {
			targetService = info.Services[i]
			break
		}
	}
	if targetService == nil {
		return false, fmt.Errorf("service %s not found in proto info for %s", svc.ServiceName, svc.ProtoFile)
	}

	serviceName := svc.ServiceName

	// Detect the Java outer class name (all message types are nested inside it).
	var outerClassName string
	protoDir := filepath.Join(svc.Config.OutputDir, "proto")

	// Determine if the service proto is flat (services/baseball.proto) or
	// directory-based (services/baseball/types.proto).
	svcName, svcFileName := proto.ServiceProtoParts(svc.ProtoFile)
	isFlatServiceProto := svcName == svcFileName

	isStateService := !svc.IsGrpc && !svc.HasMcp

	if isStateService {
		// For state services, read the outer class from the state watch proto itself
		stateProtoPath := filepath.Join(protoDir, svc.ProtoFile)
		if name := extractJavaOuterClassName(stateProtoPath); name != "" {
			outerClassName = name
		} else {
			outerClassName = getOuterClassNameForWrapper(svc.OutputDir, svc.ProtoFile)
		}
	} else if isFlatServiceProto {
		// For flat gRPC services, read java_outer_classname from the flat proto
		typesProtoPath := filepath.Join(protoDir, svc.ProtoFile)
		if name := extractJavaOuterClassName(typesProtoPath); name != "" {
			outerClassName = name
		} else {
			outerClassName = getOuterClassNameForWrapper(svc.TypesOutputDir, svc.ProtoFile)
		}
	} else {
		// For directory-based gRPC services, read from types.proto
		normalizedPath := strings.TrimPrefix(svc.ProtoFile, "tacplus"+string(filepath.Separator))
		svcTypeDir := strings.Split(normalizedPath, string(filepath.Separator))[0]
		typesProtoFileName := svc.PackageName
		typesProtoPath := filepath.Join(protoDir, svcTypeDir, svc.PackageName, typesProtoFileName+".proto")
		if name := extractJavaOuterClassName(typesProtoPath); name != "" {
			outerClassName = name
		} else {
			outerClassName = getOuterClassNameForWrapper(svc.TypesOutputDir, svc.ProtoFile)
		}
	}

	// Compute Java package for the wrapper.
	wrapperTypeDir := strings.Split(svc.ParentDir, string(filepath.Separator))[0]
	var fullPackage, typesPackage, grpcStubsPackage string
	if isFlatServiceProto {
		fullPackage = fmt.Sprintf("%s."+svc.Config.GetSmPrefix()+".internal.grpc.servers.%s.%s", svc.Config.JavaPackageBase, wrapperTypeDir, svc.PackageName)
		typesPackage = fmt.Sprintf("%s.types.%s.%s", svc.Config.JavaPackageBase, wrapperTypeDir, svc.PackageName)
		grpcStubsPackage = typesPackage
	} else {
		fullPackage = fmt.Sprintf("%s."+svc.Config.GetSmPrefix()+".internal.grpc.servers.%s.%s", svc.Config.JavaPackageBase, wrapperTypeDir, svc.PackageName)
		typesPackage = fmt.Sprintf("%s.types.%s.%s.%s", svc.Config.JavaPackageBase, wrapperTypeDir, svc.PackageName, svc.PackageName)
		grpcStubsPackage = typesPackage
	}

	// Build cross-package outer class name map and imports
	crossPkgOuterClasses := make(map[string]string) // proto package -> outer class name
	var crossPkgImportLines []string
	for pkg, dir := range info.CrossPackageTypes {
		if dir == "" {
			continue
		}
		dirParts := strings.Split(dir, string(filepath.Separator))

		// Compute cross-package Java package using filename stem
		crossJavaPackage := javaPackageForProtoPackage(svc.Config.JavaPackageBase, pkg, dir)

		// Determine the sub-package where types live.
		// For directory layout (2+ path components), types are in .{serviceName}/ sub-package.
		// For flat layout, types are directly in the package (no sub-package).
		var typeSubPkg string
		if len(dirParts) >= 2 {
			typeSubPkg = pkg
		} else {
			typeSubPkg = "" // types are directly in the package
		}

		// Get the outer class name for the cross-package proto file (shared source tree)
		crossTypesSrcDir := filepath.Join(svc.Config.OutputDir, "src", "main", "java")
		var crossTypesPath string
		if typeSubPkg != "" {
			crossTypesPath = filepath.Join(crossTypesSrcDir, strings.ReplaceAll(crossJavaPackage+"."+typeSubPkg, ".", string(filepath.Separator)))
		} else {
			crossTypesPath = filepath.Join(crossTypesSrcDir, strings.ReplaceAll(crossJavaPackage, ".", string(filepath.Separator)))
		}
		crossProtoFile := filepath.Join(dir, pkg+".proto")
		crossOuterClass := getOuterClassNameForWrapper(crossTypesPath, crossProtoFile)
		crossPkgOuterClasses[pkg] = crossOuterClass

		// Add import statement - import only the outer class to avoid wildcard ambiguity
		// (e.g., both packages may have a Helpers class, so we avoid "import pkg.types.*")
		if typeSubPkg != "" {
			crossPkgImportLines = append(crossPkgImportLines, fmt.Sprintf("import %s.%s.%s;", crossJavaPackage, typeSubPkg, crossOuterClass))
		} else {
			crossPkgImportLines = append(crossPkgImportLines, fmt.Sprintf("import %s.%s;", crossJavaPackage, crossOuterClass))
		}
	}
	// Sort for reproducible output — CrossPackageTypes is a Go map, so ranging it
	// emits these imports in random order; unlike Go there is no downstream
	// formatter to re-sort them. Sibling client generators (grpcclient/restclient/
	// w2sclient) already sort their import lines identically.
	sort.Strings(crossPkgImportLines)

	// Build cross-package Helpers class qualifiers for validation calls.
	// For cross-package types, we must use the fully qualified Helpers class name
	// because Java doesn't support import aliases and the local Helpers is already in scope.
	crossPkgHelpers := make(map[string]string) // proto package -> "fully.qualified.Helpers"
	for pkg, dir := range info.CrossPackageTypes {
		if dir == "" {
			continue
		}
		crossDirParts := strings.Split(dir, string(filepath.Separator))
		crossJavaPackage := javaPackageForProtoPackage(svc.Config.JavaPackageBase, pkg, dir)
		if len(crossDirParts) >= 2 {
			crossPkgHelpers[pkg] = crossJavaPackage + "." + pkg + ".Helpers"
		} else {
			crossPkgHelpers[pkg] = crossJavaPackage + ".Helpers"
		}
	}

	// Determine if this service has accounting support: grpcName must be in the
	// accountingMap AND at least one method must be in the accounting enum.
	hasAccounting := false
	if svc.GrpcName != "" {
		if acctInfo, ok := accountingMap[svc.GrpcName]; ok {
			for _, method := range targetService.Methods {
				if acctInfo.RPCs[method.Name] {
					hasAccounting = true
					break
				}
			}
		}
	}
	var accountingImports string
	if hasAccounting {
		accountingImports = fmt.Sprintf(
			"import %s.chassis.am.AccountingManager;\nimport %s.types.generated.accounting_method.AccountingMethodOuterClass.AccountingMethod;\nimport %s.types.generated.services.Services;",
			svc.Config.JavaPackageBase, svc.Config.JavaPackageBase, svc.Config.JavaPackageBase)
	}

	// Build a map of message name → field names for convention-based checks.
	// Used to detect well-known fields (e.g. user_token) without requiring annotations.
	msgFields := make(map[string]map[string]bool)
	for _, msg := range info.Messages {
		fields := make(map[string]bool)
		for _, f := range msg.Fields {
			fields[f.Name] = true
		}
		msgFields[msg.Name] = fields
	}

	// Build Handler interface and gRPC method overrides with validation (only for the target service)
	var handlerInterface strings.Builder
	var grpcMethods, watchFields strings.Builder

	handlerInterface.WriteString(fmt.Sprintf("    /**\n     * %sHandler defines the clean handler interface for %s.\n     * Implement this interface instead of the raw gRPC ImplBase.\n     * The caller parameter provides the authenticated caller's identity (null when disable_auth = true).\n     */\n", serviceName, serviceName))
	handlerInterface.WriteString(fmt.Sprintf("    public interface %sHandler {\n", serviceName))

	// Build registerMethodAuth body: collect per-method auth_services and target namespace metadata.
	// Method-level auth_services overrides service-level.
	var methodAuthLines []string
	// The handler-interface declarations of the NON-tool rpcs, kept so the
	// generated MCP self-test's stub can implement the whole interface with the
	// wrapper's own signatures rather than a second renderer's guess at them.
	var nonToolHandlerDecls []string
	// The peer half of a record query plane, whoever hosts it. It is the one
	// s2s surface whose caller can be either a /system peer (fleet-wide, as
	// always) or a per-tenant peer (pinned), so it is the one s2s surface that
	// carries the (spec.store_namespace) seams under a runtime gate. Every
	// other s2s wrapper generates exactly what it generated before.
	//
	// The host's own option (spec.service).tenant_owned is deliberately NOT a
	// condition here. The tenant boundary belongs to the CALLER: a
	// /tenants/<t>/services/<svc> peer reading through a SHARED host must still
	// be pinned to <t>'s rows. Gating on the host would emit no pin at all on
	// exactly that path — a fleet-wide read for a tenant caller, backwards from
	// the boundary this seam exists to enforce. The pin itself is already
	// caller-keyed at runtime (__nsPinned reads the request's own token claim),
	// so emitting it unconditionally here costs /system peers nothing.
	recordPlaneS2S := !svc.IsC2S && svc.SynthesizedKind == parser.SynthesizedKindRecordMap

	// The generated MCP session carrier, when this service has one. It is the
	// one rpc the wrapper serves without a handler method.
	mcpCarrier := javaMcpCarrierMethod(targetService)
	for _, method := range targetService.Methods {
		// An RPC_PRINCIPAL_MCP rpc is a tool, not a gRPC call: it gets no
		// @Override on the gRPC ImplBase and no per-method auth registration.
		// The generated ImplBase already answers UNIMPLEMENTED for a method the
		// wrapper does not override, so unlike Rust's tonic trait nothing has to
		// be emitted in its place.
		//
		// It DOES get a handler-interface method: that is what the generated MCP
		// dispatch calls (mcp_dispatch_gen.go). Only the gRPC-callable surface
		// is suppressed.
		if method.IsMcp {
			inputType := resolveJavaMethodType(method.FullInputType, method.InputType, info.Package, outerClassName, crossPkgOuterClasses)
			outputType := resolveJavaMethodType(method.FullOutputType, method.OutputType, info.Package, outerClassName, crossPkgOuterClasses)
			handlerInterface.WriteString(generateJavaHandlerMethod(method, inputType, outputType, crossPkgOuterClasses))
			continue
		}
		inputType := resolveJavaMethodType(method.FullInputType, method.InputType, info.Package, outerClassName, crossPkgOuterClasses)
		outputType := resolveJavaMethodType(method.FullOutputType, method.OutputType, info.Package, outerClassName, crossPkgOuterClasses)
		inputHelpers := resolveValidationHelpers(method.FullInputType, info.Package, crossPkgHelpers)
		outputHelpers := resolveValidationHelpers(method.FullOutputType, info.Package, crossPkgHelpers)
		if method.IsWatchCollection() {
			changeType := javaWatchTypeRef(method.Resolved.WatchChangeType, outputType, crossPkgOuterClasses)
			fmt.Fprintf(&watchFields, "    private final CollectionRuntime.WatchHub<%s> %sWatchHub = new CollectionRuntime.WatchHub<>(%d, %d);\n",
				changeType, methodNameLowerCamel(method.Name), method.Resolved.ReplayDepth, method.Resolved.IdleSecs)
		}
		// A method carrying (spec.min_group_size) that this wrapper cannot guard
		// must fail generation, not ship the size-only RPC unguarded.
		if why := javaMinGroupSizeUnenforceable(method, san, info.Package); why != "" {
			return false, fmt.Errorf("%s.%s carries (spec.min_group_size) = %d but %s",
				targetService.Name, method.Name, method.MinGroupSize, why)
		}
		// A row mutation over an append-only record has no guard to render: the
		// correct generated code for it is none at all.
		if why := javaAppendOnlyUnenforceable(method, san, info.Package); why != "" {
			return false, fmt.Errorf("%s.%s must not be generated because %s",
				targetService.Name, method.Name, why)
		}
		// A write whose store epoch this wrapper cannot stamp must fail
		// generation, not reach a handler with an unstamped epoch.
		if why := javaStoreEpochUnenforceable(method, san, info.Package); why != "" {
			return false, fmt.Errorf("%s.%s carries a (spec.store_epoch) field on %s but %s",
				targetService.Name, method.Name, method.FullInputType, why)
		}
		// (spec.owner_default): what the row's OWNER may do with its own rows.
		// A method that would be refused but has no seam at which the caller can
		// be compared to the row's principal must fail generation, not ship
		// under-enforced — the same set the Go and Rust wrappers refuse.
		if why, err := javaOwnerDefaultUnhostable(method, san, info.Package); err != nil {
			return false, fmt.Errorf("%s.%s: %w", targetService.Name, method.Name, err)
		} else if why != "" {
			return false, fmt.Errorf("%s.%s touches a type declaring (spec.owner_default) that contradicts its access, but %s",
				targetService.Name, method.Name, why)
		}
		// A c2s read over record rows the wrapper cannot narrow to the caller
		// must fail generation, not ship an unfiltered read plane.
		if why := javaRecordPrincipalUnenforceable(method, san, info.Package, svc.IsC2S); why != "" {
			return false, fmt.Errorf("%s.%s cannot be generated for a c2s surface because %s",
				targetService.Name, method.Name, why)
		}
		// (spec.rpc).requires_device: the device-only approval gate. Its
		// preconditions are structural, so a build that cannot meet one of them
		// fails here rather than generating an RPC the author believes is gated
		// and is not. Same three refusals the Go backend makes, for the same
		// reasons, and requires_device_when meets them exactly as
		// requires_device does — when it fires it hashes the WHOLE request.
		if method.HasDeviceGate() {
			if method.AuthDisabled || svc.AuthDisabled || svc.IsC2D {
				return false, fmt.Errorf("%s.%s declares (spec.rpc).%s, but its auth is disabled; the check must name the user the caller is serving (caller_namespace/caller_subject), and an rpc with no verified token has no user to name",
					targetService.Name, method.Name, method.DeviceGateAnnotation())
			}
			if method.ClientStreaming || method.ServerStreaming || method.Collection != nil {
				return false, fmt.Errorf("%s.%s declares (spec.rpc).%s, but it is not a plain unary rpc; an approval covers ONE request's hash, which a stream or a collection window does not have",
					targetService.Name, method.Name, method.DeviceGateAnnotation())
			}
			if f := javaRequiresDeviceMapField(method.FullInputType, san, info.Package); f != "" {
				return false, fmt.Errorf("%s.%s declares (spec.rpc).%s, but its request reaches a map field (%s); map iteration order is not canonical across languages, so the approval hash could not be computed identically on both ends — remove the map or drop the annotation",
					targetService.Name, method.Name, method.DeviceGateAnnotation(), f)
			}
		}
		// The generated session carrier is served by the wrapper itself
		// (javaMcpTransport), not by the service: nothing about carrying an MCP
		// session is the service's to write. Its TOOLS are on this interface;
		// the carriage is not. It still gets the ordinary bidi gRPC override —
		// same authorization, same caller extraction, same frame validation —
		// with only the delegation target swapped.
		if method != mcpCarrier {
			decl := generateJavaHandlerMethod(method, inputType, outputType, crossPkgOuterClasses)
			handlerInterface.WriteString(decl)
			// An auto-exposed rpc reaches here because it KEEPS its gRPC surface
			// (unlike a declared RPC_PRINCIPAL_MCP rpc, which continued above) —
			// but it is still a tool, and the generated MCP self-test already
			// renders a recording stub for every tool. Recording it here too
			// would give the stub two overrides of the same method, which does
			// not compile. Only an rpc that is not a tool at all belongs in the
			// throwing-stub list.
			if !method.IsMcpTool() {
				nonToolHandlerDecls = append(nonToolHandlerDecls, decl)
			}
		}
		effectiveAuthAnyUser := method.AuthAnyUser
		grpcMethods.WriteString(generateJavaGRPCWrapperSurface(method, serviceName, svc.GrpcName, inputType, outputType, inputHelpers, outputHelpers, effectiveAuthAnyUser, method.AuthDisabled || svc.AuthDisabled || svc.IsC2D, accountingMap, info.Package, svc.ParentDir, info.CrossPackageTypes, skippedTraceTypes, san, crossPkgHelpers, crossPkgOuterClasses, svc.IsC2S, recordPlaneS2S))
		grpcMethods.WriteString("\n")

		// Per-method policy is keyed by the SERVICE-QUALIFIED identifier the
		// wrapper passes to Authorizer.authorize() for this very method, never
		// by the bare RPC name: one process may host several services behind
		// one Authorizer, and a bare-name key lets a second service's
		// identically named RPC silently overwrite the first's auth_services.
		methodAuthKey := javaMethodAuthKey(info.Package, serviceName, method.Name, svc.ParentDir)

		// Resolve effective auth_services: method-level overrides service-level.
		effectiveAuth := svc.AuthServices
		if len(method.AuthServices) > 0 {
			effectiveAuth = method.AuthServices
		}
		if len(effectiveAuth) > 0 {
			var quoted []string
			for _, pattern := range effectiveAuth {
				quoted = append(quoted, fmt.Sprintf("%q", pattern))
			}
			methodAuthLines = append(methodAuthLines, fmt.Sprintf(
				"        authorizer.setMethodAuth(\"%s\", java.util.List.of(%s));",
				methodAuthKey, strings.Join(quoted, ", ")))
		}

		// Register the per-RPC auth_actors patterns. There is no service-level
		// default to fall back to: auth_actors is declared on spec.rpc only.
		// The non-empty guard is what makes the gate fail closed — an RPC that
		// declares nothing registers nothing, and a missing map entry rejects
		// any token carrying act.
		if len(method.AuthActors) > 0 {
			var quoted []string
			for _, pattern := range method.AuthActors {
				quoted = append(quoted, fmt.Sprintf("%q", pattern))
			}
			methodAuthLines = append(methodAuthLines, fmt.Sprintf(
				"        authorizer.setMethodActors(\"%s\", java.util.List.of(%s));",
				methodAuthKey, strings.Join(quoted, ", ")))
		}

		// Register request-field target_namespace_field for level-or-below enforcement.
		// auth_any_user uses target_namespace_field as a fixed namespace literal instead.
		if method.TargetNamespaceField != "" && !effectiveAuthAnyUser {
			methodAuthLines = append(methodAuthLines, fmt.Sprintf(
				"        authorizer.setMethodTargetNamespaceField(%q, %q);",
				methodAuthKey, method.TargetNamespaceField))
		}
	}
	handlerInterface.WriteString("    }\n")

	// Build registerMethodAuth static method code.
	var registerMethodAuth string
	if len(methodAuthLines) > 0 {
		registerMethodAuth = fmt.Sprintf(
			"    /**\n     * Registers per-method auth metadata with the Authorizer.\n     * Called by ServiceManager after creating the Authorizer.\n     */\n    public static void registerMethodAuth(\n            %s.chassis.grpcserver.Authorizer authorizer) {\n%s\n    }",
			svc.Config.JavaPackageBase,
			strings.Join(methodAuthLines, "\n"))
	}

	tmpl, err := loadTemplate("servicewrapper.java.tmpl")
	if err != nil {
		return false, fmt.Errorf("failed to load service wrapper template: %w", err)
	}

	// Build the types import statement for the wrappers package
	// ServiceWrapper needs to import types like FooBarServiceGrpc, Validation, and outer class nested types
	var typesImport string
	if isStateService {
		// For state services, import the types package for the outer class and nested types
		if fullPackage != typesPackage {
			typesImport = fmt.Sprintf("import %s.*;\nimport %s.%s.*;", typesPackage, typesPackage, outerClassName)
		}
	} else if fullPackage != typesPackage {
		typesImport = fmt.Sprintf("import %s.*;\nimport %s.%s.*;", typesPackage, typesPackage, outerClassName)
	}
	// Import gRPC stubs package (contains {ServiceName}Grpc class)
	if grpcStubsPackage != "" {
		typesImport += fmt.Sprintf("\nimport %s.%sGrpc;", grpcStubsPackage, svc.ServiceName)
		// For state services, also import the watch types outer class (e.g., BaseballStateWatch)
		// which contains BaseballStateCommit and BaseballStateUpdate inner classes
		if isStateService {
			typesImport += fmt.Sprintf("\nimport %s.%s;", grpcStubsPackage, outerClassName)
		}
	}
	// Add cross-package type imports
	for _, importLine := range crossPkgImportLines {
		typesImport += "\n" + importLine
	}

	// Always import ProtoException from sys.errors package for error handling
	typesImport += fmt.Sprintf("\nimport %s.types.sys.errors.ProtoException;", svc.Config.JavaPackageBase)
	// Import Utils for grpcStatusToHttp (parity with Go's utils.GrpcStatusToHTTP)
	typesImport += fmt.Sprintf("\nimport %s.utils.Utils;", svc.Config.JavaPackageBase)
	// Import TokenWrapper for proxy chain check (used when any method has a user_token field)
	typesImport += fmt.Sprintf("\nimport %s.chassis.token.TokenWrapper;", svc.Config.JavaPackageBase)
	typesImport += fmt.Sprintf("\nimport %s.chassis.collection.CollectionRuntime;", svc.Config.JavaPackageBase)
	// The approval chassis is imported only by a wrapper that actually carries
	// the (spec.rpc).requires_device gate: every other wrapper generates
	// byte-identical to before.
	hasRequiresDevice := false
	for _, m := range targetService.Methods {
		if !m.IsMcp && m.HasDeviceGate() {
			hasRequiresDevice = true
			break
		}
	}
	if hasRequiresDevice {
		typesImport += fmt.Sprintf("\nimport %s.chassis.approval.Approval;", svc.Config.JavaPackageBase)
	}

	// Trace types import for TraceRound
	traceTypesImport := fmt.Sprintf("import %s.types.sys.trace.Trace;", svc.Config.JavaPackageBase)

	serverClassName := serverClassName(svc.ServiceName)

	serviceManagerImport := svc.Config.JavaPackageBase + ".chassis.servicemanager"

	data := struct {
		Header               string
		FullPackage          string
		TypesImport          string
		ServiceManagerImport string
		ProtoFile            string
		ServiceName          string
		ServerClassName      string
		ServiceLabel         string
		HandlerInterface     string
		WatchFields          string
		GRPCMethods          string
		HasAccounting        bool
		AccountingImports    string
		TraceTypesImport     string
		RegisterMethodAuth   string
		RequiresAuth         bool
		AuthorizerFQN        string
		HasRequiresDevice    bool
	}{
		Header:               header.GeneratedHeader(consts.LangJava, svc.ProtoFile),
		FullPackage:          fullPackage,
		TypesImport:          typesImport,
		ServiceManagerImport: serviceManagerImport,
		ProtoFile:            svc.ProtoFile,
		ServiceName:          serviceName,
		ServerClassName:      serverClassName,
		ServiceLabel:         svc.Config.ServiceLabel(),
		HandlerInterface:     handlerInterface.String(),
		WatchFields:          watchFields.String(),
		GRPCMethods:          grpcMethods.String(),
		HasAccounting:        hasAccounting,
		AccountingImports:    accountingImports,
		TraceTypesImport:     traceTypesImport,
		RegisterMethodAuth:   registerMethodAuth,
		// Token-auth services (S2S/C2S, not service-level auth_disabled) get a
		// mandatory Authorizer. c2d / auth_disabled services are exempt by annotation.
		RequiresAuth:  (svc.IsS2S || svc.IsC2S) && !svc.AuthDisabled,
		AuthorizerFQN: svc.Config.JavaPackageBase + ".chassis.grpcserver.Authorizer",
		// HasRequiresDevice emits the approval-check seam: the ApprovalCheck
		// interface, its setter and the fail-closed brokerApproval helper.
		HasRequiresDevice: hasRequiresDevice,
	}

	t, err := template.New("servicewrapper").Parse(tmpl)
	if err != nil {
		return false, fmt.Errorf("failed to parse template: %w", err)
	}

	file, err := os.Create(outputPath)
	if err != nil {
		return false, fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	if err := t.Execute(file, data); err != nil {
		return false, fmt.Errorf("failed to execute template: %w", err)
	}

	// The MCP termination lands in its own class beside the wrapper: same
	// package, so the carrier method the wrapper emits calls straight into it,
	// but its own imports (the MCP SDK, Reactor, the JSON-RPC codec) do not have
	// to be threaded through the wrapper template for the services that declare
	// no tool.
	if mcpCarrier != nil {
		tools, err := javaMcpTools(targetService, info.Package, outerClassName, crossPkgOuterClasses, crossPkgHelpers, san)
		if err != nil {
			return false, err
		}
		if len(tools) == 0 {
			// HasMcpRpcs is what produced the carrier, so this cannot happen; if
			// it ever does, a carrier with no tool set is a generation bug, not a
			// service that serves nothing.
			return false, fmt.Errorf("%s has a generated MCP session carrier but no RPC_PRINCIPAL_MCP rpc", serviceName)
		}
		mcpParams := javaMcpFileParams{
			JavaPackage:       fullPackage,
			ProtoFile:         svc.ProtoFile,
			FrameRequestType:  resolveJavaMethodType(mcpCarrier.FullInputType, mcpCarrier.InputType, info.Package, outerClassName, crossPkgOuterClasses),
			FrameResponseType: resolveJavaMethodType(mcpCarrier.FullOutputType, mcpCarrier.OutputType, info.Package, outerClassName, crossPkgOuterClasses),
			// The wrapper's own import block verbatim, so every proto type the
			// dispatch names resolves exactly as it does next door, plus the
			// Caller the surface is bound to and the SDK itself.
			Imports:             javaMcpImports(typesImport, serviceManagerImport),
			NonToolHandlerDecls: nonToolHandlerDecls,
		}
		if err := writeJavaMcpDispatchFile(targetService, tools, svc.OutputDir, mcpParams); err != nil {
			return false, err
		}
		if err := writeJavaMcpDispatchTestFile(targetService, tools, svc.OutputDir, mcpParams); err != nil {
			return false, err
		}
	}

	return hasAccounting, nil
}

// resolveValidationHelpers returns the Helpers class qualifier for a given type.
// For same-package types, returns "Helpers" (the local class).
// For cross-package types, returns the fully qualified Helpers class name
// (e.g., "com.gitlab.xxx.baseball.types.Helpers") since Java doesn't support import aliases.
func resolveValidationHelpers(fullType, servicePackage string, crossPkgHelpers map[string]string) string {
	// No validation for well-known google.protobuf types
	if strings.HasPrefix(fullType, "google.protobuf.") {
		return ""
	}
	parts := strings.Split(fullType, ".")
	if len(parts) > 1 {
		pkg := strings.Join(parts[:len(parts)-1], ".")
		if pkg != servicePackage && pkg != "" {
			if helpersClass, ok := crossPkgHelpers[pkg]; ok {
				return helpersClass
			}
		}
	}
	return "Helpers"
}

func javaPackageForProtoPackage(javaPackageBase, protoPackage, dir string) string {
	firstDirComponent := strings.Split(dir, string(filepath.Separator))[0]
	if firstDirComponent == "" {
		return fmt.Sprintf("%s.types.%s", javaPackageBase, protoPackage)
	}
	if strings.HasPrefix(protoPackage, firstDirComponent+".") {
		return fmt.Sprintf("%s.types.%s", javaPackageBase, protoPackage)
	}
	return fmt.Sprintf("%s.types.%s.%s", javaPackageBase, firstDirComponent, protoPackage)
}

// resolveJavaMethodType returns the fully qualified Java type name for a method's type.
// For same-package types, returns "OuterClass.TypeName".
// For cross-package types, returns "CrossPkgOuterClass.TypeName".
func resolveJavaMethodType(fullType, simpleType, servicePackage, outerClassName string, crossPkgOuterClasses map[string]string) string {
	// Handle well-known google.protobuf types (e.g., google.protobuf.Empty)
	if strings.HasPrefix(fullType, "google.protobuf.") {
		return "com." + fullType
	}
	parts := strings.Split(fullType, ".")
	if len(parts) > 1 {
		pkg := strings.Join(parts[:len(parts)-1], ".")
		if pkg != servicePackage && pkg != "" {
			if crossOuterClass, ok := crossPkgOuterClasses[pkg]; ok {
				return crossOuterClass + "." + simpleType
			}
		}
	}
	return outerClassName + "." + simpleType
}

func methodNameLowerCamel(name string) string {
	// Use protoc's naming convention: lowercase first letter only.
	// e.g. "ExecuteDQL" → "executeDQL" (not "executeDql").
	// A snake_case round-trip would mangle all-caps acronyms.
	methodName := name
	if len(methodName) > 0 {
		methodName = strings.ToLower(methodName[:1]) + methodName[1:]
	}
	return methodName
}

func simpleTypeName(fullType string) string {
	if i := strings.LastIndex(fullType, "."); i >= 0 {
		return fullType[i+1:]
	}
	return fullType
}

// javaWatchTypeRef resolves a collection item/change proto type (e.g.
// "agentsensor.AgentEvent") to its Java reference (e.g. "Agentsensor.AgentEvent").
// The item is usually in the same package as the response envelope, so we default
// to the envelope's resolved Java outer class (which carries any outer-class
// conflict resolution). When the item is CROSS-PACKAGE (e.g. a stratum RecordMap
// paging agentsensor.AgentEvent), we resolve the item's own Java outer class from
// crossPkgOuterClasses — the SAME map resolveJavaMethodType and the wrapper's
// import lines use, so a conflict-renamed outer class (tasks.proto declaring
// "service Tasks" -> TasksOuterClass) is referenced exactly as it is imported.
// Otherwise the item would be mis-qualified with the response's package
// (Stratum.AgentEvent), which does not exist.
func javaWatchTypeRef(fullType, outputType string, crossPkgOuterClasses map[string]string) string {
	simple := simpleTypeName(fullType)
	envPrefix := outputType
	if i := strings.LastIndex(envPrefix, "."); i >= 0 {
		envPrefix = envPrefix[:i]
	}
	if i := strings.LastIndex(fullType, "."); i >= 0 {
		itemPkg := fullType[:i]
		// EqualFold: the envelope prefix is the Java outer class (e.g. "Catalog")
		// while the item package is the proto package (e.g. "catalog").
		if !strings.EqualFold(itemPkg, envPrefix) {
			if crossOuterClass, ok := crossPkgOuterClasses[itemPkg]; ok {
				return crossOuterClass + "." + simple
			}
			return javaOuterFromProtoPkg(itemPkg) + "." + simple
		}
	}
	return envPrefix + "." + simple
}

// javaOuterFromProtoPkg derives the default Java outer class for a proto package
// (capitalize the first letter), matching tacplus's java_outer_classname
// convention. This is the last-resort fallback for a cross-package item whose
// package is absent from crossPkgOuterClasses; conflict-renamed (*OuterClass)
// packages are resolved through that map in javaWatchTypeRef.
func javaOuterFromProtoPkg(pkg string) string {
	if pkg == "" {
		return pkg
	}
	return strings.ToUpper(pkg[:1]) + pkg[1:]
}

// generateJavaHandlerMethod generates a single method signature for the Handler interface.
// Unary methods return the response directly. Streaming methods keep StreamObserver pattern.
// Empty request types are omitted — the gRPC contract requires them, but the handler doesn't.
// Empty output types return void — the wrapper constructs the Empty response.
func generateJavaHandlerMethod(method *parser.MethodInfo, inputType, outputType string, crossPkgOuterClasses map[string]string) string {
	// Use protoc's naming convention: lowercase first letter only.
	// e.g. "ExecuteDQL" → "executeDQL" (not "executeDql").
	// A snake_case round-trip would mangle all-caps acronyms. This name must
	// agree with the handler.<name>(...) calls the wrapper surface emits.
	methodName := method.Name
	if len(methodName) > 0 {
		methodName = strings.ToLower(methodName[:1]) + methodName[1:]
	}
	handlerEmpty := method.FullInputType == "google.protobuf.Empty" || method.FullInputType == "trace.TraceRequest"
	emptyOutput := method.FullOutputType == "google.protobuf.Empty"

	switch {
	case !method.ClientStreaming && !method.ServerStreaming && method.IsPageCollection():
		// PAGE — a unary, client-driven window. The wrapper owns the page_token
		// codec and the page_size bound; the handler is a keyset producer: given
		// PageParams (limit, after) plus request (the filter fields) it returns one
		// CollectionRuntime.Page (items + the cursor to resume after + a total).
		itemType := javaWatchTypeRef(method.Resolved.PageItemType, outputType, crossPkgOuterClasses)
		if handlerEmpty {
			return fmt.Sprintf("        CollectionRuntime.Page<%s> %s(Caller caller, CollectionRuntime.PageParams page) throws ProtoException;\n",
				itemType, methodName)
		}
		return fmt.Sprintf("        CollectionRuntime.Page<%s> %s(Caller caller, %s request, CollectionRuntime.PageParams page) throws ProtoException;\n",
			itemType, methodName, inputType)
	case !method.ClientStreaming && !method.ServerStreaming:
		// Unary — return void for Empty output, value directly otherwise
		returnType := outputType
		if emptyOutput {
			returnType = "void"
		}
		if handlerEmpty {
			return fmt.Sprintf("        %s %s(Caller caller) throws ProtoException;\n",
				returnType, methodName)
		}
		return fmt.Sprintf("        %s %s(Caller caller, %s request) throws ProtoException;\n",
			returnType, methodName, inputType)
	case !method.ClientStreaming && method.ServerStreaming && method.IsTier2SnapshotCollection():
		// Tier-2 SNAPSHOT collection — handler returns a materialized List; the
		// wrapper chunks it into the server-stream with flow control.
		if handlerEmpty {
			return fmt.Sprintf("        java.util.List<%s> %s(Caller caller) throws ProtoException;\n",
				outputType, methodName)
		}
		return fmt.Sprintf("        java.util.List<%s> %s(Caller caller, %s request) throws ProtoException;\n",
			outputType, methodName, inputType)
	case !method.ClientStreaming && method.ServerStreaming && method.IsTier3SnapshotCollection():
		// Tier-3 SNAPSHOT cursor — handler returns a lazy Iterator producer; the
		// wrapper drives it with flow control and closes it (if AutoCloseable) on
		// teardown. A lazy producer (not a materialized List) is the cursor seam.
		if handlerEmpty {
			return fmt.Sprintf("        java.util.Iterator<%s> %s(Caller caller) throws ProtoException;\n",
				outputType, methodName)
		}
		return fmt.Sprintf("        java.util.Iterator<%s> %s(Caller caller, %s request) throws ProtoException;\n",
			outputType, methodName, inputType)
	case !method.ClientStreaming && method.ServerStreaming && method.IsWatchCollection():
		itemType := javaWatchTypeRef(method.Resolved.WatchItemType, outputType, crossPkgOuterClasses)
		changeType := javaWatchTypeRef(method.Resolved.WatchChangeType, outputType, crossPkgOuterClasses)
		if handlerEmpty {
			return fmt.Sprintf("        CollectionRuntime.WatchSource<%s, %s> %s(Caller caller) throws ProtoException;\n",
				itemType, changeType, methodName)
		}
		return fmt.Sprintf("        CollectionRuntime.WatchSource<%s, %s> %s(Caller caller, %s request) throws ProtoException;\n",
			itemType, changeType, methodName, inputType)
	case !method.ClientStreaming && method.ServerStreaming:
		// Server streaming — keep StreamObserver
		if handlerEmpty {
			return fmt.Sprintf("        void %s(Caller caller, StreamObserver<%s> responseObserver) throws ProtoException;\n",
				methodName, outputType)
		}
		return fmt.Sprintf("        void %s(Caller caller, %s request, StreamObserver<%s> responseObserver) throws ProtoException;\n",
			methodName, inputType, outputType)
	case method.ClientStreaming && !method.ServerStreaming:
		// Client streaming — keep StreamObserver
		return fmt.Sprintf("        StreamObserver<%s> %s(Caller caller, StreamObserver<%s> responseObserver) throws ProtoException;\n",
			inputType, methodName, outputType)
	case method.ClientStreaming && method.ServerStreaming:
		// Bidirectional streaming — keep StreamObserver
		return fmt.Sprintf("        StreamObserver<%s> %s(Caller caller, StreamObserver<%s> responseObserver) throws ProtoException;\n",
			inputType, methodName, outputType)
	}
	return ""
}

// generateJavaGRPCWrapper generates a gRPC method override with validation for the given method.
// Handles all four streaming combinations: unary, server streaming, client streaming, bidirectional.
// inputType and outputType are pre-resolved qualified Java type names (e.g., "Inventory.GetProductRequest").
// inputHelpers and outputHelpers are the Helpers class qualifiers for validation calls
// (e.g., "Helpers" for local types, "com.gitlab.xxx.baseball.types.Helpers" for cross-package types).
// grpcName is the service's grpc_config name (e.g. "sentinel") used to construct the AccountingMethod.
// When grpcName is empty, accounting is skipped.
// javaTypeHasTraceFields returns true if a proto type is user-defined and thus has
// the injected trace_id/noop fields.
func javaTypeHasTraceFields(fullType, servicePackage, parentDir string, crossPkgTypes map[string]string, skippedTraceTypes proto.SkippedTraceTypes) bool {
	if fullType == "trace.TraceRequest" {
		return true
	}
	if fullType == "" || fullType == "google.protobuf.Empty" {
		return false
	}
	// Types that were skipped during trace injection (they define their own
	// trace_id or noop at non-standard field numbers) don't have the injected
	// trace_id/noop pair — don't emit trace handling code for them.
	if skippedTraceTypes[fullType] {
		return false
	}
	parts := strings.Split(fullType, ".")
	if len(parts) <= 1 {
		return !strings.HasPrefix(parentDir, "sys")
	}
	pkg := strings.Join(parts[:len(parts)-1], ".")
	if pkg == servicePackage {
		return !strings.HasPrefix(parentDir, "sys")
	}
	if pkg == "google.protobuf" {
		return false
	}
	if dir, ok := crossPkgTypes[pkg]; ok {
		return !strings.HasPrefix(dir, "sys")
	}
	return !strings.HasPrefix(parentDir, "sys")
}

// minGroupSizeField resolves WHICH field of a size-only response carries the
// size the (spec.record).min_group_size guard compares. The record-map injector
// stamps the method option onto RPCs returning RecordCount (count) and
// QueryStats (size); rather than pin those two names in the generator, the field
// is read off the response message itself — the one scalar integer it carries.
//
// "" when the type is unknown or carries no single integer field.
// generateServiceWrapper turns that into a generation error rather than
// emitting an unguarded size-only RPC.
//
// The CHOICE of field is parser.MinGroupSizeField, shared with the Go and Rust
// wrappers: three languages guarding three different fields of the same
// response would be three guards, not one.
func (es *javaEgressSanitizer) minGroupSizeField(fullType, servicePackage string) string {
	if es == nil || fullType == "" {
		return ""
	}
	pkg, name := splitJavaEgressFQN(fullType, servicePackage)
	return parser.MinGroupSizeField(es.msgs[pkg+"\x00"+name])
}

// javaMinGroupSizeGuard renders the (spec.record).min_group_size refusal over a
// size-only response. valueExpr names the response in scope; indent is the
// leading whitespace of the block it is spliced into. Empty when the method
// carries no threshold — the annotation is additive, so an undeclared record's
// RPCs are emitted exactly as before.
//
// The refusal is PERMISSION_DENIED and carries no count: naming the actual
// number, or any bound tighter than the declared threshold, would disclose
// exactly what the guard exists to withhold. The threshold itself is in the
// proto and is safe to name.
func javaMinGroupSizeGuard(method *parser.MethodInfo, sizeField, valueExpr, indent string) string {
	if method.MinGroupSize <= 0 || sizeField == "" {
		return ""
	}
	n := method.MinGroupSize
	return fmt.Sprintf(`%[1]s// (spec.record).min_group_size: a response that carries only a size, over
%[1]s// fewer than %[2]d records, is a membership disclosure by arithmetic. The
%[1]s// wrapper, never the handler, refuses.
%[1]sif (%[3]s.get%[4]s() < %[2]dL) {
%[1]s    throw new ProtoException(ProtoException.PERMISSION_DENIED, "min_group_size: a count over fewer than %[2]d records is a membership disclosure; widen the query's filter");
%[1]s}
`, indent, n, valueExpr, javaPascalField(sizeField))
}

// javaMinGroupSizeUnenforceable reports why the guard cannot be generated for a
// method that declares a threshold, or "" when it can. A size-only RPC that the
// wrapper cannot guard must fail generation rather than ship unguarded — the
// same fail-closed stance (spec.store_principal) already takes for the shapes
// its egress filter cannot reach.
func javaMinGroupSizeUnenforceable(method *parser.MethodInfo, san *javaEgressSanitizer, servicePackage string) string {
	if method.MinGroupSize <= 0 {
		return ""
	}
	if method.ClientStreaming {
		return "the guard is generated on unary responses and server streams only"
	}
	if san.minGroupSizeField(method.FullOutputType, servicePackage) == "" {
		return fmt.Sprintf("response %s carries no single integer size field for the guard to compare", method.FullOutputType)
	}
	return ""
}

// generateJavaGRPCWrapper renders a wrapper for a surface that is NOT c2s. Kept
// as the plain spelling because most callers (and every render test) are asking
// about a shape the (spec.store_principal) row filter has nothing to say about.
func generateJavaGRPCWrapper(method *parser.MethodInfo, serviceName, grpcName, inputType, outputType, inputHelpers, outputHelpers string, authAnyUser bool, authDisabled bool, accountingMap map[string]service.AccountingInfo, servicePackage, parentDir string, crossPkgTypes map[string]string, skippedTraceTypes proto.SkippedTraceTypes, san *javaEgressSanitizer, crossPkgHelpers map[string]string, crossPkgOuterClasses map[string]string) string {
	return generateJavaGRPCWrapperSurface(method, serviceName, grpcName, inputType, outputType, inputHelpers, outputHelpers, authAnyUser, authDisabled, accountingMap, servicePackage, parentDir, crossPkgTypes, skippedTraceTypes, san, crossPkgHelpers, crossPkgOuterClasses, false, false)
}

// generateJavaGRPCWrapperSurface is the wrapper renderer. isC2S is what decides
// whether the (spec.store_principal) egress row filter is generated: the filter
// narrows rows to the authenticated USER, which is a statement only the c2s
// surface can make (see recordprincipal_gen.go).
//
// recordPlaneS2S is the ONE way the (spec.store_namespace) seams reach an s2s
// surface: the peer half of a record query plane, where the same four seams are
// emitted but decide at RUNTIME, per caller, whether to pin — /system peers
// fleet-wide, /tenants/<t> peers pinned to themselves. It never affects
// (spec.store_principal), which stays c2s-only: a peer service is not a user.
// See recordnamespace_gen.go.
func generateJavaGRPCWrapperSurface(method *parser.MethodInfo, serviceName, grpcName, inputType, outputType, inputHelpers, outputHelpers string, authAnyUser bool, authDisabled bool, accountingMap map[string]service.AccountingInfo, servicePackage, parentDir string, crossPkgTypes map[string]string, skippedTraceTypes proto.SkippedTraceTypes, san *javaEgressSanitizer, crossPkgHelpers map[string]string, crossPkgOuterClasses map[string]string, isC2S bool, recordPlaneS2S bool) string {
	// Use protoc's naming convention: lowercase first letter only.
	// e.g. "ExecuteDQL" → "executeDQL" (not "executeDql").
	// A snake_case round-trip would mangle all-caps acronyms — and this name is
	// what the @Override'd methods of grpc-java's generated XxxImplBase are
	// spelled with, so it must match protoc-gen-grpc-java exactly or the
	// override does not compile. It also spells the handler.<name>(...) calls,
	// which generateJavaHandlerMethod declares.
	methodName := method.Name
	if len(methodName) > 0 {
		methodName = strings.ToLower(methodName[:1]) + methodName[1:]
	}
	emptyInput := method.FullInputType == "google.protobuf.Empty"
	handlerEmpty := emptyInput || method.FullInputType == "trace.TraceRequest"

	// Full method name for logging/tracing/accounting (e.g. "SnoopyService/GetAtomic")
	fullMethod := fmt.Sprintf("%s/%s", serviceName, method.Name)
	// The name the Authorizer derives the required grant symbol from. The Authorizer
	// falls back to its OWN service name for the grant's package when it is handed an
	// unqualified name, which is right for a service the process owns and wrong for a
	// vservice: RecordQuery on the `query` trait is entitled by QUERY_RECORD_QUERY_*,
	// not by the host's STRATUM_* vocabulary. Qualify the hosted surface's name so the
	// trait's package, not the host's, is what the symbol is derived from.
	authMethod := javaMethodAuthKey(servicePackage, serviceName, method.Name, parentDir)
	// On auth_any_user methods target_namespace_field is an authored fixed
	// namespace literal, not a request field name. Unset means no namespace check
	// at all — "any authenticated user can call this RPC" — so the gate is the
	// no-arg checkAuthenticate(). A "/" default would gate on
	// isAtOrBelow(callerNamespace, "/"), true only for a caller sitting exactly at
	// "/", denying every real caller.
	authAnyUserCall := "checkAuthenticate()"
	if target := strings.TrimSpace(method.TargetNamespaceField); target != "" {
		authAnyUserCall = fmt.Sprintf("checkAuthenticate(%q)", target)
	}
	authAnyUserComment := "// Check authentication (auth_any_user: any authenticated caller)"
	if strings.TrimSpace(method.TargetNamespaceField) != "" {
		authAnyUserComment = "// Check authentication (auth_any_user: fixed namespace target)"
	}

	// Authorization check (before validation). auth_disabled methods emit NO check —
	// they are reachable without a token by proto annotation (e.g. they verify a
	// cred_pair in the body). Every other method always enforces.
	var authCheck string
	if authDisabled {
		// Nothing verifies a caller here, so accounting records carry no identity.
		authCheck = `        java.util.Map<String, String> callerIdentity = java.util.Collections.emptyMap();`
	} else if authAnyUser {
		authCheck = fmt.Sprintf(`        %s
        java.util.Map<String, String> callerIdentity;
        try {
            callerIdentity = %s;
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
            return;
        }`, authAnyUserComment, authAnyUserCall)
	} else {
		authCheck = fmt.Sprintf(`        // Check authorization
        java.util.Map<String, String> callerIdentity;
        try {
            callerIdentity = checkAuth("%s", request);
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
            return;
        }`, authMethod)
	}

	emptyOutput := method.FullOutputType == "google.protobuf.Empty"

	// Build the AccountingMethod construction expression when accounting is enabled.
	// Skip methods not in the accounting enum (e.g., synthesized state service RPCs).
	hasAccounting := false
	if grpcName != "" {
		if acctInfo, ok := accountingMap[grpcName]; ok {
			hasAccounting = acctInfo.RPCs[method.Name]
		}
	}
	var accountingMethodExpr string
	if hasAccounting {
		accountingField := strutil.ToPascalCase(grpcName)
		enumTypeName := accountingField + "RPCs"
		enumValueName := strings.ToUpper(strutil.ToSnakeCase(accountingField)) + "_RPCS_" + strings.ToUpper(strutil.ToSnakeCase(method.Name))
		accountingMethodExpr = fmt.Sprintf("AccountingMethod.newBuilder().set%s(Services.%s.%s).build()",
			accountingField, enumTypeName, enumValueName)
	}

	// Trace handling for unary methods with user-defined request/response types.
	inputHasTrace := javaTypeHasTraceFields(method.FullInputType, servicePackage, parentDir, crossPkgTypes, skippedTraceTypes)

	// Build trace-aware auth check for Java
	var traceAuthCheck string
	var traceAppend string
	var traceResponseAppend string
	var traceResponseErrorAppend string

	if inputHasTrace && !emptyInput {
		if authDisabled {
			// auth_disabled: no auth check, but the method body still needs the
			// isTracing/isNoop declarations the trace block normally provides.
			traceAuthCheck = `            boolean isTracing = !request.getTraceId().isEmpty();
            boolean isNoop = isTracing && request.getNoop();
            java.util.Map<String, String> callerIdentity = java.util.Collections.emptyMap();`
		} else if authAnyUser {
			// Auth runs unconditionally: noop skips the handler but not auth.
			traceAuthCheck = fmt.Sprintf(`            boolean isTracing = !request.getTraceId().isEmpty();
            boolean isNoop = isTracing && request.getNoop();
            %s
            java.util.Map<String, String> callerIdentity;
            try {
                callerIdentity = %s;
            } catch (ProtoException e) {
                responseObserver.onError(toGrpcError(e));
                return;
            }`, authAnyUserComment, authAnyUserCall)
		} else {
			// Auth runs unconditionally: noop skips the handler but not auth.
			traceAuthCheck = fmt.Sprintf(`            boolean isTracing = !request.getTraceId().isEmpty();
            boolean isNoop = isTracing && request.getNoop();
            // Check authorization
            java.util.Map<String, String> callerIdentity;
            try {
                callerIdentity = checkAuth("%s", request);
            } catch (ProtoException e) {
                responseObserver.onError(toGrpcError(e));
                return;
            }`, authMethod)
		}

		// Trace: publish trace event, noop returns default response.
		// Use typed AccountingMethod rpc field when accounting is available.
		var traceRpcCall string
		if accountingMethodExpr != "" {
			traceRpcCall = fmt.Sprintf(".setRpc(%s)\n                        ", accountingMethodExpr)
		}
		if !emptyOutput {
			traceAppend = fmt.Sprintf(`
            // Trace: publish event, noop skips handler
            if (isTracing) {
                Trace.TraceRound round = traceRound();
                publishTrace(Trace.TraceEvent.newBuilder()
                        .setTraceId(request.getTraceId())
                        .setRound(round)
                        %s.setKind(Trace.TraceEventKind.TRACE_EVENT_KIND_INTERNAL_REQUEST)
                        .build());
                if (isNoop) {
                    responseObserver.onNext(%s.getDefaultInstance());
                    responseObserver.onCompleted();
                    return;
                }
            }`, traceRpcCall, outputType)
		} else {
			traceAppend = fmt.Sprintf(`
            // Trace: publish event, noop skips handler
            if (isTracing) {
                Trace.TraceRound round = traceRound();
                publishTrace(Trace.TraceEvent.newBuilder()
                        .setTraceId(request.getTraceId())
                        .setRound(round)
                        %s.setKind(Trace.TraceEventKind.TRACE_EVENT_KIND_INTERNAL_REQUEST)
                        .build());
                if (isNoop) {
                    responseObserver.onNext(com.google.protobuf.Empty.getDefaultInstance());
                    responseObserver.onCompleted();
                    return;
                }
            }`, traceRpcCall)
		}
		// Trace response: publish INTERNAL_RESPONSE after handler completes.
		traceResponseAppend = fmt.Sprintf(`
            if (isTracing) {
                publishTrace(Trace.TraceEvent.newBuilder()
                        .setTraceId(request.getTraceId())
                        .setRound(traceRound())
                        %s.setKind(Trace.TraceEventKind.TRACE_EVENT_KIND_INTERNAL_RESPONSE)
                        .build());
            }`, traceRpcCall)
		traceResponseErrorAppend = fmt.Sprintf(`
                if (isTracing) {
                    publishTrace(Trace.TraceEvent.newBuilder()
                            .setTraceId(request.getTraceId())
                            .setRound(traceRound())
                            %s.setKind(Trace.TraceEventKind.TRACE_EVENT_KIND_INTERNAL_RESPONSE)
                            .setError(handlerEx.getMessage() == null ? handlerEx.toString() : handlerEx.getMessage())
                            .build());
                }`, traceRpcCall)
	}

	// Use trace-aware auth when input has trace fields
	effectiveAuthCheck := authCheck
	if inputHasTrace && !emptyInput {
		effectiveAuthCheck = traceAuthCheck
	}

	streamInputValidation := ""
	if inputHelpers != "" && !handlerEmpty {
		streamInputValidation = fmt.Sprintf(`
            // Validate request before invoking the handler.
            %s.validate%s(request);
`, inputHelpers, method.InputType)
	}
	// (spec.principal)/(spec.namespace) on the request of a server-streaming
	// shape. The request is still a single message on Tier-2, Tier-3, WATCH and
	// plain server-streaming, so this is the unary check verbatim; only the
	// seam differs.
	if !handlerEmpty {
		streamInputValidation += javaRequestScopePins(san, method.FullInputType, servicePackage, "request", method.Name, "            ")
	}
	streamResponseObserver := "responseObserver"
	if outputHelpers != "" && !emptyOutput {
		streamResponseObserver = fmt.Sprintf("validatingResponseObserver(responseObserver, value -> %s.validate%s(value))", outputHelpers, method.OutputType)
	}
	// (spec.record).min_group_size on a streamed size (Subscribe<T>Count): the
	// same arithmetic as the unary count, delivered repeatedly. The guard is a
	// delegate in front of the validating one, so a below-threshold frame
	// terminates the stream with PERMISSION_DENIED rather than being withheld —
	// a stream that goes quiet at exactly N-1 discloses the crossing by silence.
	if g := javaMinGroupSizeGuard(method, san.minGroupSizeField(method.FullOutputType, servicePackage), "value", "                "); g != "" {
		streamResponseObserver = fmt.Sprintf("guardedResponseObserver(%s, value -> {\n%s            })", streamResponseObserver, g)
	}
	needsStreamMessageAuth := method.ClientStreaming && method.TargetNamespaceField != "" && !authDisabled && !authAnyUser
	clientStreamInputObserver := func(handlerObserverExpr, responseObserverExpr string) string {
		// (spec.principal)/(spec.namespace) on a request STREAM: per message,
		// not once at stream open. Wrapped CLOSEST to the handler, so the
		// message the handler receives is the stamped one and the per-message
		// auth + validation above still see the message as the client wrote it.
		if !handlerEmpty {
			if g := javaRequestScopePins(san, method.FullInputType, servicePackage, "__scoped", method.Name, "                    "); g != "" {
				handlerObserverExpr = fmt.Sprintf("scopingRequestObserver(%s, value -> {\n                %s __scoped = value;\n%s                return __scoped;\n            }, %s)",
					handlerObserverExpr, inputType, g, responseObserverExpr)
			}
		}
		if !needsStreamMessageAuth && (inputHelpers == "" || handlerEmpty) {
			return handlerObserverExpr
		}
		var checks []string
		if needsStreamMessageAuth {
			checks = append(checks, fmt.Sprintf("checkAuth(%q, value);", authMethod))
		}
		if inputHelpers != "" && !handlerEmpty {
			checks = append(checks, fmt.Sprintf("%s.validate%s(value);", inputHelpers, method.InputType))
		}
		return fmt.Sprintf("validatingRequestObserver(%s, value -> {\n                %s\n            }, %s)", handlerObserverExpr, strings.Join(checks, "\n                "), responseObserverExpr)
	}

	switch {
	case !method.ClientStreaming && !method.ServerStreaming && method.IsPageCollection():
		// PAGE: unary on the wire. The wrapper owns the page_token codec and the
		// trust-tier page_size bound; the handler is a keyset producer returning a
		// CollectionRuntime.Page. A malformed page_token throws INVALID_ARGUMENT
		// (decodePageToken), never a silent reset to page 1.
		itemType := javaWatchTypeRef(method.Resolved.PageItemType, outputType, crossPkgOuterClasses)
		itemsPascal := strutil.ToPascalCase(method.Resolved.PageItemsField)
		pageInputValidation := ""
		if inputHelpers != "" {
			pageInputValidation = fmt.Sprintf(`
            // Validate request before decoding the page token or invoking the handler.
            %s.validate%s(request);`, inputHelpers, method.InputType)
		}
		// (spec.principal)/(spec.namespace): stamp or refuse before the page
		// token is decoded, so the window the handler is asked for is already
		// the caller's own.
		if g := javaRequestScopePins(san, method.FullInputType, servicePackage, "request", method.Name, "            "); g != "" {
			pageInputValidation += "\n" + g
		}
		pageOutputValidation := ""
		if outputHelpers != "" {
			pageOutputValidation = fmt.Sprintf(`
            try {
                %s.validate%s(response);
            } catch (ProtoException validationEx) {
                throw responseValidationError(validationEx);
            }`, outputHelpers, method.OutputType)
		}
		totalBlock := ""
		if method.Resolved.PageHasTotal {
			totalBlock = `
            if (__page.total >= 0) {
                __builder.setTotalCount(__page.total);
            }`
		}
		// Egress data entitlement: sanitize every page item after the handler
		// returns, before the response is assembled/validated.
		// (spec.store_principal) runs FIRST, before the entitlement sanitize:
		// a row the caller does not own must not reach the sanitizer at all, and
		// filtering here (not after the page is assembled) is what keeps
		// next_page_token and total_count describing the rows actually shown.
		pagePrincipal := ""
		pagePrincipalPrivate := false
		pageSrcExpr := "__page.items"
		if isC2S {
			pagePrincipal = san.principalField(method.Resolved.PageItemType, servicePackage)
			pagePrincipalPrivate = san.isRecordPrivate(method.Resolved.PageItemType, servicePackage)
		}
		pageFilter := javaPrincipalPageFilter(pagePrincipal, itemType, method.Name, "            ", pagePrincipalPrivate)
		if pageFilter != "" {
			pageSrcExpr = "__ownedItems"
		}
		// (spec.store_namespace) runs after the principal filter and over its
		// output: the tenant boundary is checked on exactly the rows that would
		// otherwise be assembled into the page.
		if isC2S || recordPlaneS2S {
			if nsFilter := javaNamespacePageFilter(san.namespaceField(method.Resolved.PageItemType, servicePackage),
				itemType, pageSrcExpr, method.Name, "            ", recordPlaneS2S); nsFilter != "" {
				pageFilter += nsFilter
				pageSrcExpr = "__tenantItems"
			}
		}
		pageSan := ""
		pageItemsExpr := pageSrcExpr
		if t, ok := san.sanitizableType(method.Resolved.PageItemType, servicePackage, crossPkgHelpers); ok {
			pageSan = javaEgressSanitizePreamble([]javaSanTarget{t}) + fmt.Sprintf(`            java.util.List<%s> __sanitizedItems = new java.util.ArrayList<>(%s.size());
            for (%s __m : %s) { __sanitizedItems.add(%s); }
`, itemType, pageSrcExpr, itemType, pageSrcExpr, t.sanitizeExpr("__m"))
			pageItemsExpr = "__sanitizedItems"
		}
		pageSan = pageFilter + pageSan
		// Build the page body: decode -> bound -> call handler -> assemble response.
		pageBody := fmt.Sprintf(`            CollectionRuntime.Cursor __after = CollectionRuntime.decodePageToken(request.getPageToken());
            long __limit = CollectionRuntime.effectivePageSize(request.getPageSize(), %dL, %dL);
            CollectionRuntime.Page<%s> __page = handler.%s(caller, request, new CollectionRuntime.PageParams(__limit, __after));
%s            %s.Builder __builder = %s.newBuilder();
            __builder.addAll%s(%s);
            __builder.setNextPageToken(CollectionRuntime.encodePageToken(__page.nextAfter));%s
            response = __builder.build();%s`,
			method.Resolved.DefaultPageSize, method.Resolved.MaxPageSize, itemType, methodName,
			pageSan, outputType, outputType, itemsPascal, pageItemsExpr, totalBlock, pageOutputValidation)

		if hasAccounting {
			return fmt.Sprintf(`    @Override
    public void %s(%s request, StreamObserver<%s> responseObserver) {
        try {
%s
%s%s
            // Extract caller identity and request ID
            String requestId = Auth.requestIdFromContext();
            Caller caller = Auth.callerFromContext();
            logger.debug("rpc method={} request_id={}", "%s", requestId);
            long startTime = System.currentTimeMillis();
            %s response;
            try {
%s
            } catch (Exception handlerEx) {
                long durationMs = System.currentTimeMillis() - startTime;
                AccountingManager.Emitter em = this.emitter;
                if (em != null) {
                    AccountingMethod accountingMethod = %s;
                    io.grpc.Status.Code code = AccountingManager.grpcCodeFromError(handlerEx);
                    em.rpcError(accountingMethod, code.value(), handlerEx.getMessage(), durationMs,
                        AccountingManager.errorContextFromError(handlerEx, AccountingManager.shouldCaptureStack(code)),
                        callerIdentity, requestId);
                }
                throw handlerEx;
            }
            long durationMs = System.currentTimeMillis() - startTime;
            AccountingManager.Emitter em = this.emitter;
            if (em != null) {
                AccountingMethod accountingMethod = %s;
                em.rpcCompleted(accountingMethod, durationMs, callerIdentity, requestId);
            }
%s
            responseObserver.onNext(response);
            responseObserver.onCompleted();
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
        } catch (Exception e) {
            responseObserver.onError(toGrpcError(e));
        }
    }
`, methodName, inputType, outputType,
				effectiveAuthCheck,
				pageInputValidation, traceAppend,
				fullMethod,
				outputType,
				pageBody,
				accountingMethodExpr,
				accountingMethodExpr,
				traceResponseAppend)
		}

		return fmt.Sprintf(`    @Override
    public void %s(%s request, StreamObserver<%s> responseObserver) {
        try {
%s
%s%s
            // Extract caller identity and request ID
            String requestId = Auth.requestIdFromContext();
            Caller caller = Auth.callerFromContext();
            logger.debug("rpc method={} request_id={}", "%s", requestId);
            %s response;
%s%s
            responseObserver.onNext(response);
            responseObserver.onCompleted();
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
        } catch (Exception e) {
            responseObserver.onError(toGrpcError(e));
        }
    }
`, methodName, inputType, outputType,
			effectiveAuthCheck,
			pageInputValidation, traceAppend,
			fullMethod,
			outputType,
			pageBody,
			traceResponseAppend)

	case !method.ClientStreaming && !method.ServerStreaming:
		// Unary — authorize, validate request, call handler, validate response
		var inputValidation, outputValidation string
		if inputHelpers != "" && !handlerEmpty {
			inputValidation = fmt.Sprintf(`
            // Validate request before invoking the handler.
            %s.validate%s(request);`, inputHelpers, method.InputType)
		}
		// (spec.store_epoch): stamp or refuse before the handler sees the
		// request. Runs after validation — a request that is not well-formed is
		// rejected on its own terms before the store writes its clock onto it.
		if !handlerEmpty {
			if g := javaStoreEpochStamp(san.storeEpochField(method.FullInputType, servicePackage), "request", "            "); g != "" {
				inputValidation += "\n" + g
			}
		}
		// (spec.store_principal) on the query predicate: pin it to the caller
		// before the handler registers the query. Same seam, same doctrine, and
		// the c2s gate is the same one the egress row filter runs under.
		if (isC2S || recordPlaneS2S) && !handlerEmpty {
			// (spec.store_principal) stays c2s-only even on the tenant-owned
			// s2s plane: a peer service is not a user, and there is no user to
			// narrow to.
			if isC2S {
				rf, pf := san.predicatePrincipalField(method.FullInputType, servicePackage)
				if g := javaPredicatePrincipalPin(rf, pf, "request", method.Name, "            "); g != "" {
					inputValidation += "\n" + g
				}
			}
			// (spec.store_namespace) on the same predicate, pinned on the same
			// seam. It exists because the tenant field became an EQ criterion when
			// (spec.record).c2s_read started requiring one: a criterion a caller
			// could set and the egress filter would then ABORT over is a
			// client-triggerable process halt, so the pin lands with it.
			nrf, nf := san.predicateNamespaceField(method.FullInputType, servicePackage)
			if g := javaPredicateNamespacePin(nrf, nf, "request", method.Name, "            ", recordPlaneS2S); g != "" {
				inputValidation += "\n" + g
			}
		}
		// (spec.principal)/(spec.namespace) on the request itself. Unlike the
		// two pins above this is not gated on the record plane or on c2s: the
		// marker is a statement about the REQUEST, and its contract in
		// spec.proto is unqualified.
		if !handlerEmpty {
			if g := javaRequestScopePins(san, method.FullInputType, servicePackage, "request", method.Name, "            "); g != "" {
				inputValidation += "\n" + g
			}
			// (spec.owner_default): refuse the access the row's owner does not
			// hold, after the stamp above because the comparison needs the bound
			// principal. Every shape that cannot host this check was refused at
			// generation (javaOwnerDefaultUnhostable), so the error is dead here.
			if g, err := javaOwnerDefaultGuard(method, san, servicePackage, javaOwnerDefaultGrpcSeam("            "), "            "); err == nil && g != "" {
				inputValidation += "\n" + g
			}
		}

		if outputHelpers != "" && !emptyOutput {
			outputValidation = fmt.Sprintf(`
            // Validate response before returning it to gRPC.
            try {
                %s.validate%s(response);
            } catch (ProtoException validationEx) {
                throw responseValidationError(validationEx);
            }`, outputHelpers, method.OutputType)
		}

		// Egress data entitlement for unary responses (e.g. RecordMap Sample<T>):
		// a sanitizable response type is sanitized whole; an injected envelope
		// (Sample<T>Response) has each record-bearing field sanitized instead.
		// Runs after the handler returns, before response validation.
		if !emptyOutput {
			var unarySan string
			if t, ok := san.sanitizableType(method.FullOutputType, servicePackage, crossPkgHelpers); ok {
				unarySan = javaEgressSanitizePreamble([]javaSanTarget{t}) +
					fmt.Sprintf("            response = %s;\n", t.sanitizeExpr("response"))
			} else if fs := san.envelopeSanFields(method.FullOutputType, servicePackage, crossPkgHelpers); len(fs) > 0 {
				var targets []javaSanTarget
				for _, f := range fs {
					targets = append(targets, f.Target)
				}
				var stmts strings.Builder
				fmt.Fprintf(&stmts, "            %s.Builder __rb = response.toBuilder();\n", outputType)
				for _, ef := range fs {
					p := javaPascalField(ef.Field.Name)
					if ef.Field.IsRepeated {
						fmt.Fprintf(&stmts, "            for (int __i = 0; __i < __rb.get%sCount(); __i++) {\n", p)
						fmt.Fprintf(&stmts, "                __rb.set%s(__i, %s);\n            }\n", p, ef.Target.sanitizeExpr("__rb.get"+p+"(__i)"))
						continue
					}
					fmt.Fprintf(&stmts, "            if (__rb.has%s()) { __rb.set%s(%s); }\n", p, p, ef.Target.sanitizeExpr("__rb.get"+p+"()"))
				}
				stmts.WriteString("            response = __rb.build();\n")
				unarySan = javaEgressSanitizePreamble(targets) + stmts.String()
			}
			// (spec.store_principal): rows the caller does not own never leave
			// the process. Ahead of the sanitize for the same reason as the page
			// leg — an unowned row must not reach the sanitizer at all.
			unaryPrincipal := ""
			if isC2S {
				var lf []javaPrincipalListField
				for _, f := range san.principalListFields(method.FullOutputType, servicePackage) {
					if f.ItemField != "" {
						lf = append(lf, f)
					}
				}
				unaryPrincipal = javaPrincipalUnaryFilter(lf, outputType, method.Name, "            ")
			}
			// (spec.store_namespace): rows of another tenant never leave the
			// process, and having found one aborts. After the principal filter
			// for the same reason as the page leg.
			unaryNamespace := ""
			if isC2S || recordPlaneS2S {
				unaryNamespace = javaNamespaceUnaryFilter(san.namespaceListFields(method.FullOutputType, servicePackage),
					outputType, method.Name, "            ", recordPlaneS2S)
			}
			// (spec.record).min_group_size runs first: the refusal is about the
			// number itself, so it must precede any transformation of the response.
			outputValidation = javaMinGroupSizeGuard(method, san.minGroupSizeField(method.FullOutputType, servicePackage), "response", "            ") +
				unaryPrincipal + unaryNamespace + unarySan + outputValidation
		}

		handlerCall := fmt.Sprintf("handler.%s(caller, request)", methodName)
		if handlerEmpty {
			handlerCall = fmt.Sprintf("handler.%s(caller)", methodName)
		}

		// (spec.rpc).requires_device sits between caller extraction and the
		// handler call, so it is spliced in right after the debug line every
		// unary shape shares.
		deviceGate := javaRequiresDeviceGate(method, servicePackage, serviceName)

		// When output is Empty, handler returns void — wrapper constructs the Empty response
		if emptyOutput {
			if hasAccounting {
				return fmt.Sprintf(`    @Override
    public void %s(%s request, StreamObserver<%s> responseObserver) {
        try {
%s
%s%s
            // Extract caller identity and request ID
            String requestId = Auth.requestIdFromContext();
            Caller caller = Auth.callerFromContext();
            logger.debug("rpc method={} request_id={}", "%s", requestId);%s
            long startTime = System.currentTimeMillis();
            try {
                %s;
            } catch (Exception handlerEx) {
%s
                long durationMs = System.currentTimeMillis() - startTime;
                AccountingManager.Emitter em = this.emitter;
                if (em != null) {
                    AccountingMethod accountingMethod = %s;
                    io.grpc.Status.Code code = AccountingManager.grpcCodeFromError(handlerEx);
                    em.rpcError(accountingMethod, code.value(), handlerEx.getMessage(), durationMs,
                        AccountingManager.errorContextFromError(handlerEx, AccountingManager.shouldCaptureStack(code)),
                        callerIdentity, requestId);
                }
                throw handlerEx;
            }
            long durationMs = System.currentTimeMillis() - startTime;
            AccountingManager.Emitter em = this.emitter;
            if (em != null) {
                AccountingMethod accountingMethod = %s;
                em.rpcCompleted(accountingMethod, durationMs, callerIdentity, requestId);
            }
            %s
            responseObserver.onNext(com.google.protobuf.Empty.getDefaultInstance());
            responseObserver.onCompleted();
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
        } catch (Exception e) {
            responseObserver.onError(toGrpcError(e));
        }
    }
`, methodName, inputType, outputType,
					effectiveAuthCheck,
					inputValidation, traceAppend,
					fullMethod, deviceGate,
					handlerCall,
					traceResponseErrorAppend,
					accountingMethodExpr,
					accountingMethodExpr,
					traceResponseAppend)
			}
			return fmt.Sprintf(`    @Override
    public void %s(%s request, StreamObserver<%s> responseObserver) {
        try {
%s
%s%s
            // Extract caller identity and request ID
            String requestId = Auth.requestIdFromContext();
            Caller caller = Auth.callerFromContext();
            logger.debug("rpc method={} request_id={}", "%s", requestId);%s
            try {
                %s;
            } catch (Exception handlerEx) {
%s
                throw handlerEx;
            }
            %s
            responseObserver.onNext(com.google.protobuf.Empty.getDefaultInstance());
            responseObserver.onCompleted();
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
        } catch (Exception e) {
            responseObserver.onError(toGrpcError(e));
        }
    }
`, methodName, inputType, outputType,
				effectiveAuthCheck,
				inputValidation, traceAppend,
				fullMethod, deviceGate,
				handlerCall,
				traceResponseErrorAppend,
				traceResponseAppend)
		}

		if hasAccounting {
			return fmt.Sprintf(`    @Override
    public void %s(%s request, StreamObserver<%s> responseObserver) {
        try {
%s
%s%s
            // Extract caller identity and request ID
            String requestId = Auth.requestIdFromContext();
            Caller caller = Auth.callerFromContext();
            logger.debug("rpc method={} request_id={}", "%s", requestId);%s
            long startTime = System.currentTimeMillis();
	            %s response;
	            try {
	                response = %s;
	%s
	            } catch (Exception handlerEx) {
%s
	                long durationMs = System.currentTimeMillis() - startTime;
	                AccountingManager.Emitter em = this.emitter;
	                if (em != null) {
	                    AccountingMethod accountingMethod = %s;
                    io.grpc.Status.Code code = AccountingManager.grpcCodeFromError(handlerEx);
                    em.rpcError(accountingMethod, code.value(), handlerEx.getMessage(), durationMs,
                        AccountingManager.errorContextFromError(handlerEx, AccountingManager.shouldCaptureStack(code)),
                        callerIdentity, requestId);
                }
                throw handlerEx;
            }
            long durationMs = System.currentTimeMillis() - startTime;
            AccountingManager.Emitter em = this.emitter;
            if (em != null) {
	                AccountingMethod accountingMethod = %s;
	                em.rpcCompleted(accountingMethod, durationMs, callerIdentity, requestId);
	            }
	%s
	            responseObserver.onNext(response);
	            responseObserver.onCompleted();
	        } catch (ProtoException e) {
	            responseObserver.onError(toGrpcError(e));
	        } catch (Exception e) {
            responseObserver.onError(toGrpcError(e));
        }
    }
`, methodName, inputType, outputType,
				effectiveAuthCheck,
				inputValidation, traceAppend,
				fullMethod, deviceGate,
				outputType, handlerCall,
				outputValidation,
				traceResponseErrorAppend,
				accountingMethodExpr,
				accountingMethodExpr,
				traceResponseAppend)
		}

		return fmt.Sprintf(`    @Override
    public void %s(%s request, StreamObserver<%s> responseObserver) {
        try {
%s
%s%s
            // Extract caller identity and request ID
            String requestId = Auth.requestIdFromContext();
            Caller caller = Auth.callerFromContext();
            logger.debug("rpc method={} request_id={}", "%s", requestId);%s
            %s response;
            try {
                response = %s;
%s
            } catch (Exception handlerEx) {
%s
                throw handlerEx;
            }
%s
            responseObserver.onNext(response);
            responseObserver.onCompleted();
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
        } catch (Exception e) {
            responseObserver.onError(toGrpcError(e));
        }
    }
`, methodName, inputType, outputType,
			effectiveAuthCheck,
			inputValidation, traceAppend,
			fullMethod, deviceGate,
			outputType, handlerCall,
			outputValidation,
			traceResponseErrorAppend,
			traceResponseAppend)

	case !method.ClientStreaming && method.ServerStreaming && method.IsTier2SnapshotCollection():
		// Tier-2 SNAPSHOT collection: handler returns a materialized List; chunk it
		// into the stream with WS-4 flow control (ServerCallStreamObserver +
		// isReady()/setOnReadyHandler) so a slow client throttles production rather
		// than causing unbounded Netty buffering. Output validation, if any, runs
		// per item via the validating observer.
		tier2HandlerCall := fmt.Sprintf("handler.%s(caller, request)", methodName)
		if handlerEmpty {
			tier2HandlerCall = fmt.Sprintf("handler.%s(caller)", methodName)
		}
		// Egress data entitlement: sanitize each item as it is drained, after the
		// handler returned the materialized list.
		tier2San := ""
		tier2ItemExpr := "__it.next()"
		if t, ok := san.sanitizableType(method.FullOutputType, servicePackage, crossPkgHelpers); ok {
			tier2San = javaEgressSanitizePreamble([]javaSanTarget{t})
			tier2ItemExpr = t.sanitizeExpr("__it.next()")
		}
		drainSetup := fmt.Sprintf(`%s            io.grpc.stub.ServerCallStreamObserver<%s> __sco = (io.grpc.stub.ServerCallStreamObserver<%s>) responseObserver;
            StreamObserver<%s> __out = %s;
            java.util.Iterator<%s> __it = __items.iterator();
            java.util.concurrent.atomic.AtomicBoolean __done = new java.util.concurrent.atomic.AtomicBoolean(false);
            __sco.setOnReadyHandler(() -> {
                while (__it.hasNext() && __sco.isReady()) {
                    __out.onNext(%s);
                }
                if (!__it.hasNext() && __done.compareAndSet(false, true)) {
                    __out.onCompleted();
                }
            });`, tier2San, outputType, outputType, outputType, streamResponseObserver, outputType, tier2ItemExpr)

		if hasAccounting {
			return fmt.Sprintf(`    @Override
    public void %s(%s request, StreamObserver<%s> responseObserver) {
        try {
%s
%s
            String requestId = Auth.requestIdFromContext();
            Caller caller = Auth.callerFromContext();
            logger.debug("rpc method={} request_id={}", "%s", requestId);
            long startTime = System.currentTimeMillis();
            java.util.List<%s> __items;
            try {
                __items = %s;
            } catch (Exception handlerEx) {
                long durationMs = System.currentTimeMillis() - startTime;
                AccountingManager.Emitter em = this.emitter;
                if (em != null) {
                    AccountingMethod accountingMethod = %s;
                    io.grpc.Status.Code code = AccountingManager.grpcCodeFromError(handlerEx);
                    em.rpcError(accountingMethod, code.value(), handlerEx.getMessage(), durationMs,
                        AccountingManager.errorContextFromError(handlerEx, AccountingManager.shouldCaptureStack(code)),
                        callerIdentity, requestId);
                }
                throw handlerEx;
            }
%s
            long durationMs = System.currentTimeMillis() - startTime;
            AccountingManager.Emitter em = this.emitter;
            if (em != null) {
                AccountingMethod accountingMethod = %s;
                em.rpcCompleted(accountingMethod, durationMs, callerIdentity, requestId);
            }
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
        } catch (Exception e) {
            responseObserver.onError(toGrpcError(e));
        }
    }
`, methodName, inputType, outputType, authCheck, streamInputValidation, fullMethod, outputType, tier2HandlerCall, accountingMethodExpr, drainSetup, accountingMethodExpr)
		}

		return fmt.Sprintf(`    @Override
    public void %s(%s request, StreamObserver<%s> responseObserver) {
        try {
%s
%s
            String requestId = Auth.requestIdFromContext();
            Caller caller = Auth.callerFromContext();
            logger.debug("rpc method={} request_id={}", "%s", requestId);
            java.util.List<%s> __items = %s;
%s
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
        } catch (Exception e) {
            responseObserver.onError(toGrpcError(e));
        }
    }
`, methodName, inputType, outputType, authCheck, streamInputValidation, fullMethod, outputType, tier2HandlerCall, drainSetup)

	case !method.ClientStreaming && method.ServerStreaming && method.IsTier3SnapshotCollection():
		// Tier-3 SNAPSHOT cursor: handler returns a lazy Iterator producer; drive it
		// with WS-4 flow control (ServerCallStreamObserver + isReady/setOnReadyHandler)
		// and close it (if AutoCloseable) on client cancel / completion — deterministic
		// teardown.
		tier3HandlerCall := fmt.Sprintf("handler.%s(caller, request)", methodName)
		if handlerEmpty {
			tier3HandlerCall = fmt.Sprintf("handler.%s(caller)", methodName)
		}
		// Egress data entitlement: sanitize each lazily-pulled item before send.
		tier3San := ""
		tier3ItemExpr := "__it.next()"
		if t, ok := san.sanitizableType(method.FullOutputType, servicePackage, crossPkgHelpers); ok {
			tier3San = javaEgressSanitizePreamble([]javaSanTarget{t})
			tier3ItemExpr = t.sanitizeExpr("__it.next()")
		}
		tier3Drive := fmt.Sprintf(`%s            io.grpc.stub.ServerCallStreamObserver<%s> __sco = (io.grpc.stub.ServerCallStreamObserver<%s>) responseObserver;
            StreamObserver<%s> __out = %s;
            java.util.concurrent.atomic.AtomicBoolean __done = new java.util.concurrent.atomic.AtomicBoolean(false);
            __sco.setOnCancelHandler(() -> {
                if (__it instanceof AutoCloseable) {
                    try { ((AutoCloseable) __it).close(); } catch (Exception __ignored) {}
                }
            });
            __sco.setOnReadyHandler(() -> {
                while (__it.hasNext() && __sco.isReady()) {
                    __out.onNext(%s);
                }
                if (!__it.hasNext() && __done.compareAndSet(false, true)) {
                    if (__it instanceof AutoCloseable) {
                        try { ((AutoCloseable) __it).close(); } catch (Exception __ignored) {}
                    }
                    __out.onCompleted();
                }
            });`, tier3San, outputType, outputType, outputType, streamResponseObserver, tier3ItemExpr)

		if hasAccounting {
			return fmt.Sprintf(`    @Override
    public void %s(%s request, StreamObserver<%s> responseObserver) {
        try {
%s
%s
            String requestId = Auth.requestIdFromContext();
            Caller caller = Auth.callerFromContext();
            logger.debug("rpc method={} request_id={}", "%s", requestId);
            long startTime = System.currentTimeMillis();
            final java.util.Iterator<%s> __it;
            try {
                __it = %s;
            } catch (Exception handlerEx) {
                long durationMs = System.currentTimeMillis() - startTime;
                AccountingManager.Emitter em = this.emitter;
                if (em != null) {
                    AccountingMethod accountingMethod = %s;
                    io.grpc.Status.Code code = AccountingManager.grpcCodeFromError(handlerEx);
                    em.rpcError(accountingMethod, code.value(), handlerEx.getMessage(), durationMs,
                        AccountingManager.errorContextFromError(handlerEx, AccountingManager.shouldCaptureStack(code)),
                        callerIdentity, requestId);
                }
                throw handlerEx;
            }
%s
            long durationMs = System.currentTimeMillis() - startTime;
            AccountingManager.Emitter em = this.emitter;
            if (em != null) {
                AccountingMethod accountingMethod = %s;
                em.rpcCompleted(accountingMethod, durationMs, callerIdentity, requestId);
            }
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
        } catch (Exception e) {
            responseObserver.onError(toGrpcError(e));
        }
    }
`, methodName, inputType, outputType, authCheck, streamInputValidation, fullMethod, outputType, tier3HandlerCall, accountingMethodExpr, tier3Drive, accountingMethodExpr)
		}

		return fmt.Sprintf(`    @Override
    public void %s(%s request, StreamObserver<%s> responseObserver) {
        try {
%s
%s
            String requestId = Auth.requestIdFromContext();
            Caller caller = Auth.callerFromContext();
            logger.debug("rpc method={} request_id={}", "%s", requestId);
            final java.util.Iterator<%s> __it = %s;
%s
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
        } catch (Exception e) {
            responseObserver.onError(toGrpcError(e));
        }
    }
	`, methodName, inputType, outputType, authCheck, streamInputValidation, fullMethod, outputType, tier3HandlerCall, tier3Drive)

	case !method.ClientStreaming && method.ServerStreaming && method.IsWatchCollection():
		// WATCH collection: handler returns a WatchSource (snapshot + live changes).
		// The generated wrapper owns envelope framing, replay/relist via WatchHub,
		// and Java flow control. A dedicated sender thread waits for readiness and
		// data so the ServerCallStreamObserver onReady callback never blocks.
		itemType := javaWatchTypeRef(method.Resolved.WatchItemType, outputType, crossPkgOuterClasses)
		changeType := javaWatchTypeRef(method.Resolved.WatchChangeType, outputType, crossPkgOuterClasses)
		watchHandlerCall := fmt.Sprintf("handler.%s(caller, request)", methodName)
		if handlerEmpty {
			watchHandlerCall = fmt.Sprintf("handler.%s(caller)", methodName)
		}
		watchCompletedAccounting := ""
		watchErrorAccounting := ""
		if hasAccounting {
			watchCompletedAccounting = fmt.Sprintf(`
                        long durationMs = System.currentTimeMillis() - startTime;
                        AccountingManager.Emitter em = this.emitter;
                        if (em != null) {
                            AccountingMethod accountingMethod = %s;
                            em.rpcCompleted(accountingMethod, durationMs, callerIdentity, requestId);
                        }`, accountingMethodExpr)
			watchErrorAccounting = fmt.Sprintf(`
                        long durationMs = System.currentTimeMillis() - startTime;
                        AccountingManager.Emitter em = this.emitter;
                        if (em != null) {
                            AccountingMethod accountingMethod = %s;
                            io.grpc.Status.Code code = AccountingManager.grpcCodeFromError(t);
                            em.rpcError(accountingMethod, code.value(), t.getMessage(), durationMs,
                                AccountingManager.errorContextFromError(t, AccountingManager.shouldCaptureStack(code)),
                                callerIdentity, requestId);
                        }`, accountingMethodExpr)
		}
		// The request only supplies a resume point when it declares a
		// `+"`from_revision`"+` field (the standard {Method}Request). Handle-based watch
		// requests (RecordMap's QueryHandle) omit it — start from 0 (snapshot + live).
		fromRevisionExpr := "request.getFromRevision()"
		if !method.Resolved.WatchHasFromRevision {
			fromRevisionExpr = "0L"
		}

		// Egress data entitlement: sanitize each snapshot item AND each delta before
		// send. A sanitizable item/change is sanitized whole; an injected envelope
		// (e.g. <T>WatchChange) goes through a local transform Function that
		// sanitizes its record-bearing fields.
		envelopeFn := func(fnName, envType string, fs []javaEnvelopeSanField) string {
			var stmts strings.Builder
			fmt.Fprintf(&stmts, "            final java.util.function.Function<%[1]s, %[1]s> %[2]s = (__c) -> {\n", envType, fnName)
			fmt.Fprintf(&stmts, "                %s.Builder __cb = __c.toBuilder();\n", envType)
			for _, ef := range fs {
				p := javaPascalField(ef.Field.Name)
				if ef.Field.IsRepeated {
					fmt.Fprintf(&stmts, "                for (int __i = 0; __i < __cb.get%sCount(); __i++) {\n", p)
					fmt.Fprintf(&stmts, "                    __cb.set%s(__i, %s);\n                }\n", p, ef.Target.sanitizeExpr("__cb.get"+p+"(__i)"))
					continue
				}
				fmt.Fprintf(&stmts, "                if (__cb.has%s()) { __cb.set%s(%s); }\n", p, p, ef.Target.sanitizeExpr("__cb.get"+p+"()"))
			}
			stmts.WriteString("                return __cb.build();\n            };\n")
			return stmts.String()
		}
		var watchTargets []javaSanTarget
		var watchFns string
		watchItemExpr := "__source.snapshot.next()"
		watchChangeExpr := "__changes.next()"
		if t, ok := san.sanitizableType(method.Resolved.WatchItemType, servicePackage, crossPkgHelpers); ok {
			watchTargets = append(watchTargets, t)
			watchItemExpr = t.sanitizeExpr(watchItemExpr)
		} else if fs := san.envelopeSanFields(method.Resolved.WatchItemType, servicePackage, crossPkgHelpers); len(fs) > 0 {
			for _, f := range fs {
				watchTargets = append(watchTargets, f.Target)
			}
			watchFns += envelopeFn("__sanitizeItem", itemType, fs)
			watchItemExpr = "__sanitizeItem.apply(" + watchItemExpr + ")"
		}
		if t, ok := san.sanitizableType(method.Resolved.WatchChangeType, servicePackage, crossPkgHelpers); ok {
			watchTargets = append(watchTargets, t)
			watchChangeExpr = t.sanitizeExpr(watchChangeExpr)
		} else if fs := san.envelopeSanFields(method.Resolved.WatchChangeType, servicePackage, crossPkgHelpers); len(fs) > 0 {
			for _, f := range fs {
				watchTargets = append(watchTargets, f.Target)
			}
			watchFns += envelopeFn("__sanitizeDelta", changeType, fs)
			watchChangeExpr = "__sanitizeDelta.apply(" + watchChangeExpr + ")"
		}
		watchPre := ""
		if len(watchTargets) > 0 {
			watchPre = javaEgressSanitizePreamble(watchTargets) + watchFns
		}
		// (spec.store_principal) on a watched stream: the guard is spliced into
		// the send path (below), so an event carrying a row the caller does not
		// own is never written. snapshot_done carries no row and still passes,
		// so a filtered stream still ends its snapshot phase.
		watchPrincipalGuard := ""
		watchNamespaceGuard := ""
		if isC2S || recordPlaneS2S {
			// As on the predicate seam: the principal guard is a statement about
			// a user, so it stays c2s-only.
			if isC2S {
				var watchPrincipalPrivate bool
				watchPrincipalGuard, watchPrincipalPrivate = san.javaPrincipalWatchGuard(method.FullOutputType, servicePackage, method.Name, "                ")
				if watchPrincipalGuard != "" {
					if watchPrincipalPrivate {
						watchPre = javaPrivatePrincipalResolve(method.Name, "            ") + watchPre
					} else {
						watchPre = javaPrincipalResolve(method.Name, "            ") + watchPre
					}
				}
			}
			// (spec.store_namespace) on a watched stream: same splice, and an
			// event carrying another tenant's row aborts rather than being
			// skipped — see recordnamespace_gen.go.
			watchNamespaceGuard = san.javaNamespaceWatchGuard(method.FullOutputType, servicePackage, method.Name, "                ", recordPlaneS2S)
			if watchNamespaceGuard != "" {
				watchPre = javaNamespaceResolve(method.Name, "            ", recordPlaneS2S) + watchPre
			}
		}
		watchDrive := fmt.Sprintf(`%s            final CollectionRuntime.WatchSource<%s, %s> __source = %s;
            final long __fromRevision = %s;
            final long __resumeRevision = __fromRevision == 0 ? __source.revision : __fromRevision;
            final java.util.Iterator<%s> __changes = %sWatchHub.watch(
                    __source.scope,
                    __resumeRevision,
                    __source.changes,
                    change -> change.get%s());
            io.grpc.stub.ServerCallStreamObserver<%s> __sco = (io.grpc.stub.ServerCallStreamObserver<%s>) responseObserver;
            StreamObserver<%s> __out = %s;
            final Object __readyLock = new Object();
            java.util.concurrent.atomic.AtomicBoolean __done = new java.util.concurrent.atomic.AtomicBoolean(false);
            __sco.setOnReadyHandler(() -> {
                synchronized (__readyLock) {
                    __readyLock.notifyAll();
                }
            });
            __sco.setOnCancelHandler(() -> {
                __done.set(true);
                synchronized (__readyLock) {
                    __readyLock.notifyAll();
                }
                if (__changes instanceof AutoCloseable) {
                    try { ((AutoCloseable) __changes).close(); } catch (Exception __ignored) {}
                }
            });
            java.util.function.Consumer<%s> __send = (__event) -> {
                synchronized (__readyLock) {
                    while (!__done.get() && !__sco.isReady()) {
                        try {
                            __readyLock.wait();
                        } catch (InterruptedException e) {
                            Thread.currentThread().interrupt();
                            throw io.grpc.Status.CANCELLED.withCause(e).asRuntimeException();
                        }
                    }
                }
                if (!__done.get()) {
                    __out.onNext(__event);
                }
            };
            Thread __watchThread = new Thread(() -> {
                try {
                    if (__fromRevision == 0) {
                        while (!__done.get() && __source.snapshot.hasNext()) {
                            __send.accept(%s.newBuilder().setSnapshotItem(%s).build());
                        }
                        if (!__done.get()) {
                            __send.accept(%s.newBuilder().setSnapshotDone(__source.revision).build());
                        }
                    }
                    while (!__done.get() && __changes.hasNext()) {
                        __send.accept(%s.newBuilder().setDelta(%s).build());
                    }
                    if (__done.compareAndSet(false, true)) {
                        __out.onCompleted();
%s
                    }
                } catch (Throwable t) {
                    if (__done.compareAndSet(false, true)) {
%s
                        __out.onError(toGrpcError(t));
                    }
                } finally {
                    if (__changes instanceof AutoCloseable) {
                        try { ((AutoCloseable) __changes).close(); } catch (Exception __ignored) {}
                    }
                }
            }, "tacplus-watch-%s");
            __watchThread.setDaemon(true);
            __watchThread.start();`, watchPre, itemType, changeType, watchHandlerCall, fromRevisionExpr, changeType, methodName, javaPascalField(method.Resolved.WatchRevisionField), outputType, outputType, outputType, streamResponseObserver, outputType, outputType, watchItemExpr, outputType, outputType, watchChangeExpr, watchCompletedAccounting, watchErrorAccounting, methodName)

		// Splice the (spec.store_principal) refusal into the send path. Done on
		// the rendered text rather than as a format verb because the guard is a
		// property of the response TYPE, not of any of the twenty positional
		// substitutions above.
		if watchPrincipalGuard+watchNamespaceGuard != "" {
			watchDrive = strings.Replace(watchDrive,
				"__send = (__event) -> {\n",
				"__send = (__event) -> {\n"+watchPrincipalGuard+watchNamespaceGuard, 1)
		}

		if hasAccounting {
			return fmt.Sprintf(`    @Override
    public void %s(%s request, StreamObserver<%s> responseObserver) {
        try {
%s
%s
            String requestId = Auth.requestIdFromContext();
            Caller caller = Auth.callerFromContext();
            logger.debug("rpc method={} request_id={}", "%s", requestId);
            long startTime = System.currentTimeMillis();
%s
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
        } catch (Exception e) {
            responseObserver.onError(toGrpcError(e));
        }
    }
`, methodName, inputType, outputType, authCheck, streamInputValidation, fullMethod, watchDrive)
		}

		return fmt.Sprintf(`    @Override
    public void %s(%s request, StreamObserver<%s> responseObserver) {
        try {
%s
%s
            String requestId = Auth.requestIdFromContext();
            Caller caller = Auth.callerFromContext();
            logger.debug("rpc method={} request_id={}", "%s", requestId);
%s
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
        } catch (Exception e) {
            responseObserver.onError(toGrpcError(e));
        }
    }
`, methodName, inputType, outputType, authCheck, streamInputValidation, fullMethod, watchDrive)

	case !method.ClientStreaming && method.ServerStreaming:
		// Server streaming — authorize, extract caller, delegate to handler
		handlerCall := fmt.Sprintf("handler.%s(caller, request, %s)", methodName, streamResponseObserver)
		if handlerEmpty {
			handlerCall = fmt.Sprintf("handler.%s(caller, %s)", methodName, streamResponseObserver)
		}

		if hasAccounting {
			return fmt.Sprintf(`    @Override
    public void %s(%s request, StreamObserver<%s> responseObserver) {
        try {
%s
%s
            // Extract caller identity and request ID
            String requestId = Auth.requestIdFromContext();
            Caller caller = Auth.callerFromContext();
            logger.debug("rpc method={} request_id={}", "%s", requestId);
            long startTime = System.currentTimeMillis();
            try {
                %s;
            } catch (Exception handlerEx) {
                long durationMs = System.currentTimeMillis() - startTime;
                AccountingManager.Emitter em = this.emitter;
                if (em != null) {
                    AccountingMethod accountingMethod = %s;
                    io.grpc.Status.Code code = AccountingManager.grpcCodeFromError(handlerEx);
                    em.rpcError(accountingMethod, code.value(), handlerEx.getMessage(), durationMs,
                        AccountingManager.errorContextFromError(handlerEx, AccountingManager.shouldCaptureStack(code)),
                        callerIdentity, requestId);
                }
                throw handlerEx;
            }
            long durationMs = System.currentTimeMillis() - startTime;
            AccountingManager.Emitter em = this.emitter;
            if (em != null) {
                AccountingMethod accountingMethod = %s;
                em.rpcCompleted(accountingMethod, durationMs, callerIdentity, requestId);
            }
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
        } catch (Exception e) {
            responseObserver.onError(toGrpcError(e));
        }
    }
	`, methodName, inputType, outputType, authCheck, streamInputValidation, fullMethod,
				handlerCall, accountingMethodExpr, accountingMethodExpr)
		}

		return fmt.Sprintf(`    @Override
    public void %s(%s request, StreamObserver<%s> responseObserver) {
        try {
%s
%s
            // Extract caller identity and request ID
            String requestId = Auth.requestIdFromContext();
            Caller caller = Auth.callerFromContext();
            logger.debug("rpc method={} request_id={}", "%s", requestId);
            %s;
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
        } catch (Exception e) {
            responseObserver.onError(toGrpcError(e));
        }
    }
	`, methodName, inputType, outputType, authCheck, streamInputValidation, fullMethod, handlerCall)

	case method.ClientStreaming && !method.ServerStreaming:
		// Client streaming — authorize on stream open, delegate to handler
		var authCheckNoReq string
		if authDisabled {
			// Nothing verifies a caller here, so accounting records carry no identity.
			authCheckNoReq = `        java.util.Map<String, String> callerIdentity = java.util.Collections.emptyMap();`
		} else if authAnyUser {
			authCheckNoReq = fmt.Sprintf(`        %s
        java.util.Map<String, String> callerIdentity;
        try {
            callerIdentity = %s;
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
            return new StreamObserver<%s>() {
                @Override public void onNext(%s value) {}
                @Override public void onError(Throwable t) {}
                @Override public void onCompleted() {}
            };
        }`, authAnyUserComment, authAnyUserCall, inputType, inputType)
		} else if needsStreamMessageAuth {
			authCheckNoReq = fmt.Sprintf(`        // Check authentication before per-message target namespace authorization
        java.util.Map<String, String> callerIdentity;
        try {
            callerIdentity = checkAuthenticate();
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
            return new StreamObserver<%s>() {
                @Override public void onNext(%s value) {}
                @Override public void onError(Throwable t) {}
                @Override public void onCompleted() {}
            };
        }`, inputType, inputType)
		} else {
			authCheckNoReq = fmt.Sprintf(`        // Check authorization
        java.util.Map<String, String> callerIdentity;
        try {
            callerIdentity = checkAuth("%s", null);
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
            return new StreamObserver<%s>() {
                @Override public void onNext(%s value) {}
                @Override public void onError(Throwable t) {}
                @Override public void onCompleted() {}
            };
        }`, authMethod, inputType, inputType)
		}

		if hasAccounting {
			accountedStreamResponseObserver := strings.ReplaceAll(streamResponseObserver, "responseObserver", "__accountingResponseObserver")
			handlerObserverExpr := clientStreamInputObserver(
				fmt.Sprintf("handler.%s(caller, %s)", methodName, accountedStreamResponseObserver),
				"__accountingResponseObserver")
			return fmt.Sprintf(`    @Override
    public StreamObserver<%s> %s(StreamObserver<%s> responseObserver) {
%s

        // Extract caller identity and request ID
        String requestId = Auth.requestIdFromContext();
        Caller caller = Auth.callerFromContext();
        logger.debug("rpc method={} request_id={}", "%s", requestId);
        long startTime = System.currentTimeMillis();
        AccountingMethod accountingMethod = %s;
        java.util.concurrent.atomic.AtomicBoolean __accounted = new java.util.concurrent.atomic.AtomicBoolean(false);
        StreamObserver<%s> __accountingResponseObserver =
                accountingResponseObserver(responseObserver, accountingMethod, startTime, callerIdentity, requestId, __accounted);
        try {
            StreamObserver<%s> __handlerObserver = %s;
            return accountingRequestObserver(__handlerObserver, accountingMethod, startTime, callerIdentity, requestId, __accounted);
        } catch (Exception e) {
            __accountingResponseObserver.onError(toGrpcError(e));
            return new StreamObserver<%s>() {
                @Override public void onNext(%s value) {}
                @Override public void onError(Throwable t) {}
                @Override public void onCompleted() {}
            };
        }
    }
	`, inputType, methodName, outputType, authCheckNoReq, fullMethod,
				accountingMethodExpr, outputType, inputType, handlerObserverExpr,
				inputType, inputType)
		}

		return fmt.Sprintf(`    @Override
    public StreamObserver<%s> %s(StreamObserver<%s> responseObserver) {
%s

        // Extract caller identity and request ID
        String requestId = Auth.requestIdFromContext();
	        Caller caller = Auth.callerFromContext();
	        logger.debug("rpc method={} request_id={}", "%s", requestId);
	        try {
	            return %s;
	        } catch (ProtoException e) {
	            responseObserver.onError(toGrpcError(e));
            return new StreamObserver<%s>() {
                @Override public void onNext(%s value) {}
                @Override public void onError(Throwable t) {}
                @Override public void onCompleted() {}
            };
        }
    }
	`, inputType, methodName, outputType, authCheckNoReq, fullMethod,
			clientStreamInputObserver(fmt.Sprintf("handler.%s(caller, %s)", methodName, streamResponseObserver), "responseObserver"),
			inputType, inputType)

	case method.ClientStreaming && method.ServerStreaming:
		// Bidirectional streaming — authorize on stream open, delegate to handler
		//
		// The generated MCP session carrier has no handler method to delegate
		// to: the whole session is generated (mcp_dispatch_gen.go). It goes
		// through this ordinary branch — same authorization, same caller
		// extraction, same validatingRequestObserver/validatingResponseObserver
		// framing — with only the delegation target swapped and a fail-closed
		// caller check in front.
		mcpCarrierCall := ""
		if method.Name == parser.McpCarrierRpcName(serviceName) {
			mcpCarrierCall = fmt.Sprintf("%sMcp.serve(handler, caller, %%s)", serviceName)
		}
		var authCheckNoReq string
		if authDisabled {
			// Nothing verifies a caller here, so accounting records carry no identity.
			authCheckNoReq = `        java.util.Map<String, String> callerIdentity = java.util.Collections.emptyMap();`
		} else if authAnyUser {
			authCheckNoReq = fmt.Sprintf(`        %s
        java.util.Map<String, String> callerIdentity;
        try {
            callerIdentity = %s;
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
            return new StreamObserver<%s>() {
                @Override public void onNext(%s value) {}
                @Override public void onError(Throwable t) {}
                @Override public void onCompleted() {}
            };
        }`, authAnyUserComment, authAnyUserCall, inputType, inputType)
		} else if needsStreamMessageAuth {
			authCheckNoReq = fmt.Sprintf(`        // Check authentication before per-message target namespace authorization
        java.util.Map<String, String> callerIdentity;
        try {
            callerIdentity = checkAuthenticate();
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
            return new StreamObserver<%s>() {
                @Override public void onNext(%s value) {}
                @Override public void onError(Throwable t) {}
                @Override public void onCompleted() {}
            };
        }`, inputType, inputType)
		} else {
			authCheckNoReq = fmt.Sprintf(`        // Check authorization
        java.util.Map<String, String> callerIdentity;
        try {
            callerIdentity = checkAuth("%s", null);
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
            return new StreamObserver<%s>() {
                @Override public void onNext(%s value) {}
                @Override public void onError(Throwable t) {}
                @Override public void onCompleted() {}
            };
        }`, authMethod, inputType, inputType)
		}

		if hasAccounting {
			accountedStreamResponseObserver := strings.ReplaceAll(streamResponseObserver, "responseObserver", "__accountingResponseObserver")
			accountedDelegate := fmt.Sprintf("handler.%s(caller, %s)", methodName, accountedStreamResponseObserver)
			if mcpCarrierCall != "" {
				accountedDelegate = fmt.Sprintf(mcpCarrierCall, accountedStreamResponseObserver)
			}
			handlerObserverExpr := clientStreamInputObserver(accountedDelegate, "__accountingResponseObserver")
			return fmt.Sprintf(`    @Override
    public StreamObserver<%s> %s(StreamObserver<%s> responseObserver) {
%s

        // Extract caller identity and request ID
        String requestId = Auth.requestIdFromContext();
        Caller caller = Auth.callerFromContext();
        logger.debug("rpc method={} request_id={}", "%s", requestId);
        long startTime = System.currentTimeMillis();
        AccountingMethod accountingMethod = %s;
        java.util.concurrent.atomic.AtomicBoolean __accounted = new java.util.concurrent.atomic.AtomicBoolean(false);
        StreamObserver<%s> __accountingResponseObserver =
                accountingResponseObserver(responseObserver, accountingMethod, startTime, callerIdentity, requestId, __accounted);
        try {
            StreamObserver<%s> __handlerObserver = %s;
            return accountingRequestObserver(__handlerObserver, accountingMethod, startTime, callerIdentity, requestId, __accounted);
        } catch (Exception e) {
            __accountingResponseObserver.onError(toGrpcError(e));
            return new StreamObserver<%s>() {
                @Override public void onNext(%s value) {}
                @Override public void onError(Throwable t) {}
                @Override public void onCompleted() {}
            };
        }
    }
	`, inputType, methodName, outputType, authCheckNoReq, fullMethod,
				accountingMethodExpr, outputType, inputType, handlerObserverExpr,
				inputType, inputType)
		}

		delegate := fmt.Sprintf("handler.%s(caller, %s)", methodName, streamResponseObserver)
		callerGuard := ""
		if mcpCarrierCall != "" {
			delegate = fmt.Sprintf(mcpCarrierCall, streamResponseObserver)
			// Fail closed: every server-computed predicate in the tool surface
			// comes from this caller, so a session with no verified identity
			// would run its tools unscoped.
			callerGuard = `
        if (caller == null) {
            responseObserver.onError(toGrpcError(new ProtoException(ProtoException.UNAUTHENTICATED, "an MCP session requires an authenticated caller")));
            return new StreamObserver<` + inputType + `>() {
                @Override public void onNext(` + inputType + ` value) {}
                @Override public void onError(Throwable t) {}
                @Override public void onCompleted() {}
            };
        }`
		}

		return fmt.Sprintf(`    @Override
    public StreamObserver<%s> %s(StreamObserver<%s> responseObserver) {
%s

        // Extract caller identity and request ID
        String requestId = Auth.requestIdFromContext();
	        Caller caller = Auth.callerFromContext();
	        logger.debug("rpc method={} request_id={}", "%s", requestId);%s
	        try {
	            return %s;
	        } catch (ProtoException e) {
	            responseObserver.onError(toGrpcError(e));
            return new StreamObserver<%s>() {
                @Override public void onNext(%s value) {}
                @Override public void onError(Throwable t) {}
                @Override public void onCompleted() {}
            };
        }
    }
	`, inputType, methodName, outputType, authCheckNoReq, fullMethod, callerGuard,
			clientStreamInputObserver(delegate, "responseObserver"),
			inputType, inputType)
	}

	return ""
}

// grpcGroupEntry tracks a service for a group-level GrpcServers aggregator.
type grpcGroupEntry struct {
	FieldName   string // e.g. "baseballGrpcWrapper"
	MethodName  string // e.g. "baseball"
	ClassName   string // e.g. "BaseballGrpcWrapper"
	FullPackage string // e.g. "com.example.s2sm.internal.grpc.servers.services.baseball"
}

// generateGrpcGroupAggregators generates GrpcServers.java at each group level
// (e.g., sm/internal/grpc/servers/services/GrpcServers.java).
func generateGrpcGroupAggregators(config *tactype.Config, wrappers []generatedGrpcWrapper) error {
	if len(wrappers) == 0 {
		return nil
	}

	// Group by ParentDir
	groups := make(map[string][]generatedGrpcWrapper)
	for _, w := range wrappers {
		groups[w.ParentDir] = append(groups[w.ParentDir], w)
	}

	for group, groupWrappers := range groups {
		groupPackage := fmt.Sprintf("%s."+config.GetSmPrefix()+".internal.grpc.servers.%s", config.JavaPackageBase, group)

		var entries []grpcGroupEntry
		// Detect duplicate method names: when multiple services share a package
		// (e.g., Admin + EventbusStateService both in "eventbus"), use the
		// lowercased class name as the method name instead of the package name.
		methodNameCounts := make(map[string]int)
		for _, w := range groupWrappers {
			methodNameCounts[w.PackageName]++
		}
		for _, w := range groupWrappers {
			fieldName := strings.ToLower(w.ClassName[:1]) + w.ClassName[1:]
			methodName := w.PackageName
			if methodNameCounts[methodName] > 1 {
				methodName = fieldName
			}
			entries = append(entries, grpcGroupEntry{
				FieldName:   fieldName,
				MethodName:  methodName,
				ClassName:   w.ClassName,
				FullPackage: w.FullPackage,
			})
		}

		data := struct {
			Header  string
			Package string
			Entries []grpcGroupEntry
		}{
			Header:  header.GeneratedHeader(consts.LangJava, config.GetSmPrefix()+"/internal/grpc/servers/"+group),
			Package: groupPackage,
			Entries: entries,
		}

		grpcGroupTmplStr, err := loadTemplate("grpc_group.java.tmpl")
		if err != nil {
			return fmt.Errorf("failed to load gRPC group template: %w", err)
		}
		tmpl, err := template.New("fegroup").Parse(grpcGroupTmplStr)
		if err != nil {
			return fmt.Errorf("failed to parse gRPC group template: %w", err)
		}

		packagePath := strings.ReplaceAll(groupPackage, ".", string(filepath.Separator))
		outputDir := filepath.Join(config.OutputDir, "src", "main", "java", packagePath)
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return fmt.Errorf("failed to create group directory: %w", err)
		}

		outputPath := filepath.Join(outputDir, "GrpcServers.java")
		file, err := os.Create(outputPath)
		if err != nil {
			return fmt.Errorf("failed to create GrpcServers.java: %w", err)
		}
		defer file.Close()

		if err := tmpl.Execute(file, data); err != nil {
			return fmt.Errorf("failed to execute gRPC group template: %w", err)
		}

		zap.L().Info("Generated group GrpcServers.java", zap.String("group", group), zap.String("path", outputPath))
	}

	return nil
}

// grpcRootGroup tracks a group for the root aggregator.
type grpcRootGroup struct {
	Name         string // e.g. "services"
	MethodName   string // e.g. "Services"
	GroupPackage string // e.g. "com.example.s2sm.internal.grpc.servers.services"
}

// generateGrpcRootAggregator generates sm/internal/grpc/servers/GrpcServers.java —
// the root aggregator that references group aggregators.
func generateGrpcRootAggregator(config *tactype.Config, wrappers []generatedGrpcWrapper) error {
	if len(wrappers) == 0 {
		return nil
	}

	rootPackage := config.JavaPackageBase + "." + config.GetSmPrefix() + ".internal.grpc.servers"

	// Collect unique groups
	seen := make(map[string]bool)
	var groups []grpcRootGroup
	for _, w := range wrappers {
		if seen[w.ParentDir] {
			continue
		}
		seen[w.ParentDir] = true
		groups = append(groups, grpcRootGroup{
			Name:         w.ParentDir,
			MethodName:   strings.ToUpper(w.ParentDir[:1]) + w.ParentDir[1:],
			GroupPackage: fmt.Sprintf("%s."+config.GetSmPrefix()+".internal.grpc.servers.%s", config.JavaPackageBase, w.ParentDir),
		})
	}

	data := struct {
		Header  string
		Package string
		Groups  []grpcRootGroup
	}{
		Header:  header.GeneratedHeader(consts.LangJava, config.GetSmPrefix()+"/internal/grpc/servers"),
		Package: rootPackage,
		Groups:  groups,
	}

	grpcRootTmplStr, err := loadTemplate("grpc_root.java.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load gRPC root template: %w", err)
	}
	tmpl, err := template.New("feroot").Parse(grpcRootTmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse gRPC root template: %w", err)
	}

	packagePath := strings.ReplaceAll(rootPackage, ".", string(filepath.Separator))
	outputDir := filepath.Join(config.OutputDir, "src", "main", "java", packagePath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create grpcservers directory: %w", err)
	}

	outputPath := filepath.Join(outputDir, "GrpcServers.java")
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create root GrpcServers.java: %w", err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, data); err != nil {
		return fmt.Errorf("failed to execute gRPC root template: %w", err)
	}

	zap.L().Info("Generated root GrpcServers.java", zap.String("path", outputPath))
	return nil
}

// ----- (spec.store_epoch) and (spec.record).mutability in the service wrapper -----
//
// These are the RUNTIME end of the two annotations whose generation end lives in
// pkg/internal/common/parser (ValidateResidencyAnnotations, validateAppendOnlyMethods).
// Both follow the shape (spec.record).min_group_size already has above: the same
// declared value drives a generation-time refusal AND a rendered guard, and the
// generator FAILS rather than emit a surface it cannot guard.

// storeEpochField resolves WHICH field of a message carries the row's store epoch,
// or "" when it declares none. The location is the annotation's whole job — the
// stamp itself is the wrapper's, exactly as with (spec.principal).
//
// Well-formedness (int64, non-repeated, RANGE-queryable) is enforced once, at
// parse time, by parser.ValidateResidencyAnnotations. This is a lookup, not a
// second opinion, and it is the SHARED parser.StoreEpochField so Go, Java and
// Rust cannot come to stamp three different fields of one request.
func (es *javaEgressSanitizer) storeEpochField(fullType, servicePackage string) string {
	if es == nil || fullType == "" {
		return ""
	}
	pkg, name := splitJavaEgressFQN(fullType, servicePackage)
	return parser.StoreEpochField(es.msgs[pkg+"\x00"+name])
}

// isAppendOnlyRecord reports whether fullType is a record declaring
// MUTABILITY_APPEND_ONLY.
func (es *javaEgressSanitizer) isAppendOnlyRecord(fullType, servicePackage string) bool {
	if es == nil || fullType == "" {
		return false
	}
	pkg, name := splitJavaEgressFQN(fullType, servicePackage)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return false
	}
	if ann, ok := msg.Annotations["record"]; !ok || !ann.BoolValue {
		return false
	}
	return msg.Annotations["record_mutability"].StringValue == "MUTABILITY_APPEND_ONLY"
}

// javaStoreEpochStamp renders the (spec.store_epoch) stamp-or-refuse check over a
// request in scope. valueExpr names the request; indent is the leading whitespace
// of the block it is spliced into.
//
// Unlike (spec.principal), there is no "equal passes" case: the store owns this
// value outright, so it is never caller-supplied and any supplied value — even a
// correct-looking one — is a refusal. INVALID_ARGUMENT, in the wrapper, never in
// the handler.
func javaStoreEpochStamp(field, valueExpr, indent string) string {
	if field == "" {
		return ""
	}
	p := javaPascalField(field)
	return fmt.Sprintf(`%[1]s// (spec.store_epoch) %[2]s: the row's last-write clock is the STORE's, never
%[1]s// the caller's. Unset -> stamped here; set -> refused. The wrapper owns the
%[1]s// stamp so no handler hand-writes it (same doctrine as (spec.principal)).
%[1]sif (%[3]s.get%[4]s() != 0L) {
%[1]s    throw new ProtoException(ProtoException.INVALID_ARGUMENT, "%[2]s is stamped by the store and must not be supplied by the caller");
%[1]s}
%[1]s%[3]s = %[3]s.toBuilder().set%[4]s(System.currentTimeMillis()).build();
`, indent, field, valueExpr, p)
}

// javaStoreEpochUnenforceable reports why the (spec.store_epoch) stamp cannot be
// generated for a method that carries an epoch-bearing request, or "" when it
// can. A write the wrapper cannot stamp must fail generation rather than reach a
// handler with an unstamped epoch — an unstamped store epoch is a row the purge
// measures as infinitely old.
func javaStoreEpochUnenforceable(method *parser.MethodInfo, san *javaEgressSanitizer, servicePackage string) string {
	if san.storeEpochField(method.FullInputType, servicePackage) == "" {
		return ""
	}
	if method.ClientStreaming || method.ServerStreaming {
		return "the stamp is generated on unary requests only; a streamed write would need a per-item stamp the wrapper does not render"
	}
	return ""
}

// javaAppendOnlyUnenforceable reports why this wrapper must not host the method,
// or "" when it may. A row mutation over an append-only record has no guard to
// render — the correct generated code for it is NO generated code — so hosting it
// is a generation failure, which is what "emits no mutating RPCs" means at the
// wrapper end.
//
// parser.validateAppendOnlyMethods refuses the same method earlier, at the
// descriptor. This is the second end: the two read the same annotation through
// the same definition of "row-mutating" (parser.IsRowMutatingPattern), so a
// revert of either one is caught by the other rather than shipping a mutating
// surface for a write-once record.
func javaAppendOnlyUnenforceable(method *parser.MethodInfo, san *javaEgressSanitizer, servicePackage string) string {
	if !parser.IsRowMutatingPattern(method.Class) {
		return ""
	}
	for _, t := range []string{method.FullInputType, method.FullOutputType} {
		if san.isAppendOnlyRecord(t, servicePackage) {
			return fmt.Sprintf("it carries %s, which declares (spec.record).mutability = MUTABILITY_APPEND_ONLY; "+
				"an append-only record's rows are written once, so no row-mutating RPC may be hosted for the type", t)
		}
	}
	return ""
}

// javaMethodAuthKey builds the method identifier the generated wrapper hands to
// Authorizer.authorize() — and therefore the key its per-method auth_services
// and target_namespace_field maps must be registered under.
//
// The shape is deliberate and must not be "fully qualified everywhere": the
// Authorizer derives the required C2S grant symbol from this same string and
// falls back to its OWN service name for the grant's package when handed an
// unqualified name. That fallback is right for a service the process owns and
// wrong for a vservice, whose trait package must supply the symbol — so a
// vservice, and only a vservice, is package-qualified here.
func javaMethodAuthKey(servicePackage, serviceName, methodName, parentDir string) string {
	if parentDir == "vservices" {
		return fmt.Sprintf("%s.%s/%s", servicePackage, serviceName, methodName)
	}
	return fmt.Sprintf("%s/%s", serviceName, methodName)
}
