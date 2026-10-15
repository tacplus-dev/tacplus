// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"go/format"
	"strings"
	"testing"
	"text/template"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// The ServiceManager hosts service_c2d services on a separate unix-socket
// server: never on the TLS listener, never with token auth wired. The c2d-only
// shape is exercised end to end by cross-test/c2d-server; this pins the MIXED
// shape (TLS + c2d in one service), which that fixture does not generate.

func c2dTestServices(cfg *tactype.Config, c2dSocket string) []ServiceInfo {
	return []ServiceInfo{
		{ProtoFile: "svc/mixed.proto", PackageName: "mixed", ServiceName: "MixedApi", GrpcName: "mixed", ParentDir: "svc", IsS2S: true, Config: cfg},
		{ProtoFile: "svc/mixed.proto", PackageName: "mixed", ServiceName: "MixedDaemon", GrpcName: "mixed", ParentDir: "svc", IsC2D: true, C2DSocketPath: c2dSocket, Config: cfg},
	}
}

func TestGoServiceManagerHostsC2DOnItsOwnSocket(t *testing.T) {
	cfg := &tactype.Config{ModuleBasePath: "example.com/mixed/model", SelfPackage: "mixed"}
	data, err := buildServiceManagerData(cfg, c2dTestServices(cfg, "/run/mixed.sock"), nil)
	if err != nil {
		t.Fatalf("buildServiceManagerData: %v", err)
	}
	if !data.HasTlsGrpc || !data.HasC2DGrpc || data.C2DSocketPath != "/run/mixed.sock" {
		t.Fatalf("HasTlsGrpc=%v HasC2DGrpc=%v C2DSocketPath=%q", data.HasTlsGrpc, data.HasC2DGrpc, data.C2DSocketPath)
	}

	tmplContent, err := loadTemplate("service_manager.go.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	tmpl, err := template.New("servicemanager").Funcs(header.Funcs()).Parse(tmplContent)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("execute: %v", err)
	}
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		t.Fatalf("format: %v", err)
	}
	src := string(formatted)

	tlsImpls := src[strings.Index(src, "impls: []grpcserver.ServiceImplementation{"):]
	tlsImpls = tlsImpls[:strings.Index(tlsImpls, "}")]
	if !strings.Contains(tlsImpls, "wrapperMixedApi,") || strings.Contains(tlsImpls, "wrapperMixedDaemon") {
		t.Fatalf("TLS server must host MixedApi and not the c2d service:\n%s", tlsImpls)
	}
	if !strings.Contains(src, "c2dSrv := newC2DServer(C2DSocketPath,\n\t\twrapperMixedDaemon,\n\t)") {
		t.Fatalf("c2d server must host the c2d wrapper on the declared socket")
	}
	if !strings.Contains(src, `const C2DSocketPath = "/run/mixed.sock"`) {
		t.Fatalf("c2d server must bind the declared socket_path")
	}
	if strings.Contains(src, "wrapperMixedDaemon.SetAuthorizeFunc") || !strings.Contains(src, "wrapperMixedApi.SetAuthorizeFunc") {
		t.Fatalf("token auth must be wired on the TLS service only")
	}
}

func TestGoC2DSocketPathIsRequiredAndSingular(t *testing.T) {
	cfg := &tactype.Config{ModuleBasePath: "example.com/mixed/model", SelfPackage: "mixed"}
	if _, err := buildServiceManagerData(cfg, c2dTestServices(cfg, ""), nil); err == nil || !strings.Contains(err.Error(), "requires socket_path") {
		t.Fatalf("missing socket_path: err = %v", err)
	}
	svcs := append(c2dTestServices(cfg, "/run/a.sock"), ServiceInfo{
		ProtoFile: "svc/mixed.proto", PackageName: "mixed", ServiceName: "OtherDaemon", GrpcName: "mixed", ParentDir: "svc", IsC2D: true, C2DSocketPath: "/run/b.sock", Config: cfg,
	})
	if _, err := buildServiceManagerData(cfg, svcs, nil); err == nil || !strings.Contains(err.Error(), "differs from") {
		t.Fatalf("conflicting socket_path: err = %v", err)
	}
}

// The standalone c2d host (ServeC2DStandalone) is emitted for a c2d-only,
// cluster-free build and for nothing else; cross-test/c2d-server runs it.
func TestGoServeC2DStandaloneOnlyForClusterFreeC2DOnlyBuild(t *testing.T) {
	render := func(cfg *tactype.Config, svcs []ServiceInfo) (serviceManagerTemplateData, string) {
		t.Helper()
		data, err := buildServiceManagerData(cfg, svcs, nil)
		if err != nil {
			t.Fatalf("buildServiceManagerData: %v", err)
		}
		tmplContent, err := loadTemplate("service_manager.go.tmpl")
		if err != nil {
			t.Fatal(err)
		}
		tmpl, err := template.New("servicemanager").Funcs(header.Funcs()).Parse(tmplContent)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			t.Fatalf("execute: %v", err)
		}
		formatted, err := format.Source(buf.Bytes())
		if err != nil {
			t.Fatalf("format: %v", err)
		}
		return data, string(formatted)
	}

	cfg := &tactype.Config{ModuleBasePath: "example.com/mixed/model", SelfPackage: "mixed"}
	daemonOnly := c2dTestServices(cfg, "/run/mixed.sock")[1:]
	data, src := render(cfg, daemonOnly)
	if !data.HostsC2DStandalone {
		t.Fatal("c2d-only, cluster-free build must get the standalone host")
	}
	if !strings.Contains(src, "func ServeC2DStandalone(ctx context.Context, socketPath string, h Handler) error {\n\tsrv := newC2DServer(socketPath,\n\t\tmixed_fe.NewMixedDaemon(h.MixedDaemonGrpcImpl()),\n\t)") {
		t.Fatalf("ServeC2DStandalone must mount the generated c2d wrapper through newC2DServer:\n%s", src[strings.Index(src, "func ServeC2DStandalone"):])
	}

	if _, src := render(cfg, c2dTestServices(cfg, "/run/mixed.sock")); strings.Contains(src, "ServeC2DStandalone") {
		t.Fatal("a build that also hosts a TLS service keeps Run only")
	}
	withState := &tactype.Config{ModuleBasePath: "example.com/mixed/model", SelfPackage: "mixed", AllStates: true}
	if _, src := render(withState, c2dTestServices(withState, "/run/mixed.sock")[1:]); strings.Contains(src, "ServeC2DStandalone") {
		t.Fatal("a c2d-only build that needs state keeps Run only")
	}
}
