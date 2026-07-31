package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

func TestTypeScriptW2SUnaryValidatesRequestAndResponse(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Catalog"}
	method := &parser.MethodInfo{
		Name:           "Lookup",
		InputType:      "LookupRequest",
		FullInputType:  "example.catalog.LookupRequest",
		OutputType:     "LookupResponse",
		FullOutputType: "example.catalog.LookupResponse",
		HttpMethod:     "POST",
		HttpPath:       "/api/example.catalog/v1/Catalog/Lookup",
	}

	got := generateTsGrpcOverRestMethod(svc, method, "example.catalog", true)
	for _, want := range []string{
		"types.validateLookupRequest(request);",
		"toJson(types.LookupRequestSchema, request, { alwaysEmitImplicit: true })",
		"const response = fromJson(types.LookupResponseSchema, json as any);",
		"types.validateLookupResponse(response);",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("W2S unary output missing %q:\n%s", want, got)
		}
	}
}

func TestTypeScriptW2SStreamingSurfacesAreEmittedAndValidated(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Catalog"}
	clientStream := &parser.MethodInfo{
		Name:            "Upload",
		InputType:       "UploadRequest",
		FullInputType:   "example.catalog.UploadRequest",
		OutputType:      "UploadResponse",
		FullOutputType:  "example.catalog.UploadResponse",
		ClientStreaming: true,
		HttpPath:        "/api/example.catalog/v1/Catalog/Upload",
	}
	bidi := &parser.MethodInfo{
		Name:            "Sync",
		InputType:       "SyncRequest",
		FullInputType:   "example.catalog.SyncRequest",
		OutputType:      "SyncResponse",
		FullOutputType:  "example.catalog.SyncResponse",
		ClientStreaming: true,
		ServerStreaming: true,
		HttpPath:        "/api/example.catalog/v1/Catalog/Sync",
	}

	if !shouldEmitGrpcOverRestMethod(clientStream, true) {
		t.Fatalf("full W2S must emit client-streaming methods")
	}
	if !shouldEmitGrpcOverRestMethod(bidi, true) {
		t.Fatalf("full W2S must emit bidi methods")
	}
	if shouldEmitGrpcOverRestMethod(clientStream, false) {
		t.Fatalf("legacy REST compatibility mode must not silently masquerade client-streaming as REST")
	}

	streamCode := generateTsGrpcOverRestClientStreamMethod(svc, clientStream, "example.catalog", true)
	for _, want := range []string{
		"types.validateUploadRequest(request);",
		"toJson(types.UploadRequestSchema, request, { alwaysEmitImplicit: true })",
		"const response = fromJson(types.UploadResponseSchema, json as any);",
		"types.validateUploadResponse(response);",
	} {
		if !strings.Contains(streamCode, want) {
			t.Fatalf("W2S client-stream output missing %q:\n%s", want, streamCode)
		}
	}

	bidiCode := generateTsGrpcOverRestBidiMethod(svc, bidi, "example.catalog", true).method
	for _, want := range []string{
		"types.validateSyncRequest(message);",
		"return toJson(types.SyncRequestSchema, message, { alwaysEmitImplicit: true });",
		"const message = fromJson(types.SyncResponseSchema, json as any);",
		"types.validateSyncResponse(message);",
	} {
		if !strings.Contains(bidiCode, want) {
			t.Fatalf("W2S bidi output missing %q:\n%s", want, bidiCode)
		}
	}
}

func TestTypeScriptRestClientsEmitImplicitDefaults(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Catalog"}
	method := &parser.MethodInfo{
		Name:           "Search",
		InputType:      "SearchRequest",
		FullInputType:  "example.catalog.SearchRequest",
		OutputType:     "SearchResponse",
		FullOutputType: "example.catalog.SearchResponse",
		HttpMethod:     "POST",
		HttpPath:       "/api/example.catalog/v1/Catalog/Search",
	}

	got := generateTsRestClientMethod(svc, method, "example.catalog")
	want := "toJson(types.SearchRequestSchema, request, { alwaysEmitImplicit: true })"
	if !strings.Contains(got, want) {
		t.Fatalf("REST client output missing %q:\n%s", want, got)
	}
}
