// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/boundaries"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/header"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/consts"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
)

// tsSingletonBoundary describes a non-repeated message field in the state root.
type tsSingletonBoundary struct {
	FieldName   string // camelCase field name: "roster"
	FieldPascal string // PascalCase: "Roster"
	TypeName    string // PascalCase message type: "TeamRoster"
	TypePrefix  string // import alias: "types" (own package) or "x509Types" (cross-package)
	// DefaultsFn is the generated proto-defaults function for TypeName:
	// "teamRosterDefaults". writeDefaults emits one per message
	// unconditionally, so it is always callable through TypePrefix.
	DefaultsFn string
}

// tsCollectionBoundary describes a repeated message field in the state root.
type tsCollectionBoundary struct {
	FieldName    string // camelCase: "players"
	FieldPascal  string // PascalCase: "Players"
	TypeName     string // PascalCase item type: "Player"
	TypePrefix   string // import alias: "types" (own package) or "x509Types" (cross-package)
	KeyFieldName string // camelCase key field: "playerId"

	// MaxEntries is the client-side snapshot safety limit: the field's
	// (spec.eviction_max_size) when declared, else the shared default.
	MaxEntries int64
}

// tsStateImportField describes a field with state_import annotation.
type tsStateImportField struct {
	FieldName     string // camelCase: "baseballState"
	FieldPascal   string // PascalCase: "BaseballState"
	TypeName      string // PascalCase: "BaseballState"
	SourcePackage string // source service name: "baseball"
	ImportAlias   string // which import to use: "types" for own, "baseballTypes" for cross-service
}

// tsCommandBoundary describes a command RPC field in the state root.
type tsCommandBoundary struct {
	FieldName   string // camelCase: "reloadConfig"
	FieldPascal string // PascalCase: "ReloadConfig"
	TypeName    string // PascalCase message type: "ReloadConfigRequest"
}

// tsCrossImport describes an import from another service's types package.
type tsCrossImport struct {
	Alias      string // import alias: "baseballTypes"
	ImportPath string // relative path: "../../../baseball/baseball/types/index.js"
}

// tsStateWrapperData is the template data for generating the TS state wrapper.
type tsStateWrapperData struct {
	Header               string
	TypesImport          string // relative import path for own types: "../../baseball/types/index.js"
	TraceImport          string // relative import path to sys/trace/index.js
	ServiceManagerImport string // relative import path to chassis/servicemanager/index.js
	ProtoPackage         string // proto package name used in gRPC method paths
	StateRootName        string // PascalCase: "BaseballState"
	StateServiceName     string // PascalCase: "BaseballStateService"
	Singletons           []tsSingletonBoundary
	Collections          []tsCollectionBoundary
	StateImports         []tsStateImportField
	Commands             []tsCommandBoundary
	CrossImports         []tsCrossImport
	ReadOnly             bool // true for :ro (accessors only, no mutators)
	WriteOnce            bool // true for :worm (write-once read-many: set/add but no lock/delete)
}

// tsGeneratedStateClient tracks a generated state client for aggregator/registry generation.
type tsGeneratedStateClient struct {
	PackageName   string // e.g. "baseball"
	DirName       string // snake_case directory name derived from state root: "ca", "cluster"
	StateRootName string // PascalCase state root: "Ca", "Cluster"
	Group         string // e.g. "services"
	Boundaries    []boundaries.BoundaryData
}

// tsClientHasSingletons reports whether a generated state client exposes any
// singleton boundary — only those carry the fetch validate-retry bound.
func tsClientHasSingletons(c tsGeneratedStateClient) bool {
	for _, b := range c.Boundaries {
		if b.GraphType == "singleton" {
			return true
		}
	}
	return false
}

