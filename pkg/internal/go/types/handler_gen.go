// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/buildinfo"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/boundaries"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/eventbus"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	stateproto "gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// filterExplicitGrpcServices returns only services whose ServiceName matches
// an entry in config.GrpcServers. This excludes state services that happen
// to live in the same proto file.
func filterExplicitGrpcServices(config *tactype.Config, servicesInfo []ServiceInfo) []ServiceInfo {
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

// generateHandlerInterfaces generates handler.go in the sm/ package.
// svcs is the list of gRPC services; may be empty for services with only states.
func generateHandlerInterfaces(config *tactype.Config, svcs []ServiceInfo, allFiles []model.ProtoFile) error {
	tmplContent, err := loadTemplate("handler.go.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load handler template: %w", err)
	}

	tmpl, err := template.New("handler").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse handler template: %w", err)
	}

	data, err := buildServiceManagerData(config, svcs, allFiles)
	if err != nil {
		return fmt.Errorf("failed to build handler template data: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute handler template: %w", err)
	}

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("failed to format handler.go: %w", err)
	}

	outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix())
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create handler output dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "handler.go")
	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write handler.go: %w", err)
	}

	zap.L().Info("Generated handler interface", zap.String("path", outputPath))
	return nil
}

// GrpcServiceEntry holds per-service data for handler.go.tmpl and service_manager.go.tmpl.
type GrpcServiceEntry struct {
	// Handler interface methods
	GetterName       string // "GetIdentityGrpcImpl"
	HandleConfigName string // "HandleIdentityConfig"
	// gRPC server type
	ServerType  string // "IdentityServiceServer"
	HandlerType string // "IdentityServiceHandler" — clean handler interface
	// Type import (for gRPC server interface types)
	TypeAlias      string // "identity_pb"
	TypeImportPath string // ".../types/services/identity"
	// Config type import — same as TypeAlias when the gRPC service lives in the
	// physical service's own proto package. Differs for virtual services: e.g.,
	// OpaEval lives in "opa_eval" but config is in "opa".
	ConfigTypeAlias      string // "opa_pb" (or same as TypeAlias)
	ConfigTypeImportPath string // ".../types/services/opa" (or same as TypeImportPath)
	// Frontend server wrapper import
	WrapperAlias      string // "identity_fe"
	WrapperImportPath string // ".../sm/internal/grpc_servers/services/identity"
	ConstructorName   string // "NewIdentityService"
	// Local variable names in Run()
	WrapperVarName  string // "wrapperIdentity"
	ConfigHolderVar string // "identityConfigHolder"
	TypedConfigVar  string // "identityCfg"
	// State accessor for config state
	ConfigStateGroupAccessor string // "Services"
	ConfigStateAccessor      string // "IdentityConfigState"
	HasConfigState           bool
	// AuthDisabled skips SetAuthorizeFunc on this service's server wrapper.
	AuthDisabled bool
	// AuthAnyUser wires the fixed-namespace authenticate-at-or-below path.
	AuthAnyUser bool
	// HasMethodAuth is true when the wrapper has a RegisterMethodAuth function.
	HasMethodAuth      bool
	MethodAuthFuncName string
	// IsFirstConfigUse is true for the first service using this config package.
	// When false, config fetch uses = instead of := to avoid redeclaration.
	IsFirstConfigUse bool
}

// WrapperImport is a deduplicated import entry for gRPC server wrappers.
type WrapperImport struct {
	Alias      string // "identity_fe"
	ImportPath string // ".../sm/internal/grpc_servers/services/identity"
}

func disambiguateWrapperImportAliases(svcs []GrpcServiceEntry) {
	pathsByAlias := make(map[string]map[string]bool)
	for _, svc := range svcs {
		if pathsByAlias[svc.WrapperAlias] == nil {
			pathsByAlias[svc.WrapperAlias] = make(map[string]bool)
		}
		pathsByAlias[svc.WrapperAlias][svc.WrapperImportPath] = true
	}
	for i := range svcs {
		if len(pathsByAlias[svcs[i].WrapperAlias]) <= 1 {
			continue
		}
		svcs[i].WrapperAlias = goImportAliasFromPath(svcs[i].WrapperImportPath, "fe")
	}
}

