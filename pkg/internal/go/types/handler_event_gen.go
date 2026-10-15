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

// generateEventHandlers generates store-agnostic event handlers for all source
// event types that have timeseries_type projections.
func generateEventHandlers(allFiles []model.ProtoFile, typesOutputDir string) error {
	// Group projections by source FQN and package.
	type handlerKey struct {
		sourceFQN string
		projPkg   string
	}
	type handlerInfo struct {
		pf              model.ProtoFile
		sourceType      string
		sourceQualifier string
		hasNoop         bool
		tsEntries       []handlerTimeseriesEntry
	}
	handlers := make(map[handlerKey]*handlerInfo)
	var handlerOrder []handlerKey

	for _, pf := range allFiles {
		if strings.HasPrefix(pf.RelPath, "google/") {
			continue
		}
		for _, msg := range pf.Messages {
			sourceFQN := extraction.SourceFQN(msg)
			if sourceFQN == "" {
				continue
			}

			sourceMsg, sourcePf, ok := extraction.FindMessage(allFiles, sourceFQN)
			if !ok {
				continue
			}

			key := handlerKey{sourceFQN: sourceFQN, projPkg: pf.Package}
			h, exists := handlers[key]
			if !exists {
				sourceQualifier := ""
				if sourcePf.Package != pf.Package {
					sourceQualifier = sourcePf.Package + "."
				}
				// Check if source message has a noop field (field 201) for trace round support.
				hasNoop := false
				for _, f := range sourceMsg.Fields {
					if f.Number == 201 && f.Name == "noop" {
						hasNoop = true
						break
					}
				}
				h = &handlerInfo{
					pf:              pf,
					sourceType:      sourceMsg.Name,
					sourceQualifier: sourceQualifier,
					hasNoop:         hasNoop,
				}
				handlers[key] = h
				handlerOrder = append(handlerOrder, key)
			}

			if _, isTsTable := msg.Annotations["timeseries_type"]; isTsTable {
				tableName := strutil.ToSnakeCase(msg.Name)
				if ann, ok := msg.Annotations["timeseries_table_name"]; ok && ann.StringValue != "" {
					tableName = ann.StringValue
				}
				h.tsEntries = append(h.tsEntries, handlerTimeseriesEntry{
					TypeName:  msg.Name,
					TableName: tableName,
				})
			}
		}
	}

	// Generate handler files. TimeseriesInsertFunc is package-scoped (it does
	// not depend on SourceType), so it is NOT declared per handler file —
	// every package that gets at least one handler here also gets exactly one
	// extraction_handler_support.go declaring it once (below). Declaring it
	// per handler file would redeclare the type as soon as a package has two
	// source types with timeseries projections.
	supportPkgs := make(map[string]model.ProtoFile)
	for _, key := range handlerOrder {
		h := handlers[key]
		if len(h.tsEntries) == 0 {
			continue
		}

		data := handlerTemplateData{
			SourceType:            h.sourceType,
			SourceQualifier:       h.sourceQualifier,
			HasTimeseries:         len(h.tsEntries) > 0,
			HasNoop:               h.hasNoop,
			TimeseriesProjections: h.tsEntries,
		}

		if err := writeEventHandlerFile(h.pf, data, typesOutputDir); err != nil {
			return fmt.Errorf("failed to generate event handler for %s: %w", key.sourceFQN, err)
		}
		zap.L().Debug("Generated event handler",
			zap.String("source", key.sourceFQN),
			zap.Int("timeseries", len(h.tsEntries)))

		if _, exists := supportPkgs[eventHandlerOutputDir(h.pf, typesOutputDir)]; !exists {
			supportPkgs[eventHandlerOutputDir(h.pf, typesOutputDir)] = h.pf
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
