// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"bytes"
	"strings"
	"testing"
	"text/template"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

func renderRecordReaders(t *testing.T, data recordReaderFile) string {
	t.Helper()
	tmplContent, err := loadTemplate("record_readers.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	tmpl, err := template.New("recordreaders").Parse(tmplContent)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("execute: %v", err)
	}
	return buf.String()
}

func messageReaderEntry() recordReaderEntry {
	return recordReaderEntry{
		FnName: "message_record_reader", StructName: "MessageRecordReader",
		RecordFQN: "messaging.Message", RecordName: "Message",
		RecordPath:   "crate::types::services::messaging::Message",
		QueryPath:    "crate::types::vservices::query",
		HostAccessor: "stratum", ClientFn: "record_map_client",
		KeyField: "delivery_id", KeyRustType: "String", KeyIsString: true,
		CreateFn: "create_message_query", ListFn: "list_message_page",
		DeleteFn: "delete_message_query", FilterType: "MessageFilter",
		OpenReqType: "OpenMessageQueryRequest", PullReqType: "PullMessageRequest",
		HandleType: "QueryHandle", HasKeyFilter: true,
		ClientModPath: "vservices::query::record_map", ClientStruct: "RecordMapGrpcClient",
	}
}

func messageReaderFile(entries ...recordReaderEntry) recordReaderFile {
	seen := map[string]bool{}
	var clients []recordReaderClient
	for _, e := range entries {
		key := e.ClientModPath + "::" + e.ClientStruct
		if seen[key] {
			continue
		}
		seen[key] = true
		clients = append(clients, recordReaderClient{ModPath: e.ClientModPath, Struct: e.ClientStruct})
	}
	return recordReaderFile{
		RecordReaders: entries,
		QueryPathRoot: entries[0].QueryPath, Clients: clients,
		HandleType: entries[0].HandleType,
	}
}

// privateReaderEntry is the SECOND reader shape: a (spec.record).private record,
// whose data path is the query plane's C2S half rather than its s2s sibling —
// record_map.go emits no rpcs for a private type on the s2s half at all.
func privateReaderEntry() recordReaderEntry {
	return recordReaderEntry{
		FnName: "secret_record_reader", StructName: "SecretRecordReader",
		RecordFQN: "messaging.Secret", RecordName: "Secret",
		RecordPath:   "crate::types::services::messaging::Secret",
		QueryPath:    "crate::types::vservices::query",
		HostAccessor: "stratum", ClientFn: "record_query_client",
		KeyField: "secret_id", KeyRustType: "String", KeyIsString: true,
		CreateFn: "create_secret_query", ListFn: "list_secret_page",
		DeleteFn: "delete_secret_query", FilterType: "SecretFilter",
		OpenReqType: "OpenSecretQueryRequest", PullReqType: "PullSecretRequest",
		HandleType: "QueryHandle", HasKeyFilter: true,
		ClientModPath: "vservices::query::record_query", ClientStruct: "RecordQueryGrpcClient",
	}
}

// One service may read both kinds, so the file borrows BOTH plane clients and
// each reader struct holds the one its own record needs. Before the private
// record's read existed, every reader in a file shared one client type.
func TestRecordReadersRenderPerRecordPlaneClient(t *testing.T) {
	out := renderRecordReaders(t, messageReaderFile(messageReaderEntry(), privateReaderEntry()))

	for _, want := range []string{
		"use super::internal::grpc::clients::vservices::query::record_map::RecordMapGrpcClient;",
		"use super::internal::grpc::clients::vservices::query::record_query::RecordQueryGrpcClient;",
		"pub struct MessageRecordReader<'a> {\n    client: &'a RecordMapGrpcClient,",
		"pub struct SecretRecordReader<'a> {\n    client: &'a RecordQueryGrpcClient,",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated readers missing %q:\n%s", want, out)
		}
	}
}

// The reader collapses the plane's three-call lifecycle into get/list.
func TestRecordReaderRendersGetAndList(t *testing.T) {
	out := renderRecordReaders(t, messageReaderFile(messageReaderEntry()))

	for _, want := range []string{
		"pub struct MessageRecordReader<'a>",
		"pub async fn get(",
		"delivery_id: &str,",
		"Result<Option<Message>, tonic::Status>",
		"pub async fn list(",
		"Result<(Vec<Message>, String, i64), tonic::Status>",
		"create_message_query(OpenMessageQueryRequest",
		"list_message_page(PullMessageRequest",
		"delete_message_query(QueryHandle",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated reader missing %q:\n%s", want, out)
		}
	}
}

// The handle is released on BOTH paths. A Create whose handle is never Deleted
// leaks a server-side query, and the leak happens on exactly the path a
// hand-rolled version forgets: when the List between them FAILS. So the Delete
// must be emitted BEFORE the `?` that propagates the List's error — this is the
// whole reason the lifecycle is generated rather than left to callers.
func TestRecordReaderReleasesHandleOnFailurePath(t *testing.T) {
	out := renderRecordReaders(t, messageReaderFile(messageReaderEntry()))

	deleteIdx := strings.Index(out, "delete_message_query(")
	if deleteIdx == -1 {
		t.Fatal("reader must release the query handle")
	}
	// The List result is bound, not unwrapped, so the Delete runs first.
	if !strings.Contains(out, "let pull = self") {
		t.Errorf("List result must be BOUND before the handle is released, not unwrapped:\n%s", out)
	}
	propagateIdx := strings.Index(out, "\n        pull\n")
	if propagateIdx == -1 || propagateIdx < deleteIdx {
		t.Errorf("the List error must be propagated AFTER the handle is released:\n%s", out)
	}
	// The release itself must not short-circuit: a failed cleanup must not lose a
	// successful read.
	if !strings.Contains(out, "let _ = self") {
		t.Errorf("handle release must be best-effort, not `?`-propagated:\n%s", out)
	}
}

