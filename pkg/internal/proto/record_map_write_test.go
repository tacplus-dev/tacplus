// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
)

// The record write surface (#163) on the plane's S2S half: per write-capable
// type the point read, the four row writes and the change feed, and across
// types the one atomic Apply. Protoc-backed, because what the wrappers consume
// is the PARSED surface — the (spec.record_entry) and (spec.record_op)
// stamps as the descriptor carries them — and a stamp that protoc refuses or
// the parser drops is the failure that matters.
func recordWriteFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	writeProto(t, dir, "store.proto", `syntax = "proto3";
package store;

import "sys/spec.proto";

option (spec.service) = {name: "stratum"};
option (spec.record_store) = {no_bus_ingest: true};

message Ping {
  string id = 1;
}

service StratumApi {
  option (spec.service_s2s) = {};
  rpc GetPing(Ping) returns (Ping);
}
`)
	writeProto(t, dir, "query.proto", `syntax = "proto3";

package query;

import "sys/spec.proto";

option (spec.trait) = {
  name: "query"
  host: "stratum"
  s2s_service: "RecordMap"
  c2s_service: "RecordQuery"
  auth_services: ["reader"]
};
`)
	writeProto(t, dir, "ident.proto", `syntax = "proto3";

package ident;

import "sys/spec.proto";

option (spec.service) = {name: "ident"};

// REMOTE, its own write and subscribe audiences.
message XidRow {
  option (spec.record) = {
    key: "xid"
    s2s: {auth_services: ["*"]}
    s2s_write: {auth_services: ["identity"]}
    s2s_subscribe: {auth_services: ["auditor"]}
    durability: DURABILITY_REMOTE
  };
  string xid = 1;
  string subject = 2;
  string tenant = 3 [(spec.store_namespace) = true];
}

// APPEND_ONLY, integer key, no declared durability (LOCAL), the trait-wide
// write audience.
message LinkRow {
  option (spec.record) = {
    key: "seq"
    s2s: {auth_services: ["*"]}
    mutability: MUTABILITY_APPEND_ONLY
  };
  int64 seq = 1;
  string xid = 2;
}

// PRIVATE: no write surface, no change feed.
message SecretRow {
  option (spec.record) = {
    key: "secret_id"
    owner: "owner"
    s2s: {auth_services: ["*"]}
    private: true
  };
  string secret_id = 1;
  string owner = 2 [(spec.query_field) = {op: QUERY_OP_EQ}, (spec.store_principal) = true];
}
`)
	return dir
}

func rpcBlock(t *testing.T, text, service string) string {
	t.Helper()
	start := strings.Index(text, "\nservice "+service+" {")
	if start < 0 {
		t.Fatalf("service %s not emitted", service)
	}
	end := strings.Index(text[start+1:], "\n}\n")
	return text[start : start+1+end]
}

