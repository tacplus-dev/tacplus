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

// (spec.principal)/(spec.namespace) on a request field bind that field to the
// authenticated caller: empty is stamped, equal passes, different is refused,
// no verified claim is UNAUTHENTICATED. The Go MCP tool dispatch has rendered
// this since it was written; the plain gRPC wrapper rendered it on NO shape at
// all, so a Go service declaring the annotation generated with no check. These
// tests pin every shape the wrapper emits.

func requestScopeSanitizer() *egressSanitizer {
	principal := map[string]model.AnnotationValue{"principal": {Type: model.AnnotationTypeBool, BoolValue: true}}
	namespace := map[string]model.AnnotationValue{"namespace": {Type: model.AnnotationTypeBool, BoolValue: true}}
	return newEgressSanitizer([]model.ProtoFile{{Package: "mail", Messages: []model.Message{
		{Name: "MailRequest", Fields: []model.Field{
			{Name: "recipient", Type: "string", Number: 1, Annotations: principal},
			{Name: "namespace", Type: "string", Number: 2, Annotations: namespace},
			{Name: "page_token", Type: "string", Number: 3},
			{Name: "page_size", Type: "int32", Number: 4},
		}},
		{Name: "MailItem", Fields: []model.Field{{Name: "id", Type: "string", Number: 1}}},
		{Name: "MailPage", Fields: []model.Field{
			{Name: "items", Type: "MailItem", Number: 1, IsMessage: true, IsRepeated: true},
			{Name: "next_page_token", Type: "string", Number: 2},
		}},
	}}})
}

func requestScopeMethod(name string) *parser.MethodInfo {
	return &parser.MethodInfo{
		Name: name, InputType: "MailRequest", OutputType: "MailItem",
		FullInputType: "mail.MailRequest", FullOutputType: "mail.MailItem",
	}
}

func renderRequestScope(m *parser.MethodInfo) string {
	svc := &parser.ServiceInfo{Name: "Mail", IsS2S: true, Methods: []*parser.MethodInfo{m}}
	return generateGRPCWrapper(svc, m, "mail", "services", "",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, requestScopeSanitizer())
}

