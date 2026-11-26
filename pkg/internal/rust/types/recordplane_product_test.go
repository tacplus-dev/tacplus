// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/collection"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// The record plane's S2S half refuses a product's caller on every rpc shape,
// right where the caller is bound and before any pin or the handler (#475,
// products-layer-2026-11-08): its rows are tenants' and the platform's, and a
// product never reaches a tenant's data but as the custodian of what the
// tenant sends it. Another S2S service does not carry the refusal: a product
// calling /system is an allowed edge. Twin of Go's
// TestGoRecordPlaneRefusesProductCaller.
func TestRustRecordPlaneRefusesProductCaller(t *testing.T) {
	const guard = "if caller.as_ref().is_some_and(|c| c.token().product().is_some()) {"
	refusal := `return Err(tonic::Status::permission_denied("QueryRows is the record plane, whose rows are tenants' and the platform's: a product's caller never reads or writes it (products-layer-2026-11-08)"));`
	method := func(client, server bool, resolved *collection.Resolved) *parser.MethodInfo {
		m := rustCallerTestMethod("QueryRows")
		m.ClientStreaming, m.ServerStreaming = client, server
		if resolved != nil {
			m.Collection = &parser.CollectionDescriptor{Mode: resolved.Mode, OrderBy: resolved.OrderBy, DefaultPageSize: resolved.DefaultPageSize}
			m.Resolved = resolved
		}
		return m
	}
	for _, tc := range []struct {
		name string
		m    *parser.MethodInfo
	}{
		{"unary", method(false, false, nil)},
		{"page", method(false, false, &collection.Resolved{
			Mode: "PAGE", OrderBy: []string{"id"}, DefaultPageSize: 50, MaxPageSize: 10000,
			PageItemType: "contractclient.ContractResponse", PageItemsField: "items",
		})},
		{"server streaming", method(false, true, nil)},
		{"snapshot stream", method(false, true, &collection.Resolved{
			Mode: "SNAPSHOT", Tier: collection.TierStream, OrderBy: []string{"id"},
		})},
		{"watch", method(false, true, &collection.Resolved{
			Mode: "WATCH", ReplayDepth: 1000, OrderBy: []string{"revision"},
			WatchOneOf: "event", WatchItemType: "contractclient.Item", WatchChangeType: "contractclient.ItemChange",
			WatchRevisionField: "revision",
		})},
		{"client streaming", method(true, false, nil)},
		{"bidi", method(true, true, nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, plane := range []bool{true, false} {
				svc := &parser.ServiceInfo{Name: "Rows", IsS2S: true, Methods: []*parser.MethodInfo{tc.m}}
				if plane {
					svc.SynthesizedKind = parser.SynthesizedKindRecordMap
				}
				body := rustMethodBody(t, rustRenderCallerWrapper(t, svc), tc.m.Name)
				has := strings.Contains(body, guard) && strings.Contains(body, refusal)
				if has != plane {
					t.Fatalf("record plane %v: refusal emitted %v\ngot:\n%s", plane, has, body)
				}
				if !plane {
					continue
				}
				bound := strings.Index(body, "servicemanager::auth::Caller::new(")
				at := strings.Index(body, refusal)
				call := strings.Index(body, "self.handler.query_rows(")
				if bound < 0 || at < bound {
					t.Fatalf("the refusal must follow the caller's binding\ngot:\n%s", body)
				}
				if call < 0 || at > call {
					t.Fatalf("the refusal must precede the handler call\ngot:\n%s", body)
				}
				if strings.Count(body, refusal) != 1 {
					t.Fatalf("the refusal must be emitted once\ngot:\n%s", body)
				}
			}
		})
	}
}
