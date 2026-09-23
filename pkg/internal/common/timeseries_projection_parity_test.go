// Copyright (c) 2026 Second Star Cavalier LLC

package common

import (
	"os"
	"strings"
	"testing"
)

// The record plane's (spec.timeseries_type) PROJECTION across the three full
// languages.
//
// A message carrying (spec.timeseries_type) is a projection: the annotation
// asks for a store-neutral surface — the column list, the proto-to-row
// conversion, and the row-to-proto application — that a downstream materializer
// renders into a concrete schema. Go and Rust generated it; Java did not, and
// parser.ValidateProjectionLanguageSupport REFUSED a Java build of any service
// owning a projection record rather than ship a store that never materializes
// it. Java's emitter (java/types/timeseries_gen.go) was ported from Go's and
// Rust's rather than re-derived, and these tests are what keeps the three from
// drifting apart.
//
// WHY SOURCE-LEVEL, AND WHY BOTH HALVES — the same reasoning as
// min_group_size_parity_test.go, which this file follows:
//
//	the SHARED half — literal text that must be identical in all three
//	    generators, because it is the same contract stated once: which
//	    annotation opts a message in, which one selects a column, and the
//	    store-neutrality promise that says what is deliberately NOT rendered.
//	the MECHANICAL half — the per-language expression of the SAME rule, one row
//	    per language. A language that loses its row has dropped the rule while
//	    the shared strings still sit in the file.

// timeseriesProjectionGenerators is where each language renders the projection:
// the emitter itself and the template it renders.
var timeseriesProjectionGenerators = map[string][]string{
	"go":   {"../go/types/timeseries_gen.go", "../../templates/go/types/timeseries_helpers.go.tmpl"},
	"java": {"../java/types/timeseries_gen.go", "../../templates/java/types/timeseries_helpers.java.tmpl"},
	"rust": {"../rust/types/timeseries_gen.go", "../../templates/rust/types/timeseries_helpers.rs.tmpl"},
}

// readTimeseriesProjectionGenerators returns each language's emitter and
// template joined, so a rule expressed in the emitter in one language and in the
// template in another still reads the same way.
func readTimeseriesProjectionGenerators(t *testing.T) map[string]string {
	t.Helper()
	out := make(map[string]string, len(timeseriesProjectionGenerators))
	for lang, paths := range timeseriesProjectionGenerators {
		var b strings.Builder
		for _, p := range paths {
			src, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("%s: read %s: %v", lang, p, err)
			}
			b.Write(src)
			b.WriteString("\n")
		}
		out[lang] = b.String()
	}
	return out
}

// wantTimeseriesShared asserts one shared contract string appears in all three.
func wantTimeseriesShared(t *testing.T, srcs map[string]string, what, want string) {
	t.Helper()
	var missing []string
	for lang, src := range srcs {
		if !strings.Contains(src, want) {
			missing = append(missing, lang)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%s: %v no longer carry %q, but the other languages still do. "+
			"(spec.timeseries_type) must not mean something different depending on which language hosts the "+
			"projection — either every language changes together or none does",
			what, missing, want)
	}
}

// wantTimeseriesPerLanguage asserts each language's own expression of a shared
// rule, and that no language is missing a row.
func wantTimeseriesPerLanguage(t *testing.T, srcs map[string]string, what string, want map[string]string) {
	t.Helper()
	for lang, w := range want {
		if !strings.Contains(srcs[lang], w) {
			t.Errorf("%s: the %s timeseries generator no longer renders %q — it has dropped the rule the other "+
				"two still express, and a %s host of this projection no longer materializes what the annotation asks for",
				what, lang, w, lang)
		}
	}
	if len(want) != len(timeseriesProjectionGenerators) {
		t.Errorf("%s: the per-language table covers %d of %d languages; a language with no row is a language "+
			"nothing checks", what, len(want), len(timeseriesProjectionGenerators))
	}
}

// ---------------------------------------------------------------------------

// WHICH MESSAGES, AND WHICH FIELDS. The message-level (spec.timeseries_type)
// opts a message in; the field-level (spec.timeseries_column) selects the
// columns, in declaration order. A language reading a different annotation, or
// projecting every field, would project a different table from the same proto.
func TestTimeseriesProjectionSelectionIsIdenticalAcrossLanguages(t *testing.T) {
	srcs := readTimeseriesProjectionGenerators(t)

	wantTimeseriesShared(t, srcs, "the opt-in annotation", `msg.Annotations["timeseries_type"]`)
	wantTimeseriesShared(t, srcs, "the column annotation", `f.Annotations["timeseries_column"]`)

	// The well-known-type tree carries no projections and is skipped before the
	// scan in every language.
	wantTimeseriesShared(t, srcs, "the well-known-type skip", `"google/"`)
}

// STORE NEUTRALITY. None of the three renders backend DDL: the annotation
// carries semantic intent (order_key / partition_key / cardinality_hint) and a
// downstream materializer renders the concrete engine schema from the
// registry. A language that started emitting schema DDL of its own would be
// making the engine choice for every other language's callers.
func TestTimeseriesProjectionIsStoreNeutralInEveryLanguage(t *testing.T) {
	srcs := readTimeseriesProjectionGenerators(t)

	for _, shared := range []string{
		"No backend DDL is rendered here",
		"the registry's semantic",
	} {
		wantTimeseriesShared(t, srcs, "the store-neutrality promise", shared)
	}
}

