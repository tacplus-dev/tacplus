// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

// agentEventSource returns a minimal record message (source of projections) with
// the given record_key annotation (empty string = default "event_id").
func agentEventSource(recordKey string) (parser.MessageDescriptor, *parser.FileDescriptor) {
	anns := map[string]model.AnnotationValue{}
	if recordKey != "" {
		anns["record_key"] = model.AnnotationValue{StringValue: recordKey}
	}
	msg := parser.MessageDescriptor{
		Name:        "AgentEvent",
		Annotations: anns,
		Fields: []parser.FieldDescriptor{
			{Name: "event_id", Type: "string", TypeKind: "scalar"},
			{Name: "agent_id", Type: "string", TypeKind: "scalar"},
			{Name: "email", Type: "string", TypeKind: "scalar"},
			{Name: "timestamp_ms", Type: "int64", TypeKind: "scalar"},
		},
	}
	fd := &parser.FileDescriptor{Package: "agentsensor", Messages: []parser.MessageDescriptor{msg}}
	return msg, fd
}

func TestGraphProjectionInjectsRecordKeyWhenAbsent(t *testing.T) {
	msg, fd := agentEventSource("")
	gp := model.GraphProjectionEntry{
		Name: "Session",
		Xid:  "email",
		Fields: []model.GraphProjectionFieldEntry{
			{Source: "email", Index: "exact"},
		},
	}

	got, err := generateGraphProjection(gp, "agentsensor.AgentEvent", msg, fd)
	if err != nil {
		t.Fatalf("generateGraphProjection failed: %v", err)
	}

	// The author projected only email; tacplus must inject event_id (field #2)
	// carrying the record key so the projection query can return record keys.
	want := `string event_id = 2 [(spec.projection_source) = "event_id", (spec.projection_record_key) = true, (spec.graph) = {mode: GRAPH_MODE_PREDICATE, index: "exact"}];`
	if !strings.Contains(got, want) {
		t.Fatalf("graph projection missing injected record key:\nwant substring: %s\ngot:\n%s", want, got)
	}
	if n := strings.Count(got, "projection_record_key"); n != 1 {
		t.Fatalf("expected exactly one projection_record_key marker, got %d:\n%s", n, got)
	}
}

func TestGraphProjectionMarksExistingRecordKeyField(t *testing.T) {
	msg, fd := agentEventSource("")
	gp := model.GraphProjectionEntry{
		Name: "Session",
		Xid:  "email",
		Fields: []model.GraphProjectionFieldEntry{
			{Source: "email", Index: "exact"},
			{Source: "event_id", Index: "exact"}, // author already projects the key
		},
	}

	got, err := generateGraphProjection(gp, "agentsensor.AgentEvent", msg, fd)
	if err != nil {
		t.Fatalf("generateGraphProjection failed: %v", err)
	}

	// The already-projected event_id field (field #2) is MARKED in place, not duplicated.
	if !strings.Contains(got, "(spec.projection_record_key) = true") {
		t.Fatalf("existing record-key field not marked:\n%s", got)
	}
	if n := strings.Count(got, "projection_record_key"); n != 1 {
		t.Fatalf("expected exactly one projection_record_key marker (no injected duplicate), got %d:\n%s", n, got)
	}
	// No injected field #3 — only the two authored fields exist.
	if strings.Contains(got, "= 3 [") {
		t.Fatalf("unexpected injected field when record key already projected:\n%s", got)
	}
}

func TestGraphProjectionHonorsCustomRecordKey(t *testing.T) {
	msg, fd := agentEventSource("agent_id")
	gp := model.GraphProjectionEntry{
		Name: "Session",
		Xid:  "email",
		Fields: []model.GraphProjectionFieldEntry{
			{Source: "email", Index: "exact"},
		},
	}

	got, err := generateGraphProjection(gp, "agentsensor.AgentEvent", msg, fd)
	if err != nil {
		t.Fatalf("generateGraphProjection failed: %v", err)
	}

	want := `string agent_id = 2 [(spec.projection_source) = "agent_id", (spec.projection_record_key) = true`
	if !strings.Contains(got, want) {
		t.Fatalf("graph projection did not honor custom record_key:\nwant substring: %s\ngot:\n%s", want, got)
	}
}