func TestRecordWriteSurfaceEmitted(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := recordWriteFixture(t)
	if err := AppendRecordMapServiceToProtos(dir, dir, "ident"); err != nil {
		t.Fatalf("AppendRecordMapServiceToProtos: %v", err)
	}
	out, err := os.ReadFile(filepath.Join(dir, "query.proto"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)
	s2s := rpcBlock(t, text, "RecordMap")
	c2s := rpcBlock(t, text, "RecordQuery")

	for _, want := range []string{
		// XidRow: everything, its own audiences and REMOTE floor.
		"rpc GetXidRow(GetXidRowRequest) returns (GetXidRowResponse)",
		"rpc PutXidRow(PutXidRowRequest) returns (RecordWriteResult)",
		"rpc InsertIfAbsentXidRow(InsertIfAbsentXidRowRequest) returns (RecordWriteResult)",
		"rpc ReplaceIfUnchangedXidRow(ReplaceIfUnchangedXidRowRequest) returns (RecordWriteResult)",
		"rpc DeleteXidRow(DeleteXidRowRequest) returns (RecordWriteResult)",
		"rpc WatchXidRowChanges(WatchXidRowChangesRequest) returns (stream XidRowWatchEvent)",
		// LinkRow: append-only — read, insert, watch.
		"rpc GetLinkRow(GetLinkRowRequest) returns (GetLinkRowResponse)",
		"rpc InsertIfAbsentLinkRow(InsertIfAbsentLinkRowRequest) returns (RecordWriteResult)",
		"rpc WatchLinkRowChanges(WatchLinkRowChangesRequest) returns (stream LinkRowWatchEvent)",
		// Apply: the union of the write audiences.
		`rpc Apply(RecordApplyRequest) returns (RecordApplyResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: ["identity", "reader"]};`,
	} {
		if !strings.Contains(s2s, want) {
			t.Errorf("S2S half missing %q", want)
		}
	}
	for _, absent := range []string{
		"PutLinkRow", "ReplaceIfUnchangedLinkRow", "DeleteLinkRow(", // append-only
		"SecretRow(", "WatchSecretRowChanges", "InsertIfAbsentSecretRow", // private
	} {
		if strings.Contains(s2s, absent) {
			t.Errorf("S2S half must not carry %q", absent)
		}
	}
	// No C2S writes in 2.5 (D3).
	for _, absent := range []string{"PutXidRow", "InsertIfAbsent", "Apply(", "WatchXidRowChanges", "GetXidRow("} {
		if strings.Contains(c2s, absent) {
			t.Errorf("C2S half must carry no write surface, found %q", absent)
		}
	}
	// The audiences: s2s_write on the writes, s2s_read fallback on Get, s2s_subscribe on Watch.
	for _, want := range []*regexp.Regexp{
		regexp.MustCompile(`rpc PutXidRow\(PutXidRowRequest\) returns \(RecordWriteResult\) \{\n    option \(spec.rpc\) = \{principal: RPC_PRINCIPAL_SERVICE, auth_services: \["identity"\]\};\n    option \(spec.record_entry\) = \{record: "ident.XidRow", durability: DURABILITY_REMOTE, key_path: "item.xid", namespace_path: "item.tenant"\};`),
		regexp.MustCompile(`rpc InsertIfAbsentLinkRow\(InsertIfAbsentLinkRowRequest\) returns \(RecordWriteResult\) \{\n    option \(spec.rpc\) = \{principal: RPC_PRINCIPAL_SERVICE, auth_services: \["reader"\]\};\n    option \(spec.record_entry\) = \{record: "ident.LinkRow", durability: DURABILITY_LOCAL, key_path: "item.seq"\};`),
		regexp.MustCompile(`rpc GetXidRow\(GetXidRowRequest\) returns \(GetXidRowResponse\) \{\n    option \(spec.rpc\) = \{principal: RPC_PRINCIPAL_SERVICE, auth_services: \["reader"\]\};`),
		regexp.MustCompile(`rpc WatchXidRowChanges\(WatchXidRowChangesRequest\) returns \(stream XidRowWatchEvent\) \{\n    option \(spec.rpc\) = \{principal: RPC_PRINCIPAL_SERVICE, auth_services: \["auditor"\]\};\n    option \(spec.method_collection\) = \{mode: COLLECTION_MODE_WATCH`),
	} {
		if !want.MatchString(s2s) {
			t.Errorf("S2S half does not match %s", want)
		}
	}
	for _, want := range []string{
		"enum RecordDurability {",
		"message RecordWriteResult {\n  int64 revision = 1;\n  RecordDurability durability = 2;\n}",
		"message GetLinkRowRequest {\n  int64 seq = 1;\n}",
		"message DeleteXidRowRequest {\n  string xid = 1;\n  RecordDurability durability = 2;\n  string tenant = 3;\n}",
		"message GetXidRowRequest {\n  string xid = 1;\n  string tenant = 2;\n}",
		"oneof expected {\n    int64 expected_revision = 2;\n    ident.XidRow expected_item = 3;\n  }",
		"message WatchXidRowChangesRequest {\n  int64 from_revision = 1;\n  string tenant = 2;\n}",
		"message WatchLinkRowChangesRequest {\n  int64 from_revision = 1;\n}",
		"message RecordApplyRequest {",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("trait proto missing %q", want)
		}
	}

	// The parsed surface the wrappers consume.
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("the injected plane must compile: %v", err)
	}
	info, err := dp.ConvertToProtoInfo("query.proto")
	if err != nil {
		t.Fatalf("ConvertToProtoInfo: %v", err)
	}
	var methods map[string]*parser.MethodInfo
	for _, svc := range info.Services {
		if svc.Name == "RecordMap" {
			methods = map[string]*parser.MethodInfo{}
			for _, m := range svc.Methods {
				methods[m.Name] = m
			}
		}
	}
	if methods == nil {
		t.Fatal("RecordMap not parsed")
	}
	entry := func(name string) *parser.RecordEntry {
		t.Helper()
		m := methods[name]
		if m == nil || m.RecordEntry == nil {
			t.Fatalf("%s carries no (spec.record_entry) stamp", name)
		}
		return m.RecordEntry
	}
	if got := entry("PutXidRow"); got.Durability != spec.Durability_DURABILITY_REMOTE || got.Record != "ident.XidRow" ||
		strings.Join(got.NamespacePath, ".") != "item.tenant" || strings.Join(got.KeyPath, ".") != "item.xid" || got.KeyKind != "string" {
		t.Errorf("PutXidRow stamp = %+v", got)
	}
	if got := entry("DeleteXidRow"); got.Durability != spec.Durability_DURABILITY_REMOTE || strings.Join(got.NamespacePath, ".") != "tenant" {
		t.Errorf("DeleteXidRow stamp = %+v (a key-addressed request carries the tenant beside the key)", got)
	}
	if got := entry("InsertIfAbsentLinkRow"); got.Durability != spec.Durability_DURABILITY_LOCAL || got.NamespacePath != nil || got.KeyKind != "int64" {
		t.Errorf("InsertIfAbsentLinkRow stamp = %+v (unset durability resolves to LOCAL; no tenant field)", got)
	}
	if got := entry("GetXidRow"); got.Durability != spec.Durability_DURABILITY_UNSPECIFIED || strings.Join(got.NamespacePath, ".") != "tenant" {
		t.Errorf("a read carries no durability, and its tenant: %+v", got)
	}
	if got := entry("WatchXidRowChanges"); got.Durability != spec.Durability_DURABILITY_UNSPECIFIED || strings.Join(got.NamespacePath, ".") != "tenant" {
		t.Errorf("WatchXidRowChanges stamp = %+v", got)
	}
	if methods["CreateXidRowQuery"].RecordEntry != nil || methods["Apply"].RecordEntry != nil {
		t.Error("only the single-type write surface RPCs carry (spec.record_entry)")
	}
	ops := methods["Apply"].RecordApplyOps
	byCase := map[string]parser.RecordEntry{}
	for _, op := range ops {
		byCase[op.CaseField] = op
	}
	if len(ops) != 5 {
		t.Fatalf("Apply must carry 4 XidRow ops + 1 LinkRow op, got %d: %+v", len(ops), ops)
	}
	put := byCase["put_xid_row"]
	if put.Record != "ident.XidRow" || put.RPC != "PutXidRow" || put.Durability != spec.Durability_DURABILITY_REMOTE ||
		strings.Join(put.KeyPath, ".") != "item.xid" || put.KeyKind != "string" || put.OpsField != "ops" || put.OneofName != "op" ||
		strings.Join(put.NamespacePath, ".") != "item.tenant" || put.OpType != "query.RecordOp" {
		t.Errorf("put_xid_row op = %+v", put)
	}
	del := byCase["delete_xid_row"]
	if strings.Join(del.KeyPath, ".") != "xid" || del.KeyKind != "string" || strings.Join(del.NamespacePath, ".") != "tenant" {
		t.Errorf("delete_xid_row keys by the request's own key field, got %+v", del)
	}
	ins := byCase["insert_if_absent_link_row"]
	if ins.RPC != "InsertIfAbsentLinkRow" || ins.KeyKind != "int64" || ins.Durability != spec.Durability_DURABILITY_LOCAL {
		t.Errorf("insert_if_absent_link_row op = %+v", ins)
	}
	if _, ok := byCase["put_link_row"]; ok {
		t.Error("an append-only record must carry no Put op in Apply")
	}
}

