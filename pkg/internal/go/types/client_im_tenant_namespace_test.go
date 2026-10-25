// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"go/format"
	"strings"
	"testing"
	"text/template"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
)

// TestGoClientIdentityManagerTenantNamespace pins #139 on the Go client_im:
// SetTenantNamespace drops the cached tokens and is sent as tenant_namespace on
// the refresh -> access mint only, a mint in flight across a switch is not
// cached, and AccessToken reads the cached access token without minting.
func TestGoClientIdentityManagerTenantNamespace(t *testing.T) {
	tmplStr, err := loadChassisTemplate("client_im/manager.go.tmpl")
	if err != nil {
		t.Fatalf("load client_im manager template: %v", err)
	}
	tmpl, err := template.New("client_im_manager").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse client_im manager template: %v", err)
	}
	for _, hasMinter := range []bool{true, false} {
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, map[string]any{
			"TokenRequestTypesPath":   "example.com/m/types/sys/tokenrequest",
			"TokenTypesPath":          "example.com/m/types/sys/token",
			"HasClientTokenExchanger": hasMinter,
			"ClientMintTypesPath":     "example.com/m/types/gateway",
			"ClientMintServiceName":   "Mint",
			"ClientTokenMintMethod":   "MintToken",
		}); err != nil {
			t.Fatalf("execute client_im manager template: %v", err)
		}
		src, err := format.Source(buf.Bytes())
		if err != nil {
			t.Fatalf("rendered client_im manager (minter=%v) is not valid Go: %v", hasMinter, err)
		}
		out := string(src)
		wants := []string{
			"func (m *ClientIdentityManager) SetTenantNamespace(ns string) {",
			"\tm.tenantNamespace = ns\n\tm.tenantEpoch++\n\tm.accessToken = nil\n\tm.tokens = make(map[string]*tokenEntry)\n",
			"func (m *ClientIdentityManager) AccessToken() string {",
			// access -> service carries no namespace; refresh -> access does.
			`m.mintCall(ctx, cachedAccess, []string{audience}, "")`,
			"m.mintCall(ctx, refresh, []string{audience}, ns)",
			"m.mintCall(ctx, refresh, nil, ns)",
			"if epoch == m.tenantEpoch {\n\t\tm.accessToken = accessEntry\n",
		}
		if hasMinter {
			wants = append(wants, "TenantNamespace: tenantNamespace,")
		}
		for _, want := range wants {
			if !strings.Contains(out, want) {
				t.Errorf("rendered client_im manager (minter=%v) missing %q", hasMinter, want)
			}
		}
	}
}
