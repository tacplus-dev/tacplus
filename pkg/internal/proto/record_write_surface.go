// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"fmt"
	"hash/fnv"
	"regexp"
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
)

// THE RECORD WRITE SURFACE (#163).
//
// Records were written by EventBus publish and only read through the plane.
// The record store now hosts a generated WRITE surface beside the query plane,
// on the plane's S2S half alone (no C2S write in 2.5: a user-token write needs
// an owner-pin rule for writes, which is its own decision). Per write-capable
// record type T, keyed by its record key K:
//
//	Get<T>(Get<T>Request{K})                       -> Get<T>Response{T, revision}   s2s_read audience
//	Put<T>(Put<T>Request{T})                       -> RecordWriteResult            s2s_write audience
//	InsertIfAbsent<T>(InsertIfAbsent<T>Request{T}) -> RecordWriteResult            ALREADY_EXISTS when present
//	ReplaceIfUnchanged<T>(...{T, expected})        -> RecordWriteResult            ABORTED when changed underneath
//	Delete<T>(Delete<T>Request{K})                 -> RecordWriteResult            NOT_FOUND when absent
//	Watch<T>Changes(Watch<T>ChangesRequest)        -> stream <T>WatchEvent         s2s_subscribe audience
//
// and, across every type, one atomic multi-entry operation:
//
//	Apply(RecordApplyRequest{repeated RecordOp}) -> RecordApplyResponse
//
// LOCKS ARE INTERNAL TO THE STORE (D1-B). There is no lock RPC: a caller never
// holds a lock across calls. Each write takes its entry's lock inside the
// store — including on an absent key, for InsertIfAbsent — and Apply takes
// every entry lock its ops name, all or nothing, in the CANONICAL ORDER:
// record type full name, then key. The order is part of the contract, not an
// implementation habit: Apply's ops must be listed in it, strictly ascending
// with no entry twice, and the generated store wrapper refuses a request that
// is not (INVALID_ARGUMENT). So every implementation takes the same locks in
// the same order and no two Applies deadlock.
//
// REVISION (D6). Every entry carries a store-assigned int64 revision,
// increasing per write; every write and Get<T> returns it, ReplaceIfUnchanged
// compares against it (or against the stored value), and Watch<T>Changes
// resumes from it. int64, not uint64, because it is the revision the
// generated WATCH collection's replay ring is keyed by and resumes from
// (from_revision is int64 in every language).
//
// DURABILITY. Every write of T is stamped with T's resolved durability floor
// ((spec.record_entry) on the single-type RPCs, (spec.record_op) on Apply's op
// cases). The generated wrapper refuses a request asking for less,
// passes the effective level to the handler, and never answers a write the
// store reports as acked below it. Watch<T>Changes delivers a write only after
// its ack (the handler's change feed is fed from acked writes).
//
// TENANT. A record with a (spec.store_namespace) field is held to a tenant.
// The stamps name the tenant field of every request (the row's own for a write
// that carries one, a tenant field beside the key for Get<T>, Delete<T> and
// Watch<T>Changes), and the generated wrapper holds a caller whose namespace
// is a tenant to its own tenant on it: unset is stamped, anything else
// refused. A /system peer keeps the fleet-wide reach
// the plane's S2S half gives it. The store answers a key-addressed request
// only for an entry in the named tenant.
//
// What is NOT emitted, and why:
//   - a private record: no write surface and no change feed. The peer-facing
//     surface has no principal to stamp as the row's owner, and its stream
//     would broadcast every principal's rows (ValidateRecordWriteAnnotations
//     refuses s2s_write on one).
//   - an APPEND_ONLY record: Get<T>, InsertIfAbsent<T> and Watch<T>Changes
//     only — nothing that changes a stored row (assertNoRowMutatingRPCs holds
//     the template to it).
//   - a record whose key is not a string or integer scalar: no write surface
//     (it cannot be keyed or lock-ordered); declaring s2s_write on one fails.

