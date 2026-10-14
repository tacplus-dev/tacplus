// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
)

func TestTypeScriptServiceWrapperEmitsAccountingForMethodKinds(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "ContractApi", AuthDisabled: true}

	for _, tc := range []struct {
		name   string
		method *parser.MethodInfo
	}{
		{
			name: "unary",
			method: &parser.MethodInfo{
				Name:           "PublicRead",
				InputType:      "ReadRequest",
				OutputType:     "ReadResponse",
				FullInputType:  "contract.ReadRequest",
				FullOutputType: "contract.ReadResponse",
				AuthDisabled:   true,
			},
		},
		{
			name: "server-streaming",
			method: &parser.MethodInfo{
				Name:            "Stream",
				InputType:       "ReadRequest",
				OutputType:      "ReadResponse",
				FullInputType:   "contract.ReadRequest",
				FullOutputType:  "contract.ReadResponse",
				ServerStreaming: true,
				AuthDisabled:    true,
			},
		},
		{
			name: "client-streaming",
			method: &parser.MethodInfo{
				Name:            "Upload",
				InputType:       "ReadRequest",
				OutputType:      "ReadResponse",
				FullInputType:   "contract.ReadRequest",
				FullOutputType:  "contract.ReadResponse",
				ClientStreaming: true,
				AuthDisabled:    true,
			},
		},
		{
			name: "bidi",
			method: &parser.MethodInfo{
				Name:            "Chat",
				InputType:       "ReadRequest",
				OutputType:      "ReadResponse",
				FullInputType:   "contract.ReadRequest",
				FullOutputType:  "contract.ReadResponse",
				ClientStreaming: true,
				ServerStreaming: true,
				AuthDisabled:    true,
			},
		},
		{
			name:   "watch",
			method: tsWatchMethod(),
		},
	} {
		tc.method.AuthDisabled = true
		rendered := generateTsGRPCWrapperMethod(
			svc,
			tc.method,
			"contract",
			"services",
			"contract",
			map[string]service.AccountingInfo{
				"contract": {
					EnumName:    "Contract",
					RPCs:        map[string]bool{tc.method.Name: true},
					RPCOrdinals: map[string]int{tc.method.Name: 1},
				},
			},
		)

		for _, want := range []string{
			"const __tacplusAccountingStart = Date.now();",
			"let __tacplusAccountingError: unknown = null;",
			"__tacplusAccountingError = err;",
			"this.emitAccounting(create(accountingTypes.AccountingMethodSchema, { contract: 1 })",
			"finally {",
		} {
			if !strings.Contains(rendered, want) {
				t.Fatalf("TypeScript %s accounting wrapper missing %q:\n%s", tc.name, want, rendered)
			}
		}
		if tc.name == "unary" && !strings.Contains(rendered, "this.publishTrace(traceId, 3, '', create(accountingTypes.AccountingMethodSchema, { contract: 1 }))") {
			t.Fatalf("TypeScript unary trace wrapper missing typed rpc method:\n%s", rendered)
		}
	}
}
