// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// tacplus#323: the Python C2S client carries each long-lived stream of a
// token-carrying service across token lifetimes on the <Svc>Rotating sibling
// (the servicemanager chassis' rotation.py), falling back to the plain method,
// on the same channel, on a server without it. The caller's surface does not
// change.
func TestPythonClientStreamsRotate(t *testing.T) {
	methods := []*parser.MethodInfo{
		{Name: "GenerateTicks", InputType: "Tick", FullInputType: "svc.Tick", OutputType: "Tick", FullOutputType: "svc.Tick", ServerStreaming: true},
		{Name: "SendTicks", InputType: "Tick", FullInputType: "svc.Tick", OutputType: "Tick", FullOutputType: "svc.Tick", ClientStreaming: true},
		{Name: "AssistTicks", InputType: "Tick", FullInputType: "svc.Tick", OutputType: "Tick", FullOutputType: "svc.Tick", ClientStreaming: true, ServerStreaming: true},
	}
	svc := &parser.ServiceInfo{Name: "TickerService", IsC2S: true, Methods: methods}
	for _, m := range methods {
		out := generatePyGrpcClientMethod(svc, m, "svc")
		for _, want := range []string{
			"rotating = await open_rotating_stream(",
			"'/svc.TickerService/" + m.Name + "'",
			"pb.Tick.SerializeToString",
			"pb.Tick.FromString",
			"self._token_provider",
			"if rotating is not None:",
			"pb_grpc.TickerServiceStub(channel)." + m.Name + "(",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s: missing %q:\n%s", m.Name, want, out)
			}
		}
		if strings.Index(out, "open_rotating_stream(") > strings.Index(out, "pb_grpc.TickerServiceStub(channel).") {
			t.Errorf("%s: the plain method must be the fallback, after the rotating open:\n%s", m.Name, out)
		}
	}
}

// A stream with no token has nothing to rotate onto.
func TestPythonClientStreamsWithoutTokensDoNotRotate(t *testing.T) {
	bidi := &parser.MethodInfo{Name: "AssistTicks", InputType: "Tick", FullInputType: "svc.Tick", OutputType: "Tick", FullOutputType: "svc.Tick", ClientStreaming: true, ServerStreaming: true}
	open := &parser.MethodInfo{Name: "Watch", InputType: "Tick", FullInputType: "svc.Tick", OutputType: "Tick", FullOutputType: "svc.Tick", ServerStreaming: true, AuthDisabled: true}
	for name, c := range map[string]struct {
		svc *parser.ServiceInfo
		m   *parser.MethodInfo
	}{
		"auth-disabled method":  {&parser.ServiceInfo{Name: "TickerService", IsC2S: true}, open},
		"auth-disabled service": {&parser.ServiceInfo{Name: "TickerService", IsC2S: true, AuthDisabled: true}, bidi},
		"c2d service":           {&parser.ServiceInfo{Name: "TickerService", IsC2D: true}, bidi},
	} {
		if out := generatePyGrpcClientMethod(c.svc, c.m, "svc"); strings.Contains(out, "open_rotating_stream") {
			t.Errorf("%s: rotates a stream that carries no token:\n%s", name, out)
		}
	}
}
