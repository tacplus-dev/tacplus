// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"strings"
	"testing"
	"text/template"

	commonparser "github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/templates"
)

// The Java <Svc>Rotating sibling (tacplus#322) is served, and a Java S2S
// client rotates, for exactly the services Go's are: those whose streams carry
// tokens.
func TestJavaServesRotatingSibling(t *testing.T) {
	bidi := &commonparser.MethodInfo{Name: "Chat", ClientStreaming: true, ServerStreaming: true}
	unary := &commonparser.MethodInfo{Name: "Get"}
	mcp := &commonparser.MethodInfo{Name: "Tool", ClientStreaming: true, ServerStreaming: true, IsMcp: true}
	cases := []struct {
		name string
		svc  commonparser.ServiceInfo
		want bool
	}{
		{"s2s streaming", commonparser.ServiceInfo{IsS2S: true, Methods: []*commonparser.MethodInfo{unary, bidi}}, true},
		{"s2s unary only", commonparser.ServiceInfo{IsS2S: true, Methods: []*commonparser.MethodInfo{unary}}, false},
		{"s2s mcp carrier only", commonparser.ServiceInfo{IsS2S: true, Methods: []*commonparser.MethodInfo{mcp}}, false},
		{"c2s only", commonparser.ServiceInfo{IsC2S: true, Methods: []*commonparser.MethodInfo{bidi}}, false},
		{"c2d", commonparser.ServiceInfo{IsS2S: true, IsC2D: true, Methods: []*commonparser.MethodInfo{bidi}}, false},
		{"auth disabled", commonparser.ServiceInfo{IsS2S: true, AuthDisabled: true, Methods: []*commonparser.MethodInfo{bidi}}, false},
	}
	for _, c := range cases {
		if got := javaServesRotatingSibling(&c.svc); got != c.want {
			t.Errorf("%s: javaServesRotatingSibling = %v, want %v", c.name, got, c.want)
		}
	}
	if javaServesRotatingSibling(nil) {
		t.Errorf("javaServesRotatingSibling(nil) = true")
	}
}

// rotationAdmit applies each method's own open-time check to a rotation, as
// the Java wrapper applies it at open: authorize with the stream's request
// (server streaming) or none, the authenticate-only check where the wrapper
// authorizes message by message, the auth_any_user check (under the method
// key for RPC_FLAG_ANY_TENANT), and a refusal for an auth-disabled method. An
// opening leg is authenticated.
func TestJavaRotationAdmitMirrorsOpenTimeChecks(t *testing.T) {
	svc := &commonparser.ServiceInfo{Name: "Ticker", IsS2S: true, Methods: []*commonparser.MethodInfo{
		{Name: "Up", ClientStreaming: true},
		{Name: "Down", ServerStreaming: true},
		{Name: "Both", ClientStreaming: true, ServerStreaming: true},
		{Name: "Scoped", ClientStreaming: true, TargetNamespaceField: "ns"},
		{Name: "Anyone", ServerStreaming: true, AuthAnyUser: true, TargetNamespaceField: "/t"},
		{Name: "Tenants", ServerStreaming: true, AuthAnyUser: true, AnyTenant: true},
		{Name: "Open", ServerStreaming: true, AuthDisabled: true},
		{Name: "Get"},
	}}
	out := javaRotationAdmit(svc, "ticker", "svc", "x.Authorizer", "x.StreamRotation", "x.TokenWrapper")
	for _, want := range []string{
		"return x.StreamRotation.serve(bindService(), this::rotationAdmit);",
		`case "Up":`,
		`return authorizer.authorize("Ticker/Up", null);`,
		`return authorizer.authorize("Ticker/Down", first);`,
		`return authorizer.authorize("Ticker/Both", null);`,
		`return rotationAuthenticate(null, "/t");`,
		`return rotationAuthenticate("Ticker/Tenants", null);`,
		"if (!rotating) {\n                    return rotationAuthenticate(null, null);\n                }",
		"throw io.grpc.Status.PERMISSION_DENIED.asRuntimeException();",
		"throw io.grpc.Status.UNIMPLEMENTED.asRuntimeException();",
		"x.Authorizer.ANY_TENANT_METHOD_CTX_KEY",
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
	if strings.Contains(scoped, "authorizer.authorize(") {
		t.Errorf("a message-authorized request stream must rotate on authenticate only:\n%s", scoped)
	}
	if strings.Count(out, "{") != strings.Count(out, "}") {
		t.Errorf("rotationAdmit braces do not balance:\n%s", out)
	}
}

// The Java rotation chassis renders and keeps its fail-closed rules: the
// sibling built from the plain definition, the pinned channel for every leg,
// refusals with no description, the 75% rotation point and the 1 s retry.
func TestJavaRotationChassisRenders(t *testing.T) {
	raw, err := templates.FS.ReadFile("java/chassis/rotation/StreamRotation.java.tmpl")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	tmpl, err := template.New("rotation").Parse(string(raw))
	if err != nil {
		t.Fatalf("parse template: %v", err)
	}
	var buf bytes.Buffer
	data := struct{ ChassisBase, TypesBase, PackageBase string }{"com.example.chassis", "com.example.types", "com.example"}
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"package com.example.chassis.rotation;",
		"import com.example.types.sys.rotation.Rotation.Envelope;",
		`public static final String SUFFIX = "Rotating";`,
		"public static final double ROTATE_AT = 0.75;",
		"static final long ROTATE_RETRY_MS = 1_000;",
		"ServerServiceDefinition.builder(service + SUFFIX)",
		"pinned = p.pin(options);",
		"Status.Code.UNIMPLEMENTED",
		"return Status.fromCode(code);",
		"opening.ctx.fork().withCancellation()",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered chassis missing %q", want)
		}
	}
}
