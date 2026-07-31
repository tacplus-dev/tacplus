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

// graphTemplateData holds pre-computed data for the graph helpers template.
type graphTemplateData struct {
	TypeName string
	Fields   []graphFieldData
}

// graphFieldData holds per-field data for the graph template.
type graphFieldData struct {
	FieldName          string // proto field name (snake_case)
	GoFieldName        string // Go field name (PascalCase)
	Mode               string // "PREDICATE" or "INCLUDE"
	ProtoType          string // original proto type (e.g., "string", "int32", "Timestamp")
	GoType             string // Go type without pointer (e.g., "SessionNode")
	GoMapType          string // Go map type (e.g., "map[string]string")
	GoRepeatedElemType string // element type for repeated message fields
	EnumType           string // fully qualified Go enum type (e.g., "SensorType")
	MessageType        string // message type name for uid edges (e.g., "AgentNode")
	IsUidEdge          bool   // true for PREDICATE on message fields
	IsEnum             bool
	IsMessage          bool
	IsMap              bool
	IsRepeated         bool
	IsRepeatedMessage  bool // repeated field where element is a message
	IsOptional         bool
	IsUpsert           bool
	Deref              string // "*" for optional fields, "" otherwise
	FromGraphAssign    string // pre-computed assignment for FromGraph scalar reads
}

// generateGraphHelpers generates ToGraph/FromGraph helper files for all messages
// that have at least one field with (spec.graph) annotations.
func generateGraphHelpers(allFiles []model.ProtoFile, typesOutputDir string) error {
	// Build set of message names that have graph helpers, so uid edge
	// references to messages without helpers can be skipped.
	graphMsgSet := make(map[string]bool)
	for _, pf := range allFiles {
		if strings.HasPrefix(pf.RelPath, "google/") {
			continue
		}
		for _, msg := range pf.Messages {
			if hasGraphFields(msg) {
				graphMsgSet[msg.Name] = true
			}
		}
	}

	for _, pf := range allFiles {
		if strings.HasPrefix(pf.RelPath, "google/") {
			continue
		}
		for _, msg := range pf.Messages {
			if !hasGraphFields(msg) {
				continue
			}

			data, err := buildGraphTemplateData(msg, pf.Package, graphMsgSet)
			if err != nil {
				return fmt.Errorf("failed to build graph helpers for %s.%s: %w", pf.Package, msg.Name, err)
			}
			if err := writeGraphFile(pf, data, typesOutputDir); err != nil {
				return fmt.Errorf("failed to generate graph helpers for %s.%s: %w", pf.Package, msg.Name, err)
			}
			zap.L().Debug("Generated graph helpers",
				zap.String("message", msg.Name),
				zap.String("package", pf.Package))
		}
	}
	return nil
}

// hasGraphFields returns true if the message should have ToGraph/FromGraph generated.
// This is true when any field has an explicit (spec.graph) annotation, OR when the
// message itself has a (spec.graph_type) annotation (all fields default to PREDICATE).
func hasGraphFields(msg model.Message) bool {
	if _, ok := msg.Annotations["graph_type"]; ok {
		return true
	}
	for _, f := range msg.Fields {
		if ann, ok := f.Annotations["graph_mode"]; ok && ann.StringValue != "" {
			return true
		}
	}
	return false
}

