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

// pySingletonBoundary describes a non-repeated message field in the state root.
type pySingletonBoundary struct {
	FieldName      string // escaped snake_case wrapper attribute: "roster"
	ProtoFieldName string // raw snake_case proto field: "roster"
	FieldPascal    string // PascalCase: "Roster"
	TypeName       string // PascalCase message type: "TeamRoster"
	ValidateRef    string // generated validator call: "pb_helpers.validate_team_roster"
	IsPersisted    bool
}

// pyCollectionBoundary describes a repeated message field in the state root.
type pyCollectionBoundary struct {
	FieldName      string // escaped snake_case wrapper attribute: "players"
	ProtoFieldName string // raw snake_case proto field: "players"
	FieldPascal    string // PascalCase: "Players"
	TypeName       string // PascalCase item type: "Player"
	KeyFieldName   string // snake_case key field: "player_id"
	IsPersisted    bool

	// MaxEntries is the client-side snapshot safety limit: the field's
	// (spec.eviction_max_size) when declared, else the shared default.
	MaxEntries int64
}

// pyStateImportField describes a field with state_import annotation.
type pyStateImportField struct {
	FieldName      string // escaped snake_case wrapper attribute: "football_team_roster"
	ProtoFieldName string // raw snake_case proto field: "football_team_roster"
	FieldPascal    string // PascalCase: "FootballTeamRoster"
	TypeName       string // PascalCase: "TeamRoster"
}

// pyCommandBoundary describes a command RPC field in the state root.
type pyCommandBoundary struct {
	FieldName   string // snake_case: "reload_config"
	FieldPascal string // PascalCase: "ReloadConfig"
	TypeName    string // PascalCase message type: "ReloadConfigRequest"
}

// pyViewBoundary describes a read-only view over multiple source collections.
type pyViewBoundary struct {
	FieldName    string   // snake_case: "all_keys"
	FieldPascal  string   // PascalCase: "AllKeys"
	TypeName     string   // PascalCase item type: "ManagedJwk"
	KeyFieldName string   // snake_case key field: "kid"
	ViewSources  []string // PascalCase source names: ["ConfigKeys", "DynamicKeys", "ExternalKeys"]
}

// pyStateWrapperData is the template data for generating the Python state wrapper.
type pyStateWrapperData struct {
	Header          string
	PackageName     string // e.g., "baseball" — used for pb2 module import
	PbImportFrom    string // computed relative import path to pb2 module
	TraceImportFrom string // computed relative import path to trace pb2 module
	// ExceptionsImportFrom is the relative import base for the shared
	// exceptions module, so the outbound auth seam can raise a structured
	// UNAUTHENTICATED instead of a bare RuntimeError.
	ExceptionsImportFrom string
	StateRootName        string
	StateServiceName     string
	Singletons           []pySingletonBoundary
	Collections          []pyCollectionBoundary
	StateImports         []pyStateImportField
	Commands             []pyCommandBoundary
	Views                []pyViewBoundary
	ReadOnly             bool // true for :ro (accessors only, no mutators)
	WriteOnce            bool // true for :worm (write-once read-many: set/add but no lock/delete)
}

// pyGeneratedStateClient tracks a generated state client for aggregator/registry generation.
type pyGeneratedStateClient struct {
	PackageName   string // e.g. "baseball"
	DirName       string // snake_case directory name derived from state root: "ca", "cluster"
	StateRootName string // PascalCase state root: "Ca", "Cluster"
	Group         string // e.g. "services"
	Boundaries    []boundaries.BoundaryData
}

// pyClientHasSingletons reports whether a generated state client exposes any
// singleton boundary — only those carry the fetch validate-retry bound.
func pyClientHasSingletons(c pyGeneratedStateClient) bool {
	for _, b := range c.Boundaries {
		if b.GraphType == "singleton" {
			return true
		}
	}
	return false
}

