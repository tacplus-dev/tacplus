// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// Go randomizes map iteration order per range, so each of these tests runs the
// real resolver several times on the same fixture: an unsorted walk flips the
// answer with overwhelming probability across this many attempts, while a
// sorted one cannot flip at all.
const mapOrderAttempts = 12

// Two files in ONE package, each declaring a state root with a DIFFERENT
// auto_tls key type. Nothing in the schema forbids this (spec.proto puts
// auto_tls on StateAnnotation, which is per state root, and nothing requires a
// package to keep its roots in one file), and (*Config).AutoTlsKeyType returns
// the first entry in config.States carrying a key type — so under raw map order
// the service's serving key algorithm was a coin flip between builds, and the
// generated service_manager took the "ecdsa" branch on some builds only.
func TestResolveStatesAutoTlsStableAcrossFilesInOnePackage(t *testing.T) {
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	writeProto(t, dir, "aaa_first.proto", `syntax = "proto3";
package dual;

import "sys/spec.proto";

message AlphaState {
  option (spec.service_state) = {name: "AlphaState", label: ["state-store"], auto_tls: TLS_KEY_TYPE_ED25519};
  string id = 1;
}
`)
	writeProto(t, dir, "zzz_second.proto", `syntax = "proto3";
package dual;

import "sys/spec.proto";

message OmegaState {
  option (spec.service_state) = {name: "OmegaState", label: ["state-store"], auto_tls: TLS_KEY_TYPE_ECDSA};
  string id = 1;
}
`)

	for i := 0; i < mapOrderAttempts; i++ {
		cfg := &tactype.Config{SelfPackage: "dual"}
		if err := ResolveStatesFromProto(cfg, dir); err != nil {
			t.Fatalf("ResolveStatesFromProto: %v", err)
		}
		// aaa_first.proto sorts first, so its declaration is the one that wins.
		if got := cfg.AutoTlsKeyType(); got != "ed25519" {
			t.Fatalf("attempt %d: AutoTlsKeyType() = %q, want ed25519 (states: %+v)", i, got, cfg.States)
		}
		var order []string
		for _, s := range cfg.States {
			order = append(order, s.ProtoFile+":"+s.MessageName)
		}
		want := "aaa_first.proto:AlphaState,zzz_second.proto:OmegaState"
		if got := strings.Join(order, ","); got != want {
			t.Fatalf("attempt %d: config.States order = %q, want %q", i, got, want)
		}
	}
}

// The backing store gets RW entries for every state partition in the build.
// Those foreign entries carry their owner's auto_tls key type, and they were
// appended in raw map order, so the store's own slice — and every generated
// artifact emitted in States order — differed between identical builds.
func TestResolveStatesStoreTailOrderIsStable(t *testing.T) {
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	for _, pkg := range []string{"pa", "pb", "pc", "pd", "pe"} {
		writeProto(t, dir, pkg+".proto", `syntax = "proto3";
package `+pkg+`;

import "sys/spec.proto";

message `+strings.ToUpper(pkg)+`State {
  option (spec.service_state) = {name: "`+strings.ToUpper(pkg)+`State", label: ["state-store"], auto_tls: TLS_KEY_TYPE_ECDSA, export: true};
  string id = 1;
}
`)
	}

	var first string
	for i := 0; i < mapOrderAttempts; i++ {
		// AllStates makes this a store, so every foreign state is resolved RW.
		cfg := &tactype.Config{SelfPackage: "store", AllStates: true}
		if err := ResolveStatesFromProto(cfg, dir); err != nil {
			t.Fatalf("ResolveStatesFromProto: %v", err)
		}
		var order []string
		for _, s := range cfg.States {
			order = append(order, s.ProtoFile)
		}
		got := strings.Join(order, ",")
		if i == 0 {
			first = got
			continue
		}
		if got != first {
			t.Fatalf("attempt %d: config.States order %q != first run %q", i, got, first)
		}
	}
}

