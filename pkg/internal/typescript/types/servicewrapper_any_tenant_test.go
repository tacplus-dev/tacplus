// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// Issue #170: an rpc declaring RPC_FLAG_ANY_TENANT is registered with the
// Authorizer under its service-qualified key, and every authenticate() path the
// wrapper takes for it names the method, so the opened door holds there too.
// Every other rpc renders exactly as before.
func TestTypeScriptServiceWrapperRegistersAnyTenant(t *testing.T) {
	outputDir := t.TempDir()
	svc := ServiceInfo{
		ProtoFile: "services/crew.proto", PackageName: "crew", ServiceName: "CrewApi", GrpcName: "crew",
		IsGrpc: true, OutputDir: outputDir, ParentDir: "services", AuthServices: []string{"identity"},
		Config: &tactype.Config{OutputDir: outputDir, TsPackageBase: "crew-tacplus"},
	}
	method := func(name string, anyTenant bool) *parser.MethodInfo {
		return &parser.MethodInfo{
			Name: name, InputType: "Req", OutputType: "Resp", FullInputType: "crew.Req", FullOutputType: "crew.Resp",
			RouteField: &parser.RouteFieldDescriptor{Field: "tenant", JSONName: "tenant", Number: 1},
			AnyTenant:  anyTenant,
		}
	}
	info := &parser.ProtoInfo{
		Package:  "crew",
		Services: []*parser.ServiceInfo{{Name: "CrewApi", Methods: []*parser.MethodInfo{method("Hail", true), method("Board", false)}}},
	}
	if _, err := generateServiceWrapper(svc, info, nil, map[string]service.AccountingInfo{}); err != nil {
		t.Fatalf("generateServiceWrapper: %v", err)
	}
	rendered := readOnlyFileInDir(t, outputDir)
	for _, want := range []string{
		`authorizer.setMethodAnyTenant("crew.CrewApi/Hail");`,
		"setMethodAnyTenant(methodKey: string): void;",
		"await this.authenticateFunc(this.metadataToRecord(metadata), targetNamespace, fullMethod);",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered wrapper missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, `setMethodAnyTenant("crew.CrewApi/Board")`) {
		t.Fatalf("default rpc must keep the door:\n%s", rendered)
	}
}

func TestTypeScriptServiceWrapperAuthenticateNamesAnyTenantMethod(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "CrewApi"}
	render := func(m parser.MethodInfo) string {
		m.Name, m.InputType, m.OutputType = "Hail", "Req", "Resp"
		m.FullInputType, m.FullOutputType = "crew.Req", "crew.Resp"
		return generateTsGRPCWrapperMethod(svc, &m, "crew", "services", "crew", map[string]service.AccountingInfo{})
	}
	for _, tc := range []struct {
		m    parser.MethodInfo
		want string
	}{
		{parser.MethodInfo{AuthAnyUser: true, AnyTenant: true}, `await this.authenticate(call.metadata, undefined, '/crew.CrewApi/Hail');`},
		{parser.MethodInfo{AuthAnyUser: true, AnyTenant: true, TargetNamespaceField: "/tenants"}, `await this.authenticate(call.metadata, "/tenants", '/crew.CrewApi/Hail');`},
		{parser.MethodInfo{ClientStreaming: true, TargetNamespaceField: "ns", AnyTenant: true}, `await this.authenticate(call.metadata, undefined, '/crew.CrewApi/Hail');`},
		{parser.MethodInfo{ClientStreaming: true, ServerStreaming: true, TargetNamespaceField: "ns", AnyTenant: true}, `await this.authenticate(stream.metadata, undefined, '/crew.CrewApi/Hail');`},
		// Without the flag, byte-for-byte what it was.
		{parser.MethodInfo{AuthAnyUser: true}, `await this.authenticate(call.metadata);`},
		{parser.MethodInfo{ClientStreaming: true, TargetNamespaceField: "ns"}, `await this.authenticate(call.metadata);`},
	} {
		if got := render(tc.m); !strings.Contains(got, tc.want) {
			t.Fatalf("want %q in:\n%s", tc.want, got)
		}
	}
}
