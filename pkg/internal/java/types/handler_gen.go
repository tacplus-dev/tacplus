// Copyright (c) 2026 Seabeam LLC
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
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	stateproto "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// javaGrpcServiceEntry holds per-service data for handler and service_manager templates.
type javaGrpcServiceEntry struct {
	GetterName               string // "getIdentityGrpcImpl"
	HandleConfigName         string // "handleIdentityConfig"
	ServiceName              string // "IdentityService"
	PackageName              string // "identity"
	ServerType               string // "IdentityServiceGrpc.IdentityServiceImplBase"
	HandlerType              string // "IdentityServiceHandler"
	WrapperPackage           string // "com.example.s2sm.internal.grpc.servers.services.identity"
	ConfigType               string // "Identity.Config"
	ConfigImport             string // "com.example.types.services.identity.Identity"
	ConfigPackageName        string // "identity" — physical service's package for config (may differ from PackageName for virtual services)
	ServerClassName          string // "IdentityService" (wrapper class name)
	WrapperVarName           string // "wrapperIdentity"
	ConfigStateGroupAccessor string // "services"
	ConfigStateAccessor      string // "identityConfigState"
	ConfigStateStub          string // "IdentityConfigStateServiceGrpc"
	HasConfigState           bool   // true when {ServiceName}ConfigState exists in config.States
	AuthDisabled             bool   // C2S: no token required
	AuthAnyUser              bool   // C2S: any authenticated user can call
	HasMethodAuth            bool   // true if wrapper has registerMethodAuth method
	// HasAccounting is true when this wrapper's generated class defines
	// setEmitter (i.e. it ended up with at least one accounted RPC, post Q2's
	// self-publish exclusion — see accounting_exclusion.go). The
	// ServiceManager wiring loop must guard setEmitter(...) calls on this,
	// since not every wrapper has that method.
	HasAccounting bool
	// HasRequiresDevice is true when this service's wrapper carries the
	// (spec.rpc).requires_device gate, so the device-approval check must be
	// wired into it.
	HasRequiresDevice bool
}

// javaApprovalBrokerEntry names the approval-broker RPC the generated
// (spec.rpc).requires_device check calls, and the generated S2S client seam it
// goes out over. Everything here is derived from the authoritative
// (spec.token_minter) declaration — no service, method or package is hardcoded.
type javaApprovalBrokerEntry struct {
	Package        string // "kernel"
	ServiceName    string // "Mint"
	MethodName     string // "BrokerPendingApproval"
	CallName       string // the client method: "brokerPendingApproval"
	GroupAccessor  string // client group accessor on GrpcClients, e.g. "services"
	ClientAccessor string // typed client accessor, e.g. "mint"
	JavaTypeFQN    string // outer class FQN, e.g. "com.example.types.services.kernel.Kernel"
}

// javaConfigFetchEntry groups every config-handler block that reads the same
// ConfigState into a single startup fetch. The initial getConfig RPC is
// identical for each block, so issuing one per block bursts N identical unary
// calls at startup and trips the kernel's per-(peer, method) polling rate limit.
type javaConfigFetchEntry struct {
	ConfigType               string   // "Eventbus.Config"
	ConfigPackageName        string   // "eventbus"
	ConfigStateGroupAccessor string   // "services"
	ConfigStateAccessor      string   // "eventbusConfigState"
	ConfigStateStub          string   // "EventbusConfigStateServiceGrpc"
	HandleConfigNames        []string // every handler fed by this one fetch
}

