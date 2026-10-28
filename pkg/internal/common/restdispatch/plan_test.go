// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package restdispatch

import (
	"os"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	protogen "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// w2sValidatePlan resolves the plan the w2s-validate cross-test fixture
// yields: two C2S services (catalog, signin) and the self S2S service.
func w2sValidatePlan(t *testing.T) *Plan {
	t.Helper()
	protoDir := t.TempDir()
	if err := os.CopyFS(protoDir, os.DirFS("../../../../cross-test/w2s-validate/proto")); err != nil {
		t.Fatalf("copy fixture: %v", err)
	}
	if err := protogen.CopySysProtos(protoDir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	if _, _, _, err := protogen.ScanAndGenerateConfigHolder(protoDir, protoDir); err != nil {
		t.Fatalf("ScanAndGenerateConfigHolder: %v", err)
	}
	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	services := []Service{
		{ProtoFile: "services/catalog.proto", PackageName: "catalog", ServiceName: "CatalogService", ParentDir: "services"},
		{ProtoFile: "services/signin.proto", PackageName: "signin", ServiceName: "SigninService", ParentDir: "services"},
		{ProtoFile: "svc/w2svalidate.proto", PackageName: "w2svalidate", ServiceName: "W2sValidateService", ParentDir: "svc", IsS2S: true},
	}
	plan, err := Collect(dp, &tactype.Config{}, services, protoDir)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}
	return plan
}

func findRoute(t *testing.T, plan *Plan, grpcPath string) Route {
	t.Helper()
	for _, r := range plan.Routes {
		if r.GrpcPath == grpcPath {
			return r
		}
	}
	t.Fatalf("no route for %s in %+v", grpcPath, plan.Routes)
	return Route{}
}

func TestCollectPlansRoutesAndArmsForC2SServicesOnly(t *testing.T) {
	plan := w2sValidatePlan(t)

	for _, r := range plan.Routes {
		if r.ServiceID == "w2svalidate" {
			t.Fatalf("an S2S service must not get a REST route outside a gateway build: %+v", r)
		}
	}

	get := findRoute(t, plan, "/catalog.CatalogService/GetItem")
	if get.ServiceID != "catalog" || get.EntitlementService != "" {
		t.Fatalf("an ordinary C2S route dials and entitles against its own package: %+v", get)
	}
	if get.AuthPolicy != AuthAnyUser {
		t.Fatalf("RPC_FLAG_AUTH_ANY_USER must plan AnyUser, got %q", get.AuthPolicy)
	}
	if get.HttpPath == "" || len(get.HttpMethods) == 0 || len(get.EntitlementScopes) == 0 {
		t.Fatalf("route is missing its REST shape: %+v", get)
	}

	watch := findRoute(t, plan, "/catalog.CatalogService/WatchItems")
	if !watch.ServerStreaming || watch.ClientStreaming {
		t.Fatalf("WatchItems is server-streaming: %+v", watch)
	}

	form := findRoute(t, plan, "/signin.SigninService/BrokerFormCallback")
	if form.AuthPolicy != AuthNoAuth {
		t.Fatalf("RPC_FLAG_PUBLIC_NO_AUTH must plan NoAuth, got %q", form.AuthPolicy)
	}
	if !form.FormBody || form.RedirectField != "redirect_url" || form.RedirectJSONName != "redirectUrl" {
		t.Fatalf("rest_browser was not carried onto the route: %+v", form)
	}
	if get.FormBody || get.RedirectField != "" || get.RedirectJSONName != "" {
		t.Fatalf("a route without rest_browser must stay JSON-only: %+v", get)
	}

	var arms []string
	for _, a := range plan.Arms {
		arms = append(arms, a.GrpcPath)
	}
	if len(arms) != 3 {
		t.Fatalf("expected GetItem, WatchItems and BrokerFormCallback arms, got %v", arms)
	}
	for _, a := range plan.Arms {
		if a.IsEmptyInput || a.IsEmptyOutput || !a.ValidateInput || !a.ValidateOutput {
			t.Fatalf("user messages are validated on both legs: %+v", a)
		}
		if a.ServiceDir != "services" || plan.PkgToDirFor(a)[a.PackageName] != "services" {
			t.Fatalf("arm does not resolve its package to its service dir: %+v", a)
		}
	}
}

func TestRouteIdentity(t *testing.T) {
	cases := []struct {
		name       string
		svc        Service
		trait      string
		serviceID  string
		audience   string
		wantErrMsg bool
	}{
		{name: "ordinary c2s", svc: Service{PackageName: "catalog"}, serviceID: "catalog"},
		{name: "trait hosted", svc: Service{PackageName: "vault", HostLabel: "egress"}, trait: "query", serviceID: "egress", audience: "query"},
		{name: "event labelled", svc: Service{PackageName: "tasks", EventLabel: "eventbus"}, serviceID: "eventbus", audience: "tasks"},
		{name: "event label beats host label", svc: Service{PackageName: "tasks", EventLabel: "eventbus", HostLabel: "egress"}, trait: "query", serviceID: "eventbus", audience: "tasks"},
		{name: "host label without trait", svc: Service{PackageName: "vault", ServiceName: "Q", HostLabel: "egress"}, wantErrMsg: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			serviceID, audience, err := RouteIdentity(tc.svc, tc.trait)
			if tc.wantErrMsg {
				if err == nil {
					t.Fatal("expected an error naming the missing trait")
				}
				return
			}
			if err != nil {
				t.Fatalf("RouteIdentity: %v", err)
			}
			if serviceID != tc.serviceID || audience != tc.audience {
				t.Fatalf("got (%q, %q), want (%q, %q)", serviceID, audience, tc.serviceID, tc.audience)
			}
		})
	}
}

