// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/boundaries"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/header"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/consts"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
	spec "gitlab.com/secondstarcavalier/schematic/tacplus/target/proto/spec/sys"
)

// Generate generates Go code from proto files
// Implements a multi-pass build system:
// 1. Copy proto files with go_package options
// 2. Generate Go code from proto files using protoc
// 3. Generate gRPC stub code for services
// 4. Generate service wrappers
// 5. Copy generated helper files (helpers.go)
// 6. Copy static packages (utils, interfaces)
// 7. Clean up stale generated files
// 8. Generate HTTP typed clients
func Generate(config *tactype.Config) error {
	// Resolve protoc paths
	if err := config.ResolveProtocPaths(); err != nil {
		return err
	}

	// Create builder temp directory for intermediate files
	builderTempDir, err := os.MkdirTemp("", "tacplus-builder-")
	if err != nil {
		return fmt.Errorf("failed to create builder temp directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(builderTempDir); err != nil {
			zap.L().Warn("failed to remove directory", zap.String("path", builderTempDir), zap.Error(err))
		}
	}()

	// Use the de-circularized flat copy of protos (set by builder) for protoc-based
	// parsing that needs the original flat structure without circular import errors.
	flatProtoDir := config.FlatProtoDir

	// Create proto directory for intermediate proto files
	// OutputDir is already the root directory (e.g., "go")
	rootOutputDir := config.OutputDir
	// All generated code goes under types/ subdirectory
	typesOutputDir := filepath.Join(rootOutputDir, "types")
	tempProtoDir := filepath.Join(rootOutputDir, "proto")

	// Delete the entire output directory to ensure a clean slate
	if _, err := os.Stat(rootOutputDir); err == nil {
		if err := os.RemoveAll(rootOutputDir); err != nil {
			return fmt.Errorf("failed to remove existing output directory: %w", err)
		}
		zap.L().Info("Removed existing output directory for clean regeneration", zap.String("dir", rootOutputDir))
	}

	if err := os.MkdirAll(tempProtoDir, 0755); err != nil {
		return fmt.Errorf("failed to create proto directory: %w", err)
	}
	// Note: We keep the proto directory (not deleted) for debugging and reference

	// Copy proto files with go_package options
	zap.L().Info("Copying proto files with go_package options")
	protoFilePaths, err := copyProtoFiles(config, tempProtoDir)
	if err != nil {
		return fmt.Errorf("failed to copy proto files: %w", err)
	}

	// Overlay embedded sys protos into tempProtoDir so protoc can resolve imports.
	// Sys are embedded in tacplus and may not exist in the user's source proto directory.
	if err := proto.CopySysProtos(tempProtoDir); err != nil {
		return fmt.Errorf("failed to copy sys protos to temp dir: %w", err)
	}

	// Add sys protos to the compilation list — they contain types (Error, Lock, etc.)
	// that need .pb.go files in the output.
	sysDir := filepath.Join(tempProtoDir, "sys")
	if entries, err := os.ReadDir(sysDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".proto") && entry.Name() != "servicename.proto" {
				protoFilePaths = append(protoFilePaths, filepath.Join("sys", entry.Name()))
			}
		}
	}

	// Add embedded common protos to the compilation list. These are shared
	// runtime request/response types used by generated identity-manager code.
	commonDir := filepath.Join(tempProtoDir, "common")
	if entries, err := os.ReadDir(commonDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".proto") {
				protoFilePaths = append(protoFilePaths, filepath.Join("common", entry.Name()))
			}
		}
	}

	// Add google/api protos to the compilation list — services that use
	// google.api.http annotations import the generated google/api package.
	googleAPIDir := filepath.Join(tempProtoDir, "google", "api")
	if entries, err := os.ReadDir(googleAPIDir); err == nil {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".proto") {
				protoFilePaths = append(protoFilePaths, filepath.Join("google", "api", entry.Name()))
			}
		}
	}

	// Add go_package to any sys protos that were embedded and don't have it yet.
	// copyProtoFiles only processes user-provided protos from InputDir, so embedded
	// sys protos need go_package injection separately.
	if err := addGoPackageToSysProtos(config, tempProtoDir); err != nil {
		return fmt.Errorf("failed to add go_package to sys protos: %w", err)
	}

	// Add go_package to embedded common protos.
	if err := addGoPackageToEmbeddedProtos(config, tempProtoDir, "common"); err != nil {
		return fmt.Errorf("failed to add go_package to common protos: %w", err)
	}

	// Add go_package to embedded google/api protos
	if err := addGoPackageToEmbeddedProtos(config, tempProtoDir, "google/api"); err != nil {
		return fmt.Errorf("failed to add go_package to google/api protos: %w", err)
	}

	// Generate generated/*.proto from grpc_config annotations.
	// Must happen before any descriptor parser runs (service protos may import it).
	_, svcRPCs, vsEntries, err := proto.ScanAndGenerateConfigHolder(config.InputDir, tempProtoDir)
	if err != nil {
		return fmt.Errorf("failed to generate generated/*.proto: %w", err)
	}

	// Inject the MCP session carrier into every service that declares an
	// RPC_PRINCIPAL_MCP tool. It has to be a real rpc in the tree protoc
	// compiles, and this is the first point at which every import a service
	// proto can name (generated/, sys/) resolves, so the descriptor parse it
	// needs succeeds. A tree with no MCP rpc is untouched.
	mcpCarriers, err := proto.AppendMcpCarrierRpcsToProtos(tempProtoDir, config.InputDir)
	if err != nil {
		return fmt.Errorf("failed to inject MCP session carrier rpcs: %w", err)
	}
	if len(mcpCarriers) > 0 {
		// The scan above ran over the un-injected tree, so the carrier is missing
		// from its service's <Svc>RPCs enum and TokenEntitlements mapping. A c2s
		// carrier needs an enum value exactly like the rpcs declared beside it.
		_, svcRPCs, vsEntries, err = proto.ScanAndGenerateConfigHolder(config.InputDir, tempProtoDir)
		if err != nil {
			return fmt.Errorf("failed to regenerate generated/*.proto after MCP carrier injection: %w", err)
		}
	}
	if err := proto.AppendEventBusWrappersToProtos(tempProtoDir, flatProtoDir); err != nil {
		return fmt.Errorf("failed to append eventbus topic wrappers to protos: %w", err)
	}
	if proto.NeedsEventBusSupportProto(tempProtoDir) {
		eventBusProto, err := proto.EnsureEventBusSupportProto(tempProtoDir, proto.EventBusSupportProtoOptions{
			GoPackage: fmt.Sprintf("%s/types/generated/eventbus_support", config.ModuleBasePath),
		})
		if err != nil {
			return fmt.Errorf("failed to generate eventbus support proto: %w", err)
		}
		if err := proto.ValidateEventBusSupportWireContract(tempProtoDir, eventBusProto, flatProtoDir); err != nil {
			return err
		}
		if eventBusProto != "" && !slices.Contains(protoFilePaths, eventBusProto) {
			protoFilePaths = append(protoFilePaths, eventBusProto)
		}
	}

	// Generate virtual service protos into generated/.
	// Must happen before the generated proto registration below so they're
	// included in the compilation list.
	opaEvalProtos, err := proto.GenerateOpaEvalProto(tempProtoDir, flatProtoDir)
	if err != nil {
		return fmt.Errorf("failed to generate OpaEval virtual service proto: %w", err)
	}

	// Validate virtual service import annotations.
	// This checks that opa_import, graph_import, and timeseries_import target
	// services that actually implement the corresponding virtual service.
	if err := proto.ValidateVirtualImports(flatProtoDir); err != nil {
		return err
	}
	if err := proto.ValidateRecordLinkage(flatProtoDir); err != nil {
		return err
	}

	// Auto-derive virtual service clients from proto annotations.
	// Services with opa_import, graph_import, or timeseries_import get
	// corresponding gRPC client entries with TargetService set.
	virtualClients, err := proto.DeriveVirtualServiceClients(flatProtoDir, config.SelfPackage)
	if err != nil {
		return fmt.Errorf("failed to derive virtual service clients: %w", err)
	}
	config.GrpcS2SClients = append(config.GrpcS2SClients, virtualClients...)

	// Infer gRPC client dependencies from grpc_s2s_client/c2s/w2s proto annotations.
	if err := service.InferGrpcClientsFromAnnotations(config, tempProtoDir); err != nil {
		return fmt.Errorf("failed to infer gRPC clients from proto annotations: %w", err)
	}

	// Add generated/*.proto files to the compilation list and inject go_package.
	// They are created after the sys scan above, so we add them explicitly.
	type genProtoInfo struct {
		file, goPackage, protoPackage string
	}
	genProtos := []genProtoInfo{
		{"generated/services.proto", fmt.Sprintf("%s/types/generated/services", config.ModuleBasePath), "services"},
		{"generated/entitlements.proto", fmt.Sprintf("%s/types/generated/entitlements", config.ModuleBasePath), "entitlements"},
		{"generated/accounting_method.proto", fmt.Sprintf("%s/types/generated/accounting_method", config.ModuleBasePath), "accounting_method"},
		{"generated/services_config.proto", fmt.Sprintf("%s/types/generated/services_config", config.ModuleBasePath), "services_config"},
	}
	// OpaEval is now appended to the source vservices proto (no separate generated file).
	for _, gp := range genProtos {
		genPath := filepath.Join(tempProtoDir, gp.file)
		if _, err := os.Stat(genPath); err == nil {
			protoFilePaths = append(protoFilePaths, gp.file)
			if content, err := os.ReadFile(genPath); err == nil {
				if !strings.Contains(string(content), "go_package") {
					modified := strings.Replace(string(content),
						"package "+gp.protoPackage+";",
						fmt.Sprintf("package %s;\noption go_package = \"%s\";", gp.protoPackage, gp.goPackage), 1)
					os.WriteFile(genPath, []byte(modified), 0644)
				}
			}
		}
	}

	// Inject per-service ConfigState messages into each service's own proto file.
	// Each grpc_config-annotated Config message gets a corresponding *ConfigState
	// service_state boundary appended to its proto file.
	zap.L().Info("Injecting per-service ConfigState messages")
	feConfigs, err := proto.InjectGrpcConfigState(config.InputDir, tempProtoDir, flatProtoDir)
	if err != nil {
		return fmt.Errorf("failed to inject grpc config state: %w", err)
	}
	if err := proto.GenerateTenantConfig(config.InputDir, flatProtoDir, tempProtoDir); err != nil {
		return fmt.Errorf("failed to generate tenant_config.proto: %w", err)
	}
	if err := proto.GenerateProjectConfig(config.InputDir, flatProtoDir, tempProtoDir); err != nil {
		return fmt.Errorf("failed to generate project_config.proto: %w", err)
	}
	// Inject go_package into the freshly-written tenant_config.proto and add it
	// to the compilation list. Must happen after GenerateTenantConfig since that
	// call overwrites any previously-injected go_package.
	tenantConfigPath := filepath.Join(tempProtoDir, "generated/tenant_config.proto")
	if content, err := os.ReadFile(tenantConfigPath); err == nil {
		if !strings.Contains(string(content), "go_package") {
			goPackage := fmt.Sprintf("%s/types/generated/tenant_config", config.ModuleBasePath)
			modified := strings.Replace(string(content),
				"package tenant_config;",
				fmt.Sprintf("package tenant_config;\noption go_package = %q;", goPackage), 1)
			_ = os.WriteFile(tenantConfigPath, []byte(modified), 0644)
		}
		if !slices.Contains(protoFilePaths, "generated/tenant_config.proto") {
			protoFilePaths = append(protoFilePaths, "generated/tenant_config.proto")
		}
	}
	// Same for the freshly-written project_config.proto.
	projectConfigPath := filepath.Join(tempProtoDir, "generated/project_config.proto")
	if content, err := os.ReadFile(projectConfigPath); err == nil {
		if !strings.Contains(string(content), "go_package") {
			goPackage := fmt.Sprintf("%s/types/generated/project_config", config.ModuleBasePath)
			modified := strings.Replace(string(content),
				"package project_config;",
				fmt.Sprintf("package project_config;\noption go_package = %q;", goPackage), 1)
			_ = os.WriteFile(projectConfigPath, []byte(modified), 0644)
		}
		if !slices.Contains(protoFilePaths, "generated/project_config.proto") {
			protoFilePaths = append(protoFilePaths, "generated/project_config.proto")
		}
	}
	if err := proto.ResolveStatesFromProto(config, config.FlatProtoDir); err != nil {
		return err
	}
	proto.InjectConfigStates(config, feConfigs)
	proto.InjectTenantConfigSubscriptions(config, config.FlatProtoDir, feConfigs)
	if err := proto.InjectHealthState(config, config.FlatProtoDir); err != nil {
		return fmt.Errorf("failed to inject health state: %w", err)
	}

	// Append state/store service definitions BEFORE trace injection so that
	// auto-appended message types also receive trace fields.
	// Inject per-field "<field>_redacted" indicator bools before native compilation
	// so they appear in every language's generated type.
	if err := proto.InjectRedactIndicatorFields(tempProtoDir, flatProtoDir); err != nil {
		return fmt.Errorf("failed to inject redact indicator fields: %w", err)
	}
	// Generate the parallel <T>Entitlement proto messages (native compilation then
	// produces the entitlement types in every language).
	if err := proto.AppendEntitlementMessagesToProtos(tempProtoDir, flatProtoDir); err != nil {
		return fmt.Errorf("failed to append entitlement messages: %w", err)
	}

	zap.L().Info("Appending state service definitions to service protos")
	if err := proto.AppendStateServiceToProtos(tempProtoDir, tempProtoDir); err != nil {
		return fmt.Errorf("failed to append state service to protos: %w", err)
	}
	if err := proto.AppendStoreServiceToProtos(tempProtoDir, flatProtoDir); err != nil {
		return fmt.Errorf("failed to append store service to protos: %w", err)
	}
	if err := proto.GenerateTransformationMessages(tempProtoDir, flatProtoDir); err != nil {
		return fmt.Errorf("failed to generate projection messages: %w", err)
	}
	if err := proto.AppendRecordMapServiceToProtos(tempProtoDir, flatProtoDir, config.SelfPackage); err != nil {
		return fmt.Errorf("failed to append record map service to protos: %w", err)
	}
	// TimeseriesGroupCounts and TimeseriesQuery used to be injected here, onto
	// the proto of every timeseries_store. Removed 2026-08-25 with the timedb
	// boneyard (BIG GOAL 3, first move #1): there is no timeseries_store in any
	// proto set any more, so both injectors could only ever no-op, and neither
	// ever had a consumer (timedb's own build.rs: "No consumer wires
	// timedb.TimeseriesQuery yet"). The typed query surface is the record plane.
	// If this service is the OPA evaluator, register one OpaEval gRPC server per
	// vservice proto file that contains spec.opa_eval definitions. Each file
	// contributes its own OpaEval service in its own package (e.g. backendtoken.OpaEval,
	// userentitlements.OpaEval), all served on the same gRPC server instance.
	if len(opaEvalProtos) > 0 {
		isOpaServer, err := proto.IsOpaServer(flatProtoDir, config.SelfPackage)
		if err != nil {
			return fmt.Errorf("failed to check OPA server status: %w", err)
		}
		if isOpaServer {
			for _, path := range opaEvalProtos {
				config.GrpcServers = append(config.GrpcServers, tactype.GrpcEntry{
					ProtoFile:   path,
					ServiceName: "OpaEval",
				})
			}
		}
	}

	// Inject trace round fields into all message types.
	// Parse from tempProtoDir so appended types are visible.
	skippedTraceTypes, err := proto.InjectTraceFields(tempProtoDir, tempProtoDir)
	if err != nil {
		return fmt.Errorf("failed to inject trace fields: %w", err)
	}
	if err := proto.SwapEmptyInputs(tempProtoDir); err != nil {
		return fmt.Errorf("failed to swap empty inputs to TraceRequest: %w", err)
	}

	zap.L().Info("Injecting registry enums into rewritten proto files")
	if err := proto.InjectRegistryEnums(tempProtoDir); err != nil {
		return fmt.Errorf("failed to inject registry enums into protos: %w", err)
	}

	// Add go_package to any existing mcp.proto so it compiles if present in source protos.
	mcpPath := filepath.Join(tempProtoDir, "services", "mcp.proto")
	if _, err := os.Stat(mcpPath); err == nil {
		content, err := os.ReadFile(mcpPath)
		if err == nil && !strings.Contains(string(content), "go_package") {
			goPackage := config.TypesModulePath() + "/types/services/mcp"
			modified := strings.Replace(string(content),
				"package mcp;",
				fmt.Sprintf("package mcp;\noption go_package = \"%s\";", goPackage),
				1)
			if err := os.WriteFile(mcpPath, []byte(modified), 0644); err != nil {
				return fmt.Errorf("failed to write mcp.proto with go_package: %w", err)
			}
			protoFilePaths = append(protoFilePaths, filepath.Join("services", "mcp.proto"))
		}
	}

	// Parse all proto files and generate helpers.
	// Runs after all proto modifications (Lock injection, state service appending)
	// so helpers are generated for all messages including auto-generated state types.
	zap.L().Info("Parsing proto files and generating helpers...")
	allParsedFiles, err := processProtoFiles(tempProtoDir, builderTempDir, config)
	if err != nil {
		return fmt.Errorf("failed to process proto files: %w", err)
	}

	// Generate timeseries typed helpers (ToTimeseriesRow/FromTimeseriesRow/DDL/ColumnNames)
	// for messages with (spec.timeseries_type) annotation.
	if err := generateTimeseriesHelpers(allParsedFiles, builderTempDir); err != nil {
		return fmt.Errorf("failed to generate timeseries helpers: %w", err)
	}

	// Generate extraction functions (Extract<Projection>From<Source>) for messages
	// with a (spec.timeseries_type).source annotation.
	if err := generateExtractionHelpers(allParsedFiles, builderTempDir); err != nil {
		return fmt.Errorf("failed to generate extraction helpers: %w", err)
	}

	// Generate store-agnostic event handlers (Handle<Source>) that dispatch source
	// events to all their graph and timeseries projections via callbacks.
	if err := generateEventHandlers(allParsedFiles, builderTempDir); err != nil {
		return fmt.Errorf("failed to generate event handlers: %w", err)
	}

	// Generate Go code from proto files (output to types/ subdirectory)
	zap.L().Info("Generating Go code from proto files")
	if err := generateProtoCode(config, protoFilePaths, tempProtoDir, typesOutputDir); err != nil {
		return fmt.Errorf("failed to generate proto code: %w", err)
	}

	// Generate builtin entitlement profiles from discovered service RPCs
	if len(svcRPCs) > 0 {
		if err := generateBuiltinProfiles(config, svcRPCs, typesOutputDir); err != nil {
			return fmt.Errorf("failed to generate builtin profiles: %w", err)
		}
	}

	// Generate gRPC stub code for services (output to types/ subdirectory)
	zap.L().Info("Generating gRPC stub code for services")
	servicesInfo, err := generateGrpcCode(config, protoFilePaths, tempProtoDir, typesOutputDir)
	if err != nil {
		return fmt.Errorf("failed to generate gRPC code: %w", err)
	}

	// Build grpcName → AccountingInfo mapping for accounting method generation.
	// Methods not in AccountingInfo.RPCs are skipped (e.g., synthesized state
	// service RPCs that share a grpcName but aren't in the accounting enum).
	accountingMap := make(map[string]service.AccountingInfo, len(svcRPCs))
	for _, svc := range svcRPCs {
		rpcs := make(map[string]bool, len(svc.RPCs))
		for _, rpc := range svc.RPCs {
			rpcs[rpc] = true
		}
		accountingMap[svc.GrpcName] = service.AccountingInfo{EnumName: svc.EnumName, RPCs: rpcs}
	}

	// Generate service wrappers for gRPC server services.
	var serverServices []ServiceInfo
	if len(servicesInfo) > 0 {
		serverServices, err = resolveServerServices(config, servicesInfo)
		if err != nil {
			return fmt.Errorf("failed to resolve gRPC server services: %w", err)
		}
		if len(serverServices) > 0 {
			if _, err := generateServiceWrappers(config, serverServices, tempProtoDir, accountingMap, skippedTraceTypes); err != nil {
				return fmt.Errorf("failed to generate service wrappers: %w", err)
			}
		}
	}

	// Token-minter discovery (service_im / state client) reads spec.* service
	// annotations from a protoc-resolvable tree, so expose the injected,
	// not-yet-stripped tempProtoDir before any discovery runs.
	config.AnnotatedProtoDir = tempProtoDir
	if err := parser.ValidateTokenMinters(config.AnnotatedProtoDir, s2sGenerationEnabled(config, serverServices)); err != nil {
		return err
	}

	// Generate handler interfaces and service manager.
	if len(serverServices) > 0 || len(config.States) > 0 {
		// Ensure sys/service_im and sys/servicemanager are generated — sm.go imports both.
		// Skip in sys-only mode: no token exchange service proto exists.
		if !config.SysOnly {
			if err := generateIdentityManager(config); err != nil {
				return fmt.Errorf("failed to generate identity manager: %w", err)
			}
			if err := generateStateClientLib(config); err != nil {
				return fmt.Errorf("failed to generate servicemanager lib: %w", err)
			}
		}
		var grpcSvcs []ServiceInfo
		if len(serverServices) > 0 {
			explicit := filterExplicitGrpcServices(config, serverServices)
			if len(explicit) > 0 {
				grpcSvcs = explicit
			} else {
				for _, svc := range serverServices {
					if svc.GrpcName != "" {
						grpcSvcs = append(grpcSvcs, svc)
					}
				}
			}
		}
		// Collect event-bus watches from legacy subscribe, explicit event_watch,
		// and store-derived subscriptions. Parse tempProtoDir (not flatProtoDir):
		// it is a superset that includes the generated projection messages
		// (timeseries_type), so a timeseries_store derives its subscriptions from
		// those projections. Parsing flatProtoDir missed them, so a store
		// silently auto-subscribed to nothing.
		config.Subscriptions, err = collectSubscriptions(config, tempProtoDir)
		if err != nil {
			return fmt.Errorf("failed to collect event-bus subscriptions: %w", err)
		}
		if len(config.Subscriptions) > 0 {
			zap.L().Info("Found event-bus subscriptions", zap.Int("count", len(config.Subscriptions)))
		}

		zap.L().Info("Generating handler interface and service_manager")
		if err := generateHandlerInterfaces(config, grpcSvcs, allParsedFiles); err != nil {
			return fmt.Errorf("failed to generate handler interfaces: %w", err)
		}
		if err := generateServiceManager(config, grpcSvcs, allParsedFiles); err != nil {
			return fmt.Errorf("failed to generate service_manager: %w", err)
		}

		// Generate per-topic event-bus subscriber packages.
		if len(config.Subscriptions) > 0 {
			if err := generateGoSubscribers(config); err != nil {
				return fmt.Errorf("failed to generate subscribers: %w", err)
			}
		}
	}

	// Generate gRPC client wrappers — typed client stubs for gRPC client services
	hasGrpcS2SClients := false
	if len(servicesInfo) > 0 {
		clientServices, err := resolveClientServices(config, servicesInfo)
		if err != nil {
			return fmt.Errorf("failed to resolve s2s gRPC client services: %w", err)
		}
		if len(clientServices) > 0 {
			hasGrpcS2SClients = true
			zap.L().Info("Generating Go gRPC client wrappers", zap.Int("count", len(clientServices)))
			generatedClients, err := generateGrpcClients(config, clientServices, tempProtoDir)
			if err != nil {
				return fmt.Errorf("failed to generate gRPC clients: %w", err)
			}
			if len(generatedClients) > 0 {
				if err := generateGrpcClientRoot(config, generatedClients); err != nil {
					return fmt.Errorf("failed to generate root gRPC clients: %w", err)
				}
			}
		}
	}

	// Generate grpc aggregator (s2sm/internal/grpc/grpc.go) — needed whenever
	// the service has gRPC servers or clients (s2sm.go imports smgrpc).
	// Auto-discovered server services (from SelfPackage) also count.
	hasGrpcServers := len(config.GrpcServers) > 0 || config.GrpcServerAll || len(serverServices) > 0
	if hasGrpcS2SClients || hasGrpcServers {
		if err := generateGrpcAggregator(config, hasGrpcS2SClients); err != nil {
			return fmt.Errorf("failed to generate gRPC aggregator: %w", err)
		}
	}

	// hasClientManagers tracks whether this build emitted any client-facing
	// service manager (c2sm/c2dm/w2sm). When true the build authenticates as a
	// client (CLI/daemon/webapp) and gets the client identity manager (client_im),
	// which mints via the client-reachable C2S exchanger.
	hasClientManagers := false

	// Generate c2sm (client-to-service) gRPC client wrappers
	if len(servicesInfo) > 0 {
		c2sServices, err := resolveC2SClientServices(config, servicesInfo)
		if err != nil {
			return fmt.Errorf("failed to resolve c2s gRPC client services: %w", err)
		}
		if len(c2sServices) > 0 {
			// Temporarily set SmPrefix to "c2sm" for output path
			origPrefix := config.SmPrefix
			config.SmPrefix = "c2sm"
			zap.L().Info("Generating Go c2s gRPC client wrappers", zap.Int("count", len(c2sServices)))
			generatedClients, err := generateGrpcClients(config, c2sServices, tempProtoDir)
			if err != nil {
				config.SmPrefix = origPrefix
				return fmt.Errorf("failed to generate c2s gRPC clients: %w", err)
			}
			if len(generatedClients) > 0 {
				if err := generateGrpcClientRoot(config, generatedClients); err != nil {
					config.SmPrefix = origPrefix
					return fmt.Errorf("failed to generate root c2s gRPC clients: %w", err)
				}
				if err := generateGrpcAggregator(config, true); err != nil {
					config.SmPrefix = origPrefix
					return fmt.Errorf("failed to generate c2s gRPC aggregator: %w", err)
				}
				if err := generateC2SServiceManager(config, generatedClients); err != nil {
					config.SmPrefix = origPrefix
					return fmt.Errorf("failed to generate c2s service manager: %w", err)
				}
				hasClientManagers = true
			}
			config.SmPrefix = origPrefix
		}
	}

	// Generate c2dm (client-to-daemon) gRPC client wrappers when the proto graph
	// declares in-scope service_c2d services (or an explicit GrpcC2DClients allowlist).
	if c2dServices := resolveC2DClientServices(config, servicesInfo); len(c2dServices) > 0 {
		origPrefix := config.SmPrefix
		config.SmPrefix = "c2dm"
		zap.L().Info("Generating Go c2d gRPC client wrappers", zap.Int("count", len(c2dServices)))
		generatedClients, err := generateGrpcClients(config, c2dServices, tempProtoDir)
		if err != nil {
			config.SmPrefix = origPrefix
			return fmt.Errorf("failed to generate c2d gRPC clients: %w", err)
		}
		if len(generatedClients) > 0 {
			if err := generateGrpcClientRoot(config, generatedClients); err != nil {
				config.SmPrefix = origPrefix
				return fmt.Errorf("failed to generate root c2d gRPC clients: %w", err)
			}
			if err := generateGrpcAggregator(config, true); err != nil {
				config.SmPrefix = origPrefix
				return fmt.Errorf("failed to generate c2d gRPC aggregator: %w", err)
			}
			if err := generateC2DServiceManager(config, generatedClients); err != nil {
				config.SmPrefix = origPrefix
				return fmt.Errorf("failed to generate c2d service manager: %w", err)
			}
			hasClientManagers = true
		}
		config.SmPrefix = origPrefix
	}

	// Generate w2sm (web-to-service) W2S client wrappers
	if len(servicesInfo) > 0 {
		w2sServices, err := resolveW2SClientServices(config, servicesInfo)
		if err != nil {
			return fmt.Errorf("failed to resolve w2s client services: %w", err)
		}
		if len(w2sServices) > 0 {
			origPrefix := config.SmPrefix
			config.SmPrefix = "w2sm"
			zap.L().Info("Generating Go w2s clients", zap.Int("count", len(w2sServices)))
			if err := generateW2SClients(w2sServices, tempProtoDir); err != nil {
				config.SmPrefix = origPrefix
				return fmt.Errorf("failed to generate w2s clients: %w", err)
			}
			if err := generateW2SServiceManager(config); err != nil {
				config.SmPrefix = origPrefix
				return fmt.Errorf("failed to generate w2s service manager: %w", err)
			}
			hasClientManagers = true
			config.SmPrefix = origPrefix
		}
	}

	// Emit the client identity manager (client_im) for builds that authenticate
	// as a client (CLI/daemon/webapp): any c2sm/c2dm/w2sm surface. Skip in
	// sys-only mode. client_im mints via the client-reachable C2S exchanger, not
	// the authoritative S2S mint (service_im).
	if hasClientManagers && !config.SysOnly {
		if err := generateClientIdentityManager(config); err != nil {
			return fmt.Errorf("failed to generate client identity manager: %w", err)
		}
	}

	// Generate per-type HTTP clients
	restPackages := resolveRestClientPackages(config, allParsedFiles)
	if len(restPackages) > 0 {
		zap.L().Info("Generating per-type HTTP clients", zap.Int("packages", len(restPackages)))
		generatedRestClients, err := generateRestClients(config, restPackages)
		if err != nil {
			return fmt.Errorf("failed to generate REST clients: %w", err)
		}
		if len(generatedRestClients) > 0 {
			if err := generateRestClientRoot(config, generatedRestClients); err != nil {
				return fmt.Errorf("failed to generate root REST clients: %w", err)
			}
		}
	}

	// Generate per-service_store typed disk-persistence helpers (UC1 snapshot +
	// UC2 redo log), wired to the generated ToJSON/FromJSON/Validate. No-op when
	// there are no service_store types.
	if err := generateDiskStore(config, tempProtoDir); err != nil {
		return fmt.Errorf("failed to generate diskstore helpers: %w", err)
	}

	// Generate the top-level registry for the full tacplus build (under types/).
	if err := generateRegistry(config, servicesInfo, tempProtoDir, typesOutputDir, allParsedFiles); err != nil {
		return fmt.Errorf("failed to generate registry: %w", err)
	}

	// Generate state wrappers — owned service gets mutators, imports get read-only.
	// Uses message-first approach: walks proto files directly for service_state annotations
	// rather than iterating servicesInfo (which can miss multi-state-root protos).
	if len(config.States) > 0 || config.AllStates {
		// Generate the shared IM and State sys.
		// Skip in sys-only mode: no token exchange service proto exists.
		if !config.SysOnly {
			if err := generateIdentityManager(config); err != nil {
				return fmt.Errorf("failed to generate identity manager: %w", err)
			}
			if err := generateStateClientLib(config); err != nil {
				return fmt.Errorf("failed to generate stateclient lib: %w", err)
			}
		}

		generatedClients, err := generateStateWrappers(config, tempProtoDir)
		if err != nil {
			return fmt.Errorf("failed to generate state wrappers: %w", err)
		}
		if len(generatedClients) > 0 {
			if err := generateRootStateWrappers(config, generatedClients); err != nil {
				return fmt.Errorf("failed to generate root state wrappers: %w", err)
			}
		}
	}

	// Copy generated helper files
	zap.L().Info("Copying generated helper files")
	if err := copyHelperFiles(builderTempDir, typesOutputDir); err != nil {
		return fmt.Errorf("failed to copy helper files: %w", err)
	}

	// Copy static packages (utils)
	zap.L().Info("Copying static packages")
	if err := copyStaticPackages(config, typesOutputDir); err != nil {
		return fmt.Errorf("failed to copy static packages: %w", err)
	}

	// Clean up stale generated files
	zap.L().Info("Cleaning up stale generated files")
	if err := cleanupStaleFiles(typesOutputDir); err != nil {
		return fmt.Errorf("failed to clean up stale files: %w", err)
	}

	// Final cleanup: Remove stale directories from previous directory structure
	for _, staleDir := range []string{"spec/utils", "spec/types", "spec/interfaces"} {
		fullDir := filepath.Join(typesOutputDir, staleDir)
		if _, err := os.Stat(fullDir); err == nil {
			if err := os.RemoveAll(fullDir); err != nil {
				zap.L().Warn("failed to remove directory", zap.String("path", fullDir), zap.Error(err))
			}
			zap.L().Info("Removed stale directory", zap.String("dir", staleDir))
		}
	}

	// Remove the old top-level registry/ package (replaced by types/registry.go).
	oldRegistryDir := filepath.Join(config.OutputDir, "registry")
	if _, err := os.Stat(oldRegistryDir); err == nil {
		if err := os.RemoveAll(oldRegistryDir); err != nil {
			zap.L().Warn("failed to remove old registry directory", zap.String("path", oldRegistryDir), zap.Error(err))
		} else {
			zap.L().Info("Removed stale registry/ directory")
		}
	}

	// Write library templates
	zap.L().Info("Writing library templates")
	declaredAuds, err := proto.ScanAcceptedAudiences(config.InputDir, config.ServiceLabel())
	if err != nil {
		return fmt.Errorf("failed to scan (spec.service).accepted_audiences: %w", err)
	}
	if err := writeLibTemplates(config, proto.AcceptedAudiencesForHost(config.ServiceLabel(), vsEntries, declaredAuds), svcRPCs); err != nil {
		return fmt.Errorf("failed to write library templates: %w", err)
	}

	// Strip spec.* annotations from output proto files and remove spec.proto.
	// These annotations have already been consumed by the compiler during code generation.
	zap.L().Info("Stripping spec annotations from output protos")
	if err := proto.StripSpecOptionsFromDir(tempProtoDir); err != nil {
		return fmt.Errorf("failed to strip spec options: %w", err)
	}

	if err := formatGeneratedGoFiles(config.OutputDir); err != nil {
		return fmt.Errorf("failed to format generated Go files: %w", err)
	}

	zap.L().Info("Go generation complete!")
	return nil
}

