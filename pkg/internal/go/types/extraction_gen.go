// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/tools/imports"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/header"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/tmplutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/consts"
	"gitlab.com/secondstarcavalier/schematic/tacplus/templates"
)

// extractionTemplateData holds pre-computed data for the extraction helpers template.
type extractionTemplateData struct {
	ProjectionType     string                 // e.g., "SessionNode"
	SourceType         string                 // e.g., "SensorEvent"
	SourceQualifier    string                 // "" if same package, "sensor." if different
	IsConditional      bool                   // graph_type with oneof refs → return nil when variant absent
	OneofVariantPascal string                 // e.g., "ToolUse" — for Get<Variant>() call
	OneofVariantSnake  string                 // e.g., "tool_use" — for doc comment
	HasOneofGroups     bool                   // unconditional but has oneof fields (timeseries pattern)
	Fields             []extractionFieldData  // all fields (for IsConditional and simple)
	TopLevelFields     []extractionFieldData  // top-level fields (for HasOneofGroups)
	OneofGroups        []extractionOneofGroup // grouped oneof fields (for HasOneofGroups)
}

// extractionFieldData holds per-field data for the extraction template.
type extractionFieldData struct {
	GoFieldName string // Go field name on projection message, e.g., "SessionId"
	ValueExpr   string // Go expression: "event.SessionId" or "payload.ToolName"
}

// extractionOneofGroup holds fields that come from the same oneof variant.
type extractionOneofGroup struct {
	VariantPascal string // e.g., "ToolUse" — for GetToolUse()
	VarName       string // local variable name, e.g., "toolUse"
	Fields        []extractionFieldData
}

// projectionGroupData holds data for the extraction_project.go.tmpl template.
type projectionGroupData struct {
	SourceType      string // e.g., "SensorEvent"
	SourceQualifier string // "" if same package, "sensor." if different
	Projections     []projectionEntry
}

// projectionEntry is a single projection type in a projection group.
type projectionEntry struct {
	TypeName string // e.g., "SessionNode"
}

