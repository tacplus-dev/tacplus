// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/registry"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// Generate generates Rust code from proto files
func Generate(config *tactype.Config) error {
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
	protoDir, err := proto.CopyAndPrepareProtoDir(config.InputDir, config.OutputDir)
	if err != nil {
		return fmt.Errorf("failed to copy proto directory: %w", err)
	}
	// (spec.json_key) is pbjson's json_name; see injectJsonNameForJsonKey.
	if err := injectJsonNameForJsonKey(protoDir); err != nil {
		return err
	}
	if err := proto.AppendEventBusWrappersToProtos(protoDir, config.FlatProtoDir); err != nil {
		return fmt.Errorf("failed to append eventbus topic wrappers to protos: %w", err)
	}
	if err := proto.AppendRoutingEnvelopesToProtos(protoDir, config.FlatProtoDir); err != nil {
		return fmt.Errorf("failed to append routing envelopes to protos: %w", err)
	}
	if _, err := proto.PrepareEventBusSupportProto(protoDir, proto.EventBusSupportProtoOptions{}, config.FlatProtoDir); err != nil {
		return err
	}

	// Generate virtual service protos into generated/.
	opaEvalProtos, err := proto.GenerateOpaEvalProto(protoDir, config.FlatProtoDir)
	if err != nil {
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
	if err := service.InferGrpcClientsFromAnnotations(config, protoDir); err != nil {
		return fmt.Errorf("failed to infer gRPC clients from proto annotations: %w", err)
	}
	// Derived clients are unioned in AFTER inference, not appended before it:
	// inference skips its explicit-declaration block when GrpcS2SClients is
	// already non-empty, so a service declaring both (spec.record_read) and an
	// explicit (spec.grpc_s2s_client) used to lose the explicit one silently.
	config.GrpcS2SClients = service.MergeGrpcEntries(config.GrpcS2SClients, virtualClients)

	// (spec.rpc).requires_device needs a client to the approval broker on the
	// authoritative (spec.token_minter) service. Auto-injected through the same
	// shared derivation Go and Java use, at the same seam and for the same
	// reason: the developer writes only the boolean, so the dependency cannot
	// disagree with the annotation that creates it.
	approvalBrokerClients, err := proto.DeriveApprovalBrokerClient(config.FlatProtoDir, config.SelfPackage)
	if err != nil {
		return fmt.Errorf("failed to derive approval-broker client: %w", err)
	}
	config.GrpcS2SClients = service.MergeGrpcEntries(config.GrpcS2SClients, approvalBrokerClients)

	// Inject per-service ConfigState messages into each service's own proto file.
	feConfigs, err := proto.InjectGrpcConfigState(config.InputDir, protoDir, config.FlatProtoDir)
	if err != nil {
		return fmt.Errorf("failed to inject gRPC server config state: %w", err)
	}
	if err := proto.ResolveServiceStates(config, feConfigs); err != nil {
		return err
	}

	// Generate TenantConfig proto from (spec.config_slice) tenant-scope annotations.
	// Must run before protoc so the generated/tenant_config.proto stub is replaced
	// with the full aggregate message including all per-service slice fields.
	if err := proto.GenerateTenantConfig(config.InputDir, config.FlatProtoDir, protoDir); err != nil {
		return fmt.Errorf("failed to generate tenant_config.proto: %w", err)
	}
	if err := proto.GenerateProjectConfig(config.InputDir, config.FlatProtoDir, protoDir); err != nil {
		return fmt.Errorf("failed to generate project_config.proto: %w", err)
	}

	// Append state/store service definitions BEFORE trace injection so that
	// auto-appended message types (LeaderRecord, EventBus envelopes, etc.)
	// also receive trace fields.
	// Inject per-field "<field>_redacted" indicator bools before native compilation
	// so they appear in every language's generated type.
	if err := proto.InjectRedactIndicatorFields(protoDir, config.FlatProtoDir); err != nil {
		return fmt.Errorf("failed to inject redact indicator fields: %w", err)
	}
	// <T>Public for every (spec.public_view) message: its SHOW fields only.
	if err := proto.AppendPublicViewMessagesToProtos(protoDir); err != nil {
		return fmt.Errorf("failed to append public view messages: %w", err)
	}
	if err := proto.AppendEntitlementMessagesToProtos(protoDir, config.FlatProtoDir); err != nil {
		return fmt.Errorf("failed to append entitlement messages: %w", err)
	}
	if err := proto.AppendStateServiceToProtos(protoDir, protoDir); err != nil {
		return fmt.Errorf("failed to append state service to protos: %w", err)
	}
	if err := proto.AppendStoreServiceToProtos(protoDir, config.FlatProtoDir); err != nil {
		return fmt.Errorf("failed to append store service to protos: %w", err)
	}
	if err := proto.GenerateTransformationMessages(protoDir, config.FlatProtoDir); err != nil {
		return fmt.Errorf("failed to generate projection messages: %w", err)
	}
	if err := proto.AppendRecordMapServiceToProtos(protoDir, config.FlatProtoDir, config.SelfPackage); err != nil {
		return fmt.Errorf("failed to append record map service to protos: %w", err)
	}
	// The CLIENT-FACING half, after the record plane above because it folds the
	// same criteria and must see the same records. Emits nothing unless a
	// service declares (spec.record_read).c2s.
	if err := proto.AppendRecordC2SServiceToProtos(protoDir, config.FlatProtoDir, config.SelfPackage); err != nil {
		return fmt.Errorf("failed to append client-facing record read plane to protos: %w", err)
	}
	// TimeseriesGroupCounts / TimeseriesQuery injection removed 2026-08-25 with
	// the timedb boneyard (BIG GOAL 3, first move #1) — no timeseries_store
	// exists in any proto set, so both could only no-op, and neither ever had a
	// consumer.
	// Emit generated/persisted_store.proto (PersistedStore enum, one value per
	// service_store) for the typed disk-persistence feature.
	if err := proto.GeneratePersistedStoreEnum(protoDir); err != nil {
		return fmt.Errorf("failed to generate PersistedStore enum: %w", err)
	}

	// generated/services.proto and generated/accounting_method.proto were first
	// written by CopyAndPrepareProtoDir, from the UN-injected source protos. The
	// injectors above can add C2S services — the record query plane's c2s half
	// does — and a C2S RPC needs a <Svc>RPCs enum value and an AccountingMethod
	// field like any hand-authored one. Re-run the scan over the INJECTED tree so
	// protoc below compiles the complete set; without this the generated service
	// wrappers name an AccountingMethod field the compiled type does not have.
	if _, _, _, err := proto.ScanAndGenerateConfigHolder(config.InputDir, protoDir); err != nil {
		return fmt.Errorf("failed to regenerate generated/*.proto after service injection: %w", err)
	}

	// Auto-register virtual service gRPC servers based on proto annotations.
	if isOpaServer, err := proto.IsOpaServer(config.FlatProtoDir, config.SelfPackage); err != nil {
		return fmt.Errorf("failed to check OPA server status: %w", err)
	} else if isOpaServer {
		for _, path := range opaEvalProtos {
			config.GrpcServers = append(config.GrpcServers, tactype.GrpcEntry{
				ProtoFile:   path,
				ServiceName: "OpaEval",
			})
		}
	}
	// Inject trace round fields into all message types.
	// Parse from protoDir (not FlatProtoDir) so appended types are visible.
	skippedTraceTypes, err := proto.InjectTraceFields(protoDir, protoDir)
	if err != nil {
		return fmt.Errorf("failed to inject trace fields: %w", err)
	}
	if err := proto.SwapEmptyInputs(protoDir); err != nil {
		return fmt.Errorf("failed to swap empty inputs to TraceRequest: %w", err)
	}
	// TypeID / <Svc>RpcId enums, after every synthetic message and service is
	// in place — the same point Go and Java inject them.
	if err := proto.InjectRegistryEnums(protoDir); err != nil {
		return fmt.Errorf("failed to inject registry enums into protos: %w", err)
	}

	// protoDir now has injected services + annotations (not yet stripped)

	// Create output directory
	if err := os.MkdirAll(config.OutputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Convert outputDir to absolute path for protoc output
	absOutputDir, err := filepath.Abs(config.OutputDir)
	if err != nil {
		return fmt.Errorf("failed to get absolute path for output directory: %w", err)
	}
	config.OutputDir = absOutputDir

	// Use the copied (and split) protos for protoc compilation.
	absProtoSrcDir, err := filepath.Abs(filepath.Join(absOutputDir, "proto"))
	if err != nil {
		return fmt.Errorf("failed to get absolute path for proto source directory: %w", err)
	}

	// absInputDir points to the proto directory with injected services + annotations.
	// Used for service detection, helper generation, and wrapper generation.
	absInputDir := absProtoSrcDir

	// Expose the annotated, protoc-resolvable proto dir for token-minter
	// discovery (writeLibTemplates) before any discovery runs.
	config.AnnotatedProtoDir = absInputDir

	// Create builder temp directory for helper files
	builderTempDir, err := os.MkdirTemp("", "tacplus-builder-rust-")
	if err != nil {
		return fmt.Errorf("failed to create builder temp directory: %w", err)
	}
	defer func() {
		if err := os.RemoveAll(builderTempDir); err != nil {
			zap.L().Warn("Failed to remove temp directory", zap.String("path", builderTempDir), zap.Error(err))
		}
	}()

	// Collect proto files from the split copy, including services/{name}/services.proto files
	// (tonic-build needs service definitions to generate client/server modules)
	var protoFiles []string
	err = filepath.Walk(absProtoSrcDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}
		relPath, err := filepath.Rel(absProtoSrcDir, path)
		if err != nil {
			return err
		}
		protoFiles = append(protoFiles, relPath)
		return nil
	})
	if err != nil {
		return fmt.Errorf("failed to collect proto files: %w", err)
	}

	zap.L().Info("Generating Rust code with protoc-gen-prost")

	// Generate Rust code using protoc with prost/serde/tonic plugins
	// (same pattern as Go, Java, TypeScript, Python — protoc with language plugins)
	if len(protoFiles) > 0 {
		zap.L().Info("Generating Rust for proto files", zap.Int("count", len(protoFiles)))

		// Resolve protoc-gen-tonic and protoc-gen-prost-serde paths
		protocGenTonic, err := exec.LookPath("protoc-gen-tonic")
		if err != nil {
			return fmt.Errorf("protoc-gen-tonic not found in PATH. Install with: cargo install protoc-gen-tonic")
		}
		protocGenProstSerde, err := exec.LookPath("protoc-gen-prost-serde")
		if err != nil {
			return fmt.Errorf("protoc-gen-prost-serde not found in PATH. Install with: cargo install protoc-gen-prost-serde")
		}

		// Temp directory for protoc output (subdirs per package)
		prostTempDir, err := os.MkdirTemp("", "prost-protoc-")
		if err != nil {
			return fmt.Errorf("failed to create prost temp directory: %w", err)
		}
		defer func() {
			if err := os.RemoveAll(prostTempDir); err != nil {
				zap.L().Warn("Failed to remove prost temp directory", zap.String("path", prostTempDir), zap.Error(err))
			}
		}()

		args := []string{
			"--plugin=protoc-gen-prost=" + config.ProtocGenRs,
			"--plugin=protoc-gen-prost-serde=" + protocGenProstSerde,
			"--plugin=protoc-gen-tonic=" + protocGenTonic,
			"--prost_out=" + prostTempDir,
			"--prost-serde_out=" + prostTempDir,
			"--tonic_out=" + prostTempDir,
			// Well-known type extern paths (use prost-wkt-types for serde support)
			"--prost_opt=extern_path=.google.protobuf.Any=::prost_wkt_types::Any",
			"--prost_opt=extern_path=.google.protobuf.Timestamp=::prost_wkt_types::Timestamp",
			"--prost_opt=extern_path=.google.protobuf.Duration=::prost_wkt_types::Duration",
			"--prost_opt=extern_path=.google.protobuf.Struct=::prost_wkt_types::Struct",
			"--prost_opt=extern_path=.google.protobuf.Value=::prost_wkt_types::Value",
			"--prost_opt=extern_path=.google.protobuf.ListValue=::prost_wkt_types::ListValue",
			"--proto_path=" + absProtoSrcDir,
		}
		args = append(args, protoFiles...)

		cmd := exec.Command(config.ProtocPath, args...)
		zap.L().Debug("Running protoc for Rust", zap.String("cmd", config.ProtocPath), zap.Int("proto_files", len(protoFiles)))

		output, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("protoc prost generation failed: %w\n%s", err, string(output))
		}

		// Merge protoc output into flat files at outputDir.
		// protoc-gen-prost outputs pkg/pkg.rs with include!("pkg.serde.rs") directives,
		// protoc-gen-prost-serde outputs pkg/pkg.serde.rs as a companion file.
		// Copy all files flat to outputDir; reorganizeProstOutput moves companions
		// alongside their main .rs file.
		if err := mergeProstProtocOutput(prostTempDir, config.OutputDir); err != nil {
			return fmt.Errorf("failed to merge prost protoc output: %w", err)
		}

		// Fix serde deserialization to skip unknown fields instead of rejecting them.
		// protoc-gen-prost-serde generates Err(unknown_field) for unrecognized JSON keys,
		// but config files contain service-specific fields alongside shared grpcconfig fields.
		if err := fixSerdeUnknownFields(config.OutputDir); err != nil {
			return fmt.Errorf("failed to fix serde unknown fields: %w", err)
		}

		// Fix enum variants named Self. prost renames them to Self_ (Rust
		// refuses Self as a raw identifier), but protoc-gen-prost-serde still
		// refers to them as ::Self, which does not compile.
		if err := fixSerdeSelfVariant(config.OutputDir); err != nil {
			return fmt.Errorf("failed to fix serde Self variant: %w", err)
		}

		zap.L().Info("Successfully generated Rust code with protoc-gen-prost")
	}

	// Reorganize prost output into nested directories
	zap.L().Info("Reorganizing prost output")
	packageToDirMap, err := reorganizeProstOutput(absProtoSrcDir, config.OutputDir)
	if err != nil {
		return fmt.Errorf("failed to reorganize prost output: %w", err)
	}
	if err := suppressTonicGeneratedWarnings(config.OutputDir); err != nil {
		return fmt.Errorf("failed to suppress tonic generated warnings: %w", err)
	}

	// Generate gRPC re-export files for service protos
	// tonic-build generates client/server modules inline; create separate _grpc.rs files
	// to match the Go layout where *_grpc.pb.go is a separate file.
	zap.L().Info("Generating Rust gRPC re-export files")
	if err := generateRustGrpcFiles(config.OutputDir, absInputDir); err != nil {
		return fmt.Errorf("failed to generate Rust gRPC re-export files: %w", err)
	}

	// Fix crate:: imports in proto files to use correct nested paths
	zap.L().Info("Fixing crate imports")
	if err := fixCrateImports(config.OutputDir, packageToDirMap); err != nil {
		return fmt.Errorf("failed to fix crate imports: %w", err)
	}

	// Generate helper files first (constants, serialization, validation)
	// This creates mod.rs files that will be updated by generateModuleStructure
	zap.L().Info("Generating Rust helper files")
	allParsedFiles, err := processProtoFiles(absInputDir, config.OutputDir)
	if err != nil {
		return fmt.Errorf("failed to generate helper files: %w", err)
	}

	// Generate timeseries typed helpers (Row struct / from_proto / columns)
	// for messages with (spec.timeseries_type) annotation.
	zap.L().Info("Generating Rust timeseries helpers")
	if err := generateRustTimeseriesHelpers(allParsedFiles, config.OutputDir); err != nil {
		return fmt.Errorf("failed to generate Rust timeseries helpers: %w", err)
	}

	// Generate extraction functions (extract_<projection>_from_<source>) for messages
	// with a (spec.timeseries_type).source annotation.
	zap.L().Info("Generating Rust extraction helpers")
	if err := generateRustExtractionHelpers(allParsedFiles, config.OutputDir); err != nil {
		return fmt.Errorf("failed to generate Rust extraction helpers: %w", err)
	}

	// Scan service RPCs early — needed by both lib templates (authorizer) and
	// service wrappers (accounting). Must happen before writeLibTemplates.
	_, svcRPCs, vsEntries, err := proto.ScanAndGenerateConfigHolder(config.InputDir, absInputDir)
	if err != nil {
		return fmt.Errorf("failed to generate generated/*.proto: %w", err)
	}

	// Write library templates BEFORE service wrappers because writeLibTemplates
	// cleans the sys/ directory. Service wrappers build on top afterward.
	zap.L().Info("Writing Rust library templates")
	declaredAuds, err := proto.ScanAcceptedAudiences(config.InputDir, config.ServiceLabel())
	if err != nil {
		return fmt.Errorf("failed to scan (spec.service).accepted_audiences: %w", err)
	}
	if err := writeLibTemplates(config, svcRPCs, proto.AcceptedAudiencesForHost(config.ServiceLabel(), vsEntries, declaredAuds)); err != nil {
		return fmt.Errorf("failed to write library templates: %w", err)
	}

	// Whether any generated service wrapper carries an MCP termination. Decides
	// the model crate's MCP SDK dependency; set by generateServiceWrappers.
	hasMcpRpcs := false

	// Detect services and generate service wrappers (before module structure so mod.rs picks them up)
	servicesInfo, err := detectServicesForWrapper(config, absInputDir)
	if err != nil {
		return fmt.Errorf("failed to detect services: %w", err)
	}
	// Build grpcName → AccountingInfo mapping for accounting method generation.
	// Services whose grpcName is NOT in this map will skip accounting emit.
	// Methods not in AccountingInfo.RPCs are also skipped (e.g., synthesized
	// state service RPCs that share a grpcName but aren't in the enum).
	accountingMap := make(map[string]service.AccountingInfo, len(svcRPCs))
	for _, svc := range svcRPCs {
		rpcs := make(map[string]bool, len(svc.RPCs))
		for _, rpc := range svc.RPCs {
			rpcs[rpc] = true
		}
		accountingMap[svc.GrpcName] = service.AccountingInfo{EnumName: svc.EnumName, RPCs: rpcs}
	}

	if len(servicesInfo) > 0 {
		// Generate service wrappers — only for gRPC server services.
		serverServices := service.ResolveServerServices(config, servicesInfo)
		// Additively include own-package c2d daemon services: the Rust
		// ServiceManager hosts them on a local unix-socket C2dServer (HasC2DGrpc),
		// excluded from the s2s gRPC port. ResolveServerServices filters c2d out
		// (cross-lang default), so add them back here so the daemon server surface
		// (Handler + ValidationWrapper) is generated for client repos.
		for _, c2d := range service.ResolveC2DServerServices(config, servicesInfo) {
			dup := false
			for _, s := range serverServices {
				if s.ProtoFile == c2d.ProtoFile && s.ServiceName == c2d.ServiceName {
					dup = true
					break
				}
			}
			if !dup {
				serverServices = append(serverServices, c2d)
			}
		}
		if len(serverServices) > 0 {
			zap.L().Info("Generating Rust service wrappers", zap.Int("count", len(serverServices)))
			wrappersHaveMcp, err := generateServiceWrappers(serverServices, absInputDir, packageToDirMap, accountingMap, skippedTraceTypes)
			if err != nil {
				return fmt.Errorf("failed to generate service wrappers: %w", err)
			}
			hasMcpRpcs = wrappersHaveMcp
		}
	}

	// Generate gRPC client wrappers — typed client stubs for gRPC client services
	var clientServices []ServiceInfo
	if len(servicesInfo) > 0 {
		clientServices = service.ResolveClientServices(config, servicesInfo)
		if len(clientServices) > 0 {
			zap.L().Info("Generating Rust gRPC client wrappers", zap.Int("count", len(clientServices)))
			if err := generateGrpcClients(clientServices, absInputDir, packageToDirMap); err != nil {
				return fmt.Errorf("failed to generate gRPC clients: %w", err)
			}
		}
	}
	if err := parser.ValidateTokenMinters(config.AnnotatedProtoDir, s2sGenerationEnabled(config, servicesInfo)); err != nil {
		return err
	}

	// Generate c2sm (client-to-service) gRPC client wrappers and entry point
	if len(servicesInfo) > 0 {
		c2sServices := service.ResolveC2SClientServices(config, servicesInfo)
		if len(c2sServices) > 0 {
			origPrefix := config.SmPrefix
			config.SmPrefix = "c2sm"
			zap.L().Info("Generating Rust c2s gRPC client wrappers", zap.Int("count", len(c2sServices)))
			if err := generateGrpcClients(c2sServices, absInputDir, packageToDirMap); err != nil {
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

	// Generate c2dm (client-to-daemon) gRPC client wrappers and entry point when
	// the proto graph declares in-scope service_c2d services (or an explicit
	// GrpcC2DClients allowlist).
	if c2dServices := service.ResolveC2DClientServices(config, servicesInfo); len(c2dServices) > 0 {
		origPrefix := config.SmPrefix
		config.SmPrefix = "c2dm"
		zap.L().Info("Generating Rust c2d gRPC client wrappers", zap.Int("count", len(c2dServices)))
		if err := generateGrpcClients(c2dServices, absInputDir, packageToDirMap); err != nil {
			config.SmPrefix = origPrefix
			return fmt.Errorf("failed to generate c2d gRPC clients: %w", err)
		}
		if err := generateC2DServiceManager(config, c2dServices); err != nil {
			config.SmPrefix = origPrefix
			return fmt.Errorf("failed to generate c2d service manager: %w", err)
		}
		config.SmPrefix = origPrefix
	}

	// Generate w2sm (web-to-service) clients and entry point.
	if len(servicesInfo) > 0 {
		w2sServices := service.ResolveW2SClientServices(config, servicesInfo)
		if len(w2sServices) > 0 {
			origPrefix := config.SmPrefix
			config.SmPrefix = "w2sm"
			zap.L().Info("Generating Rust w2s clients", zap.Int("count", len(w2sServices)))
			if err := generateW2SClients(w2sServices, absInputDir, packageToDirMap); err != nil {
				config.SmPrefix = origPrefix
				return fmt.Errorf("failed to generate w2s clients: %w", err)
			}
			if err := generateW2SServiceManager(config); err != nil {
				config.SmPrefix = origPrefix
				return fmt.Errorf("failed to generate w2s service manager: %w", err)
			}
			config.SmPrefix = origPrefix
		}
	}

	// Generate REST client wrappers — typed REST stubs for gRPC client services
	if len(servicesInfo) > 0 {
		restClientServices := service.ResolveRestClientServices(config, servicesInfo)
		if len(restClientServices) > 0 {
			zap.L().Info("Generating Rust REST client wrappers", zap.Int("count", len(restClientServices)))
			if err := generateRestClients(restClientServices, absInputDir, packageToDirMap); err != nil {
				return fmt.Errorf("failed to generate REST clients: %w", err)
			}
		}
	}

	// Generate REST→gRPC dispatch code for services with rest_dispatch annotation.
	if config.HasRestDispatch && len(servicesInfo) > 0 {
		zap.L().Info("Generating Rust REST dispatch", zap.Bool("isGateway", config.IsGateway))
		if err := generateRestDispatch(config, servicesInfo, absInputDir); err != nil {
			return fmt.Errorf("failed to generate REST dispatch: %w", err)
		}
	}

	// Generate typed disk-persistence helpers for service_store messages (no-op
	// when there are none).
	if err := generateDiskStore(config, servicesInfo, absInputDir); err != nil {
		return fmt.Errorf("failed to generate diskstore helpers: %w", err)
	}

	// Generate proxy dispatch for gateway: validate_request() and response_to_json()
	// used by proxy_grpc to decode/validate each message frame.
	if config.IsGateway && config.HasRestDispatch && len(servicesInfo) > 0 {
		zap.L().Info("Generating Rust proxy dispatch (gateway)")
		if err := generateProxyDispatch(config, servicesInfo, absInputDir, accountingMap); err != nil {
			return fmt.Errorf("failed to generate proxy dispatch: %w", err)
		}
	}

	// Collect event-bus watches from legacy subscribe, explicit event_watch,
	// and validated store-derived watches.
	// Use absInputDir (not config.FlatProtoDir) so DeriveStoreSubscriptions can see
	// the projection messages (SessionRecord, etc.) with their
	// spec.timeseries_type annotations that were generated by
	// GenerateTransformationMessages earlier in this pipeline.
	config.Subscriptions, err = collectSubscriptions(config, absInputDir)
	if err != nil {
		return fmt.Errorf("failed to collect event-bus subscriptions: %w", err)
	}
	if len(config.Subscriptions) > 0 {
		zap.L().Info("Found event-bus subscriptions", zap.Int("count", len(config.Subscriptions)))
	}

	// Generate handler interface and service_manager.
	{
		var serverSvcs []service.ServiceInfo
		if len(servicesInfo) > 0 {
			serverSvcs = service.ResolveServerServices(config, servicesInfo)
			// Additively include own-package c2d daemon services so the s2sm
			// Handler trait + ServiceManager host them (HasC2DGrpc -> C2dServer
			// unix socket). Must mirror the augmentation done for the wrapper set
			// above, or a daemon-only repo (e.g. a client daemon — only service_c2d
			// services) generates the ValidationWrappers but no s2sm::handler::Handler.
			for _, c2d := range service.ResolveC2DServerServices(config, servicesInfo) {
				dup := false
				for _, s := range serverSvcs {
					if s.ProtoFile == c2d.ProtoFile && s.ServiceName == c2d.ServiceName {
						dup = true
						break
					}
				}
				if !dup {
					serverSvcs = append(serverSvcs, c2d)
				}
			}
		}
		if len(serverSvcs) > 0 || len(config.States) > 0 {
			var grpcSvcs []service.ServiceInfo
			// Filter to gRPC-annotated services, plus MCP-only services: a
			// category-A MCP-only service (no service_s2s/service_c2s/
			// service_c2d, so IsGrpc is false) still carries a synthesized MCP
			// session carrier RPC, materialized onto its proto the same way it
			// is for the wrapper generator above (see the isGrpc/hasMcp gate
			// near detectServicesForWrapper) — it needs a Handler trait getter
			// and a ServiceManager mount just like any other gRPC service, or
			// the wrapper it gets from generateServiceWrappers compiles but is
			// never reachable. The auto-discovery in ResolveServerServices
			// already handles SelfPackage + hosted vservices + synthetic store
			// services additively, so no further restrictive filter is needed
			// here.
			for _, svc := range serverSvcs {
				if svc.IsGrpc || svc.HasMcp {
					grpcSvcs = append(grpcSvcs, svc)
				}
			}
			zap.L().Info("Generating Rust handler interface and service_manager")
			if err := generateHandlerInterface(config, grpcSvcs, clientServices, allParsedFiles); err != nil {
				return fmt.Errorf("failed to generate handler interface: %w", err)
			}
			if err := generateServiceManagerSM(config, grpcSvcs, clientServices, allParsedFiles); err != nil {
				return fmt.Errorf("failed to generate service_manager: %w", err)
			}
		}
	}

	// Generate event-bus subscriber modules.
	if len(config.Subscriptions) > 0 {
		zap.L().Info("Generating Rust event-bus subscriber modules", zap.Int("count", len(config.Subscriptions)))
		if err := generateSubscribers(config); err != nil {
			return fmt.Errorf("failed to generate event-bus subscribers: %w", err)
		}
	}

	// Generate state client wrappers for services with service_state.
	// This scans protos independently (not relying on servicesInfo) so it finds
	// services that have no gRPC service definitions.
	if err := generateRustStateClientWrappers(config, absInputDir); err != nil {
		return fmt.Errorf("failed to generate state client wrappers: %w", err)
	}

	// Generate per-type REST CRUD clients (TypedHttpClient per proto package)
	restCrudPackages := resolveRestCrudPackagesRust(config, allParsedFiles)
	if len(restCrudPackages) > 0 {
		zap.L().Info("Generating Rust REST CRUD clients", zap.Int("count", len(restCrudPackages)))
		if err := generateRustRestCrudClients(config, restCrudPackages); err != nil {
			return fmt.Errorf("failed to generate REST CRUD clients: %w", err)
		}
	}

	// Detect if any proto has service definitions (tonic-build generates code for ALL services,
	// not just explicitly configured server wrappers, so tonic must be in Cargo.toml accordingly)
	hasServices := hasProtoServices(absInputDir) || len(servicesInfo) > 0

	// Generate utils directory (utils.rs, validation.rs)
	zap.L().Info("Generating Rust utils")
	if err := generateRustUtils(config); err != nil {
		return fmt.Errorf("failed to generate utils: %w", err)
	}

	// Generate module structure (mod.rs files) for prost output and wrappers
	// This appends proto module declarations to existing helper mod.rs files
	// Must run AFTER all .rs files are generated (service wrappers, lib templates, HTTP clients)
	zap.L().Info("Generating module structure")
	if err := generateModuleStructure(config.OutputDir, config.CrateBasePath); err != nil {
		return fmt.Errorf("failed to generate module structure: %w", err)
	}

	// Fix cross-module imports (add re-exports for super:: references)
	zap.L().Info("Fixing cross-module imports")
	if err := fixCrossModuleImports(config.OutputDir, packageToDirMap); err != nil {
		return fmt.Errorf("failed to fix cross-module imports: %w", err)
	}

	// Update lib.rs again to include helper module directories
	zap.L().Info("Updating lib.rs with helper modules")
	if err := updateLibFileWithHelpers(config.OutputDir); err != nil {
		return fmt.Errorf("failed to update lib.rs: %w", err)
	}

	// Create sys/spec/ with a minimal helpers.rs (matches Go/Python/TypeScript layout)
	specDir := filepath.Join(config.OutputDir, "chassis", "spec")
	if err := os.MkdirAll(specDir, 0755); err != nil {
		return fmt.Errorf("failed to create spec directory: %w", err)
	}
	specHelpers := filepath.Join(specDir, "helpers.rs")
	if err := os.WriteFile(specHelpers, []byte("// Auto-generated helpers for spec package.\n// This package provides annotation-related helpers used by the compiler.\n"), 0644); err != nil {
		return fmt.Errorf("failed to create spec helpers: %w", err)
	}

	// Create workspace Cargo.toml at rust/Cargo.toml (one level above types/)
	// This must happen AFTER HTTP client generation so we can detect client crate directories.
	// The workspace defines shared dependency versions for all crates.
	zap.L().Info("Creating workspace Cargo.toml")
	if err := createWorkspaceCargoToml(config, config.OutputDir, hasServices, len(config.Subscriptions) > 0, hasMcpRpcs); err != nil {
		return fmt.Errorf("failed to create workspace Cargo.toml: %w", err)
	}

	// Generate aggregation files for wrappers (grpc_servers, state_clients)
	zap.L().Info("Generating Rust aggregation files")
	if err := generateAggregationFiles(config); err != nil {
		return fmt.Errorf("failed to generate aggregation files: %w", err)
	}

	// Create descriptor parser BEFORE stripping — needs spec.* options for service metadata.
	dp, err := parser.NewDescriptorParser(absInputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser for registry: %w", err)
	}

	// Strip spec.* annotations from output proto files now that we're done with them.
	if err := proto.StripSpecOptionsFromDir(absProtoSrcDir); err != nil {
		return fmt.Errorf("failed to strip spec options: %w", err)
	}

	// Generate registry.json, registry.schema.json, AGENT.md (shared across all languages).
	if err := registry.Generate(registry.Config{
		OutputDir:   config.OutputDir,
		VersionTag:  config.VersionTag,
		InputDir:    absInputDir,
		Language:    "rust",
		PackageBase: config.CrateBasePath,
	}, allParsedFiles, dp); err != nil {
		return fmt.Errorf("failed to generate registry.json: %w", err)
	}
	if err := updateLibFileWithHelpers(config.OutputDir); err != nil {
		return fmt.Errorf("failed to update lib.rs after registry helper generation: %w", err)
	}

	zap.L().Info("Rust generation complete")
	zap.L().Info("To build", zap.String("command", "cd "+config.OutputDir+" && cargo build"))
	return nil
}

// detectServicesForWrapper scans proto files to find service definitions that need wrapper generation.
// Includes annotated gRPC services and state services (service_state on messages).
func detectServicesForWrapper(config *tactype.Config, inputDir string) ([]ServiceInfo, error) {
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

		// Detect gateway and rest_dispatch annotations on the service's own proto file.
		// Must happen before the Services-empty check because the service's own
		// proto (e.g. myclient.proto) may carry rest_dispatch without defining
		// any gRPC service definitions.
		if config.SelfPackage != "" && fileDesc.Package == config.SelfPackage {
			if fileDesc.ServiceGateway {
				config.IsGateway = true
			}
			if fileDesc.ServiceRestDispatch {
				config.HasRestDispatch = true
			}
		}

		if len(fileDesc.Services) == 0 {
			return nil
		}

		// Check if any service has a gRPC annotation, or any message has service_state.
		hasGrpc := false
		for _, svc := range fileDesc.Services {
			if svc.GrpcName != "" {
				hasGrpc = true
				break
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

		parentDir := filepath.Dir(relPath)
		packageName := fileDesc.Package

		// Place servicewrapper.rs in sm/internal/grpc/servers/{parentDir}/{packageName}/
		// Only use ServiceProtoParts when the package is qualified (e.g. "services.sentinel"),
		// not when it's a simple name like "telemetry" which would be wrongly
		// overridden by the hyphenated filename stem "claude-sensor".
		actualPkg := packageName
		if strings.Contains(packageName, ".") {
			if serviceName, _ := proto.ServiceProtoParts(relPath); serviceName != "" {
				actualPkg = serviceName
			}
		}
		subdir := filepath.Join(config.GetSmPrefix(), "internal", "grpc", "servers", parentDir, actualPkg)
		outputDir := filepath.Join(config.OutputDir, subdir)

		for _, service := range fileDesc.Services {
			isGrpc := service.GrpcName != ""
			// A category-A MCP-only service (no service_s2s/service_c2s/service_c2d,
			// so GrpcName is "") still needs its wrapper generated: the synthesized
			// MCP session carrier RPC was already materialized onto its proto, and
			// skipping it here means mcp_dispatch_gen.go never runs for it either.
			hasMcp := false
			for _, m := range service.Methods {
				if m.IsMcp {
					hasMcp = true
					break
				}
			}
			if !isGrpc && !hasState && !hasMcp {
				continue
			}
			// Derive service-level auth flags from method-level annotations.
			anyUser := service.AuthAnyUser
			// A service with no RPCs registers nothing, and its wrapper emits no
			// register-method-auth function; see the Go generator for the
			// ServiceManager call this would otherwise dangle.
			hasMethodAuth := len(service.Methods) > 0 && len(service.AuthServices) > 0
			// Methods declared auth_disabled must bypass the server's
			// AuthEnforcingLayer, which runs before the per-method wrappers and
			// would otherwise 401 them regardless of the annotation.
			var authExempt []string
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
				if service.AuthDisabled || m.AuthDisabled {
					authExempt = append(authExempt, fmt.Sprintf("/%s.%s/%s", packageName, service.Name, m.Name))
				}
			}
			servicesInfo = append(servicesInfo, ServiceInfo{
				ProtoFile:       relPath,
				PackageName:     packageName,
				ServiceName:     service.Name,
				GrpcName:        service.GrpcName,
				IsGrpc:          isGrpc,
				HasMcp:          hasMcp,
				IsS2S:           service.IsS2S,
				IsC2S:           service.IsC2S,
				IsC2D:           service.IsC2D,
				C2DSocketPath:   service.C2DSocketPath,
				OutputDir:       outputDir,
				ParentDir:       parentDir,
				Config:          config,
				AuthDisabled:    service.AuthDisabled,
				AuthAnyUser:     anyUser,
				AuthServices:    service.AuthServices,
				HasMethodAuth:   hasMethodAuth,
				AuthExemptPaths: authExempt,
				EventLabel:      service.EventLabel,
				HostLabel:       service.HostLabel,
				// Propagate the synthesized-service marker so ResolveServerServices'
				// filterByPackage can drop tacplus-generated state/store/config-state
				// services. Omitting this (the Rust detect path did) left Synthesized
				// false for every service, so after 0ed57a2 switched filtering from the
				// "S2S && empty auth_services" heuristic to the Synthesized marker, the
				// generated ConfigStateService leaked into the Rust handler trait as a
				// spurious required <svc>_config_state_config handler.
				Synthesized:     service.Synthesized,
				SynthesizedKind: service.SynthesizedKind,
			})
		}

		return nil
	})

	return servicesInfo, err
}

// hasProtoServices scans proto files to check if any contain service definitions.
// This is independent of annotations - tonic_build generates code for ALL
// services in compiled protos, so tonic must be in Cargo.toml even without wrappers.
// Uses Contains on raw proto text (quick scan before full descriptor parsing).
func hasProtoServices(inputDir string) bool {
	found := false
	filepath.Walk(inputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || found || info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for _, line := range strings.Split(string(data), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "service ") && strings.Contains(trimmed, "{") {
				found = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	return found
}

// mergeProstProtocOutput copies protoc-gen-prost output files to outputDir.
// protoc-gen-prost outputs to subdirectories: pkg/pkg.rs (main types with
// include!("pkg.serde.rs") directives) and pkg/pkg.serde.rs (serde impls).
// This function copies all files flat to outputDir so the reorganize pipeline
// can move them into nested directories.
func mergeProstProtocOutput(prostTempDir, outputDir string) error {
	entries, err := os.ReadDir(prostTempDir)
	if err != nil {
		return fmt.Errorf("failed to read prost temp directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		pkgName := entry.Name()
		pkgDir := filepath.Join(prostTempDir, pkgName)

		// Copy all .rs files from this package directory to outputDir
		files, err := os.ReadDir(pkgDir)
		if err != nil {
			continue
		}

		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".rs") {
				continue
			}

			data, err := os.ReadFile(filepath.Join(pkgDir, f.Name()))
			if err != nil {
				return fmt.Errorf("failed to read %s/%s: %w", pkgName, f.Name(), err)
			}

			outPath := filepath.Join(outputDir, f.Name())
			if err := os.WriteFile(outPath, data, 0644); err != nil {
				return fmt.Errorf("failed to write %s: %w", outPath, err)
			}

			zap.L().Debug("Copied prost file", zap.String("package", pkgName), zap.String("file", f.Name()))
		}
	}

	return nil
}

// fixSerdeUnknownFields modifies protoc-gen-prost-serde output to skip unknown fields
// instead of rejecting them. Config files contain service-specific fields alongside
// shared grpcconfig fields; strict rejection breaks deserialization.
//
// Changes in each .serde.rs file:
//  1. GeneratedField enum: add __SkipField__ variant
//  2. visit_str: return Ok(GeneratedField::__SkipField__) instead of Err(unknown_field)
//  3. visit_map: add __SkipField__ arm that skips the value
func fixSerdeUnknownFields(outputDir string) error {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return fmt.Errorf("failed to read output directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".serde.rs") {
			continue
		}

		filePath := filepath.Join(outputDir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", filePath, err)
		}

		content := string(data)
		modified := false

		// 1. Add __SkipField__ to each GeneratedField enum
		if strings.Contains(content, "enum GeneratedField {") {
			content = strings.ReplaceAll(content,
				"enum GeneratedField {",
				"enum GeneratedField {\n            __SkipField__,")
			modified = true
		}

		// 2. Replace unknown_field error with __SkipField__
		if strings.Contains(content, `Err(serde::de::Error::unknown_field(value, FIELDS))`) {
			content = strings.ReplaceAll(content,
				`Err(serde::de::Error::unknown_field(value, FIELDS))`,
				`Ok(GeneratedField::__SkipField__)`)
			modified = true
		}

		// 3. Add __SkipField__ handler before each closing of the match block in visit_map.
		// Pattern: the visit_map match has "GeneratedField::SomeName => {" arms followed by "}".
		// We need to add "GeneratedField::__SkipField__ => { let _ = map_.next_value::<serde::de::IgnoredAny>()?; }"
		// This is tricky with simple string replacement, so we use line-by-line processing.
		lines := strings.Split(content, "\n")
		var result []string
		for i := 0; i < len(lines); i++ {
			line := lines[i]
			result = append(result, line)

			// Detect the pattern: "while let Some(k) = map_.next_key()? {"
			// followed by "match k {"
			trimmed := strings.TrimSpace(line)
			if trimmed == "match k {" {
				// Scan forward to find the closing "}" of the match block.
				// Insert __SkipField__ handler just before it.
				braceDepth := 1
				matchStart := len(result) // index after "match k {"
				for j := i + 1; j < len(lines); j++ {
					innerTrimmed := strings.TrimSpace(lines[j])
					for _, ch := range innerTrimmed {
						if ch == '{' {
							braceDepth++
						} else if ch == '}' {
							braceDepth--
						}
					}

					if braceDepth == 0 {
						// This is the closing brace of match k {}
						// Insert __SkipField__ handler before it
						result = append(result,
							"                        GeneratedField::__SkipField__ => {",
							"                            let _ = map_.next_value::<serde::de::IgnoredAny>()?;",
							"                        }")
						result = append(result, lines[j])
						i = j
						modified = true
						break
					}
					result = append(result, lines[j])
				}
				_ = matchStart // suppress unused warning
			}
		}
		content = strings.Join(result, "\n")

		if modified {
			if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
				return fmt.Errorf("failed to write %s: %w", filePath, err)
			}
			zap.L().Debug("Fixed serde unknown fields", zap.String("file", entry.Name()))
		}
	}

	return nil
}