// The RecordOp field numbers are derived from each type's full name, so they do
// not move when another type joins the build. A writer built before a type was
// added must still address the same ops.
func TestRecordOpNumbersIndependentOfTheRecordSet(t *testing.T) {
	a := recordType{typeName: "XidRow", pkg: "ident", key: "xid", keyType: "string", durability: "DURABILITY_LOCAL"}
	b := recordType{typeName: "AaaRow", pkg: "aaa", key: "id", keyType: "string", durability: "DURABILITY_LOCAL"}
	caseLine := regexp.MustCompile(`put_xid_row = (\d+)`)
	alone, err := recordWriteMessages([]recordType{a})
	if err != nil {
		t.Fatal(err)
	}
	with, err := recordWriteMessages([]recordType{b, a})
	if err != nil {
		t.Fatal(err)
	}
	x, y := caseLine.FindStringSubmatch(alone), caseLine.FindStringSubmatch(with)
	if x == nil || y == nil || x[1] != y[1] {
		t.Fatalf("put_xid_row must keep its number when a type sorts ahead of it: alone %v, with %v", x, y)
	}
}

// The append-only wall sees the write surface's row writes, which carry the row
// in an envelope rather than as the RPC's own request.
func TestAssertNoRowMutatingRPCsRejectsWriteSurfaceRowWrite(t *testing.T) {
	emitted := "  rpc PutTaskActivity(PutTaskActivityRequest) returns (RecordWriteResult);\n"
	err := assertNoRowMutatingRPCs(emitted, appendOnlyRecords())
	if err == nil || !strings.Contains(err.Error(), "PutTaskActivityRequest") {
		t.Fatalf("a Put over an append-only record must fail generation, got: %v", err)
	}
	ok := "  rpc InsertIfAbsentTaskActivity(InsertIfAbsentTaskActivityRequest) returns (RecordWriteResult);\n"
	if err := assertNoRowMutatingRPCs(ok, appendOnlyRecords()); err != nil {
		t.Fatalf("InsertIfAbsent writes a row once and must generate for an append-only record: %v", err)
	}
}

