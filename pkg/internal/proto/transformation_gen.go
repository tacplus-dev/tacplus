// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

// GenerateTransformationMessages scans all proto files for spec.transformation
// annotations and generates the corresponding derived message types, appending
// them to the proto file in tempProtoDir.
//
// A transformation becomes a message with spec.timeseries_type,
// spec.timeseries_column, and spec.projection_source annotations — the
// generator-internal compile target. Nothing about delivery is consulted here:
// the source need not be a spec.record and need not have an EventBus topic.
func GenerateTransformationMessages(tempProtoDir, inputDir string) error {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("transformation gen: failed to create descriptor parser: %w", err)
	}

	for relPath, fd := range dp.GetAllFiles() {
		if IsInfraProto(relPath) {
			continue
		}

		for _, msg := range fd.Messages {
			tsAnn, hasTS := msg.Annotations["transformation"]
			if !hasTS {
				continue
			}

			sourceFQN := fd.Package + "." + msg.Name

			var sb strings.Builder
			sb.WriteString("\n// ============================================================================\n")
			sb.WriteString("// Auto-generated derived types from\n")
			sb.WriteString("// spec.transformation annotations (tacplus)\n")
			sb.WriteString("// ============================================================================\n")

			for _, tp := range tsAnn.TransformationEntries {
				proto, err := generateTransformation(tp, sourceFQN, msg, fd)
				if err != nil {
					return fmt.Errorf("transformation gen: transformation %q on %s: %w", tp.Name, sourceFQN, err)
				}
				sb.WriteString(proto)
			}

			// Append to the proto file in tempProtoDir.
			filePath := filepath.Join(tempProtoDir, relPath)
			content, err := os.ReadFile(filePath)
			if err != nil {
				return fmt.Errorf("transformation gen: failed to read %s: %w", relPath, err)
			}

			var buf strings.Builder
			buf.Write(content)
			buf.WriteString(sb.String())

			if err := os.WriteFile(filePath, []byte(buf.String()), 0644); err != nil {
				return fmt.Errorf("transformation gen: failed to write %s: %w", relPath, err)
			}

			zap.L().Info("generated transformation messages",
				zap.String("source", sourceFQN),
				zap.String("file", relPath),
				zap.Int("transformations", len(tsAnn.TransformationEntries)))
		}
	}

	return nil
}

// sourceRecordKey returns the source record's key field name, or "" when the
// source is not a record at all.
//
// A record key exists only on the DELIVERY side. A transformation over a plain
// (non-record) message has no record to hydrate back to, so it gets no injected
// key — that is the whole point of the split: the pure conversion generates with
// no delivery declaration in sight.
//
// For a record the key is its declared (spec.record).key (surfaced as the
// record_key annotation), or defaultRecordKeyField when none is declared — the
// SAME default the RecordMap query plane uses, so a record and its derived types
// agree on the key. If the named field does not exist on the message, generation
// fails loudly when it resolves the key's type (resolveFieldType), never emitting
// a derived type whose record key cannot be hydrated.
func sourceRecordKey(msg parser.MessageDescriptor) string {
	if _, isRecord := msg.Annotations["record"]; !isRecord {
		return ""
	}
	if k, ok := msg.Annotations["record_key"]; ok && strings.TrimSpace(k.StringValue) != "" {
		return k.StringValue
	}
	return defaultRecordKeyField
}

// dedupStrategyName renders a model dedup strategy as its spec enum value name.
func dedupStrategyName(s model.TimeseriesDedupStrategy) string {
	if s == model.DedupLatestByVersion {
		return "DEDUP_LATEST_BY_VERSION"
	}
	return "DEDUP_NONE"
}

// cardinalityHintName renders a model cardinality hint as its spec enum value name.
func cardinalityHintName(h model.TimeseriesCardinalityHint) string {
	switch h {
	case model.CardinalityLow:
		return "CARDINALITY_LOW"
	case model.CardinalityHigh:
		return "CARDINALITY_HIGH"
	default:
		return "CARDINALITY_UNSPECIFIED"
	}
}

