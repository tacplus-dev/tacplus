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

// pyGrpcServiceEntry holds per-service data for handler and service_manager templates.
type pyGrpcServiceEntry struct {
	GetterName               string // "identity_grpc_impl"
	HandleConfigName         string // "identity_config"
	ServiceName              string // "IdentityService"
	PackageName              string // "identity"
	WrapperImportPath        string // "s2sm.internal.grpc.servers.services.identity.service"
	WrapperClassName         string // "GrpcServer"
	WrapperVarName           string // "wrapper_identity"
	ConfigStateGroupAccessor string // "services"
	ConfigStateAccessor      string // "config_state"
	HasConfigState           bool
	AuthDisabled             bool
	AuthAnyUser              bool
	HasMethodAuth            bool
}

// pySubscriptionEntry holds per-subscription data for handler and service_manager templates.
type pySubscriptionEntry struct {
	FieldPascal       string // e.g. "EventStream"
	FieldSnake        string // e.g. "event_stream"
	HandlerMethodName string // e.g. "on_event_stream" or "on_event_stream_aegis" (disambiguated)
	SubscriberAlias   string // e.g. "EventStream" or "AegisEventStream" (unique import alias)
	VarName           string // e.g. "event_stream" or "aegis_event_stream" (unique variable name)
	SourcePackage     string // e.g. "aegis"
	SourceField       string // e.g. "event_stream"
	TypeName          string // e.g. "TopologyEvent"
	TypeAlias         string // e.g. "EventStreamEvent"
	TypeImportFrom    string // e.g. "svc_tacplus.types.vservices.claude_sensor"
	TypePbModule      string // e.g. "claude_sensor_pb2"
	ModuleName        string // e.g. "aegis_event_stream" — subscriber module filename
	TargetService     string // e.g. "eventbus" — service_id to resolve via ServiceHealthState
}

// pyTargetServiceEntry describes a target service that needs a ServiceConnection.
type pyTargetServiceEntry struct {
	ServiceID    string // e.g. "identity", "eventbus"
	AccessorName string // e.g. "identity", "event_bus" (for variable names)
}

// pySmTemplateData holds the data passed to handler.py.tmpl and service_manager.py.tmpl.
type pySmTemplateData struct {
	PythonPackageBase      string
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
	HealthStateModule      string
	HealthStatePbModule    string
	StateStoreServiceID    string
	TacplusVersion         string
	TacplusGitSha          string
	ProtoSourceVersion     string
	ProtoSourceGitSha      string
	HasTracePublisher      bool
	TracePublisherTopicRef string
	TracePublisherTarget   string
	TracePublishGrpcPath   string
	GrpcServices           []pyGrpcServiceEntry
	Subscriptions          []pySubscriptionEntry
	TargetServices         []pyTargetServiceEntry
}

func pythonClientTargets(clientSvcs []service.ServiceInfo) []pyTargetServiceEntry {
	seen := make(map[string]bool)
	var targets []pyTargetServiceEntry
	for _, cs := range clientSvcs {
		target := resolveTarget(cs)
		if seen[target] {
			continue
		}
		seen[target] = true
		safeTarget := strings.ReplaceAll(target, "-", "_")
		targets = append(targets, pyTargetServiceEntry{
			ServiceID:    target,
			AccessorName: strutil.ToSnakeCase(safeTarget),
		})
	}
	return targets
}

