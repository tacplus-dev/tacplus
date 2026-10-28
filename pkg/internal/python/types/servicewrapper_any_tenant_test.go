// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// Issue #170: an rpc declaring RPC_FLAG_ANY_TENANT is registered with the
// authorizer under its service-qualified key, and every authenticate() path the
// wrapper takes for it names the method, so the opened door is honoured there
// too. Every other rpc renders exactly as before.
func TestPythonServiceWrapperRegistersAnyTenant(t *testing.T) {
	outputDir := t.TempDir()
	svc := ServiceInfo{
		ProtoFile:    "crew/crew.proto",
		PackageName:  "crew",
		ServiceName:  "CrewApi",
		GrpcName:     "crew",
		IsGrpc:       true,
		IsC2S:        true,
		ParentDir:    "crew",
		AuthServices: []string{"identity"},
		Config:       &tactype.Config{OutputDir: outputDir},
	}
	method := func(name string, anyTenant bool) *parser.MethodInfo {
		return &parser.MethodInfo{
			Name: name, InputType: "Req", OutputType: "Resp",
			FullInputType: "crew.Req", FullOutputType: "crew.Resp",
			RouteField: &parser.RouteFieldDescriptor{Field: "tenant", JSONName: "tenant", Number: 1},
			AnyTenant:  anyTenant,
		}
	}
	info := &parser.ProtoInfo{
		Package:  "crew",
		Services: []*parser.ServiceInfo{{Name: "CrewApi", Methods: []*parser.MethodInfo{method("Hail", true), method("Board", false)}}},
	}
	if err := generateServiceWrapper(svc, info, map[string]service.AccountingInfo{}); err != nil {
		t.Fatalf("generateServiceWrapper: %v", err)
	}
	var rendered string
	_ = filepath.Walk(outputDir, func(path string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && strings.HasSuffix(path, ".py") {
			b, _ := os.ReadFile(path)
			rendered += string(b)
		}
		return nil
	})
	if !strings.Contains(rendered, `authorizer.set_method_any_tenant("crew.CrewApi/Hail")`) {
		t.Fatalf("ANY_TENANT rpc must be registered under its qualified key:\n%s", rendered)
	}
	if strings.Contains(rendered, `set_method_any_tenant("crew.CrewApi/Board")`) {
		t.Fatalf("default rpc must keep the door:\n%s", rendered)
	}
}

func TestPythonServiceWrapperAuthenticateNamesAnyTenantMethod(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "CrewApi"}
	render := func(m parser.MethodInfo) string {
		m.Name, m.InputType, m.OutputType = "Hail", "Req", "Resp"
		m.FullInputType, m.FullOutputType = "crew.Req", "crew.Resp"
		return generatePyGRPCWrapperMethod(svc, &m, "crew", "crew", "crew", map[string]service.AccountingInfo{})
	}
	for _, tc := range []struct {
		m    parser.MethodInfo
		want string
	}{
		{parser.MethodInfo{AuthAnyUser: true, AnyTenant: true}, `self._authenticate(context, None, '/crew.CrewApi/Hail')`},
		{parser.MethodInfo{AuthAnyUser: true, AnyTenant: true, TargetNamespaceField: "/tenants"}, `self._authenticate(context, "/tenants", '/crew.CrewApi/Hail')`},
		{parser.MethodInfo{ClientStreaming: true, TargetNamespaceField: "ns", AnyTenant: true}, `self._authenticate(context, None, '/crew.CrewApi/Hail')`},
		// Without the flag, byte-for-byte what it was.
		{parser.MethodInfo{AuthAnyUser: true}, `self._authenticate(context)`},
		{parser.MethodInfo{ClientStreaming: true, TargetNamespaceField: "ns"}, `self._authenticate(context)`},
	} {
		if got := render(tc.m); !strings.Contains(got, tc.want) {
			t.Fatalf("want %q in:\n%s", tc.want, got)
		}
	}
	if !strings.Contains(readPythonTemplate(t, "python/types/servicewrapper.py.tmpl"), "def _authenticate(self, context, target_namespace=None, full_method=None):") {
		t.Fatalf("wrapper _authenticate must carry the method to the authorizer")
	}
}
