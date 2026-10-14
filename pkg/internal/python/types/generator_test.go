// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

func TestPythonGrpcToolsInterpreterPrefersTacplusPython(t *testing.T) {
	t.Setenv("TACPLUS_PYTHON", "/custom/python")
	t.Setenv("PYTHON", "/fallback/python")

	if got := pythonGrpcToolsInterpreter(); got != "/custom/python" {
		t.Fatalf("pythonGrpcToolsInterpreter() = %q, want TACPLUS_PYTHON", got)
	}
}

func TestPythonGrpcToolsInterpreterFallsBackToPythonEnv(t *testing.T) {
	t.Setenv("TACPLUS_PYTHON", "")
	t.Setenv("PYTHON", "/fallback/python")

	if got := pythonGrpcToolsInterpreter(); got != "/fallback/python" {
		t.Fatalf("pythonGrpcToolsInterpreter() = %q, want PYTHON", got)
	}
}

func TestPythonGrpcToolsInterpreterDefaultsToPython3(t *testing.T) {
	t.Setenv("TACPLUS_PYTHON", "")
	t.Setenv("PYTHON", "")

	if got := pythonGrpcToolsInterpreter(); got != "python3" {
		t.Fatalf("pythonGrpcToolsInterpreter() = %q, want python3", got)
	}
}

func TestPythonGrpcToolsProtocArgsGenerateMessagesAndGrpcStubs(t *testing.T) {
	got := pythonGrpcToolsProtocArgs("out", "proto", []string{"contract/contract.proto"})
	want := []string{
		"-m", "grpc_tools.protoc",
		"--python_out=out",
		"--grpc_python_out=out",
		"--proto_path=proto",
		"contract/contract.proto",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pythonGrpcToolsProtocArgs() = %#v, want %#v", got, want)
	}
}

func TestPythonProtoPackageSubdirUsesDescriptorPackage(t *testing.T) {
	subdir, protoFilename := pythonProtoPackageSubdir("contract/contract.proto", "contractclient")
	wantSubdir := filepath.Join("types", "contract", "contractclient")
	if subdir != wantSubdir {
		t.Fatalf("pythonProtoPackageSubdir() subdir = %q, want %q", subdir, wantSubdir)
	}
	if protoFilename != "contract" {
		t.Fatalf("pythonProtoPackageSubdir() protoFilename = %q, want contract", protoFilename)
	}
}

func TestPythonProtoPackageSubdirFallsBackToProtoFilename(t *testing.T) {
	subdir, protoFilename := pythonProtoPackageSubdir("contract/contract.proto", "")
	wantSubdir := filepath.Join("types", "contract", "contract")
	if subdir != wantSubdir {
		t.Fatalf("pythonProtoPackageSubdir() subdir = %q, want %q", subdir, wantSubdir)
	}
	if protoFilename != "contract" {
		t.Fatalf("pythonProtoPackageSubdir() protoFilename = %q, want contract", protoFilename)
	}
}

func TestPythonProtoLayoutPackageKey(t *testing.T) {
	got := pythonProtoLayoutPackageKey(pythonProtoLayout{
		ProtoDir:    filepath.Join("svc", "contract"),
		PackageName: "contractclient",
	})
	want := filepath.Join("svc", "contract", "contractclient")
	if got != want {
		t.Fatalf("pythonProtoLayoutPackageKey() = %q, want %q", got, want)
	}

	got = pythonProtoLayoutPackageKey(pythonProtoLayout{PackageName: "top"})
	if got != "top" {
		t.Fatalf("pythonProtoLayoutPackageKey() = %q, want top", got)
	}
}

func TestPythonClientOnlyRejectsExplicitServiceSurfaces(t *testing.T) {
	cfg := &tactype.Config{
		GrpcC2SClients:   []tactype.GrpcEntry{{ProtoFile: "services/demo.proto", ServiceName: "Demo"}},
		GrpcW2SClients:   []tactype.GrpcEntry{{ProtoFile: "services/demo.proto", ServiceName: "Demo"}},
		GrpcS2SClientAll: true,
		GrpcServerAll:    true,
		AllStates:        true,
	}
	err := validatePythonClientOnlyConfig(cfg)
	if err == nil {
		t.Fatal("expected python client-only rejection")
	}
	got := err.Error()
	for _, want := range []string{"python is a client-only target", "only client generation", "S2S", "server generation", "ServiceManager/state", "go, rust, java"} {
		if !strings.Contains(got, want) {
			t.Fatalf("rejection missing %q:\n%s", want, got)
		}
	}
	// C2S/W2S clients are full-client surfaces python emits — they must NOT appear.
	for _, absent := range []string{"C2S client", "W2S client"} {
		if strings.Contains(got, absent) {
			t.Fatalf("client surface %q should be allowed, not rejected:\n%s", absent, got)
		}
	}
}