func formatGeneratedGoFiles(root string) error {
	gofmt, err := exec.LookPath("gofmt")
	if err != nil {
		return fmt.Errorf("gofmt not found in PATH")
	}
	var files []string
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			if info.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			files = append(files, path)
		}
		return nil
	}); err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}
	args := append([]string{"-w"}, files...)
	if output, err := exec.Command(gofmt, args...).CombinedOutput(); err != nil {
		return fmt.Errorf("gofmt failed: %w\n%s", err, string(output))
	}
	return nil
}

// protoPackageForPath reads a proto file and extracts the base package name
// (first component before any dot). Returns empty string on error.
func protoPackageForPath(dir, relPath string) string {
	content, err := os.ReadFile(filepath.Join(dir, relPath))
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`(?m)^package\s+([\w.]+)\s*;`)
	matches := re.FindStringSubmatch(string(content))
	if len(matches) < 2 {
		return ""
	}
	return strings.SplitN(matches[1], ".", 2)[0]
}

// copyProtoFiles copies proto files with go_package options
func copyProtoFiles(config *tactype.Config, tempProtoDir string) ([]string, error) {
	var protoFiles []string

	err := filepath.Walk(config.InputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}

		relPath, err := filepath.Rel(config.InputDir, path)
		if err != nil {
			return err
		}

		protoFiles = append(protoFiles, relPath)

		// Read proto file
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", path, err)
		}

		// Extract package name for go_package option construction.
		// Supports dotted package names (e.g., "baseball.types") from directory-based splitting.
		packageRegex := regexp.MustCompile(`(?m)^package\s+([\w.]+)\s*;`)
		matches := packageRegex.FindStringSubmatch(string(content))
		if len(matches) < 2 {
			return fmt.Errorf("no package declaration found in %s", path)
		}
		fullPackageName := matches[1]
		// Base package name is the first component (e.g., "baseball" from "baseball.types")
		packageName := strings.SplitN(fullPackageName, ".", 2)[0]

		// Construct go_package option from the declared proto package.
		// Proto types go into the output directory:
		//   sys/jwk.proto          -> {base}/types/sys/jwk
		//   services/baseball.proto -> {base}/types/services/baseball/baseball
		//   sys/errors.proto       -> {base}/types/sys/errors
		parentDir := filepath.Dir(relPath)
		var goPackage string

		// Handle service protos (with types/ root prefix)
		// Use packageName (from proto declaration) for the import path — the proto
		// package is the source of truth, not the filename.
		if serviceName, _ := proto.ServiceProtoParts(relPath); serviceName != "" {
			goPackage = fmt.Sprintf("%s/types/%s/%s", config.ModuleBasePath, parentDir, packageName)
		} else if filepath.Base(parentDir) == packageName {
			goPackage = fmt.Sprintf("%s/types/%s", config.ModuleBasePath, parentDir)
		} else {
			goPackage = fmt.Sprintf("%s/types/%s/%s", config.ModuleBasePath, parentDir, packageName)
		}

		// Create output directory
		outputPath := filepath.Join(tempProtoDir, relPath)
		outputDir := filepath.Dir(outputPath)
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return err
		}

		// Write proto file with go_package option
		f, err := os.Create(outputPath)
		if err != nil {
			return err
		}
		defer f.Close()

		// Write standard generated code header
		fmt.Fprint(f, header.GeneratedHeader(consts.LangGo, relPath))

		// Strip any existing go_package option before inserting our own
		goPackageStripRegex := regexp.MustCompile(`(?m)^option\s+go_package\s*=\s*"[^"]*"\s*;\s*\n?`)
		strippedContent := goPackageStripRegex.ReplaceAllString(string(content), "")

		// Write content with go_package option inserted after package declaration
		modifiedContent := packageRegex.ReplaceAllString(strippedContent,
			fmt.Sprintf("package %s;\noption go_package = \"%s\";", fullPackageName, goPackage))
		fmt.Fprint(f, modifiedContent)

		zap.L().Debug("Copied proto file", zap.String("src", relPath), zap.String("dst", outputPath))
		return nil
	})

	return protoFiles, err
}

