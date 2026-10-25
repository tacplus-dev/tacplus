// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"fmt"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/buildinfo"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/boundaries"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/eventbus"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/extraction"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/keepaliveconst"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	stateproto "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// grpcServiceEntry holds per-service data for handler and service_manager templates.
type grpcServiceEntry struct {
	// Handler trait methods
	GetterName       string // "identity_grpc_impl"
	HandleConfigName string // "identity_config"
	// Proto package name (snake_case)
	PackageName string // "identity"
	// gRPC server type
	ServerType string // "IdentityServiceServer"
	// tonic server module name (snake_case service name + "_server")
	ServerModName string // "identity_service_server"
	// Rust module path for proto types
	ModPath string // "crate::types::services::identity::identity"
	// Rust base path for types (without trailing package repeat)
	TypesBasePath string // "crate::types::services::identity"
	TraitName     string // "IdentityService" (tonic server trait)
	// Handler trait name (clean interface with Caller)
	HandlerTraitName string // "IdentityServiceHandler"
	// Config module path
	ConfigModPath string // "crate::types::services::identity::identity"
	// ConfigHelpersModPath is the module holding the generated config_defaults
	// for this service's Config, e.g. "crate::types::services::identity::helpers".
	ConfigHelpersModPath string
	// Service wrapper module (absolute crate path)
	WrapperModPath string // "crate::s2sm::internal::grpc::servers::services::identity::service"
	ConstructorFn  string // "new_identity_service"
	WrapperVar     string // "wrapper_identity"
	// State config state accessor
	ConfigStateGroupAccessor string // "services"
	ConfigStateAccessor      string // "identity_config_state"
	// HasConfigState is true when the service's package has a ConfigState
	// generated (from grpc_config annotation). False for services like
	// services without a generated config state that don't have their own config state.
	HasConfigState bool
	// IsS2S is true when this is a service-to-service gRPC service.
	IsS2S bool
	// IsC2S is true when this is a client-to-service gRPC service.
	IsC2S bool
	// IsC2D is true when this is a client-to-daemon gRPC service (unix socket, no auth).
	IsC2D bool
	// HasMcp is true when this service's only gRPC surface is a synthesized MCP
	// session carrier RPC (no service_s2s/service_c2s/service_c2d annotation).
	// It needs the same mandatory-auth wiring as a C2S service: the carrier
	// authenticates its caller via the wrapper's authorize_method, exactly like
	// any other user-facing RPC.
	HasMcp bool
	// C2DSocketPath is the unix socket declared by service_c2d (required, no default).
	C2DSocketPath string
	// AuthServices lists the allowed S2S callers (service-level default).
	AuthServices []string
	// AuthDisabled skips set_authorize_func on this service's server wrapper.
	AuthDisabled bool
	// AuthAnyUser wires fixed-namespace authenticate-at-or-below instead of full authorize.
	AuthAnyUser bool
	// HasMethodAuth is true when the wrapper has a register_method_auth function.
	HasMethodAuth bool
	// HasRequiresDevice is true when this service's wrapper carries the
	// (spec.rpc).requires_device gate, so the ServiceManager must hand it the
	// approval check. A wrapper without one has no seam to wire.
	HasRequiresDevice bool
	// AuthExemptPaths are full gRPC paths this service declares auth_disabled.
	// They must bypass the server's AuthEnforcingLayer, which otherwise rejects
	// them before the per-method wrapper ever runs.
	AuthExemptPaths []string
	// IsHosted is true when this entry came from a vservice declaring HostLabel ==
	// SelfPackage rather than from the host service's own proto package.
	// Hosted entries get a default Handler trait impl that returns a stub,
	// so services can compile without immediately implementing every method.
	IsHosted bool
	// IsGeneratedRecordC2S marks the synthesized <Svc>Records surface, whose
	// handler is GENERATED (s2sm/record_c2s.rs) rather than written. It is the
	// only service for which the Handler trait carries no getter at all: there is
	// one correct body for its RPCs, that body is the authorization, and an
	// author-supplied override is not a customisation point anyone wants.
	IsGeneratedRecordC2S bool
	// GeneratedImplStruct names that generated implementation.
	GeneratedImplStruct string
}

// rustConfigHandlerEntry names one handler fed by a shared config fetch.
type rustConfigHandlerEntry struct {
	HandleConfigName string // "timedb_query_config"
	TraitName        string // "TimedbQuery"
}

// rustConfigFetchEntry groups every config-handler block that reads the same
// ConfigState into a single startup fetch. The fetch is the same unary read for
// each block, so issuing one per block bursts N identical calls at startup and
// trips the state store's per-(peer, method) polling rate limit.
type rustConfigFetchEntry struct {
	ConfigStateGroupAccessor string // "services"
	ConfigStateAccessor      string // "timedb_config_state"
	ConfigHelpersModPath     string // "crate::types::services::timedb::helpers"
	ConfigModPath            string // "crate::types::services::timedb::timedb"
	ConfigAliasType          string // "TimedbQueryConfig" — alias of the shared Config type
	Handlers                 []rustConfigHandlerEntry
}

type rustDiskStoreRuntimeEntry struct {
	StoreName     string
	LogField      string
	HandlerGetter string
}

