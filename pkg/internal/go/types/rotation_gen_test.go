// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"go/parser"
	"go/token"
	"io/fs"
	"strings"
	"testing"
	"text/template"

	commonparser "github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/templates"
)

// The <Svc>Rotating sibling (tacplus#322) is served for the services whose
// streams carry tokens, and only those: a c2d service (the socket is the
// boundary), an auth-disabled service, and a service without a streaming rpc
// have nothing to rotate.
func TestGoServesRotatingSibling(t *testing.T) {
	bidi := &commonparser.MethodInfo{Name: "Chat", ClientStreaming: true, ServerStreaming: true}
	unary := &commonparser.MethodInfo{Name: "Get"}
	cases := []struct {
		name string
		svc  commonparser.ServiceInfo
		want bool
	}{
		{"s2s streaming", commonparser.ServiceInfo{IsS2S: true, Methods: []*commonparser.MethodInfo{unary, bidi}}, true},
		{"s2s unary only", commonparser.ServiceInfo{IsS2S: true, Methods: []*commonparser.MethodInfo{unary}}, false},
		{"c2s only", commonparser.ServiceInfo{IsC2S: true, Methods: []*commonparser.MethodInfo{bidi}}, false},
		{"c2d", commonparser.ServiceInfo{IsS2S: true, IsC2D: true, Methods: []*commonparser.MethodInfo{bidi}}, false},
		{"auth disabled", commonparser.ServiceInfo{IsS2S: true, AuthDisabled: true, Methods: []*commonparser.MethodInfo{bidi}}, false},
	}
	for _, c := range cases {
		if got := goServesRotatingSibling(&c.svc); got != c.want {
			t.Errorf("%s: goServesRotatingSibling = %v, want %v", c.name, got, c.want)
		}
	}
}

// rotationAdmit applies each method's own open-time check to a rotation:
// authorize with the stream's request (server streaming) or none, the
// authenticate-only check where the wrapper authorizes message by message,
// the auth_any_user check, and a refusal for an auth-disabled method. An
// opening leg is authenticated.
func TestGoRotationAdmitMirrorsOpenTimeChecks(t *testing.T) {
	svc := &commonparser.ServiceInfo{Name: "Ticker", IsS2S: true, Methods: []*commonparser.MethodInfo{
		{Name: "Up", ClientStreaming: true},
		{Name: "Down", ServerStreaming: true},
		{Name: "Both", ClientStreaming: true, ServerStreaming: true},
		{Name: "Scoped", ClientStreaming: true, TargetNamespaceField: "ns"},
		{Name: "Anyone", ServerStreaming: true, AuthAnyUser: true, TargetNamespaceField: "/t"},
		{Name: "Open", ServerStreaming: true, AuthDisabled: true},
		{Name: "Get"},
	}}
	out := goRotationAdmit(svc, "svc")
	for _, want := range []string{
		`case "Up":`,
		`return p.authorize(ctx, "/svc" + types.Ticker_Up_FullMethodName, nil)`,
		`return p.authorize(ctx, "/svc" + types.Ticker_Down_FullMethodName, first)`,
		`return p.authorize(ctx, "/svc" + types.Ticker_Both_FullMethodName, nil)`,
		`return p.authenticateAtOrBelow(ctx, "/t")`,
		`if !rotating {
			return p.authenticate(ctx)
		}`,
		`return nil, status.Error(codes.PermissionDenied, "")`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rotationAdmit missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `case "Get":`) {
		t.Errorf("rotationAdmit admits a unary rpc:\n%s", out)
	}
	scoped := out[strings.Index(out, `case "Scoped":`):]
	scoped = scoped[:strings.Index(scoped, `case "Anyone":`)]
	if strings.Contains(scoped, "p.authorize(") {
		t.Errorf("a message-authorized request stream must rotate on authenticate only:\n%s", scoped)
	}
	src := "package x\n" + strings.ReplaceAll(out, "{{.ServerName}}", "Ticker")
	if _, err := parser.ParseFile(token.NewFileSet(), "admit.go", src, 0); err != nil {
		t.Fatalf("rotationAdmit does not parse: %v\n%s", err, src)
	}
}

// The rotation chassis renders and parses, and keeps its fail-closed rules:
// the same connection for every leg, the sibling registered from the plain
// descriptor, refusals with no description.
func TestGoRotationChassisRenders(t *testing.T) {
	raw, err := fs.ReadFile(templates.FS, "go/chassis/rotation/rotation.go.tmpl")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	tmpl, err := template.New("rotation").Parse(string(raw))
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, struct{ ModuleBasePath string }{"example.com/m"}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if _, err := parser.ParseFile(token.NewFileSet(), "rotation.go", buf.Bytes(), 0); err != nil {
		t.Fatalf("rendered chassis does not parse: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		`Suffix = "Rotating"`,
		"RotateAt = 0.75",
		"rotateRetry = time.Second",
		"ServiceName: desc.ServiceName + Suffix",
		"p.Pin(ctx)",
		"codes.Unimplemented",
		"func refusal(code codes.Code) error { return status.Error(code, \"\") }",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered chassis missing %q", want)
		}
	}
}
