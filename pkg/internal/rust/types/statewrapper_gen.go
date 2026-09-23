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

// rustSingletonBoundary describes a non-repeated message field in the state root.
type rustSingletonBoundary struct {
	FieldName   string // snake_case: "roster"
	FieldPascal string // PascalCase: "Roster"
	TypeName    string // PascalCase message type: "TeamRoster"
	// DefaultsFn is the generated proto-defaults function for TypeName:
	// "team_roster_defaults". writeDefaults emits one per message
	// unconditionally, so it is always callable through the glob type imports.
	DefaultsFn string
}

// rustCollectionBoundary describes a repeated message field in the state root.
type rustCollectionBoundary struct {
	FieldName    string // snake_case: "players"
	FieldPascal  string // PascalCase: "Players"
	TypeName     string // PascalCase item type: "Player"
	KeyFieldName string // snake_case key field: "player_id"

	// MaxEntries is the client-side snapshot safety limit: the field's
	// (spec.eviction_max_size) when declared, else the shared default.
	MaxEntries int64
}

// rustStateImportField describes a field with state_import annotation.
type rustStateImportField struct {
	FieldName string // snake_case: "football_team_roster"
	TypeName  string // PascalCase: "TeamRoster"
	CratePath string // Fully-qualified path: "crate::services::football::types::football::TeamRoster"
}

// rustCommandBoundary describes a command RPC field in the state root.
type rustCommandBoundary struct {
	FieldName   string // snake_case: "reload_config"
	FieldPascal string // PascalCase: "ReloadConfig"
	TypeName    string // PascalCase message type: "ReloadConfigRequest"
}

// rustViewBoundary describes a read-only view over multiple source collections.
type rustViewBoundary struct {
	FieldName    string   // snake_case: "all_keys"
	FieldPascal  string   // PascalCase: "AllKeys"
	TypeName     string   // PascalCase item type: "ManagedJwk"
	KeyFieldName string   // snake_case key field: "kid"
	ViewSources  []string // snake_case source names: ["config_keys", "dynamic_keys", "external_keys"]
}

// rustInlineField describes an unannotated message field (e.g., inline state reference).
type rustInlineField struct {
	FieldName string // snake_case: "state"
	TypeName  string // PascalCase: "BaseballState"
	CratePath string // Fully-qualified path: "crate::services::baseball::types::baseball::BaseballState"
}

// rustStateWrapperData is the template data for generating the Rust state wrapper.
type rustStateWrapperData struct {
	Header                 string
	StateRootName          string // PascalCase: "BaseballState"
	ModuleName             string // snake_case: "baseball_state"
	TypesCratePath         string // e.g., "services::baseball::baseball" - crate path to types module
	PackageName            string // snake_case proto package/module name: "baseball"
	StateServiceName       string // PascalCase: "BaseballStateService"
	StateServiceModule     string // snake_case tonic client module: "baseball_state_service_client"
	StateServiceClientType string // PascalCase tonic client type: "BaseballStateServiceClient"
	Singletons             []rustSingletonBoundary
	Collections            []rustCollectionBoundary
	StateImports           []rustStateImportField
	InlineFields           []rustInlineField
	Commands               []rustCommandBoundary
	Views                  []rustViewBoundary
	CrossPackageImports    string
	ReadOnly               bool // true for :ro (accessors only, no mutators)
	WriteOnce              bool // true for :worm (write-once read-many: set/add but no lock/delete)
}

// rustGeneratedStateClient tracks a generated state client for aggregator/registry generation.
type rustGeneratedStateClient struct {
	PackageName   string // e.g. "baseball"
	DirName       string // snake_case directory name derived from state root: "ca", "cluster"
	Group         string // e.g. "services"
	StateRootName string // e.g. "BaseballState"
	Boundaries    []boundaries.BoundaryData
}