// A key the filter cannot carry means no point read. The reader omits get()
// rather than emitting a method that names a filter field that does not exist.
func TestRecordReaderOmitsGetWithoutKeyFilter(t *testing.T) {
	e := messageReaderEntry()
	e.HasKeyFilter = false
	out := renderRecordReaders(t, messageReaderFile(e))

	if strings.Contains(out, "pub async fn get(") {
		t.Errorf("get() must be omitted when the key is not a filter criterion:\n%s", out)
	}
	if !strings.Contains(out, "pub async fn list(") {
		t.Errorf("list() must still be emitted:\n%s", out)
	}
}

// A non-string key is passed by value, not as &str.
func TestRecordReaderNonStringKey(t *testing.T) {
	e := messageReaderEntry()
	e.KeyField, e.KeyRustType, e.KeyIsString = "seq", "i64", false
	out := renderRecordReaders(t, messageReaderFile(e))

	if !strings.Contains(out, "seq: i64,") {
		t.Errorf("a non-string key must be taken by value:\n%s", out)
	}
	if strings.Contains(out, "seq.to_string()") {
		t.Errorf("a non-string key must not be stringified:\n%s", out)
	}
}

// The key's Rust type comes off the record, and a key the filter cannot carry is
// reported as such rather than guessed at.
func TestRecordKeyRustType(t *testing.T) {
	msg := parser.MessageDescriptor{Fields: []parser.FieldDescriptor{
		{Name: "s", Type: "string", TypeKind: "scalar"},
		{Name: "n", Type: "int64", TypeKind: "scalar"},
		{Name: "m", Type: "Nested", TypeKind: "message"},
		{Name: "r", Type: "string", TypeKind: "scalar", IsRepeated: true},
	}}
	for _, tc := range []struct {
		field, want string
		isString    bool
		ok          bool
	}{
		{"s", "String", true, true},
		{"n", "i64", false, true},
		{"m", "", false, false},
		{"r", "", false, false},
		{"absent", "", false, false},
	} {
		got, isStr, ok := recordKeyRustType(&msg, tc.field)
		if got != tc.want || isStr != tc.isString || ok != tc.ok {
			t.Errorf("recordKeyRustType(%q) = (%q,%v,%v), want (%q,%v,%v)",
				tc.field, got, isStr, ok, tc.want, tc.isString, tc.ok)
		}
	}
}

// A record with (spec.deleted_flag) gets the soft-delete escape hatch: false —
// the plane's default — excludes flagged rows, so a caller that must see one
// (an audit list, a citation resolving to a deleted-but-not-yet-purged row) has
// to ask. The flag is set ON Open<T>QueryRequest rather than left defaulted,
// which is the only call in the lifecycle that establishes a predicate.
func TestRecordReaderThreadsIncludeDeleted(t *testing.T) {
	e := messageReaderEntry()
	e.HasDeletedFlag = true
	out := renderRecordReaders(t, messageReaderFile(e))

	for _, want := range []string{
		// get(), list() and query_page() each take it,
		"        delivery_id: &str,\n        include_deleted: bool,\n",
		"        page_token: impl Into<String>,\n        include_deleted: bool,\n",
		"        page_token: String,\n        include_deleted: bool,\n",
		// each passes it on,
		"self.query_page(Some(filter), 1, String::new(), include_deleted)",
		"self.query_page(filter, page_size, page_token.into(), include_deleted)",
		// and it lands on the request that installs the predicate.
		"OpenMessageQueryRequest {\n                filter,\n                standing: false,\n                include_deleted,\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated reader missing %q:\n%s", want, out)
		}
	}
}

// Without the marker the record has no soft-delete state, so there is nothing to
// include and the reader's API is exactly what it was before the flag existed.
// This is what makes the generator change additive: every record_read reader in
// the fleet today renders unchanged.
func TestRecordReaderWithoutDeletedFlagIsUnchanged(t *testing.T) {
	out := renderRecordReaders(t, messageReaderFile(messageReaderEntry()))

	if strings.Contains(out, "include_deleted") {
		t.Errorf("a record with no (spec.deleted_flag) must not mention include_deleted:\n%s", out)
	}
	for _, want := range []string{
		"        delivery_id: &str,\n    ) -> Result<Option<Message>, tonic::Status> {",
		"        page_token: impl Into<String>,\n    ) -> Result<(Vec<Message>, String, i64), tonic::Status> {",
		"        page_token: String,\n    ) -> Result<(Vec<Message>, String, i64), tonic::Status> {",
		"self.query_page(Some(filter), 1, String::new()).await?",
		"self.query_page(filter, page_size, page_token.into()).await\n",
		"OpenMessageQueryRequest {\n                filter,\n                standing: false,\n                ..Default::default()\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("unflagged reader's API changed; missing %q:\n%s", want, out)
		}
	}
}
