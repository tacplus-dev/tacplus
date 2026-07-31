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
	"go.uber.org/zap"
)

// defaultRecordKeyField is the record-key field name assumed when a record does
// not declare one via (spec.record).key. The RecordMap query plane AND the
// projection generators (projection_gen.go) share this ONE default so a record and
// its graph/timeseries projections always agree on which field is the record key.
// Sourced from model.DefaultRecordKeyField — the single canonical default shared
// with the registry (llmdesc) emitter.
const defaultRecordKeyField = model.DefaultRecordKeyField

// recordType is a single spec.record message that the record store (stratum)
// exposes through the generated query plane.
type recordType struct {
	typeName string // e.g. "AgentEvent"
	pkg      string // e.g. "agentsensor"
	relPath  string // e.g. "vservices/agent-sensor.proto"
	key      string // record key field, e.g. "event_id" (order_by for the Pull page)
	filters  []recordFilter
}

// recordFilter is a (spec.query_field) criterion on a record field — a typed
// filter element of the generated <T>Filter message.
type recordFilter struct {
	name      string // proto field name, e.g. "spiffe_id"
	protoType string // scalar proto type, e.g. "string", "int64"
	rangeOp   bool   // true = RANGE (emit <name>_from/_to); false = EQ
}

// AppendRecordMapServiceToProtos scans all proto files for spec.record messages
// and appends a RecordMap service to the proto of every record_store (stratum).
//
// RecordMap is the record-store query plane: per-record-type, per-caller REFINABLE
// QUERIES over immutable records. Each record type T gets:
//
//	rpc Open<T>Query(...) returns (QueryHandle);    // snapshot or living query → handle
//	rpc Refine<T>(...) returns (QueryStats);         // narrow the predicate (filterable types)
//	rpc Count<T>(QueryHandle) returns (RecordCount);
//	rpc Sample<T>(...) returns (Sample<T>Response);  // seeded monte-carlo draw
//	rpc Pull<T>(...) returns (Pull<T>Response);      // paged read
//	rpc Watch<T>(QueryHandle) returns (stream ...);  // living-query changes
//	rpc Close<T>(QueryHandle) returns (QueryStats);
//
// The set holds record REFERENCES; records are immutable, only the query predicate is
// mutable (narrow-only for now). Sample, Pull, and each Watch stream item return record
// data, and ALL THREE apply EGRESS DATA ENTITLEMENT — the store calls the generated
// Sanitize<T>(record, entitlement, hmacKey) with a per-caller entitlement derived
// from caller entitlements before returning data; the sanitized view is never baked
// into stored state. Count returns only a size. The service is S2S: callers reach it through argus
// (the C2S front), never directly. tacplus knows the typed query and its shape, not the
// query→execution transform (impl-side).
//
// Like its sibling injectors (AppendTimeseriesQueryServiceToProtos), this runs on
// EVERY service's codegen so the injected service is visible in every proto view
// (e.g. argus declares grpc_s2s_client against stratum.RecordMap). The injection is
// identical regardless of who is building, so selfPackage is unused.
//
// TODO (follow-on): broaden (union) alongside narrow-only Refine. (Timeseries
// federation returns record keys — see timeseries_query.go; the egress data
// entitlement transform Sanitize<T> is generated in the per-type helpers.)
// validateRecordKeyExists fails loud when a record's key names a field the record
// does not declare. Kept as a pure function (no descriptor parsing) so the guard
// has a protoc-free unit pin — the descriptor-backed integration test skips when
// protoc is absent, which would let a revert of this check pass CI in a
// protoc-less environment (NEW-16).
func validateRecordKeyExists(pkg, msgName, key string, fields []parser.FieldDescriptor) error {
	for i := range fields {
		if fields[i].Name == key {
			return nil
		}
	}
	return fmt.Errorf("record %s.%s: record key field %q does not exist "+
		"(set (spec.record).key to an existing field)", pkg, msgName, key)
}