// generateRustStateClientWrappers generates typed state client wrappers for Rust.
// It scans inputDir independently for service_state annotations rather than relying
// on detectServicesForWrapper, which skips proto files without gRPC service definitions.
func generateRustStateClientWrappers(config *tactype.Config, inputDir string) error {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	generated := make(map[string]bool)
	var clients []rustGeneratedStateClient

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

			// Determine package name and parent dir.
			// Use the proto package name directly. Only fall back to the
			// filename stem (from ServiceProtoParts) when the package is
			// qualified (e.g. "services.sentinel" → "sentinel"), because
			// a simple package like "claudesensor" is already correct and
			// overriding it with the filename stem "claude-sensor" would
			// produce an invalid Rust identifier.
			packageName := fileDesc.Package
			qualified := strings.Contains(packageName, ".")
			if idx := strings.Index(packageName, "."); idx > 0 {
				packageName = packageName[:idx]
			}
			parentDir := filepath.Dir(relPath)
			if qualified {
				if serviceName, _ := proto.ServiceProtoParts(relPath); serviceName != "" {
					packageName = serviceName
				}
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

			content, bds, err := generateRustStateWrapper(config, svc, fileDesc, &fileDesc.Messages[i], dp, accessMode)
			if err != nil {
				return fmt.Errorf("failed to generate Rust state wrapper for %s: %w", stateRootAnnotation, err)
			}

			// Write each state root to its own subdirectory (like Go/Java)
			outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "internal", "state", parentDir, stateRootDirName)
			if err := os.MkdirAll(outputDir, 0755); err != nil {
				return fmt.Errorf("failed to create output dir for %s: %w", stateRootDirName, err)
			}

			outputPath := filepath.Join(outputDir, "state.rs")
			if err := os.WriteFile(outputPath, []byte(content), 0644); err != nil {
				return fmt.Errorf("failed to write state.rs for %s: %w", stateRootDirName, err)
			}
			zap.L().Info("Generated Rust state client wrapper", zap.String("path", outputPath))

			clients = append(clients, rustGeneratedStateClient{
				PackageName:   packageName,
				DirName:       stateRootDirName,
				Group:         parentDir,
				StateRootName: fileDesc.Messages[i].Name,
				Boundaries:    bds,
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

	// Generate registry.rs
	if err := generateRustRegistry(config, clients); err != nil {
		return fmt.Errorf("failed to generate Rust registry: %w", err)
	}

	// Generate StateClients aggregator structs (group-level and root-level)
	if err := generateRustStateClientAggregators(config, clients); err != nil {
		return fmt.Errorf("failed to generate Rust StateClients aggregators: %w", err)
	}

	return nil
}

// generateRustStateWrapper renders a state client wrapper for a single state root message.
// Returns the rendered content (to be accumulated per-package) and registry boundaries.
func generateRustStateWrapper(config *tactype.Config, svc ServiceInfo, fileDesc *parser.FileDescriptor, stateRoot *parser.MessageDescriptor, dp *parser.DescriptorParser, accessMode tactype.AccessMode) (string, []boundaries.BoundaryData, error) {
	var singletons []rustSingletonBoundary
	var collections []rustCollectionBoundary
	var stateImports []rustStateImportField
	var inlineFields []rustInlineField
	var commands []rustCommandBoundary
	var views []rustViewBoundary

	// Track cross-package types for imports: package name -> parent directory path
	crossPkgTypes := make(map[string]string)

	// Build a package-to-directory map from all known proto files
	pkgDirMap := make(map[string]string)
	for path, fd := range dp.GetAllFiles() {
		if fd.Package != "" {
			pkgDirMap[fd.Package] = filepath.Dir(path)
		}
	}
	for _, field := range stateRoot.Fields {
		if importAnn, ok := field.Annotations["state_import"]; ok && importAnn.StringValue != "" {
			typeName := extractRustSimpleTypeName(field.Type)
			cratePath := buildRustCratePathWithDir(field.Type, pkgDirMap)
			stateImports = append(stateImports, rustStateImportField{
				FieldName: field.Name,
				TypeName:  typeName,
				CratePath: cratePath,
			})
			// Track the package for cross-package import
			if pkg := extractProtoPackage(field.Type); pkg != "" && pkg != svc.PackageName {
				if dir, ok := pkgDirMap[pkg]; ok {
					crossPkgTypes[pkg] = dir
				}
			}
			continue
		}

		// Skip private boundaries (cross-package types)
		if privAnn, ok := field.Annotations["private"]; ok && privAnn.BoolValue {
			continue
		}

		// View: read-only union over source collections
		if vsAnn, ok := field.Annotations["view_sources"]; ok && len(vsAnn.StringSliceValue) > 0 {
			typeName := extractRustSimpleTypeName(field.Type)
			// Find indexed key field
			keyFieldName := ""
			boundaryMsg := findRustMessageInFile(fileDesc, field.Type)
			if boundaryMsg != nil {
				for _, f := range boundaryMsg.Fields {
					if ann, ok := f.Annotations["indexed"]; ok && ann.BoolValue {
						keyFieldName = f.Name
						break
					}
				}
			}
			views = append(views, rustViewBoundary{
				FieldName:    field.Name,
				FieldPascal:  strutil.ToPascalCase(field.Name),
				TypeName:     typeName,
				KeyFieldName: keyFieldName,
				ViewSources:  vsAnn.StringSliceValue, // keep snake_case for Rust
			})
			continue
		}

		// Command: explicit command RPC (skip normal boundary classification)
		if cmdAnn, ok := field.Annotations["command"]; ok && cmdAnn.BoolValue {
			typeName := extractRustSimpleTypeName(field.Type)
			fieldPascal := strutil.ToPascalCase(field.Name)
			commands = append(commands, rustCommandBoundary{
				FieldName:   field.Name,
				FieldPascal: fieldPascal,
				TypeName:    typeName,
			})
			continue
		}

		if field.TypeKind != "message" {
			continue
		}

		// Skip map fields with scalar/enum values: their map-entry type is not
		// a real generated type in Rust, so referencing it causes compile errors.
		if field.IsMap && field.MapValueTypeKind != "message" {
			continue
		}

		typeName := extractRustSimpleTypeName(field.Type)
		fieldPascal := strutil.ToPascalCase(field.Name)

		// Track cross-package types from message-typed boundary fields
		if pkg := extractProtoPackage(field.Type); pkg != "" && pkg != svc.PackageName {
			if dir, ok := pkgDirMap[pkg]; ok {
				crossPkgTypes[pkg] = dir
			}
		}

		// Check for topic/queue/priority_queue annotations
		if _, ok := field.Annotations["topic"]; ok {
			continue
		}
		if _, ok := field.Annotations["queue"]; ok {
			continue
		}
		if _, ok := field.Annotations["priority_queue"]; ok {
			continue
		}

		if field.IsRepeated {
			boundaryMsg := findRustMessageInFile(fileDesc, field.Type)
			keyFieldName := ""
			if boundaryMsg != nil {
				for _, f := range boundaryMsg.Fields {
					if ann, ok := f.Annotations["indexed"]; ok && ann.BoolValue {
						keyFieldName = f.Name
						break
					}
				}
			}
			collections = append(collections, rustCollectionBoundary{
				FieldName:    field.Name,
				FieldPascal:  fieldPascal,
				TypeName:     typeName,
				KeyFieldName: keyFieldName,

				MaxEntries: boundaries.CollectionSafetyLimit(field),
			})
		} else {
			singletons = append(singletons, rustSingletonBoundary{
				FieldName:   field.Name,
				FieldPascal: fieldPascal,
				TypeName:    typeName,
				DefaultsFn:  strutil.ToSnakeCase(toProstTypeName(typeName)) + "_defaults",
			})
		}
	}

	// Build cross-package import lines. Sort the keys for reproducible output —
	// crossPkgTypes is a Go map, so ranging it directly emits the `use` lines in
	// random order and byte-diffs across otherwise-identical runs.
	crossPkgKeys := make([]string, 0, len(crossPkgTypes))
	for pkg := range crossPkgTypes {
		crossPkgKeys = append(crossPkgKeys, pkg)
	}
	sort.Strings(crossPkgKeys)
	var crossPkgImports strings.Builder
	for _, pkg := range crossPkgKeys {
		dir := crossPkgTypes[pkg]
		dirModule := strings.ReplaceAll(strings.ReplaceAll(dir, "-", "_"), string(filepath.Separator), "::")
		var importPath string
		if strings.HasPrefix(dir, "sys") {
			importPath = fmt.Sprintf("#[allow(unused_imports)]\nuse crate::types::%s::%s::*;\n", dirModule, pkg)
		} else {
			importPath = fmt.Sprintf("#[allow(unused_imports)]\nuse crate::types::%s::%s::*;\n", dirModule, pkg)
		}
		crossPkgImports.WriteString(importPath)
	}

	// Compute the crate path for the types module
	typesCratePath := strings.ReplaceAll(strings.ReplaceAll(svc.ParentDir, "-", "_"), string(filepath.Separator), "::") + "::" + svc.PackageName

	// Derive gRPC state service names from the state root message name.
	// Convention: if the root already ends in "State", append "Service";
	// otherwise append "StateService" (service name always ends in "StateService").
	stateServiceName := stateRoot.Name + "Service"
	if !strings.HasSuffix(stateRoot.Name, "State") {
		stateServiceName = stateRoot.Name + "StateService"
	}
	stateServiceModule := tonicServiceModuleName(stateServiceName) + "_client"
	stateServiceClientType := tonicServiceTypeName(stateServiceName) + "Client"

	data := rustStateWrapperData{
		Header:                 header.GeneratedHeader(consts.LangRust, svc.ProtoFile),
		StateRootName:          stateRoot.Name,
		ModuleName:             strutil.ToSnakeCase(stateRoot.Name),
		TypesCratePath:         typesCratePath,
		PackageName:            svc.PackageName,
		StateServiceName:       stateServiceName,
		StateServiceModule:     stateServiceModule,
		StateServiceClientType: stateServiceClientType,
		Singletons:             singletons,
		Collections:            collections,
		StateImports:           stateImports,
		InlineFields:           inlineFields,
		Commands:               commands,
		Views:                  views,
		CrossPackageImports:    crossPkgImports.String(),
		ReadOnly:               accessMode == tactype.AccessRO || (config.HealthStateName != "" && stateRoot.Name == config.HealthStateName),
		WriteOnce:              accessMode == tactype.AccessWORM,
	}

	tmplContent, err := loadTemplate("statewrapper.rs.tmpl")
	if err != nil {
		return "", nil, fmt.Errorf("failed to load template: %w", err)
	}

	tmpl, err := template.New("rust-statewrapper").Parse(tmplContent)
	if err != nil {
		return "", nil, fmt.Errorf("failed to parse template: %w", err)
	}

	var buf strings.Builder
	if err := tmpl.Execute(&buf, data); err != nil {
		return "", nil, fmt.Errorf("failed to execute template: %w", err)
	}

	registryBoundaries := boundaries.CollectMessageBoundaries(stateRoot, fileDesc)
	return buf.String(), registryBoundaries, nil
}

// rustRegistryClientInfo is the template data for a service in registry.rs.
type rustRegistryClientInfo struct {
	PackageName string
	Group       string
	Boundaries  []boundaries.BoundaryData
}

// generateRustRegistry generates sm/internal/state/registry.rs
func generateRustRegistry(config *tactype.Config, clients []rustGeneratedStateClient) error {
	tmplContent, err := loadTemplate("registry.rs.tmpl")
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

	tmpl, err := template.New("rust-registry").Funcs(funcMap).Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse registry template: %w", err)
	}

	var allClients []rustRegistryClientInfo
	for _, c := range clients {
		allClients = append(allClients, rustRegistryClientInfo{
			PackageName: c.PackageName,
			Group:       c.Group,
			Boundaries:  c.Boundaries,
		})
	}

	data := struct {
		Header  string
		Clients []rustRegistryClientInfo
	}{
		Header:  header.GeneratedHeader(consts.LangRust, "state client registry"),
		Clients: allClients,
	}

	outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "internal", "state")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output dir: %w", err)
	}

	outputPath := filepath.Join(outputDir, "registry.rs")
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, data); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	zap.L().Info("Generated Rust registry", zap.String("path", outputPath))
	return nil
}

