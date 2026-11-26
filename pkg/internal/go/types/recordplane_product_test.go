// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
)

// The record plane's S2S half refuses a product's caller on every rpc shape,
// before any pin or the handler (#475, products-layer-2026-11-08): its rows are
// tenants' and the platform's, and a product never reaches a tenant's data but
// as the custodian of what the tenant sends it. Another S2S service does not
// carry the refusal: a product calling /system is an allowed edge.
func TestGoRecordPlaneRefusesProductCaller(t *testing.T) {
	refusal := `status.Error(codes.PermissionDenied, "QueryRows is the record plane, whose rows are tenants' and the platform's: a product's caller never reads or writes it (products-layer-2026-11-08)")`
	for _, tc := range []struct {
		name           string
		client, server bool
		failRet        string
	}{
		{name: "unary", failRet: "return nil,"},
		{name: "server streaming", server: true, failRet: "return"},
		{name: "client streaming", client: true, failRet: "return"},
		{name: "bidi", client: true, server: true, failRet: "return"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := &parser.MethodInfo{
				Name: "QueryRows", InputType: "UsageRequest", OutputType: "UsageResponse",
				FullInputType: "billing.UsageRequest", FullOutputType: "billing.UsageResponse",
				ClientStreaming: tc.client, ServerStreaming: tc.server,
			}
			for _, plane := range []bool{true, false} {
				svc := &parser.ServiceInfo{Name: "Rows", IsS2S: true, Methods: []*parser.MethodInfo{m}}
				if plane {
					svc.SynthesizedKind = parser.SynthesizedKindRecordMap
				}
				got := generateGRPCWrapper(svc, m, "billing", "services", "",
					map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, callerTidSanitizer())
				has := strings.Contains(got, "caller != nil && caller.Token().Product() != \"\" {") &&
					strings.Contains(got, tc.failRet+" "+refusal)
				if has != plane {
					t.Fatalf("record plane %v: refusal emitted %v\ngot:\n%s", plane, has, got)
				}
				if plane {
					if at, call := strings.Index(got, refusal), strings.Index(got, "p.handler.QueryRows("); call >= 0 && at > call {
						t.Fatalf("the refusal must precede the handler call\ngot:\n%s", got)
					}
				}
			}
		})
	}
}