func AppendRecordMapServiceToProtos(tempProtoDir, inputDir, selfPackage string) error {
	_ = selfPackage
	dp, err := parser.NewDescriptorParser(tempProtoDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser for record map injection: %w", err)
	}

	// Find every record_store service proto (independent of who is being built).
	// RecordMap is the store's OWN service (it lives in the store's services/ proto),
	// not a vservices/-dir hosted vservice, so it needs no host tag — the store
	// implements get<Service>GrpcImpl directly (like StratumState).
	var serviceRelPaths []string
	for relPath, fd := range dp.GetAllFiles() {
		if fd.ServiceName != "" && fd.RecordStore {
			serviceRelPaths = append(serviceRelPaths, relPath)
		}
	}
	if len(serviceRelPaths) == 0 {
		return nil
	}
	sort.Strings(serviceRelPaths)

	// Collect all spec.record messages across the fleet.
	var records []recordType
	for relPath, fd := range dp.GetAllFiles() {
		if IsInfraProto(relPath) {
			continue
		}
		for _, msg := range fd.Messages {
			if ann, ok := msg.Annotations["record"]; !ok || !ann.BoolValue {
				continue
			}
			key := defaultRecordKeyField
			if k, ok := msg.Annotations["record_key"]; ok && k.StringValue != "" {
				key = k.StringValue
			}
			// The key field MUST exist on the record — the query plane emits it as the
			// Pull/Watch order_by. Fail loud rather than silently emit an order_by on a
			// nonexistent field (mirrors the projection generators, which already do).
			if err := validateRecordKeyExists(fd.Package, msg.Name, key, msg.Fields); err != nil {
				return err
			}
			var filters []recordFilter
			for fi := range msg.Fields {
				f := &msg.Fields[fi]
				qf, ok := f.Annotations["query_field"]
				if !ok {
					continue
				}
				filters = append(filters, recordFilter{
					name:      f.Name,
					protoType: f.Type,
					rangeOp:   qf.StringValue == "range",
				})
			}
			records = append(records, recordType{
				typeName: msg.Name,
				pkg:      fd.Package,
				relPath:  relPath,
				key:      key,
				filters:  filters,
			})
		}
	}
	if len(records) == 0 {
		return nil
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].pkg != records[j].pkg {
			return records[i].pkg < records[j].pkg
		}
		return records[i].typeName < records[j].typeName
	})

	for _, serviceRelPath := range serviceRelPaths {
		if err := appendRecordMapToServiceProto(tempProtoDir, serviceRelPath, records); err != nil {
			return err
		}
	}
	return nil
}

