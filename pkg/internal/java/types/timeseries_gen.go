// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// javaTimeseriesTemplateData holds pre-computed data for the Java timeseries
// helpers template. One annotated message produces one Timeseries<Type> class.
type javaTimeseriesTemplateData struct {
	Header    string
	Package   string // Java package of the generated class (the message's own types package)
	ClassName string // "Timeseries" + message name
	TypeName  string // message name
	TypeFqn   string // fully-qualified Java type of the projected message
	Fields    []javaTimeseriesFieldData
}

// HasMessageField reports whether any column is rendered as JSON, which is what
// decides whether the JSON helper is emitted at all.
func (d javaTimeseriesTemplateData) HasMessageField() bool {
	for _, f := range d.Fields {
		if f.Kind == tsKindMessage {
			return true
		}
	}
	return false
}

// javaTimeseriesFieldData holds per-column data for the Java timeseries template.
// All Java references are fully-qualified: the generated class sits in the
// projected message's own package and must name enums and nested messages drawn
// from other proto packages without imports (Java has no import aliases, and the
// other Java emitters in this package qualify for the same reason).
type javaTimeseriesFieldData struct {
	ColumnName string // column name (same as proto field name)
	Accessor   string // Java accessor suffix, e.g. "SessionId" for getSessionId()
	Kind       string // column rendering kind; see the tsKind* constants
	TypeFqn    string // fully-qualified Java enum/message type; empty for scalars
}

// The store-neutral column kinds. Go and Rust key the same three special cases
// off the field's proto type (enum / Timestamp / message) and pass everything
// else through natively; the scalar kinds below are only Java's split of "pass
// it through" into the cast each setter needs.
const (
	tsKindEnum      = "enum"
	tsKindTimestamp = "timestamp"
	tsKindMessage   = "message"
	tsKindString    = "string"
	tsKindBytes     = "bytes"
	tsKindInt       = "int"
	tsKindLong      = "long"
	tsKindFloat     = "float"
	tsKindDouble    = "double"
	tsKindBool      = "bool"
)

// generateJavaTimeseriesHelpers emits timeseries helper classes for every message
// carrying a (spec.timeseries_type) annotation, mirroring Go's
// generateTimeseriesHelpers and Rust's generateRustTimeseriesHelpers.
//
// The generated helpers are store-neutral: column list + row conversion + row
// application. No backend DDL is rendered here — a downstream materializer
// renders the concrete schema from the registry's semantic metadata.
//
// protoDir is the split/annotated proto tree (annotations still present), and
// srcDir is the generated Java source root (used to detect outer class names).
func generateJavaTimeseriesHelpers(config *tactype.Config, protoDir, srcDir string) error {
	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser for timeseries: %w", err)
	}

	all := dp.GetAllFiles()

	// Proto package -> declaring file, for cross-package enum/message resolution.
	// Same role as Rust's protoByPkg; Java needs it to build the foreign type's
	// Java package and outer class.
	fileByPkg := javaProtoFileByPackage(all)

	relPaths := make([]string, 0, len(all))
	for relPath := range all {
		relPaths = append(relPaths, relPath)
	}
	sort.Strings(relPaths)

	tmplStr, err := loadTemplate("timeseries_helpers.java.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load timeseries template: %w", err)
	}
	tmpl, err := template.New("timeseries_helpers").Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse timeseries template: %w", err)
	}

	for _, relPath := range relPaths {
		// Go and Rust skip only the well-known-type tree; so does this.
		if strings.HasPrefix(filepath.ToSlash(relPath), "google/") {
			continue
		}
		fd := all[relPath]
		for msgIdx := range fd.Messages {
			msg := &fd.Messages[msgIdx]
			if !hasJavaTimeseriesType(msg) {
				continue
			}
			data := buildJavaTimeseriesTemplateData(config, dp, fileByPkg, protoDir, srcDir, relPath, fd.Package, msg)
			if err := writeJavaTimeseriesFile(srcDir, data, tmpl); err != nil {
				return fmt.Errorf("failed to generate Java timeseries helpers for %s.%s: %w", fd.Package, msg.Name, err)
			}
			zap.L().Debug("Generated Java timeseries helpers",
				zap.String("message", msg.Name),
				zap.String("package", fd.Package))
		}
	}
	return nil
}