// javaSubscriptionEntry holds per-subscription data for handler and service_manager templates.
type javaSubscriptionEntry struct {
	FieldPascal       string // e.g. "EventStream"
	FieldCamel        string // e.g. "eventStream"
	HandlerMethodName string // e.g. "onEventStream" or "onEventStreamAegis" (disambiguated)
	SourcePackage     string // e.g. "aegis"
	SourceField       string // e.g. "event_stream"
	TypeName          string // e.g. "TopologyEvent"
	TypeImport        string // e.g. "com.example.types.vservices.aegis.Aegis"
	TypeOuterClass    string // e.g. "Aegis"
	ClassName         string // e.g. "AegisEventStreamSubscriber"
	TargetService     string // e.g. "eventbus" — service_id to resolve via ServiceHealthState
	// PerInstanceConsumerGroup is true when the topic delivers a (spec.record).
	// A record's rows land in a NODE-LOCAL store — one in-memory map per type
	// with a disk leg beneath it, checkpointed per pod — so a node's copy is only
	// complete if that node receives every record. Under the shared service-label
	// group the broker hands each record to exactly one instance, which splits a
	// node-local store. A per-instance group makes the broker fan the topic out to
	// every instance instead.
	//
	// It was RESIDENCY_WARM that selected this until 2026-09-20. The preset enum
	// is gone and every record type now has the disk leg only WARM types had, so
	// the condition is simply "is it a record".
	PerInstanceConsumerGroup bool
	// StoreEpochFieldPascal is the PascalCase stem of the source record's
	// (spec.store_epoch) field ("" when it declares none), e.g. "StoreEpochMs".
	// A record has no write RPC, so this dispatch is the write path into the
	// store and the place the store's clock is stamped — unconditionally, over
	// whatever arrived, because there is no caller here to refuse (see
	// spec.proto's store_epoch comment).
	StoreEpochFieldPascal string
	// StoreEpochField is the same field's proto name, for the emitted comment.
	StoreEpochField string
	// RoutingOnly / EnvelopeTypeName mirror tactype.SubscriptionEntry: the
	// Handler method takes the narrow envelope when this service is named in the
	// source type's s2s_subscribe.routing_only_services. The store-epoch stamp
	// above is skipped with it — an envelope has no epoch field to stamp, and a
	// routing-only consumer is by construction not the record's store.
	RoutingOnly      bool
	EnvelopeTypeName string // e.g. "TopologyEventRoutingEnvelope"
}

// javaTargetServiceEntry describes a unique target service for ServiceConnection creation.
type javaTargetServiceEntry struct {
	ServiceID string // e.g. "opa", "identity"
	ParamName string // camelCase param name for the ChannelProvider (e.g. "opaChannel")
	FieldName string // field name for the ServiceConnection (e.g. "opaConn")
}

type javaDiskStoreRuntimeEntry struct {
	StoreName     string
	MethodName    string
	LogField      string
	HandlerGetter string
}

// javaSmTemplateData holds the data passed to Handler.java.tmpl and ServiceManager.java.tmpl.
type javaSmTemplateData struct {
	PackageBase           string
	ChassisBase           string
	TypesBase             string
	SmPrefix              string
	ServiceLabel          string
	HasGrpc               bool
	HasStates             bool
	HasGrpcS2SClients     bool
	HasServiceHealthState bool
	HasSysState           bool
	HasAutoTls            bool
	AutoTlsKeyType        string
	HasSmb                bool // true when this service is a spec.smb Service Mesh Blackbox service (SPIFFE plan D7)
	SmbAuthorityServiceID string
	HasSubscriptions      bool
	// HasRecordSubscriptions is true when any subscription sets
	// PerInstanceConsumerGroup; it gates the one helper those subscriptions need,
	// so a service with none generates byte-identical to before.
	HasRecordSubscriptions bool
	HasPublishers          bool
	HealthStateProtoDir    string // e.g. "services/identity"
	HealthStateJavaPkg     string // e.g. "services.identity" (Java package path)
	HealthStateOuterClass  string
	StateStoreServiceID    string
	TacplusVersion         string
	TacplusGitSha          string
	ProtoSourceVersion     string
	ProtoSourceGitSha      string
	HasTracePublisher      bool
	TracePublisherTopicRef string
	TracePublisherTarget   string
	TracePublishFullMethod string
	HasTypedHealth         bool   // true when the service's package declares a service_health message
	TypedHealthType        string // e.g. "ColumnarStoreHealth"
	TypedHealthImport      string // e.g. "com.example.types.services.columnarstore.Columnarstore"
	TypedHealthJavaType    string // e.g. "Columnarstore.ColumnarStoreHealth"
	GrpcServices           []javaGrpcServiceEntry
	ConfigFetches          []javaConfigFetchEntry
	Subscriptions          []javaSubscriptionEntry
	Publishers             []javaPublisherEntry
	PublisherImports       []string
	// HasAccountingPublisher/AccountingPublisher wire the durable accounting
	// emitter (chassis/am EventBusSink) to every wrapper. False when the
	// proto set declares no accounting record (client repos), in which case
	// no emitter is wired at all — see accounting_exclusion.go.
	HasAccountingPublisher bool
	AccountingPublisher    javaPublisherEntry
	TargetServices         []javaTargetServiceEntry // unique target services for S2S clients
	S2STargetsRequired     bool                     // s2s targets listed explicitly (grpc_s2s_clients) gate readiness; s2s_client_all targets do not
	RecordReaders          []javaRecordReaderEntry  // model-driven (spec.record_read) typed readers over stored record rows
	HasRecordReaders       bool
	DiskStores             []javaDiskStoreRuntimeEntry
	HasDiskStores          bool
	// HasApprovalBroker is true when some hosted service carries the
	// (spec.rpc).requires_device gate, so the ServiceManager builds the check
	// and wires it into those wrappers.
	HasApprovalBroker bool
	ApprovalBroker    javaApprovalBrokerEntry
}

