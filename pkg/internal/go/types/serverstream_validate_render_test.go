// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// A plain server-streaming method (no collection descriptor) whose input type has
// no user-facing validator — goValidateRef returns "" for google.protobuf.* and
// for TraceRequest. The generated client must omit the validation block entirely,
// like every sibling branch does. Emitting it unguarded yields
// `if err := (req); err != nil {`, which compiles but is always true, so every
// call fails with InvalidArgument regardless of the request.

func serverStreamMethod(inputType, fullInputType string) *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:            "Watch",
		InputType:       inputType,
		OutputType:      "Item",
		FullInputType:   fullInputType,
		FullOutputType:  "catalog.Item",
		ServerStreaming: true,
	}
}

func serverStreamService(m *parser.MethodInfo) *parser.ServiceInfo {
	return &parser.ServiceInfo{Name: "Catalog", IsS2S: true, Methods: []*parser.MethodInfo{m}}
}

func TestServerStreaming_Client_OmitsValidationWhenInputHasNoValidator(t *testing.T) {
	m := serverStreamMethod("Empty", "google.protobuf.Empty")
	got, err := generateGrpcClientMethod(serverStreamService(m), m, "catalog", "services")
	if err != nil {
		t.Fatalf("client gen error: %v", err)
	}
	if strings.Contains(got, "if err := (req); err != nil") {
		t.Fatalf("validator-less input emitted a degenerate validation call:\n%s", got)
	}
	for _, unwanted := range []string{
		"// Validate request before sending.",
		"validation failed:",
	} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("validator-less input must emit no validation block, found %q\ngot:\n%s", unwanted, got)
		}
	}
	if !strings.Contains(got, "return c.client.Watch(ctx, req, opts...)") {
		t.Fatalf("client method must still call the stream:\n%s", got)
	}
}

func TestServerStreaming_Client_KeepsValidationWhenInputHasValidator(t *testing.T) {
	m := serverStreamMethod("WatchRequest", "catalog.WatchRequest")
	got, err := generateGrpcClientMethod(serverStreamService(m), m, "catalog", "services")
	if err != nil {
		t.Fatalf("client gen error: %v", err)
	}
	for _, want := range []string{
		"// Validate request before sending.",
		"if err := types.ValidateWatchRequest(req); err != nil {",
		`return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("validation failed: %v", err))`,
		"return c.client.Watch(ctx, req, opts...)",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("server-streaming client missing %q\ngot:\n%s", want, got)
		}
	}
}
