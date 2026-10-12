// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
)

// The gateway (platform/services/gateway spa.rs init_bootstrap("")) injects
// window.__SCHEMATIC__ = {"gatewayUrl": ""}, where "" means same origin. The
// default ServiceManager constructor read window.__TACPLUS__ and rejected a
// falsy gatewayUrl, so it could never work behind the real gateway.
func TestTypeScriptW2SMReadsGatewayBootstrapAndAcceptsSameOrigin(t *testing.T) {
	tmpl, err := loadTemplate("w2sm.ts.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}

	for _, want := range []string{
		"this.config = config ?? (globalThis as any).__SCHEMATIC__;",
		"if (typeof this.config?.gatewayUrl !== 'string') {",
		"expected window.__SCHEMATIC__.gatewayUrl (empty string = same origin)",
		// Relative (same-origin) URLs must resolve against the page for WebSocket.
		"new URL(this.absoluteHttpUrl(url), (globalThis as any).location?.href).href",
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("w2sm.ts.tmpl missing %q", want)
		}
	}

	for _, banned := range []string{
		"__TACPLUS__",
		"if (!this.config?.gatewayUrl) {",
	} {
		if strings.Contains(tmpl, banned) {
			t.Errorf("w2sm.ts.tmpl still carries %q", banned)
		}
	}
}

func TestTypeScriptWarnsWhenPkgDeclaresNoW2SClients(t *testing.T) {
	cases := []struct {
		name     string
		config   *tactype.Config
		w2s      []service.ServiceInfo
		wantWarn bool
	}{
		{"no w2s edges", &tactype.Config{SelfPackage: "web"}, nil, true},
		{"w2s services generated", &tactype.Config{SelfPackage: "web"}, []service.ServiceInfo{{ServiceName: "Dashboard"}}, false},
		{"c2s-only client", &tactype.Config{SelfPackage: "cli", GrpcC2SClients: []tactype.GrpcEntry{{ServiceName: "Dashboard"}}}, nil, false},
		{"no pkg", &tactype.Config{}, nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zapcore.WarnLevel)
			restore := zap.L()
			zap.ReplaceGlobals(zap.New(core))
			defer zap.ReplaceGlobals(restore)

			warnNoW2SClients(tc.config, tc.w2s)

			warned := logs.FilterMessage("pkg web declares no grpc_w2s_client edges; no w2sm/ generated").Len() == 1
			if warned != tc.wantWarn || (!tc.wantWarn && logs.Len() != 0) {
				t.Fatalf("wantWarn=%v, got logs %v", tc.wantWarn, logs.All())
			}
		})
	}
}
