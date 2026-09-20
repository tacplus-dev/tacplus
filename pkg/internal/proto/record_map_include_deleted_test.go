// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// renderRecordPlane writes a trait proto carrying only its (spec.trait)
// declaration, appends the query plane for records, and returns the emitted
// text. protoc-free: appendRecordPlaneToTraitProto renders and writes, so the
// message shapes can be pinned without a descriptor round-trip.
func renderRecordPlane(t *testing.T, plane traitPlane, records []recordType) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, plane.relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`syntax = "proto3";
package taskquery;

import "sys/spec.proto";
`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := appendRecordPlaneToTraitProto(dir, plane, records); err != nil {
		t.Fatalf("appendRecordPlaneToTraitProto: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

func includeDeletedPlane() traitPlane {
	return traitPlane{
		relPath:      filepath.Join("vservices", "query.proto"),
		name:         "query",
		host:         "stratum",
		s2sService:   "RecordMap",
		c2sService:   "RecordQuery",
		authServices: []string{"reporter"},
	}
}

// include_deleted rides on the two messages that ESTABLISH a predicate, and
// nowhere else. Open<T>QueryRequest fixes it at Create; Refine<T>Request
// replaces it at Update, wholesale, alongside the filter. Everything after that
// carries only the handle, so a flag on Sample/Pull would be a second predicate
// the store could not reconcile with the one the handle already names.
//
// Pinning the exact field numbers is the point, not incidental: these are wire
// numbers on a message the fleet already speaks, so a renumbering is a
// compatibility break that must not pass silently.
func TestRecordQueryPlaneEmitsIncludeDeletedOnPredicateMessages(t *testing.T) {
	records := []recordType{{
		typeName: "TaskActivity",
		pkg:      "tasks",
		relPath:  "vservices/tasks.proto",
		key:      "event_id",
		filters:  []recordFilter{{name: "task_id", protoType: "string"}},
	}}
	got := renderRecordPlane(t, includeDeletedPlane(), records)

	for _, want := range []string{
		// filter = 1, standing = 2, so the flag takes 3.
		"message OpenTaskActivityQueryRequest {\n  TaskActivityFilter filter = 1;\n  bool standing = 2;\n  bool include_deleted = 3;\n}",
		// query_id = 1, filter = 2, so the flag takes 3 here too.
		"message RefineTaskActivityRequest {\n  string query_id = 1;\n  TaskActivityFilter filter = 2;\n  bool include_deleted = 3;\n}",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("emitted plane missing:\n%s\n\ngot:\n%s", want, got)
		}
	}

	// The handle-only half of the lifecycle must stay handle-only.
	for _, msg := range []string{"SampleTaskActivityRequest", "PullTaskActivityRequest"} {
		body := messageBody(t, got, msg)
		if strings.Contains(body, "include_deleted") {
			t.Fatalf("%s carries include_deleted; the flag belongs to the predicate, "+
				"not to a call made against an already-open handle:\n%s", msg, body)
		}
	}
}

// A record declaring no queryable fields emits no <T>Filter and no Refine, so
// its Open request is the standing flag alone — and include_deleted still lands
// on it, at the next free number. The non-filterable variant is a separate
// emission branch in record_map.go; without its own pin, adding the flag to only
// the filterable branch would go green.
func TestRecordQueryPlaneEmitsIncludeDeletedWithoutFilter(t *testing.T) {
	records := []recordType{{
		typeName: "PlainRecord",
		pkg:      "tasks",
		relPath:  "vservices/tasks.proto",
		key:      "record_id",
	}}
	got := renderRecordPlane(t, includeDeletedPlane(), records)

	want := "message OpenPlainRecordQueryRequest {\n  bool standing = 1;\n  bool include_deleted = 2;\n}"
	if !strings.Contains(got, want) {
		t.Fatalf("emitted plane missing:\n%s\n\ngot:\n%s", want, got)
	}
	if strings.Contains(got, "message RefinePlainRecordRequest") {
		t.Fatal("a record with no queryable fields must emit no Refine request")
	}
}

// messageBody returns the text between "message <name> {" and its closing brace.
// The emitted messages are flat (no nested types), so the first "}" at column
// zero ends the body.
func messageBody(t *testing.T, text, name string) string {
	t.Helper()
	head := "message " + name + " {"
	i := strings.Index(text, head)
	if i < 0 {
		t.Fatalf("emitted plane declares no %s:\n%s", name, text)
	}
	rest := text[i:]
	j := strings.Index(rest, "\n}")
	if j < 0 {
		t.Fatalf("message %s is unterminated:\n%s", name, rest)
	}
	return rest[:j+2]
}
