// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

func goRestDispatchArmForTest(grpcPath, method string, streaming bool) goRestDispatchArm {
	return goRestDispatchArm{
		GrpcPath:       grpcPath,
		ServiceID:      "catalog",
		MethodName:     method,
		NewClient:      "catalog_pb.NewCatalogServiceClient",
		FromJSON:       "catalog_pb.GetItemRequestFromJSON",
		ToJSON:         "catalog_pb.GetItemResponseToJSON",
		ValidateInput:  "catalog_pb.ValidateGetItemRequest",
		ValidateOutput: "catalog_pb.ValidateGetItemResponse",
	}
}

func renderGoRestDispatchForTest(t *testing.T, data goRestDispatchData) string {
	t.Helper()
	out, err := renderGoRestDispatch(data)
	if err != nil {
		t.Fatalf("renderGoRestDispatch: %v", err)
	}
	return string(out)
}

// The generated Go dispatch must validate the decoded request BEFORE the
// gRPC call on both legs, and the response BEFORE it is serialized, exactly
// as the Rust module does.
func TestGoRestDispatchValidatesBeforeForwardingAndSerializing(t *testing.T) {
	out := renderGoRestDispatchForTest(t, goRestDispatchData{
		Imports:    []goRestDispatchImport{{Alias: "catalog_pb", Path: "example.com/x/model/types/services/catalog"}},
		UnaryArms:  []goRestDispatchArm{goRestDispatchArmForTest("/catalog.CatalogService/GetItem", "GetItem", false)},
		StreamArms: []goRestDispatchArm{goRestDispatchArmForTest("/catalog.CatalogService/WatchItems", "WatchItems", true)},
	})

	unary := out[strings.Index(out, "func Dispatch("):strings.Index(out, "func DispatchStream(")]
	stream := out[strings.Index(out, "func DispatchStream("):]
	for _, leg := range []struct {
		name, body, call string
	}{
		{"unary", unary, ".GetItem(callCtx, req)"},
		{"stream", stream, ".WatchItems(callCtx, req)"},
	} {
		validate := strings.Index(leg.body, "catalog_pb.ValidateGetItemRequest(req)")
		call := strings.Index(leg.body, leg.call)
		if validate < 0 || call < 0 || validate > call {
			t.Fatalf("%s leg must validate the request before the gRPC call:\n%s", leg.name, leg.body)
		}
		if !strings.Contains(leg.body, `"request validation failed: %v"`) {
			t.Fatalf("%s leg does not refuse an invalid request as a validation failure:\n%s", leg.name, leg.body)
		}
	}

	if v, enc := strings.Index(unary, "catalog_pb.ValidateGetItemResponse(resp)"), strings.Index(unary, "catalog_pb.GetItemResponseToJSON(resp)"); v < 0 || enc < 0 || v > enc {
		t.Fatalf("unary leg must validate the response before serializing it:\n%s", unary)
	}
	if v, enc := strings.Index(stream, "catalog_pb.ValidateGetItemResponse(msg)"), strings.Index(stream, "catalog_pb.GetItemResponseToJSON(msg)"); v < 0 || enc < 0 || v > enc {
		t.Fatalf("stream leg must validate each item before serializing it:\n%s", stream)
	}
	if !strings.Contains(stream, `"event: error\ndata: %s\n\n"`) {
		t.Fatalf("stream leg must report a failed item as an SSE error event:\n%s", stream)
	}
}