// generateStateClientWrappers generates typed state client wrappers for TypeScript.
// It scans inputDir independently for service_state annotations rather than relying
// on detectServicesForWrapper, which skips proto files without gRPC service definitions.
func generateStateClientWrappers(config *tactype.Config, inputDir string) error {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	generated := make(map[string]bool)
	var clients []tsGeneratedStateClient

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
			if serviceName, _ := proto.ServiceProtoParts(relPath); serviceName != "" {
				packageName = serviceName
			}

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

			boundaries, err := generateTSStateWrapper(config, svc, fileDesc, &fileDesc.Messages[i], stateRootDirName, accessMode)
			if err != nil {
				return fmt.Errorf("failed to generate TS state wrapper for %s: %w", stateRootAnnotation, err)
			}

			clients = append(clients, tsGeneratedStateClient{
				PackageName:   packageName,
				DirName:       stateRootDirName,
				StateRootName: stateRootAnnotation,
				Group:         parentDir,
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

	// Generate registry.ts
	if err := generateTSRegistry(config, clients); err != nil {
		return fmt.Errorf("failed to generate TypeScript registry: %w", err)
	}

	// Generate state client aggregators (group + root)
	if err := generateTSStateClientAggregators(config, clients); err != nil {
		return fmt.Errorf("failed to generate TypeScript state client aggregators: %w", err)
	}

	return nil
}

func generateTSStateWrapper(config *tactype.Config, svc ServiceInfo, fileDesc *parser.FileDescriptor, stateRoot *parser.MessageDescriptor, stateRootDirName string, accessMode tactype.AccessMode) ([]boundaries.BoundaryData, error) {
	// Determine the actual service name (for types import — types are per-package).
	serviceName := tsActualServicePackage(svc)

	// Compute relative import path from wrapper dir to own types.
	// Output directory uses stateRootDirName (per state root), but types stay per-package.
	wrapperDir := filepath.Join(config.GetSmPrefix(), "internal", "state", svc.ParentDir, stateRootDirName)
	ownTypesDir := typescriptModuleDirForProtoDescriptor("", svc.ProtoFile, fileDesc)
	ownTypesRel, _ := filepath.Rel(wrapperDir, ownTypesDir)
	traceRel, _ := filepath.Rel(wrapperDir, filepath.Join("types", "sys", "trace"))
	serviceManagerRel, _ := filepath.Rel(wrapperDir, filepath.Join("chassis", "servicemanager"))

	var singletons []tsSingletonBoundary
	var collections []tsCollectionBoundary
	var stateImports []tsStateImportField
	var commands []tsCommandBoundary
	crossImportMap := make(map[string]tsCrossImport)

	for _, field := range stateRoot.Fields {
		// Handle state_import fields
		if importAnn, ok := field.Annotations["state_import"]; ok && importAnn.StringValue != "" {
			typeName := extractTSSimpleTypeName(field.Type)
			sourcePackage := importAnn.StringValue
			if idx := strings.Index(sourcePackage, "."); idx > 0 {
				sourcePackage = sourcePackage[:idx]
			}
			importAlias := "types"
			if sourcePackage != serviceName {
				importAlias = sourcePackage + "Types"
				if _, exists := crossImportMap[sourcePackage]; !exists {
					crossTypesDir := typescriptModuleDirForPackageOrDir(config.OutputDir, sourcePackage, "services")
					crossTypesRel, _ := filepath.Rel(wrapperDir, crossTypesDir)
					crossImportMap[sourcePackage] = tsCrossImport{
						Alias:      importAlias,
						ImportPath: filepath.ToSlash(crossTypesRel) + "/index.js",
					}
				}
			}
			stateImports = append(stateImports, tsStateImportField{
				FieldName:     strutil.ToLowerCamelCase(field.Name),
				FieldPascal:   strutil.ToPascalCase(field.Name),
				TypeName:      typeName,
				SourcePackage: sourcePackage,
				ImportAlias:   importAlias,
			})
			continue
		}

		// Skip private boundaries (cross-package types)
		if privAnn, ok := field.Annotations["private"]; ok && privAnn.BoolValue {
			continue
		}

		// Command: explicit command RPC (skip normal boundary classification)
		if cmdAnn, ok := field.Annotations["command"]; ok && cmdAnn.BoolValue {
			typeName := extractTSSimpleTypeName(field.Type)
			fieldPascal := strutil.ToPascalCase(field.Name)
			commands = append(commands, tsCommandBoundary{
				FieldName:   strutil.ToLowerCamelCase(field.Name),
				FieldPascal: fieldPascal,
				TypeName:    typeName,
			})
			continue
		}

		if field.TypeKind != "message" {
			continue
		}

		// Skip map fields with scalar/enum values: their map-entry type is not
		// a real generated type in TypeScript, so referencing it causes errors.
		if field.IsMap && field.MapValueTypeKind != "message" {
			continue
		}

		typeName := extractTSSimpleTypeName(field.Type)
		fieldPascal := strutil.ToPascalCase(field.Name)

		// Detect cross-package types (e.g., x509.X509Cert in another package)
		typePrefix := "types"
		if parts := strings.Split(field.Type, "."); len(parts) > 1 {
			fieldPkg := strings.Join(parts[:len(parts)-1], ".")
			if fieldPkg != fileDesc.Package {
				importAlias := fieldPkg + "Types"
				typePrefix = importAlias
				if _, exists := crossImportMap[fieldPkg]; !exists {
					crossDir := typescriptModuleDirForPackageOrDir(config.OutputDir, fieldPkg, "")
					crossRel, _ := filepath.Rel(wrapperDir, crossDir)
					crossImportMap[fieldPkg] = tsCrossImport{
						Alias:      importAlias,
						ImportPath: filepath.ToSlash(crossRel) + "/index.js",
					}
				}
			}
		}

		if field.IsRepeated {
			boundaryMsg := findTSMessageInFile(fileDesc, field.Type)
			keyFieldName := ""
			if boundaryMsg != nil {
				for _, f := range boundaryMsg.Fields {
					if ann, ok := f.Annotations["indexed"]; ok && ann.BoolValue {
						keyFieldName = strutil.ToLowerCamelCase(f.Name)
						break
					}
				}
			}
			collections = append(collections, tsCollectionBoundary{
				FieldName:    strutil.ToLowerCamelCase(field.Name),
				FieldPascal:  fieldPascal,
				TypeName:     typeName,
				TypePrefix:   typePrefix,
				KeyFieldName: keyFieldName,

				MaxEntries: boundaries.CollectionSafetyLimit(field),
			})
		} else {
			singletons = append(singletons, tsSingletonBoundary{
				FieldName:   strutil.ToLowerCamelCase(field.Name),
				FieldPascal: fieldPascal,
				TypeName:    typeName,
				TypePrefix:  typePrefix,
				DefaultsFn:  strutil.LcFirst(typeName) + "Defaults",
			})
		}
	}

	// Collect cross imports. Sort for reproducible output — crossImportMap is a
	// Go map, so ranging it directly emits the import block in random order and
	// byte-diffs across otherwise-identical runs (every other collection in this
	// file is likewise sorted).
	var crossImports []tsCrossImport
	for _, ci := range crossImportMap {
		crossImports = append(crossImports, ci)
	}
	sort.Slice(crossImports, func(i, j int) bool { return crossImports[i].Alias < crossImports[j].Alias })

	data := tsStateWrapperData{
		Header:               header.GeneratedHeader(consts.LangTypeScript, svc.ProtoFile),
		TypesImport:          filepath.ToSlash(ownTypesRel) + "/index.js",
		TraceImport:          filepath.ToSlash(traceRel) + "/index.js",
		ServiceManagerImport: filepath.ToSlash(serviceManagerRel) + "/index.js",
		ProtoPackage:         fileDesc.Package,
		StateRootName:        stateRoot.Name,
		StateServiceName:     proto.StateServiceName(stateRoot.Name),
		Singletons:           singletons,
		Collections:          collections,
		StateImports:         stateImports,
		Commands:             commands,
		CrossImports:         crossImports,
		ReadOnly:             accessMode == tactype.AccessRO,
		WriteOnce:            accessMode == tactype.AccessWORM,
	}

	tmplContent, err := loadTemplate("statewrapper.ts.tmpl")
	if err != nil {
		return nil, fmt.Errorf("failed to load statewrapper template: %w", err)
	}

	tmpl, err := template.New("ts-statewrapper").Parse(tmplContent)
	if err != nil {
		return nil, fmt.Errorf("failed to parse template: %w", err)
	}

	outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "internal", "state", svc.ParentDir, stateRootDirName)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output dir: %w", err)
	}

	outputPath := filepath.Join(outputDir, "state.ts")
	file, err := os.Create(outputPath)
	if err != nil {
		return nil, fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, data); err != nil {
		return nil, fmt.Errorf("failed to execute template: %w", err)
	}

	zap.L().Info("Generated TypeScript state client wrapper",
		zap.String("path", outputPath),
		zap.String("stateRoot", stateRoot.Name))

	registryBoundaries := boundaries.CollectMessageBoundaries(stateRoot, fileDesc)
	return registryBoundaries, nil
}

