// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// rustMcpCallerTidSanitizer is rustMcpTestSanitizer's tool request with a
// (spec.caller_tid) field.
func rustMcpCallerTidSanitizer() *rustEgressSanitizer {
	return newRustEgressSanitizer([]model.ProtoFile{{Package: "ticket", Messages: []model.Message{
		{Name: "GetTicketBriefRequest", Fields: []model.Field{
			{Name: "tenant", Type: "string", Number: 1,
				Annotations: map[string]model.AnnotationValue{"caller_tid": {Type: model.AnnotationTypeBool, BoolValue: true}}},
			{Name: "ticket_id", Type: "string", Number: 2},
		}},
		{Name: "GetTicketBriefResponse", Fields: []model.Field{{Name: "summary", Type: "string", Number: 1}}},
		{Name: "GetTicketRequest", Fields: []model.Field{{Name: "ticket_id", Type: "string", Number: 1}}},
		{Name: "GetTicketResponse", Fields: []model.Field{{Name: "body", Type: "string", Number: 1}}},
	}}})
}

// (spec.caller_tid) on a Rust MCP tool (#475, products-layer-2026-11-08) is
// the server's, as on the gRPC wrapper: absent from the schema, refused when
// named, and bound to the session caller's tid claim (empty stamped, another
// tenant or no tid claim refused) before the handler runs.
func TestRustMcpToolBindsCallerTid(t *testing.T) {
	svc, tool, _, _ := rustMcpTestService()

	raw, err := rustMcpInputSchema(tool, "ticket", rustMcpCallerTidSanitizer())
	if err != nil {
		t.Fatalf("rustMcpInputSchema: %v", err)
	}
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal([]byte(raw), &schema); err != nil {
		t.Fatalf("the generated tool schema is not valid JSON (%v): %s", err, raw)
	}
	if schema.Properties["tenant"] != nil || schema.Properties["ticket_id"] == nil {
		t.Fatalf("the (spec.caller_tid) field must be absent from the schema, the others present:\n%s", raw)
	}

	got, err := generateRustMcpDispatch(svc, "ticket", map[string]string{}, rustMcpCallerTidSanitizer())
	if err != nil {
		t.Fatalf("generateRustMcpDispatch: %v", err)
	}
	for _, want := range []string{
		`if arguments.contains_key("tenant")`,
		`"tenant is set by the server from your verified tid claim; it is not a tool argument"`,
		// namespace_gen.go's own block, verbatim, as the gRPC wrapper renders it.
		`// (spec.caller_tid) tenant: bind the field to the caller's tenant.`,
		`.and_then(|c| c.tid())`,
		`"GetTicketBrief requires a caller in a tenant: its token carries no tid claim (products-layer-2026-11-08)"`,
		`req.tenant = __caller_tid;`,
		`"tenant does not match the caller's tenant (its token's tid claim): a call keys only its own tenant's data"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated MCP dispatch is missing %q\ngot:\n%s", want, got)
		}
	}
	if bind, call := strings.Index(got, "req.tenant = __caller_tid;"), strings.Index(got, "self.handler.get_ticket_brief(caller, req)"); call < 0 || bind > call {
		t.Fatalf("the binding must precede the handler call:\n%s", got)
	}
}