// AppendStateServiceToProtos emits each package's shared messages (MapEmpty and
// friends) into whichever of that package's files it processes FIRST. That walk
// was a raw map range, so two identical builds put MapEmpty in different proto
// files — and therefore in different generated type modules.
func TestAppendStateServicesSharedMessageLandsInStableFile(t *testing.T) {
	for i := 0; i < mapOrderAttempts; i++ {
		dir := t.TempDir()
		if err := CopySysProtos(dir); err != nil {
			t.Fatalf("CopySysProtos: %v", err)
		}
		if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
			t.Fatalf("writeMinimalGeneratedStubs: %v", err)
		}
		writeProto(t, dir, "aaa_first.proto", `syntax = "proto3";
package shared;

import "sys/spec.proto";

message AlphaState {
  option (spec.service_state) = {name: "AlphaState", label: ["state-store"]};
  string id = 1;
}
`)
		writeProto(t, dir, "zzz_second.proto", `syntax = "proto3";
package shared;

import "sys/spec.proto";

message OmegaState {
  option (spec.service_state) = {name: "OmegaState", label: ["state-store"]};
  string id = 1;
}
`)

		if err := AppendStateServiceToProtos(dir, dir); err != nil {
			t.Fatalf("attempt %d: AppendStateServiceToProtos: %v", i, err)
		}
		// aaa_first.proto sorts first, so the package's single MapEmpty is its.
		if !strings.Contains(readFileString(t, dir, "aaa_first.proto"), "message MapEmpty") {
			t.Fatalf("attempt %d: MapEmpty missing from aaa_first.proto", i)
		}
		if strings.Contains(readFileString(t, dir, "zzz_second.proto"), "message MapEmpty") {
			t.Fatalf("attempt %d: MapEmpty emitted into zzz_second.proto instead of the package's first file", i)
		}
	}
}

func readFileString(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// DeriveVirtualServiceClients picks "the" file carrying this package's service
// identity, then reads file-scoped fields (Imports, OpaImportTargets,
// RecordReads) off it. parseServiceIdentity is per-file and defaults the service
// name to the package, so a package may legally carry option (spec.service) in
// more than one file — and the raw-map "first match" then derived a different
// client set on each build.
func TestDeriveVirtualServiceClientsPicksStableSelfFile(t *testing.T) {
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	// The OPA server whose eval input the consumer imports.
	writeProto(t, dir, "opasrv.proto", `syntax = "proto3";
package opasrv;

import "sys/spec.proto";

option (spec.service) = {name: "opasrv"};

message EvalInput {
  option (spec.opa_eval) = {entry_point: "data.x" result: "EvalResult"};
  string email = 1;
}

message EvalResult {
  bool allow = 1;
}
`)
	// Two files, one package, both carrying the service identity. Only the
	// second declares the opa_import.
	writeProto(t, dir, "aaa_consumer.proto", `syntax = "proto3";
package consumer;

import "sys/spec.proto";

option (spec.service) = {name: "consumer"};

message Ping {}
`)
	writeProto(t, dir, "zzz_consumer_opa.proto", `syntax = "proto3";
package consumer;

import "sys/spec.proto";

option (spec.service) = {name: "consumer"};
option (spec.opa_import) = {source: "opasrv.EvalInput", service: "opasrv"};

message Pong {}
`)

	for i := 0; i < mapOrderAttempts; i++ {
		entries, err := DeriveVirtualServiceClients(dir, "consumer")
		if err != nil {
			t.Fatalf("attempt %d: DeriveVirtualServiceClients: %v", i, err)
		}
		// aaa_consumer.proto sorts first and declares no opa_import, so the
		// derived client set is empty — the same on every build.
		if len(entries) != 0 {
			t.Fatalf("attempt %d: derived %d entries (%+v), want 0 — the self file must be the lowest-sorting service file, not a map-order pick", i, len(entries), entries)
		}
	}
}
