// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	protogen "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// A sys proto that declares a state root (sys/servicehealth.proto) gets its
// state client like a service's own roots, as in Go (250c3e1). A service whose
// only state is the injected sys ServiceHealth has HasStates set, so its
// ServiceManager imports s2sm.internal.state.StateClients; skipping every sys
// proto here left that package unemitted and the build uncompilable.
func TestJavaStateWrappersEmitSysStateRoot(t *testing.T) {
	inputDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(inputDir, "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(inputDir, "services", "plain.proto"), []byte(`syntax = "proto3";

package plain;

import "sys/spec.proto";

option (spec.service) = {name: "plain"};

message PingRequest { string id = 1; }

service PlainService {
  option (spec.service_s2s) = {};
  rpc Ping(PingRequest) returns (PingRequest) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, auth_services: ["*"]};
  }
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	flatProtoDir := filepath.Join(t.TempDir(), "flat")
	if err := os.CopyFS(flatProtoDir, os.DirFS(inputDir)); err != nil {
		t.Fatal(err)
	}
	if err := protogen.CopySysProtos(flatProtoDir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if _, _, _, err := protogen.ScanAndGenerateConfigHolder(inputDir, flatProtoDir); err != nil {
		t.Fatalf("ScanAndGenerateConfigHolder: %v", err)
	}

	// Java detects state roots through their synthesized state services, so
	// the input carries them as it does in a real generation.
	if err := protogen.AppendStateServiceToProtos(flatProtoDir, flatProtoDir); err != nil {
		t.Fatalf("AppendStateServiceToProtos: %v", err)
	}

	config := &tactype.Config{OutputDir: t.TempDir(), SelfPackage: "plain"}
	if err := protogen.InjectHealthState(config, flatProtoDir); err != nil {
		t.Fatalf("InjectHealthState: %v", err)
	}
	if len(config.States) == 0 {
		t.Fatal("InjectHealthState added no state; the test no longer exercises a sys-only state set")
	}
	servicesInfo, err := detectServicesForWrapper(config, flatProtoDir)
	if err != nil {
		t.Fatalf("detectServicesForWrapper: %v", err)
	}
	if err := generateJavaStateClientWrappers(config, servicesInfo, flatProtoDir); err != nil {
		t.Fatalf("generateJavaStateClientWrappers: %v", err)
	}
	var found []string
	_ = filepath.Walk(config.OutputDir, func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() && strings.Contains(filepath.ToSlash(path), "/internal/state/") {
			found = append(found, filepath.ToSlash(path))
		}
		return nil
	})
	var client, root bool
	for _, f := range found {
		client = client || strings.HasSuffix(f, "/servicehealth/StateClient.java")
		root = root || strings.HasSuffix(f, "/internal/state/StateClients.java")
	}
	if !client || !root {
		t.Errorf("sys ServiceHealth state client not generated (client %v, root StateClients %v); generated under internal/state: %v", client, root, found)
	}
}