// buildPySmTemplateData constructs the template data.
// svcs are server services; clientSvcs are S2S client services (may be nil).
func buildPySmTemplateData(config *tactype.Config, svcs []service.ServiceInfo, protoDir string, clientSvcs ...[]service.ServiceInfo) (pySmTemplateData, error) {
	var grpcSvcs []pyGrpcServiceEntry
	configStates, err := stateproto.ConfigStateAccessors(config, config.FlatProtoDir)
	if err != nil {
		return pySmTemplateData{}, err
	}

	for _, svc := range svcs {
		snakePrefix := strings.TrimSuffix(pyGeneratedSnakeName(svc.ServiceName), "_service")
		pkgName := pyModuleName(svc.PackageName)
		// Compute wrapper module name: snake_case service name, strip package prefix
		snakeName := pyGeneratedSnakeName(svc.ServiceName)
		wrapperModName := snakeName
		pkgPrefix := pkgName + "_"
		if strings.HasPrefix(wrapperModName, pkgPrefix) {
			wrapperModName = wrapperModName[len(pkgPrefix):]
		}
		wrapperImport := fmt.Sprintf("%s.%s.internal.grpc.servers.%s.%s.%s",
			config.PythonPackageBase, config.GetSmPrefix(), pyDottedPath(svc.ParentDir), pkgName, wrapperModName)

		configPkg := svc.PackageName
		if config.SelfPackage != "" && svc.PackageName != config.SelfPackage {
			configPkg = config.SelfPackage
		}
		configStateGroupAccessor := ""
		configStateAccessor := ""
		hasConfigState := false
		if stateInfo, ok := configStates[configPkg]; ok {
			configStateGroupAccessor = pyPath(filepath.Dir(stateInfo.ProtoFile))
			configStateAccessor = strutil.ToSnakeCase(stateInfo.StateName)
			hasConfigState = true
		}

		grpcSvcs = append(grpcSvcs, pyGrpcServiceEntry{
			GetterName:               snakePrefix + "_grpc_impl",
			HandleConfigName:         snakePrefix + "_config",
			ServiceName:              svc.ServiceName,
			PackageName:              pkgName,
			WrapperImportPath:        wrapperImport,
			WrapperClassName:         "GrpcServer",
			WrapperVarName:           "wrapper_" + snakePrefix,
			ConfigStateGroupAccessor: configStateGroupAccessor,
			ConfigStateAccessor:      configStateAccessor,
			HasConfigState:           hasConfigState,
			AuthDisabled:             svc.AuthDisabled,
			AuthAnyUser:              svc.AuthAnyUser,
			HasMethodAuth:            svc.HasMethodAuth,
		})
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
				return pySmTemplateData{}, err
			}
			healthStateProtoDir = stateInfo.ProtoDir
			healthStatePackage = stateInfo.Package
		}
	}

	// Detect a spec.smb Service Mesh Blackbox service in the own package (SPIFFE
	// plan D7) — drives the SMB member-cert stream seam. Mirror the Go reference:
	// scan the flat proto descriptors for a service in config.SelfPackage whose
	// ServiceDescriptor.IsSMB is set.
	hasSmb := false
	if config.FlatProtoDir != "" && config.SelfPackage != "" {
		if dp, err := parser.NewDescriptorParser(config.FlatProtoDir); err == nil {
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
				if hasSmb {
					break
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

	var subs []pySubscriptionEntry
	for _, sub := range config.Subscriptions {
		event := resolvePySubscriptionEvent(config, protoDir, sub)
		fieldPascal := strutil.ToPascalCase(sub.SourceField)
		topicPascal := eventbus.RPCStemForTopicParts(sub.SourcePackage, sub.SourceStateRoot, sub.SourceField)
		topicSnake := strutil.ToSnakeCase(topicPascal)

		handlerMethod := "on_" + sub.SourceField + "_" + topicSnake
		subscriberAlias := topicPascal
		varName := topicSnake
		typeAlias := subscriberAlias + "Event"

		subs = append(subs, pySubscriptionEntry{
			FieldPascal:       fieldPascal,
			FieldSnake:        sub.SourceField,
			HandlerMethodName: handlerMethod,
			SubscriberAlias:   subscriberAlias,
			VarName:           varName,
			SourcePackage:     sub.SourcePackage,
			SourceField:       sub.SourceField,
			TypeName:          event.TypeName,
			TypeAlias:         typeAlias,
			TypeImportFrom:    event.TypeImportFrom,
			TypePbModule:      event.TypePbModule,
			ModuleName:        topicSnake,
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
	var targetSvcs []pyTargetServiceEntry
	if len(clientSvcs) > 0 && len(clientSvcs[0]) > 0 {
		targetSvcs = pythonClientTargets(clientSvcs[0])
	}

	healthStateModule := ""
	healthStatePbModule := ""
	if healthStateProtoDir != "" {
		healthStateModule = config.PythonPackageBase + ".types." + pyDottedPath(healthStateProtoDir)
		healthStatePbModule = pyModuleName(healthStatePackage) + "_pb2"
	}

	tracePublisher, err := detectTracePublisherTemplateData(config)
	if err != nil {
		return pySmTemplateData{}, err
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

	return pySmTemplateData{
		PythonPackageBase:      config.PythonPackageBase,
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
		HealthStateModule:      healthStateModule,
		HealthStatePbModule:    healthStatePbModule,
		StateStoreServiceID:    healthStatePackage,
		TacplusVersion:         buildinfo.Version,
		TacplusGitSha:          buildinfo.GitSha,
		ProtoSourceVersion:     protoSourceVersion,
		ProtoSourceGitSha:      protoGitSha,
		HasTracePublisher:      hasTracePublisher,
		TracePublisherTopicRef: tracePublisherTopicRef,
		TracePublisherTarget:   tracePublisherTarget,
		TracePublishGrpcPath:   tracePublishGrpcPath,
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
		target:   pub.BrokerTag,
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

// generatePyHandlerInterface generates handler.py in the sm/ directory.
func generatePyHandlerInterface(config *tactype.Config, svcs []service.ServiceInfo, protoDir string) error {
	tmplContent, err := loadTemplate("handler.py.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load handler template: %w", err)
	}

	tmpl, err := template.New("handler").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse handler template: %w", err)
	}

	data, err := buildPySmTemplateData(config, svcs, protoDir)
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
	outputPath := filepath.Join(outputDir, "handler.py")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write handler.py: %w", err)
	}

	zap.L().Info("Generated Python handler class", zap.String("path", outputPath))
	return nil
}

// generatePyServiceManager generates service_manager.py in the sm/ directory.
func generatePyServiceManager(config *tactype.Config, svcs []service.ServiceInfo, protoDir string, clientSvcs ...[]service.ServiceInfo) error {
	tmplContent, err := loadTemplate("service_manager.py.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load service_manager template: %w", err)
	}

	tmpl, err := template.New("servicemanager").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse service_manager template: %w", err)
	}

	data, err := buildPySmTemplateData(config, svcs, protoDir, clientSvcs...)
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
	outputPath := filepath.Join(outputDir, "service_manager.py")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write service_manager.py: %w", err)
	}

	zap.L().Info("Generated Python service_manager", zap.String("path", outputPath))
	return nil
}

// generateC2SServiceManager generates service_manager.py in the c2sm/ directory.
// Unlike s2sm, c2sm is a lightweight client-only manager with no health, state, or lifecycle.
func generateC2SServiceManager(config *tactype.Config, clientSvcs []service.ServiceInfo) error {
	tmplContent, err := loadTemplate("c2sm.py.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load c2sm.py template: %w", err)
	}

	tmpl, err := template.New("c2sm").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse c2sm.py template: %w", err)
	}

	data := struct {
		PythonPackageBase string
		Targets           []pyTargetServiceEntry
	}{
		PythonPackageBase: config.PythonPackageBase,
		Targets:           pythonClientTargets(clientSvcs),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute c2sm.py template: %w", err)
	}

	outputDir := filepath.Join(config.OutputDir, "c2sm")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create c2sm dir: %w", err)
	}

	// Ensure __init__.py exists
	initPath := filepath.Join(outputDir, "__init__.py")
	if _, err := os.Stat(initPath); os.IsNotExist(err) {
		if err := os.WriteFile(initPath, []byte("# Auto-generated sm package\n"), 0644); err != nil {
			return fmt.Errorf("failed to create c2sm __init__.py: %w", err)
		}
	}

	outputPath := filepath.Join(outputDir, "service_manager.py")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write c2sm service_manager.py: %w", err)
	}

	zap.L().Info("Generated c2sm entry point", zap.String("path", outputPath))
	return nil
}

