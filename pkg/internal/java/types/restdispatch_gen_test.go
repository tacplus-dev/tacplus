// Copyright (c) 2026 Seabeam LLC
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
			HttpPath:          "/signin/Signin/Call\"back\n",
			GrpcPath:          "/signin.Signin/Callback",
			ServiceID:         "signin",
			HttpMethods:       []string{"POST", "GET"},
			AuthPolicy:        "NO_AUTH",
			EntitlementScopes: []string{"ANY", "READ"},
			CapabilityScope:   "READ",
			FormBody:          true,
			RedirectField:     "redirect_url",
		}},
	})
	for _, want := range []string{
		`"/signin/Signin/Call\"back\n",`,
		`List.of("POST", "GET"),`,
		`AuthPolicy.NO_AUTH,`,
		`List.of("ANY", "READ"),`,
		`"redirect_url")`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("route table is missing %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "\"\",") != 2 {
		t.Fatalf("entitlementService and targetNamespaceField must render as empty strings:\n%s", out)
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