// tsRegistryClientInfo is the template data for a service in registry.ts.
type tsRegistryClientInfo struct {
	PackageName string
	Group       string
	Boundaries  []boundaries.BoundaryData
}

// generateTSRegistry generates sm/internal/state/registry.ts
func generateTSRegistry(config *tactype.Config, clients []tsGeneratedStateClient) error {
	tmplContent, err := loadTemplate("registry.ts.tmpl")
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
		"camelCase": strutil.ToLowerCamelCase,
	}

	tmpl, err := template.New("ts-registry").Funcs(funcMap).Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse registry template: %w", err)
	}

	var allClients []tsRegistryClientInfo
	for _, c := range clients {
		allClients = append(allClients, tsRegistryClientInfo{
			PackageName: c.PackageName,
			Group:       c.Group,
			Boundaries:  c.Boundaries,
		})
	}

	data := struct {
		Header  string
		Clients []tsRegistryClientInfo
	}{
		Header:  header.GeneratedHeader(consts.LangTypeScript, "state client registry"),
		Clients: allClients,
	}

	outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "internal", "state")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output dir: %w", err)
	}

	outputPath := filepath.Join(outputDir, "registry.ts")
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, data); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	zap.L().Info("Generated TypeScript registry", zap.String("path", outputPath))
	return nil
}