// generatePyStateClientWrappers generates typed state client wrappers for Python.
// It scans inputDir independently for service_state annotations rather than relying
// on detectServicesForWrapper, which skips proto files without gRPC service definitions.
func generatePyStateClientWrappers(config *tactype.Config, inputDir string) error {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	generated := make(map[string]bool)
	var clients []pyGeneratedStateClient

	err = filepath.Walk(inputDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}

		relPath, _ := filepath.Rel(inputDir, path)
		fileDesc, err := dp.GetFileDescriptor(relPath)
		if err != nil {
			return nil
		}
		if proto.IsInfraProtoFile(relPath, fileDesc) {
			// Exception: the manifest-declared runtime config state proto is an
			// application-level state boundary for runtime config delivery.
			if !proto.IsRuntimeConfigStateProto(relPath) {
				return nil
			}
		}

		for i := range fileDesc.Messages {
			ann, ok := fileDesc.Messages[i].Annotations["service_state"]
			if !ok || ann.StringValue == "" {
				continue
			}

			// Determine package name and parent dir
			packageName := fileDesc.Package
			if idx := strings.Index(packageName, "."); idx > 0 {
				packageName = packageName[:idx]
			}
			parentDir := filepath.Dir(relPath)

			stateRootAnnotation := ann.StringValue
			accessMode, shouldGenerate := config.AccessModeForState(relPath, stateRootAnnotation)
			if !shouldGenerate {
				continue
			}
			stateRootDirName := strutil.ToSnakeCase(stateRootAnnotation)

			key := packageName + "." + stateRootAnnotation
			if generated[key] {
				continue
			}
			generated[key] = true

			svc := ServiceInfo{
				ProtoFile:   relPath,
				PackageName: packageName,
				ParentDir:   parentDir,
			}

			boundaries, err := generatePyStateWrapper(config, svc, fileDesc, &fileDesc.Messages[i], dp, stateRootDirName, accessMode)
			if err != nil {
				return fmt.Errorf("failed to generate Python state client wrapper for %s: %w", stateRootAnnotation, err)
			}

			clients = append(clients, pyGeneratedStateClient{
				PackageName:   pyModuleName(packageName),
				DirName:       stateRootDirName,
				StateRootName: stateRootAnnotation,
				Group:         pyPath(parentDir),
				Boundaries:    boundaries,
			})
		}
		return nil
	})
	if err != nil {
		return err
	}

	if len(clients) == 0 {
		return nil
	}

	// Sort clients for deterministic output
	sort.Slice(clients, func(i, j int) bool {
		if clients[i].Group != clients[j].Group {
			return clients[i].Group < clients[j].Group
		}
		return clients[i].PackageName < clients[j].PackageName
	})

	// Generate registry.py
	if err := generatePyRegistry(config, clients); err != nil {
		return fmt.Errorf("failed to generate Python registry: %w", err)
	}

	// Generate state client aggregators (group + root)
	if err := generatePyStateClientAggregators(config, clients); err != nil {
		return fmt.Errorf("failed to generate Python state client aggregators: %w", err)
	}

	return nil
}