// recordWriteSharedMessages are emitted once per trait proto, beside the
// query plane's shared messages.
const recordWriteSharedMessages = `
enum RecordDurability {
  RECORD_DURABILITY_UNSPECIFIED = 0;
  RECORD_DURABILITY_MEMORY = 1;
  RECORD_DURABILITY_LOCAL = 2;
  RECORD_DURABILITY_REMOTE = 3;
}

message RecordWriteResult {
  int64 revision = 1;
  RecordDurability durability = 2;
}
`

// recordDurabilityLevels maps a resolved (spec.record).durability onto the
// injected RecordDurability enum the wire carries. The two enums share their
// numbers, so a wrapper compares them as integers.
var recordDurabilityLevels = map[string]string{
	"DURABILITY_MEMORY": "RECORD_DURABILITY_MEMORY",
	"DURABILITY_LOCAL":  "RECORD_DURABILITY_LOCAL",
	"DURABILITY_REMOTE": "RECORD_DURABILITY_REMOTE",
}

// recordDurability resolves a record's (spec.record).durability: unset is
// DURABILITY_LOCAL.
func recordDurability(msg *parser.MessageDescriptor) string {
	if d := msg.Annotations["record_durability"].StringValue; d != "" && d != "DURABILITY_UNSPECIFIED" {
		return d
	}
	return "DURABILITY_LOCAL"
}

// recordSubscribeAudience is the audience of a record's Watch<T>Changes: its
// s2s_subscribe auth_services minus the routing-only ones. noWatch is true when
// s2s_subscribe is declared and every name in it is routing-only.
func recordSubscribeAudience(msg *parser.MessageDescriptor) (audience []string, noWatch bool) {
	surface, ok := msg.Annotations["event_surface"]
	if !ok || len(surface.EventPublishEntries) == 0 {
		return nil, false
	}
	sub := surface.EventPublishEntries[0].MethodS2SSubscribe
	if sub == nil {
		return nil, false
	}
	routingOnly := make(map[string]bool, len(sub.RoutingOnlyServices))
	for _, s := range sub.RoutingOnlyServices {
		routingOnly[s] = true
	}
	for _, s := range sub.AuthServices {
		if !routingOnly[s] {
			audience = append(audience, s)
		}
	}
	if len(audience) == 0 {
		return nil, len(sub.AuthServices) > 0
	}
	return audience, false
}

// recordNamespaceField returns the record's (spec.store_namespace) string
// field, "" when it declares none.
func recordNamespaceField(msg *parser.MessageDescriptor) string {
	for i := range msg.Fields {
		f := &msg.Fields[i]
		if f.Annotations["store_namespace"].BoolValue && f.TypeKind == "scalar" && f.Type == "string" && !f.IsRepeated {
			return f.Name
		}
	}
	return ""
}

// entryBinding renders a (spec.record_entry)/(spec.record_op) binding literal.
// keyPath and nsPath are relative to the stamped message; rpc and durability
// are omitted when empty.
func entryBinding(r recordType, rpc, durability, keyPath, nsPath string) string {
	parts := []string{fmt.Sprintf("record: %q", r.pkg+"."+r.typeName)}
	if rpc != "" {
		parts = append(parts, fmt.Sprintf("rpc: %q", rpc))
	}
	if durability != "" {
		parts = append(parts, "durability: "+durability)
	}
	if keyPath != "" {
		parts = append(parts, fmt.Sprintf("key_path: %q", keyPath))
	}
	if nsPath != "" {
		parts = append(parts, fmt.Sprintf("namespace_path: %q", nsPath))
	}
	return "{" + strings.Join(parts, ", ") + "}"
}

// paths returns an op's key and tenant field paths, relative to its request.
func (op recordWriteOp) paths(r recordType) (keyPath, nsPath string) {
	if op.byKey {
		keyPath = r.key
		if r.namespace != "" {
			nsPath = r.namespace
		}
		return keyPath, nsPath
	}
	keyPath = "item." + r.key
	if r.namespace != "" {
		nsPath = "item." + r.namespace
	}
	return keyPath, nsPath
}

