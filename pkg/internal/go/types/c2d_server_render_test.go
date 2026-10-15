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
	if !strings.Contains(src, "c2dSrv := newC2DServer(\n\t\twrapperMixedDaemon,\n\t)") {
		t.Fatalf("c2d server must host the c2d wrapper")
	}
	if !strings.Contains(src, `grpcserver.NewC2DServer("/run/mixed.sock", &multiWrapper{impls: impls})`) {
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
