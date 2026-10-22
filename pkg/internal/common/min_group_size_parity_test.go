// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"os"
	"strings"
	"testing"
)

// The record plane's (spec.record).min_group_size GUARD across the three full
// languages.
//
// min_group_size is the smallest result size a record's size-only surfaces will
// disclose: a response carrying only a number, over fewer than N records, is a
// membership disclosure by arithmetic. Java generated the guard for some time;
// Go and Rust REFUSED to generate any method carrying the annotation, so a
// Go or Rust host of the record query plane could not exist at all. Go's and
// Rust's guards (go/types/mingroupsize_gen.go, rust/types/mingroupsize_gen.go)
// were ported from Java's rather than re-derived, and these tests are what keeps
// the three from drifting apart.
//
// Per the audit that opened this item: this test, not the emitters, is what
// makes the result parity rather than three implementations that agree today.
//
// WHY SOURCE-LEVEL, AND WHY BOTH HALVES — the same reasoning as
// record_egress_parity_test.go, which this file follows:
//
//	the SHARED half — literal text that must be byte-identical in all three
//	    generators, because it is the same contract stated once: the refusal a
//	    caller reads, the comment that says why, and the two reasons a threshold
//	    is refused at generation.
//	the MECHANICAL half — the per-language expression of the SAME rule, one row
//	    per language. A language that loses its row has dropped the rule while
//	    the shared strings still sit in the file.

// minGroupSizeGenerators is where each language renders the guard: the emitter
// itself, the wrapper that splices it into the unary and stream seams, and the
// MCP dispatch that refuses a tool it cannot guard.
var minGroupSizeGenerators = map[string][]string{
	"go":   {"../go/types/mingroupsize_gen.go", "../go/types/servicewrapper.go", "../go/types/mcp_dispatch_gen.go"},
	"java": {"../java/types/servicewrapper_gen.go", "../java/types/mcp_dispatch_gen.go"},
	"rust": {"../rust/types/mingroupsize_gen.go", "../rust/types/servicewrapper_gen.go", "../rust/types/mcp_dispatch_gen.go"},
}