// smTemplateData holds the data passed to handler.rs.tmpl and service_manager.rs.tmpl.
type smTemplateData struct {
	CrateBasePath           string
	CrateBasePathUnderscore string
	SmPrefix                string
	ServiceLabel            string
	HasGrpc                 bool
	HasTlsGrpc              bool // true when non-c2d gRPC services exist (need TLS server)
	HasAuthGrpc             bool
	HasS2SGrpc              bool
	HasC2SGrpc              bool
	HasMcpGrpc              bool // true when any service is an MCP-only carrier (needs the same authorizer wiring as C2S)
	HasC2DGrpc              bool
	C2DSocketPath           string // service_c2d socket_path the daemon binds (required, no default)
	HostsC2DStandalone      bool   // emit serve_c2d_standalone: c2d is the whole surface and nothing needs the cluster (service.HostsC2DStandalone)
	HasStates               bool
	HasSysState             bool
	HasS2SClients           bool
	S2STargetsRequired      bool // s2s targets listed explicitly (grpc_s2s_clients) gate readiness; s2s_client_all targets do not
	HasServiceHealthState   bool
	// ClientOnly is the zero-rpc shape that only dials s2s peers: no rpc of any
	// kind, no smb, no subscription, and at least one s2s client edge. run() then
	// serves no listener, publishes no instance health and never waits for a
	// serving cert, so the host does not join its parent service's endpoint set;
	// it still runs the health poller that fills the dynamic s2s connections.
	ClientOnly            bool
	HasAutoTls            bool
	AutoTlsKeyType        string
	HasSmb                bool // true when this service is a spec.smb Service Mesh Blackbox service (SPIFFE plan D7)
	SmbAuthorityServiceID string
	IsGateway             bool
	HasSubscriptions      bool
	HealthStateProtoDir   string
	HealthStateModPath    string // Rust module path, e.g. "services::identity"
	StateStoreServiceID   string
	HasTypedHealth        bool   // true when the service's package declares a service_health message
	TypedHealthType       string // e.g. "ColumnarStoreHealth"
	TypedHealthModPath    string // Rust module path, e.g. "crate::types::services::columnarstore::columnarstore"
	TacplusVersion        string
	TacplusGitSha         string
	ProtoSourceVersion    string
	ProtoSourceGitSha     string
	GrpcServices          []grpcServiceEntry
	ConfigFetches         []rustConfigFetchEntry
	Subscriptions         []subscriptionEntry
	// TimeseriesInserts are every subscription's insert callbacks, once each.
	TimeseriesInserts []rustTimeseriesInsertEntry
	Publishers        []publisherEntry // model-driven EventBus topic publishers (own (spec.service_event) topics)
	HasPublishers     bool
	RecordReaders     []recordReaderEntry // model-driven (spec.record_read) typed readers over stored record rows
	HasRecordReaders  bool
	// RecordC2S is the generated handler for the client-facing record read plane,
	// nil unless this service declares (spec.record_read).c2s.
	RecordC2S *recordC2SFile
	// RecordC2SService names the ONE service whose handler implementation is
	// generated rather than supplied by the author ("<Svc>Records"), or "".
	RecordC2SService string
	TracePublisherFn string
	// AccountingPublisher is the generator-resolved publisher for the durable
	// accounting record topic. nil when the proto set declares no accounting
	// record (client repos), in which case no emitter is wired at all.
	AccountingPublisher *publisherEntry
	TargetServices      []TargetServiceInfo // per-target-service connection info for typed accessors
	DiskStores          []rustDiskStoreRuntimeEntry
	HasDiskStores       bool
	// HasApprovalBroker is true when some hosted service carries the
	// (spec.rpc).requires_device gate, so the ServiceManager builds the check
	// and wires it into those wrappers.
	HasApprovalBroker          bool
	ApprovalBroker             rustApprovalBrokerEntry
	KeepaliveTimeSecs          int
	KeepaliveTimeoutSecs       int
	KeepalivePermitMinTimeSecs int
}

// subscriptionEntry holds per-subscription data for the service_manager template.
type subscriptionEntry struct {
	FieldSnake        string // e.g. "event_stream"
	HandlerMethodName string // e.g. "on_event_stream" or "on_event_stream_aegis" (disambiguated)
	SubscriberMod     string // e.g. "aegis_event_stream" — module name under subscribe/
	SourcePackage     string // e.g. "aegis"
	SourceProtoDir    string // e.g. "common", "services"
	TypeProtoDir      string // e.g. "common" — proto directory where the type is defined
	TypePackage       string // e.g. "sensor" — package where the type is defined
	TypeName          string // e.g. "TopologyEvent"
	TargetService     string // e.g. "eventbus" — service_id to resolve via ServiceHealthState
	// RoutingOnly / EnvelopeTypeName mirror tactype.SubscriptionEntry: the trait
	// method takes the narrow envelope when this service is named in the source
	// type's s2s_subscribe.routing_only_services.
	RoutingOnly      bool
	EnvelopeTypeName string // e.g. "TopologyEventRoutingEnvelope"
	// DispatchFn is the source's async record dispatch,
	// "crate::types::<dir>::<pkg>::handle_<source>" ("" when nothing projects
	// from it), awaited after the Handler method with Inserts in its parameter
	// order.
	DispatchFn string
	Inserts    []rustTimeseriesInsertEntry
}

// rustTimeseriesInsertEntry is one typed timeseries insert callback the
// service manager holds: one per projection, shared by every subscription to
// its source. Go's TimeseriesInsertEntry.
type rustTimeseriesInsertEntry struct {
	TypeName   string // projection message, e.g. "SensorReading"
	ModPath    string // its types module, e.g. "crate::types::services::sensor"
	FieldName  string // e.g. "insert_sensor_reading"
	SetterName string // e.g. "set_sensor_reading_insert"
}

