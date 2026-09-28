// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

// (spec.rpc).requires_device_when is requires_device on a narrower set of
// requests: the SAME gate body, wrapped in a test of one request field.

// requiresDeviceWhenMethod is a unary rpc gated on effect == RULE_EFFECT_DELEGATION (2).
func requiresDeviceWhenMethod() *parser.MethodInfo {
	m := requiresDeviceMethod()
	m.RequiresDevice = false
	m.RequiresDeviceWhen = &parser.RequiresDeviceWhenDescriptor{
		Field: "effect", Equals: "RULE_EFFECT_DELEGATION", IsEnum: true, EnumNumber: 2,
	}
	return m
}

// The gate body is the requires_device one, unchanged, inside the conditional —
// still reading the request through get_ref so the handler call below can still
// consume it.
func TestRustRequiresDeviceWhenWrapsTheGateInTheCondition(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "WidgetAdmin"}
	gate := rustRequiresDeviceGate(requiresDeviceWhenMethod(), svc, "widget", "services", nil)
	if gate == "" {
		t.Fatal("a requires_device_when rpc rendered no gate")
	}
	for _, want := range []string{
		"if request.get_ref().effect == 2 {",
		"approval::from_metadata(request.metadata())?",
		"crate::types::services::widget::approval_request_hash_plain_request(request.get_ref())",
		`self.broker_approval("widget.WidgetAdmin/DeleteWidget"`,
		// The declared spelling survives into the generated source as a comment.
		"effect == RULE_EFFECT_DELEGATION",
	} {
		if !strings.Contains(gate, want) {
			t.Fatalf("gate is missing %q:\n%s", want, gate)
		}
	}
	cond := strings.Index(gate, "if request.get_ref().effect == 2 {")
	broker := strings.Index(gate, "self.broker_approval(")
	if cond < 0 || broker < 0 || cond > broker {
		t.Fatalf("the gate must sit INSIDE the condition (cond=%d broker=%d):\n%s", cond, broker, gate)
	}
	if !strings.HasSuffix(strings.TrimRight(gate, "\n"), "}") {
		t.Fatalf("the conditional is not closed:\n%s", gate)
	}
	if strings.Contains(gate, "into_inner") {
		t.Fatalf("the gate consumed the request; the handler call below could not run:\n%s", gate)
	}
}

// A string field is compared against the declared value literally.
func TestRustRequiresDeviceWhenStringField(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "WidgetAdmin"}
	method := requiresDeviceWhenMethod()
	method.RequiresDeviceWhen = &parser.RequiresDeviceWhenDescriptor{Field: "rule_id", Equals: "delegation"}
	gate := rustRequiresDeviceGate(method, svc, "widget", "services", nil)
	if !strings.Contains(gate, `if request.get_ref().rule_id == "delegation" {`) {
		t.Fatalf("gate does not compare the string field literally:\n%s", gate)
	}
}

// The conditional changes WHEN the check runs, not what the service is wired
// with: the service still counts as guarded for the ServiceManager seam, and
// the caller-side helpers are still generated unconditionally.
func TestRustRequiresDeviceWhenKeepsTheWiring(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "WidgetAdmin"}
	method := requiresDeviceWhenMethod()
	if !method.HasDeviceGate() {
		t.Fatal("a requires_device_when rpc must report HasDeviceGate")
	}
	out := rustRequiresDeviceClientHelpers(svc, method, "PlainRequest", "PlainResponse", "widget", "services", nil)
	for _, want := range []string{
		"pub fn delete_widget_approval_hash(request: &PlainRequest) -> Vec<u8> {",
		"pub async fn delete_widget_with_approval(",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("client helpers missing %q:\n%s", want, out)
		}
	}
}

// The request-shape refusals do not narrow with the condition: when the gate
// fires it hashes the WHOLE request, so a requires_device_when rpc reaching a
// map or an unresolvable type is refused exactly as requires_device is.
func TestRustRequiresDeviceWhenKeepsTheShapeRefusals(t *testing.T) {
	san := requiresDeviceSanitizer()
	if got := rustRequiresDeviceMapField("widget.DeleteWidgetRequest", san, "widget"); got != "TagLabels.labels" {
		t.Fatalf("nested map field not reported, got %q", got)
	}
	if got := rustRequiresDeviceUnhashableType("widget.OpaqueRequest", san, "widget"); got != "google.protobuf.Any" {
		t.Fatalf("an unresolvable message type was not reported, got %q", got)
	}
	// And the refusal names the annotation the author actually wrote.
	if got := requiresDeviceWhenMethod().DeviceGateAnnotation(); got != "requires_device_when" {
		t.Fatalf("DeviceGateAnnotation = %q, want requires_device_when", got)
	}
}
