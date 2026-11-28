// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"
)

// layerProto is a one-service proto: package pkg, its (spec.service) body
// option, its file-level extra options (clients, imports) and the service's
// service_s2s body.
func layerProto(pkg, serviceOption, fileOptions, s2s string) string {
	return `syntax = "proto3";

package ` + pkg + `;

import "sys/spec.proto";

option (spec.service) = {name: "` + pkg + `"` + serviceOption + `};
` + fileOptions + `

message ` + strings.ToUpper(pkg[:1]) + pkg[1:] + `Request {}
message ` + strings.ToUpper(pkg[:1]) + pkg[1:] + `Response {}

service ` + strings.ToUpper(pkg[:1]) + pkg[1:] + ` {
  option (spec.service_s2s) = {` + s2s + `};

  rpc Ping(` + strings.ToUpper(pkg[:1]) + pkg[1:] + `Request) returns (` + strings.ToUpper(pkg[:1]) + pkg[1:] + `Response);
}
`
}

// The fleet of the edge table: core (/system), billing and meter (product
// ledger), audit (product other), crew (tenant-owned).
func layerFleet(core, billing, meter, audit, crew string) []string {
	return []string{
		layerProto("core", "", core, `auth_services: ["*"]`),
		layerProto("billing", `, product: "ledger"`, billing, `auth_services: ["*"]`),
		layerProto("meter", `, product: "ledger"`, meter, `auth_services: ["*"]`),
		layerProto("audit", `, product: "other"`, audit, `auth_services: ["*"]`),
		layerProto("crew", `, tenant_owned: true`, crew, `auth_services: ["*"]`),
	}
}

func validateLayerFleet(t *testing.T, protos []string) error {
	t.Helper()
	dp := writeSurfaceProtoDir(t, protos[0], protos[1:]...)
	return ValidateProductLayer(dp)
}

func wantLayerRefusal(t *testing.T, err error, wants ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("ValidateProductLayer accepted it, want a refusal naming %q", wants)
	}
	for _, w := range wants {
		if !strings.Contains(err.Error(), w) {
			t.Fatalf("error %q does not name %q", err.Error(), w)
		}
	}
}

func TestValidateProductLayer_ParsesProduct(t *testing.T) {
	dp := writeSurfaceProtoDir(t, layerProto("billing", `, product: "ledger"`, "", `auth_services: ["*"]`))
	if err := ValidateProductLayer(dp); err != nil {
		t.Fatalf("ValidateProductLayer refused a plain product service: %v", err)
	}
	for _, fd := range dp.GetAllFiles() {
		if fd.Package == "billing" && fd.ServiceProduct != "ledger" {
			t.Fatalf("ServiceProduct = %q, want ledger", fd.ServiceProduct)
		}
	}
}

// The name rule: the operator's namespace is tac-<deployment>-<name>, the
// shape of a tenant's tac-<deployment>-<tid>, so a tid-shaped name collides.
func TestValidateProductLayer_RefusesBadNames(t *testing.T) {
	for _, tc := range []struct{ name, why string }{
		{"Ledger", "[a-z]"},
		{"l", "[a-z]"},
		{"ledger-", "[a-z]"},
		{"9ledger", "[a-z]"},
		{"led_ger", "[a-z]"},
		{strings.Repeat("a", 33), "[a-z]"},
		{"abcd1234", "tid"},
		{"ledgerab", "tid"},
		{"system", "reserved"},
		{"tenants", "reserved"},
		{"products", "reserved"},
		{"tacplus", "reserved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateLayerFleet(t, []string{layerProto("billing", `, product: "`+tc.name+`"`, "", `auth_services: ["*"]`)})
			wantLayerRefusal(t, err, "billing", tc.name, tc.why)
		})
	}
	for _, ok := range []string{"ledger", "ab", "led-ger2", "abcd-123", "abcdefghi", strings.Repeat("a", 32)} {
		if err := validateLayerFleet(t, []string{layerProto("billing", `, product: "`+ok+`"`, "", `auth_services: ["*"]`)}); err != nil {
			t.Fatalf("product %q refused: %v", ok, err)
		}
	}
}