// buildSMTemplateData constructs the template data for handler.rs and service_manager.rs.
// clientServices are the resolved S2S client ServiceInfo entries (from grpcclient_gen).
// allFiles feeds the record dispatch of each subscription (may be nil when there are none).
func buildSMTemplateData(config *tactype.Config, svcs []service.ServiceInfo, clientServices []ServiceInfo, allFiles []model.ProtoFile) (smTemplateData, error) {
	configStates, err := stateproto.ConfigStateAccessors(config, config.FlatProtoDir)
	if err != nil {
		return smTemplateData{}, err
	}

	// First pass: detect duplicate service name prefixes (e.g., "Proxy" in
	// anthropic/github/gitlab/openai). Duplicates need package-qualified names.
	prefixCount := make(map[string]int)
	for _, svc := range svcs {
		snakeName := tonicServiceModuleName(svc.ServiceName)
		prefix := strings.TrimSuffix(snakeName, "_service")
		prefixCount[prefix]++
	}

	// (spec.rpc).requires_device: which hosted services carry the gate.
	requiresDeviceSvcs, err := rustRequiresDeviceServices(config.FlatProtoDir)
	if err != nil {
		return smTemplateData{}, err
	}

	var grpcSvcs []grpcServiceEntry
	for _, svc := range svcs {
		snakeName := tonicServiceModuleName(svc.ServiceName)
		snakePrefix := strings.TrimSuffix(snakeName, "_service")
		tonicName := tonicServiceTypeName(svc.ServiceName)
		tonicServerModName := tonicServiceModuleName(svc.ServiceName) + "_server"

		// Disambiguate identifiers when multiple services share the same name
		// (e.g., Proxy → AnthropicProxy, GithubProxy, etc.)
		disambiguated := snakePrefix
		traitAlias := tonicName
		if prefixCount[snakePrefix] > 1 {
			disambiguated = svc.PackageName + "_" + snakePrefix
			traitAlias = strutil.ToTonicCamelCase(svc.PackageName) + tonicName
		}

		// Compute the wrapper module name the same way servicewrapper_gen.go does:
		// strip the package prefix from the snake-cased service name.
		wrapperModName := strutil.StripPackagePrefix(snakeName, svc.PackageName)
		// Use the service's actual parent directory (e.g. "services", "sys")
		// instead of hardcoding "services" so that lib protos like grpcconfig
		// resolve to the correct module path.
		parentMod := strings.ReplaceAll(strings.ReplaceAll(svc.ParentDir, "-", "_"), string(filepath.Separator), "::")
		typesBase := fmt.Sprintf("crate::types::%s::%s", parentMod, svc.PackageName)

		// Config types live in the physical service's package, not the gRPC service's.
		// For virtual services (e.g., OpaEval in package "opa_eval"), config is in
		// the physical service's package (e.g., "opa").
		configPkg := svc.PackageName
		configParentMod := parentMod
		if config.SelfPackage != "" && svc.PackageName != config.SelfPackage {
			configPkg = config.SelfPackage
			configParentMod = "services"
		}
		configTypesBase := fmt.Sprintf("crate::types::%s::%s", configParentMod, configPkg)
		configStateGroupAccessor := strings.ReplaceAll(configParentMod, "::", "_")
		configStateAccessor := configPkg + "_config_state"
		hasConfigState := false
		if stateInfo, ok := configStates[configPkg]; ok {
			configStateAccessor = strutil.ToSnakeCase(stateInfo.StateName)
			stateDir := filepath.ToSlash(filepath.Dir(stateInfo.ProtoFile))
			if stateDir != "" && stateDir != "." {
				configStateGroupAccessor = strutil.ToSnakeCase(strings.Split(stateDir, "/")[0])
			}
			hasConfigState = true
		}
		grpcSvcs = append(grpcSvcs, grpcServiceEntry{
			GetterName:               disambiguated + "_grpc_impl",
			HandleConfigName:         disambiguated + "_config",
			PackageName:              svc.PackageName,
			ServerType:               tonicName + "Server",
			ServerModName:            tonicServerModName,
			ModPath:                  fmt.Sprintf("%s::%s", typesBase, svc.PackageName),
			TypesBasePath:            typesBase,
			TraitName:                traitAlias,
			HandlerTraitName:         svc.ServiceName + "Handler",
			ConfigModPath:            fmt.Sprintf("%s::%s", configTypesBase, configPkg),
			ConfigHelpersModPath:     fmt.Sprintf("%s::helpers", configTypesBase),
			WrapperModPath:           fmt.Sprintf("crate::s2sm::internal::grpc::servers::%s::%s::%s", parentMod, svc.PackageName, wrapperModName),
			ConstructorFn:            "new_" + snakeName,
			WrapperVar:               "wrapper_" + disambiguated,
			ConfigStateGroupAccessor: configStateGroupAccessor,
			ConfigStateAccessor:      configStateAccessor,
			HasConfigState:           hasConfigState,
			IsS2S:                    svc.IsS2S,
			IsC2S:                    svc.IsC2S,
			IsC2D:                    svc.IsC2D,
			HasMcp:                   svc.HasMcp,
			C2DSocketPath:            svc.C2DSocketPath,
			AuthServices:             authServicesForService(svc),
			AuthDisabled:             svc.AuthDisabled,
			AuthAnyUser:              svc.AuthAnyUser,
			HasMethodAuth:            svc.HasMethodAuth,
			HasRequiresDevice:        requiresDeviceSvcs[svc.PackageName+"."+svc.ServiceName],
			AuthExemptPaths:          svc.AuthExemptPaths,
			IsHosted:                 config.SelfPackage != "" && svc.PackageName != config.SelfPackage,
			// The one service whose handler the ServiceManager supplies itself.
			IsGeneratedRecordC2S: svc.SynthesizedKind == parser.SynthesizedKindRecordC2S,
			GeneratedImplStruct:  svc.ServiceName + "GeneratedHandler",
		})
	}
	diskStores, err := collectRustDiskStoreRuntimeEntries(config, grpcSvcs)
	if err != nil {
		return smTemplateData{}, err
	}

	hasAuthGrpc := false
	hasS2SGrpc := false
	hasC2SGrpc := false
	hasMcpGrpc := false
	hasC2DGrpc := false
	c2dSocketPath := ""
	hasTlsGrpc := false
	for _, g := range grpcSvcs {
		if !g.AuthDisabled {
			hasAuthGrpc = true
		}
		if g.IsS2S {
			hasS2SGrpc = true
			hasTlsGrpc = true
		}
		if g.IsC2S {
			hasC2SGrpc = true
			hasTlsGrpc = true
		}
		if g.HasMcp {
			hasMcpGrpc = true
			// The MCP session carrier is a real client-facing RPC (same TLS
			// listener as a C2S service) — without this, the entire TLS
			// server/router/add_service block in the run() template (gated
			// on HasTlsGrpc) is skipped and the carrier is never mounted at
			// all, even though the Handler getter and wrapper are wired.
			hasTlsGrpc = true
		}
		if g.IsC2D {
			hasC2DGrpc = true
			if g.C2DSocketPath == "" {
				return smTemplateData{}, fmt.Errorf("service %s: service_c2d requires socket_path (no default — a shared default socket would collide)", g.TraitName)
			}
			c2dSocketPath = g.C2DSocketPath
		}
	}

	hasServiceHealthState := false
	healthStateProtoDir := ""
	stateStoreServiceID := "state-store"
	hasSysState := false
	for _, b := range config.States {
		if b.IsHealthState && b.AccessMode != tactype.AccessRO {
			dir := strings.TrimSuffix(b.ProtoFile, ".proto")
			if dir != "" {
				hasServiceHealthState = true
				healthStateProtoDir = dir
				stateInfo, err := stateproto.DiscoverStateRoot(config.FlatProtoDir, b.MessageName)
				if err != nil {
					return smTemplateData{}, err
				}
				healthStateProtoDir = generatedTypesRelDir(stateInfo.ProtoFile, stateInfo.Package)
				stateStoreServiceID = stateInfo.Package
			}
		}
		if strings.HasPrefix(b.ProtoFile, "sys/") {
			hasSysState = true
		}
	}
	if config.AllStates {
		hasSysState = true
	}

	// Build subscription entries from config.
	// Handler method names are ALWAYS qualified with the topic stem.
	//
	// This used to append the stem only when more than one subscription shared a
	// field name, which made a method's name depend on its SIBLINGS: retiring
	// three unrelated streams renamed the surviving one from
	// on_event_stream_agentsensor_agent_event_bus_event_stream to on_event_stream
	// and broke timedb's hand-written build.rs with "method ... is not a member of
	// trait `Handler`". A name that changes when an unrelated stream is added or
	// removed is a hazard for every hand-written impl of the trait, so the shorter
	// unique-case name is not worth it.

	// Build a package→protoDir map for resolving type locations,
	// and detect typed health messages in the service's own package.
	pkgToDirMap := make(map[string]string)
	hasTypedHealth := false
	hasSmb, smbAuthorityServiceID, err := detectSmbRuntimeMetadata(config)
	if err != nil {
		return smTemplateData{}, err
	}
	hasMcpRpcs, err := detectOwnPackageMcpRpcs(config)
	if err != nil {
		return smTemplateData{}, err
	}
	typedHealthType := ""
	typedHealthModPath := ""
	if config.FlatProtoDir != "" {
		if dp, err := parser.NewDescriptorParser(config.FlatProtoDir); err == nil {
			for relPath, fd := range dp.GetAllFiles() {
				if fd.Package != "" {
					pkgToDirMap[fd.Package] = filepath.Dir(relPath)
				}
				// Detect service_health message in the service's own package.
				if config.SelfPackage != "" && fd.Package == config.SelfPackage {
					for _, msg := range fd.Messages {
						if _, ok := msg.Annotations["service_health"]; ok {
							hasTypedHealth = true
							typedHealthType = msg.Name
							parentDir := filepath.Dir(relPath)
							typedHealthModPath = fmt.Sprintf("crate::types::%s::%s::%s",
								strings.ReplaceAll(strings.ReplaceAll(parentDir, "-", "_"), string(filepath.Separator), "::"),
								fd.Package, fd.Package)
							break
						}
					}
				}
			}
		}
		if hasSmb {
			if smbAuthorityServiceID == "" {
				return smTemplateData{}, fmt.Errorf("service manager data: SMB service %q has no declared (spec.smb_authority)", config.SelfPackage)
			}
			if !hasServiceHealthState {
				return smTemplateData{}, fmt.Errorf("service manager data: SMB service %q requires ServiceHealthState access to resolve SMB authority %q", config.SelfPackage, smbAuthorityServiceID)
			}
		}
	}

	projectionGroups, err := extraction.Plan(allFiles)
	if err != nil {
		return smTemplateData{}, err
	}
	var subs []subscriptionEntry
	var timeseriesInserts []rustTimeseriesInsertEntry
	seenInsert := make(map[string]bool)
	for _, sub := range config.Subscriptions {
		// Extract the type name and package from the fully-qualified proto name.
		typeName := sub.SourceTypeFqn
		typePkg := sub.SourcePackage
		if idx := strings.LastIndex(typeName, "."); idx >= 0 {
			typePkg = typeName[:idx]
			typeName = typeName[idx+1:]
		}
		sourceProtoDir := sub.SourceProtoDir
		if sourceProtoDir == "" {
			sourceProtoDir = "services"
		}
		// Resolve the type's proto directory (may differ from state root's directory).
		typeProtoDir := sourceProtoDir
		if dir, ok := pkgToDirMap[typePkg]; ok && dir != "." {
			typeProtoDir = dir
		}
		topicSnake := strutil.ToSnakeCase(eventbus.RPCStemForTopicParts(sub.SourcePackage, sub.SourceStateRoot, sub.SourceField))
		handlerMethod := "on_" + sub.SourceField + "_" + topicSnake
		targetService := sub.TargetService
		if targetService == "" {
			targetService = eventbus.EventBusLabel
		}
		group, dispatched, err := extraction.Dispatched(projectionGroups, sub.SourceTypeFqn)
		if err != nil {
			return smTemplateData{}, err
		}
		dispatchFn := ""
		var inserts []rustTimeseriesInsertEntry
		if dispatched {
			modPath := "crate::types::" + typeProtoDir + "::" + typePkg
			dispatchFn = modPath + "::" + rustRecordDispatchFn(group)
			for _, p := range group.Projections {
				snake := strutil.ToSnakeCase(p.Message.Name)
				ins := rustTimeseriesInsertEntry{
					TypeName:   p.Message.Name,
					ModPath:    modPath,
					FieldName:  "insert_" + snake,
					SetterName: "set_" + snake + "_insert",
				}
				inserts = append(inserts, ins)
				if !seenInsert[ins.FieldName] {
					seenInsert[ins.FieldName] = true
					timeseriesInserts = append(timeseriesInserts, ins)
				}
			}
		}
		subs = append(subs, subscriptionEntry{
			FieldSnake:        sub.SourceField,
			HandlerMethodName: handlerMethod,
			SubscriberMod:     topicSnake,
			SourcePackage:     sub.SourcePackage,
			SourceProtoDir:    sourceProtoDir,
			TypeProtoDir:      typeProtoDir,
			TypePackage:       typePkg,
			TypeName:          typeName,
			TargetService:     targetService,
			RoutingOnly:       sub.RoutingOnly,
			EnvelopeTypeName:  typeName + boundaries.RoutingEnvelopeSuffix,
			DispatchFn:        dispatchFn,
			Inserts:           inserts,
		})
	}

	// Compute proto source git metadata if available.
	protoGitSha := ""
	protoSourceVersion := ""
	if config.InputDir != "" {
		if out, err := exec.Command("git", "-C", config.InputDir, "rev-parse", "HEAD").Output(); err == nil {
			protoGitSha = strings.TrimSpace(string(out))
		}
		if out, err := exec.Command("git", "-C", config.InputDir, "describe", "--tags", "--abbrev=0").Output(); err == nil {
			protoSourceVersion = strings.TrimSpace(string(out))
		}
	}

	// Compute target services from resolved S2S client services.
	// Uses the same meshTarget resolution as the aggregator generator.
	var targetServices []TargetServiceInfo
	if len(clientServices) > 0 {
		targetServices = CollectTargetServices(clientServices)
	}

	// Discover the typed readers this service's (spec.record_read) declarations
	// ask for, over the stored rows of records it was granted read access to.
	recordReaders, err := collectRecordReaders(config, config.AnnotatedProtoDir)
	if err != nil {
		return smTemplateData{}, fmt.Errorf("failed to collect record readers: %w", err)
	}

	// The client-facing record read plane, when this service asked for one. Its
	// handler is generated, not implemented, so it is resolved here alongside the
	// readers it delegates to.
	recordC2S, err := collectRecordC2SPlane(config, config.AnnotatedProtoDir)
	if err != nil {
		return smTemplateData{}, fmt.Errorf("failed to collect the client-facing record read plane: %w", err)
	}

	// Discover model-driven EventBus publishers from explicit event_publish
	// declarations.
	publishers, err := collectEventPublishers(config, config.AnnotatedProtoDir)
	if err != nil {
		return smTemplateData{}, fmt.Errorf("failed to collect event-bus publishers: %w", err)
	}
	if len(publishers) > 0 {
		zap.L().Info("Found model-driven EventBus publishers", zap.Int("count", len(publishers)))
	}
	tracePublisherFn := ""
	for _, publisher := range publishers {
		if publisher.EventTypeFQN == boundaries.TraceEventTypeFQN {
			tracePublisherFn = publisher.FnName
			break
		}
	}

	// The accounting publisher is generator-resolved, not model-declared — see
	// collectAccountingPublisher. Append it to Publishers so its publish loop
	// and stream helper are emitted by the same template block as every other
	// topic, unless the service already declared it explicitly.
	accountingPublisher, err := collectAccountingPublisher(config, config.AnnotatedProtoDir)
	if err != nil {
		return smTemplateData{}, fmt.Errorf("failed to resolve accounting publisher: %w", err)
	}
	if accountingPublisher != nil {
		declared := false
		for _, publisher := range publishers {
			if publisher.FnName == accountingPublisher.FnName {
				declared = true
				break
			}
		}
		if !declared {
			publishers = append(publishers, *accountingPublisher)
		}
	}

	// (spec.rpc).requires_device: resolve the broker seam only when some hosted
	// wrapper actually carries the gate. Resolving it unconditionally would make
	// every build that happens to declare a token minter fail on a proto set
	// whose minter has no broker rpc.
	hasApprovalBroker := false
	for _, e := range grpcSvcs {
		if e.HasRequiresDevice {
			hasApprovalBroker = true
			break
		}
	}
	var approvalBroker rustApprovalBrokerEntry
	if hasApprovalBroker {
		approvalBroker, err = buildRustApprovalBrokerEntry(config.FlatProtoDir, clientServices)
		if err != nil {
			return smTemplateData{}, err
		}
	}

	return smTemplateData{
		CrateBasePath:           config.CrateBasePath,
		CrateBasePathUnderscore: strings.ReplaceAll(config.CrateBasePath, "-", "_"),
		SmPrefix:                config.GetSmPrefix(),
		ServiceLabel:            config.ServiceLabel(),
		HasGrpc:                 len(svcs) > 0,
		HasTlsGrpc:              hasTlsGrpc,
		HasAuthGrpc:             hasAuthGrpc,
		HasS2SGrpc:              hasS2SGrpc,
		HasC2SGrpc:              hasC2SGrpc,
		HasMcpGrpc:              hasMcpGrpc,
		HasC2DGrpc:              hasC2DGrpc,
		C2DSocketPath:           c2dSocketPath,
		HasStates:               len(config.States) > 0 || config.AllStates,
		HasSysState:             hasSysState,
		HasS2SClients:           len(config.GrpcS2SClients) > 0 || config.GrpcS2SClientAll,
		S2STargetsRequired:      len(config.GrpcS2SClients) > 0 && !config.GrpcS2SClientAll,
		HasServiceHealthState:   hasServiceHealthState,
		ClientOnly: !hasTlsGrpc && !hasC2DGrpc && !hasSmb && len(subs) == 0 && hasServiceHealthState &&
			(len(config.GrpcS2SClients) > 0 || config.GrpcS2SClientAll),
		// Every TLS gRPC service obtains its server cert via auto-TLS
		// bidi stream (single canonical cert path). The HasAutoTls gate in the
		// template historically meant "service has a state.auto_tls annotation",
		// but conceptually every TLS service needs a cert. Conflate them: any
		// service with TLS gRPC enables the auto-TLS code path, defaulting to
		// ECDSA P-256 (universal compatibility) if no explicit key type is set.
		// The service manager is emitted for every build with a server service or
		// a state, and its run() has a health-only branch (taken whenever there is
		// no TLS gRPC service: zero rpcs, c2d-only, smb-only, MCP-only) that binds
		// a TLS listener through reloading_tls_incoming and tls_server_certified_key,
		// both gated on HasAutoTls. So every service manager needs the auto-TLS
		// items, and the zero-rpc case no longer needs a state with auto_tls. Without
		// this the generated s2sm does not compile (E0425/E0609).
		HasAutoTls: true,
		AutoTlsKeyType: func() string {
			if k := config.AutoTlsKeyType(); k != "" {
				return k
			}
			return "ecdsa"
		}(),
		HasSmb:                     hasSmb,
		SmbAuthorityServiceID:      smbAuthorityServiceID,
		IsGateway:                  config.IsGateway,
		HasSubscriptions:           len(config.Subscriptions) > 0,
		HealthStateProtoDir:        healthStateProtoDir,
		HealthStateModPath:         strings.ReplaceAll(healthStateProtoDir, "/", "::"),
		StateStoreServiceID:        stateStoreServiceID,
		HasTypedHealth:             hasTypedHealth,
		TypedHealthType:            typedHealthType,
		TypedHealthModPath:         typedHealthModPath,
		TacplusVersion:             buildinfo.Version,
		TacplusGitSha:              buildinfo.GitSha,
		ProtoSourceVersion:         protoSourceVersion,
		ProtoSourceGitSha:          protoGitSha,
		GrpcServices:               grpcSvcs,
		ConfigFetches:              rustConfigFetches(grpcSvcs),
		Subscriptions:              subs,
		TimeseriesInserts:          timeseriesInserts,
		Publishers:                 publishers,
		HasPublishers:              len(publishers) > 0,
		RecordReaders:              recordReaders,
		HasRecordReaders:           len(recordReaders) > 0,
		RecordC2S:                  recordC2S,
		RecordC2SService:           recordC2SServiceName(recordC2S),
		TracePublisherFn:           tracePublisherFn,
		AccountingPublisher:        accountingPublisher,
		TargetServices:             targetServices,
		DiskStores:                 diskStores,
		HasDiskStores:              len(diskStores) > 0,
		HasApprovalBroker:          hasApprovalBroker,
		ApprovalBroker:             approvalBroker,
		KeepaliveTimeSecs:          keepaliveconst.TimeSecs,
		KeepaliveTimeoutSecs:       keepaliveconst.TimeoutSecs,
		KeepalivePermitMinTimeSecs: keepaliveconst.PermitMinTimeSecs,
		// An MCP-only service has no GrpcServices entry (see HasAutoTls above)
		// but still serves on the TLS listener, so it counts as non-c2d here.
		HostsC2DStandalone: service.HostsC2DStandalone(service.C2DHostShape{
			HostsC2D:       hasC2DGrpc,
			HostsNonC2D:    hasTlsGrpc || hasMcpRpcs,
			States:         len(config.States) > 0 || config.AllStates,
			S2SClients:     len(config.GrpcS2SClients) > 0 || config.GrpcS2SClientAll,
			Subscriptions:  len(config.Subscriptions) > 0,
			RecordReaders:  len(recordReaders) > 0,
			DiskStores:     len(diskStores) > 0,
			ApprovalBroker: hasApprovalBroker,
		}),
	}, nil
}

