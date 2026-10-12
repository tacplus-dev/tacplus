// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.uber.org/zap"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/packages"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/registry"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
)

// buildNpmPackageName constructs a valid npm package name from a base package and path components.
// For scoped packages (starting with @) without a slash, uses "/" as the first separator:
//   - @myclient-proto + [sys, alpn] → @myclient-proto/sys-alpn
//
// For scoped packages that already contain "/", uses "-":
//   - @example/controlplane-proto + [sys, alpn] → @example/controlplane-proto-sys-alpn
//
// For unscoped packages, uses "-":
//   - mypackage + [sys, alpn] → mypackage-sys-alpn
func buildNpmPackageName(basePackage string, parts ...string) string {
	suffix := strings.Join(parts, "-")
	if strings.HasPrefix(basePackage, "@") && !strings.Contains(basePackage, "/") { // npm scoped package without org separator
		return basePackage + "/" + suffix
	}
	return basePackage + "-" + suffix
}

// typescriptModuleDirForProto returns the output package directory for a proto.
// Keep this in one place so generated files and package manifests cannot drift.
func typescriptModuleDirForProto(outputDir, protoFile string) string {
	return typescriptModuleDirForProtoFeatures(outputDir, protoFile, false, false)
}

func typescriptModuleDirForProtoFile(outputDir string, pf model.ProtoFile) string {
	return typescriptModuleDirForProtoFeatures(outputDir, pf.RelPath, proto.DeclaresServiceStateModel(pf), false)
}

func typescriptModuleDirForProtoDescriptor(outputDir, protoFile string, fileDesc *parser.FileDescriptor) string {
	return typescriptModuleDirForProtoFeatures(outputDir, protoFile, proto.DeclaresServiceState(fileDesc), proto.IsSyntheticServiceOnlyFile(fileDesc))
}

func typescriptModuleDirForProtoFeatures(outputDir, protoFile string, declaresServiceState, syntheticServiceOnly bool) string {
	subdir := filepath.Dir(protoFile)
	filename := strings.TrimSuffix(filepath.Base(protoFile), ".proto")
	normalized := filepath.ToSlash(protoFile)

	if strings.HasPrefix(normalized, "sys/") {
		return filepath.Join(outputDir, "types", "sys", filename)
	}
	if svcName, fileName := proto.ServiceProtoParts(protoFile); svcName != "" && (declaresServiceState || syntheticServiceOnly || proto.IsServicesFileName(fileName)) {
		return filepath.Join(outputDir, "types", "services", svcName, "services")
	}
	return filepath.Join(outputDir, "types", subdir, filename)
}

func typeScriptServiceStateFileMap(protoFiles []model.ProtoFile) map[string]bool {
	files := make(map[string]bool)
	for _, pf := range protoFiles {
		if proto.DeclaresServiceStateModel(pf) {
			files[filepath.ToSlash(pf.RelPath)] = true
		}
	}
	return files
}

func typeScriptServiceStateFile(serviceStateFiles map[string]bool, protoFile string) bool {
	if len(serviceStateFiles) == 0 {
		return false
	}
	return serviceStateFiles[filepath.ToSlash(protoFile)]
}

func typeScriptNpmPackageNameForProto(basePackage, protoFile string, serviceStateFiles map[string]bool) string {
	moduleDir := filepath.ToSlash(typescriptModuleDirForProtoFeatures("", protoFile, typeScriptServiceStateFile(serviceStateFiles, protoFile), false))
	parts := strings.Split(moduleDir, "/")
	if len(parts) > 0 && parts[0] == "types" {
		parts = parts[1:]
	}
	if len(parts) == 0 {
		return basePackage
	}
	if len(parts) <= 1 {
		return buildNpmPackageName(basePackage, parts...)
	}
	return buildNpmPackageName(basePackage, strings.ReplaceAll(strings.Join(parts[:len(parts)-1], "/"), "/", "-"), parts[len(parts)-1])
}

var tsGeneratedPackageRe = regexp.MustCompile(`@generated from file [^(]+\s+\(package ([^,\s)]+),`)

// typescriptModuleDirForPackage resolves a proto package to the generated TypeScript
// module directory, relative to outputDir. It reads the generated _pb.ts headers
// because package names do not always match proto file slugs (for example
// vservices/claude-sensor.proto has package claudesensor).
func typescriptModuleDirForPackage(outputDir, packageName string) (string, bool) {
	if outputDir == "" || packageName == "" {
		return "", false
	}

	for _, root := range []string{"types"} {
		rootDir := filepath.Join(outputDir, root)
		if _, err := os.Stat(rootDir); os.IsNotExist(err) {
			continue
		}

		var found string
		err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !strings.HasSuffix(path, "_pb.ts") {
				return err
			}
			content, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			match := tsGeneratedPackageRe.FindStringSubmatch(string(content))
			if len(match) < 2 || match[1] != packageName {
				return nil
			}
			rel, relErr := filepath.Rel(outputDir, filepath.Dir(path))
			if relErr != nil {
				return relErr
			}
			found = filepath.ToSlash(rel)
			return filepath.SkipAll
		})
		if err != nil && err != filepath.SkipAll {
			continue
		}
		if found != "" {
			return found, true
		}
	}

	return "", false
}

func typescriptModuleDirForPackageOrDir(outputDir, packageName, protoDir string) string {
	if dir, ok := typescriptModuleDirForPackage(outputDir, packageName); ok {
		return dir
	}

	protoDir = filepath.ToSlash(protoDir)
	if protoDir == "" || protoDir == "." {
		return filepath.Join("types", "services", packageName)
	}
	if strings.HasPrefix(protoDir, "sys") {
		return filepath.Join("types", "sys", packageName)
	}
	return filepath.Join("types", protoDir, packageName)
}

func validateTypeScriptClientOnlyConfig(config *tactype.Config) error {
	var reasons []string
	if config.GrpcS2SClientAll || len(config.GrpcS2SClients) > 0 {
		reasons = append(reasons, "S2S client generation")
	}
	if config.GrpcServerAll || len(config.GrpcServers) > 0 {
		reasons = append(reasons, "server generation")
	}
	if config.AllStates || len(config.States) > 0 {
		reasons = append(reasons, "ServiceManager/state generation")
	}
	return typeScriptClientOnlyError(reasons)
}

func validateTypeScriptClientOnlyAnnotations(config *tactype.Config, protoDir string) error {
	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return fmt.Errorf("failed to parse protos for typescript client-only validation: %w", err)
	}

	files := dp.GetAllFiles()
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var reasons []string
	for _, path := range paths {
		fd := files[path]
		if !typeScriptClientOnlyFileInScope(config, fd) {
			continue
		}

		if len(fd.GrpcS2SClients) > 0 || fd.AllGrpcS2SClients {
			reasons = append(reasons, fmt.Sprintf("S2S client generation from %s", path))
		}
		if len(fd.StateImports) > 0 {
			reasons = append(reasons, fmt.Sprintf("ServiceManager/state import generation from %s", path))
		}
		if len(fd.OpaImports) > 0 || len(fd.OpaImportTargets) > 0 {
			reasons = append(reasons, fmt.Sprintf("S2S virtual-client generation from %s", path))
		}
		if fd.ServiceStore || fd.ServiceGateway || fd.ServiceRestDispatch {
			reasons = append(reasons, fmt.Sprintf("service runtime generation from (spec.service) in %s", path))
		}
		if fd.TimeseriesStore || fd.RecordStore {
			reasons = append(reasons, fmt.Sprintf("EventBus/store service generation from %s", path))
		}

		for _, svc := range fd.Services {
			label := fd.Package + "." + svc.Name
			if svc.IsS2S {
				reasons = append(reasons, fmt.Sprintf("server generation from (spec.service_s2s) on %s", label))
			}
			if svc.IsSMB {
				reasons = append(reasons, fmt.Sprintf("SMB service-side generation from %s", label))
			}
			if svc.IsSMBAuthority {
				reasons = append(reasons, fmt.Sprintf("SMB authority generation from %s", label))
			}
			// A bare (authoritative, S2S) token minter needs server-side
			// generation a client cannot emit — reject it. A {proxy: true}
			// minter is the client-reachable (C2S) mint FRONT: client_im mints
			// through it as an ordinary C2S client (the webapp path), so it is
			// client-safe. C2D minters are also client-side.
			if svc.IsTokenMinter && !svc.IsC2D && !svc.TokenMinterProxy {
				reasons = append(reasons, fmt.Sprintf("S2S token-minter generation from %s", label))
			}
		}

		for _, msg := range fd.Messages {
			label := fd.Package + "." + msg.Name
			for ann := range msg.Annotations {
				switch ann {
				case "service_state", "service_event", "grpc_config", "per_service_config", "config_slice_scope", "config_slice_service":
					reasons = append(reasons, fmt.Sprintf("ServiceManager/state generation from %s on %s", ann, label))
				case "service_store":
					reasons = append(reasons, fmt.Sprintf("service_store persistence generation from %s", label))
				case "service_health":
					reasons = append(reasons, fmt.Sprintf("health generation from %s", label))
				case "subscribe", "event_watch", "event_publish", "record":
					reasons = append(reasons, fmt.Sprintf("EventBus generation from %s on %s", ann, label))
				}
			}
		}
	}
	return typeScriptClientOnlyError(reasons)
}

func typeScriptClientOnlyFileInScope(config *tactype.Config, fd *parser.FileDescriptor) bool {
	if strings.HasPrefix(filepath.ToSlash(fd.Path), "sys/") {
		return false
	}
	if config.SelfPackage == "" {
		return true
	}
	if fd.Package == config.SelfPackage {
		return true
	}
	for _, svc := range fd.Services {
		if svc.HostLabel == config.SelfPackage {
			return true
		}
	}
	return false
}

func typeScriptClientOnlyError(reasons []string) error {
	if len(reasons) == 0 {
		return nil
	}
	reasons = uniqueSortedStrings(reasons)
	return fmt.Errorf("typescript is a client-only target; only client generation is supported. Remove %s or use a full-service target (go, rust, java)", strings.Join(reasons, "; "))
}