func TestValidateProductLayer_RefusesProductWithTenantOwned(t *testing.T) {
	err := validateLayerFleet(t, []string{layerProto("billing", `, product: "ledger", tenant_owned: true`, "", `auth_services: ["*"]`)})
	wantLayerRefusal(t, err, "billing", "product", "tenant_owned")
}

// A tacplus release ships no product code.
func TestValidateProductLayer_RefusesProductOnASysContract(t *testing.T) {
	dp := writeSurfaceProtoDir(t, layerProto("core", "", "", `auth_services: ["*"]`))
	writeTempProtoFile(t, dp.protoDir, "sys/ledgerbill.proto", layerProto("ledgerbill", `, product: "ledger"`, "", `auth_services: ["*"]`))
	dp, err := NewDescriptorParser(dp.protoDir)
	if err != nil {
		t.Fatal(err)
	}
	wantLayerRefusal(t, ValidateProductLayer(dp), "sys/ledgerbill.proto", "sys contract", "product")
}

// Every cell of the edge table, declared as a (spec.grpc_s2s_client).
func TestValidateProductLayer_S2SClientEdges(t *testing.T) {
	client := func(target string) string {
		return `option (spec.grpc_s2s_client) = {service: "` + target + `"};`
	}
	for _, tc := range []struct {
		name                              string
		core, billing, meter, audit, crew string
		refusal                           []string
	}{
		{name: "system to system", core: client("core.Core")},
		{name: "system to product refused", core: client("billing.Billing"), refusal: []string{`"core"`, `"billing"`, "never calls a product"}},
		{name: "system to tenant", core: client("crew.Crew")},
		{name: "product to system", billing: client("core.Core")},
		{name: "product to own product", billing: client("meter.Meter")},
		{name: "product to other product refused", billing: client("audit.Audit"), refusal: []string{`"billing"`, `"audit"`, "another product"}},
		{name: "product to tenant refused", billing: client("crew.Crew"), refusal: []string{`product "ledger"`, `"crew"`, "never calls a tenant", "products-layer-2026-11-08"}},
		{name: "tenant to system", crew: client("core.Core")},
		{name: "tenant to product", crew: client("billing.Billing")},
		{name: "tenant to other product", crew: client("audit.Audit")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateLayerFleet(t, layerFleet(tc.core, tc.billing, tc.meter, tc.audit, tc.crew))
			if tc.refusal == nil {
				if err != nil {
					t.Fatalf("refused an allowed edge: %v", err)
				}
				return
			}
			wantLayerRefusal(t, err, tc.refusal...)
		})
	}
}

// A caller list is the same edge seen from the target.
func TestValidateProductLayer_AuthServicesEdges(t *testing.T) {
	for _, tc := range []struct {
		name, target, option, callers string
		refusal                       []string
	}{
		{"system admits product", "core", "", `"billing"`, nil},
		{"product admits tenant", "billing", `, product: "ledger"`, `"crew"`, nil},
		{"product admits own product", "billing", `, product: "ledger"`, `"meter"`, nil},
		{"product admits system refused", "billing", `, product: "ledger"`, `"core"`, []string{`"core"`, `"billing"`, "never calls a product"}},
		{"product admits other product refused", "billing", `, product: "ledger"`, `"audit"`, []string{`"audit"`, `"billing"`, "another product"}},
		{"tenant admits product refused", "crew", `, tenant_owned: true`, `"billing"`, []string{`"billing"`, `"crew"`, "never calls a tenant"}},
		{"patterns are not resolved", "billing", `, product: "ledger"`, `"co.*"`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			protos := []string{layerProto(tc.target, tc.option, "", `auth_services: [`+tc.callers+`]`)}
			for _, other := range []struct{ pkg, option string }{
				{"core", ""}, {"billing", `, product: "ledger"`}, {"meter", `, product: "ledger"`},
				{"audit", `, product: "other"`}, {"crew", `, tenant_owned: true`},
			} {
				if other.pkg != tc.target {
					protos = append(protos, layerProto(other.pkg, other.option, "", `auth_services: ["*"]`))
				}
			}
			err := validateLayerFleet(t, protos)
			if tc.refusal == nil {
				if err != nil {
					t.Fatalf("refused an allowed edge: %v", err)
				}
				return
			}
			wantLayerRefusal(t, err, tc.refusal...)
		})
	}
}