// rustConfigFetches collapses the config-handler blocks onto one entry per
// distinct ConfigState, preserving first-appearance order.
func rustConfigFetches(grpcSvcs []grpcServiceEntry) []rustConfigFetchEntry {
	var fetches []rustConfigFetchEntry
	index := map[string]int{}
	for _, svc := range grpcSvcs {
		if !svc.HasConfigState {
			continue
		}
		// The client-facing record read plane's handler is generated, so there is
		// no author-supplied type to deliver config to. Its behaviour comes from
		// the record's annotations, not from a runtime Config, and emitting a
		// config callback for it would put a method on the Handler trait that only
		// generated code could implement.
		if svc.IsGeneratedRecordC2S {
			continue
		}
		key := svc.ConfigStateGroupAccessor + "|" + svc.ConfigStateAccessor + "|" +
			svc.ConfigHelpersModPath + "|" + svc.ConfigModPath
		handler := rustConfigHandlerEntry{HandleConfigName: svc.HandleConfigName, TraitName: svc.TraitName}
		if i, ok := index[key]; ok {
			fetches[i].Handlers = append(fetches[i].Handlers, handler)
			continue
		}
		index[key] = len(fetches)
		fetches = append(fetches, rustConfigFetchEntry{
			ConfigStateGroupAccessor: svc.ConfigStateGroupAccessor,
			ConfigStateAccessor:      svc.ConfigStateAccessor,
			ConfigHelpersModPath:     svc.ConfigHelpersModPath,
			ConfigModPath:            svc.ConfigModPath,
			ConfigAliasType:          svc.TraitName + "Config",
			Handlers:                 []rustConfigHandlerEntry{handler},
		})
	}
	return fetches
}