func uniqueSortedStrings(values []string) []string {
	sort.Strings(values)
	result := values[:0]
	for _, value := range values {
		if len(result) == 0 || result[len(result)-1] != value {
			result = append(result, value)
		}
	}
	return result
}

// Generate generates TypeScript code from proto files
// Creates separate npm modules for each proto package
func Generate(config *tactype.Config) error {
	if err := validateTypeScriptClientOnlyConfig(config); err != nil {
		return err
	}

	// Resolve protoc paths
	if err := config.ResolveProtocPaths(); err != nil {
		return err
	}

	// Delete the entire output directory to ensure a clean slate (no stale files)
	if _, err := os.Stat(config.OutputDir); err == nil {
		if err := os.RemoveAll(config.OutputDir); err != nil {
			return fmt.Errorf("failed to remove existing output directory: %w", err)
		}
	}

	// Copy proto files into output for reference (uses embedded spec protos).
	// Use individual steps instead of CopyProtoDir so we can detect services
	// AFTER injection but BEFORE stripping spec annotations.
	protoSrcDir, err := proto.CopyAndPrepareProtoDir(config.InputDir, config.OutputDir)
	if err != nil {
		return fmt.Errorf("failed to copy proto directory: %w", err)
	}
	if err := validateTypeScriptClientOnlyAnnotations(config, protoSrcDir); err != nil {
		return err
	}
	if err := proto.AppendEventBusWrappersToProtos(protoSrcDir, config.FlatProtoDir); err != nil {
		return fmt.Errorf("failed to append eventbus topic wrappers to protos: %w", err)
	}
	if err := proto.AppendRoutingEnvelopesToProtos(protoSrcDir, config.FlatProtoDir); err != nil {
		return fmt.Errorf("failed to append routing envelopes to protos: %w", err)
	}
	if proto.NeedsEventBusSupportProto(protoSrcDir) {
		if _, err := proto.EnsureEventBusSupportProto(protoSrcDir, proto.EventBusSupportProtoOptions{}); err != nil {
			return fmt.Errorf("failed to generate eventbus support proto: %w", err)
		}
	}

	// Generate virtual service protos into generated/.
	if _, err := proto.GenerateOpaEvalProto(protoSrcDir, config.FlatProtoDir); err != nil {
		return fmt.Errorf("failed to generate OpaEval virtual service proto: %w", err)
	}

	if err := proto.ValidateVirtualImports(config.FlatProtoDir); err != nil {
		return err
	}
	if err := proto.ValidateRecordLinkage(config.FlatProtoDir); err != nil {
		return err
	}

	// Auto-derive virtual service clients from proto annotations.
	virtualClients, err := proto.DeriveVirtualServiceClients(config.FlatProtoDir, config.SelfPackage)
	if err != nil {
		return fmt.Errorf("failed to derive virtual service clients: %w", err)
	}

	// Infer gRPC client dependencies from grpc_s2s_client/c2s/w2s proto annotations.
	if err := service.InferGrpcClientsFromAnnotations(config, protoSrcDir); err != nil {
		return fmt.Errorf("failed to infer gRPC clients from proto annotations: %w", err)
	}
	// Derived clients are unioned in AFTER inference, not appended before it:
	// inference skips its explicit-declaration block when GrpcS2SClients is
	// already non-empty, so a service declaring both (spec.record_read) and an
	// explicit (spec.grpc_s2s_client) used to lose the explicit one silently.
	config.GrpcS2SClients = service.MergeGrpcEntries(config.GrpcS2SClients, virtualClients)

	// Inject per-service ConfigState messages into each service's own proto file.
	feConfigs, err := proto.InjectGrpcConfigState(config.InputDir, protoSrcDir, config.FlatProtoDir)
	if err != nil {
		return fmt.Errorf("failed to inject gRPC server config state: %w", err)
	}
	if err := proto.ResolveStatesFromProto(config, config.FlatProtoDir); err != nil {
		return err
	}
	proto.InjectConfigStates(config, feConfigs)
	if err := proto.InjectHealthState(config, config.FlatProtoDir); err != nil {
		return fmt.Errorf("failed to inject health state: %w", err)
	}

	// Append state/store service definitions BEFORE trace injection so that
	// auto-appended message types also receive trace fields.
	// Inject per-field "<field>_redacted" indicator bools before native compilation
	// so they appear in every language's generated type.
	if err := proto.InjectRedactIndicatorFields(protoSrcDir, config.FlatProtoDir); err != nil {
		return fmt.Errorf("failed to inject redact indicator fields: %w", err)
	}
	// <T>Public for every (spec.public_view) message: its SHOW fields only.
	if err := proto.AppendPublicViewMessagesToProtos(protoSrcDir); err != nil {
		return fmt.Errorf("failed to append public view messages: %w", err)
	}
	if err := proto.AppendEntitlementMessagesToProtos(protoSrcDir, config.FlatProtoDir); err != nil {
		return fmt.Errorf("failed to append entitlement messages: %w", err)
	}
	if err := proto.AppendStateServiceToProtos(protoSrcDir, protoSrcDir); err != nil {
		return fmt.Errorf("failed to append state service to protos: %w", err)
	}
	if err := proto.AppendStoreServiceToProtos(protoSrcDir, config.FlatProtoDir); err != nil {
		return fmt.Errorf("failed to append store service to protos: %w", err)
	}
	if err := proto.GenerateTransformationMessages(protoSrcDir, config.FlatProtoDir); err != nil {
		return fmt.Errorf("failed to generate projection messages: %w", err)
	}
	if err := proto.AppendRecordMapServiceToProtos(protoSrcDir, config.FlatProtoDir, config.SelfPackage); err != nil {
		return fmt.Errorf("failed to append record map service to protos: %w", err)
	}
	// The CLIENT-FACING half, after the record plane above because it folds the
	// same criteria and must see the same records. Emits nothing unless a
	// service declares (spec.record_read).c2s.
	if err := proto.AppendRecordC2SServiceToProtos(protoSrcDir, config.FlatProtoDir, config.SelfPackage); err != nil {
		return fmt.Errorf("failed to append client-facing record read plane to protos: %w", err)
	}
	// TimeseriesGroupCounts / TimeseriesQuery injection removed 2026-08-25 with
	// the timedb boneyard (BIG GOAL 3, first move #1) — no timeseries_store
	// exists in any proto set, so both could only no-op, and neither ever had a
	// consumer.
	// Auto-register virtual service gRPC servers based on proto annotations.
	if isOpaServer, err := proto.IsOpaServer(config.FlatProtoDir, config.SelfPackage); err != nil {
		return fmt.Errorf("failed to check OPA server status: %w", err)
	} else if isOpaServer {
		config.GrpcServers = append(config.GrpcServers, tactype.GrpcEntry{
			ProtoFile:   "generated/opa_eval.proto",
			ServiceName: "OpaEval",
		})
	}
	// Inject trace round fields into all message types.
	// Parse from protoSrcDir so appended types are visible.
	if _, err := proto.InjectTraceFields(protoSrcDir, protoSrcDir); err != nil {
		return fmt.Errorf("failed to inject trace fields: %w", err)
	}
	if err := proto.SwapEmptyInputs(protoSrcDir); err != nil {
		return fmt.Errorf("failed to swap empty inputs to TraceRequest: %w", err)
	}

	// Generate generated/*.proto from grpc_config annotations. Must happen before
	// proto codegen so accounting_method/services contain the real RPC enums.
	_, svcRPCs, vsEntries, err := proto.ScanAndGenerateConfigHolder(config.InputDir, protoSrcDir)
	if err != nil {
		return fmt.Errorf("failed to generate generated/*.proto: %w", err)
	}

	// protoSrcDir now has injected services + annotations (not yet stripped)

	// Create builder temp directory for intermediate files
	builderTempDir, err := os.MkdirTemp("", "tacplus-builder-")
	if err != nil {
		return fmt.Errorf("failed to create builder temp directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(builderTempDir); err != nil {
			zap.L().Warn("failed to remove builder temp directory", zap.String("path", builderTempDir), zap.Error(err))
		}
	}()

	// Parse all proto files and generate consolidated helpers.ts files.
	// Uses protoSrcDir (which contains generated kitchen_sink.proto) so all protos get helpers.
	zap.L().Info("Parsing proto files and generating helpers...")
	allParsedFiles, err := processProtoFiles(protoSrcDir, builderTempDir)
	if err != nil {
		return fmt.Errorf("failed to process proto files: %w", err)
	}
	serviceStateFiles := typeScriptServiceStateFileMap(allParsedFiles)

	// Generate package-specific helper files (token, errors, etc.)
	zap.L().Info("Generating package-specific helpers...")
	if err := generatePackageHelpers(protoSrcDir, builderTempDir); err != nil {
		return fmt.Errorf("failed to generate package helpers: %w", err)
	}

	zap.L().Info("Generating TypeScript code")

	// Collect proto files from the split copy, skipping services/{name}/services.proto files
	// (protoc-gen-es only generates code for messages/enums, not service stubs)
	protoFiles, err := collectProtoFiles(protoSrcDir)
	if err != nil {
		return err
	}

	// Generate TypeScript code for each proto file
	for _, protoFile := range protoFiles {
		if err := generateTypeScriptModule(config, protoSrcDir, protoFile, serviceStateFiles); err != nil {
			return fmt.Errorf("failed to generate TypeScript for %s: %w", protoFile, err)
		}
	}

	// NOTE: the former Connect-RPC *_grpc.ts stub step is GONE. It shelled out to
	// protoc-gen-connect-es — a Connect-ES v1-only plugin absent from every image
	// (the stack pins protobuf-es v2, which removed the plugin) — so TS C2S
	// generation could never succeed anywhere. Nothing consumed the stubs: the
	// typed client wrappers call @grpc/grpc-js directly with protobuf-es
	// serialization, and the index builder excluded *_grpc.ts because protoc-gen-es
	// v2 already exports the service descriptors from *_pb.ts.

	// Copy generated helper files from builder
	zap.L().Info("Copying generated helper files")
	if err := copyHelperFiles(builderTempDir, config.OutputDir); err != nil {
		return fmt.Errorf("failed to copy helper files: %w", err)
	}

	// Copy utils directory with error helpers and create structural directories
	zap.L().Info("Copying utils directory")
	if err := copyUtilsDirectory(config.OutputDir); err != nil {
		return fmt.Errorf("failed to copy utils directory: %w", err)
	}

	// Generate utils files (utils.ts, validation.ts)
	zap.L().Info("Generating TypeScript utils")
	if err := generateTypeScriptUtils(config); err != nil {
		return fmt.Errorf("failed to generate utils: %w", err)
	}

	// Create types/sys/spec/ with a minimal index.ts
	// (spec.proto is stripped during proto processing but the package still needs to exist)
	zap.L().Info("Creating sys/spec")
	if err := createSpecPackage(config.OutputDir); err != nil {
		return fmt.Errorf("failed to create spec package: %w", err)
	}

	// Fix import paths in generated TypeScript files
	zap.L().Info("Fixing import paths")
	if err := fixImportPaths(config.OutputDir, serviceStateFiles); err != nil {
		return fmt.Errorf("failed to fix import paths: %w", err)
	}

	// Generate service wrappers for services.
	// Use protoSrcDir (has injected state services + annotations) to detect
	// both gRPC services and injected state services.
	zap.L().Info("Generating service wrappers")
	servicesInfo, err := detectServicesForWrapper(config, protoSrcDir)
	if err != nil {
		return fmt.Errorf("failed to detect services: %w", err)
	}
	if len(servicesInfo) > 0 {
		// Generate service wrappers — only for gRPC server services
		serverServices := service.ResolveServerServices(config, servicesInfo)
		if len(serverServices) > 0 {
			zap.L().Info("Generating TypeScript service wrappers", zap.Int("count", len(serverServices)))
			accountingMap := make(map[string]service.AccountingInfo, len(svcRPCs))
			for _, svc := range svcRPCs {
				rpcs := make(map[string]bool, len(svc.RPCs))
				ordinals := make(map[string]int, len(svc.RPCs))
				for i, rpc := range svc.RPCs {
					rpcs[rpc] = true
					ordinals[rpc] = i + 1
				}
				accountingMap[svc.GrpcName] = service.AccountingInfo{EnumName: svc.EnumName, RPCs: rpcs, RPCOrdinals: ordinals}
			}
			if _, err := generateServiceWrappers(serverServices, protoSrcDir, accountingMap); err != nil {
				return fmt.Errorf("failed to generate service wrappers: %w", err)
			}
			if err := generateServicesPackageJson(serverServices); err != nil {
				return fmt.Errorf("failed to generate services package.json: %w", err)
			}
		}
	}

	// Generate gRPC client wrappers — typed client stubs for gRPC client services
	var s2sClientServices []service.ServiceInfo
	if len(servicesInfo) > 0 {
		s2sClientServices = service.ResolveClientServices(config, servicesInfo)
		if len(s2sClientServices) > 0 {
			zap.L().Info("Generating TypeScript gRPC client wrappers", zap.Int("count", len(s2sClientServices)))
			if err := generateGrpcClients(s2sClientServices, protoSrcDir); err != nil {
				return fmt.Errorf("failed to generate gRPC clients: %w", err)
			}
		}
	}

	// Generate c2sm (client-to-service) gRPC client wrappers
	if len(servicesInfo) > 0 {
		c2sServices := service.ResolveC2SClientServices(config, servicesInfo)
		if len(c2sServices) > 0 {
			origPrefix := config.SmPrefix
			config.SmPrefix = "c2sm"
			zap.L().Info("Generating TypeScript c2s gRPC client wrappers", zap.Int("count", len(c2sServices)))
			if err := generateGrpcClients(c2sServices, protoSrcDir); err != nil {
				config.SmPrefix = origPrefix
				return fmt.Errorf("failed to generate c2s gRPC clients: %w", err)
			}
			if err := generateC2SServiceManager(config, c2sServices); err != nil {
				config.SmPrefix = origPrefix
				return fmt.Errorf("failed to generate c2s service manager: %w", err)
			}
			config.SmPrefix = origPrefix
		}
	}

	// Generate c2dm (client-to-daemon) gRPC client wrappers when the proto graph
	// declares in-scope service_c2d services (or an explicit GrpcC2DClients allowlist).
	if c2dServices := service.ResolveC2DClientServices(config, servicesInfo); len(c2dServices) > 0 {
		origPrefix := config.SmPrefix
		config.SmPrefix = "c2dm"
		zap.L().Info("Generating TypeScript c2d gRPC client wrappers", zap.Int("count", len(c2dServices)))
		if err := generateGrpcClients(c2dServices, protoSrcDir); err != nil {
			config.SmPrefix = origPrefix
			return fmt.Errorf("failed to generate c2d gRPC clients: %w", err)
		}
		if err := generateC2DServiceManager(config, c2dServices); err != nil {
			config.SmPrefix = origPrefix
			return fmt.Errorf("failed to generate c2d service manager: %w", err)
		}
		config.SmPrefix = origPrefix
	}

	// Generate REST client wrappers — typed REST stubs for gRPC client services
	if len(servicesInfo) > 0 {
		restClientServices := service.ResolveRestClientServices(config, servicesInfo)
		if len(restClientServices) > 0 {
			zap.L().Info("Generating TypeScript REST client wrappers", zap.Int("count", len(restClientServices)))
			if err := generateRestClients(restClientServices, protoSrcDir); err != nil {
				return fmt.Errorf("failed to generate REST clients: %w", err)
			}
		}
	}

	// Generate gRPC-over-REST client wrappers — same API as gRPC clients but via REST transport
	if len(servicesInfo) > 0 {
		restClientServices := service.ResolveRestClientServices(config, servicesInfo)
		if len(restClientServices) > 0 {
			zap.L().Info("Generating TypeScript gRPC-over-REST client wrappers", zap.Int("count", len(restClientServices)))
			if err := generateGrpcOverRestClients(restClientServices, protoSrcDir); err != nil {
				return fmt.Errorf("failed to generate gRPC-over-REST clients: %w", err)
			}
		}
	}

	// Generate w2sm (web-to-service) gRPC-over-REST client wrappers
	var w2sServices []service.ServiceInfo
	if len(servicesInfo) > 0 {
		w2sServices = service.ResolveW2SClientServices(config, servicesInfo)
		if len(w2sServices) > 0 {
			origPrefix := config.SmPrefix
			config.SmPrefix = "w2sm"
			zap.L().Info("Generating TypeScript w2s gRPC-over-REST client wrappers", zap.Int("count", len(w2sServices)))
			if err := generateW2SGrpcOverRestClients(w2sServices, protoSrcDir); err != nil {
				config.SmPrefix = origPrefix
				return fmt.Errorf("failed to generate w2s gRPC-over-REST clients: %w", err)
			}
			if err := generateW2SServiceManager(config); err != nil {
				config.SmPrefix = origPrefix
				return fmt.Errorf("failed to generate w2sm entry point: %w", err)
			}
			config.SmPrefix = origPrefix
		}
	}
	warnNoW2SClients(config, w2sServices)

	// Collect event-bus watches from legacy subscribe, explicit event_watch,
	// and validated store-derived watches.
	config.Subscriptions, err = collectTsSubscriptions(config, protoSrcDir)
	if err != nil {
		return fmt.Errorf("failed to collect event-bus subscriptions: %w", err)
	}
	if len(config.Subscriptions) > 0 {
		zap.L().Info("Found event-bus subscriptions", zap.Int("count", len(config.Subscriptions)))
	}
	config.AnnotatedProtoDir = protoSrcDir

	// Discover the token-mint exchangers NOW, before StripSpecOptionsFromDir
	// removes the (spec.token_minter) annotations that discovery keys on. Two
	// distinct exchangers (mirroring rust's libs.go): the authoritative S2S mint
	// (service_im) and the client-reachable C2S mint (client_im).
	mintExchangerInfo, err := parser.DiscoverTokenExchangerPackage(config.AnnotatedProtoDir)
	if err != nil {
		return fmt.Errorf("failed to discover token exchanger package: %w", err)
	}
	clientMintExchangerInfo, err := parser.DiscoverClientTokenExchangerPackage(config.AnnotatedProtoDir)
	if err != nil {
		return fmt.Errorf("failed to discover client token exchanger package: %w", err)
	}

	// Full-client target: no service runtime is emitted. Server services never
	// resolve here (ResolveServerServices returns nil), and injected/visible
	// states must not trigger the runtime SM — it imports service-side sys
	// (service_im, grpcserver, healthreporter) that client builds do not emit.
	// Subscriptions also cannot populate for client-only targets today (the
	// collection scans only States/GrpcServers protos, which are rejected up
	// front) and the runtime SM template is not client-safe — so a non-empty set
	// means the design gap is being hit: fail loudly rather than emit broken
	// imports (NEW-33). Client-side subscription support (a client package
	// consuming eventbus topics) needs a subscription-only SM with client-safe
	// imports — a design decision, not a silent fallthrough.
	if len(config.Subscriptions) > 0 {
		return fmt.Errorf("typescript is a client-only target; event-bus subscriptions require the service-runtime ServiceManager, which client builds do not emit (client-side subscription support is not implemented yet)")
	}

	// (Event-bus subscriber modules — generateTsSubscribers and the
	// subscriber.ts.tmpl envelope machinery, pinned by the PC-C-5 suite — wire
	// back in here once client-side subscription support lands; see the fail-loud
	// guard above.)

	// Generate state client wrappers for services with service_state
	if err := generateStateClientWrappers(config, protoSrcDir); err != nil {
		return fmt.Errorf("failed to generate state client wrappers: %w", err)
	}

	// Generate per-type REST CRUD clients
	restCrudPackages := resolveRestCrudPackages(config, allParsedFiles)
	if len(restCrudPackages) > 0 {
		zap.L().Info("Generating per-type REST CRUD clients (TS)", zap.Int("packages", len(restCrudPackages)))
		if err := generateTsRestCrudClients(config, restCrudPackages); err != nil {
			return fmt.Errorf("failed to generate REST CRUD clients: %w", err)
		}
	}

	// Generate typed disk-persistence helpers for service_store messages (no-op
	// when there are none).
	if err := generateTsDiskStore(config, servicesInfo, protoSrcDir); err != nil {
		return fmt.Errorf("failed to generate diskstore helpers: %w", err)
	}

	// Generate package.json for each module (before stripping, needs sys/spec.proto)
	zap.L().Info("Generating package.json files")
	if err := generatePackageJsonFiles(config, protoSrcDir, protoFiles, serviceStateFiles); err != nil {
		return fmt.Errorf("failed to generate package.json files: %w", err)
	}

	// Generate registry.json, registry.schema.json, AGENT.md (shared across all languages).
	// Must run BEFORE StripSpecOptionsFromDir because registry generation needs service annotations.
	// to detect allow_user / AUTH_MODEL_U2S.
	dp, err := parser.NewDescriptorParser(protoSrcDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser for registry: %w", err)
	}
	if err := registry.Generate(registry.Config{
		OutputDir:   config.OutputDir,
		VersionTag:  config.VersionTag,
		InputDir:    protoSrcDir,
		Language:    "typescript",
		PackageBase: config.TsPackageBase,
	}, allParsedFiles, dp); err != nil {
		return fmt.Errorf("failed to generate registry.json: %w", err)
	}

	// Strip spec.* annotations from output proto files now that all dependent operations are done.
	if err := proto.StripSpecOptionsFromDir(protoSrcDir); err != nil {
		return fmt.Errorf("failed to strip spec options: %w", err)
	}

	// Generate index.ts files for clean exports in types/ directory
	zap.L().Info("Generating index.ts files")
	if err := generateIndexFiles(filepath.Join(config.OutputDir, "types")); err != nil {
		return fmt.Errorf("failed to generate index files: %w", err)
	}

	// Generate root package.json for workspace
	zap.L().Info("Generating workspace package.json")
	if err := generateWorkspacePackageJson(config); err != nil {
		return fmt.Errorf("failed to generate workspace package.json: %w", err)
	}

	// Write library templates
	zap.L().Info("Writing TypeScript library templates")
	declaredAuds, err := proto.ScanAcceptedAudiences(config.InputDir, config.ServiceLabel())
	if err != nil {
		return fmt.Errorf("failed to scan (spec.service).accepted_audiences: %w", err)
	}
	if err := writeLibTemplates(config, mintExchangerInfo, clientMintExchangerInfo, proto.AcceptedAudiencesForHost(config.ServiceLabel(), vsEntries, declaredAuds)); err != nil {
		return fmt.Errorf("failed to write library templates: %w", err)
	}

	// Generate index.ts for sys/ AFTER templates so we include both _pb exports and template exports
	if err := generateIndexFiles(filepath.Join(config.OutputDir, "chassis")); err != nil {
		return fmt.Errorf("failed to generate sys index files: %w", err)
	}

	// Generate aggregation files for wrappers (grpc_servers, state_clients)
	zap.L().Info("Generating TypeScript aggregation files")
	if err := generateAggregationFiles(config); err != nil {
		return fmt.Errorf("failed to generate aggregation files: %w", err)
	}

	// Ensure every workspace package has an index.ts so its main entrypoint
	// resolves to a built index.js. Must run after all packages and their
	// source files exist, and before repointing entrypoints.
	zap.L().Info("Ensuring workspace package index files")
	if err := ensurePackageIndexes(config); err != nil {
		return fmt.Errorf("failed to ensure package index files: %w", err)
	}

	// Repoint every workspace package's runtime entrypoint at the single rooted
	// dist/ tree the root `tsc` build produces (OF-29). Must run after every
	// package.json has been written (createPackageJson, lib templates,
	// rest-client packages, root workspace).
	zap.L().Info("Repointing workspace package entrypoints to rooted dist/")
	if err := repointPackageEntrypoints(config); err != nil {
		return fmt.Errorf("failed to repoint package entrypoints: %w", err)
	}

	zap.L().Info("TypeScript generation complete!")
	zap.L().Info("To install dependencies", zap.String("command", "cd "+config.OutputDir+"&& npm install"))
	return nil
}