// generateTransformation generates a derived row message from a TransformationEntry.
func generateTransformation(tp model.TransformationEntry, sourceFQN string, msg parser.MessageDescriptor, fd *parser.FileDescriptor) (string, error) {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n// %s — auto-generated transformation of %s.\n", tp.Name, sourceFQN))
	sb.WriteString(fmt.Sprintf("message %s {\n", tp.Name))
	sb.WriteString(fmt.Sprintf("  option (spec.timeseries_type) = {\n"))
	sb.WriteString(fmt.Sprintf("    source: %q\n", sourceFQN))
	if d := tp.Dedup; d != nil {
		sb.WriteString("    dedup: {")
		sb.WriteString("strategy: " + dedupStrategyName(d.Strategy))
		for _, k := range d.KeyFields {
			sb.WriteString(fmt.Sprintf(" key_fields: %q", k))
		}
		if d.VersionField != "" {
			sb.WriteString(fmt.Sprintf(" version_field: %q", d.VersionField))
		}
		sb.WriteString("}\n")
	}
	sb.WriteString(fmt.Sprintf("  };\n\n"))

	recordKey := sourceRecordKey(msg)
	recordKeyEmitted := false

	for i, f := range tp.Fields {
		fieldNum := i + 1
		fieldName := resolveOutputFieldName(f.Source, f.As)
		protoType, err := resolveFieldType(f.Source, msg, fd)
		if err != nil {
			return "", fmt.Errorf("field %q: %w", f.Source, err)
		}

		// Build timeseries_column annotation.
		var colParts []string
		if f.OrderKey > 0 {
			colParts = append(colParts, fmt.Sprintf("order_key: %d", f.OrderKey))
		}
		if f.PartitionKey {
			colParts = append(colParts, "partition_key: true")
		}
		if f.CardinalityHint != model.CardinalityUnspecified {
			colParts = append(colParts, "cardinality_hint: "+cardinalityHintName(f.CardinalityHint))
		}

		var anns []string
		anns = append(anns, fmt.Sprintf("(spec.timeseries_column) = {%s}", strings.Join(colParts, ", ")))
		anns = append(anns, fmt.Sprintf("(spec.projection_source) = %q", f.Source))

		// Mark the field that carries the source record's key (for federation/hydration).
		if recordKey != "" && f.Source == recordKey {
			anns = append(anns, "(spec.projection_record_key) = true")
			recordKeyEmitted = true
		}

		sb.WriteString(fmt.Sprintf("  %s %s = %d [\n    %s\n  ];\n", protoType, fieldName, fieldNum, strings.Join(anns, ",\n    ")))
	}

	// Auto-inject the source record's key so the derived-type query can return
	// record keys (federation → hydrate). Only when the source IS a record (a
	// transformation over a plain message has no record to hydrate back to) and the
	// author didn't already project it.
	if recordKey != "" && !recordKeyEmitted {
		keyType, err := resolveFieldType(recordKey, msg, fd)
		if err != nil {
			return "", fmt.Errorf("record key %q for transformation %q: %w", recordKey, tp.Name, err)
		}
		sb.WriteString(fmt.Sprintf("  %s %s = %d [\n    (spec.timeseries_column) = {},\n    (spec.projection_source) = %q,\n    (spec.projection_record_key) = true\n  ];\n",
			keyType, recordKey, len(tp.Fields)+1, recordKey))
	}

	sb.WriteString("}\n")
	return sb.String(), nil
}

// resolveFieldType resolves a field path (e.g. "email", "tool_use.tool_name")
// to its proto type by looking up the source message's fields.
func resolveFieldType(fieldPath string, msg parser.MessageDescriptor, fd *parser.FileDescriptor) (string, error) {
	parts := strings.SplitN(fieldPath, ".", 2)

	// Simple field: look up directly on the source message.
	if len(parts) == 1 {
		for _, f := range msg.Fields {
			if f.Name == fieldPath {
				return fieldProtoType(f, fd), nil
			}
		}
		return "", fmt.Errorf("field %q not found on %s", fieldPath, msg.Name)
	}

	// Dot path: first segment is a oneof field name, second is a field on its message type.
	oneofFieldName := parts[0]
	nestedFieldName := parts[1]

	// Find the oneof field on the source message.
	var oneofField *parser.FieldDescriptor
	for i := range msg.Fields {
		if msg.Fields[i].Name == oneofFieldName && msg.Fields[i].IsOneOf {
			oneofField = &msg.Fields[i]
			break
		}
	}
	if oneofField == nil {
		return "", fmt.Errorf("oneof field %q not found on %s", oneofFieldName, msg.Name)
	}

	// Find the message type of the oneof field.
	// Type may be package-qualified (e.g. "telemetry.ToolUseEvent") — strip prefix.
	typeName := oneofField.Type
	if idx := strings.LastIndex(typeName, "."); idx >= 0 {
		typeName = typeName[idx+1:]
	}
	for _, m := range fd.Messages {
		if m.Name == typeName {
			for _, f := range m.Fields {
				if f.Name == nestedFieldName {
					return fieldProtoType(f, fd), nil
				}
			}
			return "", fmt.Errorf("field %q not found on %s", nestedFieldName, typeName)
		}
	}

	return "", fmt.Errorf("message type %q not found for oneof field %q", typeName, oneofFieldName)
}

// fieldProtoType returns the proto type string for a field descriptor.
// For enums, returns the qualified enum name. For messages, returns the type name.
// For scalars, returns the proto primitive type.
func fieldProtoType(f parser.FieldDescriptor, fd *parser.FileDescriptor) string {
	switch f.TypeKind {
	case "enum":
		// Enum types are referenced by name in the same package.
		return f.Type
	case "message":
		return f.Type
	default:
		// Scalar: f.Type is already the proto type (string, int64, bool, etc.)
		return f.Type
	}
}

// resolveOutputFieldName determines the output field name for a projection field.
// Uses 'as' if set, otherwise the last segment of the source path.
func resolveOutputFieldName(source, as string) string {
	if as != "" {
		return as
	}
	parts := strings.Split(source, ".")
	return parts[len(parts)-1]
}
