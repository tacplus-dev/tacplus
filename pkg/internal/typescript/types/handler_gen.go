// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/buildinfo"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/boundaries"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/eventbus"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	stateproto "gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// tsGrpcServiceEntry holds per-service data for handler and service_manager templates.
type tsGrpcServiceEntry struct {
	GetterName               string // "identityGrpcImpl"
	HandleConfigName         string // "identityConfig"
	ServiceName              string // "IdentityService"
	HandlerTypeName          string // "IdentityServiceHandler"
	PackageName              string // "identity"
	ImportPath               string // "../../types/services/identity/index.js"
	WrapperImportPath        string // "./internal/grpc/servers/services/identity/service.js"
	WrapperVarName           string // "wrapperIdentity"
	WrapperClassName         string // "GrpcServer"
	ConfigStateGroupAccessor string // "services"
	ConfigStateAccessor      string // "configState"
	HasConfigState           bool
	AuthDisabled             bool
	AuthAnyUser              bool
}

// tsSubscriptionEntry holds per-subscription data for handler and service_manager templates.
type tsSubscriptionEntry struct {
	FieldPascal       string // e.g. "EventStream"
	FieldCamel        string // e.g. "eventStream"
	HandlerMethodName string // e.g. "onEventStream" or "onEventStreamAegis" (disambiguated)
	SourcePackage     string // e.g. "aegis"
	SourceField       string // e.g. "event_stream"
	TypeName          string // e.g. "TopologyEvent"
	TypeAlias         string // e.g. "EventStreamEvent"
	TypeImportPath    string // relative import path to generated type index
	ModuleName        string // e.g. "aegis_event_stream" — subscriber module filename
	SubscriberAlias   string // e.g. "AegisEventStream" — unique subscriber import alias
	TargetService     string // e.g. "eventbus" — service_id to resolve via ServiceHealthState
}

// tsTargetServiceEntry describes a target service that needs a ServiceConnection.
type tsTargetServiceEntry struct {
	ServiceID    string // e.g. "identity", "eventbus"
	AccessorName string // e.g. "identity", "event_bus" (for variable names)
	ParamName    string // e.g. "identityCp"
}

// tsSmTemplateData holds the data passed to handler.ts.tmpl and service_manager.ts.tmpl.
type tsSmTemplateData struct {
	TsPackageBase          string
	TsPackagePrefix        string
	ServiceLabel           string
	HasGrpc                bool
	HasStates              bool
	HasGrpcS2SClients      bool
	HasServiceHealthState  bool
	HasAutoTls             bool
	AutoTlsKeyType         string
	HasSmb                 bool
	HasSubscriptions       bool
	HealthStateProtoDir    string // e.g. "services/identity"
	HealthStatePackage     string
	StateStoreServiceID    string
	TacplusVersion         string
	TacplusGitSha          string
	ProtoSourceVersion     string
	ProtoSourceGitSha      string
	HasTracePublisher      bool
	TracePublisherTopicRef string
	TracePublisherTarget   string
	TracePublishGrpcPath   string
	// TokenExchangerPackage / MintServiceName are needed by service_manager.ts.tmpl
	// to call X509Mint directly. They are discovered the same way as in sys.go.
	HasTokenExchanger     bool
	TokenExchangerPackage string
	MintServiceName       string
	DefaultAudience       string
	GrpcServices          []tsGrpcServiceEntry
	Subscriptions         []tsSubscriptionEntry
	TargetServices        []tsTargetServiceEntry
}

func tsClientTargets(clientSvcs []service.ServiceInfo) []tsTargetServiceEntry {
	seen := make(map[string]bool)
	var targets []tsTargetServiceEntry
	for _, cs := range clientSvcs {
		target := resolveTarget(cs)
		if seen[target] {
			continue
		}
		seen[target] = true
		safeTarget := strings.ReplaceAll(target, "-", "_")
		accessor := strutil.ToSnakeCase(safeTarget)
		targets = append(targets, tsTargetServiceEntry{
			ServiceID:    target,
			AccessorName: accessor,
			ParamName:    strutil.ToLowerCamelCase(accessor) + "Cp",
		})
	}
	return targets
}

