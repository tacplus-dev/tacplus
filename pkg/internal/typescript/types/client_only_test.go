// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/packages"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

func TestTypeScriptClientOnlyRejectsExplicitServiceSurfaces(t *testing.T) {
	cfg := &tactype.Config{
		GrpcS2SClientAll: true,
		GrpcServerAll:    true,
		AllStates:        true,
	}
	err := validateTypeScriptClientOnlyConfig(cfg)
	if err == nil {
		t.Fatal("expected typescript client-only rejection")
	}
	got := err.Error()
	for _, want := range []string{"typescript is a client-only target", "only client generation", "S2S", "server generation", "ServiceManager/state", "go, rust, java"} {
		if !strings.Contains(got, want) {
			t.Fatalf("rejection missing %q:\n%s", want, got)
		}
	}
}

func TestTypeScriptClientOnlyAllowsClientSurfaces(t *testing.T) {
	cfg := &tactype.Config{
		GrpcC2SClients: []tactype.GrpcEntry{{ProtoFile: "services/demo.proto", ServiceName: "Demo"}},
		GrpcW2SClients: []tactype.GrpcEntry{{ProtoFile: "services/demo.proto", ServiceName: "Demo"}},
		RestClients:    []tactype.GrpcEntry{{ProtoFile: "services/demo.proto", ServiceName: "Demo"}},
	}
	if err := validateTypeScriptClientOnlyConfig(cfg); err != nil {
		t.Fatalf("typescript client surfaces should be allowed: %v", err)
	}
}

func TestTypeScriptClientOnlyRejectsServiceSideAnnotations(t *testing.T) {
	protoDir := prepareTypeScriptClientOnlyProtoDir(t, `syntax = "proto3";
package daemon;
import "sys/spec.proto";

message PingRequest {
  string value = 1;
}

message PingResponse {
  string value = 1;
}

message DaemonState {
  option (spec.service_state) = {};
  string id = 1;
  PingResponse imported_ping = 2 [(spec.state_import) = "other.OtherState.ping"];
}

message DaemonHealth {
  option (spec.service_health) = {};
  string id = 1;
}

message DaemonEvent {
  option (spec.record) = {key: "event_id", owner: "owner"};
  string event_id = 1;
  string owner = 2;
}

service DaemonBackend {
  option (spec.service_s2s) = {};
  option (spec.smb) = {};
  rpc Ping(PingRequest) returns (PingResponse);
}
`)

	err := validateTypeScriptClientOnlyAnnotations(&tactype.Config{SelfPackage: "daemon"}, protoDir)
	if err == nil {
		t.Fatal("expected typescript client-only annotation rejection")
	}
	got := err.Error()
	for _, want := range []string{"server generation", "ServiceManager/state", "state import generation", "health generation", "EventBus generation", "SMB service-side", "only client generation"} {
		if !strings.Contains(got, want) {
			t.Fatalf("annotation rejection missing %q:\n%s", want, got)
		}
	}
}

func TestTypeScriptClientOnlyAllowsC2SAnnotations(t *testing.T) {
	protoDir := prepareTypeScriptClientOnlyProtoDir(t, `syntax = "proto3";
package daemon;
import "sys/spec.proto";

message PingRequest {
  string value = 1;
}

message PingResponse {
  string value = 1;
}

service DaemonApi {
  option (spec.service_c2s) = {};
  rpc Ping(PingRequest) returns (PingResponse);
}
`)

	err := validateTypeScriptClientOnlyAnnotations(&tactype.Config{
		SelfPackage: "daemon",
	}, protoDir)
	if err != nil {
		t.Fatalf("C2S-only typescript annotations should be allowed: %v", err)
	}
}

func TestTypeScriptClientOnlyAllowsC2DAnnotations(t *testing.T) {
	protoDir := prepareTypeScriptClientOnlyProtoDir(t, `syntax = "proto3";
package daemon;
import "sys/spec.proto";

message PingRequest {
  string value = 1;
}

message PingResponse {
  string value = 1;
}

service DaemonApi {
  option (spec.service_c2d) = {};
  rpc Ping(PingRequest) returns (PingResponse);
}
`)

	err := validateTypeScriptClientOnlyAnnotations(&tactype.Config{
		SelfPackage: "daemon",
	}, protoDir)
	if err != nil {
		t.Fatalf("C2D-only typescript annotations should be allowed: %v", err)
	}
}

func TestTypeScriptClientOnlyLibraryTemplateFilterSkipsServiceRuntime(t *testing.T) {
	for _, path := range []string{
		"servicemanager/EndpointConn.ts.tmpl",
		"servicemanager/ServiceConnection.ts.tmpl",
		"servicemanager/index.ts.tmpl",
		"servicemanager/package.json.tmpl",
		"servicemanager/tsconfig.json.tmpl",
		"tags/index.ts.tmpl",
		"tags/package.json.tmpl",
		"tags/tsconfig.json.tmpl",
	} {
		if !typeScriptClientOnlyLibTemplate(path) {
			t.Fatalf("client primitive template %q should be emitted", path)
		}
	}
	for _, path := range []string{
		"grpcserver/GrpcServer.ts.tmpl",
		"healthreporter/HealthReporter.ts.tmpl",
		"servicemanager/Auth.ts.tmpl",
	} {
		if typeScriptClientOnlyLibTemplate(path) {
			t.Fatalf("service runtime template %q should be skipped", path)
		}
	}
}

