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

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/boundaries"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/header"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/consts"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// ==================== Per-service StateClient types ====================

// javaSingletonBoundary describes a non-repeated message field in the state root.
type javaSingletonBoundary struct {
	FieldName    string // camelCase: "roster"
	FieldPascal  string // PascalCase: "Roster"
	TypeName     string // Simple: "TeamRoster"
	JavaType     string // Qualified: "Baseball.TeamRoster"
	IsPersisted  bool
}

// javaCollectionBoundary describes a repeated message field in the state root.
type javaCollectionBoundary struct {
	FieldName      string // camelCase: "players"
	FieldPascal    string // PascalCase: "Players"
	TypeName       string // Simple: "Player"
	JavaType       string // Qualified: "Baseball.Player"
	KeyFieldName   string // camelCase: "playerId"
	KeyFieldPascal string // PascalCase: "PlayerId"
	IsPersisted    bool

	// MaxEntries is the client-side snapshot safety limit: the field's
	// (spec.eviction_max_size) when declared, else the shared default.
	MaxEntries int64
}

// javaStateImportField describes a field with state_import annotation.
type javaStateImportField struct {
	FieldName   string // camelCase: "baseballState"
	FieldPascal string // PascalCase: "BaseballState"
	TypeName    string // Simple: "BaseballState"
	JavaType    string // Qualified: "Baseball.BaseballState"
}

// javaTopicBoundary describes a pub/sub topic field in the state root.
type javaTopicBoundary struct {
	FieldName           string // camelCase: "eventStream"
	FieldPascal         string // PascalCase: "EventStream"
	TypeName            string // Simple: "EventData"
	JavaType            string // Qualified: "Baseball.EventData"
	SubscribeRecordType string // Qualified generated envelope: "Baseball.StateFieldSubscribeRecord"
}

// javaCommandBoundary describes a command RPC field in the state root.
type javaCommandBoundary struct {
	FieldName   string // camelCase: "reloadConfig"
	FieldPascal string // PascalCase: "ReloadConfig"
	TypeName    string // Simple: "ReloadConfigRequest"
	JavaType    string // Qualified: "Baseball.ReloadConfigRequest"
}

// javaViewBoundary describes a read-only view over multiple source collections.
type javaViewBoundary struct {
	FieldName      string   // camelCase: "allKeys"
	FieldPascal    string   // PascalCase: "AllKeys"
	TypeName       string   // Simple: "ManagedJwk"
	JavaType       string   // Qualified: "Jwk.ManagedJwk"
	KeyFieldName   string   // camelCase: "kid"
	KeyFieldPascal string   // PascalCase: "Kid"
	ViewSources    []string // PascalCase source field names: ["ConfigKeys", "DynamicKeys", "ExternalKeys"]
}

// javaStateWrapperData is the template data for generating the Java state wrapper.
type javaStateWrapperData struct {
	Header           string
	Package          string // full Java package
	OuterClassName   string // "Baseball"
	StateRootName    string // "BaseballState"
	StateServiceName string // "BaseballStateService" — the gRPC service name
	GrpcServiceFQN   string // "com.example.types.services.baseball.BaseballStateServiceGrpc" — fully qualified gRPC stub class
	TraceFQN         string // "com.example.types.sys.trace.Trace"
	// IdentityManagerFQN is the chassis IdentityManager, used by the watch loops
	// to invalidate a REJECTED credential so the retry re-mints instead of
	// replaying a token the authority no longer accepts.
	IdentityManagerFQN string // "com.example.chassis.service_im.IdentityManager"
	Imports          []string
	Singletons       []javaSingletonBoundary
	Collections      []javaCollectionBoundary
	Topics           []javaTopicBoundary
	StateImports     []javaStateImportField
	Commands         []javaCommandBoundary
	Views            []javaViewBoundary
	ReadOnly         bool // true for :ro (accessors only, no mutators)
	WriteOnce        bool // true for :worm (write-once read-many: set/add but no lock/delete)
}

// ==================== Aggregator / registry types ====================

// javaGeneratedStateClient tracks a generated state client for aggregation.
type javaGeneratedStateClient struct {
	PackageName   string // "baseball"
	PascalName    string // "Baseball"
	FieldName     string // camelCase accessor name: "baseballState", "bootstrapJwks"
	StateRootName string // "BaseballState", "BootstrapJwks"
	ClientFQN     string // fully qualified StateClient class
	Group         string // "services"
	Boundaries    []boundaries.BoundaryData
}

