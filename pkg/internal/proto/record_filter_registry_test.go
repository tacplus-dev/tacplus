// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/llmdesc"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// Golden cross-check: what the registry emits for a record field's filterability
// (llmdesc.FieldDescriptor.FilterMode -> registry.json "filter_mode") must agree
// with what the record-plane injector actually puts on the generated <T>Filter.
//
// A registry consumer has no compiled record type to read; it learns
// "is this field filterable, and EQ or RANGE" from the registry alone. If the two
// ever drift, the consumer builds a filter criterion the store cannot honor.
//
// Covers all three shapes in one fixture:
//   - agentsensor.AgentEvent — an EQ field, a RANGE field, and unannotated fields.
//   - tasks.TaskActivity     — EQ only (no RANGE criterion on its <T>Filter).
//   - tasks.TaskNote         — zero (spec.query_field) fields, so the injector emits
//     NO TaskNoteFilter at all; the registry must still describe every field, each
//     as not filterable.
func TestRegistryFilterModeMatchesGeneratedFilter(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := nestedRedactFixtureDir(t)
	writeProto(t, dir, filepath.Join("services", "stratum.proto"), `syntax = "proto3";
package stratum;

import "sys/spec.proto";

option (spec.service) = {name: "stratum"};
option (spec.record_store) = {};
`)
	writeProto(t, dir, filepath.Join("vservices", "agent-sensor.proto"), `syntax = "proto3";
package agentsensor;

import "sys/spec.proto";

message AgentEvent {
  option (spec.record) = {key: "event_id"};
  string event_id = 1;
  string spiffe_id = 2 [(spec.query_field) = {op: QUERY_OP_EQ}];
  int64 timestamp_ms = 3 [(spec.query_field) = {op: QUERY_OP_RANGE}];
  string payload = 4;
}
`)
	// The plane is emitted from the trait declaration, not from the record store,
	// so the fixture has to declare the trait that asks for it.
	writeProto(t, dir, filepath.Join("vservices", "query.proto"), `syntax = "proto3";
package taskquery;

import "sys/spec.proto";

option (spec.trait) = {
  id: 40
  name: "query"
  host: "stratum"
  s2s_service: "RecordMap"
  c2s_service: "RecordQuery"
  auth_services: ["reporter"]
};
`)
	writeProto(t, dir, filepath.Join("vservices", "tasks.proto"), `syntax = "proto3";
package tasks;

import "sys/spec.proto";

message TaskActivity {
  option (spec.record) = {key: "activity_id"};
  string activity_id = 1;
  string status = 2 [(spec.query_field) = {op: QUERY_OP_EQ}];
  string note = 3;
}

message TaskNote {
  option (spec.record) = {key: "note_id"};
  string note_id = 1;
  string body = 2;
}
`)

	// What the registry will say, straight off the descriptors llmdesc reads.
	// Keyed "<pkg>.<Type>.<field>" -> filter_mode, exactly one entry per
	// (record type, field) pair.
	wantFilterMode := map[string]string{
		"agentsensor.AgentEvent.event_id":     "",
		"agentsensor.AgentEvent.spiffe_id":    "eq",
		"agentsensor.AgentEvent.timestamp_ms": "range",
		"agentsensor.AgentEvent.payload":      "",
		"tasks.TaskActivity.activity_id":      "",
		"tasks.TaskActivity.status":           "eq",
		"tasks.TaskActivity.note":             "",
		"tasks.TaskNote.note_id":              "",
		"tasks.TaskNote.body":                 "",
	}

	allFiles := []model.ProtoFile{}
	for _, rel := range []string{
		filepath.Join("vservices", "agent-sensor.proto"),
		filepath.Join("vservices", "tasks.proto"),
	} {
		allFiles = append(allFiles, parseFixtureProtoFiles(t, dir, rel)...)
	}
	msgIdx := llmdesc.BuildMessageIndex(allFiles)
	enumIdx := llmdesc.BuildEnumIndex(allFiles)

	gotFilterMode := map[string]string{}
	for _, pf := range allFiles {
		for _, msg := range pf.Messages {
			desc := llmdesc.BuildTypeDescriptor(msg, pf.Package, msgIdx, enumIdx, make(map[string]bool))
			for _, fd := range desc.Fields {
				key := pf.Package + "." + msg.Name + "." + fd.Name
				gotFilterMode[key] = fd.FilterMode
			}
		}
	}

	// Every field of every record type is described — none silently omitted.
	if len(gotFilterMode) != len(wantFilterMode) {
		t.Fatalf("registry described %d fields, want %d: %v", len(gotFilterMode), len(wantFilterMode), gotFilterMode)
	}
	for key, want := range wantFilterMode {
		got, ok := gotFilterMode[key]
		if !ok {
			t.Fatalf("registry omitted field %s entirely", key)
		}
		if got != want {
			t.Errorf("registry filter_mode for %s = %q, want %q", key, got, want)
		}
	}

	// Now the other half of the golden: the proto the RecordMap injector actually
	// emits. The registry's claim must match it criterion for criterion.
	if err := AppendRecordMapServiceToProtos(dir, dir, ""); err != nil {
		t.Fatalf("AppendRecordMapServiceToProtos: %v", err)
	}
	injected, err := os.ReadFile(filepath.Join(dir, "vservices", "query.proto"))
	if err != nil {
		t.Fatal(err)
	}
	generated := string(injected)

	for _, rec := range []struct {
		pkg      string
		typeName string
		fields   []struct {
			name      string
			protoType string
		}
	}{
		{pkg: "agentsensor", typeName: "AgentEvent", fields: []struct {
			name      string
			protoType string
		}{{"event_id", "string"}, {"spiffe_id", "string"}, {"timestamp_ms", "int64"}, {"payload", "string"}}},
		{pkg: "tasks", typeName: "TaskActivity", fields: []struct {
			name      string
			protoType string
		}{{"activity_id", "string"}, {"status", "string"}, {"note", "string"}}},
		{pkg: "tasks", typeName: "TaskNote", fields: []struct {
			name      string
			protoType string
		}{{"note_id", "string"}, {"body", "string"}}},
	} {
		filterBody, hasFilter := filterMessageBody(generated, rec.typeName)

		anyFilterable := false
		for _, f := range rec.fields {
			if wantFilterMode[rec.pkg+"."+rec.typeName+"."+f.name] != "" {
				anyFilterable = true
			}
		}
		// A record type the registry calls entirely unfilterable must have no
		// <T>Filter message, and vice versa.
		if anyFilterable != hasFilter {
			t.Fatalf("%s.%s: registry says filterable=%v but generated %sFilter present=%v",
				rec.pkg, rec.typeName, anyFilterable, rec.typeName, hasFilter)
		}

		for _, f := range rec.fields {
			mode := wantFilterMode[rec.pkg+"."+rec.typeName+"."+f.name]
			eqCriterion := fmt.Sprintf("optional %s %s = ", f.protoType, f.name)
			fromCriterion := fmt.Sprintf("optional %s %s_from = ", f.protoType, f.name)
			toCriterion := fmt.Sprintf("optional %s %s_to = ", f.protoType, f.name)

			switch mode {
			case "eq":
				if !strings.Contains(filterBody, eqCriterion) {
					t.Errorf("%s.%s: registry says %q is EQ-filterable but %sFilter has no %q:\n%s",
						rec.pkg, rec.typeName, f.name, rec.typeName, eqCriterion, filterBody)
				}
				if strings.Contains(filterBody, fromCriterion) || strings.Contains(filterBody, toCriterion) {
					t.Errorf("%s.%s: registry says %q is EQ but %sFilter carries a range pair:\n%s",
						rec.pkg, rec.typeName, f.name, rec.typeName, filterBody)
				}
			case "range":
				if !strings.Contains(filterBody, fromCriterion) || !strings.Contains(filterBody, toCriterion) {
					t.Errorf("%s.%s: registry says %q is RANGE-filterable but %sFilter lacks the _from/_to pair:\n%s",
						rec.pkg, rec.typeName, f.name, rec.typeName, filterBody)
				}
				if strings.Contains(filterBody, eqCriterion) {
					t.Errorf("%s.%s: registry says %q is RANGE but %sFilter carries a bare EQ criterion:\n%s",
						rec.pkg, rec.typeName, f.name, rec.typeName, filterBody)
				}
			case "":
				if strings.Contains(filterBody, eqCriterion) || strings.Contains(filterBody, fromCriterion) || strings.Contains(filterBody, toCriterion) {
					t.Errorf("%s.%s: registry says %q is not filterable but %sFilter carries a criterion for it:\n%s",
						rec.pkg, rec.typeName, f.name, rec.typeName, filterBody)
				}
			default:
				t.Fatalf("unexpected filter mode %q in fixture expectations", mode)
			}
		}
	}
}

// filterMessageBody returns the body of the generated "message <typeName>Filter {...}"
// block, and whether the injector emitted one at all.
func filterMessageBody(generated, typeName string) (string, bool) {
	header := "message " + typeName + "Filter {"
	start := strings.Index(generated, header)
	if start < 0 {
		return "", false
	}
	rest := generated[start+len(header):]
	end := strings.Index(rest, "}")
	if end < 0 {
		return rest, true
	}
	return rest[:end], true
}
