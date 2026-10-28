// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

func javaRestDispatchArmForTest(grpcPath, method string) javaRestDispatchArm {
	return javaRestDispatchArm{
		GrpcPath:       grpcPath,
		ServiceID:      "catalog",
		MethodName:     method,
		StubClass:      "x.types.services.catalog.CatalogServiceGrpc",
		StubName:       "CatalogServiceBlockingStub",
		ReqType:        "x.types.services.catalog.Catalog.GetItemRequest",
		RespType:       "x.types.services.catalog.Catalog.GetItemResponse",
		FromJson:       "x.types.services.catalog.Helpers.getItemRequestFromJson",
		ToJson:         "x.types.services.catalog.Helpers.getItemResponseToJson",
		ValidateInput:  "x.types.services.catalog.Helpers.validateGetItemRequest",
		ValidateOutput: "x.types.services.catalog.Helpers.validateGetItemResponse",
	}
}

func renderJavaRestDispatchForTest(t *testing.T, data javaRestDispatchData) string {
	t.Helper()
	data.PackageBase = "x"
	data.SmPrefix = "s2sm"
	data.ProtoExceptionFqn = "x.types.sys.errors.ProtoException"
	out, err := renderJavaRestDispatch(data)
	if err != nil {
		t.Fatalf("renderJavaRestDispatch: %v", err)
	}
	return string(out)
}

// The generated Java dispatch must validate the decoded request BEFORE the
// gRPC call on both legs, and the response BEFORE it is serialized, exactly
// as the Rust and Go planes do.
func TestJavaRestDispatchValidatesBeforeForwardingAndSerializing(t *testing.T) {
	out := renderJavaRestDispatchForTest(t, javaRestDispatchData{
		UnaryArms:  []javaRestDispatchArm{javaRestDispatchArmForTest("/catalog.CatalogService/GetItem", "getItem")},
		StreamArms: []javaRestDispatchArm{javaRestDispatchArmForTest("/catalog.CatalogService/WatchItems", "watchItems")},
	})

	unary := out[strings.Index(out, "public static String dispatch("):strings.Index(out, "public static void dispatchStream(")]
	stream := out[strings.Index(out, "public static void dispatchStream("):]
	for _, leg := range []struct {
		name, body, call string
	}{
		{"unary", unary, "stub.getItem(req)"},
		{"stream", stream, "stub.watchItems(req)"},
	} {
		validate := strings.Index(leg.body, "Helpers.validateGetItemRequest(req)")
		call := strings.Index(leg.body, leg.call)
		if validate < 0 || call < 0 || validate > call {
			t.Fatalf("%s leg must validate the request before the gRPC call:\n%s", leg.name, leg.body)
		}
		if !strings.Contains(leg.body, `"request validation failed: "`) {
			t.Fatalf("%s leg does not refuse an invalid request as a validation failure:\n%s", leg.name, leg.body)
		}
	}

	if v, enc := strings.Index(unary, "Helpers.validateGetItemResponse(resp)"), strings.Index(unary, "Helpers.getItemResponseToJson(resp)"); v < 0 || enc < 0 || v > enc {
		t.Fatalf("unary leg must validate the response before serializing it:\n%s", unary)
	}
	if v, enc := strings.Index(stream, "Helpers.validateGetItemResponse(msg)"), strings.Index(stream, "Helpers.getItemResponseToJson(msg)"); v < 0 || enc < 0 || v > enc {
		t.Fatalf("stream leg must validate each item before serializing it:\n%s", stream)
	}
	if !strings.Contains(stream, `sseError(w, "response validation failed: " + e.getMessage())`) {
		t.Fatalf("stream leg must report a failed item as an SSE error event:\n%s", stream)
	}
}