// buildGraphTemplateData converts a model.Message into template-ready data.
// graphMsgSet contains the names of messages that have graph helpers.
func buildGraphTemplateData(msg model.Message, pkg string, graphMsgSet map[string]bool) (graphTemplateData, error) {
	data := graphTemplateData{
		TypeName: msg.Name,
	}

	// Messages with graph_type default all fields to PREDICATE when no
	// explicit (spec.graph) annotation is present.
	_, hasGraphType := msg.Annotations["graph_type"]

	for _, f := range msg.Fields {
		modeAnn, hasMode := f.Annotations["graph_mode"]
		mode := modeAnn.StringValue
		if !hasMode || mode == "" {
			if hasGraphType {
				// Default to PREDICATE for graph_type messages.
				mode = "PREDICATE"
			} else {
				continue
			}
		}

		fd := graphFieldData{
			FieldName:   f.Name,
			GoFieldName: toGoFieldName(f.Name),
			Mode:        mode,
			ProtoType:   f.Type,
			IsEnum:      f.IsEnum,
			IsMessage:   f.IsMessage,
			IsMap:       f.IsMap,
			IsRepeated:  f.IsRepeated,
			IsOptional:  f.IsOptional,
		}

		if f.IsOptional {
			fd.Deref = "*"
		}

		// Check upsert
		if upsertAnn, ok := f.Annotations["graph_upsert"]; ok && upsertAnn.BoolValue {
			fd.IsUpsert = true
		}

		// Handle uid edges (PREDICATE on message type).
		// Only create uid edges when the referenced message has graph helpers
		// (i.e., it also has graph_type or graph_mode annotations). Otherwise
		// the generated code would call a non-existent ToGraph/FromGraph.
		if mode == "PREDICATE" && f.IsMessage {
			if graphMsgSet[f.Type] {
				fd.IsUidEdge = true
				fd.MessageType = f.Type
				fd.GoType = f.Type
			} else {
				return graphTemplateData{}, fmt.Errorf("predicate field %s references message %s without graph helpers", f.Name, f.Type)
			}
		}

		// Enum type for FromGraph
		if f.IsEnum {
			if f.TypePackage != "" && f.TypePackage != pkg {
				fd.EnumType = f.TypePackage + "." + f.Type
			} else {
				fd.EnumType = f.Type
			}
		}

		// Message type for INCLUDE
		if f.IsMessage && mode == "INCLUDE" {
			if f.TypePackage != "" && f.TypePackage != pkg {
				fd.GoType = f.TypePackage + "." + f.Type
			} else {
				fd.GoType = f.Type
			}
		}

		// Map type for INCLUDE
		if f.IsMap {
			fd.GoMapType = fmt.Sprintf("map[%s]%s", f.MapKeyType, mapValueGoType(f))
		}

		// Repeated message element type
		if f.IsRepeated && f.IsMessage {
			fd.IsRepeatedMessage = true
			if f.TypePackage != "" && f.TypePackage != pkg {
				fd.GoRepeatedElemType = f.TypePackage + "." + f.Type
			} else {
				fd.GoRepeatedElemType = f.Type
			}
		}

		// Repeated non-message type
		if f.IsRepeated && !f.IsMessage {
			fd.GoType = "[]" + protoTypeToGoType(f.Type)
		}

		// Pre-compute FromGraph scalar assignment
		if mode == "PREDICATE" && !f.IsMessage && !f.IsEnum {
			fd.FromGraphAssign = buildFromGraphScalar(f, fd.GoFieldName)
		}

		data.Fields = append(data.Fields, fd)
	}

	return data, nil
}

// writeGraphFile renders the graph template and writes it to the output directory.
func writeGraphFile(pf model.ProtoFile, data graphTemplateData, typesOutputDir string) error {
	// Determine output subdirectory (same logic as processProtoFiles)
	parentDir := filepath.Dir(pf.RelPath)
	var subdir string
	if filepath.Base(parentDir) == pf.Package {
		subdir = parentDir
	} else {
		subdir = filepath.Join(parentDir, pf.Package)
	}

	fileName := "graph_" + strutil.ToSnakeCase(data.TypeName) + ".go"
	outputDir := filepath.Join(typesOutputDir, "golang", subdir)
	outputPath := filepath.Join(outputDir, fileName)

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Render template
	typeFS, err := fs.Sub(templates.FS, "go/types")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}

	funcMap := tmplutil.BaseFuncMap()
	rendered, err := tmplutil.ExecuteTemplate(typeFS, "graph_helpers.go.tmpl", funcMap, data)
	if err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	// Build file with header, package declaration, imports, and rendered body
	var b strings.Builder
	b.WriteString(header.GeneratedHeader(consts.LangGo, pf.RelPath))
	fmt.Fprintf(&b, "package %s\n\n", pf.Package)

	// Add all possible imports — goimports removes unused ones
	b.WriteString("import (\n")
	b.WriteString("\"encoding/json\"\n")
	b.WriteString("\"fmt\"\n")
	b.WriteString("\"time\"\n")
	b.WriteString("\"google.golang.org/protobuf/encoding/protojson\"\n")
	b.WriteString("\"google.golang.org/protobuf/types/known/timestamppb\"\n")
	b.WriteString(")\n\n")

	b.Write(rendered)

	// Format with goimports
	formatted, err := imports.Process(outputPath, []byte(b.String()), &imports.Options{
		Comments:  true,
		TabIndent: true,
		TabWidth:  8,
	})
	if err != nil {
		// Write unformatted for debugging
		zap.L().Warn("goimports failed, writing unformatted", zap.String("path", outputPath), zap.Error(err))
		formatted = []byte(b.String())
	}

	return os.WriteFile(outputPath, formatted, 0644)
}

