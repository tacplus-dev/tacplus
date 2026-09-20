// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/collection"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
)

// (spec.principal)/(spec.namespace) on a request field bind that field to the
// authenticated caller. The Java wrapper rendered this on NO rpc shape — the
// record plane next door rendered the store_ halves only — so a Java service
// declaring the marker generated with the field left exactly as the client
// wrote it. These tests pin every shape the wrapper emits.

func javaRequestScopeSanitizer() *javaEgressSanitizer {
	principal := map[string]model.AnnotationValue{"principal": {Type: model.AnnotationTypeBool, BoolValue: true}}
	namespace := map[string]model.AnnotationValue{"namespace": {Type: model.AnnotationTypeBool, BoolValue: true}}
	return newJavaEgressSanitizer([]model.ProtoFile{{Package: "mail", Messages: []model.Message{
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

func javaRequestScopeMethod(name string) *parser.MethodInfo {
	return &parser.MethodInfo{
		Name: name, InputType: "MailRequest", OutputType: "MailItem",
		FullInputType: "mail.MailRequest", FullOutputType: "mail.MailItem",
	}
}

func renderJavaRequestScope(m *parser.MethodInfo) string {
	return generateJavaGRPCWrapper(
		m, "Mail", "mail",
		"MailOuterClass.MailRequest", "MailOuterClass.MailItem",
		"Helpers", "Helpers",
		false, false, nil,
		"mail", "services", nil, proto.SkippedTraceTypes{},
		javaRequestScopeSanitizer(), nil, nil)
}

// wantJavaRequestScope asserts all four arms for both markers over valueExpr.
func wantJavaRequestScope(t *testing.T, got, valueExpr string) {
	t.Helper()
	for _, want := range []string{
		"final String __reqPrincipal = __reqPrincipalTok == null ? null : __reqPrincipalTok.getSubject();",
		`throw new ProtoException(ProtoException.UNAUTHENTICATED, "`,
		"requires an authenticated caller\");",
		"if (" + valueExpr + ".getRecipient().isEmpty()) {",
		valueExpr + " = " + valueExpr + ".toBuilder().setRecipient(__reqPrincipal).build();",
		`throw new ProtoException(ProtoException.INVALID_ARGUMENT, "recipient does not match the authenticated caller");`,
		"final String __reqNamespace = __reqNamespaceTok == null ? null : __reqNamespaceTok.getNamespace();",
		"requires an authenticated caller carrying a namespace claim\");",
		"if (" + valueExpr + ".getNamespace().isEmpty()) {",
		valueExpr + " = " + valueExpr + ".toBuilder().setNamespace(__reqNamespace).build();",
		`throw new ProtoException(ProtoException.INVALID_ARGUMENT, "namespace does not match the authenticated caller's namespace");`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("Java wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestJavaRequestScope_Unary(t *testing.T) {
	got := renderJavaRequestScope(javaRequestScopeMethod("GetMail"))
	wantJavaRequestScope(t, got, "request")
	if strings.Index(got, "setRecipient(__reqPrincipal)") > strings.Index(got, "handler.getMail(") {
		t.Fatalf("the stamp must precede the handler call\ngot:\n%s", got)
	}
}

func TestJavaRequestScope_Page(t *testing.T) {
	m := javaRequestScopeMethod("ListMail")
	m.OutputType, m.FullOutputType = "MailPage", "mail.MailPage"
	m.Collection = &parser.CollectionDescriptor{Mode: "PAGE", OrderBy: []string{"id"}, DefaultPageSize: 50}
	m.Resolved = &collection.Resolved{
		Mode: "PAGE", OrderBy: []string{"id"}, DefaultPageSize: 50, MaxPageSize: 10000,
		PageItemType: "mail.MailItem", PageItemsField: "items",
	}
	got := renderJavaRequestScope(m)
	wantJavaRequestScope(t, got, "request")
	if strings.Index(got, "setRecipient(__reqPrincipal)") > strings.Index(got, "CollectionRuntime.decodePageToken") {
		t.Fatalf("the PAGE stamp must precede the page-token decode\ngot:\n%s", got)
	}
}

func TestJavaRequestScope_Tier2Snapshot(t *testing.T) {
	m := javaRequestScopeMethod("StreamMail")
	m.ServerStreaming = true
	m.Collection = &parser.CollectionDescriptor{Mode: "SNAPSHOT", OrderBy: []string{"id"}, MaxItems: 1000}
	m.Resolved = &collection.Resolved{Mode: "SNAPSHOT", Tier: collection.TierStream, MaxItems: 1000, OrderBy: []string{"id"}}
	wantJavaRequestScope(t, renderJavaRequestScope(m), "request")
}

func TestJavaRequestScope_Tier3Cursor(t *testing.T) {
	m := javaRequestScopeMethod("CursorMail")
	m.ServerStreaming = true
	m.Collection = &parser.CollectionDescriptor{Mode: "SNAPSHOT", OrderBy: []string{"id"}}
	m.Resolved = &collection.Resolved{Mode: "SNAPSHOT", Tier: collection.TierCursor, OrderBy: []string{"id"}}
	wantJavaRequestScope(t, renderJavaRequestScope(m), "request")
}

func TestJavaRequestScope_Watch(t *testing.T) {
	m := javaRequestScopeMethod("WatchMail")
	m.ServerStreaming = true
	m.OutputType, m.FullOutputType = "MailWatchEvent", "mail.MailWatchEvent"
	m.Collection = &parser.CollectionDescriptor{Mode: "WATCH"}
	m.Resolved = &collection.Resolved{
		Mode: "WATCH", WatchOneOf: "event", WatchItemType: "mail.MailItem",
		WatchChangeType: "mail.MailItem", ReplayDepth: 64, IdleSecs: 300,
		WatchRevisionField:   "revision",
		WatchHasFromRevision: true,
	}
	wantJavaRequestScope(t, renderJavaRequestScope(m), "request")
}

func TestJavaRequestScope_ServerStreaming(t *testing.T) {
	m := javaRequestScopeMethod("TailMail")
	m.ServerStreaming = true
	wantJavaRequestScope(t, renderJavaRequestScope(m), "request")
}

// A request stream is checked per message, through a transforming observer:
// once at stream open would let message 2 name a different principal.
func TestJavaRequestScope_ClientStreamingIsPerMessage(t *testing.T) {
	m := javaRequestScopeMethod("UploadMail")
	m.ClientStreaming = true
	got := renderJavaRequestScope(m)
	wantJavaRequestScope(t, got, "__scoped")
	for _, want := range []string{
		"scopingRequestObserver(handler.uploadMail(",
		"MailOuterClass.MailRequest __scoped = value;",
		"return __scoped;",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("client-streaming must install the per-message scope observer, missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestJavaRequestScope_BidiIsPerMessage(t *testing.T) {
	m := javaRequestScopeMethod("ChatMail")
	m.ClientStreaming, m.ServerStreaming = true, true
	got := renderJavaRequestScope(m)
	wantJavaRequestScope(t, got, "__scoped")
	if !strings.Contains(got, "scopingRequestObserver(handler.chatMail(") {
		t.Fatalf("bidi must install the per-message scope observer\ngot:\n%s", got)
	}
}

// A request declaring neither marker must be untouched.
func TestJavaRequestScope_UnannotatedRequestIsUntouched(t *testing.T) {
	m := javaRequestScopeMethod("Ping")
	m.InputType, m.FullInputType = "MailItem", "mail.MailItem"
	if got := renderJavaRequestScope(m); strings.Contains(got, "__reqPrincipal") || strings.Contains(got, "__reqNamespace") {
		t.Fatalf("a request with no marker must emit no guard\ngot:\n%s", got)
	}
}