func TestTypeScriptWriteLibTemplatesEmitsTagsHelpers(t *testing.T) {
	out := t.TempDir()
	protoDir := t.TempDir()
	depsDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(depsDir, "dependencies.yaml"), []byte(`lang:
  typescript:
    dependencies:
      "@bufbuild/protobuf": "^2.0.0"
`), 0644); err != nil {
		t.Fatalf("write dependencies.yaml: %v", err)
	}
	packages.SetProtoDir(depsDir)
	t.Cleanup(func() { packages.SetProtoDir("") })
	cfg := &tactype.Config{
		OutputDir:         out,
		TsPackageBase:     "demo-tacplus",
		AnnotatedProtoDir: protoDir,
	}
	if err := writeLibTemplates(cfg, nil, nil, []string{cfg.ServiceLabel()}); err != nil {
		t.Fatalf("writeLibTemplates: %v", err)
	}
	for _, rel := range []string{
		filepath.Join("chassis", "jwk", "ImmutableJwk.ts"),
		filepath.Join("chassis", "key", "ImmutableKey.ts"),
		filepath.Join("chassis", "spiffe", "SpiffeId.ts"),
		filepath.Join("chassis", "tags", "index.ts"),
		filepath.Join("chassis", "tags", "package.json"),
		filepath.Join("chassis", "tags", "tsconfig.json"),
		filepath.Join("chassis", "token", "TokenWrapper.ts"),
		filepath.Join("chassis", "token", "index.ts"),
		filepath.Join("chassis", "token", "package.json"),
		// client_im is the TS client's identity manager (service_im is skipped).
		filepath.Join("chassis", "client_im", "ClientIdentityManager.ts"),
		filepath.Join("chassis", "client_im", "TokenSource.ts"),
		filepath.Join("chassis", "client_im", "index.ts"),
		filepath.Join("chassis", "client_im", "package.json"),
		filepath.Join("chassis", "client_im", "tsconfig.json"),
	} {
		if _, err := os.Stat(filepath.Join(out, rel)); err != nil {
			t.Fatalf("expected client lib template %s to be emitted: %v", rel, err)
		}
	}
	// service_im (cloud/CredPair bootstrap -> authoritative S2S MintServiceToken)
	// is service identity; client-only builds emit client_im, never service_im.
	for _, rel := range []string{
		filepath.Join("chassis", "service_im", "IdentityManager.ts"),
		filepath.Join("chassis", "service_im", "CloudMinter.ts"),
	} {
		if _, err := os.Stat(filepath.Join(out, rel)); err == nil {
			t.Fatalf("service_im template %s must NOT be emitted for a client-only target", rel)
		}
	}
	tokenWrapper, err := os.ReadFile(filepath.Join(out, "chassis", "token", "TokenWrapper.ts"))
	if err != nil {
		t.Fatalf("read token wrapper: %v", err)
	}
	for _, want := range []string{
		`export interface TokenSigningKey`,
		`export interface TokenVerificationJwk`,
		`from '../spiffe/index.js'`,
	} {
		if !strings.Contains(string(tokenWrapper), want) {
			t.Fatalf("TokenWrapper missing expected content %q:\n%s", want, string(tokenWrapper))
		}
	}
	for _, forbidden := range []string{
		`from '../key/index.js'`,
		`from '../jwk/index.js'`,
	} {
		if strings.Contains(string(tokenWrapper), forbidden) {
			t.Fatalf("TokenWrapper should use structural token key interfaces, found stale import %q:\n%s", forbidden, string(tokenWrapper))
		}
	}
	spiffe, err := os.ReadFile(filepath.Join(out, "chassis", "spiffe", "SpiffeId.ts"))
	if err != nil {
		t.Fatalf("read spiffe helper: %v", err)
	}
	if strings.Contains(string(spiffe), "from 'crypto'") || strings.Contains(string(spiffe), "createHash") {
		t.Fatalf("client SPIFFE helper must not import Node crypto:\n%s", string(spiffe))
	}
}

func prepareTypeScriptClientOnlyProtoDir(t *testing.T, content string) string {
	t.Helper()
	inputDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(inputDir, "services"), 0755); err != nil {
		t.Fatalf("mkdir services: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inputDir, "services", "daemon.proto"), []byte(content), 0644); err != nil {
		t.Fatalf("write daemon.proto: %v", err)
	}
	outDir := t.TempDir()
	protoDir, err := proto.CopyAndPrepareProtoDir(inputDir, outDir)
	if err != nil {
		t.Fatalf("prepare proto dir: %v", err)
	}
	return protoDir
}