// collectProtoFiles walks the input directory and collects all proto files.
// Skips services/{name}/services.proto files since protoc-gen-es only generates
// code for messages/enums, not service stubs.
func collectProtoFiles(inputDir string) ([]string, error) {
	var protoFiles []string

	err := filepath.Walk(inputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}

		relPath, err := filepath.Rel(inputDir, path)
		if err != nil {
			return err
		}

		// Do NOT skip services/{name}/services.proto — it may contain
		// state root messages (e.g., BaseballState with SERVICE_TYPE_BACKEND_STATE).
		// protoc-gen-es will only generate code for messages/enums, not service
		// blocks, so including services.proto is harmless when it only has services.

		protoFiles = append(protoFiles, relPath)
		return nil
	})

	return protoFiles, err
}

// generateTypeScriptModule generates TypeScript code for a single proto file
func generateTypeScriptModule(config *tactype.Config, inputDir, protoFile string, serviceStateFiles map[string]bool) error {
	subdir := filepath.Dir(protoFile)
	filename := strings.TrimSuffix(filepath.Base(protoFile), ".proto")
	outputDir := typescriptModuleDirForProtoFeatures(config.OutputDir, protoFile, typeScriptServiceStateFile(serviceStateFiles, protoFile), false)

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}

	zap.L().Info("Generating TypeScript", zap.String("proto", protoFile), zap.String("output", outputDir))

	// Determine which protoc-gen-es to use
	protocGenEs := config.ProtocGenTs
	var cmd *exec.Cmd

	if strings.Contains(protocGenEs, "npx") { // detect npx wrapper vs direct binary path
		// Use npx wrapper
		wrapperPath, err := createNpxWrapper()
		if err != nil {
			return fmt.Errorf("failed to create npx wrapper: %w", err)
		}
		defer os.Remove(wrapperPath)
		cmd = exec.Command(config.ProtocPath,
			"--plugin=protoc-gen-es="+wrapperPath,
			"--es_out="+outputDir,
			"--es_opt=target=ts",
			"--proto_path="+inputDir,
			protoFile)
	} else {
		// Use direct path
		cmd = exec.Command(config.ProtocPath,
			"--plugin=protoc-gen-es="+protocGenEs,
			"--es_out="+outputDir,
			"--es_opt=target=ts",
			"--proto_path="+inputDir,
			protoFile)
	}

	// Log the command being executed
	zap.L().Debug("Running protoc", zap.String("protoc", config.ProtocPath), zap.String("args", strings.Join(cmd.Args[1:], "")))

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("protoc failed: %w\n%s", err, string(output))
	}

	// Flatten the directory structure - protoc creates nested dirs matching proto structure
	// e.g., types/sys/alpn/sys/alpn_pb.ts should be types/sys/alpn/alpn_pb.ts
	nestedFile := filepath.Join(outputDir, subdir, filename+"_pb.ts")
	flatFile := filepath.Join(outputDir, filename+"_pb.ts")
	if _, err := os.Stat(nestedFile); err == nil {
		if err := os.Rename(nestedFile, flatFile); err != nil {
			return fmt.Errorf("failed to flatten output: %w", err)
		}
		// Remove the now-empty nested directory tree starting from the first component
		// of subdir relative to outputDir
		firstComponent := strings.SplitN(filepath.ToSlash(subdir), "/", 2)[0]
		nestedDir := filepath.Join(outputDir, firstComponent)
		if err := os.RemoveAll(nestedDir); err != nil {
			zap.L().Warn("failed to remove nested directory", zap.String("path", nestedDir), zap.Error(err))
		}
	}

	// Create bridge file that re-exports from the generated _pb.ts file
	pbFile := filepath.Join(outputDir, filename+"_pb.ts")
	bridgeFile := filepath.Join(outputDir, filename+".ts")

	if _, err := os.Stat(pbFile); err == nil {
		zap.L().Debug("Creating bridge file", zap.String("path", bridgeFile))
		bridgeContent := fmt.Sprintf("// Auto-generated bridge file for %s\nexport * from './%s_pb.js';\n",
			filename, filename)
		if err := os.WriteFile(bridgeFile, []byte(bridgeContent), 0644); err != nil {
			return fmt.Errorf("failed to create bridge file: %w", err)
		}
	}

	return nil
}