// A state import reads the imported service's state: an edge to it.
func TestValidateProductLayer_StateImportEdges(t *testing.T) {
	imp := func(root string) string {
		return `option (spec.state_imports) = {source: "` + root + `"};`
	}
	if err := validateLayerFleet(t, layerFleet("", imp("core.CoreState"), "", "", "")); err != nil {
		t.Fatalf("product importing /system state refused: %v", err)
	}
	wantLayerRefusal(t, validateLayerFleet(t, layerFleet("", imp("crew.CrewState"), "", "", "")), `"billing"`, `"crew"`, "state_import", "never calls a tenant")
	wantLayerRefusal(t, validateLayerFleet(t, layerFleet(imp("billing.BillingState"), "", "", "", "")), `"core"`, `"billing"`, "never calls a product")
}

func callerTidProto(option, request string) string {
	return `syntax = "proto3";

package billing;

import "sys/spec.proto";

option (spec.service) = {name: "billing"` + option + `};

message RecordUsageRequest {
` + request + `
  int64 units = 9;
}
message RecordUsageResponse {}

service Billing {
  option (spec.service_s2s) = {auth_services: ["*"]};

  rpc RecordUsage(RecordUsageRequest) returns (RecordUsageResponse);
}
`
}

// The custodian key: a product service names a tenant only through caller_tid,
// and caller_tid is one plain string.
func TestValidateProductLayer_CallerTid(t *testing.T) {
	for _, tc := range []struct {
		name, option, request string
		refusal               []string
	}{
		{"product keyed by caller_tid", `, product: "ledger"`, `  string tenant = 1 [(spec.caller_tid) = true];`, nil},
		{"any service may bind it", ``, `  string tid = 1 [(spec.caller_tid) = true];`, nil},
		{"product with a plain tenant field refused", `, product: "ledger"`, `  string tenant = 1;`, []string{"RecordUsageRequest.tenant", "caller_tid", "product"}},
		{"product with a plain tenant_id field refused", `, product: "ledger"`, `  string tenant_id = 1;`, []string{"RecordUsageRequest.tenant_id", "caller_tid"}},
		{"product with a plain tid field refused", `, product: "ledger"`, `  string tid = 1;`, []string{"RecordUsageRequest.tid", "caller_tid"}},
		{"platform with a plain tenant field", ``, `  string tenant = 1;`, nil},
		{"non-string refused", ``, `  int64 tenant = 1 [(spec.caller_tid) = true];`, []string{"RecordUsageRequest.tenant", "caller_tid", "string"}},
		{"repeated refused", ``, `  repeated string tenant = 1 [(spec.caller_tid) = true];`, []string{"RecordUsageRequest.tenant", "caller_tid", "repeated"}},
		{"optional refused", ``, `  optional string tenant = 1 [(spec.caller_tid) = true];`, []string{"RecordUsageRequest.tenant", "caller_tid", "optional"}},
		{"required refused", ``, `  string tenant = 1 [(spec.caller_tid) = true, (spec.assert_required) = true];`, []string{"RecordUsageRequest.tenant", "caller_tid", "assert_required"}},
		{"min size refused", ``, `  string tenant = 1 [(spec.caller_tid) = true, (spec.assert_min_size) = 8];`, []string{"RecordUsageRequest.tenant", "caller_tid", "assert_min_size"}},
		{"max size kept", ``, `  string tenant = 1 [(spec.caller_tid) = true, (spec.assert_max_size) = 8];`, nil},
		{"two refused", ``, "  string tenant = 1 [(spec.caller_tid) = true];\n  string tid = 2 [(spec.caller_tid) = true];", []string{"RecordUsageRequest", "caller_tid", "more than one"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateLayerFleet(t, []string{callerTidProto(tc.option, tc.request)})
			if tc.refusal == nil {
				if err != nil {
					t.Fatalf("refused: %v", err)
				}
				return
			}
			wantLayerRefusal(t, err, tc.refusal...)
		})
	}
}