func goImportAliasFromPath(importPath string, suffix string) string {
	parts := strings.Split(filepath.ToSlash(importPath), "/")
	if len(parts) >= 2 {
		return sanitizeGoImportAlias(parts[len(parts)-2] + "_" + parts[len(parts)-1] + "_" + suffix)
	}
	return sanitizeGoImportAlias(importPath + "_" + suffix)
}

func sanitizeGoImportAlias(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	alias := b.String()
	if alias == "" {
		return "_"
	}
	if alias[0] >= '0' && alias[0] <= '9' {
		return "_" + alias
	}
	return alias
}

// SubscriptionTemplateEntry holds the data for a single subscription in the template.
type SubscriptionTemplateEntry struct {
	FieldPascal              string // e.g. "EventStream"
	HandlerMethodName        string // e.g. "OnEventStream" or "OnEventStreamAegis" (disambiguated)
	SourcePackage            string // e.g. "aegis"
	SourceField              string // e.g. "event_stream"
	TypeName                 string // e.g. "TopologyEvent"
	HandlerFuncFieldName     string // e.g. "onEventStreamFunc"
	HandlerFuncSetterName    string // e.g. "SetOnEventStreamFunc"
	SubPkgAlias              string // e.g. "event_stream_sub"
	SubPkgImport             string // e.g. "module/tacplus/subscribe/aegis/event_stream"
	TypePkgAlias             string // e.g. "aegis_pb"
	TypePkgImport            string // e.g. "module/tacplus/types/services/aegis"
	EventBusImport           string // e.g. "module/tacplus/types/sys/eventbus"
	HasGraphProjections      bool   // source event has graph_type projections
	HasTimeseriesProjections bool   // source event has timeseries_type projections
	TargetService            string // e.g. "eventbus" — service_id to resolve via ServiceHealthState
}

// DiskStoreRuntimeEntry holds per-store data for ServiceManager-owned redo logs.
type DiskStoreRuntimeEntry struct {
	StoreName     string // snake_case message name; file basename
	TypeName      string // PascalCase message name; generated helper suffix
	LogField      string // field on DiskStoreLogs, e.g. credPairLog
	HandlerGetter string // top-level Handler getter for the synthetic store service
}

// serviceManagerTemplateData holds the data passed to service_manager.go.tmpl and handler.go.tmpl.
type serviceManagerTemplateData struct {
	ServiceLabel             string
	ModuleBasePath           string
	SmPrefix                 string
	ServiceManagerImportPath string
	HasServiceHealthState    bool     // true when the service has RW access to ServiceHealthState
	HealthStateProtoDir      string   // proto dir containing ServiceHealthState (e.g. "services/identity")
	HealthStateGroupAccessor string   // root state-client group accessor containing ServiceHealth
	HasGrpc                  bool     // true when gRPC services are configured
	HasGrpcServers           bool     // true when gRPC server wrappers are configured
	HasStates                bool     // true when states are configured
	HasSysState             bool     // true when any state is from the sys/ proto dir
	HasGrpcS2SClients        bool     // true when gRPC clients are configured
	HasSubscriptions         bool     // true when event-bus subscriptions are declared
	HasPublishers            bool     // true when event-bus publishers are declared and resolvable
	HasAutoTls               bool     // true when any state has auto_tls enabled
	AutoTlsKeyType           string   // key algorithm: "ed25519" or "ecdsa"
	HasSmb                   bool     // true when this service is a spec.smb Service Mesh Blackbox service (SPIFFE plan D7)
	SmbAuthorityServiceID    string   // service_id of the declared (spec.smb_authority) service
	HasGraphProjections      bool     // true when any subscription has graph projections
	HasTimeseriesProjections bool     // true when any subscription has timeseries projections
	ProjectionPkgAlias       string   // alias for source event package (e.g. "sensor_pb")
	ProjectionPkgImport      string   // import path for source event package
	HasTypedHealth           bool     // true when the service's package declares a service_health message
	TypedHealthType          string   // e.g. "GraphStoreHealth"
	TypedHealthPkgAlias      string   // e.g. "graphstore_pb"
	TypedHealthPkgImport     string   // e.g. "module/types/services/graphstore"
	TacplusVersion           string   // e.g. "v1.2.71"
	TacplusGitSha            string   // git SHA of tacplus binary source
	ProtoSourceVersion       string   // git tag of proto source dir, e.g. "v1.2.80-main.80"
	ProtoSourceGitSha        string   // git SHA of proto source dir
	HasTracePublisher        bool     // true when actor declares trace.TraceEvent event_publish
	TracePublisherTopicRef   string   // canonical topic_ref for trace publisher
	TracePublisherTarget     string   // broker service_id for trace publisher
	TracePublishStreamName   string   // canonical EventBus publish stream name
	TracePublishGrpcPath     string   // canonical gRPC path for trace publish stream
	S2STargetServices        []string // unique service IDs for S2S client ServiceConnections
	GrpcServices             []GrpcServiceEntry
	WrapperImports           []WrapperImport // deduplicated wrapper imports
	TypeImports              []WrapperImport // deduplicated type imports (for config)
	Subscriptions            []SubscriptionTemplateEntry
	PublisherTypeImports     []WrapperImport
	Publishers               []PublisherTemplateEntry
	DiskStores               []DiskStoreRuntimeEntry
	HasDiskStores            bool
}

