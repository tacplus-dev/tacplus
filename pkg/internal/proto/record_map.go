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

// traitPlane is a resolved (spec.trait) declaration that asks for the record
// query plane: the file the plane is emitted into, the contract name callers are
// entitled against, the physical service that serves the RPCs, the two sibling
// service names, and the peers allowed onto the S2S half.
//
// Every one of these was a generator constant before: the host was "whatever
// declares (spec.record_store)", the names were the literals RecordMap and
// RecordQuery, and the S2S audience was the literal ["argus"] repeated at eight
// call sites — an application service named in platform generator source, which
// stayed there for months after that service left the fleet because nothing in
// a build could see it was stale. A declaration can be deleted; a constant
// cannot.
type traitPlane struct {
	relPath      string   // proto file declaring the trait; the plane is appended here
	name         string   // trait name — the entitlement/audience key
	host         string   // routing label of the physical implementor
	s2sService   string   // emitted peer-facing service name
	c2sService   string   // emitted client-facing service name
	authServices []string // peer identities allowed to call the S2S half
}

// AppendRecordMapServiceToProtos scans all proto files for spec.record messages
// and appends the record query plane to the proto of every (spec.trait) that
// declares a record store as its host.
//
// The TRAIT is what asks for the plane. Nothing here knows the name of a store,
// a service or a peer: the host, the two emitted service names and the S2S
// audience all come off the declaration (see traitPlane). Delete the trait
// declaration and the plane is not emitted at all — no host to append to, no
// services, no fallback.
//
// The plane is the record store's per-record-type, per-caller REFINABLE QUERIES
// over immutable records, and it is emitted on BOTH caller surfaces as two
// sibling services — a service may declare only one surface, so one service
// cannot serve both (see the surface comment in appendRecordPlaneToTraitProto):
//
//	service <trait.s2s_service>  (spec.service_s2s)  peers, authorized by peer identity
//	service <trait.c2s_service>  (spec.service_c2s)  user tokens through the gateway,
//	                                                 authorized by service entitlements
//
// Both carry (spec.service_*).label = the trait's host, so the physical service
// implements them; callers are entitled against the TRAIT name, never the host
// label (the audience is the contract, not the implementor — see
// routeIdentityForService in the rust REST dispatch generator).
//
// Each record type T gets one query lifecycle per surface; the s2s spelling is
// the generated query vocabulary, the c2s spelling is its governed-verb twin:
//
//	Open<T>Query(...) -> QueryHandle       Create<T>Query(...)  snapshot or living query → handle
//	Refine<T>(...) -> QueryStats           Update<T>Query(...)  replace the predicate (filterable types)
//	Count<T>(QueryHandle) -> RecordCount   Get<T>Count(...)
//	Sample<T>(...) -> Sample<T>Response    Get<T>Sample(...)    seeded monte-carlo draw
//	Pull<T>(...) -> Pull<T>Response        List<T>(...)         paged read
//	Watch<T>(QueryHandle) -> stream ...    Watch<T>(...)        living-query changes
//	Close<T>(QueryHandle) -> QueryStats    Delete<T>Query(...)
//
// The set holds record REFERENCES; records are immutable, only the query predicate is
// mutable — Refine replaces it outright (no narrow-only enforcement: nothing compares
// the new predicate against the old one, so a caller entitled to Open is equally free
// to Refine to match-all; the RecordMap authorizer is per-RPC-method, not per-filter,
// so this is not a privilege escalation). Sample, Pull, and each Watch stream item return record
// data, and ALL THREE apply EGRESS DATA ENTITLEMENT — the store calls the generated
// Sanitize<T>(record, entitlement, hmacKey) with a per-caller entitlement derived
// from caller entitlements before returning data; the sanitized view is never baked
// into stored state. Count returns only a size. Both surfaces run the same
// lifecycle over the same handles; they differ only in who may call and how that
// caller is authorized. tacplus knows the typed query and its shape, not the
// query→execution transform (impl-side).
//
// NOT built here: the per-caller row filter (which record ROWS a c2s caller may
// see, as opposed to which FIELDS egress raw). The c2s surface is shaped so it
// can be added later — Create<T>Query is the lifecycle's only entry point, so a
// row predicate pinned there is carried by every call made against the handle.
//
// Like AppendStoreServiceToProtos, this runs on EVERY service's codegen so the
// injected service is visible in every proto view (a peer declaring a
// grpc_s2s_client against the trait's S2S half needs to see it). The injection
// is identical regardless of who is building, so selfPackage is unused.
//
// TODO (follow-on): broaden (union) alongside narrow-only Refine. (The egress
// data entitlement transform Sanitize<T> is generated in the per-type helpers.)
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

	// Find every trait that asks for this plane (independent of who is being
	// built). No trait, no plane — record collection above still runs, so the
	// record-key guard keeps its reach whether or not anything is emitted.
	planes, err := collectRecordQueryPlanes(dp)
	if err != nil {
		return err
	}

	for _, plane := range planes {
		if err := appendRecordPlaneToTraitProto(tempProtoDir, plane, records); err != nil {
			return err
		}
	}
	return nil
}