// createNpxWrapper creates a wrapper script for npx protoc-gen-es in a fresh,
// exclusively-created temp file, so a shared /tmp can't let another user
// plant or symlink the path before us. Caller is responsible for removing it.
func createNpxWrapper() (string, error) {
	f, err := os.CreateTemp("", "protoc-gen-es-wrapper-*.sh")
	if err != nil {
		return "", err
	}
	defer f.Close()

	content := "#!/bin/bash\nexec npx -y @bufbuild/protoc-gen-es@^2 \"$@\"\n"
	if _, err := f.WriteString(content); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	if err := f.Chmod(0700); err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

// copyHelperFiles copies generated enum and constant files from builder output
func copyHelperFiles(builderTempDir, outputDir string) error {
	tsDir := filepath.Join(builderTempDir, "typescript")
	if _, err := os.Stat(tsDir); os.IsNotExist(err) {
		zap.L().Debug("No helper files generated")
		return nil
	}

	return filepath.Walk(tsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}

		relPath, err := filepath.Rel(tsDir, path)
		if err != nil {
			return err
		}

		dstFile := filepath.Join(outputDir, relPath)
		dstDir := filepath.Dir(dstFile)

		// Skip helpers.ts if no _pb.ts file exists in the destination directory.
		// This happens for service-only proto files (e.g., services.proto) where
		// protoc-gen-es generates no _pb.ts but the parser model includes
		// referenced message types from other proto files.
		if filepath.Base(relPath) == "helpers.ts" {
			hasPb := false
			if entries, err := os.ReadDir(dstDir); err == nil {
				for _, e := range entries {
					if strings.HasSuffix(e.Name(), "_pb.ts") {
						hasPb = true
						break
					}
				}
			}
			if !hasPb {
				zap.L().Debug("Skipping helpers.ts (no _pb.ts in output dir)", zap.String("path", relPath))
				return nil
			}
		}

		if err := os.MkdirAll(dstDir, 0755); err != nil {
			return err
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		if err := os.WriteFile(dstFile, content, 0644); err != nil {
			return err
		}

		zap.L().Debug("Copied helper file", zap.String("path", relPath))
		return nil
	})
}