func generatePyStateWrapper(config *tactype.Config, svc ServiceInfo, fileDesc *parser.FileDescriptor, stateRoot *parser.MessageDescriptor, _dp *parser.DescriptorParser, stateRootDirName string, accessMode tactype.AccessMode) ([]boundaries.BoundaryData, error) {
	var singletons []pySingletonBoundary
	var collections []pyCollectionBoundary
	var stateImports []pyStateImportField
	var commands []pyCommandBoundary
	var views []pyViewBoundary
	for _, field := range stateRoot.Fields {
		if importAnn, ok := field.Annotations["state_import"]; ok && importAnn.StringValue != "" {
			typeName := extractPySimpleTypeName(field.Type)
			stateImports = append(stateImports, pyStateImportField{
				FieldName:      escapePyKeyword(strutil.ToSnakeCase(field.Name)),
				ProtoFieldName: strutil.ToSnakeCase(field.Name),
				FieldPascal:    strutil.ToPascalCase(field.Name),
				TypeName:       typeName,
			})
			continue
		}

		// Skip private boundaries (cross-package types)
		if privAnn, ok := field.Annotations["private"]; ok && privAnn.BoolValue {
			continue
		}

		// View: read-only union over source collections
		if vsAnn, ok := field.Annotations["view_sources"]; ok && len(vsAnn.StringSliceValue) > 0 {
			typeName := extractPySimpleTypeName(field.Type)
			// Find indexed key field
			keyFieldName := ""
			boundaryMsg := findPyMessageInFile(fileDesc, field.Type)
			if boundaryMsg != nil {
				for _, f := range boundaryMsg.Fields {
					if ann, ok := f.Annotations["indexed"]; ok && ann.BoolValue {
						keyFieldName = f.Name
						break
					}
				}
			}
			// Convert source field names to PascalCase
			var sourcePascal []string
			for _, src := range vsAnn.StringSliceValue {
				sourcePascal = append(sourcePascal, strutil.ToPascalCase(src))
			}
			views = append(views, pyViewBoundary{
				FieldName:    escapePyKeyword(strutil.ToSnakeCase(field.Name)),
				FieldPascal:  strutil.ToPascalCase(field.Name),
				TypeName:     typeName,
				KeyFieldName: strutil.ToSnakeCase(keyFieldName),
				ViewSources:  sourcePascal,
			})
			continue
		}

		// Command: explicit command RPC (skip normal boundary classification)
		if cmdAnn, ok := field.Annotations["command"]; ok && cmdAnn.BoolValue {
			typeName := extractPySimpleTypeName(field.Type)
			fieldPascal := strutil.ToPascalCase(field.Name)
			commands = append(commands, pyCommandBoundary{
				FieldName:   escapePyKeyword(strutil.ToSnakeCase(field.Name)),
				FieldPascal: fieldPascal,
				TypeName:    typeName,
			})
			continue
		}

		if field.TypeKind != "message" {
			continue
		}

		// Skip map fields with scalar/enum values: their map-entry type is not
		// a real generated type in Python, so referencing it causes errors.
		if field.IsMap && field.MapValueTypeKind != "message" {
			continue
		}

		typeName := extractPySimpleTypeName(field.Type)
		fieldPascal := strutil.ToPascalCase(field.Name)

		if field.IsRepeated {
			boundaryMsg := findPyMessageInFile(fileDesc, field.Type)
			keyFieldName := ""
			if boundaryMsg != nil {
				for _, f := range boundaryMsg.Fields {
					if ann, ok := f.Annotations["indexed"]; ok && ann.BoolValue {
						keyFieldName = f.Name
						break
					}
				}
			}
			collections = append(collections, pyCollectionBoundary{
				FieldName:      escapePyKeyword(strutil.ToSnakeCase(field.Name)),
				ProtoFieldName: strutil.ToSnakeCase(field.Name),
				FieldPascal:    fieldPascal,
				TypeName:       typeName,
				KeyFieldName:   strutil.ToSnakeCase(keyFieldName),
				IsPersisted:    field.Annotations["persistence"].BoolValue,

				MaxEntries: boundaries.CollectionSafetyLimit(field),
			})
		} else {
			singletons = append(singletons, pySingletonBoundary{
				FieldName:      escapePyKeyword(strutil.ToSnakeCase(field.Name)),
				ProtoFieldName: strutil.ToSnakeCase(field.Name),
				FieldPascal:    fieldPascal,
				TypeName:       typeName,
				ValidateRef:    "pb_helpers.validate_" + strutil.ToSnakeCase(typeName),
				IsPersisted:    field.Annotations["persistence"].BoolValue,
			})
		}
	}

	// Compute relative import path from sm/internal/state/{parentDir}/{stateRootDirName}/ to types.
	pbModuleName := pyProtoFileModuleName(svc.ProtoFile)
	parentParts := splitPyPath(svc.ParentDir)
	wrapperDepth := 3 + len(parentParts) + 1 // sm + internal + state + parentDir parts + stateRootDirName
	dots := strings.Repeat(".", wrapperDepth+1)
	pbImportFrom := dots + "types." + pyProtoImportPath(svc.ParentDir, svc.PackageName, svc.ProtoFile)
	traceImportFrom := dots + "types.sys.trace"

	data := pyStateWrapperData{
		Header:               header.GeneratedHeader(consts.LangPython, svc.ProtoFile),
		PackageName:          pbModuleName,
		PbImportFrom:         pbImportFrom,
		TraceImportFrom:      traceImportFrom,
		ExceptionsImportFrom: pyExceptionsImportFrom(dots),
		StateRootName:        stateRoot.Name,
		StateServiceName:     proto.StateServiceName(stateRoot.Name),
		Singletons:           singletons,
		Collections:          collections,
		StateImports:         stateImports,
		Commands:             commands,
		Views:                views,
		ReadOnly:             accessMode == tactype.AccessRO || (config.HealthStateName != "" && stateRoot.Name == config.HealthStateName),
		WriteOnce:            accessMode == tactype.AccessWORM,
	}

	tmplContent, err := loadTemplate("statewrapper.py.tmpl")
	if err != nil {
		return nil, fmt.Errorf("failed to load template: %w", err)
	}

	funcMap := template.FuncMap{
		"snakeCase": strutil.ToSnakeCase,
		"pyGet":     pyFieldGet,
	}
	tmpl, err := template.New("py-statewrapper").Funcs(funcMap).Parse(tmplContent)
	if err != nil {
		return nil, fmt.Errorf("failed to parse template: %w", err)
	}

	outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "internal", "state", pyPath(svc.ParentDir), stateRootDirName)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output dir: %w", err)
	}

	// Write __init__.py
	initPath := filepath.Join(outputDir, "__init__.py")
	if err := os.WriteFile(initPath, []byte("# Auto-generated\n"), 0644); err != nil {
		return nil, fmt.Errorf("failed to create __init__.py: %w", err)
	}

	outputPath := filepath.Join(outputDir, "state.py")
	file, err := os.Create(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, data); err != nil {
		return nil, fmt.Errorf("failed to execute template: %w", err)
	}

	zap.L().Info("Generated Python state client wrapper",
		zap.String("path", outputPath),
		zap.String("stateRoot", stateRoot.Name))

	registryBoundaries := boundaries.CollectMessageBoundaries(stateRoot, fileDesc)
	return registryBoundaries, nil
}