// nsLine renders a key-addressed request's tenant field, "" for a record with
// none.
func nsLine(r recordType, number int) string {
	if r.namespace == "" {
		return ""
	}
	return fmt.Sprintf("  string %s = %d;\n", r.namespace, number)
}

// writeKeyTypes are the key types the write surface can carry and every
// generated wrapper can order the same way (bytewise for a string, numerically
// for an integer).
var writeKeyTypes = map[string]bool{
	"string": true,
	"int32":  true, "int64": true, "uint32": true, "uint64": true,
	"sint32": true, "sint64": true, "fixed32": true, "fixed64": true,
	"sfixed32": true, "sfixed64": true,
}

// writeKeyType returns the proto type of the record's key field when the write
// surface can carry it, "" otherwise.
func writeKeyType(msg *parser.MessageDescriptor, key string) string {
	for i := range msg.Fields {
		f := &msg.Fields[i]
		if f.Name != key {
			continue
		}
		if f.IsRepeated || f.IsMap || f.TypeKind != "scalar" || !writeKeyTypes[f.Type] {
			return ""
		}
		return f.Type
	}
	return ""
}

// writeCapable reports whether the record gets the write surface.
func writeCapable(r recordType) bool {
	return !r.private && r.keyType != ""
}

// watchCapable reports whether the record gets Watch<T>Changes.
func watchCapable(r recordType) bool {
	return !r.private && !r.noWatch
}

// writeAuthLiteral is the audience of one record's write RPCs: its own
// s2s_write list, else the trait-wide one (the s2s_read fallback).
func writeAuthLiteral(plane traitPlane, r recordType) string {
	if len(r.writeAuth) > 0 {
		return authServicesLiteral(r.writeAuth)
	}
	return authServicesLiteral(plane.authServices)
}

// subscribeAuthLiteral is the audience of one record's Watch<T>Changes.
func subscribeAuthLiteral(plane traitPlane, r recordType) string {
	if len(r.subscribeAuth) > 0 {
		return authServicesLiteral(r.subscribeAuth)
	}
	return authServicesLiteral(plane.authServices)
}

// recordWriteOp is one row write of the surface, in the fixed order of its
// Apply op-case offset.
type recordWriteOp struct {
	verb     string // RPC verb and message prefix: Put, InsertIfAbsent, ...
	caseName string // Apply oneof case prefix: put, insert_if_absent, ...
	offset   int    // field-number offset of the op's case inside its type's block
	rowWrite bool   // changes a stored row (refused for an append-only type)
	byKey    bool   // the request carries the key, not the row
}

var recordWriteOps = []recordWriteOp{
	{verb: "Put", caseName: "put", offset: 0, rowWrite: true},
	{verb: "InsertIfAbsent", caseName: "insert_if_absent", offset: 1},
	{verb: "ReplaceIfUnchanged", caseName: "replace_if_unchanged", offset: 2, rowWrite: true},
	{verb: "Delete", caseName: "delete", offset: 3, rowWrite: true, byKey: true},
}

// opsFor returns the row writes the record gets: all four, or InsertIfAbsent
// alone for an append-only type.
func opsFor(r recordType) []recordWriteOp {
	var ops []recordWriteOp
	for _, op := range recordWriteOps {
		if op.rowWrite && r.appendOnly {
			continue
		}
		ops = append(ops, op)
	}
	return ops
}

// RecordOp field numbers. Apply's RecordOp is one oneof over every
// write-capable type in the fleet, and a writer and the store are generated
// from DIFFERENT builds whose record sets differ. A number assigned by position
// in the set would move when a type is added ahead of another, and a writer
// built before that addition would then send a Put of one type that the store
// decodes as an op on another. So a type's block of four numbers is derived
// from its full name alone: the same in every build that has the type, whatever
// else the build has.
const (
	recordOpBase      = 16
	recordOpBlockBits = 24
)

