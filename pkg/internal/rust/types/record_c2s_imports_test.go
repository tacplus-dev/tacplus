// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// The client-facing record read plane glob-imports both its own package's types
// and the query plane's. Since the record write surface (#163) the plane's
// package also defines Get<T>Request/Get<T>Response, so the reader's own
// request and response types must be imported by name: an explicit import
// shadows both globs, where two globs naming one type make it ambiguous and the
// crate does not compile.
func TestRecordC2SImportsItsOwnTypesExplicitly(t *testing.T) {
	out := renderRecordC2S(t, vaultRecordC2S())
	want := "use crate::types::services::vault::{ GetVaultRowRequest, GetVaultRowResponse, GetVaultRowPageRequest, GetVaultRowPageResponse };"
	if !strings.Contains(out, want) {
		t.Fatalf("record_c2s.rs must import its own request/response types by name:\nwant %s\ngot:\n%s", want, out)
	}
}

// Issue #207: on a plane hosted by a tenant-owned record store the generated
// handler reads from its caller's own tenant's instance, the caller's tid
// claim (never derived from its namespace, #284), and refuses a caller whose
// token carries none.
func TestRecordC2SRoutedReadsTheCallersTenant(t *testing.T) {
	data := vaultRecordC2S()
	data.Routed = true
	out := renderRecordC2S(t, data)
	for _, want := range []string{
		"fn record_c2s_tenant(method: &str, caller: &Caller) -> Result<String, tonic::Status> {",
		"servicemanager::tenant_route::caller_tenant(caller.tid()).ok_or_else(|| {",
		"tonic::Status::permission_denied(format!(",
		"{}: the caller's token carries no tid claim, and the record store is one instance per tenant",
		`let __tenant = record_c2s_tenant("GetVaultRow", __caller)?;`,
		`let __tenant = record_c2s_tenant("GetVaultRowPage", __caller)?;`,
		".list(&__tenant, Some(filter), 1, String::new())",
		".list(&__tenant, Some(filter), req.page_size, req.page_token)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("routed record_c2s.rs missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "tenant_of_caller_namespace") {
		t.Errorf("routed record_c2s.rs derives the caller's tid from its namespace:\n%s", out)
	}
	// The generated tests drive record_c2s_tenant with callers whose namespace
	// names the tenant and whose tid claim is the only source of its tid.
	for _, want := range []string{
		"#[cfg(test)]\nmod record_c2s_tenant_tests {",
		"fn the_tenant_is_the_tid_claim()",
		"fn a_tid_shaped_namespace_never_names_the_tenant()",
		"fn a_tid_shaped_namespace_with_no_tid_claim_is_refused()",
		"fn a_system_caller_is_refused()",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("routed record_c2s.rs missing generated test %q:\n%s", want, out)
		}
	}
	if plain := renderRecordC2S(t, vaultRecordC2S()); strings.Contains(plain, "record_c2s_tenant") || strings.Contains(plain, "__tenant") || strings.Contains(plain, "#[cfg(test)]") {
		t.Errorf("an unrouted plane reads no tenant:\n%s", plain)
	}
}

func vaultRecordC2S() *recordC2SFile {
	return &recordC2SFile{
		StructName: "VaultRecordsGeneratedHandler", TraitName: "VaultRecordsHandler", ServiceName: "VaultRecords",
		TraitPath: "crate::s2sm::records", TypesPath: "crate::types::services::vault", QueryPath: "crate::types::vservices::query",
		Methods: []recordC2SMethod{{
			RecordName: "VaultRow", GetFn: "get_vault_row", ListFn: "get_vault_row_page",
			GetReq: "GetVaultRowRequest", GetResp: "GetVaultRowResponse",
			ListReq: "GetVaultRowPageRequest", ListResp: "GetVaultRowPageResponse",
			FilterType: "VaultRowFilter", KeyField: "row_id", KeyIsString: true,
			GetMethodName: "GetVaultRow", ListMethodNam: "GetVaultRowPage",
		}},
	}
}

func renderRecordC2S(t *testing.T, data *recordC2SFile) string {
	t.Helper()
	dir := t.TempDir()
	config := &tactype.Config{OutputDir: dir}
	if err := generateRecordC2SPlane(config, data); err != nil {
		t.Fatalf("generateRecordC2SPlane: %v", err)
	}
	var out []byte
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && d.Name() == "record_c2s.rs" {
			out, _ = os.ReadFile(p)
		}
		return nil
	})
	return string(out)
}
