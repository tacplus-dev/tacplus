// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package tacplusyaml

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScopeKeysReadAndChecked(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, Filename), []byte("language: rust\noutput-dir: model\ngrpc-servers: all\n"), 0o644)
	cfg, err := Read(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.WholeFleet() || cfg.GrpcClients != "" {
		t.Fatalf("cfg = %+v", cfg)
	}
	os.WriteFile(filepath.Join(dir, Filename), []byte("language: go\noutput-dir: model\ngrpc-clients: some\n"), 0o644)
	if _, err := Read(dir); err == nil {
		t.Fatal("grpc-clients: some must be refused")
	}
	if !(&Config{Records: ScopeAll}).WholeFleet() {
		t.Fatal("records: all is the whole fleet")
	}
	if (&Config{}).WholeFleet() || (*Config)(nil).WholeFleet() {
		t.Fatal("unset scope is not the whole fleet")
	}
}