// javaGroupStateClientsData is the template data for the group StateClients.
type javaGroupStateClientsData struct {
	Header  string
	Package string
	Group   string
	Clients []javaGeneratedStateClient
}

// javaStateClientGroupInfo represents a top-level proto directory group.
type javaStateClientGroupInfo struct {
	Name      string // "services"
	FieldName string // Java accessor field/method name, e.g. "services"
	FQN       string // fully qualified StateClients class for this group
}

// javaRootStateClientsData is the template data for the root StateClients.
type javaRootStateClientsData struct {
	Header  string
	Package string
	Groups  []javaStateClientGroupInfo
}

// ==================== Top-level entry point ====================

// generateJavaStateClientWrappers generates typed state client wrappers
// and group/root aggregators for Java.
func generateJavaStateClientWrappers(config *tactype.Config, servicesInfo []ServiceInfo, inputDir string) error {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	generated := make(map[string]bool)
	var clients []javaGeneratedStateClient

	for _, svc := range servicesInfo {
		fileDesc, err := dp.GetFileDescriptor(svc.ProtoFile)
		if err != nil {
			continue
		}
		// Exception: the manifest-declared runtime config state proto is an
		// application-level state boundary for runtime config delivery.
		if proto.IsInfraProtoFile(svc.ProtoFile, fileDesc) {
			if !proto.IsRuntimeConfigStateProto(svc.ProtoFile) {
				continue
			}
		}

		for i := range fileDesc.Messages {
			ann, ok := fileDesc.Messages[i].Annotations["service_state"]
			if !ok || ann.StringValue == "" {
				continue
			}

			accessMode, shouldGenerate := config.AccessModeForState(svc.ProtoFile, ann.StringValue)
			if !shouldGenerate {
				continue
			}

			key := svc.PackageName + "." + ann.StringValue
			if generated[key] {
				continue
			}
			generated[key] = true

			client, err := generateJavaStateWrapper(config, svc, fileDesc, &fileDesc.Messages[i], dp, accessMode)
			if err != nil {
				return fmt.Errorf("failed to generate Java state wrapper for %s: %w", ann.StringValue, err)
			}
			clients = append(clients, *client)
		}
	}

	if len(clients) == 0 {
		return nil
	}

	sort.Slice(clients, func(i, j int) bool {
		if clients[i].Group != clients[j].Group {
			return clients[i].Group < clients[j].Group
		}
		if clients[i].PackageName != clients[j].PackageName {
			return clients[i].PackageName < clients[j].PackageName
		}
		return clients[i].StateRootName < clients[j].StateRootName
	})

	// Generate group aggregators, root aggregator, and registry
	if err := generateJavaRootStateClients(config, clients); err != nil {
		return fmt.Errorf("failed to generate Java state client aggregators: %w", err)
	}

	return nil
}

// ==================== Per-service StateClient generation ====================

