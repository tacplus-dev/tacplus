// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
	"text/template"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
)

// The Rust port of Java's quorum write gate (P3, ae2e9ad): a store declaring
// (spec.quorum_write) gets its declared floor as a constant and a
// require_<store>_quorum that calls the chassis gate; a store that declares
// none gets neither. The floor itself is parser.StoreQuorum, shared with Go and
// Java. The chassis gate carries its own #[cfg(test)] tests.

func renderRustDiskStoresForTest(t *testing.T, store diskStore) string {
	t.Helper()
	tmplStr, err := loadTemplate("diskstore_stores.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	tmpl := template.Must(template.New("diskstore_stores").Parse(tmplStr))
	var out strings.Builder
	if err := tmpl.Execute(&out, diskStoreData{Header: "// header", Stores: []diskStore{store}}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	return out.String()
}

func rustQuorumStore(quorum bool, min int) diskStore {
	return diskStore{
		StoreName:        "cred_pair",
		TypePath:         "crate::types::services::vault::CredPair",
		ToJsonPath:       "crate::types::services::vault::cred_pair_to_json",
		FromJsonPath:     "crate::types::services::vault::cred_pair_from_json",
		KeyField:         "client_id",
		QuorumWrite:      quorum,
		MinDurableCopies: min,
		ConstName:        "CRED_PAIR",
	}
}

func TestRustDiskStoresEmitsTheQuorumGateForADeclaringStore(t *testing.T) {
	rendered := renderRustDiskStoresForTest(t, rustQuorumStore(true, 2))
	for _, want := range []string{
		"pub const MIN_DURABLE_COPIES_CRED_PAIR: u32 = 2;",
		"pub fn require_cred_pair_quorum(durable_copies: u32) -> diskstore::Result<()> {",
		`diskstore::require_quorum("cred_pair", durable_copies, MIN_DURABLE_COPIES_CRED_PAIR)`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered diskstore_stores missing quorum marker %q:\n%s", want, rendered)
		}
	}
}

func TestRustDiskStoresEmitsNoQuorumGateWithoutTheAnnotation(t *testing.T) {
	rendered := renderRustDiskStoresForTest(t, rustQuorumStore(false, 0))
	for _, unwanted := range []string{"MIN_DURABLE_COPIES_CRED_PAIR", "require_cred_pair_quorum", "require_quorum("} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("undeclared store got quorum surface %q:\n%s", unwanted, rendered)
		}
	}
}

// Rust cannot ask a trait object whether it also implements LocalRows, so the
// opt-in Go and Java express by implementing the interface on the store is a
// Handler method here, defaulting to the whole-store baseline.
func TestRustHandlerOffersLocalRowsPerDiskStore(t *testing.T) {
	tmplStr, err := loadTemplate("handler.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	tmpl := template.Must(template.New("handler").Funcs(header.Funcs()).Parse(tmplStr))
	var out strings.Builder
	if err := tmpl.Execute(&out, smTemplateData{
		DiskStores:    []rustDiskStoreRuntimeEntry{{StoreName: "cred_pair", LogField: "cred_pair_log", HandlerGetter: "cred_pair_store_grpc_impl"}},
		HasDiskStores: true,
	}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	want := "    fn cred_pair_local_rows(&self) -> Option<Arc<dyn diskstore::LocalRows>> {\n        None\n    }"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("Handler missing the LocalRows seam %q:\n%s", want, out.String())
	}
}