// addGoPackageToSysProtos walks tempProtoDir/sys/ and injects go_package options
// into any proto files that don't already have one (e.g., embedded sys protos).
func addGoPackageToSysProtos(config *tactype.Config, tempProtoDir string) error {
	sysDir := filepath.Join(tempProtoDir, "sys")
	if _, err := os.Stat(sysDir); os.IsNotExist(err) {
		return nil
	}

	packageRegex := regexp.MustCompile(`(?m)^package\s+([\w.]+)\s*;`)
	goPackageRegex := regexp.MustCompile(`(?m)^option\s+go_package\s*=`)

	return filepath.Walk(sysDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return err
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		// Skip if already has go_package
		if goPackageRegex.Match(content) {
			return nil
		}

		relPath, err := filepath.Rel(tempProtoDir, path)
		if err != nil {
			return err
		}

		matches := packageRegex.FindStringSubmatch(string(content))
		if len(matches) < 2 {
			return nil
		}
		packageName := strings.SplitN(matches[1], ".", 2)[0]
		parentDir := filepath.Dir(relPath)

		var goPackage string
		if filepath.Base(parentDir) == packageName {
			goPackage = fmt.Sprintf("%s/types/%s", config.ModuleBasePath, parentDir)
		} else {
			goPackage = fmt.Sprintf("%s/types/%s/%s", config.ModuleBasePath, parentDir, packageName)
		}

		modifiedContent := packageRegex.ReplaceAllString(string(content),
			fmt.Sprintf("package %s;\noption go_package = \"%s\";", matches[1], goPackage))

		if err := os.WriteFile(path, []byte(modifiedContent), 0644); err != nil {
			return err
		}

		zap.L().Debug("Added go_package to sys proto", zap.String("path", relPath))
		return nil
	})
}