// copyUtilsDirectory copies the utils directory with error helpers to output.
// Error helpers (newErrMissingInvalidData, etc.) are written to types/sys/errors/errors.ts,
// which is the TypeScript equivalent of the Go errors package.
// Also creates the empty utils/ directory to match the Go layout.
func copyUtilsDirectory(outputDir string) error {
	errorsDir := filepath.Join(outputDir, "types", "sys", "errors")
	if err := os.MkdirAll(errorsDir, 0755); err != nil {
		return fmt.Errorf("failed to create errors directory: %w", err)
	}

	// Write embedded errors.ts template to sys/errors/
	errorsContent, err := ErrorsTs()
	if err != nil {
		return fmt.Errorf("failed to load errors.ts template: %w", err)
	}
	dstFile := filepath.Join(errorsDir, "errors.ts")
	if err := os.WriteFile(dstFile, []byte(errorsContent), 0644); err != nil {
		return fmt.Errorf("failed to write errors.ts: %w", err)
	}

	zap.L().Debug("Created sys/errors/errors.ts")

	// Create empty utils/ directory to match Go layout
	utilsDir := filepath.Join(outputDir, "utils")
	if err := os.MkdirAll(utilsDir, 0755); err != nil {
		return fmt.Errorf("failed to create utils directory: %w", err)
	}

	zap.L().Debug("Created utils/ directory")
	return nil
}

// generatePackageJsonFiles creates package.json for each TypeScript module
func generatePackageJsonFiles(config *tactype.Config, inputDir string, protoFiles []string, serviceStateFiles map[string]bool) error {
	outputDir := config.OutputDir
	for _, protoFile := range protoFiles {
		subdir := filepath.Dir(protoFile)
		filename := strings.TrimSuffix(filepath.Base(protoFile), ".proto")

		// Read proto file to get package name and dependencies
		protoPath := filepath.Join(inputDir, protoFile)
		content, err := os.ReadFile(protoPath)
		if err != nil {
			return err
		}

		packageRegex := regexp.MustCompile(`(?m)^package\s+(\w+)\s*;`)
		matches := packageRegex.FindStringSubmatch(string(content))
		if len(matches) < 2 {
			continue
		}
		packageName := matches[1]

		// Extract imports
		importRegex := regexp.MustCompile(`(?m)^import\s+"([^"]+)";`)
		importMatches := importRegex.FindAllStringSubmatch(string(content), -1)

		var dependencies []string
		for _, match := range importMatches {
			if len(match) >= 2 {
				importPath := match[1]
				if strings.HasPrefix(importPath, "google/protobuf/") {
					continue
				}
				dependencies = append(dependencies, importPath)
			}
		}

		// State source protos and services.proto go to the services/ directory which already
		// has its own package.json from generateServicesPackageJson, so skip duplicates.
		if svcName, fileName := proto.ServiceProtoParts(protoFile); svcName != "" && (typeScriptServiceStateFile(serviceStateFiles, protoFile) || proto.IsServicesFileName(fileName)) {
			continue
		}

		moduleDir := typescriptModuleDirForProtoFeatures(outputDir, protoFile, typeScriptServiceStateFile(serviceStateFiles, protoFile), false)
		if err := createPackageJson(config, moduleDir, subdir, filename, packageName, dependencies, serviceStateFiles); err != nil {
			return err
		}
	}

	return nil
}

// createPackageJson creates a package.json file for a TypeScript module
func createPackageJson(config *tactype.Config, moduleDir, subdir, filename, packageName string, dependencies []string, serviceStateFiles map[string]bool) error {
	packageJsonPath := filepath.Join(moduleDir, "package.json")

	// Ensure directory exists
	if err := os.MkdirAll(moduleDir, 0755); err != nil {
		return fmt.Errorf("failed to create module directory: %w", err)
	}

	// Load package registry for dependency versions
	registry, err := packages.GetPackageRegistry()
	if err != nil {
		return fmt.Errorf("failed to load package registry: %w", err)
	}

	// Helper to get version from registry (no fallbacks - must be in dependencies.yaml)
	getVersion := func(pkgName string) (string, error) {
		if ver, ok := registry.GetTypeScriptVersion(pkgName); ok {
			return ver, nil
		}
		return "", fmt.Errorf("%s version not found in dependencies.yaml", pkgName)
	}

	// Use config.TsPackageBase instead of hardcoded fallback
	basePackage := config.TsPackageBase
	if basePackage == "" {
		return fmt.Errorf("TsPackageBase is required for TypeScript generation; set --ts-package-base or config.yaml lang.typescript.package_base")
	}
	// Construct npm package name using buildNpmPackageName for proper scoped package format.
	// Sys packages use sys-{name} format (same as other categories).
	npmPackageName := buildNpmPackageName(basePackage, strings.ReplaceAll(subdir, "/", "-"), filename)

	// Check if servicewrapper.ts exists (indicates service with express dependency)
	serviceWrapperPath := filepath.Join(moduleDir, "servicewrapper.ts")
	hasServiceWrapper := false
	if _, err := os.Stat(serviceWrapperPath); err == nil {
		hasServiceWrapper = true
	}

	// Resolve all dependency versions upfront (no fallbacks - must be in dependencies.yaml)
	bufbuildVersion, err := getVersion("@bufbuild/protobuf")
	if err != nil {
		return err
	}
	jsYamlVersion, err := getVersion("js-yaml")
	if err != nil {
		return err
	}
	iarnaTomlVersion, err := getVersion("@iarna/toml")
	if err != nil {
		return err
	}
	typesJsYamlVersion, err := getVersion("@types/js-yaml")
	if err != nil {
		return err
	}

	var expressVersion, typesExpressVersion string
	if hasServiceWrapper {
		expressVersion, err = getVersion("express")
		if err != nil {
			return err
		}
		typesExpressVersion, err = getVersion("@types/express")
		if err != nil {
			return err
		}
	}

	tsVersion, ok := registry.GetToolVersion("typescript")
	if !ok {
		return fmt.Errorf("typescript tool version not found in dependencies.yaml")
	}

	var b strings.Builder
	b.WriteString("{\n")
	fmt.Fprintf(&b, "\"name\": \"%s\",\n", npmPackageName)
	b.WriteString("\"version\": \"1.0.0\",\n")
	fmt.Fprintf(&b, "\"description\": \"TypeScript types for %s proto package\",\n", packageName)
	fmt.Fprintf(&b, "\"main\": \"index.ts\",\n")
	b.WriteString("\"exports\": {\n")
	b.WriteString("\".\": \"./index.ts\",\n")
	b.WriteString("\"./*\": \"./*\"\n")
	b.WriteString("},\n")
	b.WriteString("\"type\": \"module\",\n")
	b.WriteString("\"scripts\": {\n")
	b.WriteString("\"build\": \"tsc\"\n")
	b.WriteString("},\n")
	b.WriteString("\"dependencies\": {\n")
	fmt.Fprintf(&b, "\"@bufbuild/protobuf\": \"%s\",\n", bufbuildVersion)
	fmt.Fprintf(&b, "\"js-yaml\": \"%s\",\n", jsYamlVersion)
	fmt.Fprintf(&b, "\"@iarna/toml\": \"%s\"", iarnaTomlVersion)

	// Add express if servicewrapper exists
	if hasServiceWrapper {
		b.WriteString(",\n")
		fmt.Fprintf(&b, "\"express\": \"%s\"", expressVersion)
	}

	// Add dependencies on other generated packages (resolved via npm workspaces)
	// Deduplicate since multiple imports may resolve to the same package
	seen := make(map[string]bool)
	for _, dep := range dependencies {
		depPackageName := typeScriptNpmPackageNameForProto(basePackage, dep, serviceStateFiles)
		if seen[depPackageName] {
			continue
		}
		seen[depPackageName] = true
		b.WriteString(",\n")
		fmt.Fprintf(&b, "\"%s\": \"*\"", depPackageName)
	}

	b.WriteString("},\n")
	b.WriteString("\"devDependencies\": {\n")
	fmt.Fprintf(&b, "\"typescript\": \"%s\",\n", tsVersion)
	fmt.Fprintf(&b, "\"@types/js-yaml\": \"%s\"", typesJsYamlVersion)

	// Add @types/express if servicewrapper exists
	if hasServiceWrapper {
		b.WriteString(",\n")
		fmt.Fprintf(&b, "\"@types/express\": \"%s\"", typesExpressVersion)
	}

	b.WriteString("},\n")
	b.WriteString("\"keywords\": [\"protobuf\", \"typescript\", \"types\"],\n")
	b.WriteString("\"license\": \"UNLICENSED\",\n")
	b.WriteString("\"private\": true\n")
	b.WriteString("}\n")

	if err := os.WriteFile(packageJsonPath, []byte(b.String()), 0644); err != nil {
		return err
	}

	zap.L().Debug("Created package.json", zap.String("package", npmPackageName))

	// Also create tsconfig.json
	tsconfigPath := filepath.Join(moduleDir, "tsconfig.json")
	tsconfig := `{
  "compilerOptions": {
    "target": "ES2020",
    "module": "ES2020",
    "lib": ["ES2020", "DOM"],
    "declaration": true,
    "outDir": "dist",
    "strict": true,
    "esModuleInterop": true,
    "skipLibCheck": true,
    "forceConsistentCasingInFileNames": true,
    "moduleResolution": "bundler"
  },
  "include": ["*.ts"],
  "exclude": ["node_modules", "dist", "*.test.ts", "*_test.ts"]
}
`
	return os.WriteFile(tsconfigPath, []byte(tsconfig), 0644)
}