func collectRustDiskStoreRuntimeEntries(config *tactype.Config, grpcSvcs []grpcServiceEntry) ([]rustDiskStoreRuntimeEntry, error) {
	if config.FlatProtoDir == "" {
		return nil, nil
	}
	gettersByService := make(map[string]string, len(grpcSvcs))
	for _, svc := range grpcSvcs {
		serviceName := strings.TrimSuffix(svc.ServerType, "Server")
		gettersByService[serviceName] = svc.GetterName
	}
	dp, err := parser.NewDescriptorParser(config.FlatProtoDir)
	if err != nil {
		return nil, fmt.Errorf("service manager data: collect diskstore runtimes: %w", err)
	}
	var entries []rustDiskStoreRuntimeEntry
	for relPath, fd := range dp.GetAllFiles() {
		if stateproto.IsInfraProtoFile(relPath, fd) {
			continue
		}
		for _, msg := range fd.Messages {
			if _, ok := msg.Annotations["service_store"]; !ok {
				continue
			}
			hasKey := false
			for _, field := range msg.Fields {
				if ann, ok := field.Annotations["store_key"]; ok && ann.BoolValue {
					hasKey = true
					break
				}
			}
			if !hasKey {
				continue
			}
			getter, ok := gettersByService[msg.Name+"StoreService"]
			if !ok {
				continue
			}
			storeName := prostSnakeName(msg.Name)
			entries = append(entries, rustDiskStoreRuntimeEntry{
				StoreName:     storeName,
				LogField:      storeName + "_log",
				HandlerGetter: getter,
			})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].StoreName < entries[j].StoreName })
	return entries, nil
}

