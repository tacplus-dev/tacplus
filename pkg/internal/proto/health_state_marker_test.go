// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// Regression for OF-4: a user state coincidentally named "ServiceHealth" must
// behave like any other user state. InjectHealthState must NOT inject (and must
// NOT mark) when such a state already exists, so emitters — which now key health
// behavior off StateEntry.IsHealthState / config.HealthStateName rather than the
// "ServiceHealth" literal — leave it as an ordinary state.
func TestInjectHealthState_UserNamedServiceHealthNotMarked(t *testing.T) {
	cfg := &tactype.Config{
		States: []tactype.StateEntry{
			{ProtoFile: "services/app.proto", MessageName: "ServiceHealth", AccessMode: tactype.AccessRW},
		},
	}
	if err := InjectHealthState(cfg, t.TempDir()); err != nil {
		t.Fatalf("InjectHealthState: %v", err)
	}
	if len(cfg.States) != 1 {
		t.Fatalf("expected no injection (user state present), got %d states", len(cfg.States))
	}
	if cfg.States[0].IsHealthState {
		t.Error("user-declared ServiceHealth state must NOT be marked IsHealthState")
	}
	if cfg.HealthStateName != "" {
		t.Errorf("HealthStateName = %q, want empty (nothing injected)", cfg.HealthStateName)
	}
}

func TestInjectHealthState_AutoDiscoveredGrpcServer(t *testing.T) {
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "services"), 0755); err != nil {
		t.Fatalf("mkdir services: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "platform"), 0755); err != nil {
		t.Fatalf("mkdir platform: %v", err)
	}
	serviceProto := `syntax = "proto3";

package appclient;

import "sys/spec.proto";

option (spec.service) = {name: "app"};

message PingRequest {}
message PingResponse {}

service AppService {
  option (spec.service_s2s) = {auth_services: ["*"]};

  rpc Ping(PingRequest) returns (PingResponse);
}
`
	if err := os.WriteFile(filepath.Join(dir, "services", "app.proto"), []byte(serviceProto), 0644); err != nil {
		t.Fatalf("write service proto: %v", err)
	}
	// ServiceHealth is supplied by the sys contract (sys/servicehealth.proto).
	cfg := &tactype.Config{SelfPackage: "app"}
	if err := InjectHealthState(cfg, dir); err != nil {
		t.Fatalf("InjectHealthState: %v", err)
	}
	if len(cfg.States) != 1 {
		t.Fatalf("expected health state injection, got %d states", len(cfg.States))
	}
	state := cfg.States[0]
	if !state.IsHealthState || state.MessageName != "ServiceHealth" || state.AccessMode != tactype.AccessRW {
		t.Fatalf("unexpected health state entry: %+v", state)
	}
	if cfg.HealthStateName != "ServiceHealth" {
		t.Fatalf("HealthStateName = %q, want ServiceHealth", cfg.HealthStateName)
	}
}

func TestInjectHealthState_MarksDeclaredSharedServiceHealthRoot(t *testing.T) {
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "services"), 0755); err != nil {
		t.Fatalf("mkdir services: %v", err)
	}
	healthProto := `syntax = "proto3";

package kernel;

import "sys/healthreport.proto";
import "sys/spec.proto";

message ServiceHealth {
  option (spec.service_state) = {name: "ServiceHealth", label: ["kernel"], export: true};

  repeated healthreport.ServiceInstanceHealth instances = 1 [
    (spec.instance_keyed) = true,
    (spec.assert_max_size) = 256
  ];
}
`
	if err := os.WriteFile(filepath.Join(dir, "services", "kernel.proto"), []byte(healthProto), 0644); err != nil {
		t.Fatalf("write kernel proto: %v", err)
	}

	cfg := &tactype.Config{
		States: []tactype.StateEntry{{
			ProtoFile:   "services/kernel.proto",
			MessageName: "ServiceHealth",
			AccessMode:  tactype.AccessRW,
		}},
	}
	if err := InjectHealthState(cfg, dir); err != nil {
		t.Fatalf("InjectHealthState: %v", err)
	}
	if len(cfg.States) != 1 {
		t.Fatalf("expected existing health root to be marked without duplicate injection, got %d states", len(cfg.States))
	}
	if !cfg.States[0].IsHealthState {
		t.Fatalf("declared shared ServiceHealth root was not marked: %+v", cfg.States[0])
	}
	if cfg.HealthStateName != "ServiceHealth" {
		t.Fatalf("HealthStateName = %q, want ServiceHealth", cfg.HealthStateName)
	}
}