func generateJavaStateWrapper(config *tactype.Config, svc ServiceInfo, fileDesc *parser.FileDescriptor, stateRoot *parser.MessageDescriptor, dp *parser.DescriptorParser, accessMode tactype.AccessMode) (*javaGeneratedStateClient, error) {
	serviceName := svc.PackageName
	outerClass := strutil.ToCamelCase(serviceName)
	// Check for naming collision with a message or service of the same name.
	// protoc adds "OuterClass" suffix when any top-level type (message, enum, service) shares
	// the outer class name, so we must mirror that logic here.
	outerClassConflict := false
	for _, msg := range fileDesc.Messages {
		if msg.Name == outerClass {
			outerClassConflict = true
			break
		}
	}
	if !outerClassConflict {
		for _, svc := range fileDesc.Services {
			if svc.Name == outerClass {
				outerClassConflict = true
				break
			}
		}
	}
	if outerClassConflict {
		outerClass += "OuterClass"
	}
	basePackage := strings.TrimSuffix(config.JavaPackageBase, ".types")

	var singletons []javaSingletonBoundary
	var collections []javaCollectionBoundary
	var topics []javaTopicBoundary
	var stateImports []javaStateImportField
	var commands []javaCommandBoundary
	var views []javaViewBoundary
	var imports []string

	for _, field := range stateRoot.Fields {
		if importAnn, ok := field.Annotations["state_import"]; ok && importAnn.StringValue != "" {
			typeName := extractSimpleTypeName(field.Type)
			javaType := outerClass + "." + typeName
			if parts := strings.Split(field.Type, "."); len(parts) > 1 {
				crossPkg := strings.Join(parts[:len(parts)-1], ".")
				if crossPkg != fileDesc.Package {
					crossOuter := resolveCrossOuterClass(dp, crossPkg)
					javaType = crossOuter + "." + typeName
					crossDir := resolveProtoDir(dp, crossPkg)
					crossPackage := javaPackageForProtoPackage(basePackage, crossPkg, crossDir)
					imports = append(imports, crossPackage+"."+crossOuter)
				}
			}
			stateImports = append(stateImports, javaStateImportField{
				FieldName:   strutil.ToLowerCamelCase(field.Name),
				FieldPascal: strutil.ToPascalCase(field.Name),
				TypeName:    typeName,
				JavaType:    javaType,
			})
			continue
		}

		// View: read-only union of multiple source collections (skip normal classification)
		if vsAnn, ok := field.Annotations["view_sources"]; ok && len(vsAnn.StringSliceValue) > 0 {
			typeName := extractSimpleTypeName(field.Type)
			javaType := outerClass + "." + typeName
			if parts := strings.SplitN(field.Type, ".", 2); len(parts) == 2 && parts[0] != fileDesc.Package {
				crossPkg := parts[0]
				crossOuter := resolveCrossOuterClass(dp, crossPkg)
				javaType = crossOuter + "." + typeName
				crossDir := resolveProtoDir(dp, crossPkg)
				crossImport := fmt.Sprintf("%s.types.%s.%s.%s", basePackage, crossDir, crossPkg, crossOuter)
				imports = append(imports, crossImport)
			}
			// Find indexed key field
			keyFieldName, keyFieldPascal := "", ""
			boundaryMsg := findJavaMessageInFile(fileDesc, field.Type)
			if boundaryMsg == nil && strings.Contains(field.Type, ".") {
				crossPkg := strings.SplitN(field.Type, ".", 2)[0]
				simpleType := extractSimpleTypeName(field.Type)
				boundaryMsg = findMessageInParser(dp, crossPkg, simpleType)
			}
			if boundaryMsg != nil {
				for _, f := range boundaryMsg.Fields {
					if ann, ok := f.Annotations["indexed"]; ok && ann.BoolValue {
						keyFieldName = strutil.ToLowerCamelCase(f.Name)
						keyFieldPascal = strutil.ToPascalCase(f.Name)
						break
					}
				}
			}
			// Convert source field names to PascalCase
			var sourcePascal []string
			for _, src := range vsAnn.StringSliceValue {
				sourcePascal = append(sourcePascal, strutil.ToPascalCase(src))
			}
			views = append(views, javaViewBoundary{
				FieldName:      strutil.ToLowerCamelCase(field.Name),
				FieldPascal:    strutil.ToPascalCase(field.Name),
				TypeName:       typeName,
				JavaType:       javaType,
				KeyFieldName:   keyFieldName,
				KeyFieldPascal: keyFieldPascal,
				ViewSources:    sourcePascal,
			})
			continue
		}

		// Command: explicit command RPC (skip normal boundary classification)
		if cmdAnn, ok := field.Annotations["command"]; ok && cmdAnn.BoolValue {
			if privAnn, ok := field.Annotations["private"]; ok && privAnn.BoolValue {
				continue
			}
			typeName := extractSimpleTypeName(field.Type)
			javaType := outerClass + "." + typeName
			if parts := strings.SplitN(field.Type, ".", 2); len(parts) == 2 && parts[0] != fileDesc.Package {
				crossPkg := parts[0]
				crossOuter := resolveCrossOuterClass(dp, crossPkg)
				javaType = crossOuter + "." + typeName
			}
			commands = append(commands, javaCommandBoundary{
				FieldName:   strutil.ToLowerCamelCase(field.Name),
				FieldPascal: strutil.ToPascalCase(field.Name),
				TypeName:    typeName,
				JavaType:    javaType,
			})
			continue
		}

		// Topic: pub/sub broadcast boundary. Topic fields are backed by
		// PublishTo*/SubscribeTo* RPCs, not singleton Watch* state RPCs.
		if _, ok := field.Annotations["topic"]; ok {
			typeName := extractSimpleTypeName(field.Type)
			javaType := outerClass + "." + typeName
			if parts := strings.SplitN(field.Type, ".", 2); len(parts) == 2 && parts[0] != fileDesc.Package {
				crossPkg := parts[0]
				crossOuter := resolveCrossOuterClass(dp, crossPkg)
				javaType = crossOuter + "." + typeName
				crossDir := resolveProtoDir(dp, crossPkg)
				crossImport := fmt.Sprintf("%s.types.%s.%s.%s", basePackage, crossDir, crossPkg, crossOuter)
				imports = append(imports, crossImport)
			}
			topics = append(topics, javaTopicBoundary{
				FieldName:           strutil.ToLowerCamelCase(field.Name),
				FieldPascal:         strutil.ToPascalCase(field.Name),
				TypeName:            typeName,
				JavaType:            javaType,
				SubscribeRecordType: outerClass + "." + proto.TopicSubscribeRecordName(stateRoot.Name, strutil.ToPascalCase(field.Name)),
			})
			continue
		}

		if field.TypeKind != "message" {
			continue
		}

		// Skip map fields with scalar/enum values: their map-entry type is not
		// a real Java class, so state client code that references it won't compile.
		if field.IsMap && field.MapValueTypeKind != "message" {
			continue
		}

		// Skip private boundaries — they are not exposed in the state client
		if privAnn, ok := field.Annotations["private"]; ok && privAnn.BoolValue {
			continue
		}

		typeName := extractSimpleTypeName(field.Type)

		// Check for cross-package type reference
		javaType := outerClass + "." + typeName
		if parts := strings.SplitN(field.Type, ".", 2); len(parts) == 2 && parts[0] != fileDesc.Package {
			crossPkg := parts[0]
			crossOuter := resolveCrossOuterClass(dp, crossPkg)
			javaType = crossOuter + "." + typeName
			// Add import for the cross-package outer class
			crossDir := resolveProtoDir(dp, crossPkg)
			crossImport := fmt.Sprintf("%s.types.%s.%s.%s", basePackage, crossDir, crossPkg, crossOuter)
			imports = append(imports, crossImport)
		}

		if field.IsRepeated {
			boundaryMsg := findJavaMessageInFile(fileDesc, field.Type)
			keyFieldName := ""
			keyFieldPascal := ""
			if boundaryMsg != nil {
				for _, f := range boundaryMsg.Fields {
					if ann, ok := f.Annotations["indexed"]; ok && ann.BoolValue {
						keyFieldName = strutil.ToLowerCamelCase(f.Name)
						keyFieldPascal = strutil.ToPascalCase(f.Name)
						break
					}
				}
			}
			collections = append(collections, javaCollectionBoundary{
				FieldName:      strutil.ToLowerCamelCase(field.Name),
				FieldPascal:    strutil.ToPascalCase(field.Name),
				TypeName:       typeName,
				JavaType:       javaType,
				KeyFieldName:   keyFieldName,
				KeyFieldPascal: keyFieldPascal,
				IsPersisted:    field.Annotations["persistence"].BoolValue,

				MaxEntries: boundaries.CollectionSafetyLimit(field),
			})
		} else {
			singletons = append(singletons, javaSingletonBoundary{
				FieldName:    strutil.ToLowerCamelCase(field.Name),
				FieldPascal:  strutil.ToPascalCase(field.Name),
				TypeName:     typeName,
				JavaType:     javaType,
				IsPersisted:  field.Annotations["persistence"].BoolValue,
			})
		}
	}

	// Compute Java package and import for the outer class.
	// Include state root name as subpackage to avoid collisions when
	// multiple state roots share the same proto package (e.g., BootstrapJwks
	// and GrpcJwks both in the same package).
	rootSubPkg := strings.ToLower(stateRoot.Name)
	statePathPkg := javaStatePathPackage(svc.ParentDir)
	javaPackage := basePackage + "." + config.GetSmPrefix() + ".internal.state"
	if statePathPkg != "" {
		javaPackage += "." + statePathPkg
	}
	javaPackage += "." + serviceName + "." + rootSubPkg

	sn, fn := proto.ServiceProtoParts(svc.ProtoFile)
	stateTypeDir := strings.Split(svc.ParentDir, string(filepath.Separator))[0]
	var typesImportPath string
	if sn == fn {
		typesImportPath = fmt.Sprintf("%s.types.%s.%s.%s", basePackage, stateTypeDir, serviceName, outerClass)
	} else {
		typesImportPath = fmt.Sprintf("%s.types.%s.%s.%s.%s", basePackage, stateTypeDir, serviceName, serviceName, outerClass)
	}

	imports = append(imports, typesImportPath)
	imports = uniqueJavaStateImports(imports)

	// Compute gRPC service name and FQN for the state service stub.
	stateServiceName := proto.StateServiceName(stateRoot.Name)
	typesPackage := typesImportPath[:strings.LastIndex(typesImportPath, ".")]
	grpcServiceFQN := typesPackage + "." + stateServiceName + "Grpc"

	data := javaStateWrapperData{
		Header:           header.GeneratedHeader(consts.LangJava, svc.ProtoFile),
		Package:          javaPackage,
		OuterClassName:   outerClass,
		StateRootName:    stateRoot.Name,
		StateServiceName: stateServiceName,
		GrpcServiceFQN:   grpcServiceFQN,
		TraceFQN:         basePackage + ".types.sys.trace.Trace",
		IdentityManagerFQN: basePackage + ".chassis.service_im.IdentityManager",
		Imports:          imports,
		Singletons:       singletons,
		Collections:      collections,
		Topics:           topics,
		StateImports:     stateImports,
		Commands:         commands,
		Views:            views,
		ReadOnly:         accessMode == tactype.AccessRO || (config.HealthStateName != "" && stateRoot.Name == config.HealthStateName),
		WriteOnce:        accessMode == tactype.AccessWORM,
	}

	if err := executeJavaTemplate("statewrapper.java.tmpl", "java-statewrapper", javaPackage, "StateClient.java", config, data); err != nil {
		return nil, err
	}

	zap.L().Info("Generated Java state client wrapper",
		zap.String("service", serviceName),
		zap.String("stateRoot", stateRoot.Name))

	// Collect boundary info for aggregators/registry
	bds := boundaries.CollectMessageBoundaries(stateRoot, fileDesc)
	clientFQN := javaPackage + ".StateClient"

	return &javaGeneratedStateClient{
		PackageName:   serviceName,
		PascalName:    outerClass,
		FieldName:     strutil.ToLowerCamelCase(stateRoot.Name),
		StateRootName: stateRoot.Name,
		ClientFQN:     clientFQN,
		Group:         javaStateTopGroup(svc.ParentDir),
		Boundaries:    bds,
	}, nil
}

