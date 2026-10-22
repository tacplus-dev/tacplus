// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"strings"
	"testing"
	"text/template"

	"github.com/tacplus-dev/tacplus/templates"
)

// (spec.quorum_write) was parsed, described and registered but no generated line
// ever enforced it. These tests pin the gate emitted into DiskStores only for a
// store that declared one; the floor read off the store's fields is pinned by
// parser.StoreQuorum's tests.

func renderDiskStoresForTest(t *testing.T, stores []javaDiskStore) string {
	t.Helper()
	tmplStr, err := loadTemplate("diskstore_stores.java.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	tmpl, err := template.New("diskstore_stores").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, javaDiskStoreData{
		Header:      "// header",
		Package:     "com.example.chassis.diskstore.stores",
		ChassisBase: "com.example.chassis",
		Stores:      stores,
	}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	return out.String()
}

func quorumStore(quorum bool, min int) javaDiskStore {
	return javaDiskStore{
		StoreName:        "cred_pair",
		MethodName:       "CredPair",
		ConstName:        "CRED_PAIR",
		TypeFqn:          "com.example.types.CredPair",
		HelpersFqn:       "com.example.types.Helpers",
		ToJsonFn:         "credPairToJson",
		FromJsonFn:       "credPairFromJson",
		ValidateFn:       "validateCredPair",
		KeyGetter:        "getClientId",
		QuorumWrite:      quorum,
		MinDurableCopies: min,
	}
}

func TestDiskStoresEmitsTheQuorumGateForADeclaringStore(t *testing.T) {
	rendered := renderDiskStoresForTest(t, []javaDiskStore{quorumStore(true, 2)})
	for _, want := range []string{
		"import com.example.chassis.diskstore.QuorumGate;",
		"public static final int MIN_DURABLE_COPIES_CRED_PAIR = 2;",
		"public static void requireCredPairQuorum(int durableCopies) {",
		"QuorumGate.require(\"cred_pair\", durableCopies, MIN_DURABLE_COPIES_CRED_PAIR);",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered DiskStores missing quorum marker %q:\n%s", want, rendered)
		}
	}
}

func TestDiskStoresEmitsNoQuorumGateWithoutTheAnnotation(t *testing.T) {
	rendered := renderDiskStoresForTest(t, []javaDiskStore{quorumStore(false, 0)})
	for _, unwanted := range []string{
		"MIN_DURABLE_COPIES_CRED_PAIR",
		"requireCredPairQuorum",
		"QuorumGate.require(",
	} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("undeclared store got quorum surface %q:\n%s", unwanted, rendered)
		}
	}
}

// The gate is a refusal, not a degrade: FAILED_PRECONDITION, and nothing that
// lowers the floor to whatever the cluster can supply.
func TestQuorumGateTemplateRefusesBelowTheFloor(t *testing.T) {
	src, err := templates.FS.ReadFile("java/chassis/diskstore/QuorumGate.java.tmpl")
	if err != nil {
		t.Fatalf("read QuorumGate template: %v", err)
	}
	rendered := string(src)
	for _, want := range []string{
		"public static void require(String store, int durableCopies, int minDurableCopies)",
		"if (durableCopies < minDurableCopies)",
		"throw DiskStoreException.belowQuorum(store, durableCopies, minDurableCopies);",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("QuorumGate template missing %q:\n%s", want, rendered)
		}
	}

	exc, err := templates.FS.ReadFile("java/chassis/diskstore/DiskStoreException.java.tmpl")
	if err != nil {
		t.Fatalf("read DiskStoreException template: %v", err)
	}
	for _, want := range []string{
		"public static final int FAILED_PRECONDITION = 9;",
		"public static DiskStoreException belowQuorum(String store, int durableCopies, int minDurableCopies)",
		"new DiskStoreException(FAILED_PRECONDITION,",
	} {
		if !strings.Contains(string(exc), want) {
			t.Fatalf("DiskStoreException template missing %q:\n%s", want, string(exc))
		}
	}
}
