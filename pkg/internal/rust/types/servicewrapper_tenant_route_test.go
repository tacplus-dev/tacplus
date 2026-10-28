// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// Issue #170: the tenant-owned instance's server-side backstop. For a routed
// rpc the generated wrapper refuses, after request validation and before the
// handler, a request whose route field is not a tid or names another tenant.

func crewRouteField() *parser.RouteFieldDescriptor {
	return &parser.RouteFieldDescriptor{Field: "target_tenant", JSONName: "targetTenant", Number: 2}
}

func crewMethod(name string, client, server bool) *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:            name,
		InputType:       "CrewRequest",
		OutputType:      "CrewResponse",
		FullInputType:   "crew.CrewRequest",
		FullOutputType:  "crew.CrewResponse",
		ClientStreaming: client,
		ServerStreaming: server,
		RouteField:      crewRouteField(),
	}
}

func crewSanitizer() *rustEgressSanitizer {
	return newRustEgressSanitizer([]model.ProtoFile{{Package: "crew", Messages: []model.Message{
		{Name: "CrewRequest", Fields: []model.Field{
			{Name: "id", Type: "string", Number: 1},
			{Name: "target_tenant", Type: "string", Number: 2},
		}},
		{Name: "CrewResponse", Fields: []model.Field{{Name: "id", Type: "string", Number: 1}}},
	}}})
}

func renderCrewWrapper(t *testing.T, m *parser.MethodInfo) string {
	t.Helper()
	svc := &parser.ServiceInfo{Name: "CrewService", IsS2S: true, AuthServices: []string{"*"}}
	return mustGenerateRustGRPCWrapper(t, svc, m, "crew", "services", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, crewSanitizer())
}

func assertOrdered(t *testing.T, got string, parts ...string) {
	t.Helper()
	last := -1
	for _, p := range parts {
		idx := strings.Index(got, p)
		if idx < 0 {
			t.Fatalf("missing %q:\n%s", p, got)
		}
		if idx < last {
			t.Fatalf("%q is out of order (want %q):\n%s", p, parts, got)
		}
		last = idx
	}
}

func TestRustRoutedUnaryWrapperChecksTheRouteAfterValidation(t *testing.T) {
	got := renderCrewWrapper(t, crewMethod("GetCrew", false, false))
	assertOrdered(t, got,
		"req.validate()",
		`self.check_route_tenant("target_tenant", &req.target_tenant)?;`,
		"self.handler.get_crew(")
}

func TestRustRoutedServerStreamWrapperChecksTheRouteBeforeTheHandler(t *testing.T) {
	got := renderCrewWrapper(t, crewMethod("WatchCrew", false, true))
	assertOrdered(t, got,
		"request.get_ref().validate()",
		`self.check_route_tenant("target_tenant", &request.get_ref().target_tenant)?;`,
		"self.handler.watch_crew(")
}

// A request stream is not one request: every inbound message is checked, after
// it is validated, and a stream that names another tenant mid-way fails.
func TestRustRoutedRequestStreamWrapperChecksEveryMessage(t *testing.T) {
	for _, m := range []*parser.MethodInfo{crewMethod("UploadCrew", true, false), crewMethod("ChatCrew", true, true)} {
		got := renderCrewWrapper(t, m)
		assertOrdered(t, got,
			"msg.validate()",
			"let __route_wrapper = self.clone();",
			`__route_wrapper.check_route_tenant("target_tenant", &msg.target_tenant)?;`,
			"self.handler."+prostSnakeName(m.Name)+"(")
	}
}

// A platform service's rpc carries no route field and no backstop.
func TestRustPlatformWrapperHasNoRouteBackstop(t *testing.T) {
	for _, m := range []*parser.MethodInfo{crewMethod("GetCrew", false, false), crewMethod("WatchCrew", false, true), crewMethod("UploadCrew", true, false)} {
		m.RouteField = nil
		if got := renderCrewWrapper(t, m); strings.Contains(got, "check_route_tenant") {
			t.Fatalf("%s: a platform rpc must not carry the route backstop:\n%s", m.Name, got)
		}
	}
}

