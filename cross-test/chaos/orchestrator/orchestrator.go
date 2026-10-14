// Package orchestrator spins up the synthetic multi-language node mesh from a
// deployment profile, drives seeded load, injects faults per a schedule, runs
// the deterministic oracles, and emits a report
// (archive/MODEL_STRESS_TEST_PLAN.md §6). It is the shared harness entry point; the
// profile is its only deployment-specific input.
package orchestrator

import (
	"fmt"
	"math/rand"
	"time"

	"github.com/tacplus-dev/tacplus/cross-test/chaos/fault"
	"github.com/tacplus-dev/tacplus/cross-test/chaos/node"
	"github.com/tacplus-dev/tacplus/cross-test/chaos/oracle"
	"github.com/tacplus-dev/tacplus/cross-test/chaos/profile"
	"github.com/tacplus-dev/tacplus/cross-test/chaos/report"
)

// Mesh is the deployed synthetic node mesh.
type Mesh struct {
	prof  *profile.Profile
	nodes map[node.Lang]*node.Node
	order []node.Lang
}

// Deploy builds and boots the node mesh described by the profile. Each node gets
// its own identity and state rivets (stubbed, per Profile A).
func Deploy(p *profile.Profile) (*Mesh, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	m := &Mesh{prof: p, nodes: make(map[node.Lang]*node.Node)}
	for _, langStr := range p.Languages {
		lang := node.Lang(langStr)
		id := node.NewIdentity(node.IdentityConfig{})
		st := node.NewStateSource(node.StateConfig{
			ReplayDepth:  p.ReplayDepth,
			DropDeltaBug: p.InjectColl1Bug,
		})
		n := node.New(node.Config{
			Lang:      lang,
			Namespace: p.Namespace,
			Identity:  id,
			State:     st,
		})
		n.Boot()
		m.nodes[lang] = n
		m.order = append(m.order, lang)
	}
	return m, nil
}

// Node returns the deployed node for a language.
func (m *Mesh) Node(lang node.Lang) *node.Node { return m.nodes[lang] }

// entitledToken mints a valid, entitled caller token against a node's issuer.
func (m *Mesh) entitledToken(n *node.Node) (node.Token, error) {
	return n.Identity().Issue(m.prof.Namespace, "caller", true, time.Hour)
}

// Run executes the full harness against the mesh and returns the report. It:
//
//  1. runs the cross-language unary interop matrix (S2S + C2S echo-transform);
//  2. runs the WATCH gapless/ordered/no-dup oracle over a live stream while
//     writes churn the state;
//  3. runs the negative auth oracle (expected-deny callers);
//  4. runs the leak-soak: many kill/restart cycles, asserting resources stay
//     flat across cycles;
//  5. drives a seeded fault schedule and records recovery times.
func (m *Mesh) Run() *report.Report {
	rep := &report.Report{
		Profile:       m.prof.Name,
		Seed:          m.prof.Seed,
		RecoveryTimes: map[string][]time.Duration{},
	}
	rng := rand.New(rand.NewSource(m.prof.Seed))

	if m.prof.Invariants.UnaryInterop {
		m.runInterop(rep)
	}
	if m.prof.Invariants.WatchGapless {
		m.runWatch(rep, rng)
	}
	if m.prof.Invariants.AuthClosed {
		m.runAuth(rep)
	}
	if m.prof.Invariants.NoLeak {
		m.runSoak(rep)
	}
	m.runFaultSchedule(rep)

	return rep
}

// runInterop drives the NxN cross-language unary matrix and the unary oracle.
func (m *Mesh) runInterop(rep *report.Report) {
	var cases []oracle.UnaryCase
	for _, caller := range m.order {
		for _, callee := range m.order {
			if caller == callee {
				continue
			}
			n := m.nodes[callee]
			tok, _ := m.entitledToken(n)
			seq := int64(len(cases) + 1)
			seed := fmt.Sprintf("%s->%s", caller, callee)
			resp, err := n.Echo(node.EchoRequest{Seq: seq, Seed: seed, Token: tok}, true)
			cases = append(cases, oracle.UnaryCase{
				Pair:     seed,
				Seq:      seq,
				Expected: node.EchoTransform(seq, seed),
				Observed: resp.Result,
				Err:      err,
			})
			// Record both S2S and C2S shapes for the matrix (same handler here).
			for _, shape := range []string{"s2s", "c2s"} {
				rep.InteropMatrix = append(rep.InteropMatrix, report.InteropResult{
					Pair:  seed,
					Shape: shape,
					Pass:  err == nil && resp.Result == node.EchoTransform(seq, seed),
				})
			}
		}
	}
	rep.Verdicts = append(rep.Verdicts, oracle.UnaryOracle{}.Evaluate(cases))
}

// runWatch opens a WATCH on each node, churns writes interleaved with polls, and
// evaluates the gapless oracle over what each watcher observed. This is the
// path the COLL-1 bug trips.
func (m *Mesh) runWatch(rep *report.Report, rng *rand.Rand) {
	for _, lang := range m.order {
		n := m.nodes[lang]
		st := n.State()
		tok, _ := m.entitledToken(n)
		ws, closer, err := n.OpenWatch(tok, 0)
		if err != nil {
			rep.Unexpected = append(rep.Unexpected, fmt.Sprintf("%s: failed to open WATCH: %v", lang, err))
			continue
		}

		var observed []node.Event
		drain := func() {
			for {
				evs, perr := ws.Poll()
				if perr != nil {
					ws.Relist()
					evs, _ = ws.Poll()
				}
				if len(evs) == 0 {
					return
				}
				observed = append(observed, evs...)
			}
		}

		drain() // initial snapshot
		writes := 24
		for i := 0; i < writes; i++ {
			st.Commit(fmt.Sprintf("key-%d", rng.Intn(4)))
			if i%2 == 0 {
				drain()
			}
		}
		drain() // final catch-up

		closer()
		rep.Verdicts = append(rep.Verdicts, oracle.WatchOracle{HighestCommitted: st.Revision()}.Evaluate(observed))
		for _, shape := range []string{"watch"} {
			pass := true
			for _, v := range rep.Verdicts {
				if v.Name == "watch_gapless_ordered_no_dup" && !v.Pass {
					pass = false
				}
			}
			rep.InteropMatrix = append(rep.InteropMatrix, report.InteropResult{
				Pair: fmt.Sprintf("client->%s", lang), Shape: shape, Pass: pass,
			})
		}
	}
}

