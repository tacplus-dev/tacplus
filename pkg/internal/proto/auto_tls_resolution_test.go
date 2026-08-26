// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

const autoTlsStateProto = `syntax = "proto3";
package %PKG%;

import "sys/spec.proto";

message FooState {
  option (spec.service_state) = {name: "FooState", label: ["state-store"], auto_tls: %TLS%, export: true};
  string id = 1;
}
`

func writeAutoTlsStateProto(t *testing.T, dir, pkg, tlsKeyType string) {
	t.Helper()
	content := strings.ReplaceAll(strings.ReplaceAll(autoTlsStateProto, "%PKG%", pkg), "%TLS%", tlsKeyType)
	writeProto(t, dir, pkg+".proto", content)
}

// Regression for OF-19: auto_tls key type must be resolved PACKAGE-QUALIFIED,
// not by bare message name. Two packages each declaring a "FooState" with a
// different auto_tls key type must each keep their own — the deleted bare-name
// ResolveAutoTls/AutoTlsKeyTypeForMessage helpers would let one package's value
// override the other (map-iteration order).
func TestResolveStatesAutoTlsIsPackageQualified(t *testing.T) {
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	writeAutoTlsStateProto(t, dir, "alpha", "TLS_KEY_TYPE_ED25519")
	writeAutoTlsStateProto(t, dir, "beta", "TLS_KEY_TYPE_ECDSA")

	// alpha owns its state; AllStates makes it a store so beta's state is also
	// resolved (RW) — both end up in config.States, both named FooState.
	cfg := &tactype.Config{SelfPackage: "alpha", AllStates: true}
	if err := ResolveStatesFromProto(cfg, dir); err != nil {
		t.Fatalf("ResolveStatesFromProto: %v", err)
	}

	byProto := map[string]string{}
	for _, s := range cfg.States {
		if s.MessageName == "FooState" {
			byProto[s.ProtoFile] = s.AutoTlsKeyType
		}
	}
	if got := byProto["alpha.proto"]; got != "ed25519" {
		t.Errorf("alpha FooState auto_tls = %q, want ed25519 (got states: %+v)", got, cfg.States)
	}
	if got := byProto["beta.proto"]; got != "ecdsa" {
		t.Errorf("beta FooState auto_tls = %q, want ecdsa (got states: %+v)", got, cfg.States)
	}
}

// Regression for OF-19: a descriptor parse failure during state resolution must
// be fatal, not silently swallowed (which would generate with no states).
func TestResolveStatesFailsLoudOnParseError(t *testing.T) {
	cfg := &tactype.Config{SelfPackage: "x"}
	// Empty dir → NewDescriptorParser finds no protos → error must propagate.
	if err := ResolveStatesFromProto(cfg, t.TempDir()); err == nil {
		t.Fatal("expected fatal error on parse failure, got nil")
	}
}