// buildServiceManagerData constructs the template data for service_manager.go and handler.go generation.
// svcs is the list of gRPC services; may be empty for services with only states.
// allFiles is used to compute projection flags for subscriptions (may be nil if no subscriptions).
func buildServiceManagerData(config *tactype.Config, svcs []ServiceInfo, allFiles []model.ProtoFile) (serviceManagerTemplateData, error) {
	var grpcSvcs []GrpcServiceEntry
	seenConfigDirs := make(map[string]bool)
	configStates, err := stateproto.ConfigStateAccessors(config, config.FlatProtoDir)
	if err != nil {
		return serviceManagerTemplateData{}, err
	}

	// Pre-count prefix occurrences. When two services share the same ServiceName
	// (e.g. two OpaEval services from different vservice packages), their getter
	// names must be disambiguated by prepending the import directory (package name).
	prefixCounts := make(map[string]int)
	for _, svc := range svcs {
		prefixCounts[strings.TrimSuffix(svc.ServiceName, "Service")]++
	}

	for _, svc := range svcs {
		importDir := svc.ImportDirName()
		// Use ServiceName minus "Service" suffix as PascalCase prefix for method names.
		// prefix: service-name-based PascalCase for public method names (e.g. "McpAssist", "PolicySim")
		prefix := strings.TrimSuffix(svc.ServiceName, "Service")

		// When multiple services share the same ServiceName (e.g. two OpaEval services
		// from different vservice proto packages), prepend the capitalised import
		// directory so each getter is unique (e.g. "BackendtokenOpaEval", "UserentitlementsOpaEval").
		if prefixCounts[prefix] > 1 {
			capImport := strings.ToUpper(importDir[:1]) + importDir[1:]
			prefix = capImport + prefix
		}

		// Config types live in the physical service's package, not the gRPC service's.
		// For virtual services (e.g., OpaEval in package "opa_eval"), the config is in
		// the physical service's package (e.g., "opa").
		configDir := importDir
		configParentDir := svc.ParentDir
		if config.SelfPackage != "" && importDir != config.SelfPackage {
			configDir = config.SelfPackage
			// Config types always live under services/, not the virtual service's parent dir
			configParentDir = "services"
		}
		capConfigDir := strings.ToUpper(configDir[:1]) + configDir[1:]
		configStateGroupAccessor := strings.ToUpper(configParentDir[:1]) + configParentDir[1:]
		configStateAccessor := capConfigDir + "ConfigState"
		hasConfigState := false
		if stateInfo, ok := configStates[configDir]; ok {
			configStateAccessor = stateInfo.StateName
			stateDir := filepath.ToSlash(filepath.Dir(stateInfo.ProtoFile))
			if stateDir != "" && stateDir != "." {
				group := strings.Split(stateDir, "/")[0]
				configStateGroupAccessor = strings.ToUpper(group[:1]) + group[1:]
			}
			hasConfigState = true
		}
		isFirst := !seenConfigDirs[configDir]
		seenConfigDirs[configDir] = true
		grpcSvcs = append(grpcSvcs, GrpcServiceEntry{
			GetterName:               prefix + "GrpcImpl",
			HandleConfigName:         prefix + "Config",
			ServerType:               svc.ServiceName + "Server",
			HandlerType:              svc.ServiceName + "Handler",
			TypeAlias:                importDir + "_pb",
			TypeImportPath:           config.TypesImportPath(svc.ParentDir, importDir),
			ConfigTypeAlias:          configDir + "_pb",
			ConfigTypeImportPath:     config.TypesImportPath(configParentDir, configDir),
			WrapperAlias:             importDir + "_fe",
			WrapperImportPath:        config.ModuleBasePath + "/" + config.GetSmPrefix() + "/grpc/servers/" + svc.ParentDir + "/" + importDir,
			ConstructorName:          "New" + svc.ServiceName,
			WrapperVarName:           "wrapper" + prefix,
			ConfigHolderVar:          configDir + "ConfigHolder",
			TypedConfigVar:           configDir + "Cfg",
			ConfigStateGroupAccessor: configStateGroupAccessor,
			ConfigStateAccessor:      configStateAccessor,
			HasConfigState:           hasConfigState,
			AuthDisabled:             svc.AuthDisabled, // computed from service+method level
			AuthAnyUser:              svc.AuthAnyUser,  // computed: true if ANY method has auth_any_user
			HasMethodAuth:            svc.HasMethodAuth,
			MethodAuthFuncName:       "Register" + svc.ServiceName + "MethodAuth",
			IsFirstConfigUse:         isFirst,
		})
	}
	disambiguateWrapperImportAliases(grpcSvcs)
	diskStores, err := collectDiskStoreRuntimeEntries(config, grpcSvcs)
	if err != nil {
		return serviceManagerTemplateData{}, err
	}

	hasServiceHealthState := false
	healthStateProtoDir := ""
	healthStateGroupAccessor := ""
	hasSysState := false
	for _, b := range config.States {
		if b.IsHealthState && b.AccessMode != tactype.AccessRO {
			hasServiceHealthState = true
			stateInfo, err := stateproto.DiscoverStateRoot(config.FlatProtoDir, b.MessageName)
			if err != nil {
				return serviceManagerTemplateData{}, err
			}
			healthStateProtoDir = generatedTypesRelDir(stateInfo.ProtoFile, stateInfo.Package)
			healthStateGroupAccessor = goAccessorName(filepath.Dir(stateInfo.ProtoFile))
		}
		if strings.HasPrefix(b.ProtoFile, "sys/") {
			hasSysState = true
		}
	}
	// Store-service state access includes sys.
	if config.AllStates {
		hasSysState = true
	}

	// Deduplicate imports — multiple services in the same proto package
	// share a single wrapper/types package (e.g., a service has TokenExchanger + X509CertExchanger).
	seen := make(map[string]bool)
	var wrapperImports []WrapperImport
	var typeImports []WrapperImport
	for _, svc := range grpcSvcs {
		if !seen[svc.WrapperAlias] {
			seen[svc.WrapperAlias] = true
			wrapperImports = append(wrapperImports, WrapperImport{
				Alias:      svc.WrapperAlias,
				ImportPath: svc.WrapperImportPath,
			})
		}
		if !svc.HasConfigState {
			continue
		}
		if svc.ConfigTypeAlias != svc.TypeAlias {
			// Virtual service: TypeAlias is only used in comments, not code.
			// Import only the config package (the physical service's types).
			if !seen[svc.ConfigTypeAlias] {
				seen[svc.ConfigTypeAlias] = true
				typeImports = append(typeImports, WrapperImport{
					Alias:      svc.ConfigTypeAlias,
					ImportPath: svc.ConfigTypeImportPath,
				})
			}
		} else if !seen[svc.TypeAlias] {
			seen[svc.TypeAlias] = true
			typeImports = append(typeImports, WrapperImport{
				Alias:      svc.TypeAlias,
				ImportPath: svc.TypeImportPath,
			})
		}
	}

	// Build subscription template entries.
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
	hasSmb := false
	smbAuthorityServiceID := ""
	hasTypedHealth := false
	typedHealthType := ""
	typedHealthPkgAlias := ""
	typedHealthPkgImport := ""
	if config.FlatProtoDir != "" {
		// A swallowed parse error here silently left hasSmb=false (and dropped
		// typed-health detection), making the whole SMB cert-stream seam vanish
		// without a diagnostic (OF-18). Fail loudly instead.
		dp, err := parser.NewDescriptorParser(config.FlatProtoDir)
		if err != nil {
			return serviceManagerTemplateData{}, fmt.Errorf("service manager data: parse proto descriptors in %q: %w", config.FlatProtoDir, err)
		}
		{
			if id, err := parser.SmbAuthorityServiceID(dp); err != nil {
				return serviceManagerTemplateData{}, fmt.Errorf("service manager data: resolve SMB authority: %w", err)
			} else {
				smbAuthorityServiceID = id
			}
			for relPath, fd := range dp.GetAllFiles() {
				if fd.Package != "" {
					pkgToDirMap[fd.Package] = filepath.Dir(relPath)
				}
				// Detect a spec.smb Service Mesh Blackbox service in the own package
				// (SPIFFE plan D7) — drives the SMB cert-stream seam.
				if config.SelfPackage != "" && fd.Package == config.SelfPackage {
					for si := range fd.Services {
						if fd.Services[si].IsSMB {
							hasSmb = true
							break
						}
					}
				}
				// Detect service_health message in the service's own package.
				if config.SelfPackage != "" && fd.Package == config.SelfPackage {
					for _, msg := range fd.Messages {
						if _, ok := msg.Annotations["service_health"]; ok {
							hasTypedHealth = true
							typedHealthType = msg.Name
							parentDir := filepath.Dir(relPath)
							typedHealthPkgAlias = fd.Package + "_pb"
							typedHealthPkgImport = config.TypesImportPath(parentDir, fd.Package)
							break
						}
					}
				}
			}
		}
		if hasSmb {
			if smbAuthorityServiceID == "" {
				return serviceManagerTemplateData{}, fmt.Errorf("service manager data: SMB service %q has no declared (spec.smb_authority)", config.SelfPackage)
			}
			if !hasServiceHealthState {
				return serviceManagerTemplateData{}, fmt.Errorf("service manager data: SMB service %q requires ServiceHealthState access to resolve SMB authority %q", config.SelfPackage, smbAuthorityServiceID)
			}
		}
	}

	// Ensure typed health type package is importable (may already be present from gRPC services).
	if hasTypedHealth && typedHealthPkgAlias != "" && !seen[typedHealthPkgAlias] {
		seen[typedHealthPkgAlias] = true
		typeImports = append(typeImports, WrapperImport{
			Alias:      typedHealthPkgAlias,
			ImportPath: typedHealthPkgImport,
		})
	}

	var subEntries []SubscriptionTemplateEntry
	for _, sub := range config.Subscriptions {
		parts := strings.Split(sub.SourceField, "_")
		fieldPascal := ""
		for _, part := range parts {
			if len(part) > 0 {
				fieldPascal += strings.ToUpper(part[:1]) + part[1:]
			}
		}

		// Extract type name and package from FQN (e.g. "sensor.SensorEvent" → pkg="sensor", name="SensorEvent").
		typeName := sub.SourceTypeFqn
		typePkg := sub.SourcePackage
		if idx := strings.LastIndex(typeName, "."); idx >= 0 {
			typePkg = typeName[:idx]
			typeName = typeName[idx+1:]
		}
		if typeName == "" {
			typeName = "Event" // fallback
		}

		// Resolve the type's proto directory (may differ from state root's directory).
		typeProtoDir := sub.SourceProtoDir
		if typeProtoDir == "" {
			typeProtoDir = "services"
		}
		if dir, ok := pkgToDirMap[typePkg]; ok && dir != "." {
			typeProtoDir = dir
		}

		topicPascal := eventbus.RPCStemForTopicParts(sub.SourcePackage, sub.SourceStateRoot, sub.SourceField)
		handlerMethod := "On" + fieldPascal + topicPascal
		targetService := sub.TargetService
		if targetService == "" {
			targetService = eventbus.EventBusTag
		}

		// Determine projection flags by scanning allFiles for messages that
		// extract from this subscription's source event type.
		hasGraph := false
		hasTimeseries := false
		for _, pf := range allFiles {
			for _, msg := range pf.Messages {
				if extractionSourceFQN(msg) == sub.SourceTypeFqn {
					if _, ok := msg.Annotations["graph_type"]; ok {
						hasGraph = true
					}
					if _, ok := msg.Annotations["timeseries_type"]; ok {
						hasTimeseries = true
					}
				}
			}
		}

		subEntries = append(subEntries, SubscriptionTemplateEntry{
			FieldPascal:              fieldPascal,
			HandlerMethodName:        handlerMethod,
			SourcePackage:            sub.SourcePackage,
			SourceField:              sub.SourceField,
			TypeName:                 typeName,
			HandlerFuncFieldName:     strutil.ToLowerFirst(handlerMethod) + "Func",
			HandlerFuncSetterName:    "Set" + handlerMethod + "Func",
			SubPkgAlias:              strings.ToLower(topicPascal) + "_sub",
			SubPkgImport:             config.ModuleBasePath + "/subscribe/" + sub.SourcePackage + "/" + sub.SourceStateRoot + "/" + sub.SourceField,
			TypePkgAlias:             typePkg + "_pb",
			TypePkgImport:            config.ModuleBasePath + "/types/" + typeProtoDir + "/" + typePkg,
			EventBusImport:           config.ModuleBasePath + "/types/generated/eventbus_support",
			HasGraphProjections:      hasGraph,
			HasTimeseriesProjections: hasTimeseries,
			TargetService:            targetService,
		})
	}

	// Compute top-level projection flags across all subscriptions.
	hasAnyGraph := false
	hasAnyTimeseries := false
	projPkgAlias := ""
	projPkgImport := ""
	for _, se := range subEntries {
		if se.HasGraphProjections {
			hasAnyGraph = true
		}
		if se.HasTimeseriesProjections {
			hasAnyTimeseries = true
		}
		if (se.HasGraphProjections || se.HasTimeseriesProjections) && projPkgAlias == "" {
			projPkgAlias = se.TypePkgAlias
			projPkgImport = se.TypePkgImport
		}
	}

	publishers, err := collectGoEventPublishers(config, config.AnnotatedProtoDir, pkgToDirMap)
	if err != nil {
		return serviceManagerTemplateData{}, err
	}
	hasPublishers := len(publishers) > 0 && hasServiceHealthState
	publisherTypeImports := goPublisherTypeImports(publishers, subEntries, typeImports, len(svcs) > 0 && (len(config.States) > 0 || config.AllStates))

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

	// Compute unique S2S target service IDs for ServiceConnection creation.
	// Uses the same resolution logic as grpcclient_gen: TargetService if set,
	// ProtoPackage if set, otherwise derive from the proto filename.
	var s2sTargetServices []string
	s2sSeen := make(map[string]bool)
	for _, entry := range config.GrpcS2SClients {
		var targetID string
		if entry.TargetService != "" {
			targetID = entry.TargetService
		} else if entry.ProtoPackage != "" {
			targetID = entry.ProtoPackage
		} else {
			// Derive from proto filename: "services/baseball.proto" -> "baseball"
			base := filepath.Base(entry.ProtoFile)
			targetID = strings.TrimSuffix(base, filepath.Ext(base))
		}
		if !s2sSeen[targetID] {
			s2sSeen[targetID] = true
			s2sTargetServices = append(s2sTargetServices, targetID)
		}
	}

	tracePublisher, err := detectTracePublisherTemplateData(config)
	if err != nil {
		return serviceManagerTemplateData{}, err
	}
	hasTracePublisher := tracePublisher != nil && hasServiceHealthState
	tracePublisherTopicRef := ""
	tracePublisherTarget := ""
	tracePublishStreamName := ""
	tracePublishGrpcPath := ""
	if tracePublisher != nil {
		tracePublisherTopicRef = tracePublisher.topicRef
		tracePublisherTarget = tracePublisher.target
		tracePublishStreamName = tracePublisher.streamName
		tracePublishGrpcPath = tracePublisher.grpcPath
	}

	return serviceManagerTemplateData{
		ServiceLabel:             config.ServiceLabel(),
		ModuleBasePath:           config.ModuleBasePath,
		SmPrefix:                 config.GetSmPrefix(),
		ServiceManagerImportPath: config.ModuleBasePath + "/sm",
		HasServiceHealthState:    hasServiceHealthState,
		HealthStateProtoDir:      healthStateProtoDir,
		HealthStateGroupAccessor: healthStateGroupAccessor,
		HasGrpc:                  len(svcs) > 0,
		HasGrpcServers:           len(svcs) > 0,
		HasStates:                len(config.States) > 0 || config.AllStates,
		HasSysState:             hasSysState,
		HasGrpcS2SClients:        len(config.GrpcS2SClients) > 0 || config.GrpcS2SClientAll,
		HasSubscriptions:         len(subEntries) > 0,
		HasPublishers:            hasPublishers,
		HasAutoTls:               config.HasAutoTls(),
		AutoTlsKeyType:           config.AutoTlsKeyType(),
		HasSmb:                   hasSmb,
		SmbAuthorityServiceID:    smbAuthorityServiceID,
		HasGraphProjections:      hasAnyGraph,
		HasTimeseriesProjections: hasAnyTimeseries,
		ProjectionPkgAlias:       projPkgAlias,
		ProjectionPkgImport:      projPkgImport,
		HasTypedHealth:           hasTypedHealth,
		TypedHealthType:          typedHealthType,
		TypedHealthPkgAlias:      typedHealthPkgAlias,
		TypedHealthPkgImport:     typedHealthPkgImport,
		TacplusVersion:           buildinfo.Version,
		TacplusGitSha:            buildinfo.GitSha,
		ProtoSourceVersion:       protoSourceVersion,
		ProtoSourceGitSha:        protoGitSha,
		HasTracePublisher:        hasTracePublisher,
		TracePublisherTopicRef:   tracePublisherTopicRef,
		TracePublisherTarget:     tracePublisherTarget,
		TracePublishStreamName:   tracePublishStreamName,
		TracePublishGrpcPath:     tracePublishGrpcPath,
		S2STargetServices:        s2sTargetServices,
		GrpcServices:             grpcSvcs,
		WrapperImports:           wrapperImports,
		TypeImports:              typeImports,
		Subscriptions:            subEntries,
		PublisherTypeImports:     publisherTypeImports,
		Publishers:               publishers,
		DiskStores:               diskStores,
		HasDiskStores:            len(diskStores) > 0,
	}, nil
}

