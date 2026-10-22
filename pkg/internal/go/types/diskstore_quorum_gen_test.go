// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"go/format"
	"strings"
	"testing"
	"text/template"
)

// The Go port of Java's quorum write gate (P3, ae2e9ad): a store declaring
// (spec.quorum_write) gets its declared floor as a constant and a
// Require<Type>Quorum that calls the chassis gate; a store that declares none
// gets neither. The floor itself is parser.StoreQuorum, shared with Java and
// Rust; the chassis gate is exercised by the generated quorum_test.go that
// current-generation runs.

func renderGoDiskStoresForTest(t *testing.T, store diskStoreEntry) string {
	t.Helper()
	tmplStr, err := loadTemplate("diskstore_stores.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	tmpl := template.Must(template.New("diskstore_stores").Parse(tmplStr))
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, diskStoreData{
		Header:           "// header",
		DiskStoreLibPath: "example.com/svc/chassis/diskstore",
		Imports:          []diskStoreImport{{Alias: "vault_pb", Path: "example.com/svc/types/services/vault"}},
		Stores:           []diskStoreEntry{store},
		AnyRedo:          true,
	}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		t.Fatalf("rendered diskstore.go does not parse: %v\n%s", err, buf.String())
	}
	return string(formatted)
}

func goQuorumStore(quorum bool, min int) diskStoreEntry {
	return diskStoreEntry{
		StoreName:        "cred_pair",
		TypeName:         "CredPair",
		GoType:           "vault_pb.CredPair",
		ToJSON:           "vault_pb.CredPairToJSON",
		FromJSON:         "vault_pb.CredPairFromJSON",
		Validate:         "vault_pb.ValidateCredPair",
		KeyField:         "ClientId",
		QuorumWrite:      quorum,
		MinDurableCopies: min,
	}
}

func TestGoDiskStoresEmitsTheQuorumGateForADeclaringStore(t *testing.T) {
	rendered := renderGoDiskStoresForTest(t, goQuorumStore(true, 2))
	for _, want := range []string{
		"const MinDurableCopiesCredPair = 2",
		"func RequireCredPairQuorum(durableCopies int) error {",
		`return diskstore.RequireQuorum("cred_pair", durableCopies, MinDurableCopiesCredPair)`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered diskstore.go missing quorum marker %q:\n%s", want, rendered)
		}
	}
}

func TestGoDiskStoresEmitsNoQuorumGateWithoutTheAnnotation(t *testing.T) {
	rendered := renderGoDiskStoresForTest(t, goQuorumStore(false, 0))
	for _, unwanted := range []string{"MinDurableCopiesCredPair", "RequireCredPairQuorum", "RequireQuorum("} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("undeclared store got quorum surface %q:\n%s", unwanted, rendered)
		}
	}
}