// buildTsSmTemplateData constructs the template data.
// svcs are server services; clientSvcs are S2S client services (may be nil).
func buildTsSmTemplateData(config *tactype.Config, svcs []service.ServiceInfo, clientSvcs ...[]service.ServiceInfo) (tsSmTemplateData, error) {
	var grpcSvcs []tsGrpcServiceEntry
	configStates, err := stateproto.ConfigStateAccessors(config, config.FlatProtoDir)
	if err != nil {
		return tsSmTemplateData{}, err
	}

	for _, svc := range svcs {
		snakePrefix := strings.TrimSuffix(tsGeneratedSnakeName(svc.ServiceName), "_service")
		lcPrefix := strutil.ToLowerCamelCase(snakePrefix)
		prefix := strutil.ToPascalCase(snakePrefix)
		// Compute wrapper file name: snake_case service name, strip package prefix
		snakeName := tsGeneratedSnakeName(svc.ServiceName)
		wrapperFileName := strutil.StripPackagePrefix(snakeName, svc.PackageName)
		wrapperImport := fmt.Sprintf("./internal/grpc/servers/%s/%s/%s.js", svc.ParentDir, svc.PackageName, wrapperFileName)
		typeModuleDir := typescriptModuleDirForPackageOrDir(config.OutputDir, svc.PackageName, svc.ParentDir)

		configPkg := svc.PackageName
		if config.SelfPackage != "" && svc.PackageName != config.SelfPackage {
			configPkg = config.SelfPackage
		}
		configStateGroupAccessor := ""
		configStateAccessor := ""
		hasConfigState := false
		if stateInfo, ok := configStates[configPkg]; ok {
			configStateGroupAccessor = filepath.ToSlash(filepath.Dir(stateInfo.ProtoFile))
			configStateAccessor = strutil.ToLowerCamelCase(stateInfo.StateName)
			hasConfigState = true
		}

		grpcSvcs = append(grpcSvcs, tsGrpcServiceEntry{
			GetterName:               lcPrefix + "GrpcImpl",
			HandleConfigName:         lcPrefix + "Config",
			ServiceName:              svc.ServiceName,
			HandlerTypeName:          svc.ServiceName + "Handler",
			PackageName:              svc.PackageName,
			ImportPath:               fmt.Sprintf("../%s/index.js", filepath.ToSlash(typeModuleDir)),
			WrapperImportPath:        wrapperImport,
			WrapperVarName:           "wrapper" + prefix,
			WrapperClassName:         "GrpcServer",
			ConfigStateGroupAccessor: configStateGroupAccessor,
			ConfigStateAccessor:      configStateAccessor,
			HasConfigState:           hasConfigState,
			AuthDisabled:             svc.AuthDisabled,
			AuthAnyUser:              svc.AuthAnyUser,
		})
	}

	// Compute package prefix (same logic as sys.go)
	tsPackagePrefix := config.TsPackageBase + "-"
	if strings.HasPrefix(config.TsPackageBase, "@") && !strings.Contains(config.TsPackageBase, "/") {
		tsPackagePrefix = config.TsPackageBase + "/"
	}

	hasServiceHealthState := false
	healthStateProtoDir := ""
	healthStatePackage := "state-store"
	for _, b := range config.States {
		if b.IsHealthState && b.AccessMode != tactype.AccessRO {
			hasServiceHealthState = true
			healthStateProtoDir = strings.TrimSuffix(b.ProtoFile, ".proto")
			stateInfo, err := stateproto.DiscoverStateRoot(config.FlatProtoDir, b.MessageName)
			if err != nil {
				return tsSmTemplateData{}, err
			}
			healthStateProtoDir = stateInfo.ProtoDir
			healthStatePackage = stateInfo.Package
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

	var subs []tsSubscriptionEntry
	for _, sub := range config.Subscriptions {
		event := resolveTsSubscriptionEvent(config, sub)
		fieldPascal := strutil.ToPascalCase(sub.SourceField)
		fieldCamel := strings.ToLower(fieldPascal[:1]) + fieldPascal[1:]
		topicPascal := eventbus.RPCStemForTopicParts(sub.SourcePackage, sub.SourceStateRoot, sub.SourceField)
		topicSnake := strutil.ToSnakeCase(topicPascal)

		handlerMethod := "on" + fieldPascal + topicPascal
		subscriberAlias := topicPascal
		typeAlias := subscriberAlias + "Event"

		subs = append(subs, tsSubscriptionEntry{
			FieldPascal:       fieldPascal,
			FieldCamel:        fieldCamel,
			HandlerMethodName: handlerMethod,
			SourcePackage:     sub.SourcePackage,
			SourceField:       sub.SourceField,
			TypeName:          event.TypeName,
			TypeAlias:         typeAlias,
			TypeImportPath:    tsRelativeModuleImport(config.GetSmPrefix(), event.ModuleDir),
			ModuleName:        topicSnake,
			SubscriberAlias:   subscriberAlias,
			TargetService:     "eventbus",
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

	// Compute distinct target services from client services (for ServiceConnection creation).
	var targetSvcs []tsTargetServiceEntry
	if len(clientSvcs) > 0 && len(clientSvcs[0]) > 0 {
		targetSvcs = tsClientTargets(clientSvcs[0])
	}

	// Discover the proto package containing the token mint service so the
	// generated ServiceManager can call X509Mint directly.
	texPkg := ""
	mintServiceName := ""
	hasTokenExchanger := false
	info, err := parser.DiscoverTokenExchangerPackage(config.AnnotatedProtoDir)
	if err != nil {
		return tsSmTemplateData{}, err
	}
	if info != nil {
		texPkg = info.Package
		mintServiceName = info.ServiceName
		hasTokenExchanger = true
	}
	defaultAudience := config.ServiceLabel()
	if hasTokenExchanger {
		defaultAudience = texPkg
	}
	tracePublisher, err := detectTracePublisherTemplateData(config)
	if err != nil {
		return tsSmTemplateData{}, err
	}
	hasTracePublisher := tracePublisher != nil && hasServiceHealthState
	tracePublisherTopicRef := ""
	tracePublisherTarget := ""
	tracePublishGrpcPath := ""
	if tracePublisher != nil {
		tracePublisherTopicRef = tracePublisher.topicRef
		tracePublisherTarget = tracePublisher.target
		tracePublishGrpcPath = tracePublisher.grpcPath
	}

	// Detect a spec.smb Service Mesh Blackbox service in the own package
	// (SPIFFE plan D7) — drives the SMB member-cert stream seam. Mirrors the
	// SelfPackage-based detection in the Go generator.
	hasSmb := false
	if config.FlatProtoDir != "" {
		if dp, err := parser.NewDescriptorParser(config.FlatProtoDir); err == nil {
			for _, fd := range dp.GetAllFiles() {
				if config.SelfPackage != "" && fd.Package == config.SelfPackage {
					for si := range fd.Services {
						if fd.Services[si].IsSMB {
							hasSmb = true
							break
						}
					}
				}
			}
		}
	}

	return tsSmTemplateData{
		TsPackageBase:          config.TsPackageBase,
		TsPackagePrefix:        tsPackagePrefix,
		ServiceLabel:           config.ServiceLabel(),
		HasGrpc:                len(svcs) > 0,
		HasStates:              len(config.States) > 0 || config.AllStates,
		HasGrpcS2SClients:      len(config.GrpcS2SClients) > 0 || config.GrpcS2SClientAll,
		HasServiceHealthState:  hasServiceHealthState,
		HasAutoTls:             config.HasAutoTls(),
		AutoTlsKeyType:         config.AutoTlsKeyType(),
		HasSmb:                 hasSmb,
		HasSubscriptions:       len(subs) > 0,
		HealthStateProtoDir:    healthStateProtoDir,
		HealthStatePackage:     healthStatePackage,
		StateStoreServiceID:    healthStatePackage,
		TacplusVersion:         buildinfo.Version,
		TacplusGitSha:          buildinfo.GitSha,
		ProtoSourceVersion:     protoSourceVersion,
		ProtoSourceGitSha:      protoGitSha,
		HasTracePublisher:      hasTracePublisher,
		TracePublisherTopicRef: tracePublisherTopicRef,
		TracePublisherTarget:   tracePublisherTarget,
		TracePublishGrpcPath:   tracePublishGrpcPath,
		HasTokenExchanger:      hasTokenExchanger,
		TokenExchangerPackage:  texPkg,
		MintServiceName:        mintServiceName,
		DefaultAudience:        defaultAudience,
		GrpcServices:           grpcSvcs,
		Subscriptions:          subs,
		TargetServices:         targetSvcs,
	}, nil
}

type tracePublisherTemplateData struct {
	topicRef string
	target   string
	grpcPath string
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
		topicRef: pub.TopicRef,
		target:   pub.BrokerLabel,
		grpcPath: pub.PublishPath,
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

// generateTsHandlerInterface generates handler.ts in the sm/ directory.
func generateTsHandlerInterface(config *tactype.Config, svcs []service.ServiceInfo) error {
	tmplContent, err := loadTemplate("handler.ts.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load handler template: %w", err)
	}

	tmpl, err := template.New("handler").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse handler template: %w", err)
	}

	data, err := buildTsSmTemplateData(config, svcs)
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
	outputPath := filepath.Join(outputDir, "handler.ts")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write handler.ts: %w", err)
	}

	zap.L().Info("Generated TypeScript handler interface", zap.String("path", outputPath))
	return nil
}

// generateTsServiceManager generates service_manager.ts in the sm/ directory.
func generateTsServiceManager(config *tactype.Config, svcs []service.ServiceInfo, clientSvcs ...[]service.ServiceInfo) error {
	tmplContent, err := loadTemplate("service_manager.ts.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load service_manager template: %w", err)
	}

	tmpl, err := template.New("servicemanager").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse service_manager template: %w", err)
	}

	data, err := buildTsSmTemplateData(config, svcs, clientSvcs...)
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
	outputPath := filepath.Join(outputDir, "index.ts")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write service_manager index.ts: %w", err)
	}

	zap.L().Info("Generated TypeScript service_manager", zap.String("path", outputPath))
	return nil
}

