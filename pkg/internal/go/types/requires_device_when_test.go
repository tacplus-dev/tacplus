// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
)

// (spec.rpc).requires_device_when is requires_device on a narrower set of
// requests: the SAME gate body, wrapped in a test of one request field. The
// unified-CRUD rpc it exists for writes three kinds of row and only one of them
// is a decision about another person.

// approvalWhenFixtureFiles is the requires_device fixture plus the unified-CRUD
// request the conditional gate reads: an enum field and a string field.
func approvalWhenFixtureFiles() []model.ProtoFile {
	files := approvalFixtureFiles()
	files[0].Messages = append(files[0].Messages, model.Message{
		Name: "SetRuleRequest",
		Fields: []model.Field{
			{Name: "rule_id", Type: "string", Number: 1},
			{Name: "effect", Type: "RuleEffect", IsEnum: true, Number: 2},
		},
	})
	return files
}

// approvalWhenMethod is a unary rpc gated on effect == RULE_EFFECT_DELEGATION (2).
func approvalWhenMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:           "SetRule",
		InputType:      "SetRuleRequest",
		OutputType:     "RotateKeyResponse",
		FullInputType:  "vault.SetRuleRequest",
		FullOutputType: "vault.RotateKeyResponse",
		RequiresDeviceWhen: &parser.RequiresDeviceWhenDescriptor{
			Field: "effect", Equals: "RULE_EFFECT_DELEGATION", IsEnum: true, EnumNumber: 2,
		},
	}
}

// The generated wrapper must carry the same gate body requires_device emits,
// wrapped in the conditional — and the whole thing still ahead of the handler
// call. A gate emitted outside the conditional would gate the rpc the
// annotation exists to leave ungated; one emitted after the handler would be no
// gate at all.
func TestGoRequiresDeviceWhenWrapsTheGateInTheCondition(t *testing.T) {
	san := newEgressSanitizer(approvalWhenFixtureFiles())
	method := approvalWhenMethod()
	svc := &parser.ServiceInfo{Name: "VaultAdmin", Methods: []*parser.MethodInfo{method}}

	src := generateGRPCWrapper(svc, method, "vault", "services", "vault",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, san)

	for _, want := range []string{
		"if req.Effect == 2 {",
		"approval.FromIncomingContext(ctx)",
		"approval.RequestHash(req)",
		`p.brokerApproval(ctx, "vault.VaultAdmin/SetRule", approvalID, requestHash, caller, deviceAssertion)`,
		// The declared spelling survives into the generated source as a comment,
		// so a reader of the wrapper sees the enum value, not just the number.
		"effect == RULE_EFFECT_DELEGATION",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("generated wrapper is missing %q:\n%s", want, src)
		}
	}

	cond := strings.Index(src, "if req.Effect == 2 {")
	gate := strings.Index(src, "p.brokerApproval(")
	handler := strings.Index(src, "p.handler.SetRule(")
	if cond < 0 || gate < 0 || handler < 0 || cond > gate || gate > handler {
		t.Fatalf("the gate must sit INSIDE the condition and BEFORE the handler call (cond=%d gate=%d handler=%d):\n%s", cond, gate, handler, src)
	}
}

// A string field is compared against the declared value literally.
func TestGoRequiresDeviceWhenStringField(t *testing.T) {
	san := newEgressSanitizer(approvalWhenFixtureFiles())
	method := approvalWhenMethod()
	method.RequiresDeviceWhen = &parser.RequiresDeviceWhenDescriptor{Field: "rule_id", Equals: "delegation"}
	svc := &parser.ServiceInfo{Name: "VaultAdmin", Methods: []*parser.MethodInfo{method}}

	src := generateGRPCWrapper(svc, method, "vault", "services", "vault",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, san)

	if !strings.Contains(src, `if req.RuleId == "delegation" {`) {
		t.Fatalf("generated wrapper does not compare the string field literally:\n%s", src)
	}
}

// The conditional changes WHEN the check runs, not what the service is wired
// with: the wrapper still gets the broker seam, and the caller-side helpers are
// still generated unconditionally (the caller that never reaches the gated path
// simply never uses them).
func TestGoRequiresDeviceWhenKeepsTheWiring(t *testing.T) {
	svc := approvalFixtureSvc(t)
	method := approvalWhenMethod()
	info := &parser.ProtoInfo{
		Package:  "vault",
		Services: []*parser.ServiceInfo{{Name: "VaultAdmin", Methods: []*parser.MethodInfo{method}}},
	}
	if err := generateServiceWrapper(svc, info, map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, newEgressSanitizer(approvalWhenFixtureFiles())); err != nil {
		t.Fatalf("generateServiceWrapper: %v", err)
	}
	if !goServiceHasRequiresDevice(parser.ServiceDescriptor{
		Methods: []parser.MethodDescriptor{{Name: "SetRule", RequiresDeviceWhen: method.RequiresDeviceWhen}},
	}) {
		t.Fatal("a requires_device_when rpc must make its service need the approval-broker client")
	}

	src, err := generateGrpcClientMethod(&parser.ServiceInfo{Name: "VaultAdmin"}, method, "vault", "services")
	if err != nil {
		t.Fatalf("generateGrpcClientMethod: %v", err)
	}
	for _, want := range []string{"SetRuleApprovalHash", "SetRuleWithApproval"} {
		if !strings.Contains(src, want) {
			t.Fatalf("generated client is missing %q:\n%s", want, src)
		}
	}
}

// The structural refusals are about the whole request shape, which the
// condition does not narrow: when the gate fires it hashes the WHOLE request,
// so a requires_device_when rpc is refused everywhere a requires_device one is
// — and the refusal names the annotation the author actually wrote.
func TestGoRequiresDeviceWhenStructuralRefusals(t *testing.T) {
	cases := []struct {
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
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := approvalFixtureSvc(t)
			method := approvalWhenMethod()
			tc.mutate(method)
			info := &parser.ProtoInfo{
				Package:  "vault",
				Services: []*parser.ServiceInfo{{Name: "VaultAdmin", Methods: []*parser.MethodInfo{method}}},
			}
			err := generateServiceWrapper(svc, info, map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, newEgressSanitizer(approvalWhenFixtureFiles()))
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