// THE COLUMN LIST is the ordering contract every other surface is defined
// against: the row conversion emits values in this order, and the row
// application reads them back in it. Each language names it in its own idiom.
func TestTimeseriesColumnListIsGeneratedInEveryLanguage(t *testing.T) {
	srcs := readTimeseriesProjectionGenerators(t)

	wantTimeseriesPerLanguage(t, srcs, "the column list", map[string]string{
		"go":   "func {{.TypeName}}TimeseriesColumns() []string {",
		"java": "public static java.util.List<String> columns() {",
		"rust": "pub fn {{.SnakeTypeName}}_timeseries_columns() -> &'static [&'static str] {",
	})
	wantTimeseriesPerLanguage(t, srcs, "the proto-to-row conversion", map[string]string{
		"go":   "func {{.TypeName}}ToTimeseriesRow(msg *{{.TypeName}}) []interface{} {",
		"java": "public static java.util.List<Object> toRow({{.TypeFqn}} msg) {",
		"rust": "pub fn from_proto(msg: &{{.TypeName}}) -> Self {",
	})
	// The generated file is named for the projected type in every language, so a
	// projection is findable by the message it projects.
	wantTimeseriesPerLanguage(t, srcs, "the generated file name", map[string]string{
		"go":   `fileName := "timeseries_" + strutil.ToSnakeCase(data.TypeName) + ".go"`,
		"java": `outputPath := filepath.Join(outputDir, data.ClassName+".java")`,
		"rust": `fileName := "timeseries_" + strutil.ToSnakeCase(data.TypeName) + ".rs"`,
	})
}

// THE THREE SPECIAL COLUMN KINDS. Everything else passes through natively, but
// an enum, a Timestamp and a nested message each have ONE representation the
// three languages agree on — an enum is its VALUE NAME, a Timestamp is UNIX
// MILLIS, a nested message is JSON. Two languages disagreeing here would write
// rows into the same table that the other cannot read back.
func TestTimeseriesColumnEncodingIsTheSameInEveryLanguage(t *testing.T) {
	srcs := readTimeseriesProjectionGenerators(t)

	// The enum column is the value NAME, never its number: a renumbered enum
	// must not silently re-point historical rows.
	wantTimeseriesPerLanguage(t, srcs, "the ENUM column encoding", map[string]string{
		"go":   "msg.{{.GoFieldName}}.String(),",
		"java": "row.add(msg.get{{.Accessor}}().name());",
		"rust": "as_str_name().to_string()",
	})
	// The Timestamp column is unix MILLIS, computed the same way everywhere.
	wantTimeseriesPerLanguage(t, srcs, "the TIMESTAMP column encoding", map[string]string{
		"go":   "AsTime().UnixMilli()",
		"java": "getSeconds() * 1000L + msg.get{{.Accessor}}().getNanos() / 1000000L",
		"rust": "ts.seconds * 1000 + (ts.nanos as i64) / 1_000_000",
	})
	// A nested message column is JSON, and an absent one is the empty string —
	// not null, which no column type in a timeseries store accepts uniformly.
	wantTimeseriesPerLanguage(t, srcs, "the MESSAGE column encoding", map[string]string{
		"go":   "protojson.Marshal(msg.{{.GoFieldName}})",
		"java": "com.google.protobuf.util.JsonFormat.printer().print(msg)",
		"rust": "serde_json::to_string(m)",
	})
}

// CROSS-PACKAGE TYPES. A projection column whose enum or message is declared in
// another proto package must be named in FULL, or the generated source does not
// compile — a bare name only resolves against the generating file's own package.
// Rust learned this from a live schematic build; Go and Java qualify for the
// same reason. This is mechanical per language (a crate path, a Go package
// selector, a Java FQN), but no language may drop it.
func TestTimeseriesCrossPackageTypesAreQualifiedInEveryLanguage(t *testing.T) {
	srcs := readTimeseriesProjectionGenerators(t)

	wantTimeseriesPerLanguage(t, srcs, "cross-package type qualification", map[string]string{
		"go":   `fd.EnumType = f.TypePackage + "." + f.Type`,
		"java": "func javaTimeseriesTypeFqn(",
		"rust": `fd.EnumType = fmt.Sprintf("crate::%s::%s::%s",`,
	})
}

// EVERY FULL-SERVICE LANGUAGE HOSTS THE PROJECTION. The refusal in
// parser.ValidateProjectionLanguageSupport exists so a store-hosting language
// with no emitter fails the build instead of shipping a record surface that
// never materializes. A language that generates the projection must therefore
// be listed as supported — a listed-but-unimplemented language ships the silent
// gap the refusal was written to prevent, and an implemented-but-unlisted one
// refuses a build it could serve.
func TestTimeseriesProjectionLanguagesMatchTheSupportList(t *testing.T) {
	src, err := os.ReadFile("parser/projection_validate.go")
	if err != nil {
		t.Fatalf("read projection_validate.go: %v", err)
	}
	list := string(src)
	for lang := range timeseriesProjectionGenerators {
		if !strings.Contains(list, `"`+lang+`":   true`) && !strings.Contains(list, `"`+lang+`": true`) {
			t.Errorf("%s generates timeseries projection codegen but is not in projectionSupportedLanguages; "+
				"tacplus would refuse a %s build of a service that owns a projection record it can in fact serve",
				lang, lang)
		}
	}
}
