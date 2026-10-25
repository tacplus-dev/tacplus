// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"strings"
	"testing"
	"text/template"
)

func TestRustClientIdentityManagerDeclaresAccessTokenPurpose(t *testing.T) {
	tmplStr := loadRustLibTemplate(t, "rust/chassis/client_im/src/manager.rs.tmpl")
	tmpl, err := template.New("client_im_manager").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse client_im manager template: %v", err)
	}

	var out bytes.Buffer
	if err := tmpl.Execute(&out, map[string]any{
		"HasClientTokenExchanger":     true,
		"CrateBasePathUnderscore":     "demo_model",
		"ClientTokenExchangerPackage": "gateway",
		"ClientMintClientModule":      "mint_client",
		"ClientMintClientType":        "MintClient",
		"ClientTokenMintMethod":       "mint_token",
	}); err != nil {
		t.Fatalf("execute client_im manager template: %v", err)
	}
	rendered := out.String()

	for _, want := range []string{
		"use demo_model_types::sys::token::token::TokenPurpose;",
		"token_purpose: TokenPurpose::Access as i32,",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered client_im manager missing %q:\n%s", want, rendered)
		}
	}

	requestIdx := strings.Index(rendered, "TokenMintRequest {")
	purposeIdx := strings.Index(rendered, "token_purpose: TokenPurpose::Access as i32,")
	serviceAudsIdx := strings.Index(rendered, "service_auds: service_auds.to_vec(),")
	if requestIdx < 0 || purposeIdx < 0 || serviceAudsIdx < 0 {
		t.Fatalf("rendered client_im manager missing batch mint request markers:\n%s", rendered)
	}
	if !(requestIdx < purposeIdx && purposeIdx < serviceAudsIdx) {
		t.Fatalf("client_im batch mint request must declare token_purpose with service_auds:\n%s", rendered)
	}
}

// TestRustClientIdentityManagerTenantNamespace pins #139 on the Rust client_im:
// set_tenant_namespace drops the cached tokens and is sent as tenant_namespace
// on the refresh → access mint only, a mint in flight across a switch is not
// cached, and access_token reads the cached access token without minting.
func TestRustClientIdentityManagerTenantNamespace(t *testing.T) {
	tmplStr := loadRustLibTemplate(t, "rust/chassis/client_im/src/manager.rs.tmpl")
	tmpl, err := template.New("client_im_manager").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse client_im manager template: %v", err)
	}
	for _, hasMinter := range []bool{true, false} {
		var out bytes.Buffer
		if err := tmpl.Execute(&out, map[string]any{
			"HasClientTokenExchanger":     hasMinter,
			"CrateBasePathUnderscore":     "demo_model",
			"ClientTokenExchangerPackage": "gateway",
			"ClientMintClientModule":      "mint_client",
			"ClientMintClientType":        "MintClient",
			"ClientTokenMintMethod":       "mint_token",
		}); err != nil {
			t.Fatalf("execute client_im manager template: %v", err)
		}
		rendered := out.String()
		wants := []string{
			"pub async fn set_tenant_namespace(&self, ns: Option<String>) {",
			"        tenant.namespace = ns;\n        tenant.epoch += 1;\n        *self.access_token.write().await = None;\n        self.tokens.write().await.clear();\n",
			"pub async fn access_token(&self) -> Option<String> {",
			// access → service carries no namespace; refresh → access does.
			"self.mint_call(access, &auds, None).await?",
			"self.mint_call(refresh, &auds, ns).await?",
			"self.mint_call(refresh, &[], ns).await?",
			"if tenant.epoch == epoch {\n            *self.access_token.write().await = Some(access_entry);\n",
		}
		if hasMinter {
			wants = append(wants, "tenant_namespace: tenant_namespace.unwrap_or_default(),")
		}
		for _, want := range wants {
			if !strings.Contains(rendered, want) {
				t.Errorf("rendered client_im manager (minter=%v) missing %q", hasMinter, want)
			}
		}
	}
}