func recordOpBlock(fqn string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(fqn))
	return recordOpBase + int(h.Sum32()%(1<<recordOpBlockBits))*len(recordWriteOps)
}

// recordWriteMessages renders the per-type request/response messages of the
// write surface and the Apply envelope. A name-derived block that lands in the
// protobuf reserved range or on another type's block fails generation rather
// than renumbering anything.
func recordWriteMessages(plane traitPlane, records []recordType) (string, error) {
	route := plane.routeLine()
	var sb strings.Builder
	var applyCases []string
	blocks := map[int]string{}
	for _, r := range records {
		fq := r.pkg + "." + r.typeName
		if !r.private {
			sb.WriteString(fmt.Sprintf("\nmessage Watch%sChangesRequest {\n  int64 from_revision = 1;\n%s%s}\n", r.typeName, nsLine(r, 2), route))
		}
		if !writeCapable(r) {
			continue
		}
		sb.WriteString(fmt.Sprintf(`
message Get%[1]sRequest {
  %[3]s %[4]s = 1;
%[5]s%[6]s}

message Get%[1]sResponse {
  %[2]s item = 1;
  int64 revision = 2;
}
`, r.typeName, fq, r.keyType, r.key, nsLine(r, 2), route))
		block := recordOpBlock(fq)
		if prev, ok := blocks[block]; ok {
			return "", fmt.Errorf("records %s and %s derive the same RecordOp field-number block (%d) from their full names; "+
				"rename one of them", prev, fq, block)
		}
		if block+len(recordWriteOps) > 19000 && block < 20000 {
			return "", fmt.Errorf("record %s derives RecordOp field-number block %d, inside protobuf's reserved range 19000-19999; "+
				"rename the record", fq, block)
		}
		blocks[block] = fq
		snake := strutil.ToSnakeCase(r.typeName)
		for _, op := range opsFor(r) {
			req := op.verb + r.typeName + "Request"
			switch {
			case op.byKey:
				sb.WriteString(fmt.Sprintf("\nmessage %s {\n  %s %s = 1;\n  RecordDurability durability = 2;\n%s%s}\n", req, r.keyType, r.key, nsLine(r, 3), route))
			case op.verb == "ReplaceIfUnchanged":
				sb.WriteString(fmt.Sprintf(`
message %[1]s {
  %[2]s item = 1;
  oneof expected {
    int64 expected_revision = 2;
    %[2]s expected_item = 3;
  }
  RecordDurability durability = 4;
%[3]s}
`, req, fq, route))
			default:
				sb.WriteString(fmt.Sprintf("\nmessage %s {\n  %s item = 1;\n  RecordDurability durability = 2;\n%s}\n", req, fq, route))
			}
			keyPath, nsPath := op.paths(r)
			applyCases = append(applyCases, fmt.Sprintf("    %s %s_%s = %d [(spec.record_op) = %s];\n",
				req, op.caseName, snake, block+op.offset, entryBinding(r, op.verb+r.typeName, r.durability, keyPath, nsPath)))
		}
	}
	if len(applyCases) > 0 {
		sb.WriteString("\nmessage RecordOp {\n  oneof op {\n")
		for _, c := range applyCases {
			sb.WriteString(c)
		}
		sb.WriteString(`  }
}

message RecordApplyRequest {
  repeated RecordOp ops = 1 [(spec.assert_min_size) = 1, (spec.assert_max_size) = 256];
  RecordDurability durability = 2;
` + route + `}

message RecordApplyResponse {
  repeated RecordWriteResult results = 1;
}
`)
	}
	return sb.String(), nil
}

