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
	dir := t.TempDir()
	config := &tactype.Config{OutputDir: dir}
	data := &recordC2SFile{
		StructName: "VaultRecordsGeneratedHandler", TraitName: "VaultRecordsHandler", ServiceName: "VaultRecords",
		TraitPath: "crate::s2sm::records", TypesPath: "crate::types::services::vault", QueryPath: "crate::types::vservices::query",
		Methods: []recordC2SMethod{{
			RecordName: "VaultRow", GetFn: "get_vault_row", ListFn: "get_vault_row_page",
			GetReq: "GetVaultRowRequest", GetResp: "GetVaultRowResponse",
			ListReq: "GetVaultRowPageRequest", ListResp: "GetVaultRowPageResponse",
			FilterType: "VaultRowFilter", KeyField: "row_id", KeyIsString: true,
		}},
	}
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
	want := "use crate::types::services::vault::{ GetVaultRowRequest, GetVaultRowResponse, GetVaultRowPageRequest, GetVaultRowPageResponse };"
	if !strings.Contains(string(out), want) {
		t.Fatalf("record_c2s.rs must import its own request/response types by name:\nwant %s\ngot:\n%s", want, out)
	}
}
