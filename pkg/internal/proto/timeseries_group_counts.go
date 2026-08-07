// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"go.uber.org/zap"
)

// toUpperSnake renders an identifier as SCREAMING_SNAKE for proto enum values
// ("SessionRecord" -> "SESSION_RECORD", "event_type" -> "EVENT_TYPE").

// tsGroupProjection is one timeseries_projection table that exposes a typed
// grouped-count query — the projection name (= table) plus its low-cardinality
// columns (the efficient, meaningful GROUP BY targets).
type tsGroupProjection struct {
	name      string   // projection/table type name, e.g. "SessionRecord"
	groupCols []string // low-cardinality column names, e.g. ["event_type", "tool_name"]
}

// AppendTimeseriesGroupCountsServiceToProtos scans all proto files for
// spec.timeseries_projection annotations and appends a TimeseriesGroupCounts
// service to the proto of every timeseries_store (timedb).
//
// TimeseriesGroupCounts is the typed grouped-analytics plane (counterpart to the
// RecordMap plane on the record store): for each projection table P that declares
// low-cardinality columns (cardinality_hint: CARDINALITY_LOW), it generates
//
//	rpc Get<P>GroupCounts(Get<P>GroupCountsRequest) returns (Get<P>GroupCountsResponse);
//
// where the request carries a <P>GroupField enum (one value per low-cardinality
// column) and a TimeRange, and the response is a typed repeated {key, count}. This
// replaces the common "count grouped by a low-cardinality field" raw query so
// callers (argus) stop emitting backend SQL for it and get a typed result.
// timedb translates it to a grouped count over the named column.
//
// Like its sibling injectors (AppendRecordMapServiceToProtos) this runs on EVERY
// service's codegen so the injected service is visible in every proto view
// (e.g. argus declares a grpc_s2s_client against timedb.TimeseriesGroupCounts);
// the injection is identical regardless of who is building, so selfPackage is
// unused. ExecuteQuery (raw SQL) stays for richer analytics — this is the typed
// fast-path for the group-count case, not a full replacement.
func AppendTimeseriesGroupCountsServiceToProtos(tempProtoDir, inputDir, selfPackage string) error {
	_ = selfPackage
	dp, err := parser.NewDescriptorParser(tempProtoDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser for timeseries group-counts injection: %w", err)
	}

	// Find every timeseries_store service proto (independent of who is building).
	// Like RecordMap, this is the store's OWN service (it lives in the store's
	// services/ proto), so the store implements get<Service>GrpcImpl directly.
	var serviceRelPaths []string
	for relPath, fd := range dp.GetAllFiles() {
		if fd.ServiceName != "" && fd.TimeseriesStore {
			serviceRelPaths = append(serviceRelPaths, relPath)
		}
	}
	if len(serviceRelPaths) == 0 {
		return nil
	}
	sort.Strings(serviceRelPaths)

	// Collect every timeseries_projection that has low-cardinality columns.
	var projs []tsGroupProjection
	seen := make(map[string]bool)
	for relPath, fd := range dp.GetAllFiles() {
		if IsInfraProto(relPath) {
			continue
		}
		for _, msg := range fd.Messages {
			ann, ok := msg.Annotations["timeseries_projection"]
			if !ok {
				continue
			}
			for _, tp := range ann.TimeseriesProjectionEntries {
				var cols []string
				for _, f := range tp.Fields {
					if f.CardinalityHint != model.CardinalityLow {
						continue
					}
					col := f.As
					if col == "" {
						col = lastSourceSegment(f.Source)
					}
					cols = append(cols, col)
				}
				if len(cols) == 0 || seen[tp.Name] {
					continue
				}
				seen[tp.Name] = true
				projs = append(projs, tsGroupProjection{name: tp.Name, groupCols: cols})
			}
		}
	}
	if len(projs) == 0 {
		return nil
	}
	sort.Slice(projs, func(i, j int) bool { return projs[i].name < projs[j].name })

	for _, serviceRelPath := range serviceRelPaths {
		if err := appendTimeseriesGroupCountsToServiceProto(tempProtoDir, serviceRelPath, projs); err != nil {
			return err
		}
	}
	return nil
}

// lastSourceSegment returns the trailing segment of a projection source path
// ("tool_use.tool_name" -> "tool_name", "session_id" -> "session_id"), matching
// the default column name (As) the registry assigns.
func lastSourceSegment(source string) string {
	if idx := strings.LastIndex(source, "."); idx >= 0 {
		return source[idx+1:]
	}
	return source
}

// appendTimeseriesGroupCountsToServiceProto appends the TimeseriesGroupCounts
// service definition and its per-projection messages to one timeseries_store
// service proto file.
func appendTimeseriesGroupCountsToServiceProto(tempProtoDir, relPath string, projs []tsGroupProjection) error {
	filePath := filepath.Join(tempProtoDir, relPath)
	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read proto file %s: %w", relPath, err)
	}

	var sb strings.Builder
	sb.WriteString("\n// Auto-generated timeseries grouped-count plane from spec.timeseries_projection (tacplus)\n")
	sb.WriteString(`
message TimeseriesGroupCount {
  string key = 1;
  int64 count = 2;
}
`)

	for _, p := range projs {
		enumPrefix := strutil.ToUpperSnakeCase(p.name) + "_GROUP_FIELD"
		sb.WriteString(fmt.Sprintf("\nenum %sGroupField {\n", p.name))
		sb.WriteString(fmt.Sprintf("  %s_UNSPECIFIED = 0;\n", enumPrefix))
		for i, col := range p.groupCols {
			sb.WriteString(fmt.Sprintf("  %s_%s = %d;\n", enumPrefix, strutil.ToUpperSnakeCase(col), i+1))
		}
		sb.WriteString("}\n")
		sb.WriteString(fmt.Sprintf(`
message Get%[1]sGroupCountsRequest {
  %[1]sGroupField group_by = 1;
  TimeRange time_range = 2;
}

message Get%[1]sGroupCountsResponse {
  repeated TimeseriesGroupCount counts = 1;
}
`, p.name))
	}

	// The TimeseriesGroupCounts service. S2S, scoped to argus (the C2S front),
	// hosted by the timeseries store itself (its own services/ proto).
	sb.WriteString("\nservice TimeseriesGroupCounts {\n")
	sb.WriteString("  option (spec.service_s2s) = {auth_services: [\"argus\"]};\n")
	for _, p := range projs {
		sb.WriteString(fmt.Sprintf(
			"  rpc Get%[1]sGroupCounts(Get%[1]sGroupCountsRequest) returns (Get%[1]sGroupCountsResponse);\n",
			p.name))
	}
	sb.WriteString("}\n")

	var buf strings.Builder
	buf.Write(content)
	buf.WriteString(sb.String())
	if err := os.WriteFile(filePath, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write proto file %s: %w", relPath, err)
	}

	zap.L().Info("appended TimeseriesGroupCounts service",
		zap.String("file", relPath),
		zap.Int("projections", len(projs)))
	return nil
}
