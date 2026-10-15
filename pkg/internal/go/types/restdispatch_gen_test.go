// Copyright (c) 2026 Seabeam LLC
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