func javaSmTargetServices(config *tactype.Config, clientSvcs []ServiceInfo) []javaTargetServiceEntry {
	targets := javaClientTargets(config, clientSvcs)
	result := make([]javaTargetServiceEntry, 0, len(targets))
	for _, target := range targets {
		paramName := strings.TrimSuffix(target.ParamName, "Channel")
		result = append(result, javaTargetServiceEntry{
			ServiceID: target.ServiceID,
			ParamName: target.ParamName,
			FieldName: paramName + "Conn",
		})
	}
	return result
}

// buildJavaSmTemplateData constructs the template data.
func buildJavaSmTemplateData(config *tactype.Config, svcs []ServiceInfo, clientSvcs []ServiceInfo, protoDir string, hasAccountingByService map[string]bool) (javaSmTemplateData, error) {
	packageBase := config.JavaPackageBase
	configStates, err := stateproto.ConfigStateAccessors(config, config.FlatProtoDir)
	if err != nil {
		return javaSmTemplateData{}, err
	}

	var grpcSvcs []javaGrpcServiceEntry
	for _, svc := range svcs {
		prefix := strings.TrimSuffix(svc.ServiceName, "Service")
		wrapperTypeDir := svc.ParentDir
		if idx := strings.Index(wrapperTypeDir, string(filepath.Separator)); idx > 0 {
			wrapperTypeDir = wrapperTypeDir[:idx]
		}
		wrapperPkg := packageBase + "." + config.GetSmPrefix() + ".internal.grpc.servers." + wrapperTypeDir + "." + svc.PackageName

		// Config types live in the physical service's package, not the gRPC service's.
		// For virtual services (e.g., OpaEval in package "opa_eval"), config is in
		// the physical service's package (e.g., "opa").
		configPkg := svc.PackageName
		configParentDir := svc.ParentDir
		if config.SelfPackage != "" && svc.PackageName != config.SelfPackage {
			configPkg = config.SelfPackage
			configParentDir = "services"
		}
		hasConfigState := false
		configStateName := strutil.ToPascalCase(configPkg) + "ConfigState"
		configStateGroupAccessor := strutil.ToLowerCamelCase(javaStateTopGroup(configParentDir))
		configStateAccessor := strutil.ToLowerCamelCase(configStateName)
		configProtoFile := svc.ProtoFile
		if stateInfo, ok := configStates[configPkg]; ok {
			hasConfigState = true
			configStateName = stateInfo.StateName
			configStateAccessor = strutil.ToLowerCamelCase(stateInfo.StateName)
			configStateGroupAccessor = strutil.ToLowerCamelCase(javaStateTopGroup(filepath.Dir(stateInfo.ProtoFile)))
			configProtoFile = stateInfo.ProtoFile
		}
		configOuterClass := javaOuterClassForProto(protoDir, svc.TypesOutputDir, configProtoFile, configPkg)
		configTypeDir := strings.ReplaceAll(configParentDir, string(filepath.Separator), ".")
		configImport := packageBase + ".types." + configTypeDir + "." + configPkg + "." + configOuterClass

		grpcSvcs = append(grpcSvcs, javaGrpcServiceEntry{
			GetterName:               "get" + prefix + "GrpcImpl",
			HandleConfigName:         "handle" + prefix + "Config",
			ServiceName:              svc.ServiceName,
			PackageName:              svc.PackageName,
			ServerType:               svc.ServiceName + "Grpc." + svc.ServiceName + "ImplBase",
			HandlerType:              svc.ServiceName + "Handler",
			WrapperPackage:           wrapperPkg,
			ConfigType:               configOuterClass + ".Config",
			ConfigImport:             configImport,
			ConfigPackageName:        configPkg,
			ServerClassName:          svc.ServiceName,
			WrapperVarName:           "wrapper" + prefix,
			ConfigStateGroupAccessor: configStateGroupAccessor,
			ConfigStateAccessor:      configStateAccessor,
			ConfigStateStub:          configStateName + "ServiceGrpc",
			HasConfigState:           hasConfigState,
			AuthDisabled:             svc.AuthDisabled,
			AuthAnyUser:              svc.AuthAnyUser,
			HasMethodAuth:            svc.HasMethodAuth,
			HasAccounting:            hasAccountingByService[svc.ServiceName],
			HasRequiresDevice:        svc.HasRequiresDevice,
		})
	}
	diskStores, err := collectJavaDiskStoreRuntimeEntries(config, grpcSvcs)
	if err != nil {
		return javaSmTemplateData{}, err
	}

	hasServiceHealthState := false
	healthStateProtoDir := ""
	healthStatePackage := "state-store"
	healthStateOuterClass := ""
	for _, b := range config.States {
		if b.IsHealthState && b.AccessMode != tactype.AccessRO {
			hasServiceHealthState = true
			healthStateProtoDir = strings.TrimSuffix(b.ProtoFile, ".proto")
			stateInfo, err := stateproto.DiscoverStateRoot(config.FlatProtoDir, b.MessageName)
			if err != nil {
				return javaSmTemplateData{}, err
			}
			healthStateProtoDir = generatedTypesRelDir(stateInfo.ProtoFile, stateInfo.Package)
			healthStatePackage = stateInfo.Package
			healthStateOuterClass = javaOuterClassForProto(protoDir, "", stateInfo.ProtoFile, stateInfo.Package)
		}
	}

	hasSysState := false
	for _, b := range config.States {
		if strings.HasPrefix(b.ProtoFile, "sys/") {
			hasSysState = true
		}
	}
	if config.AllStates {
		hasSysState = true
	}

	hasSmb := false
	smbAuthorityServiceID := ""
	hasTypedHealth := false
	typedHealthType := ""
	typedHealthImport := ""
	typedHealthJavaType := ""
	if config.FlatProtoDir != "" && config.SelfPackage != "" {
		// Detect spec.smb and typed service health in the own package. These are
		// declared model surfaces; a descriptor parse failure must not silently
		// drop either runtime seam.
		dp, err := parser.NewDescriptorParser(config.FlatProtoDir)
		if err != nil {
			return javaSmTemplateData{}, fmt.Errorf("service manager data: parse proto descriptors in %q: %w", config.FlatProtoDir, err)
		}
		if id, err := parser.SmbAuthorityServiceID(dp); err != nil {
			return javaSmTemplateData{}, fmt.Errorf("service manager data: resolve SMB authority: %w", err)
		} else {
			smbAuthorityServiceID = id
		}
		for relPath, fd := range dp.GetAllFiles() {
			if fd.Package != config.SelfPackage {
				continue
			}
			// Detect a spec.smb Service Mesh Blackbox service in the own package
			// (SPIFFE plan D7) — drives the SMB member-cert stream seam.
			if !hasSmb {
				for si := range fd.Services {
					if fd.Services[si].IsSMB {
						hasSmb = true
						break
					}
				}
			}
			// Detect service_health message in the service's own package.
			if !hasTypedHealth {
				declaresServiceState := false
				for _, msg := range fd.Messages {
					if _, ok := msg.Annotations["service_state"]; ok {
						declaresServiceState = true
						break
					}
				}
				for _, msg := range fd.Messages {
					if _, ok := msg.Annotations["service_health"]; ok {
						hasTypedHealth = true
						typedHealthType = msg.Name
						typedHealthOuterClass := javaOuterClassForProto(protoDir, "", relPath, fd.Package)
						typedHealthImport = javaTypesImportForProto(packageBase, relPath, fd.Package, typedHealthOuterClass, declaresServiceState)
						typedHealthJavaType = typedHealthOuterClass + "." + msg.Name
						break
					}
				}
			}
			if hasSmb {
				if smbAuthorityServiceID == "" {
					return javaSmTemplateData{}, fmt.Errorf("service manager data: SMB service %q has no declared (spec.smb_authority)", config.SelfPackage)
				}
				if !hasServiceHealthState {
					return javaSmTemplateData{}, fmt.Errorf("service manager data: SMB service %q requires ServiceHealthState access to resolve SMB authority %q", config.SelfPackage, smbAuthorityServiceID)
				}
			}
		}
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

	var subs []javaSubscriptionEntry
	for _, sub := range config.Subscriptions {
		event := resolveJavaSubscriptionEvent(config, protoDir, sub)
		fieldPascal := strutil.ToPascalCase(sub.SourceField)
		fieldCamel := strings.ToLower(fieldPascal[:1]) + fieldPascal[1:]
		topicPascal := eventbus.RPCStemForTopicParts(sub.SourcePackage, sub.SourceStateRoot, sub.SourceField)
		className := topicPascal + "Subscriber"

		handlerMethod := "on" + fieldPascal + topicPascal
		targetService := sub.TargetService
		if targetService == "" {
			targetService = eventbus.EventBusLabel
		}

		subs = append(subs, javaSubscriptionEntry{
			FieldPascal:       fieldPascal,
			FieldCamel:        fieldCamel,
			HandlerMethodName: handlerMethod,
			SourcePackage:     sub.SourcePackage,
			SourceField:       sub.SourceField,
			TypeName:          event.TypeName,
			TypeImport:        event.TypeImport,
			TypeOuterClass:    event.TypeOuterClass,
			ClassName:         className,
			TargetService:     targetService,

			PerInstanceConsumerGroup: sub.SourceIsRecord,

			StoreEpochFieldPascal: javaPascalField(sub.SourceStoreEpochField),
			StoreEpochField:       sub.SourceStoreEpochField,

			RoutingOnly:      sub.RoutingOnly,
			EnvelopeTypeName: event.TypeName + boundaries.RoutingEnvelopeSuffix,
		})
	}

	hasRecordSubscriptions := false
	for _, sub := range subs {
		if sub.PerInstanceConsumerGroup {
			hasRecordSubscriptions = true
			break
		}
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

	targetServices := javaSmTargetServices(config, clientSvcs)
	recordReaders, err := collectJavaRecordReaders(config, config.AnnotatedProtoDir)
	if err != nil {
		return javaSmTemplateData{}, err
	}
	tracePublisher, err := detectTracePublisherTemplateData(config)
	if err != nil {
		return javaSmTemplateData{}, err
	}
	hasTracePublisher := tracePublisher != nil && hasServiceHealthState
	tracePublisherTopicRef := ""
	tracePublisherTarget := ""
	tracePublishFullMethod := ""
	if tracePublisher != nil {
		tracePublisherTopicRef = tracePublisher.topicRef
		tracePublisherTarget = tracePublisher.target
		tracePublishFullMethod = tracePublisher.fullMethod
	}
	publishers, publisherImports, err := collectJavaEventPublishers(config, protoDir)
	if err != nil {
		return javaSmTemplateData{}, err
	}
	hasPublishers := len(publishers) > 0 && hasServiceHealthState

	accountingPublisher, err := collectJavaAccountingPublisher(config, protoDir)
	if err != nil {
		return javaSmTemplateData{}, fmt.Errorf("failed to resolve accounting publisher: %w", err)
	}
	hasAccountingPublisher := accountingPublisher != nil && hasServiceHealthState && len(svcs) > 0
	var accountingPublisherEntry javaPublisherEntry
	if accountingPublisher != nil {
		accountingPublisherEntry = *accountingPublisher
	}

	// (spec.rpc).requires_device: resolve the broker seam only when some hosted
	// wrapper actually carries the gate. Resolving it unconditionally would make
	// every build that happens to declare a token minter fail on a proto set
	// whose minter has no broker RPC.
	hasApprovalBroker := false
	for _, e := range grpcSvcs {
		if e.HasRequiresDevice {
			hasApprovalBroker = true
			break
		}
	}
	var approvalBroker javaApprovalBrokerEntry
	if hasApprovalBroker {
		broker, err := parser.DiscoverApprovalBroker(config.FlatProtoDir)
		if err != nil {
			return javaSmTemplateData{}, err
		}
		if broker == nil {
			return javaSmTemplateData{}, fmt.Errorf("service manager data: a hosted rpc declares (spec.rpc).requires_device, but no authoritative (spec.token_minter) service in this proto set hosts an approval broker; the generated device-approval check has nothing to call")
		}
		// The minter cannot be the guarded service. The check goes out over the
		// generated S2S client to the broker, and no package gets a generated
		// client to ITSELF — so wiring this would emit a call to an accessor
		// that does not exist. Refused here, with the reason, rather than
		// generated into a build that fails in javac with "cannot find symbol".
		if broker.Package == config.SelfPackage {
			return javaSmTemplateData{}, fmt.Errorf("%s hosts the approval broker (%s.%s/%s) AND declares a (spec.rpc).requires_device rpc; the generated check calls the broker over a generated S2S client, and a package gets no generated client to itself, so this cannot be wired — move the guarded rpc to another service",
				config.SelfPackage, broker.Package, broker.ServiceName, broker.MethodName)
		}
		brokerProtoFile := broker.Dir + ".proto"
		brokerGroup := filepath.ToSlash(filepath.Dir(broker.Dir))
		brokerOuterClass := javaOuterClassForProto(protoDir, "", brokerProtoFile, broker.Package)
		approvalBroker = javaApprovalBrokerEntry{
			Package:     broker.Package,
			ServiceName: broker.ServiceName,
			MethodName:  broker.MethodName,
			CallName:    strutil.ToLowerCamelCase(broker.MethodName),
			// Both accessors are spelled the way the generated client
			// aggregators spell them (grpcclient_gen.go): the group by its
			// proto dir, the client by its service name minus a "Service"
			// suffix.
			GroupAccessor:  strutil.ToLowerCamelCase(strings.ReplaceAll(brokerGroup, "-", "_")),
			ClientAccessor: javaGrpcClientAccessorName(broker.ServiceName),
			// Spelled the way this function already spells a service's config
			// types import a few dozen lines up: types/<group>/<package>/<OuterClass>.
			JavaTypeFQN: packageBase + ".types." + strings.ReplaceAll(brokerGroup, "/", ".") + "." + broker.Package + "." + brokerOuterClass,
		}
	}

	return javaSmTemplateData{
		PackageBase:            packageBase,
		ChassisBase:            packageBase + ".chassis",
		TypesBase:              packageBase + ".types",
		SmPrefix:               config.GetSmPrefix(),
		ServiceLabel:           config.ServiceLabel(),
		HasGrpc:                len(svcs) > 0,
		HasStates:              len(config.States) > 0 || config.AllStates,
		HasGrpcS2SClients:      len(config.GrpcS2SClients) > 0 || config.GrpcS2SClientAll,
		HasServiceHealthState:  hasServiceHealthState,
		HasSysState:            hasSysState,
		HasAutoTls:             config.HasAutoTls(),
		AutoTlsKeyType:         config.AutoTlsKeyType(),
		HasSmb:                 hasSmb,
		SmbAuthorityServiceID:  smbAuthorityServiceID,
		HasSubscriptions:       len(subs) > 0,
		HasRecordSubscriptions: hasRecordSubscriptions,
		HasPublishers:          hasPublishers,
		HealthStateProtoDir:    healthStateProtoDir,
		HealthStateJavaPkg:     strings.ReplaceAll(healthStateProtoDir, "/", "."),
		HealthStateOuterClass:  healthStateOuterClass,
		StateStoreServiceID:    healthStatePackage,
		TacplusVersion:         buildinfo.Version,
		TacplusGitSha:          buildinfo.GitSha,
		ProtoSourceVersion:     protoSourceVersion,
		ProtoSourceGitSha:      protoGitSha,
		HasTracePublisher:      hasTracePublisher,
		TracePublisherTopicRef: tracePublisherTopicRef,
		TracePublisherTarget:   tracePublisherTarget,
		TracePublishFullMethod: tracePublishFullMethod,
		HasTypedHealth:         hasTypedHealth,
		TypedHealthType:        typedHealthType,
		TypedHealthImport:      typedHealthImport,
		TypedHealthJavaType:    typedHealthJavaType,
		GrpcServices:           grpcSvcs,
		ConfigFetches:          javaConfigFetches(grpcSvcs),
		Subscriptions:          subs,
		Publishers:             publishers,
		PublisherImports:       publisherImports,
		HasAccountingPublisher: hasAccountingPublisher,
		AccountingPublisher:    accountingPublisherEntry,
		TargetServices:         targetServices,
		S2STargetsRequired:     len(config.GrpcS2SClients) > 0 && !config.GrpcS2SClientAll,
		RecordReaders:          recordReaders,
		HasRecordReaders:       len(recordReaders) > 0,
		DiskStores:             diskStores,
		HasDiskStores:          len(diskStores) > 0,
		HasApprovalBroker:      hasApprovalBroker,
		ApprovalBroker:         approvalBroker,
	}, nil
}

// javaConfigFetches collapses the config-handler blocks onto one entry per
// distinct ConfigState, preserving first-appearance order.
func javaConfigFetches(grpcSvcs []javaGrpcServiceEntry) []javaConfigFetchEntry {
	var fetches []javaConfigFetchEntry
	index := map[string]int{}
	for _, svc := range grpcSvcs {
		if !svc.HasConfigState {
			continue
		}
		key := svc.ConfigStateStub + "|" + svc.ConfigType + "|" + svc.ConfigPackageName +
			"|" + svc.ConfigStateGroupAccessor + "|" + svc.ConfigStateAccessor
		if i, ok := index[key]; ok {
			fetches[i].HandleConfigNames = append(fetches[i].HandleConfigNames, svc.HandleConfigName)
			continue
		}
		index[key] = len(fetches)
		fetches = append(fetches, javaConfigFetchEntry{
			ConfigType:               svc.ConfigType,
			ConfigPackageName:        svc.ConfigPackageName,
			ConfigStateGroupAccessor: svc.ConfigStateGroupAccessor,
			ConfigStateAccessor:      svc.ConfigStateAccessor,
			ConfigStateStub:          svc.ConfigStateStub,
			HandleConfigNames:        []string{svc.HandleConfigName},
		})
	}
	return fetches
}

func collectJavaDiskStoreRuntimeEntries(config *tactype.Config, grpcSvcs []javaGrpcServiceEntry) ([]javaDiskStoreRuntimeEntry, error) {
	if config.FlatProtoDir == "" {
		return nil, nil
	}
	gettersByService := make(map[string]string, len(grpcSvcs))
	for _, svc := range grpcSvcs {
		gettersByService[svc.ServiceName] = svc.GetterName
	}
	dp, err := parser.NewDescriptorParser(config.FlatProtoDir)
	if err != nil {
		return nil, fmt.Errorf("service manager data: collect diskstore runtimes: %w", err)
	}
	var entries []javaDiskStoreRuntimeEntry
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
			entries = append(entries, javaDiskStoreRuntimeEntry{
				StoreName:     strutil.ToSnakeCase(msg.Name),
				MethodName:    msg.Name,
				LogField:      strutil.ToLowerFirst(msg.Name) + "Log",
				HandlerGetter: getter,
			})
		}
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].StoreName < entries[j].StoreName })
	return entries, nil
}

