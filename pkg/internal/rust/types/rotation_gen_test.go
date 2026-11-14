// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"io/fs"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/templates"
)

// The <Svc>Rotating sibling (tacplus#322) is served for the services whose
// streams carry tokens, and only those: a c2d service (the socket is the
// boundary), an auth-disabled service, and a service without a streaming rpc
// have nothing to rotate. Parity with TestGoServesRotatingSibling.
func TestRustServesRotatingSibling(t *testing.T) {
	bidi := &parser.MethodInfo{Name: "Chat", ClientStreaming: true, ServerStreaming: true}
	unary := &parser.MethodInfo{Name: "Get"}
	mcp := &parser.MethodInfo{Name: "Tool", ServerStreaming: true, IsMcp: true}
	cases := []struct {
		name string
		svc  parser.ServiceInfo
		want bool
	}{
		{"s2s streaming", parser.ServiceInfo{IsS2S: true, Methods: []*parser.MethodInfo{unary, bidi}}, true},
		{"s2s unary only", parser.ServiceInfo{IsS2S: true, Methods: []*parser.MethodInfo{unary}}, false},
		{"s2s mcp tool only", parser.ServiceInfo{IsS2S: true, Methods: []*parser.MethodInfo{mcp}}, false},
		{"c2s only", parser.ServiceInfo{IsC2S: true, Methods: []*parser.MethodInfo{bidi}}, false},
		{"c2d", parser.ServiceInfo{IsS2S: true, IsC2D: true, Methods: []*parser.MethodInfo{bidi}}, false},
		{"auth disabled", parser.ServiceInfo{IsS2S: true, AuthDisabled: true, Methods: []*parser.MethodInfo{bidi}}, false},
	}
	for _, c := range cases {
		if got := rustServesRotatingSibling(&c.svc); got != c.want {
			t.Errorf("%s: rustServesRotatingSibling = %v, want %v", c.name, got, c.want)
		}
		// The descriptor-level predicate the ServiceManager and the crate
		// gate use must agree with the wrapper's.
		d := parser.ServiceDescriptor{Name: c.svc.Name, IsS2S: c.svc.IsS2S, IsC2D: c.svc.IsC2D, AuthDisabled: c.svc.AuthDisabled}
		for _, m := range c.svc.Methods {
			d.Methods = append(d.Methods, parser.MethodDescriptor{Name: m.Name, ClientStreaming: m.ClientStreaming, ServerStreaming: m.ServerStreaming, IsMcp: m.IsMcp})
		}
		if got := rustDescriptorRotates(&d); got != c.want {
			t.Errorf("%s: rustDescriptorRotates = %v, want %v", c.name, got, c.want)
		}
	}
}

// rotation_admit applies each method's own open-time check to a rotation:
// authorize, the auth_any_user check, a server stream's target namespace
// check against the stream's request, and a refusal for an auth-disabled
// method. An opening leg is authenticated. Parity with
// TestGoRotationAdmitMirrorsOpenTimeChecks.
func TestRustRotationAdmitMirrorsOpenTimeChecks(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Ticker", IsS2S: true, Methods: []*parser.MethodInfo{
		{Name: "Up", ClientStreaming: true},
		{Name: "Down", ServerStreaming: true, InputType: "Req", FullInputType: "svc.Req"},
		{Name: "Scoped", ServerStreaming: true, TargetNamespaceField: "ns", InputType: "Req", FullInputType: "svc.Req"},
		{Name: "Anyone", ServerStreaming: true, AuthAnyUser: true, TargetNamespaceField: "/t"},
		{Name: "Open", ServerStreaming: true, AuthDisabled: true},
		{Name: "Get"},
	}}
	out := rustRotationSibling(svc, "svc", "pkgdir", map[string]string{})
	for _, want := range []string{
		`impl rotation::SiblingName for TickerRotating`,
		`const NAME: &'static str = "svc.TickerRotating";`,
		`rotation::Sibling::new("svc.Ticker", ROTATING_METHODS, plain, admit)`,
		`return auth.authenticate("/pkgdir/svc.Ticker/Up", authorization).await.map(|_| ());`,
		`auth.authorize("/pkgdir/svc.Ticker/Up", authorization).await.map(|_| ())`,
		`auth.authorize("/pkgdir/svc.Ticker/Down", authorization).await.map(|_| ())`,
		`return Err(tonic::Status::unavailable(""))`,
		`auth.check_at_or_below("/pkgdir/svc.Ticker/Scoped", authorization, &request.ns)`,
		`auth.check_at_or_below("/pkgdir/svc.Ticker/Anyone", authorization, "/t")`,
		`return Err(tonic::Status::permission_denied(""));`,
		`const ROTATING_METHODS: &[&str] = &["Up", "Down", "Scoped", "Anyone", "Open"];`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rotation sibling missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, `"Get" =>`) {
		t.Errorf("rotation_admit admits a unary rpc:\n%s", out)
	}
	if got := rustRotationSibling(&parser.ServiceInfo{Name: "U", IsS2S: true, Methods: []*parser.MethodInfo{{Name: "Get"}}}, "svc", "d", nil); got != "" {
		t.Errorf("a service with no streaming rpc got a sibling:\n%s", got)
	}
}

