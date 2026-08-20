// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"testing"

	spec "gitlab.com/secondstarcavalier/schematic/tacplus/target/proto/spec/sys"
)

// Pins the spec.rpc method option: principal and exposure land as the declared
// enum values, flags land as the declared unordered set, exposure falls back to
// RPC_EXPOSURE_INTERNAL when omitted, and a method with no spec.rpc keeps a nil
// RpcDescriptor. Nothing consumes spec.rpc yet, so this parse is the whole
// contract the annotation currently has.

// rpcParser writes a proto using spec.rpc into a temp dir (alongside a copy of
// spec.proto) and returns a DescriptorParser over it.
func rpcParser(t *testing.T, protoBody string) *DescriptorParser {
	t.Helper()
	dir := t.TempDir()
	copySpecProto(t, dir)
	content := `syntax = "proto3";

package rpctest;

import "sys/spec.proto";

message Ping { string m = 1; }
` + protoBody
	writeTempProtoFile(t, dir, "rpctest.proto", content)
	p, err := NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	return p
}

func TestParseMethodOptions_Rpc(t *testing.T) {
	skipIfNoProtoc(t)

	p := rpcParser(t, `
service Surface {
  // Internal service-to-service call: today's normal pattern.
  rpc Sync(Ping) returns (Ping) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_SERVICE};
  }

  // External user call with an entitlement check.
  rpc Read(Ping) returns (Ping) {
    option (spec.rpc) = {
      principal: RPC_PRINCIPAL_USER,
      exposure: RPC_EXPOSURE_EXTERNAL,
      flags: [RPC_FLAG_AUTH_ENTITLED_USERS]
    };
  }

  // External user call that is public and open to any authenticated user:
  // two independent flags on one RPC.
  rpc Discover(Ping) returns (Ping) {
    option (spec.rpc) = {
      principal: RPC_PRINCIPAL_USER,
      exposure: RPC_EXPOSURE_EXTERNAL,
      flags: [RPC_FLAG_AUTH_DISABLED, RPC_FLAG_AUTH_ANY_USER]
    };
  }

  // No spec.rpc at all.
  rpc Legacy(Ping) returns (Ping);
}
`)

	fd, err := p.GetFileDescriptor("rpctest.proto")
	if err != nil {
		t.Fatalf("GetFileDescriptor: %v", err)
	}
	if len(fd.Services) != 1 {
		t.Fatalf("services = %d, want 1", len(fd.Services))
	}

	got := map[string]*RpcDescriptor{}
	for _, m := range fd.Services[0].Methods {
		got[m.Name] = m.Rpc
	}

	want := map[string]*RpcDescriptor{
		"Sync": {
			Principal: spec.RpcPrincipal_RPC_PRINCIPAL_SERVICE,
			Exposure:  spec.RpcExposure_RPC_EXPOSURE_INTERNAL,
		},
		"Read": {
			Principal: spec.RpcPrincipal_RPC_PRINCIPAL_USER,
			Exposure:  spec.RpcExposure_RPC_EXPOSURE_EXTERNAL,
			Flags:     []spec.RpcFlag{spec.RpcFlag_RPC_FLAG_AUTH_ENTITLED_USERS},
		},
		"Discover": {
			Principal: spec.RpcPrincipal_RPC_PRINCIPAL_USER,
			Exposure:  spec.RpcExposure_RPC_EXPOSURE_EXTERNAL,
			Flags: []spec.RpcFlag{
				spec.RpcFlag_RPC_FLAG_AUTH_DISABLED,
				spec.RpcFlag_RPC_FLAG_AUTH_ANY_USER,
			},
		},
		"Legacy": nil,
	}

	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("%s: method missing from parsed descriptor", name)
			continue
		}
		if w == nil {
			if g != nil {
				t.Errorf("%s: Rpc = %+v, want nil (no spec.rpc declared)", name, g)
			}
			continue
		}
		if g == nil {
			t.Errorf("%s: Rpc = nil, want %+v", name, w)
			continue
		}
		if g.Principal != w.Principal {
			t.Errorf("%s: Principal = %s, want %s", name, g.Principal, w.Principal)
		}
		if g.Exposure != w.Exposure {
			t.Errorf("%s: Exposure = %s, want %s", name, g.Exposure, w.Exposure)
		}
		if len(g.Flags) != len(w.Flags) {
			t.Errorf("%s: Flags = %v, want %v", name, g.Flags, w.Flags)
			continue
		}
		for i := range w.Flags {
			if g.Flags[i] != w.Flags[i] {
				t.Errorf("%s: Flags = %v, want %v", name, g.Flags, w.Flags)
				break
			}
		}
	}
}