func collectDiskStoreRuntimeEntries(config *tactype.Config, grpcSvcs []GrpcServiceEntry) ([]DiskStoreRuntimeEntry, error) {
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
	var entries []DiskStoreRuntimeEntry
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
			entries = append(entries, DiskStoreRuntimeEntry{
				StoreName:     strutil.ToSnakeCase(msg.Name),
				TypeName:      msg.Name,
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
	streamName string
	grpcPath   string
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
		target:     pub.BrokerTag,
		streamName: pub.PublishStreamName,
		grpcPath:   pub.PublishPath,
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

// generateServiceManager generates the sm/ package.
// svcs is the list of gRPC services; may be empty for services with only states.
func generateServiceManager(config *tactype.Config, svcs []ServiceInfo, allFiles []model.ProtoFile) error {
	tmplContent, err := loadTemplate("service_manager.go.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load service_manager.go template: %w", err)
	}

	tmpl, err := template.New("servicemanager").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse service_manager.go template: %w", err)
	}

	data, err := buildServiceManagerData(config, svcs, allFiles)
	if err != nil {
		return fmt.Errorf("failed to build service manager template data: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute service_manager.go template: %w", err)
	}

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("failed to format service_manager.go: %w\nraw output:\n%s", err, buf.String())
	}

	outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix())
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create service_manager dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "s2sm.go")
	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write service_manager.go: %w", err)
	}

	zap.L().Info("Generated service-manager package", zap.String("path", outputPath))

	if err := generateServiceManagerIdentityErrorTest(data, outputDir); err != nil {
		return err
	}
	return nil
}