// readMinGroupSizeGenerators returns each language's generator sources joined,
// so a rule split across three files in one language and two in another still
// reads the same way.
func readMinGroupSizeGenerators(t *testing.T) map[string]string {
	t.Helper()
	out := make(map[string]string, len(minGroupSizeGenerators))
	for lang, paths := range minGroupSizeGenerators {
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

// wantMinGroupSizeShared asserts one shared contract string appears in all three.
func wantMinGroupSizeShared(t *testing.T, srcs map[string]string, what, want string) {
	t.Helper()
	var missing []string
	for lang, src := range srcs {
		if !strings.Contains(src, want) {
			missing = append(missing, lang)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%s: %v no longer emit %q, but the other languages still do. "+
			"(spec.record).min_group_size must not read differently depending on which language hosts the "+
			"size-only query — either every language changes together or none does",
			what, missing, want)
	}
}

// wantMinGroupSizePerLanguage asserts each language's own expression of a shared
// rule, and that no language is missing a row.
func wantMinGroupSizePerLanguage(t *testing.T, srcs map[string]string, what string, want map[string]string) {
	t.Helper()
	for lang, w := range want {
		if !strings.Contains(srcs[lang], w) {
			t.Errorf("%s: the %s generator no longer renders %q — it has dropped the rule the other two still "+
				"enforce, and a %s host of this size-only query is now wider than a host in any other language",
				what, lang, w, lang)
		}
	}
	if len(want) != len(minGroupSizeGenerators) {
		t.Errorf("%s: the per-language table covers %d of %d languages; a language with no row is a language "+
			"nothing checks", what, len(want), len(minGroupSizeGenerators))
	}
}

// ---------------------------------------------------------------------------

// THE REFUSAL. Below the threshold the wrapper — never the handler — denies,
// with PERMISSION_DENIED and no count in the text: naming the actual number, or
// any bound tighter than the declared threshold, would disclose exactly what the
// guard exists to withhold. The threshold itself is in the proto and is safe to
// name.
func TestMinGroupSizeRefusalIsIdenticalAcrossLanguages(t *testing.T) {
	srcs := readMinGroupSizeGenerators(t)

	for _, shared := range []string{
		"// (spec.record).min_group_size: a response that carries only a size, over",
		"fewer than %[2]d records, is a membership disclosure by arithmetic. The",
		"// wrapper, never the handler, refuses.",
		"min_group_size: a count over fewer than %[2]d records is a membership disclosure; widen the query's filter",
	} {
		wantMinGroupSizeShared(t, srcs, "the min_group_size refusal", shared)
	}

	// The comparison itself, and the status each language denies with. The
	// threshold is compared against the response's own size field — never a
	// hardcoded "count" — so Get<T>Count and Update<T>Query are guarded by the
	// same rule against different fields.
	wantMinGroupSizePerLanguage(t, srcs, "the below-threshold comparison", map[string]string{
		"go":   "if %[3]s.Get%[4]s() < %[2]d {",
		"java": "if (%[3]s.get%[4]s() < %[2]dL) {",
		"rust": "if %[3]s.%[4]s < %[2]d {",
	})
	wantMinGroupSizePerLanguage(t, srcs, "the refusal status", map[string]string{
		"go":   "status.Error(codes.PermissionDenied,",
		"java": "ProtoException.PERMISSION_DENIED,",
		"rust": "tonic::Status::permission_denied(",
	})
}

// WHICH FIELD IS "THE SIZE" is read off the response message — the one scalar
// integer it carries — by the SHARED parser.MinGroupSizeField. Three languages
// each picking their own field would be three guards, not one.
func TestMinGroupSizeFieldChoiceIsShared(t *testing.T) {
	srcs := readMinGroupSizeGenerators(t)
	wantMinGroupSizeShared(t, srcs, "the size-field choice", "parser.MinGroupSizeField(")
}

// WHICH SEAMS. The unary response and the server stream, in every language: a
// guard on the count and not on the subscription is bypassable in one RPC.
func TestMinGroupSizeCoversTheSameSeamsInEveryLanguage(t *testing.T) {
	srcs := readMinGroupSizeGenerators(t)

	wantMinGroupSizePerLanguage(t, srcs, "the UNARY seam", map[string]string{
		"go":   `goMinGroupSizeGuard(method, san.minGroupSizeField(method.FullOutputType, servicePackage), "resp",`,
		"java": `javaMinGroupSizeGuard(method, san.minGroupSizeField(method.FullOutputType, servicePackage), "response",`,
		"rust": `rustMinGroupSizeGuard(method, san.minGroupSizeField(method.FullOutputType, servicePackage), "resp",`,
	})
	// The stream guard runs BEFORE the per-frame validation in all three, so a
	// below-threshold frame fails the stream carrying its own status rather than
	// being withheld — a stream that goes quiet at exactly N-1 discloses the
	// crossing by silence.
	wantMinGroupSizePerLanguage(t, srcs, "the SERVER STREAM seam", map[string]string{
		"go":   "goMinGroupSizeStreamGuard(method, san.minGroupSizeField(method.FullOutputType, servicePackage), goOutput)",
		"java": `javaMinGroupSizeGuard(method, san.minGroupSizeField(method.FullOutputType, servicePackage), "value",`,
		"rust": "rustMinGroupSizeStreamGuard(method, san.minGroupSizeField(method.FullOutputType, servicePackage))",
	})
}

// WHAT IS STILL REFUSED AT GENERATION, and why none of it is a per-language gap.
// A size-only RPC the wrapper cannot guard must fail the build rather than ship
// the disclosure — the same fail-closed stance (spec.store_principal) takes for
// the shapes its egress filter cannot reach. The two reasons are the same in
// every language, stated in the same words.
func TestMinGroupSizeUnenforceableShapesAreTheSameEverywhere(t *testing.T) {
	srcs := readMinGroupSizeGenerators(t)

	for _, shared := range []string{
		"the guard is generated on unary responses and server streams only",
		"carries no single integer size field for the guard to compare",
	} {
		wantMinGroupSizeShared(t, srcs, "the unguardable-shape refusal", shared)
	}
	wantMinGroupSizePerLanguage(t, srcs, "the generation-time check", map[string]string{
		"go":   "goMinGroupSizeUnenforceable(method, san, info.Package)",
		"java": "javaMinGroupSizeUnenforceable(method, san, info.Package)",
		"rust": "rustMinGroupSizeUnenforceable(method, san, info.Package)",
	})
}

// THE MCP DISPATCH SEAM renders no size-only guard in ANY language, so all three
// refuse to publish a threshold-carrying rpc as a tool. That refusal is parity,
// not a gap: it is Java's own stance, and a tool dispatched by a seam with no
// guard would be served unguarded next door to a gRPC wrapper that guards it.
func TestMinGroupSizeMcpDispatchRefusesInEveryLanguage(t *testing.T) {
	srcs := readMinGroupSizeGenerators(t)

	wantMinGroupSizeShared(t, srcs, "the MCP tool refusal",
		"but the generated MCP dispatch renders no size-only guard; publish it as a gRPC rpc or drop the threshold")
	wantMinGroupSizePerLanguage(t, srcs, "the MCP dispatch check", map[string]string{
		"go":   "func goMcpToolGeneratable(",
		"java": "func javaMcpToolGeneratable(",
		"rust": "func rustMcpToolGeneratable(",
	})
}

// THE OLD REFUSALS ARE GONE. Go and Rust once refused to generate ANY method
// carrying the annotation, on the grounds that only Java could guard it. Both
// now guard it, so those sentences must not survive anywhere — a generator that
// still carries one is a generator that still refuses a surface the other two
// serve.
func TestMinGroupSizeJavaOnlyRefusalsAreGone(t *testing.T) {
	srcs := readMinGroupSizeGenerators(t)

	for _, gone := range []string{
		"a Go host of this record query plane would serve it unguarded",
		"a Rust host of this record query plane would serve it unguarded",
		"a Go host would dispatch it unguarded",
		"a Rust host would dispatch it unguarded",
	} {
		for lang, src := range srcs {
			if strings.Contains(src, gone) {
				t.Errorf("the %s generator still carries the pre-parity refusal %q; (spec.record).min_group_size "+
					"is guarded in Go, Java and Rust now, so refusing the annotation outright is refusing a surface "+
					"this language can serve", lang, gone)
			}
		}
	}
}