// ==================== Group aggregator ====================

// generateJavaGroupStateClients generates StateClients.java for a group (e.g. "services").
func generateJavaGroupStateClients(config *tactype.Config, group string, clients []javaGeneratedStateClient) error {
	basePackage := strings.TrimSuffix(config.JavaPackageBase, ".types")
	groupPkg := javaStatePathPackage(group)
	javaPackage := basePackage + "." + config.GetSmPrefix() + ".internal.state"
	if groupPkg != "" {
		javaPackage += "." + groupPkg
	}

	data := javaGroupStateClientsData{
		Header:  header.GeneratedHeader(consts.LangJava, "state client aggregator ("+group+")"),
		Package: javaPackage,
		Group:   group,
		Clients: clients,
	}

	if err := executeJavaTemplate("group_state_clients.java.tmpl", "java-group-state", javaPackage, "StateClients.java", config, data); err != nil {
		return err
	}

	zap.L().Info("Generated Java group StateClients", zap.String("group", group))
	return nil
}

// ==================== Root aggregator ====================

// generateJavaRootStateClients generates the root StateClients.java and per-group aggregators.
func generateJavaRootStateClients(config *tactype.Config, clients []javaGeneratedStateClient) error {
	basePackage := strings.TrimSuffix(config.JavaPackageBase, ".types")

	// Group clients by top-level proto directory
	groups := make(map[string][]javaGeneratedStateClient)
	for _, c := range clients {
		groups[c.Group] = append(groups[c.Group], c)
	}

	// Generate per-group aggregator files
	for group, groupClients := range groups {
		if err := generateJavaGroupStateClients(config, group, groupClients); err != nil {
			return fmt.Errorf("failed to generate %s group state clients: %w", group, err)
		}
	}

	// Build group list for root
	var groupList []javaStateClientGroupInfo
	for group := range groups {
		groupPkg := javaStatePathPackage(group)
		fqn := basePackage + "." + config.GetSmPrefix() + ".internal.state"
		if groupPkg != "" {
			fqn += "." + groupPkg
		}
		groupList = append(groupList, javaStateClientGroupInfo{
			Name:      group,
			FieldName: strutil.ToLowerCamelCase(group),
			FQN:       fqn + ".StateClients",
		})
	}
	sort.Slice(groupList, func(i, j int) bool {
		return groupList[i].Name < groupList[j].Name
	})

	rootPackage := basePackage + "." + config.GetSmPrefix() + ".internal.state"
	data := javaRootStateClientsData{
		Header:  header.GeneratedHeader(consts.LangJava, "state client root aggregator"),
		Package: rootPackage,
		Groups:  groupList,
	}

	if err := executeJavaTemplate("root_state_clients.java.tmpl", "java-root-state", rootPackage, "StateClients.java", config, data); err != nil {
		return err
	}

	zap.L().Info("Generated Java root StateClients", zap.Int("groups", len(groupList)))

	return nil
}

