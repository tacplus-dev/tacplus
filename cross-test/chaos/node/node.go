package node

import (
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Lang identifies the synthetic node's language. All languages are functionally
// identical (generated from the same test proto); the field exists so the
// orchestrator can build the cross-language interop matrix.
type Lang string

const (
	Go         Lang = "go"
	Rust       Lang = "rust"
	Java       Lang = "java"
	TypeScript Lang = "typescript"
	Python     Lang = "python"
)

// Phase is the ServiceManager lifecycle phase. Readiness is the generated
// unified gate Ready = cert ∧ config ∧ first-health
// (docs/MODEL_STRESS_TEST_PLAN.md §6).
type Phase int

const (
	PhaseDown Phase = iota
	PhaseBooting
	PhaseReady
	PhaseDraining
)

func (p Phase) String() string {
	switch p {
	case PhaseDown:
		return "down"
	case PhaseBooting:
		return "booting"
	case PhaseReady:
		return "ready"
	case PhaseDraining:
		return "draining"
	default:
		return "unknown"
	}
}

// Resources is a point-in-time scrape of a node's resource counters, the input
// to the leak oracle's trend analysis (docs/MODEL_STRESS_TEST_PLAN.md §5.3).
type Resources struct {
	Goroutines int64 // open watch streams + in-flight tasks
	OpenFDs    int64 // open peer connections
}

// EchoRequest carries a seed; the response is a pure function of it so the
// caller can recompute the expected value (unary S2S/C2S oracle, §2).
type EchoRequest struct {
	Seq   int64
	Seed  string
	Token Token
}

// EchoResponse is the deterministic transform of the request.
type EchoResponse struct {
	Seq    int64
	Result string
}

// Node is a synthetic tacplus-generated model node: both a server (exposes the
// test S2S/C2S services + a WATCH collection) and a client (calls peers). Its
// handlers are thin deterministic stubs; the harness machinery does the work.
type Node struct {
	Lang      Lang
	Namespace string

	identity *Identity
	state    *StateSource

	mu     sync.RWMutex
	phase  Phase
	booted time.Time

	// resource counters — incremented on open, decremented on close. A leak
	// shows as a counter that fails to return to baseline across kill cycles.
	openStreams int64
	openConns   int64

	// crashed marks a SIGKILL: in-flight calls must fail fast, not hang.
	crashed atomic.Bool
}

// Config builds a Node.
type Config struct {
	Lang      Lang
	Namespace string
	Identity  *Identity
	State     *StateSource
}

// New constructs a node in the Down phase. Call Boot to bring it Ready.
func New(cfg Config) *Node {
	ns := cfg.Namespace
	if ns == "" {
		ns = "default"
	}
	return &Node{
		Lang:      cfg.Lang,
		Namespace: ns,
		identity:  cfg.Identity,
		state:     cfg.State,
		phase:     PhaseDown,
	}
}

// Boot runs the SM bootstrap (cert, config, first health) and reaches Ready.
func (n *Node) Boot() {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.phase = PhaseBooting
	n.crashed.Store(false)
	n.booted = time.Now()
	n.phase = PhaseReady
}

// Ready reports the generated unified readiness gate.
func (n *Node) Ready() bool {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.phase == PhaseReady && !n.crashed.Load()
}

// Phase reports the lifecycle phase.
func (n *Node) Phase() Phase {
	n.mu.RLock()
	defer n.mu.RUnlock()
	return n.phase
}

// Identity exposes the node's identity rivet (for the orchestrator to mint
// caller tokens against).
func (n *Node) Identity() *Identity { return n.identity }

// State exposes the node's state source (the WATCH/collection backing).
func (n *Node) State() *StateSource { return n.state }

// Kill simulates SIGKILL: the node crashes, all streams/conns are torn down,
// and in-flight calls fail fast.
func (n *Node) Kill() {
	n.crashed.Store(true)
	n.mu.Lock()
	defer n.mu.Unlock()
	n.phase = PhaseDown
	atomic.StoreInt64(&n.openStreams, 0)
	atomic.StoreInt64(&n.openConns, 0)
}

// Drain simulates SIGTERM: bounded graceful shutdown. Streams/conns close, then
// the node goes Down. Returns the (simulated) time the drain took.
func (n *Node) Drain(budget time.Duration) time.Duration {
	n.mu.Lock()
	n.phase = PhaseDraining
	n.mu.Unlock()

	// A correct drain releases all resources well within the budget.
	atomic.StoreInt64(&n.openStreams, 0)
	atomic.StoreInt64(&n.openConns, 0)

	n.mu.Lock()
	n.phase = PhaseDown
	n.mu.Unlock()
	return budget / 10
}

// Resources scrapes the node's current resource counters.
func (n *Node) Resources() Resources {
	return Resources{
		Goroutines: atomic.LoadInt64(&n.openStreams),
		OpenFDs:    atomic.LoadInt64(&n.openConns),
	}
}

// ErrUnavailable is returned fast when a crashed/down node is called, so the
// recovery oracle can confirm calls fail within a bounded deadline (§5.1).
type ErrUnavailable struct{ Lang Lang }

func (e *ErrUnavailable) Error() string {
	return fmt.Sprintf("node %s unavailable", e.Lang)
}

// Echo is the unary S2S/C2S handler: it verifies the caller token (fail-closed)
// then returns the deterministic transform of the request. requireEntitled
// selects the auth-required vs auth-denied method surface.
func (n *Node) Echo(req EchoRequest, requireEntitled bool) (EchoResponse, error) {
	if !n.Ready() {
		return EchoResponse{}, &ErrUnavailable{Lang: n.Lang}
	}
	if err := n.identity.Verify(req.Token, n.Namespace, requireEntitled); err != nil {
		return EchoResponse{}, err
	}
	atomic.AddInt64(&n.openConns, 1)
	defer atomic.AddInt64(&n.openConns, -1)
	return EchoResponse{Seq: req.Seq, Result: EchoTransform(req.Seq, req.Seed)}, nil
}

// EchoTransform is the pure function the unary oracle recomputes to detect
// validation drift / wrong-bytes binding / truncation.
func EchoTransform(seq int64, seed string) string {
	return fmt.Sprintf("echo:%s#%d", seed, seq)
}

// OpenWatch verifies the caller, opens a WATCH stream, and accounts the stream
// as an open resource. The returned closer must be called to release it (the
// leak oracle checks the counter returns to baseline).
func (n *Node) OpenWatch(t Token, fromRevision int64) (*WatchStream, func(), error) {
	if !n.Ready() {
		return nil, nil, &ErrUnavailable{Lang: n.Lang}
	}
	if err := n.identity.Verify(t, n.Namespace, true); err != nil {
		return nil, nil, err
	}
	atomic.AddInt64(&n.openStreams, 1)
	ws := n.state.NewWatchStream(fromRevision)
	var once sync.Once
	closer := func() {
		once.Do(func() { atomic.AddInt64(&n.openStreams, -1) })
	}
	return ws, closer, nil
}
