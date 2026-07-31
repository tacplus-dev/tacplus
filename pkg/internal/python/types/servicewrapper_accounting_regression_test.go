// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
)

func TestPythonServiceWrapperEmitsAccountingForMethodKinds(t *testing.T) {
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
			method: pyWatchMethod(),
		},
	} {
		tc.method.AuthDisabled = true
		rendered := generatePyGRPCWrapperMethod(
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
			"__tacplus_accounting_start = time.time()",
			"__tacplus_accounting_error = None",
			"__tacplus_accounting_error = e",
			"self._emit_accounting(accounting_method_pb2.AccountingMethod(contract=1)",
			"finally:",
		} {
			if !strings.Contains(rendered, want) {
				t.Fatalf("Python %s accounting wrapper missing %q:\n%s", tc.name, want, rendered)
			}
		}
		if tc.name == "unary" && !strings.Contains(rendered, "method=accounting_method_pb2.AccountingMethod(contract=1)") {
			t.Fatalf("Python unary trace wrapper missing typed rpc method:\n%s", rendered)
		}
	}
}