// ==================== Helpers ====================

func javaStatePathPackage(path string) string {
	normalized := filepath.ToSlash(path)
	if normalized == "" || normalized == "." {
		return ""
	}
	parts := strings.Split(normalized, "/")
	for i, part := range parts {
		parts[i] = strings.ReplaceAll(part, "-", "_")
	}
	return strings.Join(parts, ".")
}

func javaStateTopGroup(path string) string {
	normalized := filepath.ToSlash(path)
	if normalized == "" || normalized == "." {
		return ""
	}
	group := strings.Split(normalized, "/")[0]
	return strings.ReplaceAll(group, "-", "_")
}

func uniqueJavaStateImports(imports []string) []string {
	seen := make(map[string]bool, len(imports))
	var unique []string
	for _, imp := range imports {
		if imp == "" || seen[imp] {
			continue
		}
		seen[imp] = true
		unique = append(unique, imp)
	}
	sort.Strings(unique)
	return unique
}

// executeJavaTemplate loads a template, executes it, and writes to the correct Java output path.
func executeJavaTemplate(tmplFile, tmplName, javaPackage, fileName string, config *tactype.Config, data interface{}) error {
	tmplContent, err := loadTemplate(tmplFile)
	if err != nil {
		return fmt.Errorf("failed to load template %s: %w", tmplFile, err)
	}

	tmpl, err := template.New(tmplName).Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse template %s: %w", tmplFile, err)
	}

	packagePath := strings.ReplaceAll(javaPackage, ".", string(filepath.Separator))
	outputDir := filepath.Join(config.OutputDir, "src", "main", "java", packagePath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output dir: %w", err)
	}

	outputPath := filepath.Join(outputDir, fileName)
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", outputPath, err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, data); err != nil {
		return fmt.Errorf("failed to execute template %s: %w", tmplFile, err)
	}

	return nil
}