// addGoPackageToEmbeddedProtos injects go_package options into embedded proto files
// under the given subdirectory (e.g., "google/api") that don't already have one.
func addGoPackageToEmbeddedProtos(config *tactype.Config, tempProtoDir, subdir string) error {
	dir := filepath.Join(tempProtoDir, subdir)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}

	packageRegex := regexp.MustCompile(`(?m)^package\s+([\w.]+)\s*;`)
	goPackageRegex := regexp.MustCompile(`(?m)^option\s+go_package\s*=`)

	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return err
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		if goPackageRegex.Match(content) {
			return nil
		}

		relPath, err := filepath.Rel(tempProtoDir, path)
		if err != nil {
			return err
		}

		matches := packageRegex.FindStringSubmatch(string(content))
		if len(matches) < 2 {
			return nil
		}
		packageName := strings.SplitN(matches[1], ".", 2)[0]
		parentDir := filepath.Dir(relPath)

		var goPackage string
		if filepath.Base(parentDir) == packageName {
			goPackage = fmt.Sprintf("%s/types/%s", config.ModuleBasePath, parentDir)
		} else {
			goPackage = fmt.Sprintf("%s/types/%s/%s", config.ModuleBasePath, parentDir, packageName)
		}

		modifiedContent := packageRegex.ReplaceAllString(string(content),
			fmt.Sprintf("package %s;\noption go_package = \"%s\";", matches[1], goPackage))

		if err := os.WriteFile(path, []byte(modifiedContent), 0644); err != nil {
			return err
		}

		zap.L().Debug("Added go_package to embedded proto", zap.String("path", relPath))
		return nil
	})
}

