// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	protogen "github.com/tacplus-dev/tacplus/pkg/internal/proto"
)

// targetLayerProto is an S2S service of package pkg with serviceOption in its
// (spec.service).
func targetLayerProto(pkg, svc, serviceOption string) string {
	return `syntax = "proto3";

package ` + pkg + `;

import "sys/spec.proto";

option (spec.service) = {name: "` + pkg + `"` + serviceOption + `};

message PingRequest {}
message PingResponse {}

service ` + svc + ` {
  option (spec.service_s2s) = {auth_services: ["*"]};

  rpc Ping(PingRequest) returns (PingResponse);
}
`
}

// #475: a generated S2S client carries its target's layer, "tenant" or
// "product:<name>", for TargetLayer.check; a /system target admits every
// layer, so its client carries none, and a C2S, W2S or C2D client carries a
// user's token, no S2S edge.
func TestJavaGrpcClientTargetLayer(t *testing.T) {
	protoDir := t.TempDir()
	if err := protogen.CopySysProtos(protoDir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	servicesDir := filepath.Join(protoDir, "services")
	if err := os.MkdirAll(servicesDir, 0o755); err != nil {
		t.Fatalf("mkdir services: %v", err)
	}
	for name, src := range map[string]string{
		"core.proto":    targetLayerProto("core", "Core", ""),
		"billing.proto": targetLayerProto("billing", "Billing", `, product: "ledger"`),
		"crew.proto":    targetLayerProto("crew", "Crew", `, tenant_owned: true`),
	} {
		if err := os.WriteFile(filepath.Join(servicesDir, name), []byte(src), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if _, _, _, err := protogen.ScanAndGenerateConfigHolder(protoDir, protoDir); err != nil {
		t.Fatalf("ScanAndGenerateConfigHolder: %v", err)
	}
	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	for _, tc := range []struct{ prefix, ref, want string }{
		{"s2sm", "core.Core", ""},
		{"s2sm", "billing.Billing", "product:ledger"},
		{"s2sm", "crew.Crew", "tenant"},
		{"s2sm", "absent.Absent", ""},
		{"c2sm", "billing.Billing", ""},
		{"w2sm", "crew.Crew", ""},
		{"c2dm", "billing.Billing", ""},
	} {
		if got := javaGrpcClientTargetLayer(tc.prefix, dp, tc.ref); got != tc.want {
			t.Errorf("javaGrpcClientTargetLayer(%s, %q) = %q, want %q", tc.prefix, tc.ref, got, tc.want)
		}
	}
	if got := javaGrpcClientTargetLayer("s2sm", nil, "crew.Crew"); got != "" {
		t.Errorf("javaGrpcClientTargetLayer without protos = %q, want empty", got)
	}
}

// A client with a target layer guards the channel of every stub it calls
// through, the blocking one (unary and blocking server streams) and the async
// one (streams), outside the rotation so the guard checks the stream's open;
// a client without one is exactly what it was.
func TestJavaGrpcClientGuardsTargetLayer(t *testing.T) {
	tmplStr, err := loadTemplate("grpcclient.java.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	render := func(layer string, rotates bool) string {
		t.Helper()
		var b strings.Builder
		tmpl := template.Must(template.New("grpcclient").Parse(tmplStr))
		if err := tmpl.Execute(&b, map[string]any{
			"ClientName": "BillingGrpcClient", "ServiceName": "Billing",
			"HasUnary": true, "HasStreaming": true, "Rotates": rotates,
			"RotationFQN": "x.chassis.rotation.StreamRotation", "TargetLayer": layer,
		}); err != nil {
			t.Fatal(err)
		}
		return b.String()
	}
	for _, rotates := range []bool{false, true} {
		got := render("product:ledger", rotates)
		async := `this.asyncStub = BillingGrpc.newStub(TargetLayer.guard(channel, "product:ledger", tokenProvider));`
		if rotates {
			async = `this.asyncStub = BillingGrpc.newStub(TargetLayer.guard(x.chassis.rotation.StreamRotation.channel(channel, tokenProvider), "product:ledger", tokenProvider));`
		}
		for _, want := range []string{
			`this.blockingStub = BillingGrpc.newBlockingStub(TargetLayer.guard(channel, "product:ledger", tokenProvider));`,
			async,
			"this.channel = channel;",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("a product-target client (rotates %v) missing %q:\n%s", rotates, want, got)
			}
		}
		if plain := render("", rotates); strings.Contains(plain, "TargetLayer") {
			t.Errorf("a /system-target client (rotates %v) must carry no layer guard:\n%s", rotates, plain)
		}
	}
	if got := render("tenant", false); !strings.Contains(got, `TargetLayer.guard(channel, "tenant", tokenProvider)`) {
		t.Errorf("a tenant-target client must guard with \"tenant\":\n%s", got)
	}
}
