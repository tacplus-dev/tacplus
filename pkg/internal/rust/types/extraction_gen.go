// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/header"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/consts"
)

// rustExtractionTemplateData holds pre-computed data for the Rust extraction helpers template.
type rustExtractionTemplateData struct {
	ProjectionType     string                     // e.g., "SessionNode"
	SnakeProjection    string                     // e.g., "session_node"
	SourceType         string                     // e.g., "SensorEvent"
	SnakeSource        string                     // e.g., "sensor_event"
	SourceQualifier    string                     // "" if same package, "sensor::" if different (unused for now — same package)
	IsConditional      bool                       // graph_type with oneof refs -> return None when variant absent
	OneofVariantPascal string                     // e.g., "ToolUse" — for match arm
	OneofVariantSnake  string                     // e.g., "tool_use" — for doc comment
	PayloadEnumPath    string                     // e.g., "sensor_event::Payload" — the Rust enum path for the oneof
	HasOneofGroups     bool                       // unconditional but has oneof fields (timeseries pattern)
	Fields             []rustExtractionFieldData  // all fields (for IsConditional and simple)
	TopLevelFields     []rustExtractionFieldData  // top-level fields (for HasOneofGroups)
	OneofGroups        []rustExtractionOneofGroup // grouped oneof fields (for HasOneofGroups)
}

// rustExtractionFieldData holds per-field data for the Rust extraction template.
type rustExtractionFieldData struct {
	RustFieldName string // Rust field name on projection message (snake_case)
	ValueExpr     string // Rust expression: "event.session_id.clone()" or "payload.tool_name.clone()"
}

// rustExtractionOneofGroup holds fields from the same oneof variant.
type rustExtractionOneofGroup struct {
	VariantPascal string // e.g., "ToolUse" — for match arm
	VarName       string // local variable name, e.g., "tool_use"
	Fields        []rustExtractionFieldData
}

// rustProjectionGroupData holds data for the extraction_project.rs.tmpl template.
type rustProjectionGroupData struct {
	SourceType      string // e.g., "SensorEvent"
	SnakeSource     string // e.g., "sensor_event"
	SourceQualifier string // "" if same package
	Projections     []rustProjectionEntry
}

// rustProjectionEntry is a single projection type in a projection group.
type rustProjectionEntry struct {
	TypeName  string // e.g., "SessionNode"
	SnakeName string // e.g., "session_node"
}

// generateRustExtractionHelpers generates extraction functions for all messages that
// have a graph_type.source or timeseries_type.source annotation.
func generateRustExtractionHelpers(allFiles []model.ProtoFile, outputDir string) error {
	extractionTmplContent, err := loadTemplate("extraction_helpers.rs.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load extraction template: %w", err)
	}
	extractionTmpl, err := template.New("extraction").Parse(extractionTmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse extraction template: %w", err)
	}

	projectTmplContent, err := loadTemplate("extraction_project.rs.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load extraction project template: %w", err)
	}
	projectTmpl, err := template.New("project").Parse(projectTmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse extraction project template: %w", err)
	}

	// Track projections grouped by source FQN for generating projection groups.
	type projGroupKey struct {
		sourceFQN string
		projPkg   string
	}
	type projGroupInfo struct {
		pf              model.ProtoFile
		sourceType      string
		sourceQualifier string
		projections     []string
	}
	projGroups := make(map[projGroupKey]*projGroupInfo)
	var projGroupOrder []projGroupKey

	for _, pf := range allFiles {
		if strings.HasPrefix(pf.RelPath, "google/") {
			continue
		}
		for _, msg := range pf.Messages {
			sourceFQN := rustExtractionSourceFQN(msg)
			if sourceFQN == "" {
				continue
			}

			sourceMsg, sourcePf, ok := rustFindMessageByFQN(allFiles, sourceFQN)
			if !ok {
				return fmt.Errorf("Rust extraction source %q for projection %s.%s not found", sourceFQN, pf.Package, msg.Name)
			}

			data, err := buildRustExtractionTemplateData(msg, pf.Package, sourceMsg, sourcePf.Package, allFiles)
			if err != nil {
				return fmt.Errorf("failed to build Rust extraction data for %s.%s: %w", pf.Package, msg.Name, err)
			}

			if err := writeRustExtractionFile(pf, data, extractionTmpl, outputDir); err != nil {
				return fmt.Errorf("failed to generate Rust extraction helpers for %s.%s: %w", pf.Package, msg.Name, err)
			}
			zap.L().Debug("Generated Rust extraction helpers",
				zap.String("projection", msg.Name),
				zap.String("source", sourceFQN),
				zap.String("package", pf.Package))

			// Track for projection group generation.
			key := projGroupKey{sourceFQN: sourceFQN, projPkg: pf.Package}
			if g, ok := projGroups[key]; ok {
				g.projections = append(g.projections, msg.Name)
			} else {
				projGroups[key] = &projGroupInfo{
					pf:              pf,
					sourceType:      sourceMsg.Name,
					sourceQualifier: data.SourceQualifier,
					projections:     []string{msg.Name},
				}
				projGroupOrder = append(projGroupOrder, key)
			}
		}
	}

	// Generate projection group files.
	for _, key := range projGroupOrder {
		g := projGroups[key]
		entries := make([]rustProjectionEntry, len(g.projections))
		for i, name := range g.projections {
			entries[i] = rustProjectionEntry{
				TypeName:  name,
				SnakeName: strutil.ToSnakeCase(name),
			}
		}
		groupData := rustProjectionGroupData{
			SourceType:      g.sourceType,
			SnakeSource:     strutil.ToSnakeCase(g.sourceType),
			SourceQualifier: g.sourceQualifier,
			Projections:     entries,
		}
		if err := writeRustProjectionGroupFile(g.pf, groupData, projectTmpl, outputDir); err != nil {
			return fmt.Errorf("failed to generate Rust projection group for %s: %w", key.sourceFQN, err)
		}
		zap.L().Debug("Generated Rust projection group",
			zap.String("source", key.sourceFQN),
			zap.Int("projections", len(g.projections)))
	}

	return nil
}