// detectSmbRuntimeMetadata reports whether the service's own proto package
// declares a spec.smb Service Mesh Blackbox service and returns the service_id
// of the declared spec.smb_authority target.
func detectSmbRuntimeMetadata(config *tactype.Config) (bool, string, error) {
	if config.FlatProtoDir == "" || config.SelfPackage == "" {
		return false, "", nil
	}
	dp, err := parser.NewDescriptorParser(config.FlatProtoDir)
	if err != nil {
		return false, "", fmt.Errorf("service manager data: parse proto descriptors in %q: %w", config.FlatProtoDir, err)
	}
	authorityID, err := parser.SmbAuthorityServiceID(dp)
	if err != nil {
		return false, "", fmt.Errorf("service manager data: resolve SMB authority: %w", err)
	}
	hasSmb := false
	for _, fd := range dp.GetAllFiles() {
		if fd.Package != config.SelfPackage {
			continue
		}
		for si := range fd.Services {
			if fd.Services[si].IsSMB {
				hasSmb = true
				break
			}
		}
	}
	return hasSmb, authorityID, nil
}

// detectOwnPackageMcpRpcs reports whether the service's own proto package
// declares a service with at least one RPC_PRINCIPAL_MCP rpc — the same
// condition cargo.go's HasMcpRpcs is built from, read here off the descriptors
// because such a service is absent from the resolved gRPC service list when it
// declares no ordinary s2s/c2s surface of its own.
func detectOwnPackageMcpRpcs(config *tactype.Config) (bool, error) {
	if config.FlatProtoDir == "" || config.SelfPackage == "" {
		return false, nil
	}
	dp, err := parser.NewDescriptorParser(config.FlatProtoDir)
	if err != nil {
		return false, fmt.Errorf("service manager data: parse proto descriptors in %q: %w", config.FlatProtoDir, err)
	}
	for _, fd := range dp.GetAllFiles() {
		if fd.Package != config.SelfPackage {
			continue
		}
		for si := range fd.Services {
			if fd.Services[si].HasMcpRpcs {
				return true, nil
			}
		}
	}
	return false, nil
}

