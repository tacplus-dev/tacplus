package profile

import (
	"path/filepath"
	"testing"
)

func TestLoadProfileA(t *testing.T) {
	p, err := Load(filepath.Join("..", "testdata", "profile-a.yaml"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if p.Kind != "model" {
		t.Fatalf("kind = %q, want model", p.Kind)
	}
	if len(p.Languages) != 2 {
		t.Fatalf("languages = %v, want 2", p.Languages)
	}
	if !p.Invariants.WatchGapless {
		t.Fatal("watch_gapless invariant should be enabled")
	}
}

func TestValidateRejectsProfileB(t *testing.T) {
	p := DefaultProfileA()
	p.Kind = "system"
	if err := p.Validate(); err == nil {
		t.Fatal("kind=system (Profile B) must be rejected: not implemented")
	}
}

func TestValidateRejectsTooFewLanguages(t *testing.T) {
	p := DefaultProfileA()
	p.Languages = []string{"go"}
	if err := p.Validate(); err == nil {
		t.Fatal("a single-language profile cannot form an interop matrix")
	}
}