// appendRecordMapToServiceProto appends the RecordMap service definition and
// required imports to one record_store service proto file.
func appendRecordMapToServiceProto(tempProtoDir, relPath string, records []recordType) error {
	filePath := filepath.Join(tempProtoDir, relPath)
	content, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("failed to read proto file %s: %w", relPath, err)
	}
	existing := string(content)

	// Import each record type's source proto (for the typed Pull response).
	importPaths := make(map[string]bool)
	for _, r := range records {
		importPaths[r.relPath] = true
	}
	var missingImports []string
	for importPath := range importPaths {
		quoted := fmt.Sprintf(`"%s"`, importPath)
		if !strings.Contains(existing, quoted) {
			missingImports = append(missingImports, fmt.Sprintf(`import "%s";`, importPath))
		}
	}
	sort.Strings(missingImports)
	if len(missingImports) > 0 {
		content = injectImports(content, missingImports)
	}

	var sb strings.Builder
	sb.WriteString("\n// Auto-generated record query plane from spec.record annotations (tacplus)\n")

	// Shared handle/stats/change messages (emitted once per service proto).
	sb.WriteString(`
message QueryHandle {
  string query_id = 1;
}

message QueryStats {
  string query_id = 1;
  int64 size = 2;
}

message RecordCount {
  int64 count = 1;
}

enum RecordChangeType {
  RECORD_CHANGE_TYPE_UNSPECIFIED = 0;
  RECORD_CHANGE_TYPE_ADD = 1;
  RECORD_CHANGE_TYPE_MODIFY = 2;
  RECORD_CHANGE_TYPE_DELETE = 3;
}
`)

	// Per-record-type request/response messages.
	for _, r := range records {
		fq := r.pkg + "." + r.typeName
		hasFilter := len(r.filters) > 0

		// Typed filter (only when the record declares queryable fields). The store
		// translates the filter into its query; the caller never writes raw queries.
		if hasFilter {
			sb.WriteString(fmt.Sprintf("\nmessage %sFilter {\n", r.typeName))
			n := 1
			for _, f := range r.filters {
				if f.rangeOp {
					sb.WriteString(fmt.Sprintf("  optional %s %s_from = %d;\n", f.protoType, f.name, n))
					n++
					sb.WriteString(fmt.Sprintf("  optional %s %s_to = %d;\n", f.protoType, f.name, n))
					n++
				} else {
					sb.WriteString(fmt.Sprintf("  optional %s %s = %d;\n", f.protoType, f.name, n))
					n++
				}
			}
			sb.WriteString("}\n")
		}

		// OpenQuery request: initial predicate (if the type is filterable) plus the
		// standing flag (one-time snapshot vs living query). Refine narrows it in place.
		if hasFilter {
			sb.WriteString(fmt.Sprintf(`
message Open%[1]sQueryRequest {
  %[1]sFilter filter = 1;
  bool standing = 2;
}

message Refine%[1]sRequest {
  string query_id = 1;
  %[1]sFilter filter = 2;
}
`, r.typeName))
		} else {
			sb.WriteString(fmt.Sprintf(`
message Open%[1]sQueryRequest {
  bool standing = 1;
}
`, r.typeName))
		}

		// Sample (seeded monte-carlo draw) + Pull (paged read) — like Watch, both apply
		// egress redaction (records are immutable; the released view is per-caller at read).
		sb.WriteString(fmt.Sprintf(`
message Sample%[1]sRequest {
  string query_id = 1;
  int32 n = 2;
  uint64 seed = 3;
}

message Sample%[1]sResponse {
  repeated %[2]s items = 1;
}

message Pull%[1]sRequest {
  string query_id = 1;
  int32 page_size = 2;
  string page_token = 3;
}

message Pull%[1]sResponse {
  repeated %[2]s items = 1;
  string next_page_token = 2;
  int64 total_count = 3;
}
`, r.typeName, fq))

		// Live watch envelope (only for filterable types — a living query needs a predicate).
		if hasFilter {
			sb.WriteString(fmt.Sprintf(`
message %[1]sWatchChange {
  RecordChangeType type = 1;
  %[2]s item = 2;
  int64 revision = 3;
}

message %[1]sWatchEvent {
  oneof event {
    %[2]s snapshot_item = 1;
    int64 snapshot_done = 2;
    %[1]sWatchChange delta = 3;
  }
}
`, r.typeName, fq))
		}
	}

	// The RecordMap service — S2S, scoped to argus (the C2S front); clients never
	// call stratum directly. Per-caller refinable queries over immutable records;
	// Sample/Pull/Watch apply egress redaction. It is the record store's own service
	// (emitted into its services/ proto), so the store implements getRecordMapGrpcImpl.
	//
	// The `tacplus:generated-service` sentinel marks this as synthesized so the
	// EnforceVerbs gate skips it — its query-lifecycle verbs (Open/Count/Sample/
	// Pull/Close/Refine) are a generated S2S vocabulary, not governed REST verbs
	// (cf. the state/opa injectors, which mark themselves the same way).
	sb.WriteString("\n// tacplus:generated-service\nservice RecordMap {\n")
	sb.WriteString("  option (spec.service_s2s) = {auth_services: [\"argus\"]};\n")
	for _, r := range records {
		hasFilter := len(r.filters) > 0
		// Universal query lifecycle: open (snapshot or living) → count/sample/pull → close.
		sb.WriteString(fmt.Sprintf(`
  rpc Open%[1]sQuery(Open%[1]sQueryRequest) returns (QueryHandle);
  rpc Count%[1]s(QueryHandle) returns (RecordCount);
  rpc Sample%[1]s(Sample%[1]sRequest) returns (Sample%[1]sResponse);
  rpc Pull%[1]s(Pull%[1]sRequest) returns (Pull%[1]sResponse) {
    option (spec.method_collection) = {mode: COLLECTION_MODE_PAGE, order_by: [%[2]q], default_page_size: 100};
  }
  rpc Close%[1]s(QueryHandle) returns (QueryStats);
`, r.typeName, r.key))
		if hasFilter {
			// Refine narrows the query's predicate in place (narrow-only for now).
			// Watch streams a living query's changes (snapshot then deltas).
			// SubscribeCount streams the live size.
			sb.WriteString(fmt.Sprintf(`  rpc Refine%[1]s(Refine%[1]sRequest) returns (QueryStats);
  rpc Watch%[1]s(QueryHandle) returns (stream %[1]sWatchEvent) {
    option (spec.method_collection) = {mode: COLLECTION_MODE_WATCH, order_by: [%[2]q]};
  }
  rpc Subscribe%[1]sCount(QueryHandle) returns (stream RecordCount);
`, r.typeName, r.key))
		}
	}
	sb.WriteString("}\n")

	var buf strings.Builder
	buf.Write(content)
	buf.WriteString(sb.String())
	if err := os.WriteFile(filePath, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write proto file %s: %w", relPath, err)
	}

	zap.L().Info("appended record query plane",
		zap.String("file", relPath),
		zap.Int("record_types", len(records)))
	return nil
}

// injectImports inserts import lines after the last existing import statement.
func injectImports(content []byte, imports []string) []byte {
	lines := strings.Split(string(content), "\n")
	lastImportIdx := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "import ") {
			lastImportIdx = i
		}
	}

	if lastImportIdx == -1 {
		// No imports — insert after package declaration.
		for i, line := range lines {
			if strings.HasPrefix(strings.TrimSpace(line), "package ") {
				lastImportIdx = i
				break
			}
		}
	}

	if lastImportIdx == -1 {
		lastImportIdx = 0
	}

	var result []string
	result = append(result, lines[:lastImportIdx+1]...)
	result = append(result, imports...)
	result = append(result, lines[lastImportIdx+1:]...)
	return []byte(strings.Join(result, "\n"))
}