// generateIndexFiles creates index.ts files for clean exports
func generateIndexFiles(outputDir string) error {
	return filepath.Walk(outputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || !info.IsDir() {
			return err
		}
		return generateIndexForDir(path)
	})
}

var (
	indexExportRe = regexp.MustCompile(`(?m)^export\s+(?:function|class|interface|type|enum|const|let|var|abstract\s+class)\s+(\w+)`)
	indexRefRe    = regexp.MustCompile(`from\s+['"]\./([^'"/]+)(?:\.js)?['"]`)
)

// generateIndexForDir writes (or supplements) a single directory's index.ts,
// re-exporting every non-test, non-bridge .ts sibling. An existing index.ts is
// preserved and only supplemented with exports it does not already cover. A
// directory with no exportable files is left untouched.
func generateIndexForDir(path string) error {
	{
		exportRe := indexExportRe
		dirName := filepath.Base(path)

		// Find all .ts files except index.ts
		var fileNames []string
		entries, err := os.ReadDir(path)
		if err != nil {
			return err
		}

		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".ts") {
				continue
			}
			if entry.Name() == "index.ts" {
				continue
			}
			// Skip test files
			if strings.HasSuffix(entry.Name(), ".test.ts") || strings.HasSuffix(entry.Name(), "_test.ts") {
				continue
			}
			// Skip .d.ts files (they're either generated by tsc or bridge files)
			if strings.HasSuffix(entry.Name(), ".d.ts") {
				continue
			}
			// Skip _grpc.ts files - their service exports conflict with _pb.ts service exports.
			// The bridge file (e.g., spec.ts) already re-exports _pb.ts.
			if strings.HasSuffix(entry.Name(), "_grpc.ts") {
				continue
			}

			baseName := strings.TrimSuffix(entry.Name(), ".ts")
			// Skip bridge files (file matching directory name, e.g., logger.ts in logger/).
			// These just re-export from _pb.ts and would cause duplicate exports.
			if baseName == dirName {
				continue
			}
			fileNames = append(fileNames, baseName)
		}

		if len(fileNames) == 0 {
			return nil
		}

		// Scan for exported names to detect conflicts across files
		nameToFiles := make(map[string][]string) // exported name -> list of files
		for _, baseName := range fileNames {
			filePath := filepath.Join(path, baseName+".ts")
			content, readErr := os.ReadFile(filePath)
			if readErr != nil {
				continue
			}
			matches := exportRe.FindAllStringSubmatch(string(content), -1)
			for _, m := range matches {
				nameToFiles[m[1]] = append(nameToFiles[m[1]], baseName)
			}
		}

		// Find files involved in export name conflicts
		conflictFiles := make(map[string]bool)
		for _, files := range nameToFiles {
			if len(files) > 1 {
				for _, f := range files {
					conflictFiles[f] = true
				}
			}
		}

		indexPath := filepath.Join(path, "index.ts")
		existingIndex := ""
		referencedFiles := make(map[string]bool)
		if existing, readErr := os.ReadFile(indexPath); readErr == nil {
			existingIndex = strings.TrimRight(string(existing), "\n")
			for _, match := range indexRefRe.FindAllStringSubmatch(existingIndex, -1) {
				referencedFiles[strings.TrimSuffix(match[1], ".js")] = true
			}
		} else if !os.IsNotExist(readErr) {
			return readErr
		}

		// Generate index.ts with namespace re-exports for conflicting files.
		// If a template already wrote an index, preserve its explicit public API
		// and append only exports for files the template did not already cover.
		var exports []string
		for _, baseName := range fileNames {
			if referencedFiles[baseName] {
				continue
			}
			if conflictFiles[baseName] {
				exports = append(exports, fmt.Sprintf("export * as %s from './%s.js';", baseName, baseName))
			} else {
				exports = append(exports, fmt.Sprintf("export * from './%s.js';", baseName))
			}
		}

		if existingIndex != "" && len(exports) == 0 {
			return nil
		}

		content := "// Auto-generated index file\n"
		if existingIndex != "" {
			content = existingIndex + "\n// Auto-generated supplemental exports\n"
		}
		content += strings.Join(exports, "\n")
		if len(exports) > 0 {
			content += "\n"
		}
		if err := os.WriteFile(indexPath, []byte(content), 0644); err != nil {
			return err
		}
		zap.L().Debug("Created index.ts", zap.String("dir", path))

		return nil
	}
}

// ensurePackageIndexes generates an index.ts for every generated workspace
// package that lacks one, so that the package's `main: <dist>/index.js`
// entrypoint resolves to a built file. generateIndexFiles only runs over
// types/ and sys/; the grpc client/server/rest packages under
// {c2,c2d,s2,w2}sm/ carry a single hand-emitted source file (e.g.
// contract_api.ts) and no index, which is why their declared entrypoint never
// resolved (latent OF-29 class). We only touch directories that ARE workspace
// packages (own a package.json), so loose aggregator dirs are left alone.
func ensurePackageIndexes(config *tactype.Config) error {
	outDir := config.OutputDir
	rootPkg := filepath.Join(outDir, "package.json")
	return filepath.Walk(outDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "node_modules" || info.Name() == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Name() != "package.json" || path == rootPkg {
			return nil
		}
		dir := filepath.Dir(path)
		if _, statErr := os.Stat(filepath.Join(dir, "index.ts")); statErr == nil {
			return nil // already has an index
		}
		return generateIndexForDir(dir)
	})
}

// generateWorkspacePackageJson creates a root package.json for npm workspace
func generateWorkspacePackageJson(config *tactype.Config) error {
	packageJsonPath := filepath.Join(config.OutputDir, "package.json")

	// Load package registry for dependency versions
	registry, err := packages.GetPackageRegistry()
	if err != nil {
		return fmt.Errorf("failed to load package registry: %w", err)
	}

	tsVersion, ok := registry.GetToolVersion("typescript")
	if !ok {
		return fmt.Errorf("typescript tool version not found in dependencies.yaml")
	}

	// Use configured package base instead of hardcoded @tacplus
	basePackage := config.TsPackageBase
	if basePackage == "" {
		return fmt.Errorf("TsPackageBase is required for TypeScript generation")
	}

	content := fmt.Sprintf(`{
  "name": "%s",
  "version": "1.0.0",
  "type": "module",
  "description": "TypeScript type definitions for protocol buffers",
  "private": true,
  "workspaces": [
    "c2dm/internal/grpc/clients/*/*",
    "c2sm/internal/grpc/clients/*/*",
    "c2sm/internal/grpc_over_rest/*/*",
    "c2sm/internal/rest/*/*",
    "chassis/*",
    "s2sm/internal/grpc/clients/*/*",
    "s2sm/internal/grpc_over_rest/*/*",
    "s2sm/internal/rest/*/*",
    "types/clients/*",
    "types/common/*",
    "types/generated/*",
    "types/services/*",
    "types/sys/*",
    "types/vservices/*",
    "types/utils/*",
    "w2sm/internal/grpc/clients/*/*",
    "w2sm/internal/grpc_over_rest/*/*",
    "w2sm/internal/rest/*/*"
  ],
  "scripts": {
    "build": "tsc -p tsconfig.json",
    "clean": "rm -rf dist **/dist"
  },
  "devDependencies": {
    "@types/node": "^20.11.16",
    "typescript": "%s"
  },
  "keywords": ["protobuf", "typescript", "types"],
  "license": "UNLICENSED"
}
`, basePackage, tsVersion)

	if err := os.WriteFile(packageJsonPath, []byte(content), 0644); err != nil {
		return err
	}

	tsconfigPath := filepath.Join(config.OutputDir, "tsconfig.json")
	tsconfig := `{
  "compilerOptions": {
    "target": "ES2022",
    "module": "ES2022",
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "declaration": true,
    "outDir": "dist",
    "rootDir": ".",
    "strict": true,
    "esModuleInterop": true,
    "skipLibCheck": true,
    "forceConsistentCasingInFileNames": true,
    "moduleResolution": "bundler",
    "resolveJsonModule": true,
    "allowSyntheticDefaultImports": true,
    "types": ["node"]
  },
  "include": ["**/*.ts"],
  "exclude": ["node_modules", "dist", "**/*.test.ts", "**/*_test.ts"]
}
`
	if err := os.WriteFile(tsconfigPath, []byte(tsconfig), 0644); err != nil {
		return err
	}

	// Generate .npmrc for GitLab registry if configured
	if npmURL := config.Registry.GitLabNpmURL(); npmURL != "" {
		// Extract scope from interfaces package (e.g., "@example" from "@example/controlplane-interfaces")
		scope := ""
		if iface := config.Lang.TypeScript.Interfaces; strings.HasPrefix(iface, "@") {
			if idx := strings.Index(iface, "/"); idx > 0 {
				scope = iface[:idx]
			}
		}
		if scope != "" {
			npmrcPath := filepath.Join(config.OutputDir, ".npmrc")
			npmrcContent := fmt.Sprintf("%s:registry=%s/\n//%s/:_authToken=${GITLAB_NPM_TOKEN}\n",
				scope,
				npmURL,
				strings.TrimPrefix(npmURL, "https://"),
			)
			if err := os.WriteFile(npmrcPath, []byte(npmrcContent), 0644); err != nil {
				return fmt.Errorf("failed to write .npmrc: %w", err)
			}
			zap.L().Debug("Created .npmrc for GitLab registry")
		}
	}

	zap.L().Debug("Created workspace package.json")
	return nil
}