// serdeSelfVariantRe matches a reference to an enum variant named Self. The
// trailing (?:[A-Za-z0-9_]) exclusion keeps Self_ and variants that merely
// start with Self (SelfSigned) untouched.
var serdeSelfVariantRe = regexp.MustCompile(`::Self([^A-Za-z0-9_])`)

// fixSerdeSelfVariant rewrites ::Self to ::Self_ in protoc-gen-prost-serde
// output. A proto enum value that reduces to the variant name Self (for
// example AUTH_METHOD_SELF on enum AuthMethod) is emitted by prost as Self_,
// because Rust rejects Self as a raw identifier. protoc-gen-prost-serde does
// not apply that sanitizing and emits ::Self, which fails to compile.
func fixSerdeSelfVariant(outputDir string) error {
	entries, err := os.ReadDir(outputDir)
	if err != nil {
		return fmt.Errorf("failed to read output directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".serde.rs") {
			continue
		}

		filePath := filepath.Join(outputDir, entry.Name())
		data, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", filePath, err)
		}

		fixed := serdeSelfVariantRe.ReplaceAll(data, []byte("::Self_$1"))
		if len(fixed) == len(data) {
			continue
		}

		if err := os.WriteFile(filePath, fixed, 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", filePath, err)
		}
		zap.L().Debug("Fixed serde Self variant", zap.String("file", entry.Name()))
	}

	return nil
}

