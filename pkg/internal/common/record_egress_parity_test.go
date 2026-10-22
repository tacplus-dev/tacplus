// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"os"
	"strings"
	"testing"
)

// The record plane's EGRESS ROW FILTERS across the three full languages.
//
// Go, Java and Rust each generate a service wrapper that narrows the rows a
// read hands back to the caller's own tenant and owner. Java and Rust have done
// so for some time; the Go wrapper rendered NO filter on any seam and refused
// generation wherever one was owed (the refusal lived in
// go/types/servicewrapper.go's goPinnedEgressRows/goPrivateEgressRows). Go's
// filter (go/types/recordegress_gen.go) was ported from the other two rather
// than re-derived, and these tests are what keeps the three from drifting apart
// again.
//
// WHY SOURCE-LEVEL, AND WHY BOTH HALVES. Each test asserts two things:
//
//	the SHARED half — literal text that must be byte-identical in all three
//	    generators, because it is the same contract stated once: the operator-
//	    facing FATAL diagnostics, and the refusal a caller reads. A language
//	    that reworded one has drifted from the other two on the only surface
//	    anybody outside the process can observe.
//	the MECHANICAL half — the per-language expression of the SAME rule, one
//	    row per language. A language that loses its row has dropped the rule
//	    while the shared strings still sit in the file.
//
// Neither half alone is enough: the first would pass on a language that kept
// the message and deleted the check, the second on three languages checking
// three different things.

// recordEgressGenerators is where each language renders the filter.
var recordEgressGenerators = map[string][]string{
	"go":   {"../go/types/recordegress_gen.go"},
	"java": {"../java/types/recordnamespace_gen.go", "../java/types/recordprincipal_gen.go"},
	"rust": {"../rust/types/namespace_gen.go", "../rust/types/principal_gen.go", "../rust/types/watch_egress_gen.go"},
}