type tracePublisherTemplateData struct {
	topicRef   string
	target     string
	fullMethod string
}

func detectTracePublisherTemplateData(config *tactype.Config) (*tracePublisherTemplateData, error) {
	protoDir := config.AnnotatedProtoDir
	if protoDir == "" {
		return nil, nil
	}
	p, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return nil, fmt.Errorf("create descriptor parser for trace publisher detection: %w", err)
	}
	pub, err := boundaries.FindTraceEventPublisherData(p, protoDir, eventPublishProtoFiles(config), config.SelfPackage)
	if err != nil {
		return nil, err
	}
	if pub == nil {
		return nil, nil
	}
	return &tracePublisherTemplateData{
		topicRef:   pub.TopicRef,
		target:     pub.BrokerLabel,
		fullMethod: pub.PublishFullMethod,
	}, nil
}

func eventPublishProtoFiles(config *tactype.Config) []string {
	seen := make(map[string]bool)
	var protoFiles []string
	add := func(protoFile string) {
		if protoFile == "" || seen[protoFile] {
			return
		}
		seen[protoFile] = true
		protoFiles = append(protoFiles, protoFile)
	}
	for _, state := range config.States {
		add(state.ProtoFile)
	}
	for _, grpc := range config.GrpcServers {
		add(grpc.ProtoFile)
	}
	return protoFiles
}

