// Package report renders the harness report (archive/MODEL_STRESS_TEST_PLAN.md §7):
// interop matrix, recovery-time distribution, invariant violations, resource
// trends, injected-fault log vs unexpected-failure log, overall verdict + seed.
package report

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/tacplus-dev/tacplus/cross-test/chaos/node"
	"github.com/tacplus-dev/tacplus/cross-test/chaos/oracle"
)

// Report is the full run result.
type Report struct {
	Profile        string
	Seed           int64
	InteropMatrix  []InteropResult
	Verdicts       []oracle.Verdict
	RecoveryTimes  map[string][]time.Duration // fault kind -> observed recovery times
	ResourceTrend  []node.Resources           // per soak cycle, post-recovery
	InjectedFaults []string                   // expected chaos
	Unexpected     []string                   // real failures (hangs, etc.)
}

// InteropResult is one cell of the NxN interop matrix.
type InteropResult struct {
	Pair  string // "go->rust"
	Shape string // "s2s" | "c2s" | "watch"
	Pass  bool
}

// Pass reports the overall verdict: all oracles green and no unexpected
// failures.
func (r *Report) Pass() bool {
	for _, v := range r.Verdicts {
		if !v.Pass {
			return false
		}
	}
	return len(r.Unexpected) == 0
}

// String renders a human-readable report.
func (r *Report) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "=== Chaos harness report: %s (seed=%d) ===\n", r.Profile, r.Seed)

	fmt.Fprintf(&b, "\n-- Interop matrix --\n")
	for _, c := range r.InteropMatrix {
		status := "PASS"
		if !c.Pass {
			status = "FAIL"
		}
		fmt.Fprintf(&b, "  %-16s %-6s %s\n", c.Pair, c.Shape, status)
	}

	fmt.Fprintf(&b, "\n-- Invariant oracles --\n")
	for _, v := range r.Verdicts {
		status := "PASS"
		if !v.Pass {
			status = "FAIL"
		}
		fmt.Fprintf(&b, "  %-32s %s\n", v.Name, status)
		for _, vio := range v.Violations {
			fmt.Fprintf(&b, "      ! %s\n", vio)
		}
	}

	if len(r.RecoveryTimes) > 0 {
		fmt.Fprintf(&b, "\n-- Recovery times (p50/p99) --\n")
		kinds := make([]string, 0, len(r.RecoveryTimes))
		for k := range r.RecoveryTimes {
			kinds = append(kinds, k)
		}
		sort.Strings(kinds)
		for _, k := range kinds {
			p50, p99 := percentiles(r.RecoveryTimes[k])
			fmt.Fprintf(&b, "  %-20s p50=%s p99=%s\n", k, p50, p99)
		}
	}

	if len(r.ResourceTrend) > 0 {
		fmt.Fprintf(&b, "\n-- Resource trend (per cycle) --\n")
		for i, res := range r.ResourceTrend {
			fmt.Fprintf(&b, "  cycle %2d: goroutines=%d fds=%d\n", i, res.Goroutines, res.OpenFDs)
		}
	}

	fmt.Fprintf(&b, "\n-- Injected faults (expected chaos) --\n")
	for _, f := range r.InjectedFaults {
		fmt.Fprintf(&b, "  + %s\n", f)
	}

	if len(r.Unexpected) > 0 {
		fmt.Fprintf(&b, "\n-- UNEXPECTED failures --\n")
		for _, u := range r.Unexpected {
			fmt.Fprintf(&b, "  ! %s\n", u)
		}
	}

	verdict := "PASS"
	if !r.Pass() {
		verdict = "FAIL"
	}
	fmt.Fprintf(&b, "\n=== OVERALL: %s (replay with seed=%d) ===\n", verdict, r.Seed)
	return b.String()
}

func percentiles(ds []time.Duration) (p50, p99 time.Duration) {
	if len(ds) == 0 {
		return 0, 0
	}
	s := append([]time.Duration(nil), ds...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	idx := func(p float64) int {
		i := int(p * float64(len(s)-1))
		if i < 0 {
			i = 0
		}
		if i >= len(s) {
			i = len(s) - 1
		}
		return i
	}
	return s[idx(0.50)], s[idx(0.99)]
}