// hasJavaTimeseriesType returns true if the message has a timeseries_type annotation.
func hasJavaTimeseriesType(msg *parser.MessageDescriptor) bool {
	_, ok := msg.Annotations["timeseries_type"]
	return ok
}

// buildJavaTimeseriesTemplateData converts a message descriptor into
// template-ready data for Java. Only fields carrying (spec.timeseries_column)
// become columns, in declaration order — the same selection and order Go and
// Rust use.
func buildJavaTimeseriesTemplateData(
	config *tactype.Config,
	dp *parser.DescriptorParser,
	fileByPkg map[string]string,
	protoDir, srcDir, relPath, protoPackage string,
	msg *parser.MessageDescriptor,
) javaTimeseriesTemplateData {
	typesPackage := javaTimeseriesPackage(config, relPath, protoPackage)
	outerClass := javaTimeseriesOuterClass(protoDir, srcDir, typesPackage, relPath)

	data := javaTimeseriesTemplateData{
		Header:    header.GeneratedHeader(consts.LangJava, relPath),
		Package:   typesPackage,
		ClassName: "Timeseries" + msg.Name,
		TypeName:  msg.Name,
		TypeFqn:   typesPackage + "." + outerClass + "." + msg.Name,
	}

	for i := range msg.Fields {
		f := &msg.Fields[i]
		if _, hasTS := f.Annotations["timeseries_column"]; !hasTS {
			continue
		}

		fd := javaTimeseriesFieldData{
			ColumnName: f.Name,
			Accessor:   javaFieldNameUpper(f.Name),
		}

		switch {
		case f.TypeKind == "enum":
			fd.Kind = tsKindEnum
			fd.TypeFqn = javaTimeseriesTypeFqn(config, dp, fileByPkg, protoDir, srcDir, f.Type)
		case f.TypeKind == "message" && f.Type == "google.protobuf.Timestamp":
			fd.Kind = tsKindTimestamp
		case f.TypeKind == "message":
			fd.Kind = tsKindMessage
			fd.TypeFqn = javaTimeseriesTypeFqn(config, dp, fileByPkg, protoDir, srcDir, f.Type)
		default:
			fd.Kind = javaTimeseriesScalarKind(f.Type)
		}

		data.Fields = append(data.Fields, fd)
	}

	return data
}

// javaTimeseriesScalarKind maps a proto scalar type to its column kind. Scalars
// pass through natively (Go stores the Go field value; this stores the Java
// field value) — only the cast applyRow reads them back with differs.
func javaTimeseriesScalarKind(protoType string) string {
	switch protoType {
	case "int32", "sint32", "sfixed32", "uint32", "fixed32":
		return tsKindInt
	case "int64", "sint64", "sfixed64", "uint64", "fixed64":
		return tsKindLong
	case "float":
		return tsKindFloat
	case "double":
		return tsKindDouble
	case "bool":
		return tsKindBool
	case "bytes":
		return tsKindBytes
	default:
		return tsKindString
	}
}

// javaTimeseriesPackage returns the Java types package a proto file's messages
// land in.
func javaTimeseriesPackage(config *tactype.Config, relPath, protoPackage string) string {
	parentDir := filepath.Dir(relPath)
	if parentDir == "." {
		parentDir = ""
	}
	return javaPackageForProtoPackage(config.JavaPackageBase, protoPackage, parentDir)
}