// rustClientHasSingletons reports whether a generated state client exposes any
// singleton boundary — only those carry the fetch validate-retry bound.
func rustClientHasSingletons(c rustGeneratedStateClient) bool {
	for _, b := range c.Boundaries {
		if b.GraphType == "singleton" {
			return true
		}
	}
	return false
}

// generateRustStateClientAggregators creates group-level and root-level StateClients
// aggregator structs, mirroring how generateRustGrpcClientAggregators works for GrpcClients.
// Group level: sm/internal/state/{group}/states.rs — holds one field per service
// Root level:  sm/internal/state/state.rs — holds one field per group
func generateRustStateClientAggregators(config *tactype.Config, clients []rustGeneratedStateClient) error {
	smDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "internal", "state")

	// Group clients by their group (parentDir)
	groups := make(map[string][]rustGeneratedStateClient)
	var groupOrder []string
	for _, c := range clients {
		if _, seen := groups[c.Group]; !seen {
			groupOrder = append(groupOrder, c.Group)
		}
		groups[c.Group] = append(groups[c.Group], c)
	}
	sort.Strings(groupOrder)

	// Generate group-level aggregators
	for _, group := range groupOrder {
		groupClients := groups[group]
		groupDir := filepath.Join(smDir, group)
		groupFile := filepath.Join(groupDir, "states.rs")

		if err := os.MkdirAll(groupDir, 0755); err != nil {
			return fmt.Errorf("failed to create group directory %s: %w", group, err)
		}

		var buf strings.Builder
		buf.WriteString(header.GeneratedHeader(consts.LangRust, "state"))
		buf.WriteString("\nuse std::future::Future;\nuse std::pin::Pin;\nuse std::sync::Arc;\nuse tonic::transport::Channel;\n\n")

		// Import individual state client types
		for _, c := range groupClients {
			clientTypeName := c.StateRootName + "Client"
			buf.WriteString(fmt.Sprintf("use super::%s::state::%s;\n", c.DirName, clientTypeName))
		}

		buf.WriteString("\n/// A token provider returns a bearer token string for authenticated state calls.\n")
		buf.WriteString("pub type TokenProvider = Arc<\n")
		buf.WriteString("    dyn Fn() -> Pin<Box<dyn Future<Output = Result<String, Box<dyn std::error::Error + Send + Sync>>> + Send>>\n")
		buf.WriteString("        + Send\n")
		buf.WriteString("        + Sync,\n")
		buf.WriteString(">;\n")

		buf.WriteString("\n/// StateClients aggregates all typed state clients for this group.\n")
		buf.WriteString("pub struct StateClients {\n")
		for _, c := range groupClients {
			fieldName := strutil.ToSnakeCase(c.StateRootName)
			clientTypeName := c.StateRootName + "Client"
			buf.WriteString(fmt.Sprintf("    %s: %s,\n", fieldName, clientTypeName))
		}
		buf.WriteString("}\n\nimpl StateClients {\n")
		buf.WriteString("    /// Creates all typed state clients for this group.\n")
		buf.WriteString("    pub async fn new(conn: Channel, token_provider: Option<TokenProvider>) -> Result<Self, Box<dyn std::error::Error>> {\n")
		buf.WriteString("        Ok(Self {\n")
		for _, c := range groupClients {
			fieldName := strutil.ToSnakeCase(c.StateRootName)
			clientTypeName := c.StateRootName + "Client"
			buf.WriteString(fmt.Sprintf("            %s: %s::new(conn.clone(), token_provider.clone()).await?,\n", fieldName, clientTypeName))
		}
		buf.WriteString("        })\n    }\n\n")

		buf.WriteString("    /// Stops all background Watch loops owned by this group.\n")
		buf.WriteString("    pub fn close(&self) {\n")
		for _, c := range groupClients {
			fieldName := strutil.ToSnakeCase(c.StateRootName)
			buf.WriteString(fmt.Sprintf("        self.%s.close();\n", fieldName))
		}
		buf.WriteString("    }\n\n")

		buf.WriteString("    /// Sets the singleton-fetch validate-retry bound on every state client in this group.\n")
		buf.WriteString("    #[allow(unused_variables)]\n")
		buf.WriteString("    pub fn set_config_fetch_timeout_secs(&self, secs: u64) {\n")
		for _, c := range groupClients {
			if !rustClientHasSingletons(c) {
				continue
			}
			fieldName := strutil.ToSnakeCase(c.StateRootName)
			buf.WriteString(fmt.Sprintf("        self.%s.set_config_fetch_timeout_secs(secs);\n", fieldName))
		}
		buf.WriteString("    }\n\n")

		// Accessor methods
		for _, c := range groupClients {
			fieldName := strutil.ToSnakeCase(c.StateRootName)
			clientTypeName := c.StateRootName + "Client"
			buf.WriteString(fmt.Sprintf("    /// Returns the %s state client.\n", clientTypeName))
			buf.WriteString(fmt.Sprintf("    pub fn %s(&self) -> &%s {\n        &self.%s\n    }\n\n", fieldName, clientTypeName, fieldName))
			buf.WriteString(fmt.Sprintf("    /// Returns a mutable reference to the %s state client.\n", clientTypeName))
			buf.WriteString(fmt.Sprintf("    pub fn %s_mut(&mut self) -> &mut %s {\n        &mut self.%s\n    }\n\n", fieldName, clientTypeName, fieldName))
		}
		buf.WriteString("}\n")

		if err := os.WriteFile(groupFile, []byte(buf.String()), 0644); err != nil {
			return fmt.Errorf("failed to write group states.rs: %w", err)
		}

		zap.L().Info("Generated group StateClients aggregator", zap.String("group", group), zap.String("path", groupFile))
	}

	// Generate root-level aggregator
	rootFile := filepath.Join(smDir, "states.rs")
	var buf strings.Builder
	buf.WriteString(header.GeneratedHeader(consts.LangRust, "state"))
	buf.WriteString("\nuse std::future::Future;\nuse std::pin::Pin;\nuse std::sync::Arc;\nuse tonic::transport::Channel;\n\n")

	for _, group := range groupOrder {
		title := strings.ToUpper(group[:1]) + group[1:]
		buf.WriteString(fmt.Sprintf("use super::%s::states::StateClients as %sStateClients;\n", group, title))
	}

	buf.WriteString("\n/// A token provider returns a bearer token string for authenticated state calls.\n")
	buf.WriteString("pub type TokenProvider = Arc<\n")
	buf.WriteString("    dyn Fn() -> Pin<Box<dyn Future<Output = Result<String, Box<dyn std::error::Error + Send + Sync>>> + Send>>\n")
	buf.WriteString("        + Send\n")
	buf.WriteString("        + Sync,\n")
	buf.WriteString(">;\n")

	buf.WriteString("\n/// StateClients aggregates all typed state client groups.\n")
	buf.WriteString("pub struct StateClients {\n")
	for _, group := range groupOrder {
		title := strings.ToUpper(group[:1]) + group[1:]
		buf.WriteString(fmt.Sprintf("    %s: %sStateClients,\n", group, title))
	}
	buf.WriteString("}\n\nimpl StateClients {\n")
	buf.WriteString("    /// Creates all typed state client groups.\n")
	buf.WriteString("    pub async fn new(conn: Channel, token_provider: Option<TokenProvider>) -> Result<Self, Box<dyn std::error::Error>> {\n")
	buf.WriteString("        Ok(Self {\n")
	for _, group := range groupOrder {
		title := strings.ToUpper(group[:1]) + group[1:]
		buf.WriteString(fmt.Sprintf("            %s: %sStateClients::new(conn.clone(), token_provider.clone()).await?,\n", group, title))
	}
	buf.WriteString("        })\n    }\n\n")

	buf.WriteString("    /// Stops all background Watch loops owned by every state client group.\n")
	buf.WriteString("    pub fn close(&self) {\n")
	for _, group := range groupOrder {
		buf.WriteString(fmt.Sprintf("        self.%s.close();\n", group))
	}
	buf.WriteString("    }\n\n")

	buf.WriteString("    /// Sets the singleton-fetch validate-retry bound on every state client group.\n")
	buf.WriteString("    pub fn set_config_fetch_timeout_secs(&self, secs: u64) {\n")
	for _, group := range groupOrder {
		buf.WriteString(fmt.Sprintf("        self.%s.set_config_fetch_timeout_secs(secs);\n", group))
	}
	buf.WriteString("    }\n\n")

	for _, group := range groupOrder {
		title := strings.ToUpper(group[:1]) + group[1:]
		buf.WriteString(fmt.Sprintf("    /// Returns the %s state clients group.\n", group))
		buf.WriteString(fmt.Sprintf("    pub fn %s(&self) -> &%sStateClients {\n        &self.%s\n    }\n\n", group, title, group))
		buf.WriteString(fmt.Sprintf("    /// Returns a mutable reference to the %s state clients group.\n", group))
		buf.WriteString(fmt.Sprintf("    pub fn %s_mut(&mut self) -> &mut %sStateClients {\n        &mut self.%s\n    }\n\n", group, title, group))
	}
	buf.WriteString("}\n")

	if err := os.WriteFile(rootFile, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write root state.rs: %w", err)
	}

	zap.L().Info("Generated root StateClients aggregator", zap.String("path", rootFile))
	return nil
}