// caller_tid on a message no rpc takes as its request binds nothing.
func TestValidateProductLayer_CallerTidOnlyOnARequest(t *testing.T) {
	proto := callerTidProto("", `  string tenant = 1;`) + `
message Row {
  string tenant = 1 [(spec.caller_tid) = true];
}
`
	wantLayerRefusal(t, validateLayerFleet(t, []string{proto}), "Row.tenant", "caller_tid", "request")
}

func TestS2SEdgeRefusal(t *testing.T) {
	sys, ledger, other, tenant := ServiceLayer{}, ServiceLayer{Product: "ledger"}, ServiceLayer{Product: "other"}, ServiceLayer{Tenant: true}
	for _, tc := range []struct {
		caller, target ServiceLayer
		refused        bool
	}{
		{sys, sys, false}, {sys, ledger, true}, {sys, tenant, false},
		{ledger, sys, false}, {ledger, ledger, false}, {ledger, other, true}, {ledger, tenant, true},
		{tenant, sys, false}, {tenant, ledger, false}, {tenant, tenant, false},
	} {
		if got := S2SEdgeRefusal(tc.caller, tc.target) != ""; got != tc.refused {
			t.Errorf("S2SEdgeRefusal(%v, %v) refused = %v, want %v", tc.caller, tc.target, got, tc.refused)
		}
	}
}

// A server of a product service generates where its doors are generated (Go,
// Java, Rust), and nowhere else; a product's custodian rows ((spec.caller_tid)
// on a product service) generate where OnTenantDeleted is generated (Go, Java,
// Rust), and wait for it in every other language.
func TestValidateProductLayerHosting(t *testing.T) {
	dp := writeSurfaceProtoDir(t, layerProto("billing", `, product: "ledger"`, "", `auth_services: ["*"]`),
		layerProto("crew", `, tenant_owned: true`, "", `auth_services: ["*"]`))
	for _, lang := range []string{"go", "java", "rust", "python", "typescript"} {
		if err := ValidateProductLayerHosting(dp, "crew", lang); err != nil {
			t.Fatalf("%s: a tenant service of a fleet with a product refused: %v", lang, err)
		}
	}
	for _, lang := range []string{"go", "java", "rust"} {
		if err := ValidateProductLayerHosting(dp, "billing", lang); err != nil {
			t.Fatalf("%s: a product server refused, though its doors are generated: %v", lang, err)
		}
	}
	for _, lang := range []string{"python", "typescript"} {
		wantLayerRefusal(t, ValidateProductLayerHosting(dp, "billing", lang), "billing", "product", "#475", lang)
	}
	for _, lang := range []string{"swift", "csharp"} {
		if err := ValidateProductLayerHosting(dp, "billing", lang); err != nil {
			t.Fatalf("%s: a client of a product service refused: %v", lang, err)
		}
	}

	tid := writeSurfaceProtoDir(t, callerTidProto("", `  string tenant = 1 [(spec.caller_tid) = true];`))
	for _, lang := range []string{"go", "java", "rust"} {
		if err := ValidateProductLayerHosting(tid, "billing", lang); err != nil {
			t.Fatalf("%s: a (spec.caller_tid) server refused, though its binding is generated: %v", lang, err)
		}
	}
	for _, lang := range []string{"python", "typescript"} {
		wantLayerRefusal(t, ValidateProductLayerHosting(tid, "billing", lang), "RecordUsageRequest.tenant", "caller_tid", lang)
	}

	custodian := writeSurfaceProtoDir(t, callerTidProto(`, product: "ledger"`, `  string tenant = 1 [(spec.caller_tid) = true];`))
	for _, lang := range []string{"go", "java", "rust"} {
		if err := ValidateProductLayerHosting(custodian, "billing", lang); err != nil {
			t.Fatalf("%s: a product's custodian server refused, though its OnTenantDeleted is generated: %v", lang, err)
		}
	}
	for _, lang := range []string{"python", "typescript"} {
		wantLayerRefusal(t, ValidateProductLayerHosting(custodian, "billing", lang), "RecordUsageRequest.tenant", "OnTenantDeleted", lang)
	}
}

