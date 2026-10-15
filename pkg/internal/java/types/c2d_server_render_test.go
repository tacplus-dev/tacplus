// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

// The ServiceManager hosts service_c2d services on a separate unix-socket
// server: never on the TLS listener, never with token auth wired. The c2d-only
// shape is exercised end to end by cross-test/c2d-server; this pins the MIXED
// shape (TLS + c2d in one service), which that fixture does not generate.
// Go twin: c2d_server_render_test.go under go/types.
func TestJavaServiceManagerHostsC2DOnItsOwnSocket(t *testing.T) {
	sm := renderJavaTemplateForTest(t, "ServiceManager.java.tmpl", javaSmTemplateData{
		PackageBase:   "com.example",
		ChassisBase:   "com.example.chassis",
		TypesBase:     "com.example.types",
		SmPrefix:      "s2sm",
		ServiceLabel:  "mixed",
		HasGrpc:       true,
		HasTlsGrpc:    true,
		HasC2DGrpc:    true,
		C2DSocketPath: "/run/mixed.sock",
		GrpcServices: []javaGrpcServiceEntry{
			{GetterName: "getMixedApiGrpcImpl", ServiceName: "MixedApi", ServerClassName: "MixedApi", WrapperVarName: "wrapperMixedApi", WrapperPackage: "com.example.s2sm.internal.grpc.servers.svc.mixed", HasMethodAuth: true},
			{GetterName: "getMixedDaemonGrpcImpl", ServiceName: "MixedDaemon", ServerClassName: "MixedDaemon", WrapperVarName: "wrapperMixedDaemon", WrapperPackage: "com.example.s2sm.internal.grpc.servers.svc.mixed", IsC2D: true, HasMethodAuth: true},
		},
	})

	tlsImpl := sm[strings.Index(sm, "ServiceImplementation multiImpl = new ServiceImplementation() {"):]
	tlsImpl = tlsImpl[:strings.Index(tlsImpl, "handler.additionalServices()")]
	if !strings.Contains(tlsImpl, "grpcServerBuilder.addService(wrapperMixedApi);") || strings.Contains(tlsImpl, "wrapperMixedDaemon") {
		t.Fatalf("TLS server must host MixedApi and not the c2d service:\n%s", tlsImpl)
	}
	for _, want := range []string{
		"C2DServer c2dSrv = newC2DServer(new io.grpc.BindableService[] {\n                wrapperMixedDaemon,\n        });",
		`return new C2DServer("/run/mixed.sock", new ServiceImplementation() {`,
		"c2dSrv.start();",
		"c2dSrv.shutdown();",
		"srv.listen();",
		"new MixedApi(handler.getMixedApiGrpcImpl(), authorizer);",
		"new MixedDaemon(handler.getMixedDaemonGrpcImpl());",
		"MixedApi.registerMethodAuth(authorizer);",
	} {
		if !strings.Contains(sm, want) {
			t.Fatalf("ServiceManager missing %q", want)
		}
	}
	for _, unwanted := range []string{
		"MixedDaemon.registerMethodAuth(authorizer);",
		"wrapperMixedDaemon.setAuthenticateFunc(",
	} {
		if strings.Contains(sm, unwanted) {
			t.Fatalf("token auth must not be wired on the c2d service: found %q", unwanted)
		}
	}
}
