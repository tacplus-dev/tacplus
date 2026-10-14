// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/proto"
)

// (spec.rpc).requires_device_when is requires_device on a narrower set of
// requests: the SAME gate body, wrapped in a test of one request field.

// javaApprovalWhenFixtureFiles is the requires_device fixture plus the
// unified-CRUD request the conditional gate reads.
func javaApprovalWhenFixtureFiles() []model.ProtoFile {
	files := javaApprovalFixtureFiles()
	files[0].Messages = append(files[0].Messages, model.Message{
		Name: "SetRuleRequest",
		Fields: []model.Field{
			{Name: "rule_id", Type: "string", Number: 1},
			{Name: "effect", Type: "RuleEffect", IsEnum: true, Number: 2},
		},
	})
	return files
}

// javaApprovalWhenMethod is a unary rpc gated on effect == RULE_EFFECT_DELEGATION (2).
func javaApprovalWhenMethod() *parser.MethodInfo {
	m := javaApprovalFixtureMethod("SetRule", "SetRuleRequest")
	m.RequiresDevice = false
	m.RequiresDeviceWhen = &parser.RequiresDeviceWhenDescriptor{
		Field: "effect", Equals: "RULE_EFFECT_DELEGATION", IsEnum: true, EnumNumber: 2,
	}
	return m
}

// The generated wrapper must carry the same gate body requires_device emits,
// wrapped in the conditional — and the whole thing still between caller
// extraction and the handler call.
func TestJavaRequiresDeviceWhenWrapsTheGateInTheCondition(t *testing.T) {
	san := newJavaEgressSanitizer(javaApprovalWhenFixtureFiles())
	method := javaApprovalWhenMethod()

	src := generateJavaGRPCWrapper(method, "VaultAdmin", "vault",
		"Vault.SetRuleRequest", "Vault.RotateKeyResponse", "Helpers", "Helpers",
		false, false, nil, "vault", "services", nil, proto.SkippedTraceTypes{}, san, nil, nil)

	for _, want := range []string{
		"if (request.getEffectValue() == 2) {",
		"Approval.Presented __approval = Approval.fromIncomingContext();",
		"byte[] __requestHash = Approval.requestHash(request);",
		`brokerApproval("vault.VaultAdmin/SetRule", __approval, __requestHash, caller);`,
		// The declared spelling survives into the generated source as a comment.
		"effect == RULE_EFFECT_DELEGATION",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("generated wrapper is missing %q:\n%s", want, src)
		}
	}

	cond := strings.Index(src, "if (request.getEffectValue() == 2) {")
	gate := strings.Index(src, "brokerApproval(")
	handler := strings.Index(src, "handler.setRule(")
	if cond < 0 || gate < 0 || handler < 0 || cond > gate || gate > handler {
		t.Fatalf("the gate must sit INSIDE the condition and BEFORE the handler call (cond=%d gate=%d handler=%d):\n%s", cond, gate, handler, src)
	}
	if extract := strings.Index(src, "Caller caller = Auth.callerFromContext();"); extract < 0 || extract > cond {
		t.Fatalf("the gate must run after caller extraction, not before it:\n%s", src)
	}
}

// A string field is compared against the declared value literally — and
// constant-first, so a request whose field is unset is false rather than a NPE.
func TestJavaRequiresDeviceWhenStringField(t *testing.T) {
	san := newJavaEgressSanitizer(javaApprovalWhenFixtureFiles())
	method := javaApprovalWhenMethod()
	method.RequiresDeviceWhen = &parser.RequiresDeviceWhenDescriptor{Field: "rule_id", Equals: "delegation"}

	src := generateJavaGRPCWrapper(method, "VaultAdmin", "vault",
		"Vault.SetRuleRequest", "Vault.RotateKeyResponse", "Helpers", "Helpers",
		false, false, nil, "vault", "services", nil, proto.SkippedTraceTypes{}, san, nil, nil)

	if !strings.Contains(src, `if ("delegation".equals(request.getRuleId())) {`) {
		t.Fatalf("generated wrapper does not compare the string field literally:\n%s", src)
	}
}

// The conditional changes WHEN the check runs, not what the wrapper is wired
// with: the approval chassis import and the broker seam are still emitted.
func TestJavaRequiresDeviceWhenKeepsTheWiring(t *testing.T) {
	svc := javaApprovalFixtureSvc(t)
	method := javaApprovalWhenMethod()

	if _, err := generateServiceWrapper(svc, javaApprovalProtoInfo(method), nil, proto.SkippedTraceTypes{},
		newJavaEgressSanitizer(javaApprovalWhenFixtureFiles())); err != nil {
		t.Fatalf("generateServiceWrapper: %v", err)
	}

	helpers := javaRequiresDeviceClientHelpers(&parser.ServiceInfo{Name: "VaultAdmin"}, method,
		"Vault.SetRuleRequest", "Vault.RotateKeyResponse")
	for _, want := range []string{"setRuleApprovalHash", "setRuleWithApproval"} {
		if !strings.Contains(helpers, want) {
			t.Fatalf("generated client is missing %q:\n%s", want, helpers)
		}
	}
}

// The structural refusals are about the whole request shape, which the
// condition does not narrow — and the refusal names the annotation the author
// actually wrote.
func TestJavaRequiresDeviceWhenStructuralRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*parser.MethodInfo)
		want   string
	}{
		{"auth disabled", func(m *parser.MethodInfo) { m.AuthDisabled = true }, "auth is disabled"},
		{"server streaming", func(m *parser.MethodInfo) { m.ServerStreaming = true }, "not a plain unary rpc"},
		{"map field", func(m *parser.MethodInfo) {
			m.InputType = "TagKeyRequest"
			m.FullInputType = "vault.TagKeyRequest"
		}, "reaches a map field"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := javaApprovalFixtureSvc(t)
			method := javaApprovalWhenMethod()
			tc.mutate(method)
			_, err := generateServiceWrapper(svc, javaApprovalProtoInfo(method), nil, proto.SkippedTraceTypes{},
				newJavaEgressSanitizer(javaApprovalWhenFixtureFiles()))
			if err == nil {
				t.Fatalf("%s: generation succeeded; the rpc would be ungated or unhashable", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: refusal message does not say %q: %v", tc.name, tc.want, err)
			}
			if !strings.Contains(err.Error(), "(spec.rpc).requires_device_when") {
				t.Fatalf("%s: refusal names the wrong annotation: %v", tc.name, err)
			}
		})
	}
}
