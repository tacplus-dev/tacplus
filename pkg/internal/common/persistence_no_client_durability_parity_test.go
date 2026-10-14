// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// (spec.persistence) is NOBODY's durability today.
//
// The annotation requests that a graph boundary be durable. No store implements
// it. Kernel — which briefly did, under the 2026-09-23 decision — does not: as of
// 2026-09-25 kernel is in-memory coordination plus its own identity snapshot and
// holds no durable user data, and proto/state.go now REFUSES the annotation
// outright on a kernel-labelled state root ("durable data belongs in stratum
// records"). tacplus implements nothing either. It TRANSPORTS the flag —
// common/boundaries/boundaries.go reads it once, registry.go carries it into
// registry.json as BoundaryInfo.persistence — descriptively, for a future store
// that does implement it. Every target is a passive descriptor consumer.
//
// That makes the claim tacplus must keep mechanically true a NEGATIVE one: no
// generated state client, in any language, may acquire durability of its own, and
// no emitted surface may name a store that honors the flag. A state client that
// wrote a local snapshot would be a durable store for the boundary that the proto
// author never asked for and cannot see; an emitted comment naming kernel as the
// persister would be a promise no process keeps. The decision doc asked for a
// fail-generation check for the client half; there is no per-target predicate
// tacplus can evaluate for it (it has no model of which process hosts the state
// plane), so THIS FILE is the enforceable form of that half of the claim.
//
// WHY SOURCE-LEVEL, AND WHY BOTH HALVES — the same reasoning as
// store_epoch_parity_test.go and min_group_size_parity_test.go, which this file
// follows:
//
//	the SHARED half — properties that hold across the whole tree at once,
//	    because they are one contract stated once: exactly one site reads the
//	    annotation, and no emitted comment promises local durability.
//	the MECHANICAL half — the per-language expression of the SAME rule, one row
//	    per language. A language that loses its row is a language nothing checks.
//
// All five languages that emit a state client are covered, python and
// typescript included. They are client-only, which is exactly why they are the
// likeliest to grow a "harmless" local cache — the false promise this file
// exists to catch.

// persistenceStateGenerators is where each language renders its state client:
// the generator, and the template that holds the emitted code.
var persistenceStateGenerators = map[string]struct {
	generators []string
	templates  []string
}{
	"go":         {[]string{"../go/types/stateclient_gen.go"}, []string{"../../templates/go/types/state_wrapper.go.tmpl"}},
	"java":       {[]string{"../java/types/statewrapper_gen.go"}, []string{"../../templates/java/types/statewrapper.java.tmpl"}},
	"rust":       {[]string{"../rust/types/statewrapper_gen.go"}, []string{"../../templates/rust/types/statewrapper.rs.tmpl"}},
	"python":     {[]string{"../python/types/statewrapper_gen.go"}, []string{"../../templates/python/types/statewrapper.py.tmpl"}},
	"typescript": {[]string{"../typescript/types/statewrapper.go"}, []string{"../../templates/typescript/types/statewrapper.ts.tmpl"}},
}

// persistenceRegistryTemplates are the three generated registry modules that
// expose BoundaryInfo.persistence to target-language code.
var persistenceRegistryTemplates = map[string]string{
	"python":     "../../templates/python/types/registry.py.tmpl",
	"typescript": "../../templates/typescript/types/registry.ts.tmpl",
	"rust":       "../../templates/rust/types/registry.rs.tmpl",
}

func readPersistenceFile(t *testing.T, path string) string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(src)
}

// emittedStrings returns the STRING LITERALS of a generator, joined. Emitted
// target-language code always arrives as a literal; the generator's own calls
// are code. Scanning the raw file instead would be useless for Go, where the
// generator's own os.WriteFile of its output is spelled exactly like an emitted
// one — which is the whole reason this walks the AST.
func emittedStrings(t *testing.T, path string) string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var b strings.Builder
	ast.Inspect(f, func(n ast.Node) bool {
		if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			b.WriteString(lit.Value)
			b.WriteString("\n")
		}
		return true
	})
	return b.String()
}

// ---------------------------------------------------------------------------