// buildFromGraphScalar returns the Go assignment code for reading a scalar
// predicate from a graph result map[string]interface{}.
func buildFromGraphScalar(f model.Field, goFieldName string) string {
	switch f.Type {
	case "string":
		if f.IsRepeated {
			return fmt.Sprintf("if arr, ok := v.([]interface{}); ok { for _, item := range arr { if s, ok := item.(string); ok { msg.%s = append(msg.%s, s) } } }", goFieldName, goFieldName)
		}
		if f.IsOptional {
			return fmt.Sprintf("if s, ok := v.(string); ok { msg.%s = &s }", goFieldName)
		}
		return fmt.Sprintf("if s, ok := v.(string); ok { msg.%s = s }", goFieldName)
	case "bytes":
		return fmt.Sprintf("if s, ok := v.(string); ok { msg.%s = []byte(s) }", goFieldName)
	case "bool":
		if f.IsOptional {
			return fmt.Sprintf("if b, ok := v.(bool); ok { msg.%s = &b }", goFieldName)
		}
		return fmt.Sprintf("if b, ok := v.(bool); ok { msg.%s = b }", goFieldName)
	case "int32":
		return buildFromGraphInt(goFieldName, "int32", f.IsOptional)
	case "int64":
		return buildFromGraphInt(goFieldName, "int64", f.IsOptional)
	case "uint32":
		return buildFromGraphInt(goFieldName, "uint32", f.IsOptional)
	case "uint64":
		return buildFromGraphInt(goFieldName, "uint64", f.IsOptional)
	case "sint32":
		return buildFromGraphInt(goFieldName, "int32", f.IsOptional)
	case "sint64":
		return buildFromGraphInt(goFieldName, "int64", f.IsOptional)
	case "fixed32":
		return buildFromGraphInt(goFieldName, "uint32", f.IsOptional)
	case "fixed64":
		return buildFromGraphInt(goFieldName, "uint64", f.IsOptional)
	case "sfixed32":
		return buildFromGraphInt(goFieldName, "int32", f.IsOptional)
	case "sfixed64":
		return buildFromGraphInt(goFieldName, "int64", f.IsOptional)
	case "float":
		return buildFromGraphFloat(goFieldName, "float32", f.IsOptional)
	case "double":
		return buildFromGraphFloat(goFieldName, "float64", f.IsOptional)
	default:
		return fmt.Sprintf("_ = v // unsupported type %s for field %s", f.Type, f.Name)
	}
}

func buildFromGraphInt(goFieldName, goType string, isOptional bool) string {
	// Graph store returns numbers as json.Number or float64 depending on encoding
	if isOptional {
		return fmt.Sprintf("switch n := v.(type) {\ncase json.Number:\nif i, err := n.Int64(); err == nil { val := %s(i); msg.%s = &val }\ncase float64:\nval := %s(n); msg.%s = &val\n}",
			goType, goFieldName, goType, goFieldName)
	}
	return fmt.Sprintf("switch n := v.(type) {\ncase json.Number:\nif i, err := n.Int64(); err == nil { msg.%s = %s(i) }\ncase float64:\nmsg.%s = %s(n)\n}",
		goFieldName, goType, goFieldName, goType)
}

func buildFromGraphFloat(goFieldName, goType string, isOptional bool) string {
	if isOptional {
		return fmt.Sprintf("switch n := v.(type) {\ncase json.Number:\nif f, err := n.Float64(); err == nil { val := %s(f); msg.%s = &val }\ncase float64:\nval := %s(n); msg.%s = &val\n}",
			goType, goFieldName, goType, goFieldName)
	}
	return fmt.Sprintf("switch n := v.(type) {\ncase json.Number:\nif f, err := n.Float64(); err == nil { msg.%s = %s(f) }\ncase float64:\nmsg.%s = %s(n)\n}",
		goFieldName, goType, goFieldName, goType)
}

// mapValueGoType returns the Go type for a map value field.
func mapValueGoType(f model.Field) string {
	if f.MapValueIsMessage {
		if f.MapValueTypePackage != "" {
			return "*" + f.MapValueTypePackage + "." + f.MapValueType
		}
		return "*" + f.MapValueType
	}
	return protoTypeToGoType(f.MapValueType)
}
