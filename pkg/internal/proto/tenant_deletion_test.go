// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// A product service binding (spec.caller_tid) keeps tenants' data as their
// custodian, and deletes it with the tenant (#475): it reads the fleet's
// tenant deletions (sys/tenantdeletion.proto) read-only, and the state host's
// generated state service authorizes it.

const custodianProto = `syntax = "proto3";

package billing;

import "sys/spec.proto";

option (spec.service) = {name: "billing"%s};

message RecordUsageRequest {
  string tenant = 1%s;
  int64 units = 2;
}
message RecordUsageResponse {}

service BillingService {
  option (spec.service_s2s) = {auth_services: ["*"]};

  rpc RecordUsage(RecordUsageRequest) returns (RecordUsageResponse);
}
`

func tenantDeletionFixture(t *testing.T, product, callerTid string) string {
	t.Helper()
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
	body := []byte(fmt.Sprintf(custodianProto, product, callerTid))
	if err := os.WriteFile(filepath.Join(dir, "services", "billing.proto"), body, 0644); err != nil {
		t.Fatalf("write billing.proto: %v", err)
	}
	return dir
}

func TestInjectTenantDeletionState(t *testing.T) {
	for _, tc := range []struct {
		name, product, callerTid string
		want                     bool
	}{
		{"a product's custodian reads the deletions", `, product: "ledger"`, ` [(spec.caller_tid) = true]`, true},
		{"a product keeping no tenant's rows does not", `, product: "ledger"`, "", false},
		{"a platform service binding caller_tid does not", "", ` [(spec.caller_tid) = true]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := tenantDeletionFixture(t, tc.product, tc.callerTid)
			cfg := &tactype.Config{SelfPackage: "billing"}
			if err := InjectTenantDeletionState(cfg, dir); err != nil {
				t.Fatalf("InjectTenantDeletionState: %v", err)
			}
			var got []tactype.StateEntry
			for _, s := range cfg.States {
				if s.IsTenantDeletionState {
					got = append(got, s)
				}
			}
			if !tc.want {
				if len(cfg.States) != 0 {
					t.Fatalf("states = %+v, want none injected", cfg.States)
				}
				return
			}
			want := []tactype.StateEntry{{
				ProtoFile:             "sys/tenantdeletion.proto",
				MessageName:           TenantDeletionStateName,
				AccessMode:            tactype.AccessRO,
				IsTenantDeletionState: true,
			}}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("injected %+v, want %+v", got, want)
			}
			// Injecting twice adds nothing.
			if err := InjectTenantDeletionState(cfg, dir); err != nil || len(cfg.States) != 1 {
				t.Fatalf("second injection: %v, states %+v", err, cfg.States)
			}
		})
	}
}

// The state host is generated from the fleet's merged tree, which holds the
// product's proto: its TenantDeletions state service authorizes every product
// custodian there, and no other service.
func TestTenantDeletionImporters(t *testing.T) {
	dir := tenantDeletionFixture(t, `, product: "ledger"`, ` [(spec.caller_tid) = true]`)
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	got := stateImportersByRoot(dp.GetAllFiles())["tenantdeletion."+TenantDeletionStateName]
	if !reflect.DeepEqual(got, []string{"billing"}) {
		t.Fatalf("TenantDeletions importers = %v, want [billing]", got)
	}

	plain := tenantDeletionFixture(t, `, product: "ledger"`, "")
	dp, err = parser.NewDescriptorParser(plain)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := stateImportersByRoot(dp.GetAllFiles())["tenantdeletion."+TenantDeletionStateName]; len(got) != 0 {
		t.Fatalf("TenantDeletions importers without a custodian = %v, want none", got)
	}
}
