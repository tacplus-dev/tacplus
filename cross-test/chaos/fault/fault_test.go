package fault

import (
	"testing"
	"time"

	"gitlab.com/seabeam/schematic/tacplus/cross-test/chaos/node"
)

func newTarget() *Target {
	id := node.NewIdentity(node.IdentityConfig{})
	st := node.NewStateSource(node.StateConfig{ReplayDepth: 16})
	n := node.New(node.Config{Lang: node.Go, Namespace: "ns", Identity: id, State: st})
	n.Boot()
	tok, _ := id.Issue("ns", "caller", true, time.Hour)
	ws, _, _ := n.OpenWatch(tok, 0)
	return &Target{Node: n, Identity: id, State: st, Watchers: []*node.WatchStream{ws}}
}

func TestSIGKILLInjectHealRecovers(t *testing.T) {
	tgt := newTarget()
	f := SIGKILLFault{}
	f.Inject(tgt)
	if tgt.Node.Ready() {
		t.Fatal("node should be down after SIGKILL")
	}
	f.Heal(tgt)
	if !tgt.Node.Ready() {
		t.Fatal("node should recover after heal")
	}
}

func TestNetworkPartitionBlackholesThenResumesGapless(t *testing.T) {
	tgt := newTarget()
	ws := tgt.Watchers[0]
	if _, err := ws.Poll(); err != nil { // snapshot phase
		t.Fatalf("snapshot poll: %v", err)
	}

	NetworkPartitionFault{}.Inject(tgt)
	for i := 0; i < 5; i++ {
		tgt.State.Commit("k")
	}
	evs, _ := ws.Poll()
	if len(evs) != 0 {
		t.Fatalf("partitioned watcher should observe nothing, got %d events", len(evs))
	}

	NetworkPartitionFault{}.Heal(tgt)
	evs, err := ws.Poll()
	if err != nil {
		t.Fatalf("post-heal poll: %v", err)
	}
	if len(evs) != 5 {
		t.Fatalf("watcher must resume gapless with all 5 deltas, got %d", len(evs))
	}
}

func TestDependencyDownFailsClosed(t *testing.T) {
	tgt := newTarget()
	DependencyDownFault{}.Inject(tgt)
	if _, err := tgt.Identity.Issue("ns", "c", true, time.Hour); err == nil {
		t.Fatal("issuer down: Issue must fail")
	}
	DependencyDownFault{}.Heal(tgt)
	if _, err := tgt.Identity.Issue("ns", "c", true, time.Hour); err != nil {
		t.Fatalf("issuer restored: Issue should succeed: %v", err)
	}
}

func TestScheduleDeterministic(t *testing.T) {
	a := NewSchedule(42, Menu())
	b := NewSchedule(42, Menu())
	for i := 0; i < 10; i++ {
		if a.Next().Kind() != b.Next().Kind() {
			t.Fatal("same seed must produce the same schedule (deterministic replay)")
		}
	}
}