// javaTimeseriesOuterClass returns the protoc outer class hosting a proto file's
// messages.
func javaTimeseriesOuterClass(protoDir, srcDir, typesPackage, relPath string) string {
	typesPackagePath := strings.ReplaceAll(typesPackage, ".", string(filepath.Separator))
	return detectOuterClassName(srcDir, typesPackagePath, filepath.Join(protoDir, relPath))
}

// javaTimeseriesTypeFqn resolves a proto full name (e.g. "sensor.Level" or
// "accounting.Event.Level") to its fully-qualified Java type.
//
// Cross-package enums and messages are the case Rust's timeseries_gen_test
// pins: a bare type name only resolves when the type lives in the SAME package,
// so a foreign type must be named in full or the generated source does not
// compile. Java has no import aliases, so every reference is qualified.
func javaTimeseriesTypeFqn(
	config *tactype.Config,
	dp *parser.DescriptorParser,
	fileByPkg map[string]string,
	protoDir, srcDir, fullName string,
) string {
	if rest, ok := strings.CutPrefix(fullName, "google.protobuf."); ok {
		return "com.google.protobuf." + rest
	}

	// Longest declaring package wins: "a.b.Msg.Nested" belongs to package "a.b"
	// when a file declares it, not to "a".
	bestPkg := ""
	for pkg := range fileByPkg {
		if !strings.HasPrefix(fullName, pkg+".") {
			continue
		}
		if len(pkg) > len(bestPkg) {
			bestPkg = pkg
		}
	}
	if bestPkg == "" {
		// No declaring file in this build's proto set. Leave the proto name
		// alone rather than inventing a Java package: a wrong package is a
		// compile error that reads as a tacplus bug, a proto name reads as the
		// missing import it is.
		return fullName
	}

	relPath := fileByPkg[bestPkg]
	// A package may span several files; the type's own file owns its outer class.
	if owner := javaTimeseriesDeclaringFile(dp, bestPkg, strings.TrimPrefix(fullName, bestPkg+".")); owner != "" {
		relPath = owner
	}
	typesPackage := javaTimeseriesPackage(config, relPath, bestPkg)
	outerClass := javaTimeseriesOuterClass(protoDir, srcDir, typesPackage, relPath)
	return typesPackage + "." + outerClass + "." + strings.TrimPrefix(fullName, bestPkg+".")
}

// javaTimeseriesDeclaringFile finds which file of a package declares a
// top-level message or enum, so the reference names that file's outer class.
// Returns "" when no file in the package declares it.
func javaTimeseriesDeclaringFile(dp *parser.DescriptorParser, pkg, localName string) string {
	top := localName
	if idx := strings.Index(top, "."); idx >= 0 {
		top = top[:idx]
	}
	best := ""
	for relPath, fd := range dp.GetAllFiles() {
		if fd.Package != pkg {
			continue
		}
		declares := false
		for i := range fd.Messages {
			if fd.Messages[i].Name == top {
				declares = true
				break
			}
		}
		if !declares {
			for i := range fd.Enums {
				if fd.Enums[i].Name == top {
					declares = true
					break
				}
			}
		}
		if declares && (best == "" || relPath < best) {
			best = relPath
		}
	}
	return best
}

// writeJavaTimeseriesFile renders the timeseries template into the projected
// message's own Java package.
func writeJavaTimeseriesFile(srcDir string, data javaTimeseriesTemplateData, tmpl *template.Template) error {
	outputDir := filepath.Join(srcDir, strings.ReplaceAll(data.Package, ".", string(filepath.Separator)))
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}
	outputPath := filepath.Join(outputDir, data.ClassName+".java")

	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create timeseries output file: %w", err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, data); err != nil {
		return fmt.Errorf("failed to execute timeseries template: %w", err)
	}

	zap.L().Info("Generated timeseries helpers (Java)",
		zap.String("class", data.ClassName),
		zap.Int("columns", len(data.Fields)),
		zap.String("path", outputPath))
	return nil
}