// generateProtoCode runs protoc to generate .pb.go files
func generateProtoCode(config *tactype.Config, protoFiles []string, tempProtoDir string, typesOutputDir string) error {
	for _, protoFile := range protoFiles {
		relPath := protoFile

		parentDir := filepath.Dir(relPath)

		// Read the proto file to get the package name (use raw path for file I/O)
		protoPath := filepath.Join(tempProtoDir, relPath)
		content, err := os.ReadFile(protoPath)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", relPath, err)
		}
		packageRegex := regexp.MustCompile(`(?m)^package\s+([\w.]+)\s*;`)
		matches := packageRegex.FindStringSubmatch(string(content))
		if len(matches) < 2 {
			return fmt.Errorf("no package declaration found in %s", relPath)
		}
		fullPackageName := matches[1]
		packageName := strings.SplitN(fullPackageName, ".", 2)[0]

		// Handle service protos — use proto package name for directory, not filename
		var subdir string
		if serviceName, _ := proto.ServiceProtoParts(relPath); serviceName != "" {
			subdir = filepath.Join(parentDir, packageName)
		} else if filepath.Base(parentDir) == packageName {
			subdir = parentDir
		} else {
			subdir = filepath.Join(parentDir, packageName)
		}
		outputDir := filepath.Join(typesOutputDir, subdir)
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return err
		}

		zap.L().Debug("Building Go", zap.String("proto", protoFile), zap.String("outputDir", outputDir))

		args := []string{
			"--go_out=" + outputDir,
			"--go_opt=paths=source_relative",
			"--proto_path=" + tempProtoDir,
		}
		if config.ProtocGenGo != "" {
			args = append([]string{"--plugin=protoc-gen-go=" + config.ProtocGenGo}, args...)
		}
		args = append(args, relPath)

		cmd := exec.Command(config.ProtocPath, args...)

		// Log the command being executed
		zap.L().Debug("Running protoc", zap.String("cmd", config.ProtocPath), zap.Strings("args", cmd.Args[1:]))

		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("protoc failed for %s: %w\n%s", protoFile, err, string(output))
		}

		// Move the generated file from nested path to target location.
		// protoc generates at: outputDir/relPath (with .pb.go instead of .proto).
		// Use the natural protoc output name (e.g., ballsports.pb.go, types.pb.go).
		generatedFile := filepath.Join(outputDir, relPath[:len(relPath)-6]+".pb.go")
		baseName := filepath.Base(relPath[:len(relPath)-6]) + ".pb.go"
		targetFile := filepath.Join(outputDir, baseName)
		if err := os.Rename(generatedFile, targetFile); err != nil {
			return fmt.Errorf("failed to rename %s to %s: %w", generatedFile, filepath.Base(targetFile), err)
		}

		// Replace protoc-gen-go header with our consistent tacplus header
		if err := header.ReplaceProtocHeader(targetFile, consts.LangGo, "package"); err != nil {
			return fmt.Errorf("failed to replace protoc header in %s: %w", targetFile, err)
		}

		// Remove any subdirectories created by protoc
		if err := removeSubdirs(outputDir); err != nil {
			return err
		}

	}

	return nil
}

// ServiceInfo holds information about a generated gRPC service
type ServiceInfo struct {
	ProtoFile     string
	PackageName   string // Go package name from proto declaration (e.g. "ironmaiden")
	DirName       string // Directory name from proto package (e.g. "ironmaiden"); used in import paths
	ServiceName   string
	GrpcName      string // service identity used for routing (defaults to proto package)
	HostLabel       string // physical service hosting this virtual service, from service_s2s tag
	OutputDir     string
	ParentDir     string
	Config        *tactype.Config
	ReadOnly      bool     // true for :ro (accessors only, no mutators)
	WriteOnce     bool     // true for :worm (write-once read-many: set/add/publish but no lock/delete)
	AuthDisabled  bool     // true when authorization is disabled by proto annotations
	AuthAnyUser   bool     // true when service uses auth_any_user (verified token plus fixed namespace gate)
	AuthServices  []string // S2S: allowed caller identities (service-level default)
	HasMethodAuth bool     // true if any per-method auth metadata must be registered
	IsS2S         bool     // true when this is a service-to-service service
	IsC2D         bool     // true when this is a client-to-daemon service
	IsSMB         bool     // true when this service has spec.smb
	SmbRoot       spec.SmbRoot
	// SynthesizedKind is WHICH tacplus-generated surface this is
	// (parser.SynthesizedKind*), empty for hand-authored services. Carried so the
	// token-minter gate can tell a generated STATE service (the process must reach
	// its state host, so it needs an S2S identity) from a generated STORE service
	// (a c2d daemon may never serve or call one).
	SynthesizedKind string
}

// ImportDirName returns the directory name to use in Go import paths.
// Uses the proto package name (not the filename) as the source of truth.
func (s ServiceInfo) ImportDirName() string {
	if s.DirName != "" {
		return s.DirName
	}
	return s.PackageName
}

// filterServicesByGrpcEntries returns services whose ProtoFile matches any gRPC entry's ProtoFile.
// AuthDisabled from the matching GrpcEntry is propagated to the ServiceInfo.
func filterServicesByGrpcEntries(servicesInfo []ServiceInfo, entries []tactype.GrpcEntry) []ServiceInfo {
	type entryMeta struct {
		match        bool
		authDisabled bool
	}
	protoSet := make(map[string]entryMeta)
	for _, fe := range entries {
		existing := protoSet[fe.ProtoFile]
		existing.match = true
		if fe.AuthDisabled {
			existing.authDisabled = true
		}
		protoSet[fe.ProtoFile] = existing
	}
	var result []ServiceInfo
	for _, svc := range servicesInfo {
		if meta, ok := protoSet[svc.ProtoFile]; ok {
			if meta.authDisabled {
				svc.AuthDisabled = true
			}
			result = append(result, svc)
		}
	}
	return result
}

// filterServicesByGrpcEntriesExact returns services matching explicit proto and
// service entries. W2S uses this to avoid accidentally generating unrelated
// services that share a proto file with a selected client dependency.
func filterServicesByGrpcEntriesExact(servicesInfo []ServiceInfo, entries []tactype.GrpcEntry) []ServiceInfo {
	type key struct{ proto, svc string }
	type entryMeta struct {
		match        bool
		authDisabled bool
	}
	wanted := make(map[key]entryMeta)
	for _, fe := range entries {
		k := key{proto: fe.ProtoFile, svc: fe.ServiceName}
		existing := wanted[k]
		existing.match = true
		if fe.AuthDisabled {
			existing.authDisabled = true
		}
		wanted[k] = existing
	}
	var result []ServiceInfo
	for _, svc := range servicesInfo {
		meta, ok := wanted[key{proto: svc.ProtoFile, svc: svc.ServiceName}]
		if !ok {
			meta, ok = wanted[key{proto: svc.ProtoFile, svc: ""}]
		}
		if !ok || !meta.match {
			continue
		}
		if meta.authDisabled {
			svc.AuthDisabled = true
		}
		result = append(result, svc)
	}
	return result
}