// Composed names that collide fail with the records named, not in protoc.
func TestAssertNoDuplicateRPCs(t *testing.T) {
	emitted := "  rpc DeleteFooQuery(QueryHandle) returns (QueryStats);\n  rpc DeleteFooQuery(DeleteFooQueryRequest) returns (RecordWriteResult);\n"
	if err := assertNoDuplicateRPCs(emitted); err == nil || !strings.Contains(err.Error(), "DeleteFooQuery") {
		t.Fatalf("a duplicated composed RPC name must fail generation, got: %v", err)
	}
}

// s2s_write on a record whose key the surface cannot carry fails loud.
func TestRecordWriteRefusesUnkeyableRecord(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := recordWriteFixture(t)
	writeProto(t, dir, "bad.proto", `syntax = "proto3";

package bad;

import "sys/spec.proto";

message Key {
  string a = 1;
}

message BadRow {
  option (spec.record) = {
    key: "k"
    s2s: {auth_services: ["*"]}
    s2s_write: {auth_services: ["identity"]}
  };
  Key k = 1;
}
`)
	err := AppendRecordMapServiceToProtos(dir, dir, "ident")
	if err == nil || !strings.Contains(err.Error(), "bad.BadRow") || !strings.Contains(err.Error(), "s2s_write") {
		t.Fatalf("s2s_write on a record with a message key must fail, got: %v", err)
	}
}
