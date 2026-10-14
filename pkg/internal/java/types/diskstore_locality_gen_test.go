// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// The roll baseline used to be handler.<store>().list(null) verbatim — the WHOLE
// cluster's rows, written into every node's own log. In a clustered service that
// breaks the partial-log invariant recovery leans on: a node's log claims keys it
// does not own, refreshed every roll interval, so "the current owner's log holds
// the newer epoch" no longer bounds staleness. These pin the narrowing seam.

func TestJavaServiceManagerNarrowsTheRollBaselineToLocalRows(t *testing.T) {
	rendered := renderJavaTemplateForTest(t, "ServiceManager.java.tmpl", javaSmTemplateData{
		PackageBase:  "com.example",
		ChassisBase:  "com.example.sys",
		TypesBase:    "com.example.types",
		SmPrefix:     "s2sm",
		ServiceLabel: "demo",
		DiskStores: []javaDiskStoreRuntimeEntry{{
			StoreName:     "cred_pair",
			MethodName:    "CredPair",
			LogField:      "credPairLog",
			HandlerGetter: "getCredPairStoreGrpcImpl",
		}},
		HasDiskStores: true,
	})

	for _, want := range []string{
		"import com.example.sys.diskstore.LocalRows;",
		"var store = handler.getCredPairStoreGrpcImpl();",
		"if (store instanceof LocalRows) {",
		"LocalRows local = (LocalRows) store;",
		"if (local.isLocalRow(rec.key)) {",
		"return scoped;",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Java ServiceManager missing locality marker %q:\n%s", want, rendered)
		}
	}
}

// A store that does not partition keeps the whole-store baseline — the shape
// every service_store had before the seam existed.
func TestJavaServiceManagerKeepsTheWholeStoreBaselineByDefault(t *testing.T) {
	rendered := renderJavaTemplateForTest(t, "ServiceManager.java.tmpl", javaSmTemplateData{
		PackageBase:  "com.example",
		ChassisBase:  "com.example.sys",
		TypesBase:    "com.example.types",
		SmPrefix:     "s2sm",
		ServiceLabel: "demo",
		DiskStores: []javaDiskStoreRuntimeEntry{{
			StoreName:     "cred_pair",
			MethodName:    "CredPair",
			LogField:      "credPairLog",
			HandlerGetter: "getCredPairStoreGrpcImpl",
		}},
		HasDiskStores: true,
	})
	if !strings.Contains(rendered, "return records;") {
		t.Fatalf("rendered Java ServiceManager dropped the unnarrowed fallback:\n%s", rendered)
	}
}

func TestLocalRowsTemplateDeclaresTheOwnershipPredicate(t *testing.T) {
	src, err := templates.FS.ReadFile("java/chassis/diskstore/LocalRows.java.tmpl")
	if err != nil {
		t.Fatalf("read LocalRows template: %v", err)
	}
	for _, want := range []string{
		"public interface LocalRows {",
		"boolean isLocalRow(String key);",
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("LocalRows template missing %q:\n%s", want, string(src))
		}
	}
}