// generateC2DServiceManager generates service_manager.py in the c2dm/ directory.
func generateC2DServiceManager(config *tactype.Config, clientSvcs []service.ServiceInfo) error {
	tmplContent, err := loadTemplate("c2dm.py.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load c2dm.py template: %w", err)
	}

	tmpl, err := template.New("c2dm").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse c2dm.py template: %w", err)
	}

	data := struct {
		PythonPackageBase string
		Targets           []pyTargetServiceEntry
	}{
		PythonPackageBase: config.PythonPackageBase,
		Targets:           pythonClientTargets(clientSvcs),
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute c2dm.py template: %w", err)
	}

	outputDir := filepath.Join(config.OutputDir, "c2dm")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create c2dm dir: %w", err)
	}

	initPath := filepath.Join(outputDir, "__init__.py")
	if _, err := os.Stat(initPath); os.IsNotExist(err) {
		if err := os.WriteFile(initPath, []byte("# Auto-generated sm package\n"), 0644); err != nil {
			return fmt.Errorf("failed to create c2dm __init__.py: %w", err)
		}
	}

	outputPath := filepath.Join(outputDir, "service_manager.py")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write c2dm service_manager.py: %w", err)
	}

	zap.L().Info("Generated c2dm entry point", zap.String("path", outputPath))
	return nil
}

// generateW2SServiceManager generates service_manager.py in the w2sm/ directory.
// Unlike s2sm, w2sm is a lightweight client-only manager for HTTP/SSE/WebSocket clients.
func generateW2SServiceManager(config *tactype.Config) error {
	tmplContent, err := loadTemplate("w2sm.py.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load w2sm.py template: %w", err)
	}

	tmpl, err := template.New("w2sm").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse w2sm.py template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, struct{}{}); err != nil {
		return fmt.Errorf("failed to execute w2sm.py template: %w", err)
	}

	outputDir := filepath.Join(config.OutputDir, "w2sm")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create w2sm dir: %w", err)
	}

	initPath := filepath.Join(outputDir, "__init__.py")
	if _, err := os.Stat(initPath); os.IsNotExist(err) {
		if err := os.WriteFile(initPath, []byte("# Auto-generated sm package\n"), 0644); err != nil {
			return fmt.Errorf("failed to create w2sm __init__.py: %w", err)
		}
	}

	outputPath := filepath.Join(outputDir, "service_manager.py")
	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write w2sm service_manager.py: %w", err)
	}

	zap.L().Info("Generated w2sm entry point", zap.String("path", outputPath))
	return nil
}

// filterExplicitGrpcServicesPy returns only services whose ServiceName matches
// an entry in config.GrpcServers.
func filterExplicitGrpcServicesPy(config *tactype.Config, servicesInfo []service.ServiceInfo) []service.ServiceInfo {
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
