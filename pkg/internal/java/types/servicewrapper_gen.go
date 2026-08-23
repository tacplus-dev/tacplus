// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/header"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/consts"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
	spec "gitlab.com/secondstarcavalier/schematic/tacplus/target/proto/spec/sys"
)

// ServiceInfo represents a service that needs a wrapper generated.
type ServiceInfo struct {
	ProtoFile      string
	PackageName    string
	ServiceName    string
	GrpcName       string // service identity for routing (package name)
	IsGrpc         bool
	IsS2S          bool // true if service has service_s2s annotation
	IsC2S          bool // true if service has service_c2s annotation
	IsC2D          bool // true if service has service_c2d annotation
	IsSMB          bool // true if service has smb annotation
	SmbRoot        spec.SmbRoot
	OutputDir      string
	TypesOutputDir string // Output dir for the types package (for outer class name detection)
	ParentDir      string
	Config         *tactype.Config
	AuthDisabled   bool     // C2S: no token required
	AuthAnyUser    bool     // C2S: any authenticated user can call
	AuthServices   []string // S2S: allowed caller identities
	HasMethodAuth  bool     // true if any per-method auth metadata must be registered
	HostTag        string   // non-empty for vservices: the SelfPackage of the service that hosts this RPC surface
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
			if svc.GrpcName != "" {
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
			if !isGrpc && !hasState {
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
			for _, m := range service.Methods {
				if m.AuthAnyUser {
					anyUser = true
				}
				if len(m.AuthServices) > 0 {
					hasMethodAuth = true
				}
				if m.TargetNamespaceField != "" && !m.AuthAnyUser {
					hasMethodAuth = true
				}
			}
			servicesInfo = append(servicesInfo, ServiceInfo{
				ProtoFile:       relPath,
				PackageName:     packageName,
				ServiceName:     service.Name,
				GrpcName:        service.GrpcName,
				IsGrpc:          isGrpc,
				IsS2S:           service.IsS2S,
				SynthesizedKind: service.SynthesizedKind,
				IsC2S:           service.IsC2S,
				IsC2D:           service.IsC2D,
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
				HostTag:         service.HostTag,
			})
		}

		return nil
	})

	return servicesInfo, err
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
//   - Hosted vservices from other packages that declare {tag: "<SelfPackage>"} on their service_s2s annotation
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
			if svc.PackageName == config.SelfPackage && svc.IsGrpc && (svc.IsS2S || svc.IsC2S) {
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

// filterByHostTag returns vservice entries from other packages whose HostTag matches pkg.
// These are services defined in vservices/ protos that designate pkg as their host via
// option (spec.service_s2s) = {tag: "<pkg>"}.
func filterByHostTag(servicesInfo []ServiceInfo, pkg string) []ServiceInfo {
	var result []ServiceInfo
	for _, svc := range servicesInfo {
		if svc.HostTag == pkg && svc.PackageName != pkg {
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
		if svc.IsGrpc && !svc.IsC2D {
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
func generateServiceWrappers(config *tactype.Config, servicesInfo []ServiceInfo, inputDir string, accountingMap map[string]service.AccountingInfo, skippedTraceTypes proto.SkippedTraceTypes) error {
	// Create one descriptor parser for the entire proto root (like Go does)
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
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
			return fmt.Errorf("convert %s for egress sanitize resolution: %w", relPath, err)
		}
		allFiles = append(allFiles, mf)
	}
	san := newJavaEgressSanitizer(allFiles)

	var generated []generatedGrpcWrapper

	for _, svc := range servicesInfo {
		// Use the descriptor parser to get method information
		protoInfo, err := descriptorParser.ConvertToProtoInfo(svc.ProtoFile)
		if err != nil {
			return fmt.Errorf("failed to parse %s: %w", svc.ProtoFile, err)
		}

		// Generate ServiceWrapper.java in the services output directory
		if err := generateServiceWrapper(svc, protoInfo, accountingMap, skippedTraceTypes, san); err != nil {
			return fmt.Errorf("failed to generate service wrapper for %s: %w", svc.ServiceName, err)
		}

		// Track for aggregator generation (only gRPC services)
		if svc.IsGrpc {
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
		return fmt.Errorf("failed to generate gRPC group aggregators: %w", err)
	}

	// Generate root gRPC aggregator
	if err := generateGrpcRootAggregator(config, generated); err != nil {
		return fmt.Errorf("failed to generate gRPC root aggregator: %w", err)
	}

	return nil
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
func generateServiceWrapper(svc ServiceInfo, info *parser.ProtoInfo, accountingMap map[string]service.AccountingInfo, skippedTraceTypes proto.SkippedTraceTypes, san *javaEgressSanitizer) error {
	className := serverClassName(svc.ServiceName)
	outputPath := filepath.Join(svc.OutputDir, className+".java")

	if err := os.MkdirAll(svc.OutputDir, 0755); err != nil {
		return fmt.Errorf("failed to create service wrapper output dir: %w", err)
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
		return fmt.Errorf("service %s not found in proto info for %s", svc.ServiceName, svc.ProtoFile)
	}

	serviceName := svc.ServiceName

	// Detect the Java outer class name (all message types are nested inside it).
	var outerClassName string
	protoDir := filepath.Join(svc.Config.OutputDir, "proto")

	// Determine if the service proto is flat (services/baseball.proto) or
	// directory-based (services/baseball/types.proto).
	svcName, svcFileName := proto.ServiceProtoParts(svc.ProtoFile)
	isFlatServiceProto := svcName == svcFileName

	isStateService := !svc.IsGrpc

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
	for _, method := range targetService.Methods {
		inputType := resolveJavaMethodType(method.FullInputType, method.InputType, info.Package, outerClassName, crossPkgOuterClasses)
		outputType := resolveJavaMethodType(method.FullOutputType, method.OutputType, info.Package, outerClassName, crossPkgOuterClasses)
		inputHelpers := resolveValidationHelpers(method.FullInputType, info.Package, crossPkgHelpers)
		outputHelpers := resolveValidationHelpers(method.FullOutputType, info.Package, crossPkgHelpers)
		if method.IsWatchCollection() {
			changeType := javaWatchTypeRef(method.Resolved.WatchChangeType, outputType, crossPkgOuterClasses)
			fmt.Fprintf(&watchFields, "    private final CollectionRuntime.WatchHub<%s> %sWatchHub = new CollectionRuntime.WatchHub<>(%d, %d);\n",
				changeType, methodNameLowerCamel(method.Name), method.Resolved.ReplayDepth, method.Resolved.IdleSecs)
		}
		handlerInterface.WriteString(generateJavaHandlerMethod(method, inputType, outputType, crossPkgOuterClasses))
		effectiveAuthAnyUser := method.AuthAnyUser
		grpcMethods.WriteString(generateJavaGRPCWrapper(method, serviceName, svc.GrpcName, inputType, outputType, inputHelpers, outputHelpers, effectiveAuthAnyUser, method.AuthDisabled || svc.AuthDisabled || svc.IsC2D, accountingMap, info.Package, svc.ParentDir, info.CrossPackageTypes, skippedTraceTypes, san, crossPkgHelpers, crossPkgOuterClasses))
		grpcMethods.WriteString("\n")

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
				method.Name, strings.Join(quoted, ", ")))
		}

		// Register request-field target_namespace_field for level-or-below enforcement.
		// auth_any_user uses target_namespace_field as a fixed namespace literal instead.
		if method.TargetNamespaceField != "" && !effectiveAuthAnyUser {
			methodAuthLines = append(methodAuthLines, fmt.Sprintf(
				"        authorizer.setMethodTargetNamespaceField(%q, %q);",
				method.Name, method.TargetNamespaceField))
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
		return fmt.Errorf("failed to load service wrapper template: %w", err)
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
	}

	t, err := template.New("servicewrapper").Parse(tmpl)
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	if err := t.Execute(file, data); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	return nil
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
	methodName := strutil.ToCamelCase(strutil.ToSnakeCase(name))
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
	methodName := strutil.ToCamelCase(strutil.ToSnakeCase(method.Name))
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

func generateJavaGRPCWrapper(method *parser.MethodInfo, serviceName, grpcName, inputType, outputType, inputHelpers, outputHelpers string, authAnyUser bool, authDisabled bool, accountingMap map[string]service.AccountingInfo, servicePackage, parentDir string, crossPkgTypes map[string]string, skippedTraceTypes proto.SkippedTraceTypes, san *javaEgressSanitizer, crossPkgHelpers map[string]string, crossPkgOuterClasses map[string]string) string {
	methodName := strutil.ToCamelCase(strutil.ToSnakeCase(method.Name))
	// Ensure first letter is lowercase for Java method convention
	if len(methodName) > 0 {
		methodName = strings.ToLower(methodName[:1]) + methodName[1:]
	}
	emptyInput := method.FullInputType == "google.protobuf.Empty"
	handlerEmpty := emptyInput || method.FullInputType == "trace.TraceRequest"

	// Full method name for authorization (e.g. "SnoopyService/GetAtomic")
	fullMethod := fmt.Sprintf("%s/%s", serviceName, method.Name)
	authAnyUserTarget := strings.TrimSpace(method.TargetNamespaceField)
	if authAnyUserTarget == "" {
		authAnyUserTarget = "/"
	}

	// Authorization check (before validation). auth_disabled methods emit NO check —
	// they are reachable without a token by proto annotation (e.g. they verify a
	// cred_pair in the body). Every other method always enforces.
	var authCheck string
	if authDisabled {
		authCheck = ""
	} else if authAnyUser {
		authCheck = fmt.Sprintf(`        // Check authentication (auth_any_user: fixed namespace target)
        try {
            checkAuthenticate(%q);
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
            return;
        }`, authAnyUserTarget)
	} else {
		authCheck = fmt.Sprintf(`        // Check authorization
        try {
            checkAuth("%s", request);
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
            return;
        }`, fullMethod)
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
            boolean isNoop = isTracing && request.getNoop();`
		} else if authAnyUser {
			// Auth runs unconditionally: noop skips the handler but not auth.
			traceAuthCheck = fmt.Sprintf(`            boolean isTracing = !request.getTraceId().isEmpty();
            boolean isNoop = isTracing && request.getNoop();
            // Check authentication (auth_any_user: fixed namespace target)
            try {
                checkAuthenticate(%q);
            } catch (ProtoException e) {
                responseObserver.onError(toGrpcError(e));
                return;
            }`, authAnyUserTarget)
		} else {
			// Auth runs unconditionally: noop skips the handler but not auth.
			traceAuthCheck = fmt.Sprintf(`            boolean isTracing = !request.getTraceId().isEmpty();
            boolean isNoop = isTracing && request.getNoop();
            // Check authorization
            try {
                checkAuth("%s", request);
            } catch (ProtoException e) {
                responseObserver.onError(toGrpcError(e));
                return;
            }`, fullMethod)
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
	streamResponseObserver := "responseObserver"
	if outputHelpers != "" && !emptyOutput {
		streamResponseObserver = fmt.Sprintf("validatingResponseObserver(responseObserver, value -> %s.validate%s(value))", outputHelpers, method.OutputType)
	}
	needsStreamMessageAuth := method.ClientStreaming && method.TargetNamespaceField != "" && !authDisabled && !authAnyUser
	clientStreamInputObserver := func(handlerObserverExpr, responseObserverExpr string) string {
		if !needsStreamMessageAuth && (inputHelpers == "" || handlerEmpty) {
			return handlerObserverExpr
		}
		var checks []string
		if needsStreamMessageAuth {
			checks = append(checks, fmt.Sprintf("checkAuth(%q, value);", fullMethod))
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
		pageSan := ""
		pageItemsExpr := "__page.items"
		if t, ok := san.sanitizableType(method.Resolved.PageItemType, servicePackage, crossPkgHelpers); ok {
			pageSan = javaEgressSanitizePreamble([]javaSanTarget{t}) + fmt.Sprintf(`            java.util.List<%s> __sanitizedItems = new java.util.ArrayList<>(__page.items.size());
            for (%s __m : __page.items) { __sanitizedItems.add(%s); }
`, itemType, itemType, t.sanitizeExpr("__m"))
			pageItemsExpr = "__sanitizedItems"
		}
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
            Caller caller = null;
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
                        new java.util.HashMap<>(), requestId);
                }
                throw handlerEx;
            }
            long durationMs = System.currentTimeMillis() - startTime;
            AccountingManager.Emitter em = this.emitter;
            if (em != null) {
                AccountingMethod accountingMethod = %s;
                em.rpcCompleted(accountingMethod, durationMs, new java.util.HashMap<>(), requestId);
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
            Caller caller = null;
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
			outputValidation = unarySan + outputValidation
		}

		handlerCall := fmt.Sprintf("handler.%s(caller, request)", methodName)
		if handlerEmpty {
			handlerCall = fmt.Sprintf("handler.%s(caller)", methodName)
		}

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
            Caller caller = null;
            logger.debug("rpc method={} request_id={}", "%s", requestId);
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
                        new java.util.HashMap<>(), requestId);
                }
                throw handlerEx;
            }
            long durationMs = System.currentTimeMillis() - startTime;
            AccountingManager.Emitter em = this.emitter;
            if (em != null) {
                AccountingMethod accountingMethod = %s;
                em.rpcCompleted(accountingMethod, durationMs, new java.util.HashMap<>(), requestId);
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
					fullMethod,
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
            Caller caller = null;
            logger.debug("rpc method={} request_id={}", "%s", requestId);
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
				fullMethod,
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
            Caller caller = null;
            logger.debug("rpc method={} request_id={}", "%s", requestId);
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
                        new java.util.HashMap<>(), requestId);
                }
                throw handlerEx;
            }
            long durationMs = System.currentTimeMillis() - startTime;
            AccountingManager.Emitter em = this.emitter;
            if (em != null) {
	                AccountingMethod accountingMethod = %s;
	                em.rpcCompleted(accountingMethod, durationMs, new java.util.HashMap<>(), requestId);
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
				fullMethod,
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
            Caller caller = null;
            logger.debug("rpc method={} request_id={}", "%s", requestId);
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
			fullMethod,
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
            Caller caller = null;
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
                        new java.util.HashMap<>(), requestId);
                }
                throw handlerEx;
            }
%s
            long durationMs = System.currentTimeMillis() - startTime;
            AccountingManager.Emitter em = this.emitter;
            if (em != null) {
                AccountingMethod accountingMethod = %s;
                em.rpcCompleted(accountingMethod, durationMs, new java.util.HashMap<>(), requestId);
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
            Caller caller = null;
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
            Caller caller = null;
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
                        new java.util.HashMap<>(), requestId);
                }
                throw handlerEx;
            }
%s
            long durationMs = System.currentTimeMillis() - startTime;
            AccountingManager.Emitter em = this.emitter;
            if (em != null) {
                AccountingMethod accountingMethod = %s;
                em.rpcCompleted(accountingMethod, durationMs, new java.util.HashMap<>(), requestId);
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
            Caller caller = null;
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
                            em.rpcCompleted(accountingMethod, durationMs, new java.util.HashMap<>(), requestId);
                        }`, accountingMethodExpr)
			watchErrorAccounting = fmt.Sprintf(`
                        long durationMs = System.currentTimeMillis() - startTime;
                        AccountingManager.Emitter em = this.emitter;
                        if (em != null) {
                            AccountingMethod accountingMethod = %s;
                            io.grpc.Status.Code code = AccountingManager.grpcCodeFromError(t);
                            em.rpcError(accountingMethod, code.value(), t.getMessage(), durationMs,
                                AccountingManager.errorContextFromError(t, AccountingManager.shouldCaptureStack(code)),
                                new java.util.HashMap<>(), requestId);
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
		watchDrive := fmt.Sprintf(`%s            final CollectionRuntime.WatchSource<%s, %s> __source = %s;
            final long __fromRevision = %s;
            final long __resumeRevision = __fromRevision == 0 ? __source.revision : __fromRevision;
            final java.util.Iterator<%s> __changes = %sWatchHub.watch(
                    __source.scope,
                    __resumeRevision,
                    __source.changes,
                    change -> change.getRevision());
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
            __watchThread.start();`, watchPre, itemType, changeType, watchHandlerCall, fromRevisionExpr, changeType, methodName, outputType, outputType, outputType, streamResponseObserver, outputType, outputType, watchItemExpr, outputType, outputType, watchChangeExpr, watchCompletedAccounting, watchErrorAccounting, methodName)

		if hasAccounting {
			return fmt.Sprintf(`    @Override
    public void %s(%s request, StreamObserver<%s> responseObserver) {
        try {
%s
%s
            String requestId = Auth.requestIdFromContext();
            Caller caller = null;
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
            Caller caller = null;
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
            Caller caller = null;
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
                        new java.util.HashMap<>(), requestId);
                }
                throw handlerEx;
            }
            long durationMs = System.currentTimeMillis() - startTime;
            AccountingManager.Emitter em = this.emitter;
            if (em != null) {
                AccountingMethod accountingMethod = %s;
                em.rpcCompleted(accountingMethod, durationMs, new java.util.HashMap<>(), requestId);
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
            Caller caller = null;
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
			authCheckNoReq = ""
		} else if authAnyUser {
			authCheckNoReq = fmt.Sprintf(`        // Check authentication (auth_any_user: fixed namespace target)
        try {
            checkAuthenticate(%q);
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
            return new StreamObserver<%s>() {
                @Override public void onNext(%s value) {}
                @Override public void onError(Throwable t) {}
                @Override public void onCompleted() {}
            };
        }`, authAnyUserTarget, inputType, inputType)
		} else if needsStreamMessageAuth {
			authCheckNoReq = fmt.Sprintf(`        // Check authentication before per-message target namespace authorization
        try {
            checkAuthenticate("/");
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
        try {
            checkAuth("%s", null);
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
            return new StreamObserver<%s>() {
                @Override public void onNext(%s value) {}
                @Override public void onError(Throwable t) {}
                @Override public void onCompleted() {}
            };
        }`, fullMethod, inputType, inputType)
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
        Caller caller = null;
        logger.debug("rpc method={} request_id={}", "%s", requestId);
        long startTime = System.currentTimeMillis();
        AccountingMethod accountingMethod = %s;
        java.util.concurrent.atomic.AtomicBoolean __accounted = new java.util.concurrent.atomic.AtomicBoolean(false);
        StreamObserver<%s> __accountingResponseObserver =
                accountingResponseObserver(responseObserver, accountingMethod, startTime, requestId, __accounted);
        try {
            StreamObserver<%s> __handlerObserver = %s;
            return accountingRequestObserver(__handlerObserver, accountingMethod, startTime, requestId, __accounted);
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
	        Caller caller = null;
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
		var authCheckNoReq string
		if authDisabled {
			authCheckNoReq = ""
		} else if authAnyUser {
			authCheckNoReq = fmt.Sprintf(`        // Check authentication (auth_any_user: fixed namespace target)
        try {
            checkAuthenticate(%q);
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
            return new StreamObserver<%s>() {
                @Override public void onNext(%s value) {}
                @Override public void onError(Throwable t) {}
                @Override public void onCompleted() {}
            };
        }`, authAnyUserTarget, inputType, inputType)
		} else if needsStreamMessageAuth {
			authCheckNoReq = fmt.Sprintf(`        // Check authentication before per-message target namespace authorization
        try {
            checkAuthenticate("/");
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
        try {
            checkAuth("%s", null);
        } catch (ProtoException e) {
            responseObserver.onError(toGrpcError(e));
            return new StreamObserver<%s>() {
                @Override public void onNext(%s value) {}
                @Override public void onError(Throwable t) {}
                @Override public void onCompleted() {}
            };
        }`, fullMethod, inputType, inputType)
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
        Caller caller = null;
        logger.debug("rpc method={} request_id={}", "%s", requestId);
        long startTime = System.currentTimeMillis();
        AccountingMethod accountingMethod = %s;
        java.util.concurrent.atomic.AtomicBoolean __accounted = new java.util.concurrent.atomic.AtomicBoolean(false);
        StreamObserver<%s> __accountingResponseObserver =
                accountingResponseObserver(responseObserver, accountingMethod, startTime, requestId, __accounted);
        try {
            StreamObserver<%s> __handlerObserver = %s;
            return accountingRequestObserver(__handlerObserver, accountingMethod, startTime, requestId, __accounted);
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
	        Caller caller = null;
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