// recordWriteRPCs renders one record's write surface RPCs on the S2S half.
func recordWriteRPCs(plane traitPlane, r recordType) string {
	if r.private {
		return ""
	}
	var sb strings.Builder
	if watchCapable(r) {
		sb.WriteString(fmt.Sprintf(`  rpc Watch%[1]sChanges(Watch%[1]sChangesRequest) returns (stream %[1]sWatchEvent) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: %[3]s%[5]s};
    option (spec.method_collection) = {mode: COLLECTION_MODE_WATCH, order_by: [%[2]q]};
    option (spec.record_entry) = %[4]s;
  }
`, r.typeName, r.key, subscribeAuthLiteral(plane, r), entryBinding(r, "", "", "", r.namespace), plane.routeOption()))
	}
	if !writeCapable(r) {
		return sb.String()
	}
	sb.WriteString(fmt.Sprintf(`  rpc Get%[1]s(Get%[1]sRequest) returns (Get%[1]sResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: %[2]s%[4]s};
    option (spec.record_entry) = %[3]s;
  }
`, r.typeName, readAuthLiteral(plane, r), entryBinding(r, "", "", r.key, r.namespace), plane.routeOption()))
	auth := writeAuthLiteral(plane, r)
	for _, op := range opsFor(r) {
		keyPath, nsPath := op.paths(r)
		sb.WriteString(fmt.Sprintf(`  rpc %[1]s%[2]s(%[1]s%[2]sRequest) returns (RecordWriteResult) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: %[3]s%[5]s};
    option (spec.record_entry) = %[4]s;
  }
`, op.verb, r.typeName, auth, entryBinding(r, "", r.durability, keyPath, nsPath), plane.routeOption()))
	}
	return sb.String()
}

// recordApplyRPC renders Apply, when any record is write-capable. Its audience
// is the UNION of every write-capable type's write audience — whoever may make
// any one of its ops may call it — and the generated wrapper then authorizes
// EACH op exactly as the op's single-type RPC is authorized, so an Apply can
// never write a type its caller could not write directly.
func recordApplyRPC(plane traitPlane, records []recordType) string {
	seen := map[string]bool{}
	var union []string
	hasWrites := false
	for _, r := range records {
		if !writeCapable(r) {
			continue
		}
		hasWrites = true
		list := r.writeAuth
		if len(list) == 0 {
			list = plane.authServices
		}
		for _, s := range list {
			if !seen[s] {
				seen[s] = true
				union = append(union, s)
			}
		}
	}
	if !hasWrites {
		return ""
	}
	if seen["*"] {
		union = []string{"*"}
	}
	sort.Strings(union)
	return fmt.Sprintf(`  rpc Apply(RecordApplyRequest) returns (RecordApplyResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: %s%s};
  }
`, authServicesLiteral(union), plane.routeOption())
}

// assertNoDuplicateRPCs refuses a rendered plane half that names one RPC
// twice. The plane's names are composed from record names (Delete<T>Query
// closes a handle, Delete<T> deletes a row), so a record named FooQuery beside
// a record named Foo would otherwise reach protoc as a duplicate method with
// no word about which records caused it.
func assertNoDuplicateRPCs(planeText string) error {
	seen := map[string]bool{}
	var dups []string
	for _, m := range emittedRPCSignature.FindAllStringSubmatch(planeText, -1) {
		if seen[m[1]] {
			dups = append(dups, m[1])
		}
		seen[m[1]] = true
	}
	if len(dups) == 0 {
		return nil
	}
	sort.Strings(dups)
	return fmt.Errorf("record names compose to the same RPC name more than once (%s); rename one of the records involved",
		strings.Join(dups, ", "))
}

// rowWriteRequest matches a row-write request message of the write surface
// wherever it appears in rendered plane text: an RPC's request or an Apply
// op case. Captures the verb and the remainder (record name + "Request").
var rowWriteRequest = regexp.MustCompile(`\b(Put|ReplaceIfUnchanged|Delete)([A-Za-z0-9_]+)Request\b`)