// A method whose request is google.protobuf.Empty has no validator and no
// JSON decode.
func TestJavaRestDispatchEmptyInputSkipsDecodeAndValidation(t *testing.T) {
	arm := javaRestDispatchArmForTest("/catalog.CatalogService/ListItems", "listItems")
	arm.IsEmptyInput = true
	arm.ReqType, arm.FromJson, arm.ValidateInput = "", "", ""
	out := renderJavaRestDispatchForTest(t, javaRestDispatchData{UnaryArms: []javaRestDispatchArm{arm}})
	if !strings.Contains(out, "com.google.protobuf.Empty req = com.google.protobuf.Empty.getDefaultInstance();") || strings.Contains(out, "FromJson(body)") {
		t.Fatalf("an Empty request is constructed, not decoded:\n%s", out)
	}
	if strings.Contains(out, "validateGetItemRequest") {
		t.Fatalf("an Empty request has nothing to validate:\n%s", out)
	}
}

// Route table literals are Java-quoted, so an annotation value cannot escape
// its string literal, and an unset optional field is "".
func TestJavaRestDispatchRouteTableLiterals(t *testing.T) {
	out := renderJavaRestDispatchForTest(t, javaRestDispatchData{
		Routes: []javaRestDispatchRoute{{
			HttpPath:           "/signin/Signin/Call\"back\n",
			GrpcPath:           "/signin.Signin/Callback",
			ServiceID:          "signin",
			HttpMethods:        []string{"POST", "GET"},
			AuthPolicy:         "NO_AUTH",
			EntitlementScopes:  []string{"ANY", "READ"},
			CapabilityScope:    "READ",
			FormBody:           true,
			RedirectField:      "redirectUrl",
			RedirectFieldProto: "redirect_url",
		}},
	})
	for _, want := range []string{
		`"/signin/Signin/Call\"back\n",`,
		`List.of("POST", "GET"),`,
		`AuthPolicy.NO_AUTH,`,
		`List.of("ANY", "READ"),`,
		`"redirectUrl",`,
		`"redirect_url",`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("route table is missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "\"\",") != 4 {
		t.Fatalf("entitlementService, targetNamespaceField and the (platform) route field names must render as empty strings:\n%s", out)
	}
}

func TestJavaStringLiteral(t *testing.T) {
	for in, want := range map[string]string{
		`plain`:     `"plain"`,
		`a"b`:       `"a\"b"`,
		`a\b`:       `"a\\b"`,
		"tab\there": `"tab\there"`,
		"nul\x00":   `"nul\u0000"`,
		"ünïcode":   `"ünïcode"`,
	} {
		if got := javaStringLiteral(in); got != want {
			t.Errorf("javaStringLiteral(%q) = %s, want %s", in, got, want)
		}
	}
}

// A routed route carries its route field's proto name, JSON name and wire
// number; a platform route carries "", "" and 0 (issue #170).
func TestJavaRestDispatchRouteTableCarriesRouteField(t *testing.T) {
	out := renderJavaRestDispatchForTest(t, javaRestDispatchData{
		Routes: []javaRestDispatchRoute{
			{
				HttpPath: "/crew/CrewService/Hail", GrpcPath: "/crew.CrewService/Hail", ServiceID: "crew",
				HttpMethods: []string{"POST"}, AuthPolicy: "ENTITLED",
				RouteField: "target_tenant", RouteFieldJSON: "targetTenant", RouteFieldNumber: 7,
			},
			{
				HttpPath: "/catalog/CatalogService/GetItem", GrpcPath: "/catalog.CatalogService/GetItem", ServiceID: "catalog",
				HttpMethods: []string{"POST"}, AuthPolicy: "ENTITLED",
			},
		},
	})
	for _, want := range []string{
		"public final String routeField;",
		"public final String routeFieldJson;",
		"public final int routeFieldNumber;",
		"\"target_tenant\",\n            \"targetTenant\",\n            7)",
		"\"\",\n            \"\",\n            0)",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("route table is missing %q:\n%s", want, out)
		}
	}
}