// A product service binding (spec.caller_tid) is a custodian of tenants' data:
// it, and only it, deletes a tenant's rows on the tenant's deletion.
func TestSelfProductCustodian(t *testing.T) {
	custodian := writeSurfaceProtoDir(t, callerTidProto(`, product: "ledger"`, `  string tenant = 1 [(spec.caller_tid) = true];`))
	if !SelfProductCustodian(custodian, "billing") {
		t.Error("a product service binding caller_tid is not a custodian")
	}
	if SelfProductCustodian(custodian, "crew") || SelfProductCustodian(custodian, "") || SelfProductCustodian(nil, "billing") {
		t.Error("a package that is not the custodian, or none, is one")
	}
	platform := writeSurfaceProtoDir(t, callerTidProto("", `  string tenant = 1 [(spec.caller_tid) = true];`))
	if SelfProductCustodian(platform, "billing") {
		t.Error("a platform service binding caller_tid is a product's custodian")
	}
	plain := writeSurfaceProtoDir(t, layerProto("billing", `, product: "ledger"`, "", `auth_services: ["*"]`))
	if SelfProductCustodian(plain, "billing") {
		t.Error("a product service binding no caller_tid is a custodian")
	}
	if got := ProductCustodians(custodian.GetAllFiles()); len(got) != 1 || got[0] != "billing" {
		t.Errorf("ProductCustodians = %v, want [billing]", got)
	}
	if got := ProductCustodians(plain.GetAllFiles()); len(got) != 0 {
		t.Errorf("ProductCustodians of a product with no caller_tid = %v, want none", got)
	}
}

// The generated authorizer bakes in its own service's product.
func TestSelfServiceProduct(t *testing.T) {
	dp := writeSurfaceProtoDir(t, layerProto("billing", `, product: "ledger"`, "", `auth_services: ["*"]`),
		layerProto("crew", `, tenant_owned: true`, "", `auth_services: ["*"]`))
	for _, tc := range []struct{ pkg, want string }{{"billing", "ledger"}, {"crew", ""}, {"absent", ""}, {"", ""}} {
		if got := SelfServiceProduct(dp, tc.pkg); got != tc.want {
			t.Errorf("SelfServiceProduct(%q) = %q, want %q", tc.pkg, got, tc.want)
		}
	}
	if got := SelfServiceProduct(nil, "billing"); got != "" {
		t.Errorf("SelfServiceProduct(nil) = %q, want empty", got)
	}
}

// A generated S2S client carries its target's layer for the run-time check:
// tenant, product:<name>, or none for /system and a target not loaded.
func TestClientTargetLayer(t *testing.T) {
	dp := writeSurfaceProtoDir(t, layerFleet("", "", "", "", "")[0], layerFleet("", "", "", "", "")[1:]...)
	for _, tc := range []struct{ ref, want string }{
		{"core.Core", ""},
		{"billing.Billing", "product:ledger"},
		{"audit.Audit", "product:other"},
		{"crew.Crew", "tenant"},
		{"absent.Absent", ""},
		{"nodot", ""},
	} {
		if got := ClientTargetLayer(dp, tc.ref); got != tc.want {
			t.Errorf("ClientTargetLayer(%q) = %q, want %q", tc.ref, got, tc.want)
		}
	}
	if got := ClientTargetLayer(nil, "crew.Crew"); got != "" {
		t.Errorf("ClientTargetLayer(nil) = %q, want empty", got)
	}
}

