// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
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
		"C2DServer c2dSrv = newC2DServer(C2D_SOCKET_PATH, new io.grpc.BindableService[] {\n                wrapperMixedDaemon,\n        });",
		`public static final String C2D_SOCKET_PATH = "/run/mixed.sock";`,
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

// The standalone c2d host (serveC2DStandalone) is emitted for a c2d-only,
// cluster-free build and for nothing else; cross-test/c2d-server runs it.
// Go twin: TestGoServeC2DStandaloneOnlyForClusterFreeC2DOnlyBuild.
func TestJavaServeC2DStandaloneOnlyForClusterFreeC2DOnlyBuild(t *testing.T) {
	svcs := func(cfg *tactype.Config) []ServiceInfo {
		return []ServiceInfo{
			{ProtoFile: "svc/mixed.proto", PackageName: "mixed", ServiceName: "MixedApi", GrpcName: "mixed", ParentDir: "svc", IsGrpc: true, IsS2S: true, Config: cfg},
			{ProtoFile: "svc/mixed.proto", PackageName: "mixed", ServiceName: "MixedDaemon", GrpcName: "mixed", ParentDir: "svc", IsGrpc: true, IsC2D: true, C2DSocketPath: "/run/mixed.sock", Config: cfg},
		}
	}
	hosts := func(cfg *tactype.Config, s []ServiceInfo) bool {
		t.Helper()
		data, err := buildJavaSmTemplateData(cfg, s, nil, "", nil)
		if err != nil {
			t.Fatalf("buildJavaSmTemplateData: %v", err)
		}
		return data.HostsC2DStandalone
	}
	cfg := &tactype.Config{JavaPackageBase: "com.example", SelfPackage: "mixed"}
	if !hosts(cfg, svcs(cfg)[1:]) {
		t.Fatal("c2d-only, cluster-free build must get the standalone host")
	}
	if hosts(cfg, svcs(cfg)) {
		t.Fatal("a build that also hosts a TLS service keeps run only")
	}
	withState := &tactype.Config{JavaPackageBase: "com.example", SelfPackage: "mixed", AllStates: true}
	if hosts(withState, svcs(withState)[1:]) {
		t.Fatal("a c2d-only build that needs state keeps run only")
	}

	data := javaSmTemplateData{
		PackageBase: "com.example", ChassisBase: "com.example.chassis", TypesBase: "com.example.types",
		SmPrefix: "s2sm", ServiceLabel: "mixed", HasGrpc: true, HasC2DGrpc: true, C2DSocketPath: "/run/mixed.sock",
		GrpcServices: []javaGrpcServiceEntry{
			{GetterName: "getMixedDaemonGrpcImpl", ServiceName: "MixedDaemon", ServerClassName: "MixedDaemon", WrapperVarName: "wrapperMixedDaemon", WrapperPackage: "com.example.s2sm.internal.grpc.servers.svc.mixed", IsC2D: true},
		},
	}
	if sm := renderJavaTemplateForTest(t, "ServiceManager.java.tmpl", data); strings.Contains(sm, "serveC2DStandalone") {
		t.Fatal("serveC2DStandalone rendered without HostsC2DStandalone")
	}
	data.HostsC2DStandalone = true
	sm := renderJavaTemplateForTest(t, "ServiceManager.java.tmpl", data)
	if !strings.Contains(sm, "public static void serveC2DStandalone(String socketPath, Handler handler, java.util.concurrent.Future<?> stop) throws Exception {\n        C2DServer c2dSrv = newC2DServer(socketPath, new io.grpc.BindableService[] {\n                new MixedDaemon(handler.getMixedDaemonGrpcImpl()),\n            });") {
		t.Fatalf("serveC2DStandalone must mount the generated c2d wrapper through newC2DServer:\n%s", sm[strings.Index(sm, "serveC2DStandalone"):])
	}
}
