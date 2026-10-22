// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
)

// auth_any_user means "any authenticated user can call this RPC". The optional
// target_namespace_field on such a method is an authored FIXED namespace
// literal, not a request field name; with none authored there is nothing to
// compare against, so the gate is authentication alone. Defaulting the target to
// "/" instead gated on is_at_or_below(caller, "/"), true only for a caller
// sitting exactly at "/" — every real caller denied.
func TestPythonAuthAnyUserNamespaceGate(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "ContractApi"}

	for _, tc := range []struct {
		name                 string
		authAnyUser          bool
		targetNamespaceField string
		wantCall             string
		notWant              string
	}{
		{
			name:        "any user without target namespace field authenticates only",
			authAnyUser: true,
			wantCall:    "self._authenticate(context)",
			notWant:     "self._authenticate(context, ",
		},
		{
			name:                 "any user with declared literal keeps the fixed namespace gate",
			authAnyUser:          true,
			targetNamespaceField: "/tenants/acme",
			wantCall:             `self._authenticate(context, "/tenants/acme")`,
			notWant:              "self._authenticate(context)",
		},
		{
			name:     "entitled method still authorizes",
			wantCall: "self._authorize(context, '/contract.ContractApi/Read'",
			notWant:  "self._authenticate(",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered := generatePyGRPCWrapperMethod(
				svc,
				&parser.MethodInfo{
					Name:                 "Read",
					InputType:            "ReadRequest",
					OutputType:           "ReadResponse",
					FullInputType:        "contract.ReadRequest",
					FullOutputType:       "contract.ReadResponse",
					AuthAnyUser:          tc.authAnyUser,
					TargetNamespaceField: tc.targetNamespaceField,
				},
				"contract",
				"services",
				"contract",
				map[string]service.AccountingInfo{},
			)

			if !strings.Contains(rendered, tc.wantCall) {
				t.Fatalf("rendered Python wrapper missing %q:\n%s", tc.wantCall, rendered)
			}
			if strings.Contains(rendered, tc.notWant) {
				t.Fatalf("rendered Python wrapper must not contain %q:\n%s", tc.notWant, rendered)
			}
		})
	}
}
