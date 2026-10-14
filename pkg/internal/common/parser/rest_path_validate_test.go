// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"
)

// T10: the convention REST path must mirror the gRPC + entitlement-grant
// identity by including the service segment: /{package}/{service}/{method}.
func TestConventionRestPathIncludesServiceSegment(t *testing.T) {
	skipIfNoProtoc(t)

	p := smbParser(t, `
service Catalog {
  option (spec.service_c2s) = {};
  rpc GetItem(Ping) returns (Ping);
}
`)
	fd := p.GetAllFiles()["smbtest.proto"]
	if fd == nil {
		t.Fatalf("smbtest.proto not parsed; files=%v", p.GetAllFiles())
	}
	var got string
	for si := range fd.Services {
		if fd.Services[si].Name != "Catalog" {
			continue
		}
		for mi := range fd.Services[si].Methods {
			if fd.Services[si].Methods[mi].Name == "GetItem" {
				got = fd.Services[si].Methods[mi].HttpPath
			}
		}
	}
	if got == "" {
		t.Fatalf("no HttpPath computed for smbtest.Catalog.GetItem")
	}
	if !strings.HasSuffix(got, "/smbtest/Catalog/GetItem") {
		t.Fatalf("convention REST path must be {root}/{package}/{service}/{method}; got %q", got)
	}
}

// Two services in the same package that share a method name must NOT collide,
// because the service segment disambiguates them. This is the core T10 win.
func TestValidateRestPathCollisionsAllowsServiceDisambiguation(t *testing.T) {
	dp := &DescriptorParser{descriptors: map[string]*FileDescriptor{
		"shop.proto": {
			Package: "shop",
			Services: []ServiceDescriptor{
				{
					Name:  "Catalog",
					IsC2S: true,
					Methods: []MethodDescriptor{{
						Name:        "Get",
						HttpMethods: []string{"POST"},
						HttpPath:    "/shop/Catalog/Get",
					}},
				},
				{
					Name:  "Orders",
					IsC2S: true,
					Methods: []MethodDescriptor{{
						Name:        "Get",
						HttpMethods: []string{"POST"},
						HttpPath:    "/shop/Orders/Get",
					}},
				},
			},
		},
	}}
	if err := ValidateRestPathCollisions(dp); err != nil {
		t.Fatalf("service-disambiguated paths must not collide, got: %v", err)
	}
}

// Two distinct RPCs mapping to the same (HTTP method, path) must fail generation.
func TestValidateRestPathCollisionsDetectsCollision(t *testing.T) {
	dp := &DescriptorParser{descriptors: map[string]*FileDescriptor{
		"shop.proto": {
			Package: "shop",
			Services: []ServiceDescriptor{
				{
					Name:  "Catalog",
					IsC2S: true,
					Methods: []MethodDescriptor{{
						Name:        "Get",
						HttpMethods: []string{"POST"},
						HttpPath:    "/shop/dup",
					}},
				},
				{
					Name:  "Orders",
					IsC2S: true,
					Methods: []MethodDescriptor{{
						Name:        "Fetch",
						HttpMethods: []string{"POST"},
						HttpPath:    "/shop/dup",
					}},
				},
			},
		},
	}}
	err := ValidateRestPathCollisions(dp)
	if err == nil {
		t.Fatal("expected a REST path collision error for two RPCs sharing a route")
	}
	for _, want := range []string{"REST path collisions", "shop.Catalog.Get", "shop.Orders.Fetch"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("collision error missing %q; got: %v", want, err)
		}
	}
}

// Different HTTP methods on the same path are distinct routes and must not
// collide (GET /x and POST /x are different endpoints).
func TestValidateRestPathCollisionsDistinguishesHTTPMethod(t *testing.T) {
	dp := &DescriptorParser{descriptors: map[string]*FileDescriptor{
		"shop.proto": {
			Package: "shop",
			Services: []ServiceDescriptor{{
				Name:  "Catalog",
				IsC2S: true,
				Methods: []MethodDescriptor{
					{Name: "A", HttpMethods: []string{"GET"}, HttpPath: "/shop/x"},
					{Name: "B", HttpMethods: []string{"POST"}, HttpPath: "/shop/x"},
				},
			}},
		},
	}}
	if err := ValidateRestPathCollisions(dp); err != nil {
		t.Fatalf("distinct HTTP methods on the same path must not collide, got: %v", err)
	}
}

// D23: REST paths sit at the origin root. A tree with no root constant yields
// /{package}/{Service}/{Rpc} and a rest_path alias of /{rest_path}.
func TestRestPathsAreOriginRooted(t *testing.T) {
	skipIfNoProtoc(t)

	fd := mcpFile(t, `
service Surface {
  option (spec.service_c2s) = {auth_entitled_users: true};
  rpc ListJwks(Ping) returns (Ping) {
    option (spec.rest_path) = ".well-known/jwks.json";
  }
}
`)

	m := mcpMethod(t, fd, "ListJwks")
	if m.HttpPath != "/rpctest/Surface/ListJwks" {
		t.Errorf("HttpPath = %q, want %q", m.HttpPath, "/rpctest/Surface/ListJwks")
	}
	if m.HttpAliasPath != "/.well-known/jwks.json" {
		t.Errorf("HttpAliasPath = %q, want %q", m.HttpAliasPath, "/.well-known/jwks.json")
	}
}