// Every authorizer check the wrapper makes names its rpc, so the tenant door
// can follow that rpc's RPC_FLAG_ANY_TENANT on every path: the any-user
// authenticate, the fixed-namespace and the target_namespace_field checks.
func TestRustWrapperAuthChecksNameTheirRpc(t *testing.T) {
	full := "/services/crew.CrewService/GetCrew"
	anyUser := crewMethod("GetCrew", false, false)
	anyUser.AuthAnyUser = true
	if got := renderCrewWrapper(t, anyUser); !strings.Contains(got, `self.enforce_authenticated("`+full+`", &request).await?;`) {
		t.Fatalf("auth_any_user must authenticate for its rpc:\n%s", got)
	}
	targeted := crewMethod("GetCrew", false, false)
	targeted.TargetNamespaceField = "namespace"
	if got := renderCrewWrapper(t, targeted); !strings.Contains(got, `self.enforce_at_or_below("`+full+`", &request, &request.get_ref().namespace).await?;`) {
		t.Fatalf("target_namespace_field must check at-or-below for its rpc:\n%s", got)
	}
	streamed := crewMethod("UploadCrew", true, false)
	streamed.TargetNamespaceField = "namespace"
	if got := renderCrewWrapper(t, streamed); !strings.Contains(got, `wrapper.enforce_at_or_below_authorization("/services/crew.CrewService/UploadCrew", authorization.as_deref(), &msg.namespace).await?;`) {
		t.Fatalf("a request stream's at-or-below check must name its rpc:\n%s", got)
	}
}

func generateCrewServiceWrapper(t *testing.T, methods ...*parser.MethodInfo) string {
	t.Helper()
	target := &parser.ServiceInfo{Name: "CrewService", IsS2S: true, AuthServices: []string{"*"}, Methods: methods}
	outputDir := t.TempDir()
	svc := ServiceInfo{
		ProtoFile: "services/crew.proto", PackageName: "crew", ServiceName: "CrewService", GrpcName: "crew",
		IsGrpc: true, IsS2S: true, AuthServices: []string{"*"}, OutputDir: outputDir, ParentDir: "services",
		Config: &tactype.Config{OutputDir: outputDir},
	}
	info := &parser.ProtoInfo{Package: "crew", Services: []*parser.ServiceInfo{target}}
	if err := generateServiceWrapper(svc, info, map[string]string{}, map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, crewSanitizer()); err != nil {
		t.Fatalf("generateServiceWrapper: %v", err)
	}
	entries, err := os.ReadDir(outputDir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("expected one generated wrapper, got %v (%v)", entries, err)
	}
	b, err := os.ReadFile(filepath.Join(outputDir, entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// RPC_FLAG_ANY_TENANT is registered with the authorizer exactly as
// auth_services and auth_actors are; an rpc without it registers nothing, so
// the default door ("members of the instance's tenant only") holds for it.
func TestRustWrapperRegistersAnyTenantRpcs(t *testing.T) {
	open := crewMethod("GetCrew", false, false)
	open.AnyTenant = true
	got := generateCrewServiceWrapper(t, open, crewMethod("PutCrew", false, false))
	if !strings.Contains(got, `auth.set_method_any_tenant("crew.CrewService/GetCrew");`) {
		t.Fatalf("an ANY_TENANT rpc must be registered with the authorizer:\n%s", got)
	}
	if strings.Contains(got, `set_method_any_tenant("crew.CrewService/PutCrew")`) {
		t.Fatalf("a default rpc must keep the tenant door:\n%s", got)
	}
}

// The routed wrapper holds the tenant its instance serves (set by the
// ServiceManager from the instance's own namespace) and checks against it; a
// platform wrapper carries none of it.
func TestRustRoutedWrapperCarriesItsHostTenant(t *testing.T) {
	routed := generateCrewServiceWrapper(t, crewMethod("GetCrew", false, false))
	for _, want := range []string{
		"pub fn set_host_namespace(&self, namespace: &str) {",
		"servicemanager::tenant_route::host_tenant_of_namespace(namespace)",
		"fn check_route_tenant(&self, field: &str, value: &str) -> Result<(), tonic::Status> {",
		"servicemanager::tenant_route::check_routed_tenant(field, value, &host)",
	} {
		if !strings.Contains(routed, want) {
			t.Fatalf("routed wrapper missing %q:\n%s", want, routed)
		}
	}
	m := crewMethod("GetCrew", false, false)
	m.RouteField = nil
	if platform := generateCrewServiceWrapper(t, m); strings.Contains(platform, "host_tenant") {
		t.Fatalf("a platform wrapper must not carry a host tenant:\n%s", platform)
	}
}
