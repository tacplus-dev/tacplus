// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/restdispatch"
)

// Issue #170: the gateway's ROUTES table carries each route's route field (its
// proto name, JSON name and wire number), so the REST handler and the gRPC
// proxy read the target tenant the same way. A platform route carries none.
func TestRustRouteEntryCarriesTheRouteField(t *testing.T) {
	routed := rustRouteEntry(restdispatch.Route{
		GrpcPath: "/crew.CrewService/GetCrew", ServiceID: "crew", AuthPolicy: restdispatch.AuthEntitled,
		RouteField: "target_tenant", RouteFieldJSON: "targetTenant", RouteFieldNumber: 2,
	})
	if routed.RouteField != `Some("target_tenant")` || routed.RouteFieldJSON != `Some("targetTenant")` || routed.RouteFieldNumber != 2 {
		t.Fatalf("routed entry = %+v, want the route field's proto name, JSON name and number", routed)
	}
	platform := rustRouteEntry(restdispatch.Route{GrpcPath: "/dock.DockService/GetDock", ServiceID: "dock", AuthPolicy: restdispatch.AuthEntitled})
	if platform.RouteField != "None" || platform.RouteFieldJSON != "None" || platform.RouteFieldNumber != 0 {
		t.Fatalf("platform entry = %+v, want no route field", platform)
	}

	out := renderRustRestDispatchForTest(t, restDispatchData{Routes: []routeEntry{routed, platform}})
	for _, want := range []string{
		"pub route_field: Option<&'static str>,",
		"pub route_field_json: Option<&'static str>,",
		"pub route_field_number: u32,",
		`route_field: Some("target_tenant"),`,
		`route_field_json: Some("targetTenant"),`,
		"route_field_number: 2,",
		"route_field: None,",
		"route_field_number: 0,",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("ROUTES table missing %q:\n%s", want, out)
		}
	}
}

// The routing helpers every router shares are emitted beside the table: the
// tid check, the field check and the raw-frame reader the gateway's generic
// gRPC proxy uses. One implementation (servicemanager::tenant_route), so the
// gateway, the S2S clients and the instance's backstop cannot disagree.
func TestRustRestDispatchEmitsTheRoutingHelpers(t *testing.T) {
	out := renderRustRestDispatchForTest(t, restDispatchData{})
	for _, want := range []string{
		"pub use servicemanager::tenant_route::valid_tenant_id;",
		"pub fn route_tenant(field: &str, value: &str) -> Result<String, tonic::Status> {",
		"servicemanager::tenant_route::route_tenant(field, value)",
		"pub fn route_tenant_from_wire(frame: &[u8], route: &RouteInfo) -> Result<String, tonic::Status> {",
		"servicemanager::tenant_route::route_tenant_from_wire(frame, route.route_field_number, route.route_field.unwrap_or(\"\"))",
		"async fn channel(&self, service_id: &str, tenant: &str) -> Result<Channel, tonic::Status>;",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rest dispatch missing %q:\n%s", want, out)
		}
	}
}

func routedDispatchArms() restDispatchData {
	unary := dispatchArm{
		GrpcPath: "/crew.CrewService/GetCrew", ServiceID: "crew", MethodSnake: "get_crew",
		ClientPath: "crew::crew_service_client::CrewServiceClient", FromJsonPath: "crew::get_crew_request_from_json",
		ToJsonPath: "crew::get_crew_response_to_json", ValidateInput: true,
		RouteField: "target_tenant", RouteFieldIdent: "target_tenant",
	}
	stream := unary
	stream.GrpcPath, stream.MethodSnake = "/crew.CrewService/WatchCrew", "watch_crew"
	stream.FromJsonPath, stream.ToJsonPath = "crew::watch_crew_request_from_json", "crew::watch_crew_event_to_json"
	platform := dispatchArm{
		GrpcPath: "/dock.DockService/GetDock", ServiceID: "dock", MethodSnake: "get_dock",
		ClientPath: "dock::dock_service_client::DockServiceClient", FromJsonPath: "dock::get_dock_request_from_json",
		ToJsonPath: "dock::get_dock_response_to_json", ValidateInput: true,
	}
	return restDispatchData{HasStreaming: true, UnaryArms: []dispatchArm{unary, platform}, StreamArms: []dispatchArm{stream}}
}

// A routed REST arm decodes and validates the request, reads its route field
// and refuses an empty or malformed one (HTTP 400) BEFORE the dispatcher is
// asked for a channel, then dials that tenant only.
func TestRustRestDispatchRoutedArmReadsTheTenantBeforeDialling(t *testing.T) {
	out := renderRustRestDispatchForTest(t, routedDispatchArms())
	for _, arm := range []struct{ decode, method string }{
		{"crew::get_crew_request_from_json(body)", "get_crew"},
		{"crew::watch_crew_request_from_json(body)", "watch_crew"},
	} {
		start := strings.Index(out, arm.decode)
		if start < 0 {
			t.Fatalf("missing %s:\n%s", arm.decode, out)
		}
		body := out[start:]
		validate := strings.Index(body, "req.validate()")
		route := strings.Index(body, `route_tenant("target_tenant", &req.target_tenant)`)
		dial := strings.Index(body, `dispatcher.channel("crew", &tenant)`)
		forward := strings.Index(body, "."+arm.method+"(with_metadata(req")
		if validate < 0 || route < 0 || dial < 0 || forward < 0 {
			t.Fatalf("%s: routed arm missing validate/route/dial/forward:\n%s", arm.method, body)
		}
		if !(validate < route && route < dial && dial < forward) {
			t.Fatalf("%s: routed arm must validate, read the tenant, then dial it, then forward:\n%s", arm.method, body)
		}
	}
	if !strings.Contains(out, `let tenant = route_tenant("target_tenant", &req.target_tenant).map_err(grpc_err)?;`) {
		t.Fatalf("unary routed arm must answer a bad route field with grpc_err (INVALID_ARGUMENT -> 400):\n%s", out)
	}
}

// A platform arm is not addressed by tenant: it dials with "" exactly where it
// always dialled, before the body is decoded.
func TestRustRestDispatchPlatformArmDialsWithoutATenant(t *testing.T) {
	out := renderRustRestDispatchForTest(t, routedDispatchArms())
	start := strings.Index(out, `"/dock.DockService/GetDock" => {`)
	if start < 0 {
		t.Fatalf("platform arm missing:\n%s", out)
	}
	body := out[start:]
	dial := strings.Index(body, `dispatcher.channel("dock", "").await`)
	decode := strings.Index(body, "dock::get_dock_request_from_json(body)")
	if dial < 0 || decode < 0 || dial > decode {
		t.Fatalf("platform arm must dial with no tenant, as before:\n%s", body)
	}
	if strings.Contains(body[:decode], "route_tenant") {
		t.Fatalf("platform arm must not read a route field:\n%s", body)
	}
}