// pyRegistryClientInfo is the template data for a service in registry.py.
type pyRegistryClientInfo struct {
	PackageName string
	Group       string
	Boundaries  []boundaries.BoundaryData
}

// generatePyRegistry generates sm/internal/state/registry.py
func generatePyRegistry(config *tactype.Config, clients []pyGeneratedStateClient) error {
	tmplContent, err := loadTemplate("registry.py.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load registry template: %w", err)
	}

	funcMap := template.FuncMap{
		"title": func(s string) string {
			if len(s) == 0 {
				return s
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
		"snakeCase": strutil.ToSnakeCase,
	}

	tmpl, err := template.New("py-registry").Funcs(funcMap).Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse registry template: %w", err)
	}

	var allClients []pyRegistryClientInfo
	for _, c := range clients {
		allClients = append(allClients, pyRegistryClientInfo{
			PackageName: c.PackageName,
			Group:       c.Group,
			Boundaries:  c.Boundaries,
		})
	}

	data := struct {
		Header  string
		Clients []pyRegistryClientInfo
	}{
		Header:  header.GeneratedHeader(consts.LangPython, "state client registry"),
		Clients: allClients,
	}

	outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "internal", "state")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output dir: %w", err)
	}

	outputPath := filepath.Join(outputDir, "registry.py")
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, data); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	zap.L().Info("Generated Python registry", zap.String("path", outputPath))
	return nil
}

