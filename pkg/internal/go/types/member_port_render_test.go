// Copyright (c) 2026 Jody Scott
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

// A (spec.member_port) service's Go ServiceManager owns its member port
// (tacplus#212, #322): the config is checked before any retry loop, the port
// starts once New holds the X509Mint certificate, and it closes with the
// manager. cross-test/memberport generates the fixture and runs the port; this
// pins the rendered wiring, and that a service without the annotation gets none.
func TestGoServiceManagerOwnsTheMemberPort(t *testing.T) {
	render := func(hasMemberPort bool) string {
		t.Helper()
		cfg := &tactype.Config{ModuleBasePath: "example.com/grid/model", SelfPackage: "grid"}
		svcs := []ServiceInfo{{ProtoFile: "svc/grid.proto", PackageName: "grid", ServiceName: "GridService", GrpcName: "grid", ParentDir: "svc", IsS2S: true, Config: cfg}}
		data, err := buildServiceManagerData(cfg, svcs, nil)
		if err != nil {
			t.Fatalf("buildServiceManagerData: %v", err)
		}
		data.HasMemberPort = hasMemberPort
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
		return string(formatted)
	}

	src := render(true)
	for _, want := range []string{
		`"example.com/grid/model/chassis/memberport"`,
		"func (sm *ServiceManager) MemberPort() *memberport.MemberPort {",
		`memberAudience := memberport.AudienceFor("grid")`,
		"func() (string, error) { return sm.identity.GetTokenFor(memberAudience) },",
		"sm.auth.JwkCache().Get,",
		"memberport.ServingCertFrom(sm.sc.TLSCertMaterial, sm.reportTLSDegraded),",
		"if err := sm.startMemberPort(); err != nil {\n\t\tsm.Close()\n\t\treturn nil, err\n\t}",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("the ServiceManager is missing %q", want)
		}
	}
	check := strings.Index(src, "if _, _, err := memberPortAddresses(cfg); err != nil {")
	retry := strings.Index(src, "for attempt := 1; ; attempt++ {")
	if check < 0 || retry < 0 || check > retry {
		t.Fatalf("grpcconfig.member_port must be checked before the init retry loop (check at %d, loop at %d)", check, retry)
	}
	if strings.Count(src, "sm.memberPort.Close()") != 2 {
		t.Fatalf("the member port must close with Close and with Run's shutdown")
	}

	if plain := render(false); strings.Contains(plain, "memberport") || strings.Contains(plain, "MemberPort") {
		t.Fatalf("a service without (spec.member_port) gets no member port")
	}
}
