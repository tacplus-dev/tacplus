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

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/header"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/consts"
)

// rustTimeseriesTemplateData holds pre-computed data for the Rust timeseries helpers template.
type rustTimeseriesTemplateData struct {
	TypeName      string
	SnakeTypeName string
	Fields        []rustTimeseriesFieldData
}

// rustTimeseriesFieldData holds per-field data for the Rust timeseries template.
type rustTimeseriesFieldData struct {
	ColumnName    string // column name (same as proto field name)
	RustFieldName string // Rust field name (snake_case)
	ProtoType     string // original proto type (e.g., "string", "int32", "Timestamp")
	RustType      string // Rust type for the row struct field
	EnumType      string // Rust enum type name
	IsEnum        bool
	IsMessage     bool
}

// generateRustTimeseriesHelpers generates timeseries helper files for all messages
// that have a (spec.timeseries_type) annotation.
func generateRustTimeseriesHelpers(allFiles []model.ProtoFile, outputDir string) error {
	tmplContent, err := loadTemplate("timeseries_helpers.rs.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load timeseries template: %w", err)
	}

	tmpl, err := template.New("timeseries").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse timeseries template: %w", err)
	}

	// Build a lookup of package -> proto file for cross-package enum resolution,
	// same pattern as writeSerialization's protoByPkg.
	protoByPkg := make(map[string]model.ProtoFile)
	for _, p := range allFiles {
		protoByPkg[p.Package] = p
	}

	for _, pf := range allFiles {
		if strings.HasPrefix(pf.RelPath, "google/") {
			continue
		}
		for _, msg := range pf.Messages {
			if !hasRustTimeseriesType(msg) {
				continue
			}

			data := buildRustTimeseriesTemplateData(msg, pf.Package, protoByPkg)
			if err := writeRustTimeseriesFile(pf, data, tmpl, outputDir); err != nil {
				return fmt.Errorf("failed to generate Rust timeseries helpers for %s.%s: %w", pf.Package, msg.Name, err)
			}
			zap.L().Debug("Generated Rust timeseries helpers",
				zap.String("message", msg.Name),
				zap.String("package", pf.Package))
		}
	}
	return nil
}

// hasRustTimeseriesType returns true if the message has a timeseries_type annotation.
func hasRustTimeseriesType(msg model.Message) bool {
	_, ok := msg.Annotations["timeseries_type"]
	return ok
}

// buildRustTimeseriesTemplateData converts a model.Message into template-ready
// data for Rust. The generated helpers are store-neutral: Row struct + from_proto
// + column list. No backend DDL is rendered here — a downstream materializer
// renders the concrete schema from the registry's semantic metadata.
func buildRustTimeseriesTemplateData(msg model.Message, pkg string, protoByPkg map[string]model.ProtoFile) rustTimeseriesTemplateData {
	data := rustTimeseriesTemplateData{
		TypeName:      msg.Name,
		SnakeTypeName: strutil.ToSnakeCase(msg.Name),
	}

	for _, f := range msg.Fields {
		if _, hasTS := f.Annotations["timeseries_column"]; !hasTS {
			continue
		}

		fd := rustTimeseriesFieldData{
			ColumnName:    f.Name,
			RustFieldName: f.Name, // proto field names are already snake_case
			ProtoType:     f.Type,
			IsEnum:        f.IsEnum,
			IsMessage:     f.IsMessage,
		}

		// Map proto type to Rust type for the Row struct
		fd.RustType = protoTypeToRustRowType(f.Type, f.IsEnum, f.IsMessage)

		if f.IsEnum {
			fd.EnumType = f.Type
			// Cross-package enums aren't visible via the generated file's `use super::*`,
			// so qualify them with a full crate path (same pattern as writeSerialization's
			// collectEnumInfo).
			if f.TypePackage != "" {
				if epf, ok := protoByPkg[f.TypePackage]; ok {
					parentDir := filepath.Dir(epf.RelPath)
					fd.EnumType = fmt.Sprintf("crate::%s::%s::%s",
						strings.ReplaceAll(parentDir, "/", "::"), f.TypePackage, f.Type)
				}
			}
		}

		data.Fields = append(data.Fields, fd)
	}

	return data
}

// protoTypeToRustRowType maps proto types to Rust types for the Row struct.
// Enums and messages are stored as String (enum name / JSON).
func protoTypeToRustRowType(protoType string, isEnum, isMessage bool) string {
	if isEnum {
		return "String"
	}
	if isMessage {
		return "String"
	}
	switch protoType {
	case "string":
		return "String"
	case "int32", "sint32", "sfixed32":
		return "i32"
	case "int64", "sint64", "sfixed64":
		return "i64"
	case "uint32", "fixed32":
		return "u32"
	case "uint64", "fixed64":
		return "u64"
	case "float":
		return "f32"
	case "double":
		return "f64"
	case "bool":
		return "bool"
	case "bytes":
		return "String"
	case "Timestamp":
		return "i64" // millis
	default:
		return "String"
	}
}

// writeRustTimeseriesFile renders the timeseries template and writes it to the output directory.
func writeRustTimeseriesFile(pf model.ProtoFile, data rustTimeseriesTemplateData, tmpl *template.Template, outputDir string) error {
	parentDir := filepath.Dir(pf.RelPath)
	subdir := filepath.Join("types", parentDir, pf.Package)

	fileName := "timeseries_" + strutil.ToSnakeCase(data.TypeName) + ".rs"
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
