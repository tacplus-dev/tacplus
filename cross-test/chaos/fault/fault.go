// Package fault is the chaos/fault engine: a fault-menu abstraction plus
// concrete faults from docs/MODEL_STRESS_TEST_PLAN.md §4. It is deployment-
// agnostic — faults act on a Target interface so the same engine drives both
// the Profile A synthetic mesh and a future Profile B real stack.
package fault

import (
	"math/rand"
	"time"

	"github.com/tacplus-dev/tacplus/cross-test/chaos/node"
)

// Target is the surface a fault acts on: one node plus its rivets. The
// orchestrator supplies a Target per node; faults manipulate it and Heal undoes
// the manipulation so recovery can be observed.
type Target struct {
	Node     *node.Node
	Identity *node.Identity
	State    *node.StateSource

	// Watchers are the live WATCH streams against this target's state. The
	// network-partition fault toggles their blackhole flag.
	Watchers []*node.WatchStream
}

// Kind enumerates the fault menu.
type Kind string

const (
	KindSIGTERM          Kind = "sigterm"           // bounded graceful shutdown
	KindSIGKILL          Kind = "sigkill"           // crash recovery
	KindNetworkPartition Kind = "network_partition" // blackhole to state store
	KindDependencyDown   Kind = "dependency_down"   // identity issuer down (fail-closed)
	KindTokenExpiry      Kind = "token_expiry"      // clock skew / expiry
	KindBackpressure     Kind = "backpressure"      // slow consumer / drop deltas window
)

// Fault is one injectable fault. Inject perturbs the target; Heal restores it.
// Faults are reversible so the recovery and leak oracles can measure recovery.
type Fault interface {
	Kind() Kind
	// Inject perturbs the target. It returns a description for the report's
	// injected-fault log (distinguishing expected chaos from real failures).
	Inject(t *Target) string
	// Heal reverses the fault, restoring the target to a recoverable state.
	Heal(t *Target)
}

// SIGTERMFault gracefully drains the node within a bounded budget.
type SIGTERMFault struct{ Budget time.Duration }

func (f SIGTERMFault) Kind() Kind { return KindSIGTERM }
func (f SIGTERMFault) Inject(t *Target) string {
	took := t.Node.Drain(f.Budget)
	return "SIGTERM graceful drain took " + took.String()
}
func (f SIGTERMFault) Heal(t *Target) { t.Node.Boot() }

// SIGKILLFault crashes the node; in-flight calls must fail fast.
type SIGKILLFault struct{}

func (f SIGKILLFault) Kind() Kind              { return KindSIGKILL }
func (f SIGKILLFault) Inject(t *Target) string { t.Node.Kill(); return "SIGKILL crash" }
func (f SIGKILLFault) Heal(t *Target)          { t.Node.Boot() }

// NetworkPartitionFault blackholes the watchers from the state store. Revisions
// keep advancing; the watcher must resume gapless once healed.
type NetworkPartitionFault struct{}

func (f NetworkPartitionFault) Kind() Kind { return KindNetworkPartition }
func (f NetworkPartitionFault) Inject(t *Target) string {
	for _, w := range t.Watchers {
		w.SetPartitioned(true)
	}
	return "network partition to state store"
}
func (f NetworkPartitionFault) Heal(t *Target) {
	for _, w := range t.Watchers {
		w.SetPartitioned(false)
	}
}

// DependencyDownFault takes the identity issuer down. Verification must still
// fail closed (expired tokens denied), and refresh must error.
type DependencyDownFault struct{}

func (f DependencyDownFault) Kind() Kind { return KindDependencyDown }
func (f DependencyDownFault) Inject(t *Target) string {
	t.Identity.SetRefreshFails(true)
	return "identity issuer down"
}
func (f DependencyDownFault) Heal(t *Target) { t.Identity.SetRefreshFails(false) }

// TokenExpiryFault injects clock skew, expiring outstanding tokens.
type TokenExpiryFault struct{ Skew time.Duration }

func (f TokenExpiryFault) Kind() Kind { return KindTokenExpiry }
func (f TokenExpiryFault) Inject(t *Target) string {
	t.Identity.SetClockSkew(f.Skew)
	return "clock skew " + f.Skew.String()
}
func (f TokenExpiryFault) Heal(t *Target) { t.Identity.SetClockSkew(0) }

// Menu is the default fault menu for Profile A.
func Menu() []Fault {
	return []Fault{
		SIGTERMFault{Budget: 5 * time.Second},
		SIGKILLFault{},
		NetworkPartitionFault{},
		DependencyDownFault{},
		TokenExpiryFault{Skew: 2 * time.Hour},
	}
}

// Schedule is a seeded, reproducible sequence of faults. Randomness drives the
// schedule; deterministic oracles drive the verdict (§2).
type Schedule struct {
	rng  *rand.Rand
	menu []Fault
}

// NewSchedule builds a seeded schedule over the given menu.
func NewSchedule(seed int64, menu []Fault) *Schedule {
	return &Schedule{rng: rand.New(rand.NewSource(seed)), menu: menu}
}

// Next returns the next fault to inject (seeded selection).
func (s *Schedule) Next() Fault {
	if len(s.menu) == 0 {
		return nil
	}
	return s.menu[s.rng.Intn(len(s.menu))]
}
