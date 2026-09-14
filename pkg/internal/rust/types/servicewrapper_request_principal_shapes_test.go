// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/collection"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
)

// (spec.principal)/(spec.namespace) on a REQUEST field is unqualified in
// spec.proto — "the generated wrapper" resolves the caller's claim and stamps,
// accepts or refuses. It carves out no RPC shape. Until this fix the Rust
// wrapper spliced the check into the plain-unary emit only, so the same
// annotation on a PAGE, Tier-2 SNAPSHOT, WATCH, server-streaming,
// client-streaming or bidi rpc generated silently with no check at all. These
// tests pin every one of those shapes.

// requestScopeSanitizer indexes one request message carrying both markers, plus
// the response shapes the collection modes need.
func requestScopeSanitizer() *rustEgressSanitizer {
	principal := map[string]model.AnnotationValue{"principal": {Type: model.AnnotationTypeBool, BoolValue: true}}
	namespace := map[string]model.AnnotationValue{"namespace": {Type: model.AnnotationTypeBool, BoolValue: true}}
	return newRustEgressSanitizer([]model.ProtoFile{{Package: "mail", Messages: []model.Message{
		{Name: "MailRequest", Fields: []model.Field{
			{Name: "recipient", Type: "string", Number: 1, Annotations: principal},
			{Name: "namespace", Type: "string", Number: 2, Annotations: namespace},
			{Name: "page_token", Type: "string", Number: 3},
			{Name: "page_size", Type: "int32", Number: 4},
			{Name: "from_revision", Type: "int64", Number: 5},
		}},
		{Name: "MailItem", Fields: []model.Field{
			{Name: "id", Type: "string", Number: 1},
		}},
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

func renderRequestScope(t *testing.T, m *parser.MethodInfo) string {
	t.Helper()
	return mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "Mail", IsS2S: true},
		m, "mail", "services", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, requestScopeSanitizer())
}

// wantRequestScope asserts the stamp-or-refuse for both markers is present over
// the named binding: empty -> stamped, different -> INVALID_ARGUMENT, and no
// verified claim at all -> UNAUTHENTICATED.
func wantRequestScope(t *testing.T, got, reqVar string) {
	t.Helper()
	for _, want := range []string{
		`.and_then(|c| c.token().subject())`,
		`requires an authenticated caller"))?;`,
		reqVar + `.recipient.is_empty() {`,
		reqVar + `.recipient = __principal`,
		`"recipient does not match the authenticated caller"));`,
		`.and_then(|c| c.namespace())`,
		`requires an authenticated caller carrying a namespace claim"))?;`,
		reqVar + `.namespace.is_empty() {`,
		reqVar + `.namespace = __namespace`,
		`"namespace does not match the authenticated caller's namespace"));`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("wrapper missing %q\ngot:\n%s", want, got)
		}
	}
}

func TestRustRequestScope_Page(t *testing.T) {
	m := requestScopeMethod("ListMail")
	m.OutputType, m.FullOutputType = "MailPage", "mail.MailPage"
	m.Collection = &parser.CollectionDescriptor{Mode: "PAGE", OrderBy: []string{"id"}, DefaultPageSize: 50}
	m.Resolved = &collection.Resolved{
		Mode: "PAGE", OrderBy: []string{"id"}, DefaultPageSize: 50, MaxPageSize: 10000,
		PageItemType: "mail.MailItem", PageItemsField: "items",
	}
	got := renderRequestScope(t, m)
	wantRequestScope(t, got, "req")
	// The stamp must run BEFORE the page token is decoded: the window the
	// handler is asked for has to be the caller's own.
	stamp := strings.Index(got, "req.recipient = __principal")
	decode := strings.Index(got, "collection::decode_page_token")
	if stamp < 0 || decode < 0 || stamp > decode {
		t.Fatalf("the PAGE stamp must precede decode_page_token\ngot:\n%s", got)
	}
}

func TestRustRequestScope_Tier2Snapshot(t *testing.T) {
	m := requestScopeMethod("StreamMail")
	m.ServerStreaming = true
	m.Collection = &parser.CollectionDescriptor{Mode: "SNAPSHOT"}
	m.Resolved = &collection.Resolved{Mode: "SNAPSHOT", Tier: collection.TierStream}
	got := renderRequestScope(t, m)
	wantRequestScope(t, got, "req")
	// into_inner() is hoisted into the owned binding the stamp needs, and the
	// handler is handed THAT, not a second, unstamped one.
	if !strings.Contains(got, "let req = request.into_inner();") {
		t.Fatalf("Tier-2 did not hoist the request into an owned binding\ngot:\n%s", got)
	}
	if !strings.Contains(got, "self.handler.stream_mail(caller.as_ref(), req).await") {
		t.Fatalf("Tier-2 handler must receive the stamped request\ngot:\n%s", got)
	}
}

func TestRustRequestScope_Watch(t *testing.T) {
	m := requestScopeMethod("WatchMail")
	m.ServerStreaming = true
	m.OutputType, m.FullOutputType = "MailWatchEvent", "mail.MailWatchEvent"
	m.Collection = &parser.CollectionDescriptor{Mode: "WATCH"}
	m.Resolved = &collection.Resolved{
		Mode: "WATCH", WatchOneOf: "event", WatchItemType: "mail.MailItem",
		WatchChangeType: "mail.MailItem", ReplayDepth: 64, IdleSecs: 300,
	}
	got := renderRequestScope(t, m)
	wantRequestScope(t, got, "req")
	if !strings.Contains(got, "let req_msg = req;") {
		t.Fatalf("WATCH must take its request from the stamped binding\ngot:\n%s", got)
	}
}

func TestRustRequestScope_ServerStreaming(t *testing.T) {
	m := requestScopeMethod("TailMail")
	m.ServerStreaming = true
	got := renderRequestScope(t, m)
	wantRequestScope(t, got, "req")
	if !strings.Contains(got, "self.handler.tail_mail(caller.as_ref(), req).await") {
		t.Fatalf("server-streaming handler must receive the stamped request\ngot:\n%s", got)
	}
}

// A request stream is not one request. The claim is resolved once (it cannot
// change mid-stream) but the stamp-or-refuse runs on EVERY message, so message
// N cannot name a principal message N-1 did not.
func TestRustRequestScope_ClientStreamingIsPerMessage(t *testing.T) {
	m := requestScopeMethod("UploadMail")
	m.ClientStreaming = true
	got := renderRequestScope(t, m)
	wantRequestScope(t, got, "msg")
	resolve := strings.Index(got, `.ok_or_else(|| tonic::Status::unauthenticated("UploadMail requires an authenticated caller"))?;`)
	wrap := strings.Index(got, "let stream = stream.map(move |item| -> Result<MailRequest, tonic::Status> {")
	stamp := strings.Index(got, "msg.recipient = __principal.clone();")
	if resolve < 0 || wrap < 0 || stamp < 0 || !(resolve < wrap && wrap < stamp) {
		t.Fatalf("the claim must be resolved once before the stream and stamped per message\ngot:\n%s", got)
	}
}

func TestRustRequestScope_BidiIsPerMessage(t *testing.T) {
	m := requestScopeMethod("ChatMail")
	m.ClientStreaming, m.ServerStreaming = true, true
	got := renderRequestScope(t, m)
	wantRequestScope(t, got, "msg")
	if !strings.Contains(got, "let stream = stream.map(move |item| -> Result<MailRequest, tonic::Status> {") {
		t.Fatalf("bidi must wrap the request stream with the per-message check\ngot:\n%s", got)
	}
}

// target_namespace_field puts the per-message check inside an async block (the
// at-or-below enforcement is awaited); the claims must be cloned per message
// because that block is `move`.
func TestRustRequestScope_ClientStreamingWithTargetNamespaceField(t *testing.T) {
	m := requestScopeMethod("UploadScoped")
	m.ClientStreaming = true
	m.TargetNamespaceField = "namespace"
	got := renderRequestScope(t, m)
	wantRequestScope(t, got, "msg")
	for _, want := range []string{
		"let stream = stream.then(move |item| {",
		"let __principal = __principal.clone();",
		"let __namespace = __namespace.clone();",
		"wrapper.enforce_at_or_below_authorization(authorization.as_deref(), &msg.namespace).await?;",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q\ngot:\n%s", want, got)
		}
	}
}

// The plain-unary shape is the one that already worked; it must keep working.
func TestRustRequestScope_UnaryStillStamps(t *testing.T) {
	got := renderRequestScope(t, requestScopeMethod("GetMail"))
	wantRequestScope(t, got, "req")
}
