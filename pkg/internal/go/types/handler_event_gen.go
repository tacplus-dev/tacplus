// Copyright (c) 2026 Seabeam LLC
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

// handlerTemplateData holds data for the extraction_handler.go.tmpl template.
type handlerTemplateData struct {
	SourceType            string // e.g., "SensorEvent"
	SourceQualifier       string // "" if same package, "sensor." if different
	HasTimeseries         bool   // true when at least one timeseries projection exists
	HasNoop               bool   // true when source message has a noop field (field 201)
	TimeseriesProjections []handlerTimeseriesEntry
}

// handlerTimeseriesEntry is a timeseries projection in the handler.
type handlerTimeseriesEntry struct {
	TypeName  string // e.g., "SensorTimeseries"
	TableName string // e.g., "sensor_timeseries" — from timeseries_type.name or snake_case(TypeName)
}

// generateEventHandlers generates store-agnostic event handlers for every
// group of projections the shared planner (common/extraction) finds: one
// Handle<Source> per source and projection package.
func generateEventHandlers(allFiles []model.ProtoFile, typesOutputDir string) error {
	groups, err := extraction.Plan(allFiles)
	if err != nil {
		return err
	}

	// Generate handler files. TimeseriesInsertFunc is package-scoped (it does
	// not depend on SourceType), so it is NOT declared per handler file —
	// every package that gets at least one handler here also gets exactly one
	// extraction_handler_support.go declaring it once (below). Declaring it
	// per handler file would redeclare the type as soon as a package has two
	// source types with timeseries projections.
	supportPkgs := make(map[string]model.ProtoFile)
	for _, g := range groups {
		pf := g.File()
		sourceQualifier := ""
		if g.SourceFile.Package != pf.Package {
			sourceQualifier = g.SourceFile.Package + "."
		}
		data := handlerTemplateData{
			SourceType:      g.Source.Name,
			SourceQualifier: sourceQualifier,
			HasTimeseries:   true,
			HasNoop:         g.SourceHasNoop(),
		}
		for _, p := range g.Projections {
			tableName := strutil.ToSnakeCase(p.Message.Name)
			if ann, ok := p.Message.Annotations["timeseries_table_name"]; ok && ann.StringValue != "" {
				tableName = ann.StringValue
			}
			data.TimeseriesProjections = append(data.TimeseriesProjections, handlerTimeseriesEntry{
				TypeName:  p.Message.Name,
				TableName: tableName,
			})
		}

		if err := writeEventHandlerFile(pf, data, typesOutputDir); err != nil {
			return fmt.Errorf("failed to generate event handler for %s.%s: %w", g.SourceFile.Package, g.Source.Name, err)
		}
		zap.L().Debug("Generated event handler",
			zap.String("source", g.SourceFile.Package+"."+g.Source.Name),
			zap.Int("timeseries", len(data.TimeseriesProjections)))

		if _, exists := supportPkgs[eventHandlerOutputDir(pf, typesOutputDir)]; !exists {
			supportPkgs[eventHandlerOutputDir(pf, typesOutputDir)] = pf
		}
	}

	for outputDir, pf := range supportPkgs {
		if err := writeEventHandlerSupportFile(pf, outputDir); err != nil {
			return fmt.Errorf("failed to generate event handler support file for package %s: %w", pf.Package, err)
		}
	}

	return nil
}

// eventHandlerOutputDir returns the directory writeEventHandlerFile writes
// SourceType's handler into, for grouping handlers by output package.
func eventHandlerOutputDir(pf model.ProtoFile, typesOutputDir string) string {
	parentDir := filepath.Dir(pf.RelPath)
	var subdir string
	if filepath.Base(parentDir) == pf.Package {
		subdir = parentDir
	} else {
		subdir = filepath.Join(parentDir, pf.Package)
	}
	return filepath.Join(typesOutputDir, "golang", subdir)
}

// writeEventHandlerSupportFile writes the single, package-scoped
// TimeseriesInsertFunc declaration shared by every extraction_handler_*.go
// file in outputDir's package.
func writeEventHandlerSupportFile(pf model.ProtoFile, outputDir string) error {
	outputPath := filepath.Join(outputDir, "extraction_handler_support.go")

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	var b strings.Builder
	b.WriteString(header.SimpleHeader(consts.LangGo))
	fmt.Fprintf(&b, "package %s\n\n", pf.Package)
	b.WriteString("import \"context\"\n\n")
	b.WriteString("// TimeseriesInsertFunc inserts a timeseries row into the timeseries store.\n")
	b.WriteString("// tableName is the target table, columns is the ordered column names, row is the ordered values.\n")
	b.WriteString("type TimeseriesInsertFunc func(ctx context.Context, tableName string, columns []string, row []interface{}) error\n")

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

// writeEventHandlerFile renders the handler template and writes it.
func writeEventHandlerFile(pf model.ProtoFile, data handlerTemplateData, typesOutputDir string) error {
	fileName := "extraction_handler_" + strutil.ToSnakeCase(data.SourceType) + ".go"
	outputDir := eventHandlerOutputDir(pf, typesOutputDir)
	outputPath := filepath.Join(outputDir, fileName)

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	typeFS, err := fs.Sub(templates.FS, "go/types")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}

	funcMap := tmplutil.BaseFuncMap()
	rendered, err := tmplutil.ExecuteTemplate(typeFS, "extraction_handler.go.tmpl", funcMap, data)
	if err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	var b strings.Builder
	b.WriteString(header.GeneratedHeader(consts.LangGo, pf.RelPath))
	fmt.Fprintf(&b, "package %s\n\n", pf.Package)

	// Add imports — goimports will remove unused ones.
	b.WriteString("import (\n")
	b.WriteString("\t\"context\"\n")
	b.WriteString("\t\"fmt\"\n")
	b.WriteString(")\n\n")

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
