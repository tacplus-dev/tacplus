// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"text/template"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/extraction"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
)

// rustExtractionTemplateData holds pre-computed data for the Rust extraction helpers template.
type rustExtractionTemplateData struct {
	ProjectionType  string                     // e.g., "SessionNode"
	SnakeProjection string                     // e.g., "session_node"
	SourceType      string                     // e.g., "SensorEvent"
	SnakeSource     string                     // e.g., "sensor_event"
	SourceQualifier string                     // "" if same package, "sensor::" if different (unused for now — same package)
	PayloadEnumPath string                     // e.g., "sensor_event::Payload" — the Rust enum path for the oneof
	HasOneofGroups  bool                       // unconditional but has oneof fields (timeseries pattern)
	Fields          []rustExtractionFieldData  // all fields (for the simple pattern)
	TopLevelFields  []rustExtractionFieldData  // top-level fields (for HasOneofGroups)
	OneofGroups     []rustExtractionOneofGroup // grouped oneof fields (for HasOneofGroups)
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
	HasNoop         bool   // the source carries the injected trace-round noop
	Projections     []rustProjectionEntry
}

// rustProjectionEntry is a single projection type in a projection group.
type rustProjectionEntry struct {
	TypeName  string // e.g., "SessionNode"
	SnakeName string // e.g., "session_node"
}

// generateRustExtractionHelpers generates extraction functions for all messages that
// have a timeseries_type.source annotation.
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

	groups, err := extraction.Plan(allFiles)
	if err != nil {
		return err
	}

	for _, g := range groups {
		var entries []rustProjectionEntry
		var sourceQualifier string
		for _, p := range g.Projections {
			data := buildRustExtractionTemplateData(p, g)
			sourceQualifier = data.SourceQualifier
			if err := writeRustExtractionFile(p.File, data, extractionTmpl, outputDir); err != nil {
				return fmt.Errorf("failed to generate Rust extraction helpers for %s.%s: %w", p.File.Package, p.Message.Name, err)
			}
			zap.L().Debug("Generated Rust extraction helpers",
				zap.String("projection", p.Message.Name),
				zap.String("source", g.SourceFile.Package+"."+g.Source.Name),
				zap.String("package", p.File.Package))
			entries = append(entries, rustProjectionEntry{
				TypeName:  p.Message.Name,
				SnakeName: strutil.ToSnakeCase(p.Message.Name),
			})
		}

		// Generate the projection group file.
		groupData := rustProjectionGroupData{
			SourceType:      g.Source.Name,
			SnakeSource:     strutil.ToSnakeCase(g.Source.Name),
			SourceQualifier: sourceQualifier,
			HasNoop:         g.SourceHasNoop(),
			Projections:     entries,
		}
		if err := writeRustProjectionGroupFile(g.File(), groupData, projectTmpl, outputDir); err != nil {
			return fmt.Errorf("failed to generate Rust projection group for %s.%s: %w", g.SourceFile.Package, g.Source.Name, err)
		}
		zap.L().Debug("Generated Rust projection group",
			zap.String("source", g.SourceFile.Package+"."+g.Source.Name),
			zap.Int("projections", len(g.Projections)))
	}

	return nil
}

// buildRustExtractionTemplateData converts a planned projection into
// template-ready data for the Rust extraction helpers template.
// rustRecordDispatchFn is the async handle_<source> a group's
// extraction_project_<source>.rs declares, Go's Handle<Source>.
func rustRecordDispatchFn(g extraction.Group) string {
	return "handle_" + strutil.ToSnakeCase(g.Source.Name)
}

func buildRustExtractionTemplateData(p extraction.Projection, g extraction.Group) rustExtractionTemplateData {
	sourceQualifier := ""
	if g.SourceFile.Package != p.File.Package {
		sourceQualifier = g.SourceFile.Package + "::"
	}

	data := rustExtractionTemplateData{
		ProjectionType:  p.Message.Name,
		SnakeProjection: strutil.ToSnakeCase(p.Message.Name),
		SourceType:      g.Source.Name,
		SnakeSource:     strutil.ToSnakeCase(g.Source.Name),
		SourceQualifier: sourceQualifier,
		// The prost-generated oneof enum path: <snake_source_type>::Payload
		PayloadEnumPath: strutil.ToSnakeCase(g.Source.Name) + "::Payload",
	}

	var fields []rustExtractionFieldData
	for _, f := range p.Fields {
		fields = append(fields, rustExtractionFieldData{
			RustFieldName: f.Projection.Name,
			ValueExpr:     buildRustValueExpr("event", f),
		})
	}
	if len(p.Variants) == 0 {
		// Simple extraction — all fields are top-level.
		data.Fields = fields
		return data
	}

	// Unconditional with oneof groups (timeseries pattern).
	data.HasOneofGroups = true
	data.TopLevelFields = fields
	for _, v := range p.Variants {
		group := rustExtractionOneofGroup{
			VariantPascal: strutil.ToPascalCase(v.Name),
			VarName:       v.Name,
		}
		for _, f := range v.Fields {
			group.Fields = append(group.Fields, rustExtractionFieldData{
				RustFieldName: f.Projection.Name,
				ValueExpr:     buildRustValueExpr(v.Name, f),
			})
		}
		data.OneofGroups = append(data.OneofGroups, group)
	}
	return data
}

// buildRustValueExpr builds the Rust expression for reading a source field on
// receiver ("event", or the bound oneof variant).
func buildRustValueExpr(receiver string, f extraction.Field) string {
	accessor := receiver + "." + f.SourceName
	projField := f.Projection

	// Enum source to scalar projection: prost enums are i32, so no cast and no
	// clone.
	if f.Resolved && f.Source.IsEnum && !projField.IsEnum && !projField.IsMessage {
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
