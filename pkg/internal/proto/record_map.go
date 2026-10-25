// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/verbs"
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
	// minGroupSize is (spec.record).min_group_size: the smallest result size the
	// record's size-only RPCs may disclose. 0 = undeclared, no stamp.
	minGroupSize int32
	// appendOnly is (spec.record).mutability = MUTABILITY_APPEND_ONLY: this
	// record's rows are written once. The plane emits no row-mutating RPC for
	// such a type, and assertNoRowMutatingRPCs holds the template to that.
	appendOnly bool
	// principal is the criterion naming the ROW'S OWNING PRINCIPAL, when the
	// record declares one AND it is EQ-queryable (so the plane has somewhere to
	// put it). The emitted <T>Filter carries (spec.store_principal) on that
	// criterion, which is what tells a c2s wrapper to pin the predicate to the
	// authenticated caller at Create<T>Query — the "per-caller row filter" this
	// file's surface comment describes as the lifecycle's single entry point.
	//
	// Empty when the record declares no principal, or declares one the plane
	// cannot filter on. The egress row filter still narrows the ROWS in that
	// case; what is lost is the narrowing of the SIZE-ONLY RPCs, which no egress
	// filter can reach.
	principal string
	// readAuth is (spec.record).s2s_read.auth_services: the services allowed to
	// READ this record type's stored rows. It REPLACES the trait-wide audience on
	// this record's RPCs alone, which is what makes the store's grants
	// per-service-per-record-type rather than fleet-wide (see the field comment
	// on RecordAnnotation.s2s_read).
	//
	// nil = the record declares no s2s_read and keeps the trait-wide list. A
	// DECLARED-but-empty list never reaches here: ValidateRecordReadAnnotations
	// fails the build first, on the same reasoning collectRecordQueryPlanes
	// refuses a trait with no auth_services — a read surface callable by nobody
	// is a declaration that did not mean what it says.
	readAuth []string
	// declaresRead records whether the s2s_read BLOCK was present, independent of
	// what it contained. It is one of the two gates on keyFilterCriterion below
	// (private is the other), so the key becomes an EQ criterion exactly when a
	// reader exists to ask for it.
	declaresRead bool
	// private is (spec.record).private: this record type's rows belong to exactly
	// one principal. It is what suppresses the type's RPCs on the plane's S2S
	// half (recordMapS2SService) — a peer-facing read has no caller principal to
	// compare a row's owner against, so there is nothing for the owner pin to be,
	// and emitting the read would hand every peer the record's audience names
	// every principal's rows in the tenant. The C2S half keeps every RPC: that
	// surface carries a user token, which is exactly the identity the pin needs,
	// and it is therefore the data path a private record's (spec.record_read).c2s
	// reader is generated against. It is also the second gate on the key filter
	// criterion, for the same reason: a private record can never declare the
	// s2s_read that folds a key in, and its c2s reader still needs a point read.
	private bool
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
//
// Beside the result-set lifecycle, every record type has one DIRECT read,
// Get<T>Rows: filter in, up to limit rows out (sorted by key), total and a
// truncated flag, and no state on any node — the read a service makes for one
// row or a handful, which a result set is overkill for and, being held by the
// node that opened it, could not survive a failover of the connection. A result
// set is for large data: open it, size it, refine it, page it.
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
// ALSO NOT built here, for the same reason: the SOFT-DELETE DEFAULT EXCLUSION.
// Open/Refine carry include_deleted (see the emission below), and false — the
// default — means "exclude rows whose (spec.deleted_flag) field is true". That
// exclusion is a query→execution decision, and this generator emits no
// execution: the plane's handler is an INTERFACE (<Service>Handler, emitted by
// the per-language service-wrapper generators) whose bodies the hosting service
// writes. Stratum generates its own bodies from its own proto scan
// (platform/services/stratum/codegen RecordMapImplGenerator, which builds the
// handle→filter registry and the per-type match<T>/list<T>/watch<T>), so the
// exclusion belongs there, gated on the flag this generator now emits. The
// generated egress row filters that DO drop rows in the wrapper
// ((spec.store_principal), rust/types/principal_gen.go) cannot host it: they see
// only the response and the request in hand, and for List/Sample/Watch/Count
// that request is a bare handle — the flag was fixed at Create, in state only
// the handler holds. Count has no rows to drop at all.
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
			// min_group_size is declared on the RECORD; the downstream generators
			// read it off the METHOD, so the injector propagates it onto the
			// size-only RPCs below (never re-derived from an RPC name).
			var minGroupSize int32
			if m, ok := msg.Annotations["record_min_group_size"]; ok {
				minGroupSize = int32(m.IntValue)
			}
			// mutability is declared on the RECORD and read by the emission
			// guard below; carried here so "is this type append-only" has one
			// answer on the plane, not one per call site.
			appendOnly := msg.Annotations["record_mutability"].StringValue == "MUTABILITY_APPEND_ONLY"
			// s2s_read: the record's own read audience, and the gate on the key
			// criterion below. Read BEFORE the key is folded into the filter list,
			// because whether the key becomes a filter depends on this.
			var readAuth []string
			var declaresRead bool
			if sr, ok := msg.Annotations["record_s2s_read"]; ok && sr.BoolValue {
				declaresRead = true
				readAuth = append([]string(nil), sr.StringSliceValue...)
			}
			// private is read BEFORE the key is folded in, for the same reason
			// declaresRead is: it is the second gate on the key criterion.
			private := msg.Annotations["record_private"].BoolValue
			// Point-read-by-key is the read surface's most basic question, and the
			// plane could not express it before: the key carries no
			// (spec.query_field) on any record in the fleet, so it never reached
			// <T>Filter. Fold it in as an EQ criterion when — and only when — a
			// reader is declared, which keeps this additive for every record that
			// declares no s2s_read. Skipped when the author already declared the
			// key queryable, so the criterion is never emitted twice.
			//
			// A PRIVATE record folds its key in without declaring s2s_read,
			// because it is forbidden to declare one
			// (ValidateRecordPrivateAnnotations) and its reader is nonetheless
			// real: (spec.record_read).c2s at C2S_SCOPE_PRINCIPAL reads it over
			// the plane's C2S half, which keeps the type's full lifecycle. Gating
			// the key on s2s_read alone would leave that reader's point read
			// compiling against a criterion that is never emitted.
			if declaresRead || private {
				filters = appendKeyFilterCriterion(filters, key, &msg)
			}
			principal := recordPrincipalCriterion(&msg, filters)
			records = append(records, recordType{
				typeName:     msg.Name,
				pkg:          fd.Package,
				relPath:      relPath,
				key:          key,
				filters:      filters,
				minGroupSize: minGroupSize,
				appendOnly:   appendOnly,
				principal:    principal,
				readAuth:     readAuth,
				declaresRead: declaresRead,
				private:      private,
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
		// s2s_service is the plane. c2s_service is OPTIONAL: a trait may host the
		// s2s half alone, and then no client-facing sibling is emitted and no
		// grant for it lands in the catalog. A service that wants its records
		// readable by users declares (spec.record_read).c2s and gets a scoped
		// <Svc>Records plane of its own instead of a share in this fleet-wide one.
		if t.S2SService == "" {
			return nil, fmt.Errorf("trait %q in %s must declare s2s_service (got %q)", t.Name, relPath, t.S2SService)
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
					opt := ""
					if f.name == r.principal {
						opt = " [(spec.store_principal) = true]"
					}
					sb.WriteString(fmt.Sprintf("  optional %s %s = %d%s;\n", f.protoType, f.name, n, opt))
					n++
				}
			}
			sb.WriteString("}\n")
		}

		// OpenQuery request: initial predicate (if the type is filterable) plus the
		// standing flag (one-time snapshot vs living query). Refine narrows it in place.
		//
		// include_deleted is the SOFT-DELETE ESCAPE HATCH, and it lives on exactly
		// these two messages because they are the only two that establish a
		// predicate: every later call in the lifecycle carries only the handle
		// (QueryHandle / query_id), so the flag is fixed once at Create and
		// replaced wholesale at Update, exactly like the filter it rides with.
		// Sample/List/Count/Watch/SubscribeCount need no flag of their own — they
		// would have nowhere to put one that the store could honor without
		// contradicting the handle it was given.
		//
		// FALSE — the proto3 default, and the default a caller gets by saying
		// nothing — EXCLUDES rows whose (spec.deleted_flag) field is true. That is
		// the decision's "excludes ... by default": a caller must ask for deleted
		// rows, for audit and citation cases, and Get-by-key returns them either
		// way. The exclusion itself is not generated here; see the
		// include_deleted note in the package doc comment above for who enforces
		// it and why it cannot be this generator.
		//
		// Emitted for EVERY record type, including one declaring no
		// (spec.deleted_flag) field. Such a type has no soft-delete state and so
		// nothing to exclude, which makes the flag inert rather than wrong, and a
		// uniform query-plane shape is worth more than eliding one bool: the fleet
		// annotation pass adds markers to existing records AFTER this lands, and a
		// conditional field would silently renumber a live message the moment a
		// record gained its marker.
		if hasFilter {
			sb.WriteString(fmt.Sprintf(`
message Open%[1]sQueryRequest {
  %[1]sFilter filter = 1;
  bool standing = 2;
  bool include_deleted = 3;
}

message Refine%[1]sRequest {
  string query_id = 1;
  %[1]sFilter filter = 2;
  bool include_deleted = 3;
}
`, r.typeName))
		} else {
			sb.WriteString(fmt.Sprintf(`
message Open%[1]sQueryRequest {
  bool standing = 1;
  bool include_deleted = 2;
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

message Get%[1]sRowsRequest {
%[3]s  int32 limit = 2;
  bool include_deleted = 3;
}

message Get%[1]sRowsResponse {
  repeated %[2]s items = 1;
  int64 total_count = 2;
  bool truncated = 3;
}
`, r.typeName, fq, rowsFilterField(r)))

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
	//                  OPTIONAL — omitted when the trait declares no c2s_service,
	//                  in which case everything below about the c2s half simply
	//                  does not happen for that trait.
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
	s2s := recordMapS2SService(plane, records)
	// Emitted only when the trait declares c2s_service. An s2s-only trait keeps
	// the peer plane and contributes nothing to the c2s grant catalog.
	c2s := ""
	if plane.c2sService != "" {
		c2s = recordQueryC2SService(plane, records)
	}
	// (spec.record).mutability generation end, on the surface the generator
	// itself emits. ValidateAppendOnlyMethods refuses an AUTHOR-declared row
	// mutation; this refuses a GENERATOR-emitted one, so a future edit that
	// teaches the template a row-write verb cannot ship it for an append-only
	// record with every gate still green.
	for _, half := range []string{s2s, c2s} {
		if half == "" {
			continue
		}
		if err := assertNoRowMutatingRPCs(half, records); err != nil {
			return fmt.Errorf("record query plane for trait %q in %s: %w", plane.name, relPath, err)
		}
	}
	sb.WriteString(s2s)
	sb.WriteString(c2s)

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

// readAuthLiteral renders the audience for ONE record's read RPCs on the s2s
// half: the record's own (spec.record).s2s_read.auth_services when it declares
// them, and the trait-wide list otherwise.
//
// The fallback is what keeps s2s_read additive. Every record in the fleet
// predates this field, and each one must keep emitting exactly the audience it
// emitted before, or adding the field to ONE record would silently rewrite the
// authorization of every other.
func readAuthLiteral(plane traitPlane, r recordType) string {
	if len(r.readAuth) > 0 {
		return authServicesLiteral(r.readAuth)
	}
	return authServicesLiteral(plane.authServices)
}

// appendKeyFilterCriterion folds a record's KEY field into its filter list as an
// EQ criterion, so the plane can express a point read.
//
// Returns the list unchanged when the author already declared the key
// queryable — the criterion is the same one either way, and emitting it twice
// would produce a duplicate field in <T>Filter — and when the key names a field
// whose type the filter cannot carry (a message, an enum, a repeated field). The
// caller has already proven the key EXISTS (validateRecordKeyExists), so a miss
// here is a type mismatch, not a typo, and it degrades to "no point read" rather
// than failing the build: list/query still work, and the record's other
// criteria are untouched.
func appendKeyFilterCriterion(filters []recordFilter, key string, msg *parser.MessageDescriptor) []recordFilter {
	for i := range filters {
		if filters[i].name == key {
			return filters
		}
	}
	for i := range msg.Fields {
		f := &msg.Fields[i]
		if f.Name != key {
			continue
		}
		if f.IsRepeated || f.IsMap || f.TypeKind != "scalar" {
			return filters
		}
		return append(filters, recordFilter{name: f.Name, protoType: f.Type, rangeOp: false})
	}
	return filters
}

// minGroupSizeOption renders the stamped method option for a record declaring
// (spec.record).min_group_size, indented for an RPC that already has an option
// body. Empty when the record declares no threshold — the annotation is
// additive, so an undeclared record's RPCs are emitted exactly as before.
//
// A negative threshold never reaches here: ValidateMinGroupSizeAnnotations
// rejects it at generation time.
func minGroupSizeOption(r recordType) string {
	if r.minGroupSize <= 0 {
		return ""
	}
	return fmt.Sprintf("    option (spec.min_group_size) = %d;\n", r.minGroupSize)
}

// minGroupSizeBody is the same stamp for the c2s half, whose RPCs carry no
// options at all: it is either the bare ";" terminator or the option body.
func minGroupSizeBody(r recordType) string {
	if r.minGroupSize <= 0 {
		return ";"
	}
	return fmt.Sprintf(" {\n%s  }", minGroupSizeOption(r))
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
	var sb strings.Builder
	sb.WriteString("\n// tacplus:generated-service " + parser.SynthesizedKindRecordMap + "\nservice " + plane.s2sService + " {\n")
	sb.WriteString(fmt.Sprintf("  option (spec.service_s2s) = {label: %q};\n", plane.host))
	for _, r := range records {
		// A (spec.record).private type gets NO peer-facing read, in any language.
		// Its whole guarantee is that a row is readable only by the principal who
		// owns it, and this surface has no principal: a peer calls it with its own
		// service identity, so the owner pin has nothing to compare a row against
		// and the only gate the wrapper generators can apply here is the
		// (spec.store_namespace) tenant backstop. That leaves every service the
		// record's audience names able to read every principal's rows in the
		// tenant — the exact disclosure `private` exists to prevent. Skipping the
		// type here is what makes Go, Java and Rust agree without a per-language
		// check: the RPCs the wrapper would have to refuse are never emitted.
		//
		// Paired control: ValidateRecordPrivateAnnotations refuses a private
		// record that declares (spec.record).s2s_read, so an author asking for a
		// peer read on such a type fails at the annotation rather than finding the
		// grant silently emitted nothing here.
		if r.private {
			continue
		}
		hasFilter := len(r.filters) > 0
		// The record's OWN read audience when it declares one, the trait's
		// otherwise. This one line is the whole of the per-service-per-record-type
		// grant: everything downstream — the Java and Rust wrappers' method auth
		// tables — already prefers a per-METHOD auth_services over the
		// service-level default, so scoping a record's read rights is a matter of
		// emitting a different list here, not of new enforcement anywhere.
		auth := readAuthLiteral(plane, r)
		// Universal query lifecycle: create (snapshot or living) → count/sample/list → delete.
		sb.WriteString(fmt.Sprintf(`
  rpc Create%[1]sQuery(Open%[1]sQueryRequest) returns (QueryHandle) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: %[3]s};
  }
  rpc Get%[1]sCount(QueryHandle) returns (RecordCount) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: %[3]s};
%[4]s  }
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
  rpc Get%[1]sRows(Get%[1]sRowsRequest) returns (Get%[1]sRowsResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: %[3]s};
  }
`, r.typeName, r.key, auth, minGroupSizeOption(r)))
		if hasFilter {
			// UpdateQuery replaces the query's predicate in place (unrestricted — not
			// enforced narrow-only; see the package doc comment above).
			// Watch streams a living query's changes (snapshot then deltas).
			// SubscribeCount streams the live size.
			sb.WriteString(fmt.Sprintf(`  rpc Update%[1]sQuery(Refine%[1]sRequest) returns (QueryStats) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: %[3]s};
%[4]s  }
  rpc Watch%[1]s(QueryHandle) returns (stream %[1]sWatchEvent) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: %[3]s};
    option (spec.method_collection) = {mode: COLLECTION_MODE_WATCH, order_by: [%[2]q]};
  }
  rpc Subscribe%[1]sCount(QueryHandle) returns (stream RecordCount) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: %[3]s};
%[4]s  }
`, r.typeName, r.key, auth, minGroupSizeOption(r)))
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
  rpc Get%[1]sCount(QueryHandle) returns (RecordCount)%[3]s
  rpc Get%[1]sSample(Sample%[1]sRequest) returns (Sample%[1]sResponse);
  rpc List%[1]s(Pull%[1]sRequest) returns (Pull%[1]sResponse) {
    option (spec.method_collection) = {mode: COLLECTION_MODE_PAGE, order_by: [%[2]q], default_page_size: 100};
  }
  rpc Delete%[1]sQuery(QueryHandle) returns (QueryStats);
  rpc Get%[1]sRows(Get%[1]sRowsRequest) returns (Get%[1]sRowsResponse);
`, r.typeName, r.key, minGroupSizeBody(r)))
		if len(r.filters) > 0 {
			sb.WriteString(fmt.Sprintf(`  rpc Update%[1]sQuery(Refine%[1]sRequest) returns (QueryStats)%[3]s
  rpc Watch%[1]s(QueryHandle) returns (stream %[1]sWatchEvent) {
    option (spec.method_collection) = {mode: COLLECTION_MODE_WATCH, order_by: [%[2]q]};
  }
  rpc Subscribe%[1]sCount(QueryHandle) returns (stream RecordCount)%[3]s
`, r.typeName, r.key, minGroupSizeBody(r)))
		}
	}
	sb.WriteString("}\n")
	return sb.String()
}

// rowsFilterField is the filter line of Get<T>RowsRequest: the record's typed
// filter when it declares query fields, nothing for a filterless type (whose
// direct read is "every row", bounded by limit).
func rowsFilterField(r recordType) string {
	if len(r.filters) == 0 {
		return ""
	}
	return fmt.Sprintf("  %sFilter filter = 1;\n", r.typeName)
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

// emittedRPCSignature matches an emitted RPC line, capturing the method name and
// its request and response message types (with any `stream` marker dropped).
var emittedRPCSignature = regexp.MustCompile(
	`(?m)^\s*rpc\s+([A-Za-z0-9_]+)\s*\(\s*(?:stream\s+)?([A-Za-z0-9_.]+)\s*\)\s*returns\s*\(\s*(?:stream\s+)?([A-Za-z0-9_.]+)\s*\)`)

// assertNoRowMutatingRPCs refuses a rendered plane half that carries a
// row-mutating RPC over an append-only record type.
//
// A ROW-MUTATING RPC for record T is an RPC whose verb resolves to the UPDATE or
// DELETE pattern and whose REQUEST OR RESPONSE MESSAGE IS T — the same definition
// parser.AppendOnlyRecordOnMethod applies to author-declared RPCs, so the two ends
// cannot drift into meaning different things. The plane's Update<T>Query and
// Delete<T>Query are deliberately NOT caught: they carry Refine<T>Request,
// QueryHandle and QueryStats, never T, because they mutate the query predicate
// and the query's lifetime rather than a stored row.
//
// This is a generator self-check, not an author diagnostic: today the template
// emits no row write at all, so it can only fire on a future template edit. That
// is exactly what it is for — the alternative is an append-only record acquiring
// a row-write RPC with every gate green.
func assertNoRowMutatingRPCs(planeText string, records []recordType) error {
	appendOnly := make(map[string]bool)
	for _, r := range records {
		if r.appendOnly {
			appendOnly[r.typeName] = true
			appendOnly[r.pkg+"."+r.typeName] = true
		}
	}
	if len(appendOnly) == 0 {
		return nil
	}
	var errs []string
	for _, m := range emittedRPCSignature.FindAllStringSubmatch(planeText, -1) {
		rpcName, reqType, respType := m[1], m[2], m[3]
		verb := leadingVerbOf(rpcName)
		if verb == "" {
			continue
		}
		def, ok := verbs.Lookup(verb)
		if !ok || (def.Pattern != verbs.PatternUpdate && def.Pattern != verbs.PatternDelete) {
			continue
		}
		for _, t := range []string{reqType, respType} {
			if appendOnly[t] {
				errs = append(errs, fmt.Sprintf(
					"rpc %s(%s) returns (%s) is a %s carrying %s, which declares "+
						"(spec.record).mutability = MUTABILITY_APPEND_ONLY; an append-only record's rows are "+
						"written once, so the plane must emit no row-mutating RPC for the type",
					rpcName, reqType, respType, def.Pattern, t))
				break
			}
		}
	}
	if len(errs) == 0 {
		return nil
	}
	sort.Strings(errs)
	return fmt.Errorf("append-only record mutation errors:\n  %s", strings.Join(errs, "\n  "))
}

// leadingVerbOf returns the longest registered canonical verb that prefixes an
// emitted RPC name, or "" when none does. Longest-match so "Subscribe" is not
// read as "Sub", and canonical-only so a folded synonym is not silently treated
// as its canonical (an unclassifiable name fails the verb gate elsewhere).
func leadingVerbOf(rpcName string) string {
	best := ""
	for v := range verbs.All() {
		if len(v) > len(best) && strings.HasPrefix(rpcName, v) {
			best = v
		}
	}
	return best
}

// recordPrincipalCriterion returns the name of the filter criterion that names
// the row's owning principal, or "" when the plane has none to pin.
//
// The row's principal is declared exactly as the egress filter reads it, most
// specific first: (spec.store_principal) on a record field, else
// (spec.record).owner. It only becomes a CRITERION when it is also EQ-queryable
// — a RANGE criterion cannot express "equal to me", and a field with no
// (spec.query_field) at all has no criterion in the emitted <T>Filter to carry
// the annotation.
//
// This is the SIZE-ONLY half of the row-level principal guarantee. The generated
// egress filter narrows the rows a c2s caller is handed; it cannot narrow
// Get<T>Count / Subscribe<T>Count / Update<T>Query's size, which return a number
// computed over the handle's predicate and no rows at all. Pinning the predicate
// at the lifecycle's single entry point is what narrows those too.
func recordPrincipalCriterion(msg *parser.MessageDescriptor, filters []recordFilter) string {
	if msg == nil {
		return ""
	}
	name := ""
	for i := range msg.Fields {
		if msg.Fields[i].Annotations["store_principal"].BoolValue {
			name = msg.Fields[i].Name
			break
		}
	}
	if name == "" {
		name = msg.Annotations["record_owner"].StringValue
	}
	if name == "" {
		return ""
	}
	for _, f := range filters {
		if f.name == name && !f.rangeOp && f.protoType == "string" {
			return name
		}
	}
	return ""
}
