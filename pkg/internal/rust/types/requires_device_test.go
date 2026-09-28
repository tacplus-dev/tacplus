// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"
	"text/template"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// requiresDeviceSanitizer indexes a small proto set for the refusal walks.
func requiresDeviceSanitizer() *rustEgressSanitizer {
	return newRustEgressSanitizer([]model.ProtoFile{{
		Package: "widget",
		RelPath: "services/widget/widget.proto",
		Messages: []model.Message{
			{Name: "DeleteWidgetRequest", Fields: []model.Field{
				{Name: "widget_id", Type: "string", Number: 1},
				{Name: "opts", Type: "Options", Number: 2, IsMessage: true},
			}},
			{Name: "Options", Fields: []model.Field{
				{Name: "labels", Type: "TagLabels", Number: 1, IsMessage: true},
			}},
			{Name: "TagLabels", Fields: []model.Field{
				{Name: "labels", Type: "string", Number: 1, IsMap: true, MapKeyType: "string", MapValueType: "string"},
			}},
			{Name: "PlainRequest", Fields: []model.Field{
				{Name: "name", Type: "string", Number: 1},
			}},
			{Name: "StampedRequest", Fields: []model.Field{
				{Name: "at", Type: "Timestamp", Number: 1, TypePackage: "google.protobuf", IsMessage: true},
			}},
			{Name: "OpaqueRequest", Fields: []model.Field{
				{Name: "any", Type: "Any", Number: 1, TypePackage: "google.protobuf", IsMessage: true},
			}},
		},
	}})
}

func requiresDeviceMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:           "DeleteWidget",
		InputType:      "PlainRequest",
		OutputType:     "PlainResponse",
		FullInputType:  "widget.PlainRequest",
		FullOutputType: "widget.PlainResponse",
		RequiresDevice: true,
	}
}

// TestRustRequiresDeviceGatesTheHandlerCall pins WHERE the gate sits: after the
// caller the auth check verified is in hand, and before the request is consumed
// and handed to the handler. An RPC that declares nothing gets no gate at all.
func TestRustRequiresDeviceGatesTheHandlerCall(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "WidgetAdmin"}
	gate := rustRequiresDeviceGate(requiresDeviceMethod(), svc, "widget", "services", nil)
	if gate == "" {
		t.Fatal("a requires_device rpc rendered no gate")
	}
	for _, want := range []string{
		"approval::from_metadata(request.metadata())?",
		"crate::types::services::widget::approval_request_hash_plain_request(request.get_ref())",
		"self.broker_approval(",
	} {
		if !strings.Contains(gate, want) {
			t.Fatalf("gate is missing %q:\n%s", want, gate)
		}
	}
	// The gate must read the request through get_ref, never consume it: the
	// handler call below still needs into_inner.
	if strings.Contains(gate, "into_inner") {
		t.Fatalf("the gate consumed the request; the handler call below could not run:\n%s", gate)
	}

	plain := requiresDeviceMethod()
	plain.RequiresDevice = false
	if got := rustRequiresDeviceGate(plain, svc, "widget", "services", nil); got != "" {
		t.Fatalf("an rpc declaring no requires_device rendered a gate:\n%s", got)
	}
}

// TestRustRequiresDeviceRpcMethodIsWireQualified is the landmine Java hit: the
// value reported to the broker is a WIRE value kernel compares against what the
// approval was created with, so it must be the package-qualified
// "pkg.Service/Method" every language reports — not any language's internal
// auth-key spelling. An unqualified spelling compiles, runs, and silently
// refuses every cross-language approval.
func TestRustRequiresDeviceRpcMethodIsWireQualified(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "WidgetAdmin"}
	gate := rustRequiresDeviceGate(requiresDeviceMethod(), svc, "widget", "services", nil)
	if !strings.Contains(gate, `"widget.WidgetAdmin/DeleteWidget"`) {
		t.Fatalf("rpc_method is not the wire-qualified pkg.Service/Method:\n%s", gate)
	}
	if strings.Contains(gate, `"WidgetAdmin/DeleteWidget"`) {
		t.Fatalf("rpc_method is unqualified; a person's approval would not be spendable across languages:\n%s", gate)
	}
	// It must be the SAME helper the per-method auth registration uses, so the
	// two spellings cannot drift apart.
	if want := rustQualifiedMethodAuthKey("widget", "WidgetAdmin", "DeleteWidget"); !strings.Contains(gate, `"`+want+`"`) {
		t.Fatalf("gate does not use rustQualifiedMethodAuthKey (%q):\n%s", want, gate)
	}
}

// TestRustRequiresDeviceMapFieldIsRefused: a map three messages down is the
// same cross-language iteration-order nondeterminism as one at the top level,
// and the error must NAME the field rather than panic.
func TestRustRequiresDeviceMapFieldIsRefused(t *testing.T) {
	san := requiresDeviceSanitizer()
	got := rustRequiresDeviceMapField("widget.DeleteWidgetRequest", san, "widget")
	if got != "TagLabels.labels" {
		t.Fatalf("nested map field not reported, got %q", got)
	}
	if got := rustRequiresDeviceMapField("widget.PlainRequest", san, "widget"); got != "" {
		t.Fatalf("a map-free request was reported as carrying one: %q", got)
	}
	// A map also keeps the request out of the canonicalization plan, so the
	// types builder emits nothing for it and the wrapper's refusal is the one
	// error the author sees.
	reached := map[string]bool{}
	if rustApprovalReach("widget", "DeleteWidgetRequest", san.msgs, reached) {
		t.Fatal("the canonicalization plan closed over a request reaching a map field")
	}
}

