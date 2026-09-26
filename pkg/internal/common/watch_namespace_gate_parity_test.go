// Copyright (c) 2026 Second Star Cavalier LLC

package common

import (
	"os"
	"strings"
	"testing"
)

// (spec.store_namespace) ON A WATCH IS GATED THE SAME WAY IT IS ON A UNARY READ.
//
// The namespace egress filter compares a row's tenant against the AUTHENTICATED
// CALLER's namespace claim. That is a tenant boundary on exactly two surfaces:
//
//	the c2s half             — the caller is a user and the claim is its tenant.
//	the s2s RECORD plane     — two kinds of peer reach it, so a RUNTIME gate
//	                           (__nsPinned / __ns_pinned) decides per caller:
//	                           /system keeps the fleet-wide read, /tenants/<t>
//	                           is pinned to its own tenant.
//
// On any OTHER s2s surface it is not a boundary at all. Every caller there is a
// fleet peer authorized by identity through auth_services, and its reads were
// never tenant-scoped — so narrowing them to the peer's own namespace does not
// draw a boundary, it breaks the aggregation the surface exists to serve. Worse
// than breaking it: a namespace mismatch on this filter ABORTS the process
// (record_egress_parity_test.go pins that, and it is the right response to a
// query that really did leak), so emitting the comparison where it can only ever
// mismatch converts a legitimate read into a crash on its first row.
//
// Every language therefore carries an EXISTENCE gate — `isC2S || recordPlaneS2S`
// — around the namespace filter on every seam, deciding whether the comparison
// is rendered at all, ahead of the runtime gate that decides whether a rendered
// comparison fires. Go and Java carried it on the WATCH seam from the start;
// Rust carried it on its unary and PAGE seams and NOT on WATCH, so a plain s2s
// service's watched read aborted where the same service's unary read over the
// same rows returned the rows. This file is what keeps the third seam's gate
// from being dropped again in any language.
//
// WHY THE CALL SITE AND NOT THE FILTER. The gate is not in the generator that
// renders the comparison — it is at the seam that decides to call it, which is
// why it was possible to have three matching filters and one unguarded seam. So
// each row below names the language's WATCH-seam CALL into its namespace guard,
// and asserts the gate stands immediately above that call.

// watchNamespaceGateSeams is, per language, the WATCH seam that decides whether
// a namespace guard is rendered: the file it lives in, the call it makes into
// the guard, and the existence gate that must stand above that call.
var watchNamespaceGateSeams = map[string]struct {
	file     string
	callSite string
	gate     string
}{
	"go": {
		"../go/types/recordegress_gen.go",
		"san.watchEgress(svc.Name, method.Name, method.Resolved.WatchItemType",
		"if !svc.IsC2S && !recordPlaneS2S {",
	},
	"java": {
		"../java/types/servicewrapper_gen.go",
		"san.javaNamespaceWatchGuard(method.FullOutputType",
		"if isC2S || recordPlaneS2S {",
	},
	"rust": {
		"../rust/types/servicewrapper_gen.go",
		"rustWatchEgressPreamble(watchEgress",
		"if !svc.IsC2S && !recordPlaneS2S {",
	},
}

// watchNamespaceGateWindow is how far above the call site the gate may sit. Wide
// enough for the comment each language writes over it, narrow enough that a gate
// on some unrelated seam elsewhere in the file cannot satisfy the assertion.
const watchNamespaceGateWindow = 1600

func TestWatchNamespaceGuardIsGatedIdenticallyAcrossLanguages(t *testing.T) {
	if len(watchNamespaceGateSeams) != len(recordEgressGenerators) {
		t.Fatalf("the per-language table covers %d of %d languages that render this filter; "+
			"a language with no row is a language nothing checks",
			len(watchNamespaceGateSeams), len(recordEgressGenerators))
	}
	for lang, seam := range watchNamespaceGateSeams {
		src, err := os.ReadFile(seam.file)
		if err != nil {
			t.Fatalf("%s: read %s: %v", lang, seam.file, err)
		}
		idx := strings.Index(string(src), seam.callSite)
		if idx < 0 {
			t.Errorf("%s: %s no longer calls its WATCH namespace guard as %q, so nothing here can "+
				"check whether that call is gated — re-anchor this row on the call the seam makes now",
				lang, seam.file, seam.callSite)
			continue
		}
		from := idx - watchNamespaceGateWindow
		if from < 0 {
			from = 0
		}
		if !strings.Contains(string(src)[from:idx], seam.gate) {
			t.Errorf("%s: the WATCH seam in %s calls its (spec.store_namespace) guard with no %q above it. "+
				"The comparison is against the caller's own namespace claim, which is a tenant boundary on "+
				"the c2s half and on the s2s record plane and nothing anywhere else — and a mismatch ABORTS "+
				"the process. Ungated, a plain s2s service's watched read over record rows crashes on its "+
				"first frame while the same service's unary read over the same rows returns them",
				lang, seam.file, seam.gate)
		}
	}
}
