// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"regexp"
	"sort"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/verbs"
)

// canonicalTraitVerbs are the governed verbs the record query plane's expansion
// template is allowed to spell. Every one is a canonical entry in the closed
// verb registry (asserted below, so this list cannot drift away from it).
var canonicalTraitVerbs = []string{"Create", "Get", "List", "Delete", "Update", "Watch", "Subscribe"}

var emittedRPCName = regexp.MustCompile(`(?m)^\s*rpc\s+([A-Za-z0-9_]+)\s*\(`)

// The record query plane is the trait's generated RPC surface, and it has to
// classify against the governed verb registry — an RPC the template spells with
// a non-canonical verb (Query<T>, Fetch<T>, Search<T>, …) is a hard generation
// failure downstream, discovered only by whoever next regenerates.
//
// This pins the template itself: every RPC name recordMapS2SService and
// recordQueryC2SService emit must lead with a canonical registry verb. A future
// edit introducing "rpc QueryTask(...)" into either half fails here, at the
// source, rather than in a distant descriptor-parse error.
func TestRecordQueryPlaneEmitsOnlyCanonicalVerbs(t *testing.T) {
	// Guard the allow-list itself against registry drift: each entry must be a
	// registered canonical verb, and must not be a folded synonym.
	for _, v := range canonicalTraitVerbs {
		if _, ok := verbs.Lookup(v); !ok {
			t.Fatalf("allow-listed verb %q is not in the closed verb registry", v)
		}
		if canon, isSyn := verbs.FoldOf(v); isSyn {
			t.Fatalf("allow-listed verb %q is a folded synonym of %q", v, canon)
		}
	}

	plane := traitPlane{
		relPath:      "vservices/query.proto",
		name:         "query",
		host:         "stratum",
		s2sService:   "RecordMap",
		c2sService:   "RecordQuery",
		authServices: []string{"reporter"},
	}
	// One record with filters and one without, so both arms of the template
	// (the base lifecycle and the filter-only Update/Watch/Subscribe arm) emit.
	records := []recordType{
		{typeName: "TaskActivity", pkg: "tasks", relPath: "vservices/tasks.proto", key: "event_id",
			filters: []recordFilter{{name: "spiffe_id", protoType: "string"}}},
		{typeName: "PlainRecord", pkg: "tasks", relPath: "vservices/tasks.proto", key: "record_id"},
	}

	for _, half := range []struct {
		name string
		text string
	}{
		{"s2s", recordMapS2SService(plane, records)},
		{"c2s", recordQueryC2SService(plane, records)},
	} {
		names := emittedRPCNames(t, half.text)
		if len(names) == 0 {
			t.Fatalf("%s half emitted no RPCs:\n%s", half.name, half.text)
		}
		for _, rpc := range names {
			verb := leadingCanonicalVerb(rpc)
			if verb == "" {
				t.Errorf("%s half emits rpc %q, which does not lead with a canonical registry verb (allowed: %s)",
					half.name, rpc, strings.Join(canonicalTraitVerbs, ", "))
			}
		}
	}
}

// Both halves must spell the SAME RPC names — the c2s half is the governed
// mirror of the s2s half, and a verb renamed on one side only would split the
// surface silently.
func TestRecordQueryPlaneHalvesSpellIdenticalRPCNames(t *testing.T) {
	plane := traitPlane{
		relPath: "vservices/query.proto", name: "query", host: "stratum",
		s2sService: "RecordMap", c2sService: "RecordQuery", authServices: []string{"reporter"},
	}
	records := []recordType{
		{typeName: "TaskActivity", pkg: "tasks", relPath: "vservices/tasks.proto", key: "event_id",
			filters: []recordFilter{{name: "spiffe_id", protoType: "string"}}},
	}

	s2s := emittedRPCNames(t, recordMapS2SService(plane, records))
	c2s := emittedRPCNames(t, recordQueryC2SService(plane, records))
	sort.Strings(s2s)
	sort.Strings(c2s)
	if strings.Join(s2s, ",") != strings.Join(c2s, ",") {
		t.Fatalf("s2s and c2s halves disagree on RPC names:\n  s2s: %v\n  c2s: %v", s2s, c2s)
	}
}

func emittedRPCNames(t *testing.T, protoText string) []string {
	t.Helper()
	var names []string
	for _, m := range emittedRPCName.FindAllStringSubmatch(protoText, -1) {
		names = append(names, m[1])
	}
	return names
}

// leadingCanonicalVerb returns the allow-listed verb the RPC name leads with,
// or "" if it leads with none. The match is verb-then-uppercase (or exact) so
// "Getter" does not count as "Get" and "Querying" does not count as anything.
func leadingCanonicalVerb(rpc string) string {
	for _, v := range canonicalTraitVerbs {
		if rpc == v {
			return v
		}
		if strings.HasPrefix(rpc, v) {
			rest := rpc[len(v):]
			if rest[0] >= 'A' && rest[0] <= 'Z' {
				return v
			}
		}
	}
	return ""
}
