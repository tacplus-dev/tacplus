// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"strings"
	"testing"
	"text/template"
)

func TestRustProxyDispatchRendersTraceRPCMapping(t *testing.T) {
	tmplStr, err := loadTemplate("proxy_dispatch.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}

	tmpl, err := template.New("proxy_dispatch").Delims("<<", ">>").Parse(tmplStr)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	var buf bytes.Buffer
	err = tmpl.Execute(&buf, proxyDispatchData{
		Arms: []proxyDispatchArm{
			{
				GrpcPath: "/contract.ContractApi/AuditUnary",
				AccountingMethod: `AccountingMethod {
            contract: services_pb::ContractRpCs::ContractRpcsAuditUnary as i32,
            ..Default::default()
        }`,
			},
		},
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}

	out := buf.String()
	for _, want := range []string{
		"use crate::types::generated::accounting_method::accounting_method::AccountingMethod;",
		"use crate::types::generated::services::services as services_pb;",
		"pub fn trace_rpc_for_path(grpc_path: &str) -> Option<AccountingMethod>",
		`"/contract.ContractApi/AuditUnary" => Some(AccountingMethod`,
		"contract: services_pb::ContractRpCs::ContractRpcsAuditUnary as i32",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rendered proxy dispatch missing %q:\n%s", want, out)
		}
	}
}