// resolveServerServices returns the services that should get server wrappers.
// If GrpcServerAll, returns all services from gRPC-annotated proto files.
// If GrpcServers is set, filters to matching services.
// Otherwise returns nil.
func resolveServerServices(config *tactype.Config, servicesInfo []ServiceInfo) ([]ServiceInfo, error) {
	if config.GrpcServerAll {
		return filterGrpcAnnotated(config, servicesInfo)
	}
	// Auto-discover own-package services when SelfPackage is set — matches
	// Rust's behavior. Synthetic GrpcServers entries (auto-injected for store
	// services like timeseries_mutation) are additive so other own-package
	// services aren't silently dropped.
	var result []ServiceInfo
	if config.SelfPackage != "" {
		for _, svc := range servicesInfo {
			if svc.GrpcName == "" || svc.PackageName != config.SelfPackage || svc.IsC2D {
				continue
			}
			result = append(result, svc)
		}
	}
	if len(config.GrpcServers) > 0 {
		for _, extra := range filterServicesByGrpcEntries(servicesInfo, config.GrpcServers) {
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
	return result, nil
}

// resolveClientServices returns the services that should get s2s gRPC client wrappers.
func resolveClientServices(config *tactype.Config, servicesInfo []ServiceInfo) ([]ServiceInfo, error) {
	if config.GrpcS2SClientAll {
		return filterGrpcAnnotated(config, servicesInfo)
	}
	if len(config.GrpcS2SClients) > 0 {
		return filterServicesByGrpcEntries(servicesInfo, config.GrpcS2SClients), nil
	}
	return nil, nil
}

// resolveC2SClientServices returns the services that should get c2s gRPC client wrappers.
func resolveC2SClientServices(config *tactype.Config, servicesInfo []ServiceInfo) ([]ServiceInfo, error) {
	if len(config.GrpcC2SClients) > 0 {
		return filterServicesByGrpcEntries(servicesInfo, config.GrpcC2SClients), nil
	}
	return nil, nil
}

// resolveW2SClientServices returns the services that should get w2s gRPC client wrappers.
func resolveW2SClientServices(config *tactype.Config, servicesInfo []ServiceInfo) ([]ServiceInfo, error) {
	if len(config.GrpcW2SClients) > 0 {
		return filterServicesByGrpcEntriesExact(servicesInfo, config.GrpcW2SClients), nil
	}
	return nil, nil
}

// resolveC2DClientServices returns the services that should get c2d
// (client-to-daemon) gRPC client wrappers.
func resolveC2DClientServices(config *tactype.Config, servicesInfo []ServiceInfo) []ServiceInfo {
	if len(config.GrpcC2DClients) > 0 {
		return filterC2DAnnotated(filterServicesByGrpcEntriesExact(servicesInfo, config.GrpcC2DClients))
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

func filterC2DAnnotated(servicesInfo []ServiceInfo) []ServiceInfo {
	var result []ServiceInfo
	for _, svc := range servicesInfo {
		if svc.IsC2D {
			result = append(result, svc)
		}
	}
	return result
}

// filterGrpcAnnotated returns all services from proto files that have gRPC annotations
// or event-annotated messages (service_event). Event messages generate
// gRPC-accessible state services, unlike plain service_state messages which are
// consumed only through the state client layer.
func filterGrpcAnnotated(config *tactype.Config, servicesInfo []ServiceInfo) ([]ServiceInfo, error) {
	if config.FlatProtoDir == "" {
		return nil, nil
	}
	p, err := parser.NewDescriptorParser(config.FlatProtoDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create descriptor parser for gRPC service detection: %w", err)
	}

	// Find proto files that have at least one S2S/C2S gRPC service
	// OR at least one message with a service_event annotation (events generate
	// gRPC-accessible state services). Plain service_state is excluded — those
	// services are consumed through the state client layer, not gRPC wrappers.
	// C2D services are local daemon IPC and must not be hosted by S2SM.
	grpcProtos := make(map[string]bool)
	for _, svc := range servicesInfo {
		if svc.IsC2D {
			continue
		}
		if grpcProtos[svc.ProtoFile] {
			continue
		}
		protoPath := filepath.Join(config.FlatProtoDir, svc.ProtoFile)
		fd, err := p.GetFileDescriptor(protoPath)
		if err != nil {
			continue
		}
		for _, s := range fd.Services {
			if s.GrpcName != "" {
				grpcProtos[svc.ProtoFile] = true
				break
			}
		}
		if !grpcProtos[svc.ProtoFile] {
			for _, msg := range fd.Messages {
				if ann, ok := msg.Annotations["service_event"]; ok && ann.StringValue != "" {
					grpcProtos[svc.ProtoFile] = true
					break
				}
			}
		}
	}

	var result []ServiceInfo
	for _, svc := range servicesInfo {
		if !svc.IsC2D && grpcProtos[svc.ProtoFile] {
			result = append(result, svc)
		}
	}
	return result, nil
}

// generateGrpcCode runs protoc-gen-go-grpc to generate gRPC service stubs.
// gRPC stubs are placed in the services/ subdirectory alongside wrappers.
// Uses comprehensive M= flags so protoc-gen-go-grpc generates correct imports natively.
// Returns a list of services that were generated.
func generateGrpcCode(config *tactype.Config, protoFiles []string, tempProtoDir string, typesOutputDir string) ([]ServiceInfo, error) {
	// Check if protoc-gen-go-grpc is available
	if _, err := exec.LookPath("protoc-gen-go-grpc"); err != nil {
		return nil, fmt.Errorf("protoc-gen-go-grpc not found in PATH; install with: go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest")
	}

	// Create DescriptorParser for robust proto parsing
	parser, err := parser.NewDescriptorParser(tempProtoDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	var servicesInfo []ServiceInfo

	for _, protoFile := range protoFiles {
		relPath := protoFile

		// Use DescriptorParser to check for proto-buddy generation annotation
		// This is the robust, accurate way - no regex fragility, no directory assumptions
		protoPath := filepath.Join(tempProtoDir, relPath)
		fileDesc, err := parser.GetFileDescriptor(protoPath)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", relPath, err)
		}

		// Generate gRPC stubs for all proto files that have service definitions.
		if len(fileDesc.Services) == 0 {
			continue
		}

		isLibWithService := strings.HasPrefix(relPath, "sys/")
		isVirtualService := proto.IsVirtualServiceProto(relPath)

		parentDir := filepath.Dir(relPath)
		filename := strings.TrimSuffix(filepath.Base(relPath), ".proto")
		fullPackageName := fileDesc.Package
		// Base package name: first component of dotted package (e.g., "baseball" from "baseball.services")
		packageName := strings.SplitN(fullPackageName, ".", 2)[0]

		// Dead check removed: no proto files use `package services;` or
		// `package clients;`. The proto package name is always the real
		// service name (e.g., `package baseball;`).

		dirPackageName := packageName

		// Handle service protos and other patterns
		// Use proto package name for directory, not filename
		var subdir string
		if serviceName, _ := proto.ServiceProtoParts(relPath); serviceName != "" {
			subdir = filepath.Join(parentDir, dirPackageName)
		} else if isVirtualService {
			// Virtual service gRPC stubs go in same package as types (like service protos)
			subdir = filepath.Join(parentDir, dirPackageName)
		} else if isLibWithService {
			// Lib gRPC stubs go in same package as types
			subdir = filepath.Join(parentDir, dirPackageName)
		} else if filepath.Base(parentDir) == dirPackageName {
			subdir = filepath.Join(parentDir, "services")
		} else {
			subdir = filepath.Join(parentDir, dirPackageName, "services")
		}
		outputDir := filepath.Join(typesOutputDir, subdir)
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return nil, err
		}

		zap.L().Debug("Building gRPC", zap.String("proto", protoFile), zap.String("outputDir", outputDir))

		// Construct go_package paths (with types/ root prefix)
		// Use proto package name for import paths, not filename
		var grpcImportPath string
		if serviceName, _ := proto.ServiceProtoParts(relPath); serviceName != "" {
			grpcImportPath = fmt.Sprintf("%s/types/%s/%s", config.ModuleBasePath, parentDir, dirPackageName)
		} else if isVirtualService {
			// Virtual service gRPC stubs go in same package as types
			grpcImportPath = fmt.Sprintf("%s/types/%s/%s", config.ModuleBasePath, parentDir, dirPackageName)
		} else if isLibWithService {
			// Lib gRPC stubs go in same package as types — no separate services/ dir
			grpcImportPath = fmt.Sprintf("%s/types/%s/%s", config.ModuleBasePath, parentDir, dirPackageName)
		} else if filepath.Base(parentDir) == dirPackageName {
			grpcImportPath = fmt.Sprintf("%s/types/%s/services", config.ModuleBasePath, parentDir)
		} else {
			grpcImportPath = fmt.Sprintf("%s/types/%s/%s/services", config.ModuleBasePath, parentDir, dirPackageName)
		}
		// Lib gRPC stubs are same-package — use lib package name, not "services"
		// Main service gRPC stubs are same-package (merged proto file)
		// Virtual service gRPC stubs are same-package
		// Watch proto gRPC stubs stay in "services" package
		grpcPackageName := "services"
		if isLibWithService {
			grpcPackageName = dirPackageName
		}
		if isVirtualService {
			grpcPackageName = packageName
		}
		if serviceName, _ := proto.ServiceProtoParts(relPath); serviceName != "" {
			grpcPackageName = packageName
		}

		// Read proto file content for go_package modification
		content, err := os.ReadFile(protoPath)
		if err != nil {
			return nil, fmt.Errorf("failed to read %s: %w", protoPath, err)
		}

		// REGEX USAGE RATIONALE: Modify go_package option in proto file
		//
		// Why regex here:
		// - We need to temporarily modify the go_package option in a proto file
		// - This is a simple find-replace of one line: 'option go_package = "...";'
		// - The modified file is used for protoc generation, then we restore it
		// - This is a build-time text substitution on a well-defined line
		// - Parsing the entire proto with DescriptorParser would be overkill for changing one line
		//
		// What we're doing:
		// - Change: option go_package = "X";
		// - To:     option go_package = "grpc_import_path;services";
		// - This forces protoc-gen-go-grpc to generate code in a different package
		// - Which makes it import the types package (instead of being in the same package)
		//
		// Pattern explanation: `option go_package = "[^"]+";`
		// - Matches the entire go_package option line
		// - [^"]+: Any characters except quotes (the import path)
		//
		// Alternative considered: Proto file parsing
		// - Would require parsing entire file just to change one line
		// - Regex is simpler and faster for this specific use case
		//
		// Temporarily modify the proto file to use the grpc package with "services" Go name
		// This forces protoc-gen-go-grpc to import the types package
		goPackageRegex := regexp.MustCompile(`option go_package = "[^"]+";`)
		modifiedContent := goPackageRegex.ReplaceAllString(string(content),
			fmt.Sprintf(`option go_package = "%s;%s";`, grpcImportPath, grpcPackageName))

		// Save original content before modifying go_package
		originalContent := content

		// Write modified content back to the temp proto file
		if err := os.WriteFile(protoPath, []byte(modifiedContent), 0644); err != nil {
			return nil, err
		}

		// Build comprehensive M= flags for ALL proto files so protoc-gen-go-grpc
		// generates correct import paths natively (no post-processing needed).
		args := []string{
			"--go-grpc_out=" + outputDir,
			"--go-grpc_opt=paths=source_relative",
		}
		// Map the current proto to its gRPC output path (where the generated code goes)
		moduleMapping := fmt.Sprintf("M%s=%s", relPath, grpcImportPath)
		args = append(args, "--go-grpc_opt="+moduleMapping)
		// Add M= flags for all imported protos to ensure correct cross-package imports.
		// Use proto package names for import paths, not filenames.
		for _, pf := range protoFiles {
			if pf == relPath {
				continue // already mapped above
			}
			pfNorm := filepath.ToSlash(pf)
			var mPath string
			if sn, _ := proto.ServiceProtoParts(pf); sn != "" {
				// Use proto package name for the import path
				pfPkg := protoPackageForPath(tempProtoDir, pf)
				if pfPkg == "" || pfPkg == "services" {
					pfPkg = sn // fallback for package services; or unparseable
				}
				pfParent := filepath.Dir(filepath.ToSlash(pf))
				mPath = fmt.Sprintf("%s/types/%s/%s", config.ModuleBasePath, pfParent, pfPkg)
			} else if proto.IsVirtualServiceProto(pf) {
				// Virtual service protos: same-package gRPC stubs (like service protos)
				pfPkg := protoPackageForPath(tempProtoDir, pf)
				if pfPkg == "" {
					pfPkg = strings.TrimSuffix(filepath.Base(pf), ".proto")
				}
				pfParent := filepath.Dir(filepath.ToSlash(pf))
				mPath = fmt.Sprintf("%s/types/%s/%s", config.ModuleBasePath, pfParent, pfPkg)
			} else if strings.HasPrefix(pfNorm, "sys/") {
				pfPkg := protoPackageForPath(tempProtoDir, pf)
				if pfPkg == "" {
					pfPkg = strings.TrimSuffix(filepath.Base(pf), ".proto")
				}
				mPath = fmt.Sprintf("%s/types/sys/%s", config.ModuleBasePath, pfPkg)
			} else if strings.HasPrefix(pfNorm, "common/") {
				pfPkg := protoPackageForPath(tempProtoDir, pf)
				if pfPkg == "" {
					pfPkg = strings.TrimSuffix(filepath.Base(pf), ".proto")
				}
				pfParentDir := filepath.Dir(pfNorm)
				if filepath.Base(pfParentDir) == pfPkg {
					mPath = fmt.Sprintf("%s/types/%s", config.ModuleBasePath, pfParentDir)
				} else {
					mPath = fmt.Sprintf("%s/types/%s/%s", config.ModuleBasePath, pfParentDir, pfPkg)
				}
			}
			if mPath != "" {
				args = append(args, fmt.Sprintf("--go-grpc_opt=M%s=%s", pf, mPath))
			}
		}
		args = append(args, "--proto_path="+tempProtoDir, relPath)

		// Run protoc with go-grpc plugin
		cmd := exec.Command(config.ProtocPath, args...)

		// Log the command being executed
		zap.L().Debug("Running protoc-gen-go-grpc", zap.String("cmd", config.ProtocPath), zap.Strings("args", cmd.Args[1:]))

		output, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("protoc-gen-go-grpc failed for %s: %w\n%s", protoFile, err, string(output))
		}

		// Move the generated file from nested path to target location.
		generatedFile := filepath.Join(outputDir, relPath[:len(relPath)-6]+"_grpc.pb.go") // Remove .proto, add _grpc.pb.go
		targetFile := filepath.Join(outputDir, filename+"_grpc.pb.go")
		if err := os.Rename(generatedFile, targetFile); err != nil {
			// File might already be in the correct location
			if !os.IsNotExist(err) {
				return nil, fmt.Errorf("failed to rename %s: %w", generatedFile, err)
			}
		}

		// Remove any subdirectories created by protoc
		if err := removeSubdirs(outputDir); err != nil {
			return nil, err
		}

		// Replace protoc-gen-go-grpc header with our consistent tacplus header
		grpcFile := filepath.Join(outputDir, filename+"_grpc.pb.go")
		if err := header.ReplaceProtocHeader(grpcFile, consts.LangGo, "package"); err != nil {
			return nil, fmt.Errorf("failed to replace protoc header in %s: %w", grpcFile, err)
		}

		// Restore original proto file content so subsequent iterations see correct go_package
		if err := os.WriteFile(protoPath, originalContent, 0644); err != nil {
			return nil, fmt.Errorf("failed to restore original proto file %s: %w", protoPath, err)
		}

		// Record service information for each service found (from DescriptorParser).
		protoFileForInfo := protoFile
		parentDirForInfo := parentDir
		dirNameForInfo := dirPackageName
		if serviceName, _ := proto.ServiceProtoParts(relPath); serviceName != "" {
			// Service/client protos: use actual parent dir to avoid double nesting.
			parentDirForInfo = parentDir
			// DirName uses proto package name for directory paths
			dirNameForInfo = dirPackageName
		}
		for _, service := range fileDesc.Services {
			// Derive service-level auth flags from method-level annotations.
			// auth_any_user: true if ANY method has it (service needs authenticate-at-or-below wiring).
			// auth_disabled: true if service-level OR ALL methods have it.
			anyUser := service.AuthAnyUser
			// A service with no RPCs registers nothing: the wrapper emits
			// Register<Svc>MethodAuth only when it produced at least one
			// SetMethodAuth line, so claiming HasMethodAuth here would make the
			// ServiceManager call a function that was never generated.
			hasMethodAuth := len(service.Methods) > 0 && len(service.AuthServices) > 0
			for _, m := range service.Methods {
				if m.AuthAnyUser {
					anyUser = true
				}
				if len(m.AuthServices) > 0 {
					hasMethodAuth = true
				}
				if m.TargetNamespaceField != "" {
					hasMethodAuth = true
				}
			}
			servicesInfo = append(servicesInfo, ServiceInfo{
				ProtoFile:       protoFileForInfo,
				PackageName:     packageName,
				DirName:         dirNameForInfo,
				ServiceName:     service.Name,
				GrpcName:        service.GrpcName,
				HostLabel:         service.HostLabel,
				OutputDir:       outputDir,
				ParentDir:       parentDirForInfo,
				Config:          config,
				AuthDisabled:    service.AuthDisabled,
				AuthAnyUser:     anyUser,
				AuthServices:    service.AuthServices,
				HasMethodAuth:   hasMethodAuth,
				IsS2S:           service.IsS2S,
				IsC2D:           service.IsC2D,
				IsSMB:           service.IsSMB,
				SmbRoot:         service.SmbRoot,
				SynthesizedKind: service.SynthesizedKind,
			})
		}
	}

	if len(servicesInfo) == 0 {
		zap.L().Info("No services found in proto files, skipping gRPC generation")
	}

	return servicesInfo, nil
}

// s2sGenerationEnabled reports whether this build emits a service-to-service
// surface, and therefore whether the generated ServiceManager will need an S2S
// identity (which is what ValidateTokenMinters polices).
//
// Synthesized STORE services do not count. spec.service_store synthesizes one
// <Msg>StoreService per record and emits each as S2S, so a c2d-only daemon that
// declares records — and uses only the s2sm/diskstore helpers, never the store
// services — was reported as having an S2S surface and hard-failed generation
// with "no authoritative (spec.token_minter) service is declared". sideband2 is
// the concrete case.
//
// Synthesized STATE services DO count, which is why this tests the kind rather
// than Synthesized: a service with a state root must reach its state host, so it
// genuinely needs an S2S identity, and dropping every synthesized service from
// this gate would have silently stopped requiring a minter for that whole class.
func s2sGenerationEnabled(config *tactype.Config, serverServices []ServiceInfo) bool {
	if config.GrpcS2SClientAll || len(config.GrpcS2SClients) > 0 {
		return true
	}
	for _, svc := range serverServices {
		if svc.IsS2S && svc.SynthesizedKind != parser.SynthesizedKindStore {
			return true
		}
	}
	return false
}

// copyHelperFiles copies generated helper files from builder output
func copyHelperFiles(builderTempDir, outputDir string) error {
	golangDir := filepath.Join(builderTempDir, "golang")
	if _, err := os.Stat(golangDir); os.IsNotExist(err) {
		zap.L().Info("No helper files generated")
		return nil
	}

	// Walk through all generated files
	return filepath.Walk(golangDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}

		// Only copy helpers.go, graph_*.go, timeseries_*.go, extraction_*.go, and test files
		isTestFile := strings.HasSuffix(path, "_test.go")
		isGeneratedCode := strings.HasSuffix(path, "helpers.go")
		isTimeseriesHelper := strings.HasPrefix(filepath.Base(path), "timeseries_") && strings.HasSuffix(path, ".go")
		isExtractionHelper := strings.HasPrefix(filepath.Base(path), "extraction_") && strings.HasSuffix(path, ".go")

		if !isGeneratedCode && !isTestFile && !isTimeseriesHelper && !isExtractionHelper {
			return nil
		}

		relPath, err := filepath.Rel(golangDir, path)
		if err != nil {
			return err
		}

		dstFile := filepath.Join(outputDir, relPath)
		dstDir := filepath.Dir(dstFile)
		if err := os.MkdirAll(dstDir, 0755); err != nil {
			return err
		}

		if err := copyFile(path, dstFile); err != nil {
			return err
		}
		zap.L().Debug("Copied helper file", zap.String("path", relPath))
		return nil
	})
}

