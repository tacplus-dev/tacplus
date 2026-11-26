// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/collection"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
)

// (spec.caller_tid), #475 (products-layer-2026-11-08): a product keeps a
// tenant's data only keyed by the tid of the tenant calling. The generated
// wrapper binds the field to the verified caller's tid claim on every rpc
// shape, and per message on a request stream: empty is stamped, equal passes,
// another tenant or a caller with no tid claim is PERMISSION_DENIED. The
// refusals read as Go's and Java's, verbatim.

func callerTidSanitizer() *rustEgressSanitizer {
	callerTid := map[string]model.AnnotationValue{"caller_tid": {Type: model.AnnotationTypeBool, BoolValue: true}}
	return newRustEgressSanitizer([]model.ProtoFile{{Package: "billing", Messages: []model.Message{
		{Name: "UsageRequest", Fields: []model.Field{
			{Name: "tenant", Type: "string", Number: 1, Annotations: callerTid},
			{Name: "page_token", Type: "string", Number: 2},
			{Name: "page_size", Type: "int32", Number: 3},
			{Name: "from_revision", Type: "int64", Number: 4},
		}},
		{Name: "UsageItem", Fields: []model.Field{{Name: "id", Type: "string", Number: 1}}},
		{Name: "UsagePage", Fields: []model.Field{
			{Name: "items", Type: "UsageItem", Number: 1, IsMessage: true, IsRepeated: true},
			{Name: "next_page_token", Type: "string", Number: 2},
		}},
	}}})
}

func callerTidMethod(name string, client, server bool) *parser.MethodInfo {
	return &parser.MethodInfo{
		Name: name, InputType: "UsageRequest", OutputType: "UsageItem",
		FullInputType: "billing.UsageRequest", FullOutputType: "billing.UsageItem",
		ClientStreaming: client, ServerStreaming: server,
	}
}

func renderCallerTid(t *testing.T, m *parser.MethodInfo) string {
	t.Helper()
	return mustGenerateRustGRPCWrapper(t,
		&parser.ServiceInfo{Name: "Billing", IsS2S: true},
		m, "billing", "services", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, callerTidSanitizer())
}

func wantCallerTid(t *testing.T, got, method, reqVar, stamped string) {
	t.Helper()
	assertOrdered(t, got,
		".and_then(|c| c.tid())",
		`.ok_or_else(|| tonic::Status::permission_denied("`+method+` requires a caller in a tenant: its token carries no tid claim (products-layer-2026-11-08)"))?;`)
	for _, want := range []string{
		"// (spec.caller_tid) tenant: bind the field to the caller's tenant.",
		reqVar + ".tenant.is_empty() {",
		reqVar + ".tenant = " + stamped + ";",
		"} else if " + reqVar + ".tenant != __caller_tid {",
		"return Err(tonic::Status::permission_denied(\n",
		`"tenant does not match the caller's tenant (its token's tid claim): a call keys only its own tenant's data"));`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestRustCallerTid_Unary(t *testing.T) {
	got := renderCallerTid(t, callerTidMethod("RecordUsage", false, false))
	wantCallerTid(t, got, "RecordUsage", "req", "__caller_tid")
	stamp := strings.Index(got, "req.tenant = __caller_tid;")
	call := strings.Index(got, "self.handler.record_usage(")
	if stamp < 0 || call < 0 || stamp > call {
		t.Fatalf("the binding must precede the handler call\ngot:\n%s", got)
	}
}

func TestRustCallerTid_Page(t *testing.T) {
	m := callerTidMethod("ListUsage", false, false)
	m.OutputType, m.FullOutputType = "UsagePage", "billing.UsagePage"
	m.Collection = &parser.CollectionDescriptor{Mode: "PAGE", OrderBy: []string{"id"}, DefaultPageSize: 50}
	m.Resolved = &collection.Resolved{
		Mode: "PAGE", OrderBy: []string{"id"}, DefaultPageSize: 50, MaxPageSize: 10000,
		PageItemType: "billing.UsageItem", PageItemsField: "items",
	}
	got := renderCallerTid(t, m)
	wantCallerTid(t, got, "ListUsage", "req", "__caller_tid")
	assertOrdered(t, got, "req.tenant = __caller_tid;", "collection::decode_page_token")
}

func TestRustCallerTid_ServerStreamingShapes(t *testing.T) {
	plain := callerTidMethod("TailUsage", false, true)
	snapshot := callerTidMethod("StreamUsage", false, true)
	snapshot.Collection = &parser.CollectionDescriptor{Mode: "SNAPSHOT"}
	snapshot.Resolved = &collection.Resolved{Mode: "SNAPSHOT", Tier: collection.TierStream}
	watch := callerTidMethod("WatchUsage", false, true)
	watch.OutputType, watch.FullOutputType = "UsageWatchEvent", "billing.UsageWatchEvent"
	watch.Collection = &parser.CollectionDescriptor{Mode: "WATCH"}
	watch.Resolved = &collection.Resolved{
		Mode: "WATCH", WatchOneOf: "event", WatchItemType: "billing.UsageItem",
		WatchChangeType: "billing.UsageItem", ReplayDepth: 64, IdleSecs: 300,
		WatchRevisionField: "revision",
	}
	for _, m := range []*parser.MethodInfo{plain, snapshot, watch} {
		got := renderCallerTid(t, m)
		wantCallerTid(t, got, m.Name, "req", "__caller_tid")
		// The request is hoisted into the owned binding the stamp needs.
		assertOrdered(t, got, "let req = request.into_inner();", "req.tenant = __caller_tid;")
	}
	for m, handler := range map[*parser.MethodInfo]string{
		plain:    "self.handler.tail_usage(caller.as_ref(), req).await",
		snapshot: "self.handler.stream_usage(caller.as_ref(), req).await",
	} {
		if got := renderCallerTid(t, m); !strings.Contains(got, handler) {
			t.Fatalf("%s: the handler must receive the stamped request\ngot:\n%s", m.Name, got)
		}
	}
}

// A request stream binds every message: the claim is resolved once, before the
// stream is wrapped, and the stamp-or-refuse runs per message.
func TestRustCallerTid_RequestStreamsArePerMessage(t *testing.T) {
	for _, m := range []*parser.MethodInfo{callerTidMethod("UploadUsage", true, false), callerTidMethod("ChatUsage", true, true)} {
		got := renderCallerTid(t, m)
		wantCallerTid(t, got, m.Name, "msg", "__caller_tid.clone()")
		assertOrdered(t, got,
			"let __caller_tid = caller.as_ref()",
			"let stream = stream.map(move |item| -> Result<UsageRequest, tonic::Status> {",
			"msg.tenant = __caller_tid.clone();")
	}
	m := callerTidMethod("UploadScoped", true, false)
	m.TargetNamespaceField = "tenant"
	got := renderCallerTid(t, m)
	assertOrdered(t, got,
		"let stream = stream.then(move |item| {",
		"let __caller_tid = __caller_tid.clone();",
		"msg.tenant = __caller_tid.clone();")
}

// A request without the marker carries no binding.
func TestRustCallerTid_AbsentWithoutTheMarker(t *testing.T) {
	m := callerTidMethod("RecordUsage", false, false)
	m.InputType, m.FullInputType = "UsageItem", "billing.UsageItem"
	got := renderCallerTid(t, m)
	if strings.Contains(got, "__caller_tid") {
		t.Fatalf("an unmarked request must not bind a caller tid\ngot:\n%s", got)
	}
}