// generateTSStateClientAggregators creates group and root StateClients classes for TypeScript.
func generateTSStateClientAggregators(config *tactype.Config, clients []tsGeneratedStateClient) error {
	smDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "internal", "state")

	// Group clients by their parent directory (group)
	groups := make(map[string][]tsGeneratedStateClient)
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
		groupDir := filepath.Join(smDir, group)
		serviceManagerRel, _ := filepath.Rel(groupDir, filepath.Join(config.OutputDir, "chassis", "servicemanager"))

		var buf strings.Builder
		buf.WriteString(header.GeneratedHeader(consts.LangTypeScript, "state client aggregator ("+group+")"))
		buf.WriteString("\n")

		// Import individual state clients
		for _, c := range groupClients {
			className := c.StateRootName + "Client"
			buf.WriteString(fmt.Sprintf("import { %s } from './%s/state.js';\n", className, c.DirName))
		}
		buf.WriteString(fmt.Sprintf("import { type ChannelProvider } from '%s/index.js';\n", filepath.ToSlash(serviceManagerRel)))
		buf.WriteString("\n")

		// Class definition
		buf.WriteString(fmt.Sprintf("/** Aggregates all state client wrappers in the %s group. */\n", group))
		buf.WriteString("export class StateClients {\n")

		// Private fields
		for _, c := range groupClients {
			fieldName := strutil.ToLowerCamelCase(c.StateRootName)
			className := c.StateRootName + "Client"
			buf.WriteString(fmt.Sprintf("  private readonly _%s = new %s();\n", fieldName, className))
		}
		buf.WriteString("\n")

		buf.WriteString("  setChannelProvider(channelProvider: ChannelProvider, tokenProvider?: () => Promise<string>): void {\n")
		for _, c := range groupClients {
			fieldName := strutil.ToLowerCamelCase(c.StateRootName)
			buf.WriteString(fmt.Sprintf("    this._%s.setChannelProvider(channelProvider, tokenProvider);\n", fieldName))
		}
		buf.WriteString("  }\n\n")

		buf.WriteString("  /** Sets the singleton-fetch validate-retry bound on every state client in this group. */\n")
		buf.WriteString("  setConfigFetchTimeoutSeconds(seconds: number): void {\n")
		for _, c := range groupClients {
			if !tsClientHasSingletons(c) {
				continue
			}
			fieldName := strutil.ToLowerCamelCase(c.StateRootName)
			buf.WriteString(fmt.Sprintf("    this._%s.setConfigFetchTimeoutSeconds(seconds);\n", fieldName))
		}
		buf.WriteString("  }\n\n")

		buf.WriteString("  close(): void {\n")
		for _, c := range groupClients {
			fieldName := strutil.ToLowerCamelCase(c.StateRootName)
			buf.WriteString(fmt.Sprintf("    this._%s.close();\n", fieldName))
		}
		buf.WriteString("  }\n\n")

		// Accessor methods
		for _, c := range groupClients {
			fieldName := strutil.ToLowerCamelCase(c.StateRootName)
			className := c.StateRootName + "Client"
			buf.WriteString(fmt.Sprintf("  /** Returns the %s state client. */\n", c.StateRootName))
			buf.WriteString(fmt.Sprintf("  %s(): %s { return this._%s; }\n\n", fieldName, className, fieldName))
		}

		buf.WriteString("}\n")

		if err := os.MkdirAll(groupDir, 0755); err != nil {
			return fmt.Errorf("failed to create group directory: %w", err)
		}
		if err := os.WriteFile(filepath.Join(groupDir, "state_clients.ts"), []byte(buf.String()), 0644); err != nil {
			return fmt.Errorf("failed to write group state clients: %w", err)
		}

		zap.L().Info("Generated TypeScript group StateClients", zap.String("group", group))
	}

	// Generate root aggregator
	var buf strings.Builder
	buf.WriteString(header.GeneratedHeader(consts.LangTypeScript, "state client root aggregator"))
	buf.WriteString("\n")
	serviceManagerRel, _ := filepath.Rel(smDir, filepath.Join(config.OutputDir, "chassis", "servicemanager"))

	// Import group aggregators
	for _, group := range groupOrder {
		groupPascal := strutil.ToPascalCase(group)
		buf.WriteString(fmt.Sprintf("import { StateClients as %sStateClients } from './%s/state_clients.js';\n", groupPascal, group))
	}
	buf.WriteString(fmt.Sprintf("import { type ChannelProvider } from '%s/index.js';\n", filepath.ToSlash(serviceManagerRel)))
	buf.WriteString("\n")

	// Root class
	buf.WriteString("/** Root aggregator for all state client wrappers. Delegates to per-group StateClients. */\n")
	buf.WriteString("export class StateClients {\n")

	// Private fields
	for _, group := range groupOrder {
		groupPascal := strutil.ToPascalCase(group)
		buf.WriteString(fmt.Sprintf("  private readonly _%s = new %sStateClients();\n", group, groupPascal))
	}
	buf.WriteString("\n")

	buf.WriteString("  setChannelProvider(channelProvider: ChannelProvider, tokenProvider?: () => Promise<string>): void {\n")
	for _, group := range groupOrder {
		buf.WriteString(fmt.Sprintf("    this._%s.setChannelProvider(channelProvider, tokenProvider);\n", group))
	}
	buf.WriteString("  }\n\n")

	buf.WriteString("  /** Sets the singleton-fetch validate-retry bound on every state client group. */\n")
	buf.WriteString("  setConfigFetchTimeoutSeconds(seconds: number): void {\n")
	for _, group := range groupOrder {
		buf.WriteString(fmt.Sprintf("    this._%s.setConfigFetchTimeoutSeconds(seconds);\n", group))
	}
	buf.WriteString("  }\n\n")

	buf.WriteString("  close(): void {\n")
	for _, group := range groupOrder {
		buf.WriteString(fmt.Sprintf("    this._%s.close();\n", group))
	}
	buf.WriteString("  }\n\n")

	// Accessor methods
	for _, group := range groupOrder {
		groupPascal := strutil.ToPascalCase(group)
		buf.WriteString(fmt.Sprintf("  /** Returns the %s group's state clients. */\n", group))
		buf.WriteString(fmt.Sprintf("  %s(): %sStateClients { return this._%s; }\n\n", group, groupPascal, group))
	}

	buf.WriteString("}\n")

	if err := os.WriteFile(filepath.Join(smDir, "state_clients.ts"), []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write root state clients: %w", err)
	}

	zap.L().Info("Generated TypeScript root StateClients", zap.Int("groups", len(groupOrder)))
	return nil
}

func extractTSSimpleTypeName(fullType string) string {
	parts := strings.Split(fullType, ".")
	return parts[len(parts)-1]
}

func findTSMessageInFile(fileDesc *parser.FileDescriptor, typeName string) *parser.MessageDescriptor {
	simple := extractTSSimpleTypeName(typeName)
	for i := range fileDesc.Messages {
		if fileDesc.Messages[i].Name == simple {
			return &fileDesc.Messages[i]
		}
	}
	return nil
}