// repointPackageEntrypoints rewrites every generated workspace package's
// package.json so that its runtime entrypoint (`main`) resolves to the
// package's compiled output inside the single rooted dist/ tree that the root
// `tsc -p tsconfig.json` build produces.
//
// Generated code imports a handful of sibling packages BY PACKAGE NAME (e.g.
// `import * as spiffe from 'contractclient-model-sys-spiffe'`). At runtime
// node resolves those through the npm-workspace symlink
// node_modules/<pkg> -> <pkg dir>, then through <pkg dir>/package.json `main`.
// Lib templates declared `main: dist/index.js` and createPackageJson declared
// `main: index.ts`; neither resolves to a file the rooted build emits — the
// compiled file lives at <root>/dist/<pkgRel>/index.js — so node threw
// ERR_MODULE_NOT_FOUND for every cross-package import-by-name (OF-29).
//
// We point `main` at the rooted build output (a package-dir-relative path that
// escapes the package dir, which `main` — unlike `exports` — permits), and drop
// any source-pointing `exports` so node falls through to `main`. `types` stays
// package-relative (`dist/index.d.ts`, which does not exist at build time):
// that is exactly what the working build already does for lib packages, and it
// keeps the root build's type resolution falling back to each package's source
// index.ts during the clean compile (when no dist/ exists yet).
//
// The whole generated tree builds into ONE rooted dist/, so per-package
// `dist/` (TS project references / `tsc -b`) is deliberately NOT used: the
// generated code crosses package boundaries with ~100 relative imports that
// only resolve under a single rooted mirror. This pass is the single source of
// truth for entrypoints across all package.json producers (createPackageJson,
// lib templates, rest-client packages).
func repointPackageEntrypoints(config *tactype.Config) error {
	outDir := config.OutputDir
	rootPkg := filepath.Join(outDir, "package.json")

	return filepath.Walk(outDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == "node_modules" || info.Name() == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if info.Name() != "package.json" || path == rootPkg {
			return nil
		}

		dir := filepath.Dir(path)
		pkgRel, err := filepath.Rel(outDir, dir)
		if err != nil {
			return err
		}
		distRel, err := filepath.Rel(dir, filepath.Join(outDir, "dist", pkgRel))
		if err != nil {
			return err
		}
		distRelSlash := filepath.ToSlash(distRel)

		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		// json.RawMessage preserves every other field (dependencies, scripts,
		// version constraints) verbatim; MarshalIndent sorts map keys, so the
		// output is deterministic across runs.
		var m map[string]json.RawMessage
		if err := json.Unmarshal(raw, &m); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		setStr := func(k, v string) {
			b, _ := json.Marshal(v)
			m[k] = b
		}
		setStr("main", distRelSlash+"/index.js")
		setStr("types", distRelSlash+"/index.d.ts")
		delete(m, "exports")

		out, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			return err
		}
		out = append(out, '\n')
		return os.WriteFile(path, out, 0644)
	})
}

// fixImportPaths fixes cross-package import paths in generated TypeScript files.
// All type files live under types/ root with the layout:
//   - types/sys/{name}/{file}.ts
//   - types/common/{name}/{file}.ts
//   - types/services/{svc}/{pkg}/{file}.ts
//   - types/services/{svc}/services/{file}.ts
//   - types/{proto-dir}/{package}/{file}.ts for arbitrary top-level proto dirs
//
// Protoc-gen-es generates imports relative to the proto directory structure. After
// flattening, those imports need to be rewritten to match the output layout.
// We resolve each import to an absolute proto path, then map it to the output layout.
func fixImportPaths(outputDir string, serviceStateFiles map[string]bool) error {
	// Walk both types/ and sys/ directories since sys is now at the top level
	for _, subDir := range []string{"types"} {
		dir := filepath.Join(outputDir, subDir)
		if _, err := os.Stat(dir); os.IsNotExist(err) {
			continue
		}
		err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}

			// Only process TypeScript files
			if info.IsDir() || !strings.HasSuffix(path, ".ts") {
				return nil
			}

			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			original := string(content)

			// Get the relative path from the output directory (not types/)
			relPath, err := filepath.Rel(outputDir, path)
			if err != nil {
				return err
			}
			relPath = filepath.ToSlash(relPath)

			// Determine the file's directory relative to output root
			// e.g., "types/services/baseball/services" or "sys/alpn"
			fileDir := filepath.ToSlash(filepath.Dir(relPath))

			// Determine the proto-root-relative directory for this file.
			// This is the proto directory that protoc-gen-es used when generating imports.
			//   sys/{name}/{name}_pb.ts          -> proto dir: sys/
			//   types/common/{name}/{name}_pb.ts   -> proto dir: common/
			//   types/services/{svc}/{pkg}/{pkg}_pb.ts -> proto dir: services/{svc}/
			//   types/services/{svc}/services/services_pb.ts -> proto dir: services/{svc}/
			protoDir := computeProtoDir(fileDir)
			if headerProtoDir, ok := generatedProtoDir(original); ok {
				protoDir = headerProtoDir
			}

			fixed := fixCrossPackageImports(original, fileDir, protoDir, serviceStateFiles)

			// Only write if content changed
			if fixed != original {
				if err := os.WriteFile(path, []byte(fixed), 0644); err != nil {
					return err
				}
				zap.L().Debug("Fixed imports", zap.String("file", relPath))
			}

			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// computeProtoDir determines the proto-root-relative directory for a file based on its
// location in the output tree. This is used to resolve protoc-gen-es relative imports.
// fileDir is relative to the output root (e.g., "sys/alpn", "types/common/ballsports").
func computeProtoDir(fileDir string) string {
	parts := strings.Split(fileDir, "/")
	if len(parts) < 2 {
		return fileDir
	}
	switch parts[0] {
	case "sys":
		// sys/{name}/ -> proto was at sys/
		return "sys"
	case "types":
		if len(parts) < 3 {
			return fileDir
		}
		switch parts[1] {
		case "common":
			// types/common/{name}/ -> proto was at common/
			return "common"
		case "generated":
			// types/generated/{name}/ -> proto was at generated/
			return "generated"
		case "clients":
			// types/clients/{name}/ -> proto was at clients/
			return "clients"
		case "services":
			if len(parts) >= 4 {
				// types/services/{svc}/{pkg}/ -> proto was at services/{svc}/
				// types/services/{svc}/services/ -> proto was at services/{svc}/
				return "services/" + parts[2]
			}
			// types/services/{svc}/ -> proto was at services/ (top-level service proto)
			return "services"
		case "utils":
			// types/utils/{name}/ -> proto was at utils/
			return "utils"
		case "vservices":
			// types/vservices/{name}/ -> proto was at vservices/
			return "vservices"
		case "google":
			// types/google/api/annotations/ -> proto was at google/api/
			// Rejoin all parts except "types" and the last (package dir)
			if len(parts) >= 4 {
				return strings.Join(parts[1:len(parts)-1], "/")
			}
			return strings.Join(parts[1:], "/")
		default:
			// types/{proto-dir}/{package}/ -> proto was at {proto-dir}/.
			// For nested arbitrary proto dirs, drop the final package directory.
			if len(parts) >= 3 {
				return strings.Join(parts[1:len(parts)-1], "/")
			}
			return strings.Join(parts[1:], "/")
		}
	default:
		return fileDir
	}
}

var generatedProtoFileRe = regexp.MustCompile(`(?m)^// @generated from file ([^\s]+) `)

func generatedProtoDir(content string) (string, bool) {
	match := generatedProtoFileRe.FindStringSubmatch(content)
	if len(match) < 2 {
		return "", false
	}
	dir := filepath.ToSlash(filepath.Dir(match[1]))
	if dir == "." {
		dir = ""
	}
	return dir, true
}

// protoPathToOutputPath maps a proto-root-relative path (e.g., "services/football/football_pb")
// to the correct output path relative to the output root.
// sys/ goes directly under the root; other categories go under types/.
func protoPathToOutputPath(protoPath string, serviceStateFiles map[string]bool) string {
	parts := strings.Split(protoPath, "/")
	if len(parts) < 2 {
		return protoPath + ".js"
	}

	// Determine the target file
	targetFile := parts[len(parts)-1] // e.g., "football_pb"
	targetName := strings.TrimSuffix(targetFile, "_pb")
	protoFile := filepath.ToSlash(strings.TrimSuffix(protoPath, "_pb") + ".proto")
	if typeScriptServiceStateFile(serviceStateFiles, protoFile) {
		if svcName, _ := proto.ServiceProtoParts(protoFile); svcName != "" && parts[0] != "sys" {
			return fmt.Sprintf("types/services/%s/services/%s.js", svcName, targetFile)
		}
	}

	switch parts[0] {
	case "sys":
		// sys/alpn_pb -> types/sys/alpn/alpn_pb.js (sys protos are declarative types)
		return fmt.Sprintf("types/sys/%s/%s_pb.js", targetName, targetName)
	case "generated":
		// generated/services_pb -> types/generated/services/services_pb.js
		return fmt.Sprintf("types/generated/%s/%s_pb.js", targetName, targetName)
	case "common":
		// common/ballsports_pb -> types/common/ballsports/ballsports_pb.js
		return fmt.Sprintf("types/common/%s/%s_pb.js", targetName, targetName)
	case "clients":
		// clients/myclient_pb -> types/clients/myclient/myclient_pb.js
		return fmt.Sprintf("types/clients/%s/%s_pb.js", targetName, targetName)
	case "services":
		if len(parts) >= 3 {
			svc := parts[1]
			fileName := parts[2]
			baseName := strings.TrimSuffix(fileName, "_pb")
			protoFile := filepath.ToSlash(filepath.Join(parts[0], svc, baseName+".proto"))
			// Service-state source protos and services.proto share the service module.
			if typeScriptServiceStateFile(serviceStateFiles, protoFile) || proto.IsServicesFileName(baseName) {
				return fmt.Sprintf("types/services/%s/services/%s.js", svc, fileName)
			}
			// services/{svc}/{pkg}_pb -> types/services/{svc}/{pkg}/{pkg}_pb.js
			return fmt.Sprintf("types/services/%s/%s/%s_pb.js", svc, targetName, targetName)
		}
		// Just services/{name}_pb (top-level service proto, e.g. services/cluster.proto)
		return fmt.Sprintf("types/services/%s/%s_pb.js", targetName, targetName)
	case "utils":
		// utils/myclient_pb -> types/utils/myclient/myclient_pb.js
		return fmt.Sprintf("types/utils/%s/%s_pb.js", targetName, targetName)
	case "vservices":
		// vservices/claude-sensor_pb -> types/vservices/claude-sensor/claude-sensor_pb.js
		return fmt.Sprintf("types/vservices/%s/%s_pb.js", targetName, targetName)
	case "google":
		// google/api/annotations_pb -> types/google/api/annotations/annotations_pb.js
		// Reconstruct the path: types/ + original directory structure + {name}/{name}_pb.js
		dir := strings.Join(parts[:len(parts)-1], "/")
		return fmt.Sprintf("types/%s/%s/%s_pb.js", dir, targetName, targetName)
	default:
		dir := strings.Join(parts[:len(parts)-1], "/")
		return fmt.Sprintf("types/%s/%s/%s_pb.js", dir, targetName, targetName)
	}
}

// fixCrossPackageImports fixes import statements to use correct relative paths.
//
// fileDir is the file's directory relative to types/ root (e.g., "services/baseball/services").
// protoDir is the proto-root-relative directory (e.g., "services/baseball").
//
// Strategy: resolve each relative import against protoDir to get the absolute proto path,
// then map that to the output layout, then compute the relative path from fileDir.
func fixCrossPackageImports(content, fileDir, protoDir string, serviceStateFiles map[string]bool) string {
	// Match any relative import to a _pb file (with or without .js extension)
	importRegex := regexp.MustCompile(`from\s+"((?:\.\.?/)(?:\.\./)*)([\w/.-]+?)_pb(\.js)?"`)

	content = importRegex.ReplaceAllStringFunc(content, func(match string) string {
		submatch := importRegex.FindStringSubmatch(match)
		if len(submatch) < 3 {
			return match
		}
		relPrefix := submatch[1]  // e.g., "./" or "../../"
		importPath := submatch[2] // e.g., "football/football" or "../common/ballsports"

		// Resolve the import against the proto directory to get the absolute proto path.
		// The import is relative from protoDir (the original proto file location).
		absProtoPath := filepath.ToSlash(filepath.Clean(filepath.Join(protoDir, relPrefix, importPath+"_pb")))

		// If it starts with .., it went above the proto root — shouldn't happen, skip
		if strings.HasPrefix(absProtoPath, "..") {
			return match
		}

		// Map the absolute proto path to the output path under types/
		outputPath := protoPathToOutputPath(absProtoPath, serviceStateFiles)

		// Compute relative path from fileDir to the output path
		relImport, err := filepath.Rel(fileDir, outputPath)
		if err != nil {
			return match
		}
		relImport = filepath.ToSlash(relImport)

		// Ensure it starts with ./ or ../
		if !strings.HasPrefix(relImport, ".") {
			relImport = "./" + relImport
		}

		return fmt.Sprintf(`from "%s"`, relImport)
	})

	return content
}

// createSpecPackage ensures the sys/spec directory exists.
func createSpecPackage(outputDir string) error {
	specDir := filepath.Join(outputDir, "types", "sys", "spec")
	if err := os.MkdirAll(specDir, 0755); err != nil {
		return fmt.Errorf("failed to create spec directory: %w", err)
	}

	helpersPath := filepath.Join(specDir, "helpers.ts")
	if _, err := os.Stat(helpersPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("failed to stat helpers.ts: %w", err)
	}

	helpersContent := `// Auto-generated helpers for spec package
// This package provides annotation-related helpers used by the compiler.
export {};
`
	if err := os.WriteFile(helpersPath, []byte(helpersContent), 0644); err != nil {
		return fmt.Errorf("failed to write helpers.ts: %w", err)
	}

	return nil
}

// generateAggregationFiles creates group-level and root-level aggregation files
// for gRPC servers, HTTP clients, and state clients to match Go's layout.
func generateAggregationFiles(config *tactype.Config) error {
	smDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "internal")

	categories := []struct {
		dir  string
		name string
	}{
		{"grpc/servers", "grpc_servers"},
		{"state", "state"},
		// rest aggregation is handled by generateTsRestClientAggregators
		// to avoid re-exporting the RestTransport interface from multiple files
	}

	for _, cat := range categories {
		catDir := filepath.Join(smDir, cat.dir)
		if _, err := os.Stat(catDir); os.IsNotExist(err) {
			continue
		}

		// Find group directories (e.g., "services", "common", "sys")
		entries, err := os.ReadDir(catDir)
		if err != nil {
			continue
		}

		var groups []string
		for _, e := range entries {
			if e.IsDir() {
				groups = append(groups, e.Name())
			}
		}

		// Generate group-level aggregation file for each group
		for _, group := range groups {
			groupDir := filepath.Join(catDir, group)
			if err := generateTsGroupAggregator(groupDir, cat.name); err != nil {
				return fmt.Errorf("failed to generate group %s/%s: %w", cat.dir, group, err)
			}
		}

		// Generate root-level aggregation file
		if err := generateTsRootAggregator(catDir, cat.name, groups); err != nil {
			return fmt.Errorf("failed to generate root %s: %w", cat.dir, err)
		}
	}

	return nil
}