// The record plane's rows are tenants' and the platform's: a product reaches
// them by no edge, neither a (spec.record_read) nor a client of the plane's
// S2S half (which carries the record write surface), even though the plane is
// hosted at /system. Its generated server refuses a product's caller as well.
func TestValidateProductLayer_RecordPlaneEdges(t *testing.T) {
	plane := func(reader *FileDescriptor) *DescriptorParser {
		return &DescriptorParser{descriptors: map[string]*FileDescriptor{
			"store.proto": {Path: "store.proto", Package: "store", ServiceName: "stratum", RecordStore: true},
			"query.proto": {Path: "query.proto", Package: "query", Trait: &TraitDescriptor{
				Name: "query", Host: "stratum", S2SService: "RecordMap", C2SService: "RecordQuery",
			}},
			"reader.proto": reader,
		}}
	}
	reads := []RecordRead{{Record: "owner.Row"}}
	for _, tc := range []struct {
		name    string
		reader  FileDescriptor
		refusal []string
	}{
		{name: "platform record_read", reader: FileDescriptor{ServiceName: "reader", RecordReads: reads}},
		{name: "tenant record_read", reader: FileDescriptor{ServiceName: "reader", ServiceTenantOwned: true, RecordReads: reads}},
		{name: "platform client of the plane", reader: FileDescriptor{ServiceName: "reader", GrpcS2SClients: []string{"query.RecordMap"}}},
		{name: "product record_read refused", reader: FileDescriptor{ServiceName: "reader", ServiceProduct: "ledger", RecordReads: reads},
			refusal: []string{`"reader" (product "ledger")`, `"stratum"`, "record_read owner.Row", "the record plane", "products-layer-2026-11-08"}},
		{name: "product client of the plane refused", reader: FileDescriptor{ServiceName: "reader", ServiceProduct: "ledger", GrpcS2SClients: []string{"query.RecordMap"}},
			refusal: []string{`"reader" (product "ledger")`, `"stratum"`, "grpc_s2s_client query.RecordMap", "the record plane"}},
		{name: "product client of the plane's c2s half", reader: FileDescriptor{ServiceName: "reader", ServiceProduct: "ledger", GrpcS2SClients: []string{"query.RecordQuery"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.reader
			r.Path, r.Package = "reader.proto", "reader"
			err := ValidateProductLayer(plane(&r))
			if tc.refusal == nil {
				if err != nil {
					t.Fatalf("refused an allowed edge: %v", err)
				}
				return
			}
			wantLayerRefusal(t, err, tc.refusal...)
		})
	}
}

// all_grpc_s2s_clients never generates a client for a refused edge: the
// services a declaring service may not call, by the edge table and, for a
// product, the record plane.
func TestRefusedS2SClients(t *testing.T) {
	dp := writeSurfaceProtoDir(t, layerFleet("", "", "", "", "")[0], layerFleet("", "", "", "", "")[1:]...)
	for _, tc := range []struct {
		self string
		want []string
	}{
		{self: "core", want: []string{"audit.Audit", "billing.Billing", "meter.Meter"}},
		{self: "billing", want: []string{"audit.Audit", "crew.Crew"}},
		{self: "audit", want: []string{"billing.Billing", "crew.Crew", "meter.Meter"}},
		{self: "crew"},
		{self: "unknown"},
	} {
		t.Run(tc.self, func(t *testing.T) {
			var got []string
			for _, r := range RefusedS2SClients(dp, tc.self) {
				if r.ProtoFile == "" || r.Why == "" {
					t.Fatalf("refusal %+v names no proto file or reason", r)
				}
				got = append(got, r.Ref)
			}
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("RefusedS2SClients(%s) = %v, want %v", tc.self, got, tc.want)
			}
		})
	}
	plane := &DescriptorParser{descriptors: map[string]*FileDescriptor{
		"store.proto":  {Path: "store.proto", Package: "store", ServiceName: "stratum", RecordStore: true},
		"query.proto":  {Path: "query.proto", Package: "query", Trait: &TraitDescriptor{Name: "query", Host: "stratum", S2SService: "RecordMap", C2SService: "RecordQuery"}},
		"reader.proto": {Path: "reader.proto", Package: "reader", ServiceName: "reader", ServiceProduct: "ledger"},
	}}
	if got := RefusedS2SClients(plane, "reader"); len(got) != 1 || got[0].Ref != "query.RecordMap" || got[0].ProtoFile != "query.proto" {
		t.Fatalf("a product's refused clients = %+v, want the record plane's S2S half", got)
	}
}
