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

// auth_any_user means "any authenticated user can call this RPC". The optional
// target_namespace_field on such a method is an authored FIXED namespace
// literal, not a request field name; with none authored there is nothing to
// compare against, so the gate is authentication alone. Defaulting the target to
// "/" instead gated on isAtOrBelow(callerNamespace, "/"), true only for a caller
// sitting exactly at "/" — every real caller denied.
func TestJavaAuthAnyUserNamespaceGate(t *testing.T) {
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
			wantCall:    "checkAuthenticate();",
			notWant:     `checkAuthenticate("`,
		},
		{
			name:                 "any user with declared literal keeps the fixed namespace gate",
			authAnyUser:          true,
			targetNamespaceField: "/tenants/acme",
			wantCall:             `checkAuthenticate("/tenants/acme");`,
			notWant:              "checkAuthenticate();",
		},
		{
			name:     "entitled method still authorizes",
			wantCall: `checkAuth("ContractApi/Read", request);`,
			notWant:  "checkAuthenticate(",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rendered := generateJavaGRPCWrapper(
				&parser.MethodInfo{
					Name:                 "Read",
					InputType:            "ContractRequest",
					OutputType:           "ContractResponse",
					FullInputType:        "contractclient.ContractRequest",
					FullOutputType:       "contractclient.ContractResponse",
					AuthAnyUser:          tc.authAnyUser,
					TargetNamespaceField: tc.targetNamespaceField,
				},
				"ContractApi",
				"contract",
				"Contractclient.ContractRequest",
				"Contractclient.ContractResponse",
				"Helpers",
				"Helpers",
				tc.authAnyUser,
				false,
				map[string]service.AccountingInfo{},
				"contractclient",
				"contract",
				map[string]string{},
				proto.SkippedTraceTypes{},
				nil,
				nil,
				nil,
			)

			if !strings.Contains(rendered, tc.wantCall) {
				t.Fatalf("rendered Java wrapper missing %q:\n%s", tc.wantCall, rendered)
			}
			if strings.Contains(rendered, tc.notWant) {
				t.Fatalf("rendered Java wrapper must not contain %q:\n%s", tc.notWant, rendered)
			}
		})
	}
}

// The no-arg checkAuthenticate() helper is what the bare auth_any_user gate
// calls: it must authenticate with NO namespace target, not fall back to "/".
func TestJavaCheckAuthenticateNoArgPassesNoNamespace(t *testing.T) {
	tmpl, err := loadTemplate("servicewrapper.java.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate servicewrapper.java.tmpl: %v", err)
	}
	if !strings.Contains(tmpl, "return checkAuthenticate(null);") {
		t.Fatalf("no-arg checkAuthenticate() must delegate with a null target namespace:\n%s", tmpl)
	}
}