// generateExtractionHelpers generates extraction functions for all messages that
// have a graph_type.source or timeseries_type.source annotation. These functions
// convert a source event message into a typed projection message.
//
// After generating individual Extract<Projection>From<Source> functions, it groups
// all projections by source event type and generates a Project<Source> function
// that returns a <Source>Projections struct containing all projection results.
func generateExtractionHelpers(allFiles []model.ProtoFile, typesOutputDir string) error {
	// Track projections grouped by source FQN for generating projection groups.
	// Key: source FQN (e.g. "sensor.SensorEvent"), Value: list of projection names.
	type projGroupKey struct {
		sourceFQN string
		projPkg   string // package of the projection messages
	}
	type projGroupInfo struct {
		pf              model.ProtoFile // proto file of the projection messages
		sourceType      string          // e.g. "SensorEvent"
		sourceQualifier string          // "" or "sensor."
		projections     []string        // projection type names
	}
	projGroups := make(map[projGroupKey]*projGroupInfo)
	var projGroupOrder []projGroupKey

	for _, pf := range allFiles {
		if strings.HasPrefix(pf.RelPath, "google/") {
			continue
		}
		for _, msg := range pf.Messages {
			sourceFQN := extractionSourceFQN(msg)
			if sourceFQN == "" {
				continue
			}

			sourceMsg, sourcePf, ok := findMessageByFQN(allFiles, sourceFQN)
			if !ok {
				return fmt.Errorf("extraction source %q for projection %s.%s not found", sourceFQN, pf.Package, msg.Name)
			}

			data, err := buildExtractionTemplateData(msg, pf.Package, sourceMsg, sourcePf.Package, allFiles)
			if err != nil {
				return fmt.Errorf("failed to build extraction data for %s.%s: %w", pf.Package, msg.Name, err)
			}

			if err := writeExtractionFile(pf, data, typesOutputDir); err != nil {
				return fmt.Errorf("failed to generate extraction helpers for %s.%s: %w", pf.Package, msg.Name, err)
			}
			zap.L().Debug("Generated extraction helpers",
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

	// Generate projection group files for source events with multiple projections.
	for _, key := range projGroupOrder {
		g := projGroups[key]
		entries := make([]projectionEntry, len(g.projections))
		for i, name := range g.projections {
			entries[i] = projectionEntry{TypeName: name}
		}
		groupData := projectionGroupData{
			SourceType:      g.sourceType,
			SourceQualifier: g.sourceQualifier,
			Projections:     entries,
		}
		if err := writeProjectionGroupFile(g.pf, groupData, typesOutputDir); err != nil {
			return fmt.Errorf("failed to generate projection group for %s: %w", key.sourceFQN, err)
		}
		zap.L().Debug("Generated projection group",
			zap.String("source", key.sourceFQN),
			zap.Int("projections", len(g.projections)))
	}

	return nil
}

// extractionSourceFQN returns the source event FQN from graph_type.source or
// timeseries_type.source annotations. Returns "" if neither is set.
func extractionSourceFQN(msg model.Message) string {
	if ann, ok := msg.Annotations["graph_type_source"]; ok && ann.StringValue != "" {
		return ann.StringValue
	}
	if ann, ok := msg.Annotations["timeseries_table_source"]; ok && ann.StringValue != "" {
		return ann.StringValue
	}
	return ""
}

// findMessageByFQN locates a message by fully-qualified name (pkg.MessageName).
func findMessageByFQN(allFiles []model.ProtoFile, fqn string) (model.Message, model.ProtoFile, bool) {
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

// findFieldByName finds a field by name in a message.
func findFieldByName(msg model.Message, name string) (model.Field, bool) {
	for _, f := range msg.Fields {
		if f.Name == name {
			return f, true
		}
	}
	return model.Field{}, false
}

// buildExtractionTemplateData converts a projection message and its source into
// template-ready data for the extraction helpers template.
func buildExtractionTemplateData(
	projMsg model.Message,
	projPkg string,
	sourceMsg model.Message,
	sourcePkg string,
	allFiles []model.ProtoFile,
) (extractionTemplateData, error) {
	isGraphType := false
	if _, ok := projMsg.Annotations["graph_type"]; ok {
		isGraphType = true
	}

	sourceQualifier := ""
	if sourcePkg != projPkg {
		sourceQualifier = sourcePkg + "."
	}

	data := extractionTemplateData{
		ProjectionType:  projMsg.Name,
		SourceType:      sourceMsg.Name,
		SourceQualifier: sourceQualifier,
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

	// Determine extraction pattern:
	// - graph_type with oneof refs → conditional (return nil when variant absent)
	// - timeseries_type with oneof refs → unconditional with oneof groups
	// - no oneof refs → simple struct literal
	hasOneof := len(oneofVariants) > 0

	if isGraphType && hasOneof {
		// Conditional extraction — find the single oneof variant.
		// If multiple oneof variants are referenced, use the first one in proto
		// field-declaration order for the nil guard. (Unusual case; graph types
		// typically map to one oneof variant.) Taking it from `mappings` rather
		// than ranging the oneofVariants map keeps the emitted guard stable:
		// a map range picks an arbitrary variant per run (GEN-DET-1).
		var variant string
		for _, fm := range mappings {
			if fm.oneofVariant != "" {
				variant = fm.oneofVariant
				break
			}
		}

		data.IsConditional = true
		data.OneofVariantPascal = toGoFieldName(variant)
		data.OneofVariantSnake = variant

		for _, fm := range mappings {
			fd := extractionFieldData{
				GoFieldName: toGoFieldName(fm.projField.Name),
			}

			if fm.oneofVariant != "" {
				// Field from the oneof payload — use "payload" variable.
				fd.ValueExpr = buildValueExpr("payload", fm.innerField, fm.projField, sourceMsg, sourcePkg, allFiles)
			} else {
				// Top-level field on the event.
				fd.ValueExpr = buildValueExpr("event", fm.graphSource, fm.projField, sourceMsg, sourcePkg, allFiles)
			}

			data.Fields = append(data.Fields, fd)
		}
	} else if hasOneof {
		// Unconditional with oneof groups (timeseries pattern).
		data.HasOneofGroups = true

		// Collect top-level fields.
		for _, fm := range mappings {
			if fm.oneofVariant != "" {
				continue
			}
			data.TopLevelFields = append(data.TopLevelFields, extractionFieldData{
				GoFieldName: toGoFieldName(fm.projField.Name),
				ValueExpr:   buildValueExpr("event", fm.graphSource, fm.projField, sourceMsg, sourcePkg, allFiles),
			})
		}

		// Group oneof fields by variant.
		groupMap := make(map[string]*extractionOneofGroup)
		var groupOrder []string
		for _, fm := range mappings {
			if fm.oneofVariant == "" {
				continue
			}
			g, ok := groupMap[fm.oneofVariant]
			if !ok {
				varName := toLowerCamel(fm.oneofVariant)
				g = &extractionOneofGroup{
					VariantPascal: toGoFieldName(fm.oneofVariant),
					VarName:       varName,
				}
				groupMap[fm.oneofVariant] = g
				groupOrder = append(groupOrder, fm.oneofVariant)
			}
			g.Fields = append(g.Fields, extractionFieldData{
				GoFieldName: toGoFieldName(fm.projField.Name),
				ValueExpr:   g.VarName + "." + toGoFieldName(fm.innerField),
			})
		}
		for _, variant := range groupOrder {
			data.OneofGroups = append(data.OneofGroups, *groupMap[variant])
		}
	} else {
		// Simple extraction — all fields are top-level.
		for _, fm := range mappings {
			data.Fields = append(data.Fields, extractionFieldData{
				GoFieldName: toGoFieldName(fm.projField.Name),
				ValueExpr:   buildValueExpr("event", fm.graphSource, fm.projField, sourceMsg, sourcePkg, allFiles),
			})
		}
	}

	return data, nil
}

// buildValueExpr builds the Go expression for accessing a source field value.
// receiver is "event" or "payload" or a local variable name.
// fieldName is the field name on the receiver's message type.
// projField is the projection field (to detect type mismatches).
func buildValueExpr(
	receiver string,
	fieldName string,
	projField model.Field,
	sourceMsg model.Message,
	sourcePkg string,
	allFiles []model.ProtoFile,
) string {
	accessor := receiver + "." + toGoFieldName(fieldName)

	// Resolve the source field to check for type mismatches.
	sourceField, found := findFieldByName(sourceMsg, fieldName)
	if !found {
		// Field not found in source — might be in a nested message.
		// Fall through and return the accessor as-is.
		return accessor
	}

	// Handle type mismatches between projection and source.
	if needsCast(projField, sourceField) {
		goType := protoTypeToGoType(projField.Type)
		return goType + "(" + accessor + ")"
	}

	return accessor
}

// needsCast returns true when the projection field type differs from the source
// field type in a way that requires an explicit Go type cast.
func needsCast(projField, sourceField model.Field) bool {
	// Enum source → scalar projection (e.g., SensorType → int32)
	if sourceField.IsEnum && !projField.IsEnum && !projField.IsMessage {
		return true
	}
	// Scalar source → enum projection (unlikely but handle it)
	if !sourceField.IsEnum && projField.IsEnum && !sourceField.IsMessage {
		return true
	}
	return false
}

// toLowerCamel converts a snake_case string to lowerCamelCase.
// "tool_use" → "toolUse", "event_stream" → "eventStream"
func toLowerCamel(s string) string {
	parts := strings.Split(s, "_")
	if len(parts) == 0 {
		return s
	}
	var b strings.Builder
	for i, part := range parts {
		if len(part) == 0 {
			// Consecutive underscores: emit one underscore to preserve distinctness.
			if i > 0 {
				b.WriteRune('_')
			}
			continue
		}
		if i == 0 {
			b.WriteString(strings.ToLower(part[:1]))
		} else {
			b.WriteString(strings.ToUpper(part[:1]))
		}
		if len(part) > 1 {
			b.WriteString(part[1:])
		}
	}
	result := b.String()
	// Avoid Go keywords
	if isGoKeyword(result) {
		return result + "_"
	}
	return result
}

// isGoKeyword returns true if s is a Go reserved keyword.
func isGoKeyword(s string) bool {
	switch s {
	case "break", "case", "chan", "const", "continue", "default", "defer",
		"else", "fallthrough", "for", "func", "go", "goto", "if", "import",
		"interface", "map", "package", "range", "return", "select", "struct",
		"switch", "type", "var":
		return true
	}
	return false
}

// writeProjectionGroupFile renders the extraction_project.go.tmpl template and writes
// a file containing the <Source>Projections struct and Project<Source> function.
func writeProjectionGroupFile(pf model.ProtoFile, data projectionGroupData, typesOutputDir string) error {
	parentDir := filepath.Dir(pf.RelPath)
	var subdir string
	if filepath.Base(parentDir) == pf.Package {
		subdir = parentDir
	} else {
		subdir = filepath.Join(parentDir, pf.Package)
	}

	fileName := "extraction_project_" + strutil.ToSnakeCase(data.SourceType) + ".go"
	outputDir := filepath.Join(typesOutputDir, "golang", subdir)
	outputPath := filepath.Join(outputDir, fileName)

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	typeFS, err := fs.Sub(templates.FS, "go/types")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}

	funcMap := tmplutil.BaseFuncMap()
	rendered, err := tmplutil.ExecuteTemplate(typeFS, "extraction_project.go.tmpl", funcMap, data)
	if err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	var b strings.Builder
	b.WriteString(header.GeneratedHeader(consts.LangGo, pf.RelPath))
	fmt.Fprintf(&b, "package %s\n\n", pf.Package)
	b.Write(rendered)

	formatted, err := imports.Process(outputPath, []byte(b.String()), &imports.Options{
		Comments:  true,
		TabIndent: true,
		TabWidth:  8,
	})
	if err != nil {
		zap.L().Warn("goimports failed, writing unformatted", zap.String("path", outputPath), zap.Error(err))
		formatted = []byte(b.String())
	}

	return os.WriteFile(outputPath, formatted, 0644)
}

// writeExtractionFile renders the extraction template and writes it.
func writeExtractionFile(pf model.ProtoFile, data extractionTemplateData, typesOutputDir string) error {
	parentDir := filepath.Dir(pf.RelPath)
	var subdir string
	if filepath.Base(parentDir) == pf.Package {
		subdir = parentDir
	} else {
		subdir = filepath.Join(parentDir, pf.Package)
	}

	fileName := "extraction_" + strutil.ToSnakeCase(data.ProjectionType) + ".go"
	outputDir := filepath.Join(typesOutputDir, "golang", subdir)
	outputPath := filepath.Join(outputDir, fileName)

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	typeFS, err := fs.Sub(templates.FS, "go/types")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}

	funcMap := tmplutil.BaseFuncMap()
	rendered, err := tmplutil.ExecuteTemplate(typeFS, "extraction_helpers.go.tmpl", funcMap, data)
	if err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	var b strings.Builder
	b.WriteString(header.GeneratedHeader(consts.LangGo, pf.RelPath))
	fmt.Fprintf(&b, "package %s\n\n", pf.Package)

	// Add source package import if cross-package.
	if data.SourceQualifier != "" {
		sourcePkg := strings.TrimSuffix(data.SourceQualifier, ".")
		b.WriteString("import (\n")
		// The import path needs to be resolved — for now, use a placeholder
		// that goimports can resolve from the Go module's generated types.
		fmt.Fprintf(&b, "\t// source package import resolved by goimports\n")
		fmt.Fprintf(&b, "\t_ %q\n", sourcePkg)
		b.WriteString(")\n\n")
	}

	b.Write(rendered)

	formatted, err := imports.Process(outputPath, []byte(b.String()), &imports.Options{
		Comments:  true,
		TabIndent: true,
		TabWidth:  8,
	})
	if err != nil {
		zap.L().Warn("goimports failed, writing unformatted", zap.String("path", outputPath), zap.Error(err))
		formatted = []byte(b.String())
	}

	return os.WriteFile(outputPath, formatted, 0644)
}