// generateHandlerInterface generates handler.rs in the sm/ directory.
func generateHandlerInterface(config *tactype.Config, svcs []service.ServiceInfo, clientServices []ServiceInfo, allFiles []model.ProtoFile) error {
	tmplContent, err := loadTemplate("handler.rs.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load handler template: %w", err)
	}

	tmpl, err := template.New("handler").Funcs(header.Funcs()).Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse handler template: %w", err)
	}

	data, err := buildSMTemplateData(config, svcs, clientServices, allFiles)
	if err != nil {
		return fmt.Errorf("failed to build handler template data: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute handler template: %w", err)
	}

	outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix())
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create handler output dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "handler.rs")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write handler.rs: %w", err)
	}

	zap.L().Info("Generated Rust handler trait", zap.String("path", outputPath))
	return nil
}

// generateServiceManagerSM generates service_manager.rs in the sm/ directory.
func generateServiceManagerSM(config *tactype.Config, svcs []service.ServiceInfo, clientServices []ServiceInfo, allFiles []model.ProtoFile) error {
	tmplContent, err := loadTemplate("service_manager.rs.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load service_manager template: %w", err)
	}

	tmpl, err := template.New("servicemanager").Funcs(header.Funcs()).Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse service_manager template: %w", err)
	}

	data, err := buildSMTemplateData(config, svcs, clientServices, allFiles)
	if err != nil {
		return fmt.Errorf("failed to build service manager template data: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute service_manager template: %w", err)
	}

	outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix())
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create service_manager dir: %w", err)
	}
	// The SM declares `pub mod record_readers;` when this service reads any
	// record's stored rows; write that module beside it, or the declaration names
	// a file that does not exist.
	if err := generateRecordReaders(config, data.RecordReaders); err != nil {
		return err
	}
	// Same contract for the client-facing plane's generated handler: the SM
	// declares `pub mod record_c2s;` only when it exists, so write it here.
	if err := generateRecordC2SPlane(config, data.RecordC2S); err != nil {
		return err
	}

	outputPath := filepath.Join(outputDir, "mod.rs")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write service_manager.rs: %w", err)
	}

	zap.L().Info("Generated Rust service_manager", zap.String("path", outputPath))
	return nil
}

