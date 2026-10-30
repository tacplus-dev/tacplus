// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// The record plane of a TENANT-OWNED record store (#207). Every rpc of a
// tenant-owned service names the request field carrying its target tenant
// (decisions/tenant-routing-2026-10-26.md: "Nothing is ever an exception"), so
// the plane hosted on one is routed like any other surface of it: every
// request of both halves carries target_tenant, every rpc declares it as its
// route field, and the parser counts the plane as tenant-routed, so every
// generated client pools it per tenant and every server checks it.
func recordTenantRouteFixture(t *testing.T, recordExtra string) string {
	t.Helper()
	dir := recordWriteFixture(t)
	writeProto(t, dir, "store.proto", `syntax = "proto3";
package store;

import "sys/spec.proto";

option (spec.service) = {name: "stratum", tenant_owned: true};
option (spec.record_store) = {no_bus_ingest: true};

message Ping {
  string id = 1;
  string target_tenant = 2;
}

service StratumApi {
  option (spec.service_s2s) = {};
  rpc GetPing(Ping) returns (Ping) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE, route_field: "target_tenant"};
  }
}
`)
	if recordExtra != "" {
		writeProto(t, dir, "extra.proto", recordExtra)
	}
	return dir
}

func TestRecordPlaneOnTenantOwnedHostIsTenantRouted(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := recordTenantRouteFixture(t, "")
	if err := AppendRecordMapServiceToProtos(dir, dir, "ident"); err != nil {
		t.Fatalf("AppendRecordMapServiceToProtos: %v", err)
	}
	out, err := os.ReadFile(filepath.Join(dir, "query.proto"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(out)

	// Every request message of the plane carries the route field, the shared
	// QueryHandle included (it is the request of Count/Delete/Watch, and the
	// handle Create answers, so a handle names its tenant).
	for _, msg := range []string{
		"QueryHandle", "OpenXidRowQueryRequest", "SampleXidRowRequest", "PullXidRowRequest", "GetXidRowRowsRequest",
		"OpenSecretRowQueryRequest", "RefineSecretRowRequest",
		"GetXidRowRequest", "PutXidRowRequest", "InsertIfAbsentXidRowRequest", "ReplaceIfUnchangedXidRowRequest",
		"DeleteXidRowRequest", "WatchXidRowChangesRequest", "RecordApplyRequest",
	} {
		re := regexp.MustCompile(`(?s)\nmessage ` + msg + ` \{\n(.*?)\n\}\n`)
		m := re.FindStringSubmatch(text)
		if m == nil || !strings.Contains(m[1], "  string target_tenant = 15 [(spec.assert_max_size) = 8];") {
			t.Errorf("message %s carries no target_tenant route field", msg)
		}
	}
	// The record's own tenant field stays beside the key: it says which tenant
	// the entry belongs to; target_tenant says which instance receives the call.
	if !strings.Contains(text, "message DeleteXidRowRequest {\n  string xid = 1;\n  RecordDurability durability = 2;\n  string tenant = 3;\n") {
		t.Error("DeleteXidRowRequest lost the record's own tenant field")
	}

	// Every rpc of both halves declares it.
	for _, svc := range []string{"RecordMap", "RecordQuery"} {
		block := rpcBlock(t, text, svc)
		rpcs := emittedRPCSignature.FindAllStringSubmatch(block, -1)
		if len(rpcs) == 0 {
			t.Fatalf("%s emitted no rpcs", svc)
		}
		routed := strings.Count(block, `route_field: "target_tenant"`)
		if routed != len(rpcs) {
			t.Errorf("%s: %d rpcs, %d declare route_field target_tenant:\n%s", svc, len(rpcs), routed, block)
		}
	}

	// The parsed surface: every rpc routed by target_tenant, and the plane
	// counted as tenant-routed, so ValidateTenantRouting holds it to that.
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("the injected plane must compile: %v", err)
	}
	fd, err := dp.GetFileDescriptor("query.proto")
	if err != nil {
		t.Fatal(err)
	}
	owned := parser.TenantOwnedServices(dp)
	seen := 0
	for si := range fd.Services {
		svc := &fd.Services[si]
		if svc.Name != "RecordMap" && svc.Name != "RecordQuery" {
			continue
		}
		seen++
		if !parser.IsTenantRouted(fd, svc, owned) {
			t.Errorf("%s on a tenant-owned host must be tenant-routed", svc.Name)
		}
		for _, m := range svc.Methods {
			if m.RouteField == nil || m.RouteField.Field != "target_tenant" || m.RouteField.Number != 15 {
				t.Errorf("%s.%s route field = %+v, want target_tenant (15)", svc.Name, m.Name, m.RouteField)
			}
		}
	}
	if seen != 2 {
		t.Fatalf("want both plane halves parsed, saw %d", seen)
	}
	if err := parser.ValidateTenantRouting(dp); err != nil {
		t.Fatalf("ValidateTenantRouting: %v", err)
	}
}

// A shared (platform) record store's plane is never addressed by tenant: no
// route field anywhere, exactly the plane it had before.
func TestRecordPlaneOnSharedHostIsNotTenantRouted(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := recordWriteFixture(t)
	if err := AppendRecordMapServiceToProtos(dir, dir, "ident"); err != nil {
		t.Fatalf("AppendRecordMapServiceToProtos: %v", err)
	}
	out, err := os.ReadFile(filepath.Join(dir, "query.proto"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "target_tenant") {
		t.Fatal("a shared record store's plane must carry no route field")
	}
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := parser.ValidateTenantRouting(dp); err != nil {
		t.Fatalf("ValidateTenantRouting: %v", err)
	}
}

// A record whose key or tenant field is named like the route field would put
// two meanings on one request field; generation refuses it rather than pick.
func TestRecordPlaneRouteFieldCollisionRefused(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := recordTenantRouteFixture(t, `syntax = "proto3";

package clash;

import "sys/spec.proto";

option (spec.service) = {name: "clash"};

message ClashRow {
  option (spec.record) = {key: "id", s2s: {auth_services: ["*"]}};
  string id = 1;
  string target_tenant = 2 [(spec.store_namespace) = true];
}
`)
	err := AppendRecordMapServiceToProtos(dir, dir, "ident")
	if err == nil || !strings.Contains(err.Error(), "clash.ClashRow") || !strings.Contains(err.Error(), "target_tenant") {
		t.Fatalf("want a collision error naming clash.ClashRow and target_tenant, got %v", err)
	}
}