func suppressTonicGeneratedWarnings(outputDir string) error {
	return filepath.WalkDir(outputDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".tonic.rs") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", path, err)
		}
		content := string(data)
		if strings.Contains(content, "non_camel_case_types,\n    unused_variables") ||
			strings.Contains(content, "non_camel_case_types,\nunused_variables") {
			return nil
		}

		updated := strings.ReplaceAll(
			content,
			"unused_variables,",
			"non_camel_case_types,\n    unused_variables,",
		)
		if updated == content {
			return nil
		}
		if err := os.WriteFile(path, []byte(updated), 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", path, err)
		}
		return nil
	})
}

// generateRustGrpcFiles creates *_grpc.rs files for proto files that have tonic-generated
// gRPC service modules. protoc-gen-tonic generates client/server modules that are merged
// inline in the main .rs file; this function creates separate _grpc.rs wrapper files that
// re-export those modules, matching the Go layout where *_grpc.pb.go is a separate file.
func generateRustGrpcFiles(outputDir, inputDir string) error {
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	return filepath.Walk(inputDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return walkErr
		}

		relPath, _ := filepath.Rel(inputDir, path)
		fileDesc, err := descriptorParser.GetFileDescriptor(relPath)
		if err != nil || len(fileDesc.Services) == 0 {
			return nil
		}

		// Determine output directory (same as reorganizeProstOutput)
		normalizedPath := strings.TrimPrefix(filepath.ToSlash(relPath), "tacplus/")
		dir := filepath.Dir(normalizedPath)
		serviceName := strings.TrimSuffix(filepath.Base(relPath), ".proto")

		var targetDir string
		if strings.HasPrefix(dir, "sys") {
			targetDir = filepath.Join(outputDir, "types", dir, serviceName)
		} else if filepath.Base(dir) == serviceName {
			targetDir = filepath.Join(outputDir, "types", dir, serviceName)
		} else {
			targetDir = filepath.Join(outputDir, "types", dir, serviceName)
		}

		// Check if the main .rs file exists with tonic modules
		mainRs := filepath.Join(targetDir, serviceName+".rs")
		if _, err := os.Stat(mainRs); os.IsNotExist(err) {
			return nil
		}

		// Check the main .rs and its .tonic.rs sibling for tonic client/server modules.
		// protoc-gen-tonic generates these in a separate .tonic.rs file that is include!()'d.
		var contentStr string
		if data, err := os.ReadFile(mainRs); err == nil {
			contentStr = string(data)
		}
		tonicRs := filepath.Join(targetDir, serviceName+".tonic.rs")
		if data, err := os.ReadFile(tonicRs); err == nil {
			contentStr += string(data)
		}

		hasClient := strings.Contains(contentStr, "_client {")
		hasServer := strings.Contains(contentStr, "_server {")
		if !hasClient && !hasServer {
			return nil
		}

		// Generate _grpc.rs that re-exports the tonic modules
		var b strings.Builder
		b.WriteString("// Code generated by tacplus. DO NOT EDIT.\n")
		b.WriteString("// gRPC service re-exports from tonic-generated code.\n\n")

		for _, svc := range fileDesc.Services {
			moduleName := tonicServiceModuleName(svc.Name)
			if hasClient {
				fmt.Fprintf(&b, "#[allow(ambiguous_glob_reexports, unused_imports)]\npub use super::%s::%s_client::*;\n", serviceName, moduleName)
			}
			if hasServer {
				fmt.Fprintf(&b, "#[allow(ambiguous_glob_reexports, unused_imports)]\npub use super::%s::%s_server::*;\n", serviceName, moduleName)
			}
		}

		grpcPath := filepath.Join(targetDir, serviceName+"_grpc.rs")
		if err := os.WriteFile(grpcPath, []byte(b.String()), 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", grpcPath, err)
		}

		zap.L().Debug("Generated gRPC re-export", zap.String("file", serviceName+"_grpc.rs"))
		return nil
	})
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
func s2sGenerationEnabled(config *tactype.Config, servicesInfo []ServiceInfo) bool {
	if config.GrpcS2SClientAll || len(config.GrpcS2SClients) > 0 {
		return true
	}
	for _, svc := range service.ResolveServerServices(config, servicesInfo) {
		if svc.IsS2S && svc.SynthesizedKind != parser.SynthesizedKindStore {
			return true
		}
	}
	return false
}
