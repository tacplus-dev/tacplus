// Copyright (c) 2026 Second Star Cavalier LLC

package common

import (
	"os"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/boundaries"
)

// (spec.eviction_max_size) declares a bound on how many entries a state
// collection may hold. CollectMessageBoundaries recorded it for every language,
// but only the Go state wrapper ever consumed it: Go emitted a maxEntries check
// on every Watch snapshot (the annotation's value, or a 100000 default), while
// the java/python/rust/typescript wrappers cached whatever the server sent with
// no check at all. Same proto, same annotation, one language enforcing it.
//
// This pins the whole family: each of the five watch receivers checks the
// delivered snapshot against a limit, and the four non-Go generators source that
// limit from the shared boundaries helper rather than re-reading the annotation
// (or re-inventing the default) per language.
func TestEveryStateWrapperChecksCollectionSnapshotSize(t *testing.T) {
	watchChecks := map[string]struct {
		template string
		check    string
	}{
		"go":         {"../../templates/go/types/state_wrapper.go.tmpl", "if len(msg.Entries) > maxEntries{{$c.FieldName}} {"},
		"java":       {"../../templates/java/types/statewrapper.java.tmpl", "if (value.getEntriesMap().size() > {{.MaxEntries}}) {"},
		"python":     {"../../templates/python/types/statewrapper.py.tmpl", "if len(value.entries) > {{.MaxEntries}}:"},
		"rust":       {"../../templates/rust/types/statewrapper.rs.tmpl", "if resp.entries.len() > {{.MaxEntries}} {"},
		"typescript": {"../../templates/typescript/types/statewrapper.ts.tmpl", "if ({{.FieldName}}Received > {{.MaxEntries}}) {"},
	}
	for lang, tc := range watchChecks {
		src, err := os.ReadFile(tc.template)
		if err != nil {
			t.Fatalf("%s: read %s: %v", lang, tc.template, err)
		}
		text := string(src)
		if !strings.Contains(text, tc.check) {
			t.Errorf("%s state wrapper (%s) no longer bounds a Watch snapshot: want %q. "+
				"A (spec.eviction_max_size) overflow ships undiagnosed in %s while go still reports it",
				lang, tc.template, tc.check, lang)
		}
		if !strings.Contains(text, "exceeds max collection size") {
			t.Errorf("%s state wrapper (%s) dropped the overflow diagnostic", lang, tc.template)
		}
	}

	for lang, path := range map[string]string{
		"java":       "../java/types/statewrapper_gen.go",
		"python":     "../python/types/statewrapper_gen.go",
		"rust":       "../rust/types/statewrapper_gen.go",
		"typescript": "../typescript/types/statewrapper.go",
	} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: read %s: %v", lang, path, err)
		}
		if !strings.Contains(string(src), "MaxEntries: boundaries.CollectionSafetyLimit(field),") {
			t.Errorf("%s state-wrapper generator (%s) no longer resolves the collection limit through "+
				"boundaries.CollectionSafetyLimit; it can now disagree with the other languages about "+
				"the same (spec.eviction_max_size) field", lang, path)
		}
	}
}

// Go's default is written as a literal in its template, so the shared constant
// the other four languages read must equal it or an unannotated collection gets
// two different limits depending on the target language.
func TestGoStateWrapperDefaultMatchesTheSharedSafetyLimit(t *testing.T) {
	src, err := os.ReadFile("../../templates/go/types/state_wrapper.go.tmpl")
	if err != nil {
		t.Fatalf("read go state wrapper template: %v", err)
	}
	want := "const maxEntries{{$c.FieldName}} = 100000 // default safety limit; set (spec.eviction_max_size) to override"
	if !strings.Contains(string(src), want) {
		t.Fatalf("go state wrapper template no longer declares the %d default as %q", boundaries.DefaultCollectionSafetyLimit, want)
	}
	if boundaries.DefaultCollectionSafetyLimit != 100000 {
		t.Fatalf("boundaries.DefaultCollectionSafetyLimit = %d, but the go template hardcodes 100000",
			boundaries.DefaultCollectionSafetyLimit)
	}
}