func TestPythonClientOnlyAllowsClientSurfaces(t *testing.T) {
	cfg := &tactype.Config{
		GrpcC2SClients: []tactype.GrpcEntry{{ProtoFile: "services/demo.proto", ServiceName: "Demo"}},
		GrpcW2SClients: []tactype.GrpcEntry{{ProtoFile: "services/demo.proto", ServiceName: "Demo"}},
		RestClients:    []tactype.GrpcEntry{{ProtoFile: "services/demo.proto", ServiceName: "Demo"}},
	}
	if err := validatePythonClientOnlyConfig(cfg); err != nil {
		t.Fatalf("python client surfaces should be allowed: %v", err)
	}
}

func TestPythonClientOnlyRejectsServiceSideAnnotations(t *testing.T) {
	protoDir := preparePythonClientOnlyProtoDir(t, `syntax = "proto3";
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

	err := validatePythonClientOnlyAnnotations(&tactype.Config{SelfPackage: "daemon"}, protoDir)
	if err == nil {
		t.Fatal("expected python client-only annotation rejection")
	}
	got := err.Error()
	for _, want := range []string{"server generation", "ServiceManager/state", "state import generation", "health generation", "EventBus generation", "SMB service-side", "only client generation"} {
		if !strings.Contains(got, want) {
			t.Fatalf("annotation rejection missing %q:\n%s", want, got)
		}
	}
}

func TestPythonClientOnlyAllowsC2SAnnotations(t *testing.T) {
	protoDir := preparePythonClientOnlyProtoDir(t, `syntax = "proto3";
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

	err := validatePythonClientOnlyAnnotations(&tactype.Config{
		SelfPackage: "daemon",
	}, protoDir)
	if err != nil {
		t.Fatalf("C2S-only python annotations should be allowed: %v", err)
	}
}

func TestPythonClientOnlyAllowsC2DAnnotations(t *testing.T) {
	protoDir := preparePythonClientOnlyProtoDir(t, `syntax = "proto3";
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

	err := validatePythonClientOnlyAnnotations(&tactype.Config{
		SelfPackage: "daemon",
	}, protoDir)
	if err != nil {
		t.Fatalf("C2D-only python annotations should be allowed: %v", err)
	}
}

func TestPythonClientOnlyLibraryTemplateFilterSkipsServiceRuntime(t *testing.T) {
	for _, path := range []string{
		"servicemanager/__init__.py.tmpl",
		"servicemanager/connection.py.tmpl",
		"servicemanager/endpoint.py.tmpl",
		"tags/__init__.py.tmpl",
		"client_im/__init__.py.tmpl",
		"client_im/manager.py.tmpl",
		"client_im/source.py.tmpl",
	} {
		if !pythonClientOnlyLibTemplate(path) {
			t.Fatalf("client primitive template %q should be emitted", path)
		}
	}
	for _, path := range []string{
		"grpcserver/grpc_server.py.tmpl",
		"healthreporter/reporter.py.tmpl",
		"servicemanager/manager.py.tmpl",
		// service_im (cloud/CredPair bootstrap -> authoritative S2S
		// MintServiceToken) is service identity; client-only targets emit
		// client_im, never service_im.
		"service_im/identity_manager.py.tmpl",
		"service_im/cloud_minter.py.tmpl",
	} {
		if pythonClientOnlyLibTemplate(path) {
			t.Fatalf("service runtime template %q should be skipped", path)
		}
	}
}

func TestPythonWriteLibTemplatesEmitsTagsHelpers(t *testing.T) {
	out := t.TempDir()
	protoDir := t.TempDir()
	cfg := &tactype.Config{
		OutputDir:         out,
		PythonPackageBase: "demo_tacplus",
		AnnotatedProtoDir: protoDir,
	}
	if err := writeLibTemplates(cfg, nil, nil, []string{cfg.ServiceLabel()}); err != nil {
		t.Fatalf("writeLibTemplates: %v", err)
	}
	path := filepath.Join(out, "chassis", "tags", "__init__.py")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected tags helper template to be emitted: %v", err)
	}
}

func preparePythonClientOnlyProtoDir(t *testing.T, content string) string {
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

// NEW-33 pin: a client-only target with a non-empty subscription set must FAIL
// LOUDLY — the runtime SM its subscribers need is service-side and client
// builds do not emit it. A silent revert to generating the broken-import
// branch must not survive this test.
func TestPythonClientOnlySubscriptionsFailLoudly(t *testing.T) {
	src, err := os.ReadFile("generator.go")
	if err != nil {
		t.Fatalf("read generator.go: %v", err)
	}
	for _, want := range []string{
		`if len(config.Subscriptions) > 0 {`,
		`return fmt.Errorf("python is a client-only target; event-bus subscriptions require the service-runtime ServiceManager`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("python generator must fail loudly on client-only subscriptions (NEW-33); missing %q", want)
		}
	}
}