func javaOuterClassForProto(protoDir, typesOutputDir, protoFile, fallbackPackage string) string {
	if protoDir != "" && protoFile != "" {
		if name := extractJavaOuterClassName(filepath.Join(protoDir, protoFile)); name != "" {
			return name
		}
	}
	if typesOutputDir != "" && protoFile != "" {
		if name := getOuterClassNameForWrapper(typesOutputDir, protoFile); name != "" {
			return name
		}
	}
	return strutil.ToPascalCase(fallbackPackage)
}

func javaTypesImportForProto(packageBase, protoFile, protoPackage, outerClass string, declaresServiceState bool) string {
	return javaTypesPackageForProto(packageBase, protoFile, protoPackage, declaresServiceState) + "." + outerClass
}

func javaTypesPackageForProto(packageBase, protoFile, protoPackage string, declaresServiceState bool) string {
	parts := strings.Split(filepath.ToSlash(protoFile), "/")
	typeDir := ""
	if len(parts) > 1 {
		typeDir = parts[0]
	}

	var javaPackage string
	if serviceName, fileName := stateproto.ServiceProtoParts(protoFile); serviceName != "" {
		if fileName == serviceName {
			javaPackage = fmt.Sprintf("%s.types.%s.%s", packageBase, typeDir, protoPackage)
		} else {
			subPkg := fileName
			switch {
			case fileName == "types":
				subPkg = protoPackage
			case stateproto.IsServicesFileName(fileName):
				subPkg = "services"
			case declaresServiceState:
				subPkg = "services"
			}
			javaPackage = fmt.Sprintf("%s.types.%s.%s.%s", packageBase, typeDir, protoPackage, subPkg)
		}
	} else if typeDir != "" {
		javaPackage = fmt.Sprintf("%s.types.%s.%s", packageBase, typeDir, protoPackage)
	} else {
		javaPackage = fmt.Sprintf("%s.types.%s", packageBase, protoPackage)
	}

	return strings.ReplaceAll(javaPackage, "-", "_")
}

