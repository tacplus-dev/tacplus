// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

// GenerateProjectionMessages scans all proto files for spec.graph_projection
// and spec.timeseries_projection annotations and generates the corresponding
// projection message types, appending them to the proto file in tempProtoDir.
//
// Graph projections become messages with spec.graph_type, spec.graph,
// spec.graph_index, spec.graph_edge, and spec.projection_source annotations.
//
// Timeseries projections become messages with spec.timeseries_type,
// spec.timeseries_column, and spec.projection_source annotations.
func GenerateProjectionMessages(tempProtoDir, inputDir string) error {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("projection gen: failed to create descriptor parser: %w", err)
	}

	for relPath, fd := range dp.GetAllFiles() {
		if IsInfraProto(relPath) {
			continue
		}

		for _, msg := range fd.Messages {
			graphAnn, hasGraph := msg.Annotations["graph_projection"]
			tsAnn, hasTS := msg.Annotations["timeseries_projection"]
			if !hasGraph && !hasTS {
				continue
			}

			sourceFQN := fd.Package + "." + msg.Name

			var sb strings.Builder
			sb.WriteString("\n// ============================================================================\n")
			sb.WriteString("// Auto-generated projection messages from spec.graph_projection and\n")
			sb.WriteString("// spec.timeseries_projection annotations (tacplus)\n")
			sb.WriteString("// ============================================================================\n")

			if hasGraph {
				for _, gp := range graphAnn.GraphProjectionEntries {
					proto, err := generateGraphProjection(gp, sourceFQN, msg, fd)
					if err != nil {
						return fmt.Errorf("projection gen: graph projection %q on %s: %w", gp.Name, sourceFQN, err)
					}
					sb.WriteString(proto)
				}
			}

			if hasTS {
				for _, tp := range tsAnn.TimeseriesProjectionEntries {
					proto, err := generateTimeseriesProjection(tp, sourceFQN, msg, fd)
					if err != nil {
						return fmt.Errorf("projection gen: timeseries projection %q on %s: %w", tp.Name, sourceFQN, err)
					}
					sb.WriteString(proto)
				}
			}

			// Append to the proto file in tempProtoDir.
			filePath := filepath.Join(tempProtoDir, relPath)
			content, err := os.ReadFile(filePath)
			if err != nil {
				return fmt.Errorf("projection gen: failed to read %s: %w", relPath, err)
			}

			var buf strings.Builder
			buf.Write(content)
			buf.WriteString(sb.String())

			if err := os.WriteFile(filePath, []byte(buf.String()), 0644); err != nil {
				return fmt.Errorf("projection gen: failed to write %s: %w", relPath, err)
			}

			zap.L().Info("generated projection messages",
				zap.String("source", sourceFQN),
				zap.String("file", relPath),
				zap.Int("graph", len(graphAnn.GraphProjectionEntries)),
				zap.Int("timeseries", len(tsAnn.TimeseriesProjectionEntries)))
		}
	}

	return nil
}

