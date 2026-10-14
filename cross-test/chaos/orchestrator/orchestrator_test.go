package orchestrator

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/cross-test/chaos/profile"
)

// runResult wraps a report so tests can introspect verdicts.
type runResult struct {
	verdicts []struct {
		Name string
		Pass bool
	}
	text string
}

func (r *runResult) String() string { return r.text }

func runProfile(t *testing.T, p *profile.Profile) *runResult {
	t.Helper()
	mesh, err := Deploy(p)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	rep := mesh.Run()
	rr := &runResult{text: rep.String()}
	for _, v := range rep.Verdicts {
		rr.verdicts = append(rr.verdicts, struct {
			Name string
			Pass bool
		}{v.Name, v.Pass})
	}
	return rr
}

// TestHealthyProfilePasses confirms the harness goes GREEN on a correct model:
// all oracles pass, no unexpected failures.
func TestHealthyProfilePasses(t *testing.T) {
	p := profile.DefaultProfileA()
	rr := runProfile(t, p)
	if !watchVerdictPassOf(t, rr) {
		t.Fatalf("WATCH oracle should be GREEN on a healthy model:\n%s", rr.text)
	}
	for _, v := range rr.verdicts {
		if !v.Pass {
			t.Fatalf("oracle %q unexpectedly RED on healthy profile:\n%s", v.Name, rr.text)
		}
	}
}

// TestPhase1Acceptance_Coll1BugTurnsWatchOracleRed is THE Phase-1 acceptance
// bar (docs/MODEL_STRESS_TEST_PLAN.md §9.1): re-introduce the COLL-1 dropped-
// delta bug behind a flag and confirm the harness's WATCH oracle goes RED. If
// the harness can't catch a bug we KNOW exists, it isn't yet a test.
func TestPhase1Acceptance_Coll1BugTurnsWatchOracleRed(t *testing.T) {
	// Baseline: bug off -> WATCH oracle GREEN.
	clean := profile.DefaultProfileA()
	clean.InjectColl1Bug = false
	if !watchVerdictPassOf(t, runProfile(t, clean)) {
		t.Fatal("control run (bug off) must be GREEN")
	}

	// Bug on -> WATCH oracle must go RED.
	buggy := profile.DefaultProfileA()
	buggy.InjectColl1Bug = true
	rr := runProfile(t, buggy)
	if watchVerdictPassOf(t, rr) {
		t.Fatalf("ACCEPTANCE FAILED: COLL-1 dropped-delta bug was NOT detected; WATCH oracle stayed GREEN:\n%s", rr.text)
	}
	if !strings.Contains(rr.text, "not gapless") {
		t.Fatalf("report should explain the gap; got:\n%s", rr.text)
	}
	t.Logf("Phase-1 acceptance: COLL-1 bug correctly turned the WATCH oracle RED.")
}

func watchVerdictPassOf(t *testing.T, rr *runResult) bool {
	t.Helper()
	for _, v := range rr.verdicts {
		if v.Name == "watch_gapless_ordered_no_dup" {
			return v.Pass
		}
	}
	t.Fatalf("no watch verdict in:\n%s", rr.text)
	return false
}

func TestDeployRejectsNonModelProfile(t *testing.T) {
	p := profile.DefaultProfileA()
	p.Kind = "system"
	if _, err := Deploy(p); err == nil {
		t.Fatal("Profile B (kind=system) must be rejected: not implemented")
	}
}

func TestInteropMatrixCoversAllPairs(t *testing.T) {
	p := profile.DefaultProfileA()
	p.Languages = []string{"go", "rust", "java"}
	mesh, err := Deploy(p)
	if err != nil {
		t.Fatalf("deploy: %v", err)
	}
	rep := mesh.Run()
	// 3 languages -> 6 ordered pairs, each with s2s + c2s = 12 unary cells.
	unary := 0
	for _, c := range rep.InteropMatrix {
		if c.Shape == "s2s" || c.Shape == "c2s" {
			unary++
		}
	}
	if unary != 12 {
		t.Fatalf("expected 12 unary interop cells for 3 languages, got %d", unary)
	}
}