// wantRequestScope asserts all four arms of the contract for both markers, over
// the named request expression.
func wantRequestScope(t *testing.T, got, reqExpr, failRet string) {
	t.Helper()
	for _, want := range []string{
		"__principal = caller.Token().GetSubject()",
		failRet + ` status.Error(codes.Unauthenticated, "`,
		"requires an authenticated caller\"",
		reqExpr + ".Recipient == \"\" {",
		reqExpr + ".Recipient = __principal",
		`status.Error(codes.InvalidArgument, "recipient does not match the authenticated caller")`,
		"__namespace = caller.Token().GetNamespace()",
		"requires an authenticated caller carrying a namespace claim\"",
		reqExpr + ".Namespace == \"\" {",
		reqExpr + ".Namespace = __namespace",
		`status.Error(codes.InvalidArgument, "namespace does not match the authenticated caller's namespace")`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestGoRequestScope_Unary(t *testing.T) {
	got := renderRequestScope(requestScopeMethod("GetMail"))
	wantRequestScope(t, got, "req", "return nil,")
	// The guard must run before the handler is ever called.
	if strings.Index(got, "req.Recipient = __principal") > strings.Index(got, "p.handler.GetMail(") {
		t.Fatalf("the stamp must precede the handler call\ngot:\n%s", got)
	}
}

func TestGoRequestScope_Page(t *testing.T) {
	m := requestScopeMethod("ListMail")
	m.OutputType, m.FullOutputType = "MailPage", "mail.MailPage"
	m.Collection = &parser.CollectionDescriptor{Mode: "PAGE", OrderBy: []string{"id"}, DefaultPageSize: 50}
	m.Resolved = &collection.Resolved{
		Mode: "PAGE", OrderBy: []string{"id"}, DefaultPageSize: 50, MaxPageSize: 10000,
		PageItemType: "mail.MailItem", PageItemsField: "items",
	}
	got := renderRequestScope(m)
	wantRequestScope(t, got, "req", "return nil,")
	if strings.Index(got, "req.Recipient = __principal") > strings.Index(got, "collection.DecodePageToken") {
		t.Fatalf("the PAGE stamp must precede the page-token decode\ngot:\n%s", got)
	}
}

func TestGoRequestScope_Tier2Snapshot(t *testing.T) {
	m := requestScopeMethod("StreamMail")
	m.ServerStreaming = true
	m.Collection = &parser.CollectionDescriptor{Mode: "SNAPSHOT", OrderBy: []string{"id"}, MaxItems: 1000}
	m.Resolved = &collection.Resolved{Mode: "SNAPSHOT", Tier: collection.TierStream, MaxItems: 1000, OrderBy: []string{"id"}}
	wantRequestScope(t, renderRequestScope(m), "req", "return")
}

func TestGoRequestScope_Tier3Cursor(t *testing.T) {
	m := requestScopeMethod("CursorMail")
	m.ServerStreaming = true
	m.Collection = &parser.CollectionDescriptor{Mode: "SNAPSHOT", OrderBy: []string{"id"}}
	m.Resolved = &collection.Resolved{Mode: "SNAPSHOT", Tier: collection.TierCursor, OrderBy: []string{"id"}}
	wantRequestScope(t, renderRequestScope(m), "req", "return")
}

func TestGoRequestScope_Watch(t *testing.T) {
	m := requestScopeMethod("WatchMail")
	m.ServerStreaming = true
	m.OutputType, m.FullOutputType = "MailWatchEvent", "mail.MailWatchEvent"
	m.Collection = &parser.CollectionDescriptor{Mode: "WATCH"}
	m.Resolved = &collection.Resolved{
		Mode: "WATCH", WatchOneOf: "event", WatchItemType: "mail.MailItem",
		WatchChangeType: "mail.MailItem", ReplayDepth: 64, IdleSecs: 300,
		WatchRevisionField: "revision",
	}
	wantRequestScope(t, renderRequestScope(m), "req", "return")
}

func TestGoRequestScope_ServerStreaming(t *testing.T) {
	m := requestScopeMethod("TailMail")
	m.ServerStreaming = true
	wantRequestScope(t, renderRequestScope(m), "req", "return")
}

// A request stream is checked per message, through the Recv() hook — not once
// at stream open, which would let message 2 name a different principal.
func TestGoRequestScope_ClientStreamingIsPerMessage(t *testing.T) {
	m := requestScopeMethod("UploadMail")
	m.ClientStreaming = true
	got := renderRequestScope(m)
	wantRequestScope(t, got, "req", "return")
	if !strings.Contains(got, "ValidatingClientStreamingServer[types.MailRequest, types.MailItem]{ClientStreamingServer: stream, authorizeReq: nil, scopeReq: func(req *types.MailRequest) error {") {
		t.Fatalf("client-streaming must install the per-message scope hook\ngot:\n%s", got)
	}
}

func TestGoRequestScope_BidiIsPerMessage(t *testing.T) {
	m := requestScopeMethod("ChatMail")
	m.ClientStreaming, m.ServerStreaming = true, true
	got := renderRequestScope(m)
	wantRequestScope(t, got, "req", "return")
	if !strings.Contains(got, "BidiStreamingServer: stream, authorizeReq: nil, scopeReq: func(req *types.MailRequest) error {") {
		t.Fatalf("bidi must install the per-message scope hook\ngot:\n%s", got)
	}
}

// A method whose request declares neither marker must be untouched.
func TestGoRequestScope_UnannotatedRequestIsUntouched(t *testing.T) {
	m := requestScopeMethod("Ping")
	m.InputType, m.FullInputType = "MailItem", "mail.MailItem"
	if got := renderRequestScope(m); strings.Contains(got, "__principal") || strings.Contains(got, "__namespace") {
		t.Fatalf("a request with no marker must emit no guard\ngot:\n%s", got)
	}
}

// (spec.caller_tid) binds a request field to the caller's token's tid claim
// (#475, products-layer-2026-11-08): empty is stamped, equal passes, another
// tenant is PERMISSION_DENIED, and a caller with no tid claim (the platform, a
// product) is PERMISSION_DENIED. Every shape, as the other markers.
func callerTidSanitizer() *egressSanitizer {
	tid := map[string]model.AnnotationValue{"caller_tid": {Type: model.AnnotationTypeBool, BoolValue: true}}
	return newEgressSanitizer([]model.ProtoFile{{Package: "billing", Messages: []model.Message{
		{Name: "UsageRequest", Fields: []model.Field{
			{Name: "tenant", Type: "string", Number: 1, Annotations: tid},
			{Name: "units", Type: "int64", Number: 2},
		}},
		{Name: "UsageResponse", Fields: []model.Field{{Name: "ok", Type: "bool", Number: 1}}},
	}}})
}

func TestGoRequestScope_CallerTid(t *testing.T) {
	for _, tc := range []struct {
		name               string
		client, server     bool
		failRet, installed string
	}{
		{name: "unary", failRet: "return nil,"},
		{name: "server streaming", server: true, failRet: "return"},
		{name: "client streaming", client: true, failRet: "return", installed: "scopeReq: func(req *types.UsageRequest) error {"},
		{name: "bidi", client: true, server: true, failRet: "return", installed: "scopeReq: func(req *types.UsageRequest) error {"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &parser.MethodInfo{
				Name: "RecordUsage", InputType: "UsageRequest", OutputType: "UsageResponse",
				FullInputType: "billing.UsageRequest", FullOutputType: "billing.UsageResponse",
				ClientStreaming: tc.client, ServerStreaming: tc.server,
			}
			svc := &parser.ServiceInfo{Name: "Billing", IsS2S: true, Methods: []*parser.MethodInfo{m}}
			got := generateGRPCWrapper(svc, m, "billing", "services", "",
				map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, callerTidSanitizer())
			for _, want := range []string{
				"__caller_tid = caller.Token().GetTid()",
				tc.failRet + ` status.Error(codes.PermissionDenied, "RecordUsage requires a caller in a tenant: its token carries no tid claim (products-layer-2026-11-08)")`,
				"req.Tenant == \"\" {",
				"req.Tenant = __caller_tid",
				tc.failRet + ` status.Error(codes.PermissionDenied, "tenant does not match the caller's tenant (its token's tid claim): a call keys only its own tenant's data")`,
			} {
				if !strings.Contains(got, want) {
					t.Fatalf("wrapper missing %q\ngot:\n%s", want, got)
				}
			}
			if tc.installed != "" && !strings.Contains(got, tc.installed) {
				t.Fatalf("a request stream must bind every message\ngot:\n%s", got)
			}
			// A request stream binds in its Recv() hook, handed to the handler call.
			if call := strings.Index(got, "p.handler.RecordUsage("); !tc.client && strings.Index(got, "req.Tenant = __caller_tid") > call {
				t.Fatalf("the binding must precede the handler call\ngot:\n%s", got)
			}
		})
	}
}