// An S2S client of a rotating service opens its token-carrying streams on a
// RotatingChannel; its unary calls, and every call of a client that does not
// rotate, keep the plain channel.
func TestRustClientStreamsRotate(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Ticker", IsS2S: true}
	down := &parser.MethodInfo{Name: "Down", ServerStreaming: true, InputType: "Tick", OutputType: "Tick", FullInputType: "svc.Tick", FullOutputType: "svc.Tick"}
	up := &parser.MethodInfo{Name: "Up", ClientStreaming: true, InputType: "Tick", OutputType: "Tick", FullInputType: "svc.Tick", FullOutputType: "svc.Tick"}
	both := &parser.MethodInfo{Name: "Both", ClientStreaming: true, ServerStreaming: true, InputType: "Tick", OutputType: "Tick", FullInputType: "svc.Tick", FullOutputType: "svc.Tick"}
	get := &parser.MethodInfo{Name: "Get", InputType: "Tick", OutputType: "Tick", FullInputType: "svc.Tick", FullOutputType: "svc.Tick"}
	for _, c := range []struct {
		m    *parser.MethodInfo
		want string
	}{
		{down, "TickerClient::new(rotation::RotatingChannel::new(channel, self.token_provider.clone()))"},
		{up, "TickerClient::new(rotation::RotatingChannel::new(channel, token_provider.clone()))"},
		{both, "TickerClient::new(rotation::RotatingChannel::new(channel, Some(tp.clone())))"},
	} {
		res, err := generateRustGrpcClientMethodRotating(svc, c.m, "svc", map[string]string{}, true)
		if err != nil {
			t.Fatalf("%s: %v", c.m.Name, err)
		}
		if !strings.Contains(res.method, c.want) {
			t.Errorf("%s: rotating client missing %q:\n%s", c.m.Name, c.want, res.method)
		}
		plain, err := generateRustGrpcClientMethodRotating(svc, c.m, "svc", map[string]string{}, false)
		if err != nil {
			t.Fatalf("%s: %v", c.m.Name, err)
		}
		if strings.Contains(plain.method, "rotation::") {
			t.Errorf("%s: a client that does not rotate mentions rotation:\n%s", c.m.Name, plain.method)
		}
	}
	res, err := generateRustGrpcClientMethodRotating(svc, get, "svc", map[string]string{}, true)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if strings.Contains(res.method, "rotation::") {
		t.Errorf("a unary call rotates:\n%s", res.method)
	}
}

// The rotation chassis keeps its fail-closed rules: the wire's message rule,
// rotation at 75%, mint retries every second, refusals with no description,
// make-before-break on the stream's own channel, sibling names.
func TestRustRotationChassisTemplates(t *testing.T) {
	read := func(path string) string {
		raw, err := fs.ReadFile(templates.FS, path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		return string(raw)
	}
	lib := read("rust/chassis/rotation/src/lib.rs.tmpl")
	client := read("rust/chassis/rotation/src/client.rs.tmpl")
	server := read("rust/chassis/rotation/src/server.rs.tmpl")
	cargo := read("rust/chassis/rotation/Cargo.toml.tmpl")
	for _, c := range []struct{ src, want string }{
		{lib, `pub const SUFFIX: &str = "Rotating";`},
		{lib, "pub const ROTATE_AT: f64 = 0.75;"},
		{lib, "pub(crate) const ROTATE_RETRY: Duration = Duration::from_secs(1);"},
		{lib, "!e.last && !e.fin && e.session_id.is_empty()"},
		{lib, `Code::Unauthenticated | Code::PermissionDenied => refusal(status.code())`},
		{client, "Err(e) if e.code() == Code::Unimplemented =>"},
		{client, "Err(e) if matches!(e.code(), Code::Unauthenticated | Code::PermissionDenied) =>"},
		{client, "tonic::client::Grpc::new(self.channel.clone())"},
		{server, "Some(\"its token names another subject\".to_string())"},
		{server, "(shared.admit)(method.clone(), authorization.clone(), s.first_request(), true)"},
	} {
		if !strings.Contains(c.src, c.want) {
			t.Errorf("rotation chassis missing %q", c.want)
		}
	}
	// Workspace pins only: no version of its own.
	_, deps, ok := strings.Cut(cargo, "[dependencies]")
	if !ok {
		t.Fatalf("rotation Cargo.toml has no [dependencies]")
	}
	for _, line := range strings.Split(deps, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if !strings.Contains(line, "workspace = true") && !strings.Contains(line, "path = ") {
			t.Errorf("rotation Cargo.toml declares a dependency version of its own: %q", line)
		}
	}
}