// readRecordEgressGenerators returns each language's generator sources joined,
// so a rule split across two files in one language and one in another still
// reads the same way.
func readRecordEgressGenerators(t *testing.T) map[string]string {
	t.Helper()
	out := make(map[string]string, len(recordEgressGenerators))
	for lang, paths := range recordEgressGenerators {
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

// wantInEveryLanguage asserts one shared contract string appears in all three.
func wantInEveryLanguage(t *testing.T, srcs map[string]string, what, want string) {
	t.Helper()
	var missing []string
	for lang, src := range srcs {
		if !strings.Contains(src, want) {
			missing = append(missing, lang)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%s: %v no longer emit %q, but the other languages still do. "+
			"The same annotation must not read differently depending on which language hosts the read — "+
			"either every language changes together or none does",
			what, missing, want)
	}
}

// wantPerLanguage asserts each language's own expression of a shared rule.
func wantPerLanguage(t *testing.T, srcs map[string]string, what string, want map[string]string) {
	t.Helper()
	for lang, w := range want {
		if !strings.Contains(srcs[lang], w) {
			t.Errorf("%s: the %s generator no longer renders %q — it has dropped the rule the other "+
				"two still enforce, and a %s host of this read is now wider than a host in any other language",
				what, lang, w, lang)
		}
	}
	if len(want) != len(recordEgressGenerators) {
		t.Errorf("%s: the per-language table covers %d of %d languages; a language with no row is a language "+
			"nothing checks", what, len(want), len(recordEgressGenerators))
	}
}

// ---------------------------------------------------------------------------

// (spec.store_namespace): a row is accepted when its namespace EXACTLY equals
// the caller's namespace claim, or is named by one of the caller's `linked`
// claims. A row that is neither ABORTS the process — the filter is the backstop
// BEHIND the store's own query, so it firing means that query is leaking across
// tenants and a thrown error would be swallowed as one failed RPC.
//
// On the synthesized record plane's s2s half a RUNTIME gate decides which read
// the caller gets: a /system peer keeps the fleet-wide read the plane has always
// served, a /tenants/<t> peer is pinned to its own tenant. The gate is keyed on
// the CALLER's identity, never on which service hosts the plane.
func TestRecordNamespacePinIsIdenticalAcrossLanguages(t *testing.T) {
	srcs := readRecordEgressGenerators(t)

	for _, shared := range []string{
		"FATAL: (spec.store_namespace) backstop fired:",
		"stamped with a namespace other than the caller's",
		"the store's own query is leaking across tenants",
	} {
		wantInEveryLanguage(t, srcs, "(spec.store_namespace) backstop", shared)
	}

	wantPerLanguage(t, srcs, "the caller's namespace claim", map[string]string{
		"go":   "caller.Token().GetNamespace()",
		"java": "Auth.callerNamespaceFromContext()",
		"rust": ".and_then(|c| c.namespace())",
	})
	// The `linked` widening: a row in a namespace the caller is LINKED to is
	// accepted alongside one in its own. A language that reads the namespace
	// claim and not the linked claim answers a narrower read than the others.
	wantPerLanguage(t, srcs, "the linked-claim widening", map[string]string{
		"go":   "caller.Token().GetLinked()",
		"java": "Auth.callerLinkedFromContext()",
		"rust": "caller.as_ref().map(|c| c.linked()).unwrap_or_default()",
	})
	// The abort, not a returned error: deferred/shutdown work draining in-flight
	// requests would let the very rows this fired over finish leaving the process.
	wantPerLanguage(t, srcs, "the abort on a foreign tenant's row", map[string]string{
		"go":   "os.Exit(1)",
		"java": "Runtime.getRuntime().halt(1)",
		"rust": "std::process::abort()",
	})
	// The s2s record plane's runtime gate.
	wantPerLanguage(t, srcs, "the /system runtime gate", map[string]string{
		"go":   `!strings.HasPrefix(__readNamespace, "/system")`,
		"java": `!__rowNamespace.startsWith("/system")`,
		"rust": `.starts_with("/system")`,
	})
	// The gate is a branch, not the presence of the code: an unpinned (/system)
	// caller keeps every row, so the KEEP test is what carries it and nothing
	// downstream changes shape.
	wantPerLanguage(t, srcs, "the keep-test form of the runtime gate", map[string]string{
		"go":   `return "!__nsPinned || " + keep`,
		"java": `keep = "!__nsPinned || " + keep`,
		"rust": `return "!__ns_pinned || " + keep`,
	})
}

// (spec.store_principal): a row is accepted when its owning-principal field
// equals the AUTHENTICATED caller's subject — the token's `sub` claim verbatim,
// because that is exactly what EventBus stamps into the row's owner field at
// publish time. A mismatch DROPS the row and the call proceeds: one principal's
// rows reaching another is a narrowing the plane is expected to perform, not
// evidence of a broken query, which is the one deliberate difference from the
// namespace pin above.
//
// C2S ONLY, in every language: the comparison is a statement about a USER, and
// on the s2s half the caller is a peer service authorized by identity through
// auth_services — narrowing its reads to rows it published itself would break
// peer aggregation rather than draw a privacy boundary.
func TestRecordPrincipalPinIsIdenticalAcrossLanguages(t *testing.T) {
	srcs := readRecordEgressGenerators(t)

	wantInEveryLanguage(t, srcs, "the fail-closed principal refusal",
		"requires an authenticated caller")

	wantPerLanguage(t, srcs, "the caller's subject", map[string]string{
		"go":   "caller.Token().GetSubject()",
		"java": "__pTok.getSubject()",
		"rust": ".and_then(|c| c.token().subject())",
	})
	// A plain (spec.store_principal) mismatch drops the row; only a private one
	// aborts. A language that halts here is stricter than the other two and
	// turns an ordinary narrowing into an outage.
	wantPerLanguage(t, srcs, "the soft drop on a non-private principal mismatch", map[string]string{
		"go":   "__row.Get%s() == __readPrincipal",
		"java": "if (!__rowPrincipal.equals(__pfb.get%[2]s(__pi).get%[3]s())) { __pfb.remove%[2]s(__pi); }",
		"rust": "resp.%s.retain(|__row| __row.%s == __read_principal);",
	})
	// The filter runs on the PAGE seam before the page is assembled, so
	// next_page_token and total_count describe the rows actually shown.
	wantPerLanguage(t, srcs, "the page-seam filter", map[string]string{
		"go":   "func goPrincipalPageFilter(",
		"java": "func javaPrincipalPageFilter(",
		"rust": "func rustResponsePrincipalFilterBlock(",
	})
}

// (spec.record).private: the same owner pin, but with two differences that make
// it stronger than (spec.store_principal)'s, and both must hold in all three.
//
//	IT ABORTS. Reaching a foreign owner's row on a private record means the
//	    store's own query already leaked it — the namespace pin's posture,
//	    applied to the owner seam.
//	IT BINDS AGAINST THE DELEGATE. A service/workload token calling on another's
//	    behalf is not the row's owner; whoever it is acting for is. So the pin
//	    reads the `act` claim, and a service identity calling WITHOUT one is
//	    refused outright — PERMISSION_DENIED, never a default or empty owner
//	    that would match a row by accident.
func TestRecordPrivateOwnerPinIsIdenticalAcrossLanguages(t *testing.T) {
	srcs := readRecordEgressGenerators(t)

	for _, shared := range []string{
		"FATAL: (spec.record).private backstop fired:",
		"owned by a principal other than the caller's",
		"the store's own query is leaking across owners",
		// The delegated-token gate's refusal, word for word.
		"reads a private record and requires a delegated caller identity (act claim)",
	} {
		wantInEveryLanguage(t, srcs, "(spec.record).private owner pin", shared)
	}

	// Which caller kinds are delegating. A language that widened or narrowed
	// this set pins a different identity than the other two for the same token.
	wantPerLanguage(t, srcs, "the delegating sub_type set", map[string]string{
		"go":   `case "service", "workload":`,
		"java": `if ("service".equals(__pSubType) || "workload".equals(__pSubType)) {`,
		"rust": `"service" | "workload" => `,
	})
	wantPerLanguage(t, srcs, "the act (delegate) claim", map[string]string{
		"go":   "caller.Token().GetAct()",
		"java": "__pTok.getAct()",
		"rust": "token().actor()",
	})
	// A non-delegating caller still pins against its own subject — the gate
	// changes WHICH identity is compared, never whether one is.
	wantPerLanguage(t, srcs, "the non-delegating fallback to the subject", map[string]string{
		"go":   "default:\n%[1]s\t__readPrincipal = caller.Token().GetSubject()",
		"java": "} else {\n%[1]s    __rowPrincipal = __pTok.getSubject();",
		"rust": "_ => %[2]s.token().subject()",
	})
	// And it aborts rather than dropping, on every seam that carries rows.
	wantPerLanguage(t, srcs, "the abort on a private-record owner mismatch", map[string]string{
		"go":   "func goPrincipalCrash(",
		"java": "func javaPrincipalCrash(",
		"rust": "func rustPrincipalCrash(",
	})
}

// ---------------------------------------------------------------------------

// THE ORDER THE TWO FILTERS RUN IN, on every seam that carries both.
//
// A (spec.store_principal) mismatch DROPS the row and the call proceeds; a
// (spec.store_namespace) mismatch ABORTS the process. So for a row that fails
// BOTH — a foreign tenant's row that the caller also does not own — the order
// decides whether the process survives: principal-first drops it quietly,
// namespace-first halts. Every seam in every language must therefore splice the
// principal filter BEFORE the namespace filter, and a language whose one seam
// disagrees with its own other seam is the exact drift this test exists for
// (Rust's unary seam was that seam, prepending each filter in turn so the
// namespace block landed outermost while its own PAGE seam appended them in
// order).
//
// Asserted at the SPLICE SITE rather than on the rendered filters, because the
// filters themselves are identical wherever they run — only the site knows
// which goes first.
type egressSpliceSite struct {
	file      string
	principal string
	namespace string
}

var recordEgressSpliceOrder = map[string]map[string]egressSpliceSite{
	"unary": {
		"go": {
			file:      "../go/types/recordegress_gen.go",
			principal: `goPrincipalUnaryFilter(principal, "resp", method.Name, indent) +`,
			namespace: `goNamespaceUnaryFilter(namespace, "resp", method.Name, indent, recordPlaneS2S)`,
		},
		"java": {
			file:      "../java/types/servicewrapper_gen.go",
			principal: "unaryPrincipal +",
			namespace: " unaryNamespace +",
		},
		"rust": {
			file:      "../rust/types/servicewrapper_gen.go",
			principal: "unaryEgressFilter += rustResponsePrincipalFilterBlock(",
			namespace: "unaryEgressFilter += rustResponseNamespaceFilterBlock(",
		},
	},
	"page": {
		"go": {
			file:      "../go/types/recordegress_gen.go",
			principal: "goPrincipalPageFilter(principal, method.Name, indent, private) +",
			namespace: "goNamespacePageFilter(namespace, method.Name, indent, recordPlaneS2S)",
		},
		"java": {
			file:      "../java/types/servicewrapper_gen.go",
			principal: "pageFilter := javaPrincipalPageFilter(",
			namespace: "pageFilter += nsFilter",
		},
		"rust": {
			file:      "../rust/types/servicewrapper_gen.go",
			principal: "pageEgressFilter += rustResponsePrincipalFilterBlock(",
			namespace: "pageEgressFilter += rustResponseNamespaceFilterBlock(",
		},
	},
}

func TestRecordEgressFiltersPrincipalThenNamespaceInEveryLanguage(t *testing.T) {
	for _, seam := range []string{"unary", "page"} {
		sites := recordEgressSpliceOrder[seam]
		if len(sites) != len(recordEgressGenerators) {
			t.Errorf("%s seam: the splice table covers %d of %d languages; a language with no row is a "+
				"language nothing checks", seam, len(sites), len(recordEgressGenerators))
		}
		for lang, site := range sites {
			src, err := os.ReadFile(site.file)
			if err != nil {
				t.Fatalf("%s/%s: read %s: %v", seam, lang, site.file, err)
			}
			text := string(src)
			pi := strings.Index(text, site.principal)
			ni := strings.Index(text, site.namespace)
			if pi < 0 || ni < 0 {
				t.Errorf("%s/%s: %s no longer splices both egress filters at the site this test knows "+
					"(principal found=%t, namespace found=%t). The seam was reworked; re-point the table at the "+
					"new site and re-assert the order, do not delete the row",
					seam, lang, site.file, pi >= 0, ni >= 0)
				continue
			}
			if pi > ni {
				t.Errorf("%s/%s: the (spec.store_namespace) filter is spliced BEFORE the "+
					"(spec.store_principal) filter in %s. A principal mismatch drops the row and proceeds, a "+
					"namespace mismatch aborts the process — so a row failing both now HALTS this language's "+
					"%s seam where every other seam in the fleet drops it silently. Principal first, always",
					seam, lang, site.file, seam)
			}
		}
	}
}
