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

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/header"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/tmplutil"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/consts"
	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// timeseriesTemplateData holds pre-computed data for the timeseries helpers template.
type timeseriesTemplateData struct {
	TypeName string
	Fields   []timeseriesFieldData
}

// timeseriesFieldData holds per-field data for the timeseries template.
type timeseriesFieldData struct {
	ColumnName  string // column name (same as proto field name)
	GoFieldName string // Go field name (PascalCase)
	ProtoType   string // original proto type (e.g., "string", "int32", "Timestamp")
	GoType      string // Go type for message fields
	EnumType    string // fully qualified Go enum type
	IsEnum      bool
	IsMessage   bool
}

// generateTimeseriesHelpers generates timeseries helper files for all messages
// that have a (spec.timeseries_type) annotation.
func generateTimeseriesHelpers(allFiles []model.ProtoFile, typesOutputDir string) error {
	for _, pf := range allFiles {
		if strings.HasPrefix(pf.RelPath, "google/") {
			continue
		}
		for _, msg := range pf.Messages {
			if !hasTimeseriesType(msg) {
				continue
			}

			data := buildTimeseriesTemplateData(msg, pf.Package)
			if err := writeTimeseriesFile(pf, data, typesOutputDir); err != nil {
				return fmt.Errorf("failed to generate timeseries helpers for %s.%s: %w", pf.Package, msg.Name, err)
			}
			zap.L().Debug("Generated timeseries helpers",
				zap.String("message", msg.Name),
				zap.String("package", pf.Package))
		}
	}
	return nil
}

// hasTimeseriesType returns true if the message has a timeseries_type annotation.
func hasTimeseriesType(msg model.Message) bool {
	_, ok := msg.Annotations["timeseries_type"]
	return ok
}

// buildTimeseriesTemplateData converts a model.Message into template-ready data.
// The generated helpers are store-neutral: column list + row conversion +
// scan-dest + apply-row. No backend DDL is rendered here — a downstream
// materializer renders the concrete schema from the registry's semantic metadata.
func buildTimeseriesTemplateData(msg model.Message, pkg string) timeseriesTemplateData {
	data := timeseriesTemplateData{
		TypeName: msg.Name,
	}

	for _, f := range msg.Fields {
		if _, hasTS := f.Annotations["timeseries_column"]; !hasTS {
			continue
		}

		fd := timeseriesFieldData{
			ColumnName:  f.Name,
			GoFieldName: toGoFieldName(f.Name),
			ProtoType:   f.Type,
			IsEnum:      f.IsEnum,
			IsMessage:   f.IsMessage,
		}

		if f.IsEnum {
			if f.TypePackage != "" && f.TypePackage != pkg {
				fd.EnumType = f.TypePackage + "." + f.Type
			} else {
				fd.EnumType = f.Type
			}
		}

		if f.IsMessage {
			if f.TypePackage != "" && f.TypePackage != pkg {
				fd.GoType = f.TypePackage + "." + f.Type
			} else {
				fd.GoType = f.Type
			}
		}

		data.Fields = append(data.Fields, fd)
	}

	return data
}

// writeTimeseriesFile renders the timeseries template and writes it to the output directory.
func writeTimeseriesFile(pf model.ProtoFile, data timeseriesTemplateData, typesOutputDir string) error {
	parentDir := filepath.Dir(pf.RelPath)
	var subdir string
	if filepath.Base(parentDir) == pf.Package {
		subdir = parentDir
	} else {
		subdir = filepath.Join(parentDir, pf.Package)
	}

	fileName := "timeseries_" + strutil.ToSnakeCase(data.TypeName) + ".go"
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
	rendered, err := tmplutil.ExecuteTemplate(typeFS, "timeseries_helpers.go.tmpl", funcMap, data)
	if err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	var b strings.Builder
	b.WriteString(header.GeneratedHeader(consts.LangGo, pf.RelPath))
	fmt.Fprintf(&b, "package %s\n\n", pf.Package)

	b.WriteString("import (\n")
	b.WriteString("\"time\"\n")
	b.WriteString("\"google.golang.org/protobuf/encoding/protojson\"\n")
	b.WriteString("\"google.golang.org/protobuf/types/known/timestamppb\"\n")
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