// MS-6: the platform ServiceHealth root is recognized by SHAPE, not by an
// explicit marker — the annotation name plus a repeated instance_keyed
// healthreport.ServiceInstanceHealth `instances` field. Pin the exactness of
// that shape: a message carrying the ServiceHealth annotation but with the
// `instances` field NOT instance_keyed is a near-miss and must NOT be marked as
// the shared health root (so isDeclaredServiceHealthRoot cannot loosen into
// matching ordinary states).
func TestInjectHealthState_ShapeNearMissNotRecognized(t *testing.T) {
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "services"), 0755); err != nil {
		t.Fatalf("mkdir services: %v", err)
	}
	// Same annotation + field type as the real root, but the instances field is
	// NOT instance_keyed — one requirement of the shape is absent.
	nearMiss := `syntax = "proto3";

package notkernel;

import "sys/healthreport.proto";
import "sys/spec.proto";

message ServiceHealth {
  option (spec.service_state) = {name: "ServiceHealth", label: ["notkernel"], export: true};

  repeated healthreport.ServiceInstanceHealth instances = 1 [
    (spec.assert_max_size) = 256
  ];
}
`
	if err := os.WriteFile(filepath.Join(dir, "services", "notkernel.proto"), []byte(nearMiss), 0644); err != nil {
		t.Fatalf("write near-miss proto: %v", err)
	}

	cfg := &tactype.Config{
		States: []tactype.StateEntry{{
			ProtoFile:   "services/notkernel.proto",
			MessageName: "ServiceHealth",
			AccessMode:  tactype.AccessRW,
		}},
	}
	if err := InjectHealthState(cfg, dir); err != nil {
		t.Fatalf("InjectHealthState: %v", err)
	}
	if len(cfg.States) != 1 {
		t.Fatalf("near-miss must not trigger injection, got %d states", len(cfg.States))
	}
	if cfg.States[0].IsHealthState {
		t.Fatal("a ServiceHealth-annotated message whose instances field is not instance_keyed must NOT be recognized as the shared health root")
	}
	if cfg.HealthStateName != "" {
		t.Errorf("HealthStateName = %q, want empty (near-miss not recognized)", cfg.HealthStateName)
	}
}

// A service that serves only a surface it hosts by label (the redo service,
// whose one served surface is the sys contract redolog.Redo) is a gRPC server
// like any other and reports its instance health: it gets ServiceHealth.
func TestInjectHealthState_HostOfALabelHostedSurfaceReportsHealth(t *testing.T) {
	dir := t.TempDir()
	if err := CopySysProtos(dir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if err := writeMinimalGeneratedStubs([]string{dir}); err != nil {
		t.Fatalf("writeMinimalGeneratedStubs: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "services"), 0755); err != nil {
		t.Fatalf("mkdir services: %v", err)
	}
	host := `syntax = "proto3";

package redo;

import "sys/spec.proto";

option (spec.service) = {name: "redo", tenant_owned: true, pinned_tenant: "restore"};
`
	if err := os.WriteFile(filepath.Join(dir, "services", "redo.proto"), []byte(host), 0644); err != nil {
		t.Fatalf("write redo proto: %v", err)
	}
	cfg := &tactype.Config{SelfPackage: "redo"}
	if err := InjectHealthState(cfg, dir); err != nil {
		t.Fatalf("InjectHealthState: %v", err)
	}
	if len(cfg.States) != 1 || !cfg.States[0].IsHealthState {
		t.Fatalf("the host of redolog.Redo got no ServiceHealth state: %+v", cfg.States)
	}
}