// generateJavaHandlerInterface generates Handler.java in the sm/ directory.
func generateJavaHandlerInterface(config *tactype.Config, svcs []ServiceInfo, clientSvcs []ServiceInfo, protoDir string, hasAccountingByService map[string]bool) error {
	tmplContent, err := loadTemplate("Handler.java.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load handler template: %w", err)
	}

	tmpl, err := template.New("handler").Funcs(header.Funcs()).Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse handler template: %w", err)
	}

	data, err := buildJavaSmTemplateData(config, svcs, clientSvcs, protoDir, hasAccountingByService)
	if err != nil {
		return fmt.Errorf("failed to build handler template data: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute handler template: %w", err)
	}

	packagePath := strings.ReplaceAll(config.JavaPackageBase, ".", string(filepath.Separator))
	outputDir := filepath.Join(config.LangDir(), "src", "main", "java", packagePath, config.GetSmPrefix())
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create handler output dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "Handler.java")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write Handler.java: %w", err)
	}

	zap.L().Info("Generated Java handler interface", zap.String("path", outputPath))
	return nil
}

// generateJavaServiceManager generates ServiceManager.java in the sm/ directory.
func generateJavaServiceManager(config *tactype.Config, svcs []ServiceInfo, clientSvcs []ServiceInfo, protoDir string, hasAccountingByService map[string]bool) error {
	tmplContent, err := loadTemplate("ServiceManager.java.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load service_manager template: %w", err)
	}

	tmpl, err := template.New("servicemanager").Funcs(header.Funcs()).Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse service_manager template: %w", err)
	}

	data, err := buildJavaSmTemplateData(config, svcs, clientSvcs, protoDir, hasAccountingByService)
	if err != nil {
		return fmt.Errorf("failed to build service manager template data: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute service_manager template: %w", err)
	}

	packagePath := strings.ReplaceAll(config.JavaPackageBase, ".", string(filepath.Separator))
	outputDir := filepath.Join(config.LangDir(), "src", "main", "java", packagePath, config.GetSmPrefix())
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create service_manager dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "ServiceManager.java")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write ServiceManager.java: %w", err)
	}

	zap.L().Info("Generated Java service_manager", zap.String("path", outputPath))
	// The SM exposes an accessor per (spec.record_read); write the readers
	// beside it, or the accessor names a class that does not exist.
	if err := generateJavaRecordReaders(config, data.RecordReaders); err != nil {
		return err
	}
	return nil
}

