// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// tacplus#323: the TypeScript C2S client carries each long-lived stream of a
// token-carrying service across token lifetimes on the <Svc>Rotating sibling
// (make-before-break, the servicemanager chassis' Rotation.ts), falling back
// to the plain method on a server without it. The caller's surface does not
// change: the same method, the same return type.
func TestTypeScriptClientStreamsRotate(t *testing.T) {
	methods := []*parser.MethodInfo{
		{Name: "GenerateTicks", InputType: "Tick", FullInputType: "svc.Tick", OutputType: "Tick", FullOutputType: "svc.Tick", ServerStreaming: true},
		{Name: "SendTicks", InputType: "Tick", FullInputType: "svc.Tick", OutputType: "Tick", FullOutputType: "svc.Tick", ClientStreaming: true},
		{Name: "AssistTicks", InputType: "Tick", FullInputType: "svc.Tick", OutputType: "Tick", FullOutputType: "svc.Tick", ClientStreaming: true, ServerStreaming: true},
	}
	svc := &parser.ServiceInfo{Name: "TickerService", IsC2S: true, Methods: methods}
	for _, m := range methods {
		got, err := generateTsGrpcClientMethod(svc, m, "svc")
		if err != nil {
			t.Fatalf("%s: %v", m.Name, err)
		}
		out := got.method
		for _, want := range []string{
			"openRotatingStream<",
			"path: '/svc.TickerService/" + m.Name + "'",
			"tokens: ",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: the rotating open is missing %q:\n%s", m.Name, want, out)
			}
		}
		// The fallback: the plain method on the same client.
		plain := map[string]string{"GenerateTicks": "makeServerStreamRequest", "SendTicks": "makeClientStreamRequest", "AssistTicks": "makeBidiStreamRequest"}[m.Name]
		if !strings.Contains(out, "client."+plain+"<") {
			t.Errorf("%s: no fallback to the plain %s on the same client:\n%s", m.Name, plain, out)
		}
		if strings.Index(out, "openRotatingStream<") > strings.Index(out, "client."+plain+"<") {
			t.Errorf("%s: the plain method must be the fallback, after the rotating open:\n%s", m.Name, out)
		}
	}
	server, _ := generateTsGrpcClientMethod(svc, methods[0], "svc")
	if !strings.Contains(server.method, "Promise<grpc.ClientReadableStream<types.Tick>>") && !strings.Contains(server.method, "Promise<grpc.ClientReadableStream<Tick>>") {
		t.Errorf("the server-streaming method's return type changed:\n%s", server.method)
	}
	client, _ := generateTsGrpcClientMethod(svc, methods[1], "svc")
	if !strings.Contains(client.method, "clientStreamCall(") {
		t.Errorf("the client-streaming method does not take its response from the rotating stream:\n%s", client.method)
	}
}

// A stream with no token has nothing to rotate onto: an auth-disabled method,
// an auth-disabled or c2d service. It stays on the plain method.
func TestTypeScriptClientStreamsWithoutTokensDoNotRotate(t *testing.T) {
	bidi := &parser.MethodInfo{Name: "AssistTicks", InputType: "Tick", FullInputType: "svc.Tick", OutputType: "Tick", FullOutputType: "svc.Tick", ClientStreaming: true, ServerStreaming: true}
	open := &parser.MethodInfo{Name: "Watch", InputType: "Tick", FullInputType: "svc.Tick", OutputType: "Tick", FullOutputType: "svc.Tick", ServerStreaming: true, AuthDisabled: true}
	cases := []struct {
		name string
		svc  *parser.ServiceInfo
		m    *parser.MethodInfo
	}{
		{"auth-disabled method", &parser.ServiceInfo{Name: "TickerService", IsC2S: true, Methods: []*parser.MethodInfo{open}}, open},
		{"auth-disabled service", &parser.ServiceInfo{Name: "TickerService", IsC2S: true, AuthDisabled: true, Methods: []*parser.MethodInfo{bidi}}, bidi},
		{"c2d service", &parser.ServiceInfo{Name: "TickerService", IsC2D: true, Methods: []*parser.MethodInfo{bidi}}, bidi},
	}
	for _, c := range cases {
		got, err := generateTsGrpcClientMethod(c.svc, c.m, "svc")
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if strings.Contains(got.method, "openRotatingStream") {
			t.Errorf("%s: rotates a stream that carries no token:\n%s", c.name, got.method)
		}
	}
}