// collectRecordQueryPlanes resolves every (spec.trait) declaration whose host is
// a record store into the plane to emit for it, in deterministic file order.
//
// A trait with no host declares an identity only and asks for nothing. A trait
// whose host is a real service that is NOT a record store belongs to some other
// plane and is skipped here. A trait whose host names a label the proto set does
// not serve is a typo and fails loud, the same paired control
// ValidateC2SHostLabels applies to the emitted surface.
func collectRecordQueryPlanes(dp *parser.DescriptorParser) ([]traitPlane, error) {
	served := parser.PhysicalServingLabels(dp)
	recordStores := make(map[string]bool)
	for _, fd := range dp.GetAllFiles() {
		if fd.RecordStore && fd.ServiceName != "" {
			recordStores[fd.ServiceName] = true
		}
	}

	var relPaths []string
	for relPath, fd := range dp.GetAllFiles() {
		if fd.Trait != nil {
			relPaths = append(relPaths, relPath)
		}
	}
	sort.Strings(relPaths)

	var planes []traitPlane
	for _, relPath := range relPaths {
		fd, err := dp.GetFileDescriptor(relPath)
		if err != nil {
			return nil, fmt.Errorf("resolving trait declaration in %s: %w", relPath, err)
		}
		t := fd.Trait
		if t.Host == "" {
			continue
		}
		if !served[t.Host] {
			return nil, fmt.Errorf("trait %q in %s declares host %q but no loaded service serves that label",
				t.Name, relPath, t.Host)
		}
		if !recordStores[t.Host] {
			continue
		}
		// From here the trait HAS asked for the record query plane, so every
		// input the plane needs must be declared. Emitting a half-specified
		// plane is how the hardcoded audience survived unnoticed.
		if t.Name == "" {
			return nil, fmt.Errorf("trait in %s declares host %q but no name to entitle callers against", relPath, t.Host)
		}
		if t.S2SService == "" || t.C2SService == "" {
			return nil, fmt.Errorf("trait %q in %s must declare both s2s_service and c2s_service (got %q and %q)",
				t.Name, relPath, t.S2SService, t.C2SService)
		}
		if len(t.AuthServices) == 0 {
			return nil, fmt.Errorf("trait %q in %s declares no auth_services: the s2s half would be callable by nobody", t.Name, relPath)
		}
		planes = append(planes, traitPlane{
			relPath:      relPath,
			name:         t.Name,
			host:         t.Host,
			s2sService:   t.S2SService,
			c2sService:   t.C2SService,
			authServices: t.AuthServices,
		})
	}
	return planes, nil
}

