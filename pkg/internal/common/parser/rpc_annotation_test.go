// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"testing"

	spec "gitlab.com/seabeam/schematic/tacplus/target/proto/spec/sys"
)

// Pins the spec.rpc method option: principal and exposure land as the declared
// enum values, flags land as the declared unordered set, exposure falls back to
// RPC_EXPOSURE_INTERNAL when omitted, and a method with no spec.rpc keeps a nil
// RpcDescriptor. TestParseMethodOptions_RpcAuth pins the second half — the auth
// facts spec.rpc carries into the method descriptor the generators read.

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
      flags: [RPC_FLAG_PUBLIC_NO_AUTH, RPC_FLAG_AUTH_ANY_USER]
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
				spec.RpcFlag_RPC_FLAG_PUBLIC_NO_AUTH,
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

// Pins the auth facts spec.rpc carries onto the method descriptor: both
// no-token flags land as AuthDisabled, RPC_FLAG_AUTH_ANY_USER lands as
// AuthAnyUser, and auth_services / target_namespace_field land verbatim. These
// are the fields the per-language generators and the registry read.
func TestParseMethodOptions_RpcAuth(t *testing.T) {
	skipIfNoProtoc(t)

	p := rpcParser(t, `
service Surface {
  // Public endpoint: no token at all.
  rpc Discovery(Ping) returns (Ping) {
    option (spec.rpc) = {
      principal: RPC_PRINCIPAL_USER,
      exposure: RPC_EXPOSURE_EXTERNAL,
      flags: [RPC_FLAG_PUBLIC_NO_AUTH]
    };
  }

  // Bootstrap: authenticates from the request body, so the interceptor skips it.
  rpc Bootstrap(Ping) returns (Ping) {
    option (spec.rpc) = {
      principal: RPC_PRINCIPAL_SERVICE,
      flags: [RPC_FLAG_SELF_AUTHENTICATES],
      auth_services: ["*"]
    };
  }

  // Any authenticated user, no entitlement check.
  rpc Exchange(Ping) returns (Ping) {
    option (spec.rpc) = {
      principal: RPC_PRINCIPAL_USER,
      exposure: RPC_EXPOSURE_EXTERNAL,
      flags: [RPC_FLAG_AUTH_ANY_USER]
    };
  }

  // Named callers plus a level-or-below namespace gate.
  rpc Scoped(Ping) returns (Ping) {
    option (spec.rpc) = {
      principal: RPC_PRINCIPAL_SERVICE,
      auth_services: ["sentinel", "gateway"],
      target_namespace_field: "m"
    };
  }
}
`)

	fd, err := p.GetFileDescriptor("rpctest.proto")
	if err != nil {
		t.Fatalf("GetFileDescriptor: %v", err)
	}

	type authWant struct {
		disabled      bool
		anyUser       bool
		services      []string
		targetNsField string
	}
	want := map[string]authWant{
		"Discovery": {disabled: true},
		"Bootstrap": {disabled: true, services: []string{"*"}},
		"Exchange":  {anyUser: true},
		"Scoped":    {services: []string{"sentinel", "gateway"}, targetNsField: "m"},
	}

	for _, m := range fd.Services[0].Methods {
		w, ok := want[m.Name]
		if !ok {
			t.Errorf("%s: unexpected method", m.Name)
			continue
		}
		if m.AuthDisabled != w.disabled {
			t.Errorf("%s: AuthDisabled = %v, want %v", m.Name, m.AuthDisabled, w.disabled)
		}
		if m.AuthAnyUser != w.anyUser {
			t.Errorf("%s: AuthAnyUser = %v, want %v", m.Name, m.AuthAnyUser, w.anyUser)
		}
		if len(m.AuthServices) != len(w.services) {
			t.Errorf("%s: AuthServices = %v, want %v", m.Name, m.AuthServices, w.services)
		} else {
			for i := range w.services {
				if m.AuthServices[i] != w.services[i] {
					t.Errorf("%s: AuthServices = %v, want %v", m.Name, m.AuthServices, w.services)
					break
				}
			}
		}
		if m.TargetNamespaceField != w.targetNsField {
			t.Errorf("%s: TargetNamespaceField = %q, want %q", m.Name, m.TargetNamespaceField, w.targetNsField)
		}
	}
}