// generatePyStateClientAggregators creates group and root StateClients classes for Python.
func generatePyStateClientAggregators(config *tactype.Config, clients []pyGeneratedStateClient) error {
	smDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "internal", "state")

	// Group clients by their parent directory (group)
	groups := make(map[string][]pyGeneratedStateClient)
	var groupOrder []string
	for _, c := range clients {
		if _, exists := groups[c.Group]; !exists {
			groupOrder = append(groupOrder, c.Group)
		}
		groups[c.Group] = append(groups[c.Group], c)
	}
	sort.Strings(groupOrder)

	// Generate per-group aggregators
	for _, group := range groupOrder {
		groupClients := groups[group]
		groupDir := filepath.Join(smDir, pyPath(group))

		var buf strings.Builder
		buf.WriteString(header.GeneratedHeader(consts.LangPython, "state client aggregator ("+group+")"))
		buf.WriteString("\n")
		buf.WriteString("from __future__ import annotations\n\n")

		// Import individual state clients
		for _, c := range groupClients {
			className := c.StateRootName + "Client"
			buf.WriteString(fmt.Sprintf("from .%s.state import %s\n", c.DirName, className))
		}
		buf.WriteString("\n\n")

		// Class definition
		buf.WriteString(fmt.Sprintf("class StateClients:\n"))
		buf.WriteString(fmt.Sprintf("    \"\"\"Aggregates all state client wrappers in the %s group.\"\"\"\n\n", group))
		buf.WriteString("    def __init__(self) -> None:\n")

		for _, c := range groupClients {
			fieldName := strutil.ToSnakeCase(c.StateRootName)
			className := c.StateRootName + "Client"
			buf.WriteString(fmt.Sprintf("        self._%s = %s()\n", fieldName, className))
		}
		buf.WriteString("\n")

		buf.WriteString("    def set_channel(self, channel, token_provider=None) -> None:\n")
		buf.WriteString("        \"\"\"Wires every state client to the shared state-store channel.\"\"\"\n")
		for _, c := range groupClients {
			fieldName := strutil.ToSnakeCase(c.StateRootName)
			buf.WriteString(fmt.Sprintf("        self._%s.set_channel(channel, token_provider)\n", fieldName))
		}
		buf.WriteString("\n")

		buf.WriteString("    def set_config_fetch_timeout_seconds(self, seconds: int) -> None:\n")
		buf.WriteString("        \"\"\"Sets the singleton-fetch validate-retry bound on every state client in this group.\"\"\"\n")
		for _, c := range groupClients {
			if !pyClientHasSingletons(c) {
				continue
			}
			fieldName := strutil.ToSnakeCase(c.StateRootName)
			buf.WriteString(fmt.Sprintf("        self._%s.set_config_fetch_timeout_seconds(seconds)\n", fieldName))
		}
		buf.WriteString("\n")

		buf.WriteString("    async def close(self) -> None:\n")
		buf.WriteString("        \"\"\"Stops all state watch tasks.\"\"\"\n")
		for _, c := range groupClients {
			fieldName := strutil.ToSnakeCase(c.StateRootName)
			buf.WriteString(fmt.Sprintf("        await self._%s.close()\n", fieldName))
		}
		buf.WriteString("\n")

		// Accessor methods
		for _, c := range groupClients {
			fieldName := strutil.ToSnakeCase(c.StateRootName)
			className := c.StateRootName + "Client"
			buf.WriteString(fmt.Sprintf("    def %s(self) -> %s:\n", fieldName, className))
			buf.WriteString(fmt.Sprintf("        \"\"\"Returns the %s state client.\"\"\"\n", c.StateRootName))
			buf.WriteString(fmt.Sprintf("        return self._%s\n\n", fieldName))
		}

		if err := os.MkdirAll(groupDir, 0755); err != nil {
			return fmt.Errorf("failed to create group directory: %w", err)
		}
		if err := os.WriteFile(filepath.Join(groupDir, "state_clients.py"), []byte(buf.String()), 0644); err != nil {
			return fmt.Errorf("failed to write group state clients: %w", err)
		}

		zap.L().Info("Generated Python group StateClients", zap.String("group", group))
	}

	// Generate root aggregator
	var buf strings.Builder
	buf.WriteString(header.GeneratedHeader(consts.LangPython, "state client root aggregator"))
	buf.WriteString("\n")
	buf.WriteString("from __future__ import annotations\n\n")

	// Import group aggregators
	for _, group := range groupOrder {
		buf.WriteString(fmt.Sprintf("from .%s.state_clients import StateClients as %sStateClients\n", pyDottedPath(group), strutil.ToPascalCase(group)))
	}
	buf.WriteString("\n\n")

	// Root class
	buf.WriteString("class StateClients:\n")
	buf.WriteString("    \"\"\"Root aggregator for all state client wrappers. Delegates to per-group StateClients.\"\"\"\n\n")
	buf.WriteString("    def __init__(self) -> None:\n")

	for _, group := range groupOrder {
		groupPascal := strutil.ToPascalCase(group)
		buf.WriteString(fmt.Sprintf("        self._%s = %sStateClients()\n", group, groupPascal))
	}
	buf.WriteString("\n")

	buf.WriteString("    def set_channel(self, channel, token_provider=None) -> None:\n")
	buf.WriteString("        \"\"\"Wires every state client to the shared state-store channel.\"\"\"\n")
	for _, group := range groupOrder {
		buf.WriteString(fmt.Sprintf("        self._%s.set_channel(channel, token_provider)\n", group))
	}
	buf.WriteString("\n")

	buf.WriteString("    def set_config_fetch_timeout_seconds(self, seconds: int) -> None:\n")
	buf.WriteString("        \"\"\"Sets the singleton-fetch validate-retry bound on every state client group.\"\"\"\n")
	for _, group := range groupOrder {
		buf.WriteString(fmt.Sprintf("        self._%s.set_config_fetch_timeout_seconds(seconds)\n", group))
	}
	buf.WriteString("\n")

	buf.WriteString("    async def close(self) -> None:\n")
	buf.WriteString("        \"\"\"Stops all state watch tasks.\"\"\"\n")
	for _, group := range groupOrder {
		buf.WriteString(fmt.Sprintf("        await self._%s.close()\n", group))
	}
	buf.WriteString("\n")

	// Accessor methods
	for _, group := range groupOrder {
		groupPascal := strutil.ToPascalCase(group)
		buf.WriteString(fmt.Sprintf("    def %s(self) -> %sStateClients:\n", group, groupPascal))
		buf.WriteString(fmt.Sprintf("        \"\"\"Returns the %s group's state clients.\"\"\"\n", group))
		buf.WriteString(fmt.Sprintf("        return self._%s\n\n", group))
	}

	if err := os.WriteFile(filepath.Join(smDir, "state_clients.py"), []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write root state clients: %w", err)
	}

	zap.L().Info("Generated Python root StateClients", zap.Int("groups", len(groupOrder)))
	return nil
}

func extractPySimpleTypeName(fullType string) string {
	parts := strings.Split(fullType, ".")
	return parts[len(parts)-1]
}

func findPyMessageInFile(fileDesc *parser.FileDescriptor, typeName string) *parser.MessageDescriptor {
	simple := extractPySimpleTypeName(typeName)
	for i := range fileDesc.Messages {
		if fileDesc.Messages[i].Name == simple {
			return &fileDesc.Messages[i]
		}
	}
	return nil
}