// appendRecordPlaneToTraitProto appends the two sibling service definitions and
// required imports to the proto file that declares the trait.
func appendRecordPlaneToTraitProto(tempProtoDir string, plane traitPlane, records []recordType) error {
	relPath := plane.relPath
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

	// TWO CALLER SURFACES, ONE QUERY PLANE.
	//
	// A service declares exactly one caller surface — parser.ValidateServiceSurfaces
	// refuses a service that carries two — so the plane is emitted TWICE, once per
	// surface. This is the same split testapp echo.proto uses for Echo (c2s) and
	// Probe (s2s), and the shape that validator's own error message points at.
	//
	//   <s2s_service>  S2S. Reached by a peer service, authorized by peer identity
	//                  through the trait's declared auth_services.
	//   <c2s_service>  C2S. Reached with a user token through the gateway,
	//                  authorized by that token's entitlements for the TRAIT.
	//
	// The c2s half is not a convenience alias. The platform's wildcard grant
	// catalog is built from c2s RPCs ONLY (registry.collectEntitlementGrants skips
	// every non-c2s RPC), so an s2s-only surface has no grant vocabulary at all:
	// <PKG>_ANY for a record store expanded to nothing, and no grant string —
	// wildcard or exact — could authorize a user call into it. Emitting the c2s
	// sibling is what puts the query plane INTO the generated catalog, so
	// <PKG>_ANY becomes a real grant that grows with new record types by
	// generation rather than by hand.
	//
	// Both surfaces share every message above AND every RPC name. A c2s RPC must
	// classify against the closed verb registry — an unclassifiable c2s method is
	// a hard generation failure, not a warning — so the c2s half spells the
	// lifecycle in governed verbs. That is also what earns it capability wildcards
	// (<PKG>_READ_ANY / _WRITE_ANY / _PAGE_ANY / _WATCH_ANY) instead of only the
	// package-wide <PKG>_ANY. The s2s half spells it the SAME way, so one logical
	// operation carries one name on both surfaces:
	//
	//   Create<T>Query      CREATE / WRITE
	//   Update<T>Query      UPDATE / WRITE
	//   Get<T>Count         GET    / READ
	//   Get<T>Sample        GET    / READ
	//   List<T>             LIST   / PAGE
	//   Delete<T>Query      DELETE / WRITE
	//   Watch<T>            WATCH
	//   Subscribe<T>Count   ACTION / READ
	//
	// The s2s half was originally spelled Open/Count/Sample/Pull/Close/Refine; it
	// was renamed to the governed spelling above (controller ruling, 2026-08-24)
	// because the sentinel below makes the gate skip it either way, so matching
	// the c2s names is free and a divergent vocabulary is not.
	//
	// Create<T>Query is the lifecycle's single entry point: every later call
	// carries only the handle it returns, so a per-caller row filter can later be
	// attached there without reshaping the c2s RPC boundary.
	sb.WriteString(recordMapS2SService(plane, records))
	sb.WriteString(recordQueryC2SService(plane, records))

	var buf strings.Builder
	buf.Write(content)
	buf.WriteString(sb.String())
	if err := os.WriteFile(filePath, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write proto file %s: %w", relPath, err)
	}

	zap.L().Info("appended record query plane",
		zap.String("file", relPath),
		zap.String("trait", plane.name),
		zap.String("host", plane.host),
		zap.Int("record_types", len(records)))
	return nil
}