// rustExtractionSourceFQN returns the source event FQN from graph_type.source or
// timeseries_type.source annotations. Returns "" if neither is set.
func rustExtractionSourceFQN(msg model.Message) string {
	if ann, ok := msg.Annotations["graph_type_source"]; ok && ann.StringValue != "" {
		return ann.StringValue
	}
	if ann, ok := msg.Annotations["timeseries_table_source"]; ok && ann.StringValue != "" {
		return ann.StringValue
	}
	return ""
}

// rustFindMessageByFQN locates a message by fully-qualified name (pkg.MessageName).
func rustFindMessageByFQN(allFiles []model.ProtoFile, fqn string) (model.Message, model.ProtoFile, bool) {
	parts := strings.SplitN(fqn, ".", 2)
	if len(parts) != 2 {
		return model.Message{}, model.ProtoFile{}, false
	}
	pkg, name := parts[0], parts[1]
	for _, pf := range allFiles {
		if pf.Package != pkg {
			continue
		}
		for _, msg := range pf.Messages {
			if msg.Name == name {
				return msg, pf, true
			}
		}
	}
	return model.Message{}, model.ProtoFile{}, false
}

// rustFindFieldByName finds a field by name in a message.
func rustFindFieldByName(msg model.Message, name string) (model.Field, bool) {
	for _, f := range msg.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return model.Field{}, false
}

// buildRustExtractionTemplateData converts a projection message and its source into
// template-ready data for the Rust extraction helpers template.
func buildRustExtractionTemplateData(
	projMsg model.Message,
	projPkg string,
	sourceMsg model.Message,
	sourcePkg string,
	allFiles []model.ProtoFile,
) (rustExtractionTemplateData, error) {
	isGraphType := false
	if _, ok := projMsg.Annotations["graph_type"]; ok {
		isGraphType = true
	}

	sourceQualifier := ""
	if sourcePkg != projPkg {
		sourceQualifier = sourcePkg + "::"
	}

	// Build the prost-generated oneof enum path: <snake_source_type>::Payload
	payloadEnumPath := strutil.ToSnakeCase(sourceMsg.Name) + "::Payload"

	data := rustExtractionTemplateData{
		ProjectionType:  projMsg.Name,
		SnakeProjection: strutil.ToSnakeCase(projMsg.Name),
		SourceType:      sourceMsg.Name,
		SnakeSource:     strutil.ToSnakeCase(sourceMsg.Name),
		SourceQualifier: sourceQualifier,
		PayloadEnumPath: payloadEnumPath,
	}

	// Collect fields and detect oneof usage.
	type fieldMapping struct {
		projField    model.Field
		graphSource  string
		oneofVariant string // first segment if dot notation, "" if top-level
		innerField   string // second segment if dot notation
	}

	var mappings []fieldMapping
	oneofVariants := make(map[string]bool)

	for _, f := range projMsg.Fields {
		gs, ok := f.Annotations["projection_source"]
		if !ok || gs.StringValue == "" {
			continue
		}

		fm := fieldMapping{
			projField:   f,
			graphSource: gs.StringValue,
		}

		parts := strings.SplitN(gs.StringValue, ".", 2)
		if len(parts) == 2 {
			fm.oneofVariant = parts[0]
			fm.innerField = parts[1]
			oneofVariants[parts[0]] = true
		}

		mappings = append(mappings, fm)
	}

	hasOneof := len(oneofVariants) > 0

	if isGraphType && hasOneof {
		// Conditional extraction — find the single oneof variant.
		var variant string
		for v := range oneofVariants {
			variant = v
			break
		}

		data.IsConditional = true
		data.OneofVariantPascal = strutil.ToPascalCase(variant)
		data.OneofVariantSnake = variant

		for _, fm := range mappings {
			fd := rustExtractionFieldData{
				RustFieldName: fm.projField.Name, // proto field names are snake_case
			}

			if fm.oneofVariant != "" {
				fd.ValueExpr = buildRustValueExpr("payload", fm.innerField, fm.projField, sourceMsg)
			} else {
				fd.ValueExpr = buildRustValueExpr("event", fm.graphSource, fm.projField, sourceMsg)
			}

			data.Fields = append(data.Fields, fd)
		}
	} else if hasOneof {
		// Unconditional with oneof groups (timeseries pattern).
		data.HasOneofGroups = true

		for _, fm := range mappings {
			if fm.oneofVariant != "" {
				continue
			}
			data.TopLevelFields = append(data.TopLevelFields, rustExtractionFieldData{
				RustFieldName: fm.projField.Name,
				ValueExpr:     buildRustValueExpr("event", fm.graphSource, fm.projField, sourceMsg),
			})
		}

		// Group oneof fields by variant.
		groupMap := make(map[string]*rustExtractionOneofGroup)
		var groupOrder []string
		for _, fm := range mappings {
			if fm.oneofVariant == "" {
				continue
			}
			g, ok := groupMap[fm.oneofVariant]
			if !ok {
				g = &rustExtractionOneofGroup{
					VariantPascal: strutil.ToPascalCase(fm.oneofVariant),
					VarName:       fm.oneofVariant,
				}
				groupMap[fm.oneofVariant] = g
				groupOrder = append(groupOrder, fm.oneofVariant)
			}
			g.Fields = append(g.Fields, rustExtractionFieldData{
				RustFieldName: fm.projField.Name,
				ValueExpr:     buildRustValueExpr(fm.oneofVariant, fm.innerField, fm.projField, sourceMsg),
			})
		}
		for _, variant := range groupOrder {
			data.OneofGroups = append(data.OneofGroups, *groupMap[variant])
		}
	} else {
		// Simple extraction — all fields are top-level.
		for _, fm := range mappings {
			data.Fields = append(data.Fields, rustExtractionFieldData{
				RustFieldName: fm.projField.Name,
				ValueExpr:     buildRustValueExpr("event", fm.graphSource, fm.projField, sourceMsg),
			})
		}
	}

	return data, nil
}