// copyStaticPackages copies static packages to the output directory.
// - errors.go → types/sys/errors/ (error constructors, depends on Error proto type)
// - utils.go, validation.go, validation_test.go → utils/ (common utilities, sibling to types/)
func copyStaticPackages(config *tactype.Config, outputDir string) error {
	// Resolve the errors import path for template substitution
	errorsImportPath := config.TypesModulePath() + "/types/sys/errors"

	// errors.go goes to types/sys/errors/ (same package as errors.pb.go)
	errorsDir := filepath.Join(outputDir, "sys", "errors")
	if err := os.MkdirAll(errorsDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", errorsDir, err)
	}
	errorsFile := filepath.Join(errorsDir, "errors.go")
	if err := os.WriteFile(errorsFile, []byte(ErrorsGoTmpl()), 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", errorsFile, err)
	}
	zap.L().Debug("Copied static package", zap.String("path", "sys/errors/errors.go"))

	// utils.go, validation.go, validation_test.go go to utils/ (sibling to types/)
	utilsDir := filepath.Join(filepath.Dir(outputDir), "utils")
	if err := os.MkdirAll(utilsDir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", utilsDir, err)
	}

	utilsTemplates := map[string]string{
		"utils.go.tmpl":           UtilsGoTmpl(),
		"validation.go.tmpl":      ValidationGoTmpl(),
		"validation_test.go.tmpl": ValidationTestGoTmpl(),
	}

	for filename, content := range utilsTemplates {
		// Replace placeholder with actual errors import path
		content = strings.ReplaceAll(content, "ERRORS_IMPORT_PATH", errorsImportPath)

		dstFile := strings.TrimSuffix(filename, ".tmpl")
		fullPath := filepath.Join(utilsDir, dstFile)
		if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", fullPath, err)
		}
		zap.L().Debug("Copied static package", zap.String("path", "utils/"+dstFile))
	}

	return nil
}