// generateC2SServiceManager generates the c2sm/ entry point — a lightweight
// client-only ServiceManager with no health, state, or server lifecycle.
func generateC2SServiceManager(config *tactype.Config, clientSvcs []service.ServiceInfo) error {
	tmplContent, err := loadTemplate("c2sm.ts.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load c2sm.ts template: %w", err)
	}

	tmpl, err := template.New("c2sm").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse c2sm.ts template: %w", err)
	}

	data := struct {
		ModuleBasePath string
		Targets        []tsTargetServiceEntry
	}{
		ModuleBasePath: config.ModuleBasePath,
		Targets:        tsClientTargets(clientSvcs),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute c2sm.ts template: %w", err)
	}

	outputDir := filepath.Join(config.OutputDir, "c2sm")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create c2sm dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "index.ts")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write c2sm index.ts: %w", err)
	}

	zap.L().Info("Generated TypeScript c2sm entry point", zap.String("path", outputPath))
	return nil
}

// generateC2DServiceManager generates the c2dm/ entry point - a lightweight
// client-only ServiceManager that connects to a local daemon Unix socket.
func generateC2DServiceManager(config *tactype.Config, clientSvcs []service.ServiceInfo) error {
	tmplContent, err := loadTemplate("c2dm.ts.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load c2dm.ts template: %w", err)
	}

	tmpl, err := template.New("c2dm").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse c2dm.ts template: %w", err)
	}

	data := struct {
		Targets []tsTargetServiceEntry
	}{
		Targets: tsClientTargets(clientSvcs),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute c2dm.ts template: %w", err)
	}

	outputDir := filepath.Join(config.OutputDir, "c2dm")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create c2dm dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "index.ts")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write c2dm index.ts: %w", err)
	}

	zap.L().Info("Generated TypeScript c2dm entry point", zap.String("path", outputPath))
	return nil
}

// generateW2SServiceManager generates the w2sm/ entry point — a lightweight
// browser-only ServiceManager with FetchTransport and window.__TACPLUS__ config.
func generateW2SServiceManager(config *tactype.Config) error {
	tmplContent, err := loadTemplate("w2sm.ts.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load w2sm.ts template: %w", err)
	}

	tmpl, err := template.New("w2sm").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse w2sm.ts template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, nil); err != nil {
		return fmt.Errorf("failed to execute w2sm.ts template: %w", err)
	}

	outputDir := filepath.Join(config.OutputDir, "w2sm")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create w2sm dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "index.ts")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write w2sm index.ts: %w", err)
	}

	zap.L().Info("Generated TypeScript w2sm entry point", zap.String("path", outputPath))
	return nil
}

// filterExplicitGrpcServicesTs returns only services whose ServiceName matches
// an entry in config.GrpcServers.
func filterExplicitGrpcServicesTs(config *tactype.Config, servicesInfo []service.ServiceInfo) []service.ServiceInfo {
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
