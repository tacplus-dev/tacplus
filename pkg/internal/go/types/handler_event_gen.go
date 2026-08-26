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
			sourceFQN := extractionSourceFQN(msg)
			if sourceFQN == "" {
				continue
			}

			sourceMsg, sourcePf, ok := findMessageByFQN(allFiles, sourceFQN)
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

	// Generate handler files.
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
	}

	return nil
}

// writeEventHandlerFile renders the handler template and writes it.
func writeEventHandlerFile(pf model.ProtoFile, data handlerTemplateData, typesOutputDir string) error {
	parentDir := filepath.Dir(pf.RelPath)
	var subdir string
	if filepath.Base(parentDir) == pf.Package {
		subdir = parentDir
	} else {
		subdir = filepath.Join(parentDir, pf.Package)
	}

	fileName := "extraction_handler_" + strutil.ToSnakeCase(data.SourceType) + ".go"
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
