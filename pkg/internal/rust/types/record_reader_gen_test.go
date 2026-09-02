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

func messageReaderFile(e recordReaderEntry) recordReaderFile {
	return recordReaderFile{
		RecordReaders: []recordReaderEntry{e},
		QueryPathRoot: e.QueryPath, ClientModPath: e.ClientModPath,
		ClientStruct: e.ClientStruct, HandleType: e.HandleType,
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
