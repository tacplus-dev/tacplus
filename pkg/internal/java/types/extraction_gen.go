// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/extraction"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// javaExtractionTemplateData is one <Source>Projections class: every
// projection one package draws from one source message. It carries what Go
// splits over extraction_<projection>.go and extraction_project_<source>.go:
// an extract<Projection>From<Source> per projection and <source>ToProjections.
type javaExtractionTemplateData struct {
	Header              string
	Package             string // Java package of the projections (their own types package)
	ClassName           string // "<Source>Projections"
	SourceType          string
	SourceFqn           string // fully-qualified Java type of the source message
	ToProjectionsMethod string // "<source>ToProjections"
	Projections         []javaExtractionProjection
}

type javaExtractionProjection struct {
	TypeName      string
	TypeFqn       string
	FieldName     string // field on <Source>Projections
	ExtractMethod string // "extract<Projection>From<Source>"
	Fields        []javaExtractionField
	Variants      []javaExtractionVariant // non-empty: extracted only when one is set
}

type javaExtractionVariant struct {
	Accessor string // protobuf-java accessor stem of the oneof member, e.g. "ToolUse"
	VarName  string // local holding the member's message
	Fields   []javaExtractionField
}

// javaExtractionField is one copy into the projection builder, as a Java
// statement over the builder "msg".
type javaExtractionField struct {
	Statement string
}

// generateJavaExtractionHelpers emits a <Source>Projections class for every
// group the shared planner (common/extraction) finds, mirroring Go's
// generateExtractionHelpers and Rust's generateRustExtractionHelpers.
//
// protoDir is the split/annotated proto tree and srcDir the generated Java
// source root (used to detect outer class names), as for the timeseries
// helpers the projections are also rendered with.
func generateJavaExtractionHelpers(config *tactype.Config, allFiles []model.ProtoFile, protoDir, srcDir string) error {
	groups, err := extraction.Plan(allFiles)
	if err != nil {
		return err
	}
	if len(groups) == 0 {
		return nil
	}

	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser for extraction helpers: %w", err)
	}
	fileByPkg := javaProtoFileByPackage(dp.GetAllFiles())

	tmplStr, err := loadTemplate("extraction_projections.java.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load extraction template: %w", err)
	}
	tmpl, err := template.New("extraction_projections").Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse extraction template: %w", err)
	}

	for _, g := range groups {
		file := g.File()
		typesPackage := javaTimeseriesPackage(config, file.RelPath, file.Package)
		data := javaExtractionTemplateData{
			Header:              header.GeneratedHeader(consts.LangJava, file.RelPath),
			Package:             typesPackage,
			ClassName:           g.Source.Name + "Projections",
			SourceType:          g.Source.Name,
			SourceFqn:           javaTimeseriesTypeFqn(config, dp, fileByPkg, protoDir, srcDir, g.SourceFile.Package+"."+g.Source.Name),
			ToProjectionsMethod: strutil.ToLowerFirst(g.Source.Name) + "ToProjections",
		}
		for _, p := range g.Projections {
			proj := javaExtractionProjection{
				TypeName:      p.Message.Name,
				TypeFqn:       javaTimeseriesTypeFqn(config, dp, fileByPkg, protoDir, srcDir, p.File.Package+"."+p.Message.Name),
				FieldName:     strutil.ToLowerFirst(p.Message.Name),
				ExtractMethod: "extract" + p.Message.Name + "From" + g.Source.Name,
			}
			for _, f := range p.Fields {
				proj.Fields = append(proj.Fields, javaExtractionField{Statement: javaExtractionStatement("event", f)})
			}
			for _, v := range p.Variants {
				variant := javaExtractionVariant{
					Accessor: javaFieldNameUpper(v.Name),
					VarName:  "v" + javaFieldNameUpper(v.Name),
				}
				for _, f := range v.Fields {
					variant.Fields = append(variant.Fields, javaExtractionField{Statement: javaExtractionStatement(variant.VarName, f)})
				}
				proj.Variants = append(proj.Variants, variant)
			}
			data.Projections = append(data.Projections, proj)
		}
		if err := writeJavaExtractionFile(srcDir, data, tmpl); err != nil {
			return fmt.Errorf("failed to generate Java extraction helpers for %s.%s: %w", g.SourceFile.Package, g.Source.Name, err)
		}
	}
	return nil
}

// javaExtractionStatement copies one source field on receiver into the
// projection builder "msg". The projection field takes the source field's
// proto type (spec.transformation derives it), so the copy is a plain set
// except where protobuf-java's accessors differ by kind: collections append,
// a message or proto3-optional source is copied only when present (Go copies
// the nil pointer, Rust the None), and an enum crosses to or from a scalar
// through its number, as Go's cast and Rust's i32 do.
func javaExtractionStatement(receiver string, f extraction.Field) string {
	src := receiver + ".get" + javaFieldNameUpper(f.SourceName)
	set := "msg.set" + javaFieldNameUpper(f.Projection.Name)
	srcIsEnum := f.Resolved && f.Source.IsEnum
	switch {
	case f.Projection.IsMap:
		return "msg.putAll" + javaFieldNameUpper(f.Projection.Name) + "(" + src + "Map());"
	case f.Projection.IsRepeated:
		return "msg.addAll" + javaFieldNameUpper(f.Projection.Name) + "(" + src + "List());"
	case f.Projection.IsEnum:
		value := src + "Value()"
		if f.Resolved && !f.Source.IsEnum {
			value = src + "()"
		}
		return set + "Value(" + value + ");"
	case f.Projection.IsMessage || (f.Resolved && f.Source.IsOptional):
		value := src + "()"
		if srcIsEnum {
			value = src + "Value()"
		}
		return "if (" + receiver + ".has" + javaFieldNameUpper(f.SourceName) + "()) { " + set + "(" + value + "); }"
	case srcIsEnum:
		return set + "(" + src + "Value());"
	default:
		return set + "(" + src + "());"
	}
}

// javaProtoFileByPackage maps each proto package to its first declaring file,
// for resolving a type's Java package and outer class.
func javaProtoFileByPackage(all map[string]*parser.FileDescriptor) map[string]string {
	fileByPkg := make(map[string]string, len(all))
	for relPath, fd := range all {
		if fd.Package == "" {
			continue
		}
		if prev, ok := fileByPkg[fd.Package]; !ok || relPath < prev {
			fileByPkg[fd.Package] = relPath
		}
	}
	return fileByPkg
}

// writeJavaExtractionFile renders the extraction template into the
// projections' own Java package.
func writeJavaExtractionFile(srcDir string, data javaExtractionTemplateData, tmpl *template.Template) error {
	outputDir := filepath.Join(srcDir, strings.ReplaceAll(data.Package, ".", string(filepath.Separator)))
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}
	outputPath := filepath.Join(outputDir, data.ClassName+".java")

	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create extraction output file: %w", err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, data); err != nil {
		return fmt.Errorf("failed to execute extraction template: %w", err)
	}

	zap.L().Info("Generated extraction helpers (Java)",
		zap.String("class", data.ClassName),
		zap.Int("projections", len(data.Projections)),
		zap.String("path", outputPath))
	return nil
}