// generateServiceManagerIdentityErrorTest emits the self-test for the startup
// error classifier alongside s2sm.go. isPermanentIdentityInitError is a
// package-private function of the generated sm package, so the only place that
// can execute it is a test in that same package; the consumer-contract gate runs
// `go test -race ./...` over the generated output (run.sh:7962), which is what
// makes a revert of the typed classification fail a gate instead of passing
// silently (NEW-21). The current-generation fixture has no (spec.service), so it
// emits no s2sm package at all and cannot carry this coverage.
func generateServiceManagerIdentityErrorTest(data any, outputDir string) error {
	tmplContent, err := loadTemplate("service_manager_identity_error_test.go.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load service_manager_identity_error_test.go template: %w", err)
	}

	tmpl, err := template.New("servicemanageridentityerrortest").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse service_manager_identity_error_test.go template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute service_manager_identity_error_test.go template: %w", err)
	}

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("failed to format s2sm_identity_error_test.go: %w\nraw output:\n%s", err, buf.String())
	}

	outputPath := filepath.Join(outputDir, "s2sm_identity_error_test.go")
	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write s2sm_identity_error_test.go: %w", err)
	}

	zap.L().Info("Generated service-manager identity error self-test", zap.String("path", outputPath))
	return nil
}

// generateC2SServiceManager generates the c2sm/ entry point — a lightweight
// client-only ServiceManager with no health, state, or server lifecycle.
func generateC2SServiceManager(config *tactype.Config, clients []generatedGrpcClient) error {
	tmplContent, err := loadTemplate("c2sm.go.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load c2sm.go template: %w", err)
	}

	tmpl, err := template.New("c2sm").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse c2sm.go template: %w", err)
	}

	data := struct {
		ModuleBasePath string
		Targets        []string
	}{
		ModuleBasePath: config.ModuleBasePath,
		Targets:        uniqueGrpcClientTargets(clients),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute c2sm.go template: %w", err)
	}

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("failed to format c2sm.go: %w\nraw output:\n%s", err, buf.String())
	}

	outputDir := filepath.Join(config.OutputDir, "c2sm")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create c2sm dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "c2sm.go")
	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write c2sm.go: %w", err)
	}

	zap.L().Info("Generated c2sm entry point", zap.String("path", outputPath))
	return nil
}

