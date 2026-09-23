// Copyright (c) 2026 Second Star Cavalier LLC

package common

import (
	"os"
	"strings"
	"testing"
)

// The record plane's (spec.store_epoch) STAMP across the three full languages.
//
// store_epoch is the row's last-WRITE clock, in Unix milliseconds: what
// deleted_max_age_seconds measures the delete write from and what the disk leg
// ranges on. The store owns it outright, so the wrapper refuses any
// caller-supplied value and writes its own before the handler runs. Java
// generated the stamp for some time; Go and Rust REFUSED to generate any method
// whose request carried the annotation, so a Go or Rust host of an
// epoch-bearing write could not exist at all. Go's and Rust's stamps
// (go/types/storeepoch_gen.go, rust/types/storeepoch_gen.go) were ported from
// Java's rather than re-derived, and these tests are what keeps the three from
// drifting apart.
//
// This is a REQUEST-side mutation, not an egress filter: getting it wrong writes
// the wrong clock into stored rows rather than merely failing a build, which is
// why the ORDERING against the other request-side pins is asserted here and not
// left to each language's own judgment.
//
// WHY SOURCE-LEVEL, AND WHY BOTH HALVES — the same reasoning as
// record_egress_parity_test.go and min_group_size_parity_test.go, which this
// file follows:
//
//	the SHARED half — literal text that must be byte-identical in all three
//	    generators, because it is the same contract stated once: the refusal a
//	    caller reads, the comment that says why, and the one reason a stamp is
//	    refused at generation.
//	the MECHANICAL half — the per-language expression of the SAME rule, one row
//	    per language. A language that loses its row has dropped the rule while
//	    the shared strings still sit in the file.

// storeEpochGenerators is where each language renders the stamp: the emitter
// itself, the wrapper that splices it into the unary seam, and the MCP dispatch
// that refuses a tool it cannot stamp.
var storeEpochGenerators = map[string][]string{
	"go":   {"../go/types/storeepoch_gen.go", "../go/types/servicewrapper.go", "../go/types/mcp_dispatch_gen.go"},
	"java": {"../java/types/servicewrapper_gen.go", "../java/types/mcp_dispatch_gen.go"},
	"rust": {"../rust/types/storeepoch_gen.go", "../rust/types/servicewrapper_gen.go", "../rust/types/mcp_dispatch_gen.go"},
}

