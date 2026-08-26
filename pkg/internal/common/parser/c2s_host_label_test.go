// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import "testing"

// Pins (spec.service_c2s).label into ServiceDescriptor.HostLabel, the same
// field (spec.service_s2s).label already populates. Everything downstream that
// groups or routes a surface by its host — filterByHostTag, the mesh target in
// the client generators, the gateway ROUTES entry — keys off HostLabel, so the
// C2S half needs no parallel field.
func TestParseC2SHostLabel(t *testing.T) {
	skipIfNoProtoc(t)

	dir := t.TempDir()
	copySpecProto(t, dir)
	content := `syntax = "proto3";

package taskquery;

import "sys/spec.proto";

option (spec.trait) = {
  id: 40
  name: "query"
};

message Ping { string m = 1; }
message Pong { string m = 1; }

service QueryApi {
  option (spec.service_c2s) = {
    auth_entitled_users: true
    label: "stratum"
  };
  rpc GetPing(Ping) returns (Pong);
}
`
	writeTempProtoFile(t, dir, "query.proto", content)
	p, err := NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	fd, err := p.GetFileDescriptor("query.proto")
	if err != nil {
		t.Fatalf("GetFileDescriptor: %v", err)
	}
	if len(fd.Services) != 1 {
		t.Fatalf("Services = %d, want 1", len(fd.Services))
	}
	svc := fd.Services[0]
	if !svc.IsC2S {
		t.Errorf("IsC2S = false, want true")
	}
	if svc.HostLabel != "stratum" {
		t.Errorf("HostLabel = %q, want stratum", svc.HostLabel)
	}
	if fd.VServiceName != "query" {
		t.Errorf("VServiceName = %q, want query", fd.VServiceName)
	}
}

// A C2S service with no label leaves HostLabel empty: an ordinary C2S service
// is its own host.
func TestParseC2SHostLabelAbsent(t *testing.T) {
	skipIfNoProtoc(t)

	dir := t.TempDir()
	copySpecProto(t, dir)
	content := `syntax = "proto3";

package plainapi;

import "sys/spec.proto";

message Ping { string m = 1; }
message Pong { string m = 1; }

service PlainApi {
  option (spec.service_c2s) = { auth_entitled_users: true };
  rpc GetPing(Ping) returns (Pong);
}
`
	writeTempProtoFile(t, dir, "plain.proto", content)
	p, err := NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	fd, err := p.GetFileDescriptor("plain.proto")
	if err != nil {
		t.Fatalf("GetFileDescriptor: %v", err)
	}
	if fd.Services[0].HostLabel != "" {
		t.Errorf("HostLabel = %q, want empty", fd.Services[0].HostLabel)
	}
}

// c2sHostLabelProtoSet writes a two-file proto set: a physical host service and
// a trait declaring a C2S surface hosted by hostLabel.
func c2sHostLabelProtoSet(t *testing.T, hostLabel string) *DescriptorParser {
	t.Helper()
	dir := t.TempDir()
	copySpecProto(t, dir)
	writeTempProtoFile(t, dir, "stratum.proto", `syntax = "proto3";

package stratum;

import "sys/spec.proto";

option (spec.service) = { name: "stratum" };

message Beat { string m = 1; }

service RecordStore {
  option (spec.service_s2s) = { auth_services: ["*"] };
  rpc Ping(Beat) returns (Beat);
}
`)
	writeTempProtoFile(t, dir, "query.proto", `syntax = "proto3";

package taskquery;

import "sys/spec.proto";

option (spec.trait) = {
  id: 40
  name: "query"
};

message Ping { string m = 1; }
message Pong { string m = 1; }

service QueryApi {
  option (spec.service_c2s) = {
    auth_entitled_users: true
    label: "`+hostLabel+`"
  };
  rpc GetPing(Ping) returns (Pong);
}
`)
	p, err := NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	return p
}

// End-to-end through protoc: a host label no service in the proto set serves
// fails generation, and the same declaration pointed at the real host passes.
func TestValidateC2SHostLabelsEndToEnd(t *testing.T) {
	skipIfNoProtoc(t)

	err := ValidateC2SHostLabels(c2sHostLabelProtoSet(t, "nowhere"))
	if err == nil {
		t.Fatal("a C2S host label no service serves must fail generation")
	}
	t.Logf("unserved host label rejected: %v", err)

	if err := ValidateC2SHostLabels(c2sHostLabelProtoSet(t, "stratum")); err != nil {
		t.Fatalf("the served host label must generate: %v", err)
	}
}