// cleanupStaleFiles removes generated Go files that are no longer produced by templates
func cleanupStaleFiles(outputDir string) error {
	// Define stale files to remove (files that were previously generated but are no longer)
	staleFiles := map[string][]string{
		"spec/key": {
			"publickey.go", // Merged into spec.go
		},
		"spec/x509": {
			"cert.go",     // Merged into spec.go
			"certpair.go", // Merged into spec.go
		},
	}

	// Also remove entire stale directories from previous layout
	staleDirs := []string{
		"spec/errors",     // Removed - no longer using spec errors package
		"utils/errors",    // Removed - errors merged into utils
		"spec/utils",      // Removed - utils files moved to sys/errors/
		"spec/types",      // Removed - types now directly in their packages
		"spec/interfaces", // Removed - interfaces now at top-level interfaces/
	}

	for dir, files := range staleFiles {
		fullDir := filepath.Join(outputDir, dir)
		for _, file := range files {
			fullPath := filepath.Join(fullDir, file)
			if _, err := os.Stat(fullPath); err == nil {
				// File exists, remove it
				if err := os.Remove(fullPath); err != nil {
					return fmt.Errorf("failed to remove stale file %s: %w", fullPath, err)
				}
				zap.L().Debug("Removed stale file", zap.String("dir", dir), zap.String("file", file))
			}
		}
	}

	// Remove stale directories
	for _, dir := range staleDirs {
		fullDir := filepath.Join(outputDir, dir)
		if _, err := os.Stat(fullDir); err == nil {
			// Directory exists, remove it recursively
			if err := os.RemoveAll(fullDir); err != nil {
				return fmt.Errorf("failed to remove stale directory %s: %w", fullDir, err)
			}
			zap.L().Debug("Removed stale directory", zap.String("dir", dir))
		}
	}

	// Clean up old layout subdirectories within any top-level proto-generated dir.
	// Old layout had: {dir}/*/types/, {dir}/*/clients/, etc.
	// Scan all top-level dirs instead of hardcoding "sys" and "services".
	staleSubdirNames := []string{"types"}
	allTopEntries, _ := os.ReadDir(outputDir)
	for _, topEntry := range allTopEntries {
		if !topEntry.IsDir() {
			continue
		}
		// Skip known output directories
		if topEntry.Name() == "types" || topEntry.Name() == "s2sm" || topEntry.Name() == "c2sm" || topEntry.Name() == "c2dm" || topEntry.Name() == "w2sm" || topEntry.Name() == "utils" {
			continue
		}
		topPath := filepath.Join(outputDir, topEntry.Name())
		entries, err := os.ReadDir(topPath)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			for _, staleName := range staleSubdirNames {
				staleDir := filepath.Join(topPath, entry.Name(), staleName)
				if _, err := os.Stat(staleDir); err == nil {
					if err := os.RemoveAll(staleDir); err != nil {
						return fmt.Errorf("failed to remove stale directory %s: %w", staleDir, err)
					}
					zap.L().Debug("Removed stale subdirectory", zap.String("dir", filepath.Join(topEntry.Name(), entry.Name(), staleName)))
				}
			}
		}
	}

	// Clean up stale types.pb.go files in any top-level proto-generated dir.
	// Protobuf files are now named after their proto file (e.g., errors.pb.go, not types.pb.go).
	// Scan all top-level dirs instead of hardcoding "sys" and "common".
	for _, topEntry := range allTopEntries {
		if !topEntry.IsDir() {
			continue
		}
		if topEntry.Name() == "types" || topEntry.Name() == "s2sm" || topEntry.Name() == "c2sm" || topEntry.Name() == "c2dm" || topEntry.Name() == "w2sm" || topEntry.Name() == "utils" {
			continue
		}
		topPath := filepath.Join(outputDir, topEntry.Name())
		subEntries, err := os.ReadDir(topPath)
		if err != nil {
			continue
		}
		for _, entry := range subEntries {
			if !entry.IsDir() {
				continue
			}
			staleFile := filepath.Join(topPath, entry.Name(), "types.pb.go")
			// Only remove if the properly-named .pb.go file exists alongside
			properFile := filepath.Join(topPath, entry.Name(), entry.Name()+".pb.go")
			if _, err := os.Stat(staleFile); err == nil {
				if _, err := os.Stat(properFile); err == nil {
					if err := os.Remove(staleFile); err != nil {
						return fmt.Errorf("failed to remove stale file %s: %w", staleFile, err)
					}
					zap.L().Debug("Removed stale types.pb.go", zap.String("package", entry.Name()))
				}
			}
		}
	}

	return nil
}

// Helper functions

func removeSubdirs(outputDir string) error {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return err
	}

	// Remove all subdirectories (they were created by protoc but we've already moved the .pb.go file out)
	for _, entry := range entries {
		if entry.IsDir() {
			toRemove := filepath.Join(outputDir, entry.Name())
			if err := os.RemoveAll(toRemove); err != nil {
				return err
			}
		}
	}

	return nil
}

func copyFile(src, dst string) error {
	content, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, content, 0644)
}

// generateBuiltinProfiles writes builtins.go into the generated/entitlements
// package directory. It generates a BuiltinGrantAll() function returning a
// TokenEntitlements with every discovered exact RPC grant token.
func generateBuiltinProfiles(config *tactype.Config, svcRPCs []proto.ServiceRPCEntry, typesOutputDir string) error {
	outputDir := filepath.Join(typesOutputDir, "generated", "entitlements")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create generated/entitlements dir: %w", err)
	}

	var buf strings.Builder
	buf.WriteString("// CODE GENERATED by tacplus — DO NOT EDIT\n\n")
	buf.WriteString("package entitlements\n\n")

	buf.WriteString("// BuiltinGrantAll returns a TokenEntitlements that grants every discovered\n")
	buf.WriteString("// exact C2S RPC. Used for admin-level tokens.\n")
	buf.WriteString("func BuiltinGrantAll() *TokenEntitlements {\n")
	buf.WriteString("\treturn &TokenEntitlements{\n")
	buf.WriteString("\t\tGrants: []string{\n")
	for _, svc := range svcRPCs {
		for _, token := range svc.GrantTokens {
			buf.WriteString(fmt.Sprintf("\t\t\t%q,\n", token))
		}
	}
	buf.WriteString("\t\t},\n")
	buf.WriteString("\t}\n")
	buf.WriteString("}\n")

	outputPath := filepath.Join(outputDir, "builtins.go")
	if err := os.WriteFile(outputPath, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write builtins.go: %w", err)
	}

	zap.L().Info("Generated builtins.go",
		zap.String("path", outputPath),
		zap.Int("services", len(svcRPCs)))
	return nil
}

// collectSubscriptions scans actor-owned protos for legacy subscribe and
// explicit event_watch annotations, plus store-derived watches, and returns
// them as tactype.SubscriptionEntry.
func collectSubscriptions(config *tactype.Config, protoDir string) ([]tactype.SubscriptionEntry, error) {
	if protoDir == "" {
		protoDir = config.FlatProtoDir
	}
	if protoDir == "" {
		return nil, nil
	}
	p, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return nil, fmt.Errorf("create descriptor parser for subscription detection: %w", err)
	}

	seen := make(map[string]bool)
	var protoFiles []string
	for _, state := range config.States {
		if !seen[state.ProtoFile] {
			seen[state.ProtoFile] = true
			protoFiles = append(protoFiles, state.ProtoFile)
		}
	}
	for _, grpc := range config.GrpcServers {
		if !seen[grpc.ProtoFile] {
			seen[grpc.ProtoFile] = true
			protoFiles = append(protoFiles, grpc.ProtoFile)
		}
	}

	subs, err := boundaries.CollectActorSubscriptions(p, protoDir, protoFiles, config.SelfPackage)
	if err != nil {
		return nil, err
	}
	result := make([]tactype.SubscriptionEntry, 0, len(subs))
	for _, sd := range subs {
		res := boundaries.ResolveSubscriptionSource(p, protoDir, sd)
		result = append(result, tactype.SubscriptionEntry{
			SourcePackage:   sd.SourcePackage,
			SourceStateRoot: sd.SourceStateRoot,
			SourceField:     sd.SourceField,
			TargetService:   sd.BrokerLabel,
			SourceTypeFqn:   res.TypeFqn,
			SourceProtoDir:  res.ProtoDir,
		})
		if sd.Origin == "store_derived" {
			zap.L().Info("Auto-derived store subscription",
				zap.String("source", sd.Source),
				zap.String("type", res.TypeFqn))
		}
	}

	return result, nil
}