// NO STATE CLIENT WRITES TO DISK, IN ANY LANGUAGE. One row per language, naming
// that language's own disk-write primitives. The scan covers the emitted code:
// the template, and the generator's string literals.
func TestPersistenceStateClientsWriteNoDiskInAnyLanguage(t *testing.T) {
	forbidden := map[string][]string{
		"go":         {"os.WriteFile", "os.Create"},
		"java":       {"Files.write", "FileOutputStream"},
		"rust":       {"std::fs::", "File::create"},
		"python":     {"open("},
		"typescript": {"fs.writeFile"},
	}

	if len(forbidden) != len(persistenceStateGenerators) {
		t.Fatalf("the per-language table covers %d of %d state-client languages; a language with no row is a "+
			"language nothing checks", len(forbidden), len(persistenceStateGenerators))
	}

	for lang, prims := range forbidden {
		srcs := persistenceStateGenerators[lang]
		scanned := map[string]string{}
		for _, p := range srcs.templates {
			scanned[p] = readPersistenceFile(t, p)
		}
		for _, p := range srcs.generators {
			scanned[p+" (string literals)"] = emittedStrings(t, p)
		}
		for path, src := range scanned {
			for _, prim := range prims {
				if strings.Contains(src, prim) {
					t.Errorf("%s: %s emits %q in the state client. (spec.persistence) is honored by no store "+
						"today — a generated state client that writes a boundary to local disk is a durable "+
						"store the proto author never asked for and cannot see",
						lang, path, prim)
				}
			}
		}
	}
}