// TestRustRequiresDeviceUnhashableTypeIsRefused covers the refusal Rust has and
// Go and Java do not. They reflect over a descriptor at runtime and reach any
// message; Rust generates the walk, so a type the generator cannot resolve
// would be silently ABSENT from the Rust hash and present in the other two.
func TestRustRequiresDeviceUnhashableTypeIsRefused(t *testing.T) {
	san := requiresDeviceSanitizer()
	if got := rustRequiresDeviceUnhashableType("widget.OpaqueRequest", san, "widget"); got != "google.protobuf.Any" {
		t.Fatalf("an unresolvable message type was not reported, got %q", got)
	}
	// Timestamp and Duration are canonicalized inline (same two fields Go and
	// Java reach by reflection), so they are not a refusal.
	if got := rustRequiresDeviceUnhashableType("widget.StampedRequest", san, "widget"); got != "" {
		t.Fatalf("a well-known type this generator canonicalizes inline was refused: %q", got)
	}
	if got := rustRequiresDeviceUnhashableType("widget.PlainRequest", san, "widget"); got != "" {
		t.Fatalf("a fully resolvable request was refused: %q", got)
	}
}

// TestRustRequiresDeviceClientHelpers pins the caller-side seam: the hash comes
// from the SAME generated function the server recomputes from the live request,
// and the two secrets ride as metadata rather than as request fields.
func TestRustRequiresDeviceClientHelpers(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "WidgetAdmin"}
	out := rustRequiresDeviceClientHelpers(svc, requiresDeviceMethod(), "PlainRequest", "PlainResponse", "widget", "services", nil)
	for _, want := range []string{
		"pub fn delete_widget_approval_hash(request: &PlainRequest) -> Vec<u8> {",
		"crate::types::services::widget::approval_request_hash_plain_request(request)",
		"pub async fn delete_widget_with_approval(",
		"approval::attach_outgoing(&mut request, approval_id, device_assertion)?",
		"self.delete_widget(request).await",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("client helpers missing %q:\n%s", want, out)
		}
	}
	plain := requiresDeviceMethod()
	plain.RequiresDevice = false
	if got := rustRequiresDeviceClientHelpers(svc, plain, "PlainRequest", "PlainResponse", "widget", "services", nil); got != "" {
		t.Fatalf("an rpc declaring no requires_device got caller helpers:\n%s", got)
	}
}

// TestRustApprovalMetadataKeysAreTheWireContract pins the two header names. They
// are a WIRE contract shared with Go and Java, not a Rust naming choice.
func TestRustApprovalMetadataKeysAreTheWireContract(t *testing.T) {
	raw, err := templates.FS.ReadFile("rust/chassis/approval/src/lib.rs.tmpl")
	if err != nil {
		t.Fatalf("read approval chassis template: %v", err)
	}
	src := string(raw)
	for _, want := range []string{
		`METADATA_APPROVAL_ID: &str = "x-tacplus-approval-id"`,
		`METADATA_DEVICE_ASSERTION: &str = "x-tacplus-device-assertion-bin"`,
		// The -bin suffix only works through the binary accessors.
		".get_bin(METADATA_DEVICE_ASSERTION)",
		"insert_bin(",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("the approval chassis no longer carries the wire contract %q", want)
		}
	}
}

// TestRustApprovalWrapperSeamIsGated: a service with no guarded rpc must
// generate exactly what it generated before — no approval field, no setter, no
// broker call, no chassis import. The seam is rendered, not merely guarded in
// source, so this renders the template both ways and looks at the output.
func TestRustApprovalWrapperSeamIsGated(t *testing.T) {
	raw, err := templates.FS.ReadFile("rust/types/servicewrapper.rs.tmpl")
	if err != nil {
		t.Fatalf("read servicewrapper template: %v", err)
	}
	tmpl, err := template.New("servicewrapper").Parse(string(raw))
	if err != nil {
		t.Fatalf("parse servicewrapper template: %v", err)
	}
	render := func(guarded bool) string {
		data := map[string]any{
			"AccountingImports": "", "ClientFields": "", "ClientFieldsInit": "",
			"ClientInitialization": "", "CrossPackageImports": "", "GRPCWrappers": "",
			"HandlerTrait": "", "Header": "", "McpDispatch": "", "RegisterMethodAuth": "",
			"ServiceImports": "", "ServiceLabel": "widget", "ServiceName": "WidgetAdmin",
			"TonicServiceName": "WidgetAdmin", "TraitImpls": "", "TypesCratePath": "services::widget",
			"WatchFields": "", "WatchFieldsInit": "", "WrapperName": "GrpcServer",
			"HasAccounting": false, "HasRequiresDevice": guarded,
		}
		var b strings.Builder
		if err := tmpl.Execute(&b, data); err != nil {
			t.Fatalf("render servicewrapper (guarded=%v): %v", guarded, err)
		}
		return b.String()
	}

	ungated := render(false)
	if strings.Contains(ungated, "approval") {
		t.Fatalf("a service with no requires_device rpc rendered an approval seam:\n%s",
			ungated[max(0, strings.Index(ungated, "approval")-200):])
	}

	gated := render(true)
	for _, want := range []string{
		"use approval;",
		"approval_check: Arc<tokio::sync::RwLock<Option<approval::Check>>>",
		"pub async fn set_approval_check_func(",
		"async fn broker_approval(",
		// Fail-closed twice: an unwired check refuses, and a call with no
		// verified caller has no user to name to the broker.
		"approval check not wired",
		"carries no verified caller",
	} {
		if !strings.Contains(gated, want) {
			t.Fatalf("a guarded service's wrapper is missing %q", want)
		}
	}
}