// A method whose request is google.protobuf.Empty has no validator and no
// JSON decode; the plane imports emptypb only then.
func TestGoRestDispatchEmptyInputSkipsDecodeAndValidation(t *testing.T) {
	arm := goRestDispatchArmForTest("/catalog.CatalogService/ListItems", "ListItems", false)
	arm.IsEmptyInput = true
	arm.FromJSON, arm.ValidateInput = "", ""
	out := renderGoRestDispatchForTest(t, goRestDispatchData{
		Imports: []goRestDispatchImport{
			{Alias: "catalog_pb", Path: "example.com/x/model/types/services/catalog"},
			{Alias: "emptypb", Path: "google.golang.org/protobuf/types/known/emptypb"},
		},
		UnaryArms: []goRestDispatchArm{arm},
	})
	if !strings.Contains(out, "req := &emptypb.Empty{}") || strings.Contains(out, "FromJSON(body)") {
		t.Fatalf("an Empty request is constructed, not decoded:\n%s", out)
	}
	if strings.Contains(out, "ValidateGetItemRequest") {
		t.Fatalf("an Empty request has nothing to validate:\n%s", out)
	}

	noStreams := renderGoRestDispatchForTest(t, goRestDispatchData{
		Imports:   []goRestDispatchImport{{Alias: "catalog_pb", Path: "example.com/x/model/types/services/catalog"}},
		UnaryArms: []goRestDispatchArm{goRestDispatchArmForTest("/catalog.CatalogService/GetItem", "GetItem", false)},
	})
	if strings.Contains(noStreams, "\t\"errors\"\n") {
		t.Fatalf("a plane with no streaming arms must not import errors (unused import):\n%s", noStreams)
	}
}

// Route table literals are Go-quoted, so an annotation value cannot escape
// its string literal, and an unset optional field is its zero value.
func TestGoRestDispatchRouteTableLiterals(t *testing.T) {
	out := renderGoRestDispatchForTest(t, goRestDispatchData{
		Routes: []goRestDispatchRoute{{
			HttpPath:           `/signin/Signin/Call"back`,
			GrpcPath:           "/signin.Signin/Callback",
			ServiceID:          "signin",
			HttpMethods:        []string{"POST", "GET"},
			AuthPolicy:         "NoAuth",
			EntitlementScopes:  []string{"ANY", "READ"},
			CapabilityScope:    "READ",
			FormBody:           true,
			RedirectField:      "redirectUrl",
			RedirectFieldProto: "redirect_url",
		}},
	})
	for _, want := range []string{
		`HttpPath:             "/signin/Signin/Call\"back",`,
		`HttpMethods:          []string{"POST", "GET"},`,
		`AuthPolicy:           AuthPolicyNoAuth,`,
		`EntitlementScopes:    []string{"ANY", "READ"},`,
		`EntitlementService:   "",`,
		`TargetNamespaceField: "",`,
		`FormBody:             true,`,
		`RedirectField:        "redirectUrl",`,
		`RedirectFieldProto:   "redirect_url",`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("route table is missing %q:\n%s", want, out)
		}
	}
}

// A tenant-routed route carries its route field (proto name, JSON name, wire
// number) in the table the REST plane and the gateway's gRPC proxy share; a
// platform route carries the zero values.
func TestGoRestDispatchRouteTableCarriesRouteField(t *testing.T) {
	out := renderGoRestDispatchForTest(t, goRestDispatchData{
		Routes: []goRestDispatchRoute{
			{HttpPath: "/crew/Crew/Hail", GrpcPath: "/crew.Crew/Hail", ServiceID: "crew", AuthPolicy: "Entitled",
				RouteField: "target_tenant", RouteFieldJSON: "targetTenant", RouteFieldNumber: 4},
			{HttpPath: "/catalog/Catalog/Get", GrpcPath: "/catalog.Catalog/Get", ServiceID: "catalog", AuthPolicy: "Entitled"},
		},
	})
	for _, want := range []string{
		`RouteField:           "target_tenant",`,
		`RouteFieldJSON:       "targetTenant",`,
		`RouteFieldNumber:     4,`,
		`RouteField:           "",`,
		`RouteFieldNumber:     0,`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("route table is missing %q:\n%s", want, out)
		}
	}
	// The gateway's gRPC proxy holds only raw frames: the plane exports the
	// wire reader and the tid check it routes by.
	for _, want := range []string{
		"func ValidTenantID(s string) bool",
		"func RouteTenant(field, value string) (string, error)",
		"func RouteTenantFromWire(frame []byte, route *RouteInfo) (string, error)",
		"serviceconnection.RouteTenantFromWire(frame, route.RouteField, route.RouteFieldNumber)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("plane is missing %q:\n%s", want, out)
		}
	}
}