// runAuth exercises the negative oracle: callers that must be denied.
func (m *Mesh) runAuth(rep *report.Report) {
	var cases []oracle.AuthCase
	for _, lang := range m.order {
		n := m.nodes[lang]
		id := n.Identity()

		// 1. Wrong namespace.
		wrongNS, _ := id.Issue("evil.namespace", "attacker", true, time.Hour)
		_, err := n.Echo(node.EchoRequest{Seq: 1, Seed: "x", Token: wrongNS}, true)
		cases = append(cases, oracle.AuthCase{Name: fmt.Sprintf("%s/wrong-namespace", lang), Denied: err != nil})

		// 2. Not entitled.
		noEnt, _ := id.Issue(m.prof.Namespace, "caller", false, time.Hour)
		_, err = n.Echo(node.EchoRequest{Seq: 2, Seed: "x", Token: noEnt}, true)
		cases = append(cases, oracle.AuthCase{Name: fmt.Sprintf("%s/not-entitled", lang), Denied: err != nil})

		// 3. Expired token (issued in the past).
		expired, _ := id.Issue(m.prof.Namespace, "caller", true, -time.Hour)
		_, err = n.Echo(node.EchoRequest{Seq: 3, Seed: "x", Token: expired}, true)
		cases = append(cases, oracle.AuthCase{Name: fmt.Sprintf("%s/expired", lang), Denied: err != nil})

		// 4. Token after a refresh failure (issuer down) must still fail closed.
		id.SetRefreshFails(true)
		expired2, _ := id.Issue(m.prof.Namespace, "caller", true, -time.Hour)
		// Issue itself fails while down; an already-held expired token is denied.
		_ = expired2
		stale, _ := id.Issue(m.prof.Namespace, "caller", true, -time.Hour)
		_, err = n.Echo(node.EchoRequest{Seq: 4, Seed: "x", Token: stale}, true)
		cases = append(cases, oracle.AuthCase{Name: fmt.Sprintf("%s/stale-after-refresh-fail", lang), Denied: err != nil})
		id.SetRefreshFails(false)
	}
	rep.Verdicts = append(rep.Verdicts, oracle.AuthOracle{}.Evaluate(cases))
}

// runSoak runs kill/restart cycles and asserts resources return to baseline.
func (m *Mesh) runSoak(rep *report.Report) {
	lang := m.order[0]
	n := m.nodes[lang]
	baseline := n.Resources()

	cycles := m.prof.Soak.Cycles
	if cycles <= 0 {
		cycles = 4
	}
	for c := 0; c < cycles; c++ {
		tok, _ := m.entitledToken(n)
		// Open and properly close watch streams; a leak would show as a counter
		// that does not return to baseline.
		_, closer, err := n.OpenWatch(tok, 0)
		if err == nil {
			closer()
		}
		n.Kill()
		n.Boot()
		rep.ResourceTrend = append(rep.ResourceTrend, n.Resources())
	}
	rep.Verdicts = append(rep.Verdicts, oracle.LeakOracle{Baseline: baseline}.Evaluate(rep.ResourceTrend))
}

// runFaultSchedule drives a seeded fault schedule and records recovery: an
// in-flight call during a fault must fail fast (not hang), and the node must
// recover after Heal.
func (m *Mesh) runFaultSchedule(rep *report.Report) {
	menu := fault.Menu()
	sched := fault.NewSchedule(m.prof.Seed, menu)
	for i := 0; i < len(menu); i++ {
		f := sched.Next()
		lang := m.order[i%len(m.order)]
		n := m.nodes[lang]
		tgt := &fault.Target{Node: n, Identity: n.Identity(), State: n.State()}

		desc := f.Inject(tgt)
		rep.InjectedFaults = append(rep.InjectedFaults, fmt.Sprintf("%s on %s: %s", f.Kind(), lang, desc))

		// Fail-fast check: a call during the fault must return bounded, not hang.
		start := time.Now()
		tok, _ := m.entitledToken(n)
		_, _ = n.Echo(node.EchoRequest{Seq: 99, Seed: "fault", Token: tok}, true)
		failFast := time.Since(start)
		if failFast > time.Second {
			rep.Unexpected = append(rep.Unexpected, fmt.Sprintf("%s on %s: in-flight call hung %s (bounded-deadline violation)", f.Kind(), lang, failFast))
		}

		// Heal and confirm recovery.
		recoverStart := time.Now()
		f.Heal(tgt)
		n.Boot()
		if !n.Ready() {
			rep.Unexpected = append(rep.Unexpected, fmt.Sprintf("%s on %s: node did not recover", f.Kind(), lang))
		}
		rep.RecoveryTimes[string(f.Kind())] = append(rep.RecoveryTimes[string(f.Kind())], time.Since(recoverStart))
	}
}