// generateTsGroupAggregator creates a group-level aggregation file (e.g., sm/internal/grpc_clients/services/http_clients.ts).
func generateTsGroupAggregator(groupDir, fileName string) error {
	// Find all subdirectories with .ts files
	entries, err := os.ReadDir(groupDir)
	if err != nil {
		return nil
	}

	// Collect exports per subdirectory to detect naming conflicts
	type subDirExport struct {
		dir      string
		baseName string
	}
	var allExports []subDirExport
	exportNames := make(map[string]int) // baseName -> count of subdirs exporting it
	dirFiles := make(map[string][]string)

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		subDir := filepath.Join(groupDir, e.Name())
		subEntries, err := os.ReadDir(subDir)
		if err != nil {
			continue
		}
		dirBaseNames := make(map[string]bool)
		for _, se := range subEntries {
			if se.IsDir() || !strings.HasSuffix(se.Name(), ".ts") {
				continue
			}
			if se.Name() == "index.ts" || se.Name() == fileName+".ts" {
				continue
			}
			baseName := strings.TrimSuffix(se.Name(), ".ts")
			allExports = append(allExports, subDirExport{dir: e.Name(), baseName: baseName})
			dirFiles[e.Name()] = append(dirFiles[e.Name()], baseName)
			dirBaseNames[baseName] = true
		}
		// Count each subdir that has at least one file
		for bn := range dirBaseNames {
			exportNames[bn]++
		}
	}

	// If any file name appears in multiple subdirs, use namespace re-exports
	// to avoid export ambiguity (e.g. multiple services exporting "StateClient")
	hasConflict := false
	for _, count := range exportNames {
		if count > 1 {
			hasConflict = true
			break
		}
	}

	var exports []string
	seen := make(map[string]bool)
	for _, exp := range allExports {
		if seen[exp.dir] {
			continue
		}
		seen[exp.dir] = true

		subDirFiles := dirFiles[exp.dir]
		subConflict := len(subDirFiles) > 1 && hasExportConflicts(groupDir, exp.dir, subDirFiles)
		if hasConflict || subConflict {
			// Group by directory and use namespace re-exports. This avoids both
			// same-file-name conflicts across subdirs and same-export-name conflicts
			// inside one subdir, such as multiple service wrappers exporting GrpcServer.
			exports = append(exports, fmt.Sprintf("export * as %s from './%s/index.js';", exp.dir, exp.dir))

			var indexLines []string
			indexLines = append(indexLines, "// Auto-generated index")
			for _, bn := range subDirFiles {
				if subConflict {
					indexLines = append(indexLines, fmt.Sprintf("export * as %s from './%s.js';", bn, bn))
				} else {
					indexLines = append(indexLines, fmt.Sprintf("export * from './%s.js';", bn))
				}
			}
			indexContent := strings.Join(indexLines, "\n") + "\n"
			indexPath := filepath.Join(groupDir, exp.dir, "index.ts")
			if err := os.WriteFile(indexPath, []byte(indexContent), 0644); err != nil {
				return fmt.Errorf("write index.ts for %s: %w", exp.dir, err)
			}
			continue
		}

		for _, bn := range subDirFiles {
			exports = append(exports, fmt.Sprintf("export * from './%s/%s.js';", exp.dir, bn))
		}
	}

	if len(exports) == 0 {
		return nil
	}

	content := "// Auto-generated aggregator\n" + strings.Join(exports, "\n") + "\n"
	outputPath := filepath.Join(groupDir, fileName+".ts")
	return os.WriteFile(outputPath, []byte(content), 0644)
}

// hasExportConflicts checks whether files in a subdirectory have conflicting exported names.
func hasExportConflicts(groupDir, subDir string, fileNames []string) bool {
	exportRe := regexp.MustCompile(`(?m)^export\s+(?:function|class|interface|type|enum|const|let|var|abstract\s+class)\s+(\w+)`)
	nameToFiles := make(map[string]int)
	for _, bn := range fileNames {
		fp := filepath.Join(groupDir, subDir, bn+".ts")
		content, err := os.ReadFile(fp)
		if err != nil {
			continue
		}
		matches := exportRe.FindAllStringSubmatch(string(content), -1)
		seen := make(map[string]bool)
		for _, m := range matches {
			if !seen[m[1]] {
				seen[m[1]] = true
				nameToFiles[m[1]]++
			}
		}
	}
	for _, count := range nameToFiles {
		if count > 1 {
			return true
		}
	}
	return false
}

// generateTsRootAggregator creates a root-level aggregation file (e.g., sm/internal/grpc_clients/http_clients.ts).
func generateTsRootAggregator(catDir, fileName string, groups []string) error {
	var exports []string
	for _, group := range groups {
		// Check if group has an aggregation file
		groupAgg := filepath.Join(catDir, group, fileName+".ts")
		if _, err := os.Stat(groupAgg); err == nil {
			exports = append(exports, fmt.Sprintf("export * as %s from './%s/%s.js';", group, group, fileName))
		}
	}

	if len(exports) == 0 {
		return nil
	}

	content := "// Auto-generated root aggregator\n" + strings.Join(exports, "\n") + "\n"
	outputPath := filepath.Join(catDir, fileName+".ts")
	return os.WriteFile(outputPath, []byte(content), 0644)
}