func TestCanValidateSkipsWellKnownTypes(t *testing.T) {
	for fullType, want := range map[string]bool{
		"catalog.GetItemRequest":  true,
		".catalog.GetItemRequest": true,
		"google.protobuf.Empty":   false,
		"google.protobuf.Struct":  false,
		"":                        false,
	} {
		if got := CanValidate(fullType); got != want {
			t.Errorf("CanValidate(%q) = %v, want %v", fullType, got, want)
		}
	}
}

func TestDispatchableShapeRefusesWhatTheDispatchersCannotEncode(t *testing.T) {
	cases := []struct {
		name    string
		method  parser.MethodInfo
		wantErr string
	}{
		{"user messages", parser.MethodInfo{FullInputType: "a.Req", FullOutputType: "a.Resp"}, ""},
		{"empty unary response", parser.MethodInfo{FullInputType: "a.Req", FullOutputType: "google.protobuf.Empty"}, ""},
		{"empty request", parser.MethodInfo{FullInputType: "google.protobuf.Empty", FullOutputType: "a.Resp", ServerStreaming: true}, ""},
		{"well-known request", parser.MethodInfo{FullInputType: "google.protobuf.Timestamp", FullOutputType: "a.Resp"}, "request of well-known type google.protobuf.Timestamp"},
		{"well-known response", parser.MethodInfo{FullInputType: "a.Req", FullOutputType: "google.protobuf.Struct"}, "response of well-known type google.protobuf.Struct"},
		{"empty streamed response", parser.MethodInfo{FullInputType: "a.Req", FullOutputType: "google.protobuf.Empty", ServerStreaming: true}, "cannot stream google.protobuf.Empty"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.method
			err := dispatchableShape("/a.S/M", &m, m.FullInputType == "google.protobuf.Empty", m.FullOutputType == "google.protobuf.Empty")
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected refusal: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("got %v, want an error containing %q", err, tc.wantErr)
			}
		})
	}
}

// A tenant-owned service's routes and arms carry its route field, so the
// gateway dials the target tenant's instance by it; a platform service's carry
// none.
func TestCollectCarriesTheRouteFieldOfTenantOwnedServicesOnly(t *testing.T) {
	protoDir := t.TempDir()
	if err := protogen.CopySysProtos(protoDir); err != nil {
		t.Fatalf("CopySysProtos: %v", err)
	}
	write := func(rel, text string) {
		t.Helper()
		path := protoDir + "/" + rel
		if err := os.MkdirAll(path[:strings.LastIndex(path, "/")], 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("services/crew.proto", `syntax = "proto3";
package crew;
import "sys/spec.proto";
option (spec.service) = {name: "crew", tenant_owned: true};
message GetCrewRequest { string id = 1; string target_tenant = 2; }
message GetCrewResponse { string id = 1; }
service CrewService {
  option (spec.service_c2s) = {auth_entitled_users: true};
  rpc GetCrew(GetCrewRequest) returns (GetCrewResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER, exposure: RPC_EXPOSURE_EXTERNAL, route_field: "target_tenant"};
  }
}
`)
	write("services/dock.proto", `syntax = "proto3";
package dock;
import "sys/spec.proto";
message GetDockRequest { string id = 1; }
message GetDockResponse { string id = 1; }
service DockService {
  option (spec.service_c2s) = {auth_entitled_users: true};
  rpc GetDock(GetDockRequest) returns (GetDockResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER, exposure: RPC_EXPOSURE_EXTERNAL};
  }
}
`)
	if _, _, _, err := protogen.ScanAndGenerateConfigHolder(protoDir, protoDir); err != nil {
		t.Fatalf("ScanAndGenerateConfigHolder: %v", err)
	}
	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	plan, err := Collect(dp, &tactype.Config{}, []Service{
		{ProtoFile: "services/crew.proto", PackageName: "crew", ServiceName: "CrewService", ParentDir: "services"},
		{ProtoFile: "services/dock.proto", PackageName: "dock", ServiceName: "DockService", ParentDir: "services"},
	}, protoDir)
	if err != nil {
		t.Fatalf("Collect: %v", err)
	}

	crew := findRoute(t, plan, "/crew.CrewService/GetCrew")
	if crew.RouteField != "target_tenant" || crew.RouteFieldJSON != "targetTenant" || crew.RouteFieldNumber != 2 {
		t.Fatalf("tenant-owned route does not carry its route field: %+v", crew)
	}
	dock := findRoute(t, plan, "/dock.DockService/GetDock")
	if dock.RouteField != "" || dock.RouteFieldJSON != "" || dock.RouteFieldNumber != 0 {
		t.Fatalf("platform route carries a route field: %+v", dock)
	}
	for _, arm := range plan.Arms {
		want := map[string]string{"/crew.CrewService/GetCrew": "target_tenant", "/dock.DockService/GetDock": ""}[arm.GrpcPath]
		if arm.RouteField != want {
			t.Fatalf("arm %s RouteField = %q, want %q", arm.GrpcPath, arm.RouteField, want)
		}
	}
}
