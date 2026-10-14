// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
)

// See the Python/Go twins: auth_any_user with no authored fixed namespace
// literal is plain authentication. A "/" target gated on
// isAtOrBelow(caller, "/"), which admits only a caller sitting exactly at "/".
func TestTypeScriptAuthAnyUserNamespaceGate(t *testing.T) {
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
			wantCall:    "await this.authenticate(call.metadata);",
			notWant:     "await this.authenticate(call.metadata, ",
		},
		{
			name:                 "any user with declared literal keeps the fixed namespace gate",
			authAnyUser:          true,
			targetNamespaceField: "/tenants/acme",
			wantCall:             `await this.authenticate(call.metadata, "/tenants/acme");`,
			notWant:              "await this.authenticate(call.metadata);",
		},
		{
			name:     "entitled method still authorizes",
			wantCall: "await this.authorize('/contract.ContractApi/Read'",
			notWant:  "await this.authenticate(",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered := generateTsGRPCWrapperMethod(
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
				t.Fatalf("rendered TypeScript wrapper missing %q:\n%s", tc.wantCall, rendered)
			}
			if strings.Contains(rendered, tc.notWant) {
				t.Fatalf("rendered TypeScript wrapper must not contain %q:\n%s", tc.notWant, rendered)
			}
		})
	}
}