// The generated routing helpers: the tid check, RouteTenant for a decoded
// value and RouteTenantFromWire for the gateway's gRPC proxy, all over the one
// chassis implementation, refusing as HTTP 400 / INVALID_ARGUMENT.
func TestJavaRestDispatchRoutingHelpers(t *testing.T) {
	out := renderJavaRestDispatchForTest(t, javaRestDispatchData{})
	for _, want := range []string{
		"public static boolean validTenantId(String s) {",
		"return x.chassis.serviceconnection.RouteTenant.validTenantId(s);",
		"public static String routeTenant(RouteInfo route, String value) throws HttpError {",
		"public static String routeTenantFromWire(byte[] frame, RouteInfo route) throws StatusException {",
		"x.chassis.serviceconnection.RouteTenant.fromWire(frame, route.routeFieldNumber, route.routeField)",
		"Status.INVALID_ARGUMENT",
		"Channel channel(String serviceId, String tenant) throws StatusException;",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("RestDispatch is missing %q:\n%s", want, out)
		}
	}
	// A platform route has no route field: "" is returned without reading the frame.
	helper := out[strings.Index(out, "public static String routeTenantFromWire("):]
	if i := strings.Index(helper, "if (route.routeFieldNumber == 0"); i < 0 || i > strings.Index(helper, "RouteTenant.fromWire") {
		t.Fatalf("routeTenantFromWire must answer \"\" for a platform route before reading the frame:\n%s", helper)
	}
}

// A routed arm decodes and validates the request, then reads its route field
// and refuses an empty/malformed one with HTTP 400 BEFORE the dispatcher is
// asked for a channel, which it is asked for that tenant. A platform arm asks
// for its channel with no tenant, exactly where it did before.
func TestJavaRestDispatchRoutedArmRefusesBeforeDialling(t *testing.T) {
	routed := javaRestDispatchArmForTest("/crew.CrewService/Hail", "hail")
	routed.ServiceID = "crew"
	routed.RouteField = "target_tenant"
	routed.RouteFieldGetter = "getTargetTenant"
	routedStream := routed
	routedStream.GrpcPath = "/crew.CrewService/Watch"
	routedStream.MethodName = "watch"
	platform := javaRestDispatchArmForTest("/catalog.CatalogService/GetItem", "getItem")
	out := renderJavaRestDispatchForTest(t, javaRestDispatchData{
		UnaryArms:  []javaRestDispatchArm{routed, platform},
		StreamArms: []javaRestDispatchArm{routedStream},
	})
	unary := out[strings.Index(out, "public static String dispatch("):strings.Index(out, "public static void dispatchStream(")]
	stream := out[strings.Index(out, "public static void dispatchStream("):]

	routedArm := unary[strings.Index(unary, `case "/crew.CrewService/Hail"`):strings.Index(unary, `case "/catalog.CatalogService/GetItem"`)]
	platformArm := unary[strings.Index(unary, `case "/catalog.CatalogService/GetItem"`):]
	streamArm := stream[strings.Index(stream, `case "/crew.CrewService/Watch"`):]
	for name, arm := range map[string]string{"unary": routedArm, "stream": streamArm} {
		decode := strings.Index(arm, "getItemRequestFromJson(body)")
		validate := strings.Index(arm, "validateGetItemRequest(req)")
		tenant := strings.Index(arm, `routeTenantValue("target_tenant", req.getTargetTenant())`)
		dial := strings.Index(arm, `dispatcher.channel("crew", tenant)`)
		if decode < 0 || validate < 0 || tenant < 0 || dial < 0 || !(decode < validate && validate < tenant && tenant < dial) {
			t.Fatalf("%s routed arm must decode, validate, read the route field, then dial that tenant (decode %d, validate %d, tenant %d, dial %d):\n%s",
				name, decode, validate, tenant, dial, arm)
		}
	}
	if !strings.Contains(platformArm, `dispatcher.channel("catalog", "")`) || strings.Contains(platformArm, "routeTenant") {
		t.Fatalf("a platform arm dials with no tenant and reads no route field:\n%s", platformArm)
	}
	// The platform arm keeps its order: channel, then decode.
	if strings.Index(platformArm, "dispatcher.channel(") > strings.Index(platformArm, "FromJson(body)") {
		t.Fatalf("a platform arm keeps its pre-routing order:\n%s", platformArm)
	}
}