func TestGraphProjectionFailsLoudWhenRecordKeyFieldMissing(t *testing.T) {
	// record_key names a field that does not exist on the source message, and the
	// projection does not project it → injection must fail loudly (never emit a
	// projection whose record key can't be resolved/hydrated).
	msg, fd := agentEventSource("nonexistent_field")
	gp := model.GraphProjectionEntry{
		Name:   "Session",
		Xid:    "email",
		Fields: []model.GraphProjectionFieldEntry{{Source: "email", Index: "exact"}},
	}
	if _, err := generateGraphProjection(gp, "agentsensor.AgentEvent", msg, fd); err == nil {
		t.Fatal("expected generateGraphProjection to fail when the record key field is missing")
	}
}

func TestTimeseriesProjectionFailsLoudWhenRecordKeyFieldMissing(t *testing.T) {
	msg, fd := agentEventSource("nonexistent_field")
	tp := model.TimeseriesProjectionEntry{
		Name:   "AgentEventRecord",
		Fields: []model.TimeseriesProjectionFieldEntry{{Source: "timestamp_ms", OrderKey: 1}},
	}
	if _, err := generateTimeseriesProjection(tp, "agentsensor.AgentEvent", msg, fd); err == nil {
		t.Fatal("expected generateTimeseriesProjection to fail when the record key field is missing")
	}
}

func TestTimeseriesProjectionInjectsRecordKeyWhenAbsent(t *testing.T) {
	msg, fd := agentEventSource("")
	tp := model.TimeseriesProjectionEntry{
		Name: "AgentEventRecord",
		Fields: []model.TimeseriesProjectionFieldEntry{
			{Source: "timestamp_ms", OrderKey: 1},
		},
		Retention: &model.RetentionPolicyEntry{TimestampField: "timestamp_ms", DurationSeconds: 7776000},
		Dedup:     &model.DedupPolicyEntry{Strategy: model.DedupLatestByVersion, VersionField: "timestamp_ms", KeyFields: []string{"timestamp_ms"}},
	}

	got, err := generateTimeseriesProjection(tp, "agentsensor.AgentEvent", msg, fd)
	if err != nil {
		t.Fatalf("generateTimeseriesProjection failed: %v", err)
	}

	for _, want := range []string{
		"string event_id = 2 [",
		`(spec.projection_source) = "event_id"`,
		"(spec.projection_record_key) = true",
		"retention: {timestamp_field: \"timestamp_ms\" duration_seconds: 7776000}",
		"strategy: DEDUP_LATEST_BY_VERSION",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("timeseries projection missing %q:\n%s", want, got)
		}
	}
	if n := strings.Count(got, "projection_record_key"); n != 1 {
		t.Fatalf("expected exactly one projection_record_key marker, got %d:\n%s", n, got)
	}
}

func TestTimeseriesProjectionMarksExistingRecordKeyField(t *testing.T) {
	msg, fd := agentEventSource("")
	tp := model.TimeseriesProjectionEntry{
		Name: "AgentEventRecord",
		Fields: []model.TimeseriesProjectionFieldEntry{
			{Source: "event_id", PartitionKey: true},
			{Source: "timestamp_ms", OrderKey: 1},
		},
	}

	got, err := generateTimeseriesProjection(tp, "agentsensor.AgentEvent", msg, fd)
	if err != nil {
		t.Fatalf("generateTimeseriesProjection failed: %v", err)
	}

	if n := strings.Count(got, "projection_record_key"); n != 1 {
		t.Fatalf("expected exactly one projection_record_key marker (no injected duplicate), got %d:\n%s", n, got)
	}
	if strings.Contains(got, "= 3 [") {
		t.Fatalf("unexpected injected field when record key already projected:\n%s", got)
	}
}
