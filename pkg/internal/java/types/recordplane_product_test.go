// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/collection"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
)

// The record plane's S2S half refuses a product's caller on every rpc shape,
// before any pin or the handler (#475, products-layer-2026-11-08): its rows are
// tenants' and the platform's, and a product never reaches a tenant's data but
// as the custodian of what the tenant sends it. Another S2S service does not
// carry the refusal: a product calling /system is an allowed edge. Twin of Go's
// TestGoRecordPlaneRefusesProductCaller.
func TestJavaRecordPlaneRefusesProductCaller(t *testing.T) {
	const msg = `"QueryRows is the record plane, whose rows are tenants' and the platform's: a product's caller never reads or writes it (products-layer-2026-11-08)"`
	const cond = "if (__planeTok != null && !__planeTok.product().isEmpty()) {"
	const refusal = "new ProtoException(ProtoException.PERMISSION_DENIED, " + msg + ")"
	for _, tc := range []struct {
		name           string
		client, server bool
		resolved       *collection.Resolved
		output         string
		// fail is how the shape fails: a throw inside a unary-request shape's
		// try, an onError plus a no-op observer on a request stream.
		fail string
	}{
		{name: "unary", fail: "throw " + refusal + ";"},
		{name: "page", output: "MailPage", resolved: &collection.Resolved{
			Mode: "PAGE", OrderBy: []string{"id"}, DefaultPageSize: 50, MaxPageSize: 10000,
			PageItemType: "mail.MailItem", PageItemsField: "items",
		}, fail: "throw " + refusal + ";"},
		{name: "server streaming", server: true, fail: "throw " + refusal + ";"},
		{name: "watch", server: true, output: "MailWatchEvent", resolved: &collection.Resolved{
			Mode: "WATCH", WatchOneOf: "event", WatchItemType: "mail.MailItem",
			WatchChangeType: "mail.MailItem", ReplayDepth: 64, IdleSecs: 300,
			WatchRevisionField: "revision", WatchHasFromRevision: true,
		}, fail: "throw " + refusal + ";"},
		{name: "client streaming", client: true, fail: "responseObserver.onError(toGrpcError(" + refusal + "));"},
		{name: "bidi", client: true, server: true, fail: "responseObserver.onError(toGrpcError(" + refusal + "));"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := javaRequestScopeMethod("QueryRows")
			m.ClientStreaming, m.ServerStreaming = tc.client, tc.server
			if tc.output != "" {
				m.OutputType, m.FullOutputType = tc.output, "mail."+tc.output
			}
			if tc.resolved != nil {
				m.Collection = &parser.CollectionDescriptor{Mode: tc.resolved.Mode}
				m.Resolved = tc.resolved
			}
			for _, plane := range []bool{true, false} {
				got := generateJavaGRPCWrapperSurface(
					m, "Mail", "mail",
					"MailOuterClass.MailRequest", "MailOuterClass."+m.OutputType,
					"Helpers", "Helpers",
					false, false, nil,
					"mail", "services", nil, proto.SkippedTraceTypes{},
					javaRequestScopeSanitizer(), nil, nil, false, plane, false)
				has := strings.Contains(got, cond) && strings.Contains(got, tc.fail)
				if has != plane {
					t.Fatalf("record plane %v: refusal emitted %v\ngot:\n%s", plane, has, got)
				}
				if !plane {
					continue
				}
				at := strings.Index(got, refusal)
				// Before the handler, and before the request-scope pins
				// ((spec.principal)/(spec.namespace) on MailRequest) and the
				// caller's binding, on every shape.
				for _, after := range []string{"handler.queryRows(", "__reqPrincipalTok", "Caller caller = Auth.callerFromContext();"} {
					if i := strings.Index(got, after); i >= 0 && at > i {
						t.Fatalf("the refusal must precede %q\ngot:\n%s", after, got)
					}
				}
				if strings.Count(got, refusal) != 1 {
					t.Fatalf("the refusal must be emitted once\ngot:\n%s", got)
				}
			}
		})
	}
}
