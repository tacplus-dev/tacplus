// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/templates"
)

func loadSwiftTemplate(name string) (string, error) {
	typeFS, err := fs.Sub(templates.FS, "swift/types")
	if err != nil {
		return "", fmt.Errorf("failed to create sub filesystem: %w", err)
	}
	content, err := fs.ReadFile(typeFS, name)
	if err != nil {
		return "", fmt.Errorf("failed to read template %s: %w", name, err)
	}
	return string(content), nil
}

// renderW2SFixture renders the W2S client template over one service carrying a
// unary, a server-streaming and a bidi method — the three shapes the generator
// distinguishes.
func renderW2SFixture(t *testing.T) string {
	t.Helper()
	data := w2sClientTemplateData{Services: []w2sServiceTemplateData{{
		ServiceName:    "contractclient.ContractApi",
		ClientTypeName: "ContractApiW2SClient",
		PackageName:    "contractclient",
		Methods: []w2sMethodTemplateData{
			{
				MethodName:      "GetContract",
				SwiftMethodName: "getContract",
				HTTPMethod:      "POST",
				HTTPPath:        "/api/v1/contractclient/ContractApi/GetContract",
				AuthRequired:    true,
				InputType:       "Contractclient_ContractRequest",
				OutputType:      "Contractclient_ContractResponse",
			},
			{
				MethodName:      "ListEvents",
				SwiftMethodName: "listEvents",
				HTTPMethod:      "POST",
				HTTPPath:        "/api/v1/contractclient/ContractApi/ListEvents",
				AuthRequired:    true,
				InputType:       "Contractclient_ContractRequest",
				OutputType:      "Contractclient_ContractEvent",
				IsStreaming:     true,
			},
			{
				MethodName:          "AssistEvents",
				SwiftMethodName:     "assistEvents",
				InputType:           "Contractclient_ContractEvent",
				OutputType:          "Contractclient_ContractEvent",
				IsUnsupportedStream: true,
			},
		},
	}}}
	out, err := executeSwiftTemplate("W2SClient.swift.tmpl", data)
	if err != nil {
		t.Fatalf("executeSwiftTemplate(W2SClient.swift.tmpl): %v", err)
	}
	return string(out)
}

// NEW-44 regression, updated for server-streaming support: no declared W2S
// method may be silently dropped from the generated Swift client. A
// server-streaming method must render as a real AsyncThrowingStream method
// carrying its route; a client-streaming/bidi method must still render as an
// unavailable stub, because the gateway carries those over WebSocket.
func TestSwiftW2SStreamingRendering(t *testing.T) {
	rendered := renderW2SFixture(t)

	// No method is absent.
	for _, name := range []string{"getContract", "listEvents", "assistEvents"} {
		if !strings.Contains(rendered, "func "+name+"(") {
			t.Errorf("rendered Swift W2S client is missing method %q", name)
		}
	}

	// Server-streaming renders as a real streaming call, with its route.
	for _, want := range []string{
		"public func listEvents(_ request: Contractclient_ContractRequest) async throws -> AsyncThrowingStream<Contractclient_ContractEvent, Error>",
		"try await self.client.invokeStream(",
		`path: "/api/v1/contractclient/ContractApi/ListEvents"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered Swift W2S client missing server-streaming marker %q", want)
		}
	}
	if strings.Contains(rendered, `unavailable, message: "ListEvents`) {
		t.Error("server-streaming method ListEvents must not render as an unavailable stub")
	}

	// Client-streaming/bidi stays an unavailable stub.
	for _, want := range []string{
		`@available(*, unavailable, message: "AssistEvents is client-streaming or bidi (WebSocket); not supported by the Swift W2S client")`,
		"fatalError(",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered Swift W2S client missing bidi stub marker %q", want)
		}
	}
	if strings.Contains(rendered, "func assistEvents(_ request: Contractclient_ContractEvent) async throws -> AsyncThrowingStream") {
		t.Error("bidi method AssistEvents must not render as a streaming method")
	}

	// Unary is unchanged.
	if !strings.Contains(rendered, "try await self.client.invoke(") {
		t.Error("rendered Swift W2S client missing unary invoke call")
	}
}

// The SSE transport and its parser must stay in the W2S client template: they
// are what makes server-streaming a real method rather than a stub.
func TestSwiftW2SSSETransport(t *testing.T) {
	tmpl, err := loadSwiftTemplate("W2SClient.swift.tmpl")
	if err != nil {
		t.Fatalf("loadSwiftTemplate: %v", err)
	}
	for _, want := range []string{
		"public func invokeStream<",
		"struct W2SSSEParser",
		`"text/event-stream"`,
		"session.bytes(for: httpRequest)",
		"case stream(String)",
		"maxEventBytes",
		"decodeStreamEvent",
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("Swift W2S template missing SSE transport marker %q", want)
		}
	}
}

// PAR-4 regression: Swift C2D client must validate the socket path. Removing
// the precondition from the template fails this test.
func TestSwiftC2DValidation(t *testing.T) {
	tmpl, err := loadSwiftTemplate("C2DClient.swift.tmpl")
	if err != nil {
		t.Fatalf("loadSwiftTemplate: %v", err)
	}
	if !strings.Contains(tmpl, "precondition(!socketPath.isEmpty") {
		t.Error("Swift C2D template missing socket path validation (precondition)")
	}
}