func extractRustSimpleTypeName(fullType string) string {
	parts := strings.Split(fullType, ".")
	return parts[len(parts)-1]
}

// extractProtoPackage extracts the package name from a fully-qualified proto type.
func extractProtoPackage(fullType string) string {
	parts := strings.Split(fullType, ".")
	if len(parts) > 1 {
		return parts[0]
	}
	return ""
}

// buildRustCratePathWithDir builds a fully-qualified Rust crate path for a proto type.
func buildRustCratePathWithDir(fullType string, pkgDirMap map[string]string) string {
	parts := strings.Split(fullType, ".")
	if len(parts) <= 1 {
		return parts[0]
	}
	pkg := parts[0]
	typeName := parts[len(parts)-1]
	if dir, ok := pkgDirMap[pkg]; ok {
		dirModule := strings.ReplaceAll(dir, string(filepath.Separator), "::")
		return fmt.Sprintf("crate::types::%s::%s::%s", dirModule, pkg, typeName)
	}
	// Fallback: package not in directory map — use package name as both dir and module.
	zap.L().Warn("package not found in directory map, using package name as path",
		zap.String("package", pkg), zap.String("type", fullType))
	return fmt.Sprintf("crate::types::%s::%s::%s", pkg, pkg, typeName)
}

func findRustMessageInFile(fileDesc *parser.FileDescriptor, typeName string) *parser.MessageDescriptor {
	simple := extractRustSimpleTypeName(typeName)
	for i := range fileDesc.Messages {
		if fileDesc.Messages[i].Name == simple {
			return &fileDesc.Messages[i]
		}
	}
	return nil
}
