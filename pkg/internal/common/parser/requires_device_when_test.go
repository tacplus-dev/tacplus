// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"
)

// (spec.rpc).requires_device_when names a request field and a value. Both are
// resolved against the request's descriptor at parse time, so every way of
// naming something that is not there is a refused generation rather than a
// comparison that is silently never true.

// requiresDeviceWhenParser writes a proto using spec.rpc into a temp dir and
// returns the parse result, error and all — unlike rpcParser, which fails the
// test on a parse error, these cases are ABOUT the parse error.
func requiresDeviceWhenParser(t *testing.T, protoBody string) (*DescriptorParser, error) {
	t.Helper()
	dir := t.TempDir()
	copySpecProto(t, dir)
	content := `syntax = "proto3";

package rdwtest;

import "sys/spec.proto";

enum RuleEffect {
  RULE_EFFECT_UNSPECIFIED = 0;
  RULE_EFFECT_FILTER = 1;
  RULE_EFFECT_DELEGATION = 2;
}

message SetRuleRequest {
  string name = 1;
  RuleEffect effect = 2;
  repeated string tags = 3;
  map<string, string> labels = 4;
  int64 revision = 5;
  optional string note = 6;
}

message SetRuleResponse { int64 revision = 1; }
` + protoBody
	writeTempProtoFile(t, dir, "rdwtest.proto", content)
	return NewDescriptorParser(dir)
}

// rdwService wraps one rpc's spec.rpc body in the service the fixture messages
// belong to.
func rdwService(rpcOption string) string {
	return `
service Rules {
  rpc SetRule(SetRuleRequest) returns (SetRuleResponse) {
    option (spec.rpc) = ` + rpcOption + `;
  }
}
`
}

func TestRequiresDeviceWhenRefusals(t *testing.T) {
	skipIfNoProtoc(t)

	cases := []struct {
		name   string
		option string
		want   string
	}{{
		name:   "both requires_device and requires_device_when",
		option: `{requires_device: true, requires_device_when: {field: "effect", equals: "RULE_EFFECT_DELEGATION"}}`,
		want:   "declare one or the other, not both",
	}, {
		name:   "unknown field name",
		option: `{requires_device_when: {field: "nosuchfield", equals: "x"}}`,
		want:   `no top-level field of that name`,
	}, {
		name:   "repeated field",
		option: `{requires_device_when: {field: "tags", equals: "x"}}`,
		want:   "is repeated or a map",
	}, {
		name:   "map field",
		option: `{requires_device_when: {field: "labels", equals: "x"}}`,
		want:   "is repeated or a map",
	}, {
		name:   "non-string non-enum field",
		option: `{requires_device_when: {field: "revision", equals: "1"}}`,
		want:   "which is a int64 field; the gate compares a plain string or enum field",
	}, {
		name:   "explicit presence field",
		option: `{requires_device_when: {field: "note", equals: "x"}}`,
		want:   "has explicit presence",
	}, {
		name:   "unknown enum value name",
		option: `{requires_device_when: {field: "effect", equals: "RULE_EFFECT_NOPE"}}`,
		want:   "has no value of that name",
	}, {
		name:   "no field named at all",
		option: `{requires_device_when: {equals: "RULE_EFFECT_DELEGATION"}}`,
		want:   "with no field",
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := requiresDeviceWhenParser(t, rdwService(tc.option))
			if err == nil {
				t.Fatalf("%s: parse succeeded; the rpc would generate a gate that never fires", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: refusal does not say %q: %v", tc.name, tc.want, err)
			}
			if !strings.Contains(err.Error(), "rdwtest.Rules.SetRule") {
				t.Fatalf("%s: refusal does not name the rpc: %v", tc.name, err)
			}
		})
	}
}

// The two shapes that parse: an enum field, whose declared value NAME is
// resolved to its number through the enum's descriptor, and a string field,
// whose value is taken literally.
func TestRequiresDeviceWhenParses(t *testing.T) {
	skipIfNoProtoc(t)

	cases := []struct {
		name   string
		option string
		want   RequiresDeviceWhenDescriptor
	}{{
		name:   "enum field",
		option: `{requires_device_when: {field: "effect", equals: "RULE_EFFECT_DELEGATION"}}`,
		want:   RequiresDeviceWhenDescriptor{Field: "effect", Equals: "RULE_EFFECT_DELEGATION", IsEnum: true, EnumNumber: 2},
	}, {
		name:   "string field",
		option: `{requires_device_when: {field: "name", equals: "delegation"}}`,
		want:   RequiresDeviceWhenDescriptor{Field: "name", Equals: "delegation"},
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := requiresDeviceWhenParser(t, rdwService(tc.option))
			if err != nil {
				t.Fatalf("%s: NewDescriptorParser: %v", tc.name, err)
			}
			fd, err := p.GetFileDescriptor("rdwtest.proto")
			if err != nil {
				t.Fatalf("GetFileDescriptor: %v", err)
			}
			m := fd.Services[0].Methods[0]
			if m.RequiresDevice {
				t.Fatal("requires_device_when must not set requires_device; they are separate annotations")
			}
			if m.RequiresDeviceWhen == nil {
				t.Fatalf("%s: RequiresDeviceWhen = nil, want %+v", tc.name, tc.want)
			}
			if *m.RequiresDeviceWhen != tc.want {
				t.Fatalf("%s: RequiresDeviceWhen = %+v, want %+v", tc.name, *m.RequiresDeviceWhen, tc.want)
			}
			// The gate is the gate however it was declared: every structural
			// check and the broker wiring key off this one answer.
			if !m.HasDeviceGate() {
				t.Fatal("a requires_device_when rpc must report HasDeviceGate")
			}
		})
	}
}

// An rpc that declares neither carries no gate and no descriptor at all — the
// proto sets that predate this annotation generate exactly as they did.
func TestRequiresDeviceWhenAbsent(t *testing.T) {
	skipIfNoProtoc(t)

	p, err := requiresDeviceWhenParser(t, rdwService(`{requires_device: true}`))
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	fd, err := p.GetFileDescriptor("rdwtest.proto")
	if err != nil {
		t.Fatalf("GetFileDescriptor: %v", err)
	}
	m := fd.Services[0].Methods[0]
	if m.RequiresDeviceWhen != nil {
		t.Fatalf("RequiresDeviceWhen = %+v, want nil", *m.RequiresDeviceWhen)
	}
	if !m.HasDeviceGate() {
		t.Fatal("a plain requires_device rpc must still report the unconditional gate")
	}
}