// The Dispatcher names the target tenant: a routed arm decodes and validates
// the request, reads its route field, refuses a missing or malformed tid with
// a 400 BEFORE anything is dialled, and only then asks for the tenant's
// connection. A platform arm is unchanged and asks with no tenant.
func TestGoRestDispatchRoutedArmsRouteByTheField(t *testing.T) {
	routed := goRestDispatchArmForTest("/crew.Crew/Hail", "Hail", false)
	routed.ServiceID = "crew"
	routed.RouteField = "target_tenant"
	routed.RouteGetter = "GetTargetTenant"
	routedStream := goRestDispatchArmForTest("/crew.Crew/Watch", "Watch", true)
	routedStream.ServiceID = "crew"
	routedStream.RouteField = "target_tenant"
	routedStream.RouteGetter = "GetTargetTenant"
	platform := goRestDispatchArmForTest("/catalog.CatalogService/GetItem", "GetItem", false)
	out := renderGoRestDispatchForTest(t, goRestDispatchData{
		Imports:        []goRestDispatchImport{{Alias: "catalog_pb", Path: "example.com/x/model/types/services/catalog"}},
		UnaryArms:      []goRestDispatchArm{routed, platform},
		StreamArms:     []goRestDispatchArm{routedStream},
		ModuleBasePath: "example.com/x/model",
	})
	if !strings.Contains(out, "Conn(ctx context.Context, serviceID, tenant string) (grpc.ClientConnInterface, error)") {
		t.Fatalf("Dispatcher.Conn must take the target tenant:\n%s", out)
	}
	if !strings.Contains(out, `"example.com/x/model/chassis/serviceconnection"`) {
		t.Fatalf("plane must import the serviceconnection chassis:\n%s", out)
	}

	arm := func(body, path string) string {
		start := strings.Index(body, "case "+`"`+path+`":`)
		if start < 0 {
			t.Fatalf("no arm for %s:\n%s", path, body)
		}
		rest := body[start+1:]
		end := strings.Index(rest, "\tcase ")
		if end < 0 {
			end = strings.Index(rest, "\tdefault:")
		}
		return rest[:end]
	}
	unary := out[strings.Index(out, "func Dispatch("):strings.Index(out, "func DispatchStream(")]
	stream := out[strings.Index(out, "func DispatchStream("):]
	for _, leg := range []struct{ name, body string }{
		{"unary", arm(unary, "/crew.Crew/Hail")},
		{"stream", arm(stream, "/crew.Crew/Watch")},
	} {
		decode := strings.Index(leg.body, "FromJSON(body)")
		validate := strings.Index(leg.body, "ValidateGetItemRequest(req)")
		route := strings.Index(leg.body, `RouteTenant("target_tenant", req.GetTargetTenant())`)
		conn := strings.Index(leg.body, `dispatcher.Conn(ctx, "crew", tenant)`)
		if decode < 0 || validate < 0 || route < 0 || conn < 0 || !(decode < validate && validate < route && route < conn) {
			t.Fatalf("%s routed arm must decode, validate, route, then dial (decode=%d validate=%d route=%d conn=%d):\n%s",
				leg.name, decode, validate, route, conn, leg.body)
		}
	}
	plat := arm(unary, "/catalog.CatalogService/GetItem")
	if !strings.Contains(plat, `dispatcher.Conn(ctx, "catalog", "")`) || strings.Contains(plat, "RouteTenant(") {
		t.Fatalf("a platform arm dials with no tenant and reads no route field:\n%s", plat)
	}
	if c, d := strings.Index(plat, "dispatcher.Conn("), strings.Index(plat, "FromJSON(body)"); c < 0 || d < 0 || c > d {
		t.Fatalf("a platform arm keeps its order (dial, then decode):\n%s", plat)
	}
}

// The route getter is the protoc-gen-go getter of the route field.
func TestGoRestDispatchRouteGetter(t *testing.T) {
	for field, want := range map[string]string{"tenant": "GetTenant", "target_tenant": "GetTargetTenant", "": ""} {
		if got := goRouteGetter(field); got != want {
			t.Fatalf("goRouteGetter(%q) = %q, want %q", field, got, want)
		}
	}
}
