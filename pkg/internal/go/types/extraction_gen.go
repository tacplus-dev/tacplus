// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/tools/imports"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/extraction"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/tmplutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"github.com/tacplus-dev/tacplus/templates"
)

// extractionTemplateData holds pre-computed data for the extraction helpers template.
type extractionTemplateData struct {
	ProjectionType  string                 // e.g., "SessionNode"
	SourceType      string                 // e.g., "SensorEvent"
	SourceQualifier string                 // "" if same package, "sensor." if different
	HasOneofGroups  bool                   // unconditional but has oneof fields (timeseries pattern)
	Fields          []extractionFieldData  // all fields (for the simple pattern)
	TopLevelFields  []extractionFieldData  // top-level fields (for HasOneofGroups)
	OneofGroups     []extractionOneofGroup // grouped oneof fields (for HasOneofGroups)
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
// have a timeseries_type.source annotation. These functions
// convert a source event message into a typed projection message.
//
// After generating individual Extract<Projection>From<Source> functions, it groups
// all projections by source event type and generates a Project<Source> function
// that returns a <Source>Projections struct containing all projection results.
// The projections and their field mappings come from the shared planner
// (common/extraction), which Java and Rust render from too.
func generateExtractionHelpers(allFiles []model.ProtoFile, typesOutputDir string) error {
	groups, err := extraction.Plan(allFiles)
	if err != nil {
		return err
	}

	for _, g := range groups {
		var entries []projectionEntry
		var sourceQualifier string
		for _, p := range g.Projections {
			data := buildExtractionTemplateData(p, g)
			sourceQualifier = data.SourceQualifier
			if err := writeExtractionFile(p.File, data, typesOutputDir); err != nil {
				return fmt.Errorf("failed to generate extraction helpers for %s.%s: %w", p.File.Package, p.Message.Name, err)
			}
			zap.L().Debug("Generated extraction helpers",
				zap.String("projection", p.Message.Name),
				zap.String("source", g.SourceFile.Package+"."+g.Source.Name),
				zap.String("package", p.File.Package))
			entries = append(entries, projectionEntry{TypeName: p.Message.Name})
		}

		// Generate the projection group file for the source event.
		groupData := projectionGroupData{
			SourceType:      g.Source.Name,
			SourceQualifier: sourceQualifier,
			Projections:     entries,
		}
		if err := writeProjectionGroupFile(g.File(), groupData, typesOutputDir); err != nil {
			return fmt.Errorf("failed to generate projection group for %s.%s: %w", g.SourceFile.Package, g.Source.Name, err)
		}
		zap.L().Debug("Generated projection group",
			zap.String("source", g.SourceFile.Package+"."+g.Source.Name),
			zap.Int("projections", len(g.Projections)))
	}

	return nil
}

// buildExtractionTemplateData converts a planned projection into template-ready
// data for the extraction helpers template.
func buildExtractionTemplateData(p extraction.Projection, g extraction.Group) extractionTemplateData {
	sourceQualifier := ""
	if g.SourceFile.Package != p.File.Package {
		sourceQualifier = g.SourceFile.Package + "."
	}

	data := extractionTemplateData{
		ProjectionType:  p.Message.Name,
		SourceType:      g.Source.Name,
		SourceQualifier: sourceQualifier,
	}

	var fields []extractionFieldData
	for _, f := range p.Fields {
		fields = append(fields, extractionFieldData{
			GoFieldName: toGoFieldName(f.Projection.Name),
			ValueExpr:   buildValueExpr("event", f),
		})
	}

	// Determine extraction pattern:
	// - timeseries_type with oneof refs → unconditional with oneof groups
	// - no oneof refs → simple struct literal
	if len(p.Variants) == 0 {
		data.Fields = fields
		return data
	}

	// Unconditional with oneof groups (timeseries pattern).
	data.HasOneofGroups = true
	data.TopLevelFields = fields
	for _, v := range p.Variants {
		varName := toLowerCamel(v.Name)
		group := extractionOneofGroup{
			VariantPascal: toGoFieldName(v.Name),
			VarName:       varName,
		}
		for _, f := range v.Fields {
			group.Fields = append(group.Fields, extractionFieldData{
				GoFieldName: toGoFieldName(f.Projection.Name),
				ValueExpr:   varName + "." + toGoFieldName(f.SourceName),
			})
		}
		data.OneofGroups = append(data.OneofGroups, group)
	}
	return data
}

// buildValueExpr builds the Go expression for accessing a source field value
// on receiver ("event"), casting where the projection and source field types
// differ.
func buildValueExpr(receiver string, f extraction.Field) string {
	accessor := receiver + "." + toGoFieldName(f.SourceName)

	// A field the source does not declare is returned as-is; it fails to compile
	// naming the field.
	if !f.Resolved {
		return accessor
	}

	// Handle type mismatches between projection and source.
	if needsCast(f.Projection, f.Source) {
		goType := protoTypeToGoType(f.Projection.Type)
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