// EXACTLY ONE SITE READS THE ANNOTATION'S VALUE. Every state-client generator
// once parsed (spec.persistence) into a per-language IsPersisted field that no
// template ever read — accept-and-ignore at the generator-internal layer. Those
// are deleted; common/boundaries/boundaries.go is the one remaining reader, and
// it reads the flag only to put it on the registry descriptor.
//
// proto/state.go also names the annotation, but does not read its value: it is
// the presence test behind the two refusals — (spec.persistence) on a queue,
// topic or priority queue, and (spec.persistence) on a kernel-labelled state
// root. Presence tests are how the flag is validated; value reads are how a
// language would start acting on it, which is what must not grow back.
func TestPersistenceHasOneValueReadSite(t *testing.T) {
	const wantValueRead = "../common/boundaries/boundaries.go"
	allowedNamers := map[string]string{
		"../common/boundaries/boundaries.go": "reads the value onto the registry descriptor",
		"../proto/state.go":                  "presence test: refuses the annotation on a queue/topic/priority_queue and on a kernel-labelled state root",
	}

	namers := map[string]bool{}
	valueReaders := map[string]bool{}

	root := ".."
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		src := readPersistenceFile(t, path)
		if !strings.Contains(src, `Annotations["persistence"]`) {
			return nil
		}
		namers[path] = true
		// A value read takes the annotation's payload; a presence test takes
		// only the comma-ok.
		for _, payload := range []string{
			`Annotations["persistence"].BoolValue`,
			`ann, ok := field.Annotations["persistence"]`,
		} {
			if strings.Contains(src, payload) {
				valueReaders[path] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}

	for path := range namers {
		if _, ok := allowedNamers[path]; !ok {
			t.Errorf("%s names Annotations[\"persistence\"], but the only sites allowed to are %v. tacplus "+
				"implements no persistence: it carries the flag to registry.json descriptively. A new "+
				"reader is a generator starting to act on a flag it does not own",
				path, allowedNamers)
		}
	}
	for path := range allowedNamers {
		if !namers[path] {
			t.Errorf("%s no longer names Annotations[\"persistence\"] — this test can no longer tell that the "+
				"flag is still transported and still validated", path)
		}
	}
	if len(valueReaders) != 1 || !valueReaders[wantValueRead] {
		t.Errorf("the annotation's VALUE is read by %v; it must be read by exactly one site, %s. Every extra "+
			"reader the state-client generators once had was dead plumbing no template consumed — the flag's "+
			"only consumer is registry.json, and no store reads it from there yet",
			valueReaders, wantValueRead)
	}
}

// NO EMITTED COMMENT PROMISES LOCAL DURABILITY. A generated doc string that says
// a boundary is "durable" or "survives restart" reads, at the call site, as a
// promise the generated code makes. It does not: kernel makes it. Wording is the
// part of this contract a reader actually meets, so it is pinned here and not
// left to each template's judgment.
func TestPersistenceTemplatesPromiseNoLocalDurability(t *testing.T) {
	forbidden := []string{"persisted to disk", "survives restart", "durable"}

	var paths []string
	for _, srcs := range persistenceStateGenerators {
		paths = append(paths, srcs.templates...)
	}
	for _, p := range persistenceRegistryTemplates {
		paths = append(paths, p)
	}

	for _, path := range paths {
		src := readPersistenceFile(t, path)
		for _, phrase := range forbidden {
			if strings.Contains(src, phrase) {
				t.Errorf("%s emits %q. Generated state code makes no durability promise of its own — "+
					"(spec.persistence) is honored by no store today, and a comment that says "+
					"otherwise is the false promise this file exists to catch", path, phrase)
			}
		}
	}
}

// THE REGISTRY TEMPLATES RENDER THE FLAG AS A PLAIN DESCRIPTOR FIELD. They are
// the only generated surface that exposes it, and they must stay descriptive:
// the flag is declared on BoundaryInfo and rendered into the literal, with no
// accessor or helper built on top of it. A helper is where a target language
// would start branching on a flag kernel owns.
//
// python's {{if .Persistence}}True{{else}}False{{end}} is bool-literal casing,
// not a behavioural branch — Python spells its booleans differently than Go's
// template renders them.
func TestPersistenceRegistryTemplatesStayDescriptive(t *testing.T) {
	declaration := map[string]string{
		"python":     "    persistence: bool",
		"typescript": "    persistence: boolean;",
		"rust":       "    pub persistence: bool,",
	}
	rendering := map[string]string{
		"python":     "persistence={{if .Persistence}}True{{else}}False{{end}},",
		"typescript": "persistence: {{.Persistence}},",
		"rust":       "persistence: {{.Persistence}},",
	}

	if len(declaration) != len(persistenceRegistryTemplates) || len(rendering) != len(persistenceRegistryTemplates) {
		t.Fatalf("the per-language table covers %d/%d declarations and %d/%d renderings of %d registry "+
			"templates; a template with no row is a template nothing checks",
			len(declaration), len(persistenceRegistryTemplates), len(rendering), len(persistenceRegistryTemplates),
			len(persistenceRegistryTemplates))
	}

	for lang, path := range persistenceRegistryTemplates {
		src := readPersistenceFile(t, path)
		if !strings.Contains(src, declaration[lang]) {
			t.Errorf("%s: %s no longer declares persistence as %q on BoundaryInfo", lang, path, declaration[lang])
		}
		if !strings.Contains(src, rendering[lang]) {
			t.Errorf("%s: %s no longer renders persistence as %q — the flag must reach the descriptor "+
				"unchanged, exactly as registry.json carries it", lang, path, rendering[lang])
		}
		// Nothing is built ON the flag. Three mentions, in every one of the
		// three templates: the BoundaryInfo field declaration, the descriptor
		// literal's field name, and the {{.Persistence}} action that fills it.
		const wantMentions = 3
		if n := strings.Count(strings.ToLower(src), "persist"); n != wantMentions {
			t.Errorf("%s: %s mentions persistence %d times, not %d; the registry template declares the field "+
				"and renders it, and does nothing else with it. A further mention is a helper or accessor "+
				"acting on a flag nothing implements", lang, path, n, wantMentions)
		}
	}
}

// NO EMITTED SURFACE, IN ANY LANGUAGE, NAMES A STORE THAT PERSISTS THE FLAG. The
// 2026-09-23 decision made kernel the durable store for flagged boundaries; the
// 2026-09-25 reversal removed it, leaving zero implementers. The claim that must
// now hold across every language at once is that no generated artifact tells a
// reader otherwise — a doc comment saying "kernel persists this boundary" is a
// promise no process keeps any more, and it is exactly the sentence the templates
// would have been given had the 09-23 decision stuck.
//
// The scan is line-level co-occurrence of "persist" and "kernel", over the WHOLE
// template tree rather than this file's state/registry maps: the false claim could
// be emitted from any template that knows both words (service_manager, the
// chassis), and templates mention kernel legitimately all over — as a routing
// label, a token audience, an S2S peer — so only the pairing is forbidden.
// Generator string literals are covered for the state clients through the same AST
// scan the disk-write assertion uses.
func TestNoEmittedSurfaceClaimsAStorePersistsTheFlag(t *testing.T) {
	claims := func(t *testing.T, where, src string) {
		t.Helper()
		for i, line := range strings.Split(src, "\n") {
			lower := strings.ToLower(line)
			if strings.Contains(lower, "persist") && strings.Contains(lower, "kernel") {
				t.Errorf("%s:%d names kernel and persistence together: %q. (spec.persistence) has no "+
					"implementer — kernel holds no durable user data, and durable data belongs in stratum "+
					"records; generated code must not tell a reader that kernel will persist a boundary",
					where, i+1, strings.TrimSpace(line))
			}
		}
	}

	var templates int
	root := "../../templates"
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".tmpl") {
			return nil
		}
		templates++
		claims(t, path, readPersistenceFile(t, path))
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	if templates == 0 {
		t.Fatalf("walked %s and found no templates — this test would pass vacuously", root)
	}

	for lang, srcs := range persistenceStateGenerators {
		for _, p := range srcs.generators {
			claims(t, lang+" "+p+" (string literals)", emittedStrings(t, p))
		}
	}
}