// generateC2SServiceManager generates a lightweight ServiceManager.java in the c2sm/ directory.
// Unlike the full s2sm ServiceManager, c2sm has no health, state, or lifecycle — just
// a gRPC channel and typed client accessors.
func generateC2SServiceManager(config *tactype.Config, clientSvcs []ServiceInfo) error {
	tmplContent, err := loadTemplate("C2SServiceManager.java.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load c2sm service_manager template: %w", err)
	}

	tmpl, err := template.New("c2sservicemanager").Funcs(header.Funcs()).Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse c2sm service_manager template: %w", err)
	}

	data := struct {
		PackageBase string
		Targets     []grpcClientTargetJava
	}{
		PackageBase: config.JavaPackageBase,
		Targets:     javaClientTargets(config, clientSvcs),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute c2sm service_manager template: %w", err)
	}

	packagePath := strings.ReplaceAll(config.JavaPackageBase, ".", string(filepath.Separator))
	outputDir := filepath.Join(config.LangDir(), "src", "main", "java", packagePath, "c2sm")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create c2sm service_manager dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "ServiceManager.java")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write c2sm ServiceManager.java: %w", err)
	}

	zap.L().Info("Generated Java c2sm service_manager", zap.String("path", outputPath))
	return nil
}

// generateC2DServiceManager generates a lightweight ServiceManager.java in the c2dm/ directory.
func generateC2DServiceManager(config *tactype.Config, clientSvcs []ServiceInfo) error {
	tmplContent, err := loadTemplate("C2DServiceManager.java.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load c2dm service_manager template: %w", err)
	}

	tmpl, err := template.New("c2dservicemanager").Funcs(header.Funcs()).Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse c2dm service_manager template: %w", err)
	}

	data := struct {
		PackageBase string
		Targets     []grpcClientTargetJava
	}{
		PackageBase: config.JavaPackageBase,
		Targets:     javaClientTargets(config, clientSvcs),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute c2dm service_manager template: %w", err)
	}

	packagePath := strings.ReplaceAll(config.JavaPackageBase, ".", string(filepath.Separator))
	outputDir := filepath.Join(config.LangDir(), "src", "main", "java", packagePath, "c2dm")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create c2dm service_manager dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "ServiceManager.java")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write c2dm ServiceManager.java: %w", err)
	}

	zap.L().Info("Generated Java c2dm service_manager", zap.String("path", outputPath))
	return nil
}

// filterExplicitGrpcServicesJava returns only services whose ServiceName matches
// an entry in config.GrpcServers.
func filterExplicitGrpcServicesJava(config *tactype.Config, servicesInfo []ServiceInfo) []ServiceInfo {
	nameSet := make(map[string]bool)
	for _, fe := range config.GrpcServers {
		nameSet[fe.ServiceName] = true
	}
	var result []ServiceInfo
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
