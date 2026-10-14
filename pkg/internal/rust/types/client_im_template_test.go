// Copyright (c) 2026 Seabeam LLC
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