// generateC2SServiceManager generates the c2sm/ entry point — a lightweight
// client-only ServiceManager with no health, state, or server lifecycle.
func generateC2SServiceManager(config *tactype.Config, clientServices []ServiceInfo) error {
	tmplContent, err := loadTemplate("c2sm.rs.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load c2sm.rs template: %w", err)
	}

	tmpl, err := template.New("c2sm").Funcs(header.Funcs()).Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse c2sm.rs template: %w", err)
	}

	data := struct {
		TargetServices []TargetServiceInfo
	}{
		TargetServices: CollectTargetServices(clientServices),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute c2sm.rs template: %w", err)
	}

	outputDir := filepath.Join(config.OutputDir, "c2sm")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create c2sm dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "mod.rs")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write c2sm mod.rs: %w", err)
	}

	zap.L().Info("Generated c2sm entry point", zap.String("path", outputPath))
	return nil
}

// generateC2DServiceManager generates the c2dm/ entry point.
func generateC2DServiceManager(config *tactype.Config, clientServices []ServiceInfo) error {
	tmplContent, err := loadTemplate("c2dm.rs.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load c2dm.rs template: %w", err)
	}

	tmpl, err := template.New("c2dm").Funcs(header.Funcs()).Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse c2dm.rs template: %w", err)
	}

	data := struct {
		TargetServices []TargetServiceInfo
	}{
		TargetServices: CollectTargetServices(clientServices),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute c2dm.rs template: %w", err)
	}

	outputDir := filepath.Join(config.OutputDir, "c2dm")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create c2dm dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "mod.rs")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write c2dm mod.rs: %w", err)
	}

	zap.L().Info("Generated c2dm entry point", zap.String("path", outputPath))
	return nil
}

// generateW2SServiceManager generates the w2sm/ entry point — a lightweight
// client-only ServiceManager for HTTP/SSE/WebSocket W2S clients.
func generateW2SServiceManager(config *tactype.Config) error {
	tmplContent, err := loadTemplate("w2sm.rs.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load w2sm.rs template: %w", err)
	}

	tmpl, err := template.New("w2sm").Funcs(header.Funcs()).Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse w2sm.rs template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, nil); err != nil {
		return fmt.Errorf("failed to execute w2sm.rs template: %w", err)
	}

	outputDir := filepath.Join(config.OutputDir, "w2sm")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create w2sm dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "mod.rs")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write w2sm mod.rs: %w", err)
	}

	zap.L().Info("Generated w2sm entry point", zap.String("path", outputPath))
	return nil
}

// authServicesForService returns the S2S auth_services list from the ServiceInfo.
func authServicesForService(svc service.ServiceInfo) []string {
	return svc.AuthServices
}

// filterExplicitGrpcServices returns only services whose ServiceName matches
// an entry in config.GrpcServers.
func filterExplicitGrpcServices(config *tactype.Config, servicesInfo []service.ServiceInfo) []service.ServiceInfo {
	nameSet := make(map[string]bool)
	for _, fe := range config.GrpcServers {
		nameSet[fe.ServiceName] = true
	}
	var result []service.ServiceInfo
	for _, svc := range servicesInfo {
		if nameSet[svc.ServiceName] {
			result = append(result, svc)
		}
	}
	return result
}

func generatedTypesRelDir(protoFile, packageName string) string {
	parentDir := filepath.Dir(protoFile)
	if parentDir == "." {
		parentDir = ""
	}
	if parentDir == "" {
		return packageName
	}
	if filepath.Base(parentDir) == packageName {
		return parentDir
	}
	return filepath.Join(parentDir, packageName)
}