// readStoreEpochGenerators returns each language's generator sources joined, so
// a rule split across three files in one language and two in another still reads
// the same way.
func readStoreEpochGenerators(t *testing.T) map[string]string {
	t.Helper()
	out := make(map[string]string, len(storeEpochGenerators))
	for lang, paths := range storeEpochGenerators {
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

// wantStoreEpochShared asserts one shared contract string appears in all three.
func wantStoreEpochShared(t *testing.T, srcs map[string]string, what, want string) {
	t.Helper()
	var missing []string
	for lang, src := range srcs {
		if !strings.Contains(src, want) {
			missing = append(missing, lang)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%s: %v no longer emit %q, but the other languages still do. "+
			"(spec.store_epoch) must not read differently depending on which language hosts the write — "+
			"either every language changes together or none does",
			what, missing, want)
	}
}

// wantStoreEpochPerLanguage asserts each language's own expression of a shared
// rule, and that no language is missing a row.
func wantStoreEpochPerLanguage(t *testing.T, srcs map[string]string, what string, want map[string]string) {
	t.Helper()
	for lang, w := range want {
		if !strings.Contains(srcs[lang], w) {
			t.Errorf("%s: the %s generator no longer renders %q — it has dropped the rule the other two still "+
				"enforce, and a %s host of this write now stores a different clock than a host in any other language",
				what, lang, w, lang)
		}
	}
	if len(want) != len(storeEpochGenerators) {
		t.Errorf("%s: the per-language table covers %d of %d languages; a language with no row is a language "+
			"nothing checks", what, len(want), len(storeEpochGenerators))
	}
}

// ---------------------------------------------------------------------------

// THE STAMP. The comment that says why and the refusal a caller reads are the
// same sentences in all three — one contract, stated once.
func TestStoreEpochStampIsIdenticalAcrossLanguages(t *testing.T) {
	srcs := readStoreEpochGenerators(t)

	for _, shared := range []string{
		"// (spec.store_epoch) %[2]s: the row's last-write clock is the STORE's, never",
		"// the caller's. Unset -> stamped here; set -> refused. The wrapper owns the",
		"// stamp so no handler hand-writes it (same doctrine as (spec.principal)).",
		"%[2]s is stamped by the store and must not be supplied by the caller",
	} {
		wantStoreEpochShared(t, srcs, "the store_epoch stamp", shared)
	}
}

// THERE IS NO "EQUAL PASSES" CASE. (spec.principal) accepts a field that already
// equals the caller's claim; this one does not, because the value is never
// caller-supplied — so the rendered shape is a bare "is it set" test, and the
// refusal is INVALID_ARGUMENT in the wrapper, never in the handler.
func TestStoreEpochRefusesAnySuppliedValueInEveryLanguage(t *testing.T) {
	srcs := readStoreEpochGenerators(t)

	wantStoreEpochPerLanguage(t, srcs, "the set-at-all test", map[string]string{
		"go":   "if %[3]s.%[4]s != 0 {",
		"java": "if (%[3]s.get%[4]s() != 0L) {",
		"rust": "if %[3]s.%[4]s != 0 {",
	})
	wantStoreEpochPerLanguage(t, srcs, "the refusal status", map[string]string{
		"go":   "status.Error(codes.InvalidArgument,",
		"java": "ProtoException.INVALID_ARGUMENT,",
		"rust": "tonic::Status::invalid_argument(",
	})
	// The clock each language writes. Unix MILLISECONDS in all three — the unit
	// spec.proto declares — never seconds and never a monotonic instant.
	wantStoreEpochPerLanguage(t, srcs, "the store's own clock", map[string]string{
		"go":   "%[3]s.%[4]s = time.Now().UnixMilli()",
		"java": "%[3]s.toBuilder().set%[4]s(System.currentTimeMillis()).build();",
		"rust": ".as_millis() as i64;",
	})
}

// WHICH FIELD IS "THE EPOCH" is read off the request message by the SHARED
// parser.StoreEpochField. Three languages each picking their own field would be
// three stamps, not one — and a write that stamped the wrong int64 would be
// silently wrong rather than loudly broken.
func TestStoreEpochFieldChoiceIsShared(t *testing.T) {
	srcs := readStoreEpochGenerators(t)
	wantStoreEpochShared(t, srcs, "the epoch-field choice", "parser.StoreEpochField(")
}

// WHICH SEAM. The UNARY request, in every language, spliced so the stamp runs
// before the handler — a stamp after the handler is a row already written with
// the caller's clock.
func TestStoreEpochCoversTheSameSeamInEveryLanguage(t *testing.T) {
	srcs := readStoreEpochGenerators(t)

	wantStoreEpochPerLanguage(t, srcs, "the UNARY seam", map[string]string{
		"go":   `goStoreEpochStamp(san.storeEpochField(method.FullInputType, servicePackage), "req",`,
		"java": `javaStoreEpochStamp(san.storeEpochField(method.FullInputType, servicePackage), "request",`,
		"rust": `rustStoreEpochStamp(san.storeEpochField(method.FullInputType, servicePackage), "req",`,
	})
}

// ORDERING AGAINST THE OTHER REQUEST-SIDE PINS. This is the half a response
// filter does not have: (spec.store_epoch), (spec.store_principal)/
// (spec.store_namespace) and (spec.principal)/(spec.namespace) all MUTATE the
// request before the handler sees it, so the order they are spliced in is part
// of the contract and not an implementation detail. Java stamps the epoch first;
// Go and Rust splice it at the same position relative to their own pins.
//
// The fields are disjoint — an int64 clock against string principal/namespace
// fields — so no ordering here can change a value. What it buys is one readable
// order across three languages: a future pin that DOES touch the epoch field
// lands in a known place rather than in three different ones.
func TestStoreEpochStampPrecedesTheOtherRequestPins(t *testing.T) {
	srcs := readStoreEpochGenerators(t)

	// (language, the splice of the epoch stamp, the splice of the pin that must
	// follow it, the anchor that closes the unary seam). The pin splice is
	// searched only inside [stamp, seamEnd) — every language splices the same
	// pins on its OTHER seams too, and an unbounded search would find one of
	// those and pass whatever the unary seam actually did.
	for _, tc := range []struct{ lang, stamp, after, seamEnd string }{
		{"go",
			`goStoreEpochStamp(san.storeEpochField(method.FullInputType, servicePackage), "req",`,
			`goRequestScopeBlocks(san, method, servicePackage, "req", "\t", "return nil,")`,
			"goRequiresDeviceGate(method, svc, servicePackage)"},
		{"java",
			`javaStoreEpochStamp(san.storeEpochField(method.FullInputType, servicePackage), "request",`,
			`javaRequestScopePins(san, method.FullInputType, servicePackage, "request", method.Name, "            ")`,
			"// Egress data entitlement for unary responses"},
		{"rust",
			`rustStoreEpochStamp(san.storeEpochField(method.FullInputType, servicePackage), "req",`,
			`rustRequestPrincipalBlock(reqPrincipal, method.Name, "req", "        ")`,
			"if method.IsPageCollection() {"},
	} {
		src := srcs[tc.lang]
		stamp := strings.Index(src, tc.stamp)
		if stamp < 0 {
			t.Errorf("%s: the epoch stamp splice %q is gone", tc.lang, tc.stamp)
			continue
		}
		end := strings.Index(src[stamp:], tc.seamEnd)
		if end < 0 {
			t.Errorf("%s: the unary seam's closing anchor %q no longer follows the epoch stamp; this test can no "+
				"longer tell which seam it is reading", tc.lang, tc.seamEnd)
			continue
		}
		if !strings.Contains(src[stamp:stamp+end], tc.after) {
			t.Errorf("%s: the (spec.principal)/(spec.namespace) request pins no longer follow the "+
				"(spec.store_epoch) stamp on the unary seam; Java stamps the epoch first and the other two "+
				"follow it, so that one order holds in every language", tc.lang)
		}
	}
}

// WHAT IS STILL REFUSED AT GENERATION, and why it is not a per-language gap. A
// streamed write would need a per-item stamp no wrapper renders, so it fails the
// build rather than reaching a handler with an unstamped epoch — a row the purge
// measures as infinitely old. The reason is the same in every language, stated
// in the same words.
func TestStoreEpochUnenforceableShapesAreTheSameEverywhere(t *testing.T) {
	srcs := readStoreEpochGenerators(t)

	wantStoreEpochShared(t, srcs, "the unstampable-shape refusal",
		"the stamp is generated on unary requests only; a streamed write would need a per-item stamp the wrapper does not render")
	wantStoreEpochShared(t, srcs, "the generation-time refusal",
		`carries a (spec.store_epoch) field on %s but %s`)
	wantStoreEpochPerLanguage(t, srcs, "the generation-time check", map[string]string{
		"go":   "goStoreEpochUnenforceable(method, san, info.Package)",
		"java": "javaStoreEpochUnenforceable(method, san, info.Package)",
		"rust": "rustStoreEpochUnenforceable(method, san, info.Package)",
	})
}

// THE MCP DISPATCH SEAM renders no stamp in ANY language, so all three refuse to
// publish an epoch-bearing write as a tool. That refusal is parity, not a gap:
// it is Java's own stance, and a tool dispatched by a seam with no stamp would
// write an unstamped row next door to a gRPC wrapper that stamps it.
func TestStoreEpochMcpDispatchRefusesInEveryLanguage(t *testing.T) {
	srcs := readStoreEpochGenerators(t)

	wantStoreEpochShared(t, srcs, "the MCP tool refusal",
		"but the stamp is rendered by the gRPC wrapper's write path and not by MCP dispatch; a tool call of this write would leave the store epoch unstamped")
	wantStoreEpochPerLanguage(t, srcs, "the MCP dispatch check", map[string]string{
		"go":   "func goMcpToolGeneratable(",
		"java": "func javaMcpToolGeneratable(",
		"rust": "func rustMcpToolGeneratable(",
	})
}

// THE OLD REFUSALS ARE GONE. Go and Rust once refused to generate ANY method
// whose request carried the annotation, on the grounds that only Java could
// stamp it. Both now stamp it, so those sentences must not survive anywhere — a
// generator that still carries one is a generator that still refuses a surface
// the other two serve.
func TestStoreEpochJavaOnlyRefusalsAreGone(t *testing.T) {
	srcs := readStoreEpochGenerators(t)

	for _, gone := range []string{
		"a Go host of this write would leave the store epoch unstamped",
		"a Rust host of this write would leave the store epoch unstamped",
		"the stamp is generated for Java service wrappers only",
	} {
		for lang, src := range srcs {
			if strings.Contains(src, gone) {
				t.Errorf("the %s generator still carries the pre-parity refusal %q; (spec.store_epoch) is stamped "+
					"in Go, Java and Rust now, so refusing the annotation outright is refusing a surface this "+
					"language can serve", lang, gone)
			}
		}
	}
}
