// Package profile defines the deployment-profile input to the chaos harness.
//
// The harness (orchestrator, fault engine, oracle framework, report) is built
// reusable: deployment-profile is an INPUT, not hardcoded
// (archive/MODEL_STRESS_TEST_PLAN.md §1a). This package describes Profile A —
// the tacplus-generated model with stubbed rivets (test token/JWK signer,
// in-memory state source, no kernel/state store). A future Profile B
// (Schematic system/deployment) would supply a different profile pointing the
// same machinery at a real service stack; it is intentionally NOT implemented
// here.
package profile

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Profile is the deployment profile: which nodes to deploy and which invariants
// apply. Loadable from YAML so profiles live as data, not code.
type Profile struct {
	// Name identifies the profile (e.g. "profile-a-model").
	Name string `yaml:"name"`

	// Kind is "model" (Profile A) or "system" (Profile B, unimplemented).
	Kind string `yaml:"kind"`

	// Languages is the synthetic node mesh: one node per entry. The call graph
	// forces cross-language pairs for the NxN interop matrix.
	Languages []string `yaml:"languages"`

	// Namespace is the auth namespace the entitled callers use.
	Namespace string `yaml:"namespace"`

	// ReplayDepth bounds each node's WATCH replay ring.
	ReplayDepth int `yaml:"replay_depth"`

	// Soak controls the leak-soak: number of kill/restart cycles and writes per
	// cycle. The leak oracle asserts resource counters stay flat across cycles.
	Soak SoakConfig `yaml:"soak"`

	// Invariants toggles which oracles run for this profile.
	Invariants Invariants `yaml:"invariants"`

	// Seed makes the whole run deterministic; a failed run replays from it.
	Seed int64 `yaml:"seed"`

	// InjectColl1Bug re-introduces the COLL-1 dropped-delta defect (Phase-1
	// acceptance lever). Off in normal runs; the acceptance test flips it on and
	// asserts the WATCH oracle goes RED.
	InjectColl1Bug bool `yaml:"inject_coll1_bug"`
}

// SoakConfig parameterizes the leak-soak loop.
type SoakConfig struct {
	Cycles         int `yaml:"cycles"`
	WritesPerCycle int `yaml:"writes_per_cycle"`
}

// Invariants selects the deterministic oracles to evaluate.
type Invariants struct {
	WatchGapless bool `yaml:"watch_gapless"`
	AuthClosed   bool `yaml:"auth_closed"`
	NoLeak       bool `yaml:"no_leak"`
	UnaryInterop bool `yaml:"unary_interop"`
}

// Validate checks the profile for the harness's hard requirements.
func (p *Profile) Validate() error {
	if p.Kind != "model" {
		return fmt.Errorf("profile %q: only kind=model (Profile A) is implemented, got %q", p.Name, p.Kind)
	}
	if len(p.Languages) < 2 {
		return fmt.Errorf("profile %q: need >= 2 languages for an interop matrix, got %d", p.Name, len(p.Languages))
	}
	return nil
}

// DefaultProfileA returns the baked-in Profile A defaults (the recommended
// defaults from archive/MODEL_STRESS_TEST_PLAN.md §10: synthetic rivets, kind in
// for Phase 1, all model invariants on).
func DefaultProfileA() *Profile {
	return &Profile{
		Name:        "profile-a-model",
		Kind:        "model",
		Languages:   []string{"go", "rust"},
		Namespace:   "tacplus.chaos",
		ReplayDepth: 256,
		Soak:        SoakConfig{Cycles: 8, WritesPerCycle: 16},
		Invariants: Invariants{
			WatchGapless: true,
			AuthClosed:   true,
			NoLeak:       true,
			UnaryInterop: true,
		},
		Seed:           1,
		InjectColl1Bug: false,
	}
}

// Load reads a profile from a YAML file.
func Load(path string) (*Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read profile %s: %w", path, err)
	}
	var p Profile
	if err := yaml.Unmarshal(data, &p); err != nil {
		return nil, fmt.Errorf("parse profile %s: %w", path, err)
	}
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return &p, nil
}