// generateC2DServiceManager generates the c2dm/ entry point — a lightweight
// client-only ServiceManager for local daemon RPCs over a Unix socket.
func generateC2DServiceManager(config *tactype.Config, clients []generatedGrpcClient) error {
	tmplContent, err := loadTemplate("c2dm.go.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load c2dm.go template: %w", err)
	}

	tmpl, err := template.New("c2dm").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse c2dm.go template: %w", err)
	}

	data := struct {
		ModuleBasePath string
		Targets        []string
	}{
		ModuleBasePath: config.ModuleBasePath,
		Targets:        uniqueGrpcClientTargets(clients),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute c2dm.go template: %w", err)
	}

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("failed to format c2dm.go: %w\nraw output:\n%s", err, buf.String())
	}

	outputDir := filepath.Join(config.OutputDir, "c2dm")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create c2dm dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "c2dm.go")
	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write c2dm.go: %w", err)
	}

	zap.L().Info("Generated c2dm entry point", zap.String("path", outputPath))
	return nil
}

// generateGrpcAggregator generates sm/internal/grpc/grpc.go — the top-level gRPC aggregator
// that exposes Clients() and (future) Servers() accessors.
func generateGrpcAggregator(config *tactype.Config, hasClients bool) error {
	tmplContent, err := loadTemplate("grpc_aggregator.go.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load grpc_aggregator template: %w", err)
	}

	tmpl, err := template.New("grpcaggregator").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse grpc_aggregator template: %w", err)
	}

	data := struct {
		ModuleBasePath string
		SmPrefix       string
		HasClients     bool
	}{
		ModuleBasePath: config.ModuleBasePath,
		SmPrefix:       config.GetSmPrefix(),
		HasClients:     hasClients,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute grpc_aggregator template: %w", err)
	}

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("failed to format grpc.go: %w\nraw output:\n%s", err, buf.String())
	}

	outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "grpc")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create grpc aggregator dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "grpc.go")
	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write grpc.go: %w", err)
	}

	zap.L().Info("Generated gRPC aggregator", zap.String("path", outputPath))
	return nil
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

func goAccessorName(name string) string {
	if name == "." {
		name = ""
	}
	if name == "" {
		return ""
	}
	return strings.ToUpper(name[:1]) + name[1:]
}
