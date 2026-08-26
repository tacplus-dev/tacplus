// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"testing"
)

var minGroupSizeStamp = regexp.MustCompile(`option \(spec\.min_group_size\) = (-?\d+);`)

// stampedRPCs returns, per emitted service half, the RPC names carrying a
// stamped (spec.min_group_size) option and the value each one carries. The scan
// is line-based over the emitted proto text because that is what the injector
// produces — a stamp landing outside an rpc body would show up here as an
// attribution to the previous RPC (or to none), not as a silent pass.
func stampedRPCs(t *testing.T, text string) map[string]int {
	t.Helper()
	stamped := map[string]int{}
	current := ""
	for _, line := range strings.Split(text, "\n") {
		if m := emittedRPCName.FindStringSubmatch(line); m != nil {
			current = m[1]
		}
		m := minGroupSizeStamp.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		if current == "" {
			t.Fatalf("min_group_size stamped outside any rpc body:\n%s", text)
		}
		var n int
		if _, err := fmt.Sscanf(m[1], "%d", &n); err != nil {
			t.Fatalf("unparsable stamped min_group_size %q", m[1])
		}
		stamped[current] = n
	}
	return stamped
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// The threshold is declared on the RECORD, but every downstream generator reads
// it off the METHOD (a wrapper sees Get<T>Count(QueryHandle) -> RecordCount and
// nothing in that signature names T). So the injector must stamp it onto exactly
// the size-only RPCs — the ones that return a size and no rows — on BOTH emitted
// halves: guarding one surface and not the other would put the exemption on the
// surface nobody audits, and leaving Update<T>Query unstamped makes the guard
// bypassable in one extra RPC (its QueryStats.size is the new predicate's live
// match count).
func TestRecordQueryPlaneStampsMinGroupSizeOnSizeOnlyRPCs(t *testing.T) {
	plane := traitPlane{
		relPath:      "vservices/query.proto",
		name:         "query",
		host:         "stratum",
		s2sService:   "RecordMap",
		c2sService:   "RecordQuery",
		authServices: []string{"argus"},
	}
	// TaskActivity declares a threshold; AgentEvent does not. Both carry filters
	// so both emit the Update/Watch/Subscribe arm — otherwise the undeclared
	// record could not prove that arm stays unstamped.
	records := []recordType{
		{typeName: "TaskActivity", pkg: "tasks", relPath: "vservices/tasks.proto", key: "event_id",
			filters:      []recordFilter{{name: "task_id", protoType: "string"}},
			minGroupSize: 5},
		{typeName: "AgentEvent", pkg: "agentsensor", relPath: "vservices/agent-sensor.proto", key: "event_id",
			filters: []recordFilter{{name: "spiffe_id", protoType: "string"}}},
	}

	want := []string{"GetTaskActivityCount", "SubscribeTaskActivityCount", "UpdateTaskActivityQuery"}
	for _, half := range []struct {
		name string
		text string
	}{
		{"s2s", recordMapS2SService(plane, records)},
		{"c2s", recordQueryC2SService(plane, records)},
	} {
		stamped := stampedRPCs(t, half.text)
		if got := sortedKeys(stamped); !equalStrings(got, want) {
			t.Errorf("%s half stamps min_group_size on %v, want exactly %v:\n%s",
				half.name, got, want, half.text)
		}
		for rpc, n := range stamped {
			if n != 5 {
				t.Errorf("%s half stamps min_group_size = %d on %s, want the record's declared 5",
					half.name, n, rpc)
			}
		}
	}
}

// The annotation is additive: a fleet where no record declares min_group_size
// must emit byte-identical protos to one generated before the annotation
// existed. Nothing is stamped anywhere, on either half.
func TestRecordQueryPlaneOmitsMinGroupSizeWhenUndeclared(t *testing.T) {
	plane := traitPlane{
		relPath:      "vservices/query.proto",
		name:         "query",
		host:         "stratum",
		s2sService:   "RecordMap",
		c2sService:   "RecordQuery",
		authServices: []string{"argus"},
	}
	records := []recordType{
		{typeName: "TaskActivity", pkg: "tasks", relPath: "vservices/tasks.proto", key: "event_id",
			filters: []recordFilter{{name: "task_id", protoType: "string"}}},
		{typeName: "PlainRecord", pkg: "tasks", relPath: "vservices/tasks.proto", key: "record_id"},
	}
	for _, half := range []struct {
		name string
		text string
	}{
		{"s2s", recordMapS2SService(plane, records)},
		{"c2s", recordQueryC2SService(plane, records)},
	} {
		if strings.Contains(half.text, "min_group_size") {
			t.Errorf("%s half stamps min_group_size for records that declare none:\n%s", half.name, half.text)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