// resolveCrossOuterClass determines the Java outer class name for a cross-package proto.
// Uses ToCamelCase to handle underscores and hyphens, then checks for collision.
func resolveCrossOuterClass(dp *parser.DescriptorParser, pkg string) string {
	outerClass := strutil.ToCamelCase(pkg)
	for relPath, fd := range dp.GetAllFiles() {
		_ = relPath
		if fd.Package == pkg {
			for _, msg := range fd.Messages {
				if msg.Name == outerClass {
					return outerClass + "OuterClass"
				}
			}
			for _, svc := range fd.Services {
				if svc.Name == outerClass {
					return outerClass + "OuterClass"
				}
			}
			break
		}
	}
	return outerClass
}

// resolveProtoDir finds the top-level directory for a given proto package.
func resolveProtoDir(dp *parser.DescriptorParser, pkg string) string {
	for relPath, fd := range dp.GetAllFiles() {
		if fd.Package == pkg {
			if idx := strings.IndexByte(relPath, '/'); idx > 0 {
				return relPath[:idx]
			}
		}
	}
	return "sys"
}

func findJavaMessageInFile(fileDesc *parser.FileDescriptor, typeName string) *parser.MessageDescriptor {
	simple := extractSimpleTypeName(typeName)
	for i := range fileDesc.Messages {
		if fileDesc.Messages[i].Name == simple {
			return &fileDesc.Messages[i]
		}
	}
	return nil
}
