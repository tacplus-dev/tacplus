// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"
)

// ValidateAuthAnnotations is the generation-time gate on target_namespace_field:
// the field an entitled RPC names as its authorization target must actually
// exist on the request as a string, and an auth_any_user RPC must name an
// absolute namespace literal instead. Every branch below is a deliberately
// broken model the gate must reject — the whole point of the gate is that a
// misdeclared authorization target fails the build rather than authorizing
// against a field that is not there.

// authParser builds a one-file DescriptorParser carrying a single RPC on a
// single service, plus a request message with one string and one int field.
// That is the exact IR shape ValidateAuthAnnotations consumes.
func authParser(method MethodDescriptor) *DescriptorParser {
	return &DescriptorParser{
		descriptors: map[string]*FileDescriptor{
			"tenantadmin.proto": {
				Path:    "tenantadmin.proto",
				Package: "tenantadmin",
				Messages: []MessageDescriptor{
					{
						Name: "SetTenantRequest",
						Fields: []FieldDescriptor{
							{Name: "tenant_namespace", Type: "string"},
							{Name: "generation", Type: "int64"},
						},
					},
				},
				Services: []ServiceDescriptor{
					{Name: "TenantAdmin", Methods: []MethodDescriptor{method}},
				},
			},
		},
	}
}

func TestValidateAuthAnnotationsNilParser(t *testing.T) {
	if err := ValidateAuthAnnotations(nil); err != nil {
		t.Fatalf("ValidateAuthAnnotations(nil) = %v, want nil", err)
	}
}

func TestValidateAuthAnnotationsAcceptsWellFormedMethods(t *testing.T) {
	cases := map[string]MethodDescriptor{
		"no target at all": {
			Name:      "SetTenant",
			InputType: ".tenantadmin.SetTenantRequest",
		},
		"entitled method naming a request string field": {
			Name:                 "SetTenant",
			InputType:            ".tenantadmin.SetTenantRequest",
			TargetNamespaceField: "tenant_namespace",
		},
		"auth_any_user with root namespace literal": {
			Name:                 "WhoAmI",
			InputType:            ".tenantadmin.SetTenantRequest",
			AuthAnyUser:          true,
			TargetNamespaceField: "/",
		},
		"auth_any_user with a scoped namespace literal": {
			Name:                 "WhoAmI",
			InputType:            ".tenantadmin.SetTenantRequest",
			AuthAnyUser:          true,
			TargetNamespaceField: "/tenant",
		},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateAuthAnnotations(authParser(m)); err != nil {
				t.Fatalf("ValidateAuthAnnotations rejected a well-formed method: %v", err)
			}
		})
	}
}

func TestValidateAuthAnnotationsRejectsMisdeclaredTargets(t *testing.T) {
	cases := map[string]struct {
		method MethodDescriptor
		want   string
	}{
		"auth_any_user naming a request field instead of a literal": {
			method: MethodDescriptor{
				Name:                 "WhoAmI",
				InputType:            ".tenantadmin.SetTenantRequest",
				AuthAnyUser:          true,
				TargetNamespaceField: "tenant_namespace",
			},
			want: "must be empty or an absolute namespace literal",
		},
		"auth_any_user literal padded with whitespace": {
			method: MethodDescriptor{
				Name:                 "WhoAmI",
				InputType:            ".tenantadmin.SetTenantRequest",
				AuthAnyUser:          true,
				TargetNamespaceField: " /tenant ",
			},
			want: "must be empty or an absolute namespace literal",
		},
		"entitled method naming a namespace literal instead of a field": {
			method: MethodDescriptor{
				Name:                 "SetTenant",
				InputType:            ".tenantadmin.SetTenantRequest",
				TargetNamespaceField: "/tenant",
			},
			want: "must name a request string field, not a namespace literal",
		},
		"entitled method whose input message is not in the set": {
			method: MethodDescriptor{
				Name:                 "SetTenant",
				InputType:            ".other.MissingRequest",
				TargetNamespaceField: "tenant_namespace",
			},
			want: "was not found",
		},
		"entitled method naming a field that does not exist": {
			method: MethodDescriptor{
				Name:                 "SetTenant",
				InputType:            ".tenantadmin.SetTenantRequest",
				TargetNamespaceField: "namespace",
			},
			want: "not found on input message",
		},
		"entitled method naming a non-string field": {
			method: MethodDescriptor{
				Name:                 "SetTenant",
				InputType:            ".tenantadmin.SetTenantRequest",
				TargetNamespaceField: "generation",
			},
			want: "must name a string field",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := ValidateAuthAnnotations(authParser(tc.method))
			if err == nil {
				t.Fatal("ValidateAuthAnnotations accepted a misdeclared target_namespace_field, want error")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to contain %q", err.Error(), tc.want)
			}
			if !strings.Contains(err.Error(), "TenantAdmin."+tc.method.Name) {
				t.Fatalf("error = %q, want it to name the offending RPC", err.Error())
			}
		})
	}
}

// The gate reports every violation in one pass, so a build fixing one
// misdeclared RPC is not surprised by the next one on the following run.
func TestValidateAuthAnnotationsReportsAllViolations(t *testing.T) {
	dp := authParser(MethodDescriptor{
		Name:                 "SetTenant",
		InputType:            ".tenantadmin.SetTenantRequest",
		TargetNamespaceField: "generation",
	})
	svc := &dp.descriptors["tenantadmin.proto"].Services[0]
	svc.Methods = append(svc.Methods, MethodDescriptor{
		Name:                 "DropTenant",
		InputType:            ".tenantadmin.SetTenantRequest",
		TargetNamespaceField: "namespace",
	})

	err := ValidateAuthAnnotations(dp)
	if err == nil {
		t.Fatal("ValidateAuthAnnotations accepted two misdeclared targets, want error")
	}
	for _, want := range []string{"TenantAdmin.SetTenant", "TenantAdmin.DropTenant"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want it to name %s", err.Error(), want)
		}
	}
}