// authServicesLiteral renders a trait's declared S2S audience as the proto text
// list (spec.rpc).auth_services takes.
func authServicesLiteral(authServices []string) string {
	quoted := make([]string, len(authServices))
	for i, s := range authServices {
		quoted[i] = fmt.Sprintf("%q", s)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

// recordMapS2SService renders the S2S half of the record query plane: the
// service a peer reaches directly, authorized by peer identity through the
// trait's declared auth_services rather than by a user token's entitlements.
// Per-caller refinable queries over immutable records; GetSample/List/Watch
// apply egress redaction.
//
// It carries (spec.service_s2s).label = the trait's host, so the physical
// implementor picks it up through the existing hosted-service path rather than
// the plane having to live inside the host's own proto.
//
// The `tacplus:generated-service` sentinel marks this as synthesized so the
// EnforceVerbs gate skips it (cf. the state/opa injectors, which mark themselves
// the same way). Its RPC names nonetheless match the c2s sibling's governed
// spelling exactly — see the surface comment in appendRecordPlaneToTraitProto.
func recordMapS2SService(plane traitPlane, records []recordType) string {
	auth := authServicesLiteral(plane.authServices)
	var sb strings.Builder
	sb.WriteString("\n// tacplus:generated-service " + parser.SynthesizedKindRecordMap + "\nservice " + plane.s2sService + " {\n")
	sb.WriteString(fmt.Sprintf("  option (spec.service_s2s) = {label: %q};\n", plane.host))
	for _, r := range records {
		hasFilter := len(r.filters) > 0
		// Universal query lifecycle: create (snapshot or living) → count/sample/list → delete.
		sb.WriteString(fmt.Sprintf(`
  rpc Create%[1]sQuery(Open%[1]sQueryRequest) returns (QueryHandle) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: %[3]s};
  }
  rpc Get%[1]sCount(QueryHandle) returns (RecordCount) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: %[3]s};
  }
  rpc Get%[1]sSample(Sample%[1]sRequest) returns (Sample%[1]sResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: %[3]s};
  }
  rpc List%[1]s(Pull%[1]sRequest) returns (Pull%[1]sResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: %[3]s};
    option (spec.method_collection) = {mode: COLLECTION_MODE_PAGE, order_by: [%[2]q], default_page_size: 100};
  }
  rpc Delete%[1]sQuery(QueryHandle) returns (QueryStats) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: %[3]s};
  }
`, r.typeName, r.key, auth))
		if hasFilter {
			// UpdateQuery replaces the query's predicate in place (unrestricted — not
			// enforced narrow-only; see the package doc comment above).
			// Watch streams a living query's changes (snapshot then deltas).
			// SubscribeCount streams the live size.
			sb.WriteString(fmt.Sprintf(`  rpc Update%[1]sQuery(Refine%[1]sRequest) returns (QueryStats) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: %[3]s};
  }
  rpc Watch%[1]s(QueryHandle) returns (stream %[1]sWatchEvent) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: %[3]s};
    option (spec.method_collection) = {mode: COLLECTION_MODE_WATCH, order_by: [%[2]q]};
  }
  rpc Subscribe%[1]sCount(QueryHandle) returns (stream RecordCount) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: %[3]s};
  }
`, r.typeName, r.key, auth))
		}
	}
	sb.WriteString("}\n")
	return sb.String()
}

// recordQueryC2SService renders the C2S half: the same query lifecycle, spelled
// in governed verbs so it classifies, reachable with a user token through the
// gateway and authorized by that token's entitlements for the trait. It carries
// no (spec.rpc) auth override — the service-level auth_entitled_users default is
// the whole point, since that is what puts these RPCs in the generated grant
// catalog. See the surface comment in appendRecordPlaneToTraitProto for the
// per-RPC mapping and for why the c2s half has to exist at all.
//
// (spec.service_c2s).label names the host the gateway dials. The audience it
// checks the caller's token against is the file's (spec.trait).name, not this
// label — see routeIdentityForService.
func recordQueryC2SService(plane traitPlane, records []recordType) string {
	var sb strings.Builder
	sb.WriteString("\n// tacplus:generated-service " + parser.SynthesizedKindRecordMap + "\nservice " + plane.c2sService + " {\n")
	sb.WriteString(fmt.Sprintf("  option (spec.service_c2s) = {auth_entitled_users: true, label: %q};\n", plane.host))
	for _, r := range records {
		sb.WriteString(fmt.Sprintf(`
  rpc Create%[1]sQuery(Open%[1]sQueryRequest) returns (QueryHandle);
  rpc Get%[1]sCount(QueryHandle) returns (RecordCount);
  rpc Get%[1]sSample(Sample%[1]sRequest) returns (Sample%[1]sResponse);
  rpc List%[1]s(Pull%[1]sRequest) returns (Pull%[1]sResponse) {
    option (spec.method_collection) = {mode: COLLECTION_MODE_PAGE, order_by: [%[2]q], default_page_size: 100};
  }
  rpc Delete%[1]sQuery(QueryHandle) returns (QueryStats);
`, r.typeName, r.key))
		if len(r.filters) > 0 {
			sb.WriteString(fmt.Sprintf(`  rpc Update%[1]sQuery(Refine%[1]sRequest) returns (QueryStats);
  rpc Watch%[1]s(QueryHandle) returns (stream %[1]sWatchEvent) {
    option (spec.method_collection) = {mode: COLLECTION_MODE_WATCH, order_by: [%[2]q]};
  }
  rpc Subscribe%[1]sCount(QueryHandle) returns (stream RecordCount);
`, r.typeName, r.key))
		}
	}
	sb.WriteString("}\n")
	return sb.String()
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