// buildRustValueExpr builds the Rust expression for accessing a source field value.
// receiver is "event" or "payload" or a local variable name.
// fieldName is the field name on the receiver's message type.
func buildRustValueExpr(
	receiver string,
	fieldName string,
	projField model.Field,
	sourceMsg model.Message,
) string {
	accessor := receiver + "." + fieldName

	// Resolve the source field to check for type.
	sourceField, found := rustFindFieldByName(sourceMsg, fieldName)

	// Handle type mismatches — enum source to scalar projection.
	if found && sourceField.IsEnum && !projField.IsEnum && !projField.IsMessage {
		// Enum i32 maps to i32 projection — no cast needed in Rust (prost enums are i32).
		return accessor
	}

	// Determine if we need .clone() — String fields need cloning.
	needsClone := false
	if projField.Type == "string" || projField.Type == "bytes" {
		needsClone = true
	}
	if projField.IsMessage {
		needsClone = true
	}

	if needsClone {
		return accessor + ".clone()"
	}

	return accessor
}

// writeRustExtractionFile renders the extraction template and writes it.
func writeRustExtractionFile(pf model.ProtoFile, data rustExtractionTemplateData, tmpl *template.Template, outputDir string) error {
	parentDir := filepath.Dir(pf.RelPath)
	subdir := filepath.Join("types", parentDir, pf.Package)

	fileName := "extraction_" + strutil.ToSnakeCase(data.ProjectionType) + ".rs"
	helperDir := filepath.Join(outputDir, subdir)
	outputPath := filepath.Join(helperDir, fileName)

	if err := os.MkdirAll(helperDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	var buf bytes.Buffer
	buf.WriteString(header.GeneratedHeader(consts.LangRust, pf.RelPath))
	buf.WriteString("#[allow(unused_imports)]\nuse super::*;\n\n")

	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	return os.WriteFile(outputPath, buf.Bytes(), 0644)
}

// writeRustProjectionGroupFile renders the extraction_project template and writes it.
func writeRustProjectionGroupFile(pf model.ProtoFile, data rustProjectionGroupData, tmpl *template.Template, outputDir string) error {
	parentDir := filepath.Dir(pf.RelPath)
	subdir := filepath.Join("types", parentDir, pf.Package)

	fileName := "extraction_project_" + data.SnakeSource + ".rs"
	helperDir := filepath.Join(outputDir, subdir)
	outputPath := filepath.Join(helperDir, fileName)

	if err := os.MkdirAll(helperDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	var buf bytes.Buffer
	buf.WriteString(header.GeneratedHeader(consts.LangRust, pf.RelPath))
	buf.WriteString("#[allow(unused_imports)]\nuse super::*;\n\n")

	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	return os.WriteFile(outputPath, buf.Bytes(), 0644)
}