// generateGraphProjection generates a graph node message from a GraphProjectionEntry.
func generateGraphProjection(gp model.GraphProjectionEntry, sourceFQN string, msg parser.MessageDescriptor, fd *parser.FileDescriptor) (string, error) {
	nodeName := gp.Name + "Node"

	// Resolve xid field name (after 'as' rename).
	xidFieldName := resolveFieldName(gp.Xid, gp.Fields)

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n// %sNode — auto-generated graph projection of %s.\n", gp.Name, sourceFQN))
	sb.WriteString(fmt.Sprintf("message %s {\n", nodeName))
	sb.WriteString(fmt.Sprintf("  option (spec.graph_type) = {name: %q, xid_field: %q, source: %q};\n\n", gp.Name, xidFieldName, sourceFQN))

	recordKey := sourceRecordKey(msg)
	recordKeyEmitted := false

	for i, f := range gp.Fields {
		fieldNum := i + 1
		fieldName := resolveOutputFieldName(f)
		protoType, err := resolveFieldType(f.Source, msg, fd)
		if err != nil {
			return "", fmt.Errorf("field %q: %w", f.Source, err)
		}

		// For edge fields, type is always string (holds the xid value).
		if f.Edge != "" {
			protoType = "string"
		}

		// Build field annotations.
		var anns []string
		anns = append(anns, fmt.Sprintf("(spec.projection_source) = %q", f.Source))

		// Mark the field that carries the source record's key (for federation/hydration).
		if f.Source == recordKey {
			anns = append(anns, "(spec.projection_record_key) = true")
			recordKeyEmitted = true
		}

		if f.Edge != "" {
			anns = append(anns, fmt.Sprintf("(spec.graph_edge) = %q", f.Edge))
		}
		if f.Index != "" {
			anns = append(anns, fmt.Sprintf("(spec.graph_index) = %q", f.Index))
		}

		// Build (spec.graph) annotation.
		if f.Include {
			anns = append(anns, "(spec.graph) = {mode: GRAPH_MODE_INCLUDE}")
		} else if f.Edge != "" {
			// Edge fields don't need a graph mode — graph_edge handles it.
		} else {
			var graphParts []string
			graphParts = append(graphParts, "mode: GRAPH_MODE_PREDICATE")
			if f.Index != "" {
				graphParts = append(graphParts, fmt.Sprintf("index: %q", f.Index))
			}
			if f.Upsert {
				graphParts = append(graphParts, "upsert: true")
			}
			anns = append(anns, fmt.Sprintf("(spec.graph) = {%s}", strings.Join(graphParts, ", ")))
		}

		sb.WriteString(fmt.Sprintf("  %s %s = %d [%s];\n", protoType, fieldName, fieldNum, strings.Join(anns, ", ")))
	}

	// Auto-inject the source record's key so the projection query can return record
	// keys (federation → hydrate). Only when the author didn't already project it.
	if !recordKeyEmitted {
		keyType, err := resolveFieldType(recordKey, msg, fd)
		if err != nil {
			return "", fmt.Errorf("record key %q for graph projection %q: %w", recordKey, gp.Name, err)
		}
		sb.WriteString(fmt.Sprintf("  %s %s = %d [(spec.projection_source) = %q, (spec.projection_record_key) = true, (spec.graph) = {mode: GRAPH_MODE_PREDICATE, index: \"exact\"}];\n",
			keyType, recordKey, len(gp.Fields)+1, recordKey))
	}

	sb.WriteString("}\n")
	return sb.String(), nil
}

// sourceRecordKey returns the source record's key field name: the record's
// declared (spec.record).key (surfaced as the record_key annotation), or
// defaultRecordKeyField when none is declared — the SAME default the RecordMap
// query plane uses, so a record and its projections agree on the key. If the named
// field does not exist on the message, projection generation fails loudly when it
// resolves the key's type (resolveFieldType), never emitting a projection whose
// record key cannot be hydrated.
func sourceRecordKey(msg parser.MessageDescriptor) string {
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

// generateTimeseriesProjection generates a timeseries record message from a TimeseriesProjectionEntry.
func generateTimeseriesProjection(tp model.TimeseriesProjectionEntry, sourceFQN string, msg parser.MessageDescriptor, fd *parser.FileDescriptor) (string, error) {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("\n// %s — auto-generated timeseries projection of %s.\n", tp.Name, sourceFQN))
	sb.WriteString(fmt.Sprintf("message %s {\n", tp.Name))
	sb.WriteString(fmt.Sprintf("  option (spec.timeseries_type) = {\n"))
	sb.WriteString(fmt.Sprintf("    source: %q\n", sourceFQN))
	if r := tp.Retention; r != nil {
		sb.WriteString(fmt.Sprintf("    retention: {timestamp_field: %q duration_seconds: %d}\n", r.TimestampField, r.DurationSeconds))
	}
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
		fieldName := resolveOutputFieldName(model.GraphProjectionFieldEntry{Source: f.Source, As: f.As})
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
		if f.Source == recordKey {
			anns = append(anns, "(spec.projection_record_key) = true")
			recordKeyEmitted = true
		}

		sb.WriteString(fmt.Sprintf("  %s %s = %d [\n    %s\n  ];\n", protoType, fieldName, fieldNum, strings.Join(anns, ",\n    ")))
	}

	// Auto-inject the source record's key so the projection query can return record
	// keys (federation → hydrate). Only when the author didn't already project it.
	if !recordKeyEmitted {
		keyType, err := resolveFieldType(recordKey, msg, fd)
		if err != nil {
			return "", fmt.Errorf("record key %q for timeseries projection %q: %w", recordKey, tp.Name, err)
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
	// Type may be package-qualified (e.g. "claudesensor.ToolUseEvent") — strip prefix.
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
func resolveOutputFieldName(f model.GraphProjectionFieldEntry) string {
	if f.As != "" {
		return f.As
	}
	parts := strings.Split(f.Source, ".")
	return parts[len(parts)-1]
}

// resolveFieldName finds the output name for the xid field in the projection.
func resolveFieldName(xidSource string, fields []model.GraphProjectionFieldEntry) string {
	for _, f := range fields {
		if f.Source == xidSource {
			return resolveOutputFieldName(f)
		}
	}
	// Fallback: last segment of the source path.
	parts := strings.Split(xidSource, ".")
	return parts[len(parts)-1]
}
