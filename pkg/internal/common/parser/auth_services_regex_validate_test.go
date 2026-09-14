// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"strings"
	"testing"
)

// ValidateAuthServicePatterns is the generation-time gate on auth_services
// regex syntax. Without it a malformed pattern generates cleanly and panics the
// generated service at startup, inside SetMethodAuth's MustCompile, with no way
// left to name the .proto that produced it.

// authServicesParser builds a one-file DescriptorParser with service-level and
// per-RPC auth_services, the exact IR shape the validator consumes.
func authServicesParser(svcPatterns, rpcPatterns []string) *DescriptorParser {
	return &DescriptorParser{
		descriptors: map[string]*FileDescriptor{
			"billing.proto": {
				Path:    "billing.proto",
				Package: "billing",
				Services: []ServiceDescriptor{
					{
						Name:         "BillingService",
						AuthServices: svcPatterns,
						Methods: []MethodDescriptor{
							{Name: "Charge", AuthServices: rpcPatterns},
						},
					},
				},
			},
		},
	}
}

func TestValidateAuthServicePatternsNilParser(t *testing.T) {
	if err := ValidateAuthServicePatterns(nil); err != nil {
		t.Fatalf("ValidateAuthServicePatterns(nil) = %v, want nil", err)
	}
}

func TestValidateAuthServicePatternsAcceptsValidPatterns(t *testing.T) {
	cases := map[string][]string{
		"wildcard shorthand":  {"*"},
		"literal service":     {"billing"},
		"alternation":         {"billing|payments"},
		"character class":     {"billing-[0-9]+"},
		"anchorable subgroup": {"(?:billing|payments)-svc"},
		"none declared":       nil,
	}
	for name, patterns := range cases {
		t.Run(name, func(t *testing.T) {
			if err := ValidateAuthServicePatterns(authServicesParser(patterns, nil)); err != nil {
				t.Fatalf("service-level %v rejected: %v", patterns, err)
			}
			if err := ValidateAuthServicePatterns(authServicesParser(nil, patterns)); err != nil {
				t.Fatalf("rpc-level %v rejected: %v", patterns, err)
			}
		})
	}
}

func TestValidateAuthServicePatternsRejectsMalformedServiceLevel(t *testing.T) {
	err := ValidateAuthServicePatterns(authServicesParser([]string{"billing("}, nil))
	if err == nil {
		t.Fatal("ValidateAuthServicePatterns accepted a service-level pattern that is not valid regex")
	}
	for _, want := range []string{"billing.proto", "BillingService", `"billing("`, "missing closing )"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestValidateAuthServicePatternsRejectsMalformedRpcLevel(t *testing.T) {
	err := ValidateAuthServicePatterns(authServicesParser(nil, []string{"billing[a-"}))
	if err == nil {
		t.Fatal("ValidateAuthServicePatterns accepted an rpc-level pattern that is not valid regex")
	}
	for _, want := range []string{"billing.proto", "BillingService.Charge", `"billing[a-"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}
