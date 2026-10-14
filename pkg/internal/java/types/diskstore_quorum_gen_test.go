// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/header"
	"strings"
	"testing"
	"text/template"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// (spec.quorum_write) was parsed, described and registered but no generated line
// ever enforced it. These tests pin the enforcement: the floor read off the
// store's fields, and the gate emitted into DiskStores only for a store that
// declared one.

func quorumField(name string, quorum bool, minDurableCopies int) parser.FieldDescriptor {
	ann := map[string]model.AnnotationValue{}
	if quorum {
		ann["quorum_write"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	}
	if minDurableCopies > 0 {
		ann["min_durable_copies"] = model.AnnotationValue{
			Type: model.AnnotationTypeInt, IntValue: int64(minDurableCopies)}
	}
	return parser.FieldDescriptor{Name: name, Type: "string", Annotations: ann}
}

func TestStoreQuorumDefaultsToTwoWhenUndeclared(t *testing.T) {
	msg := &parser.MessageDescriptor{
		Name:   "CredPair",
		Fields: []parser.FieldDescriptor{quorumField("client_secret", true, 0)},
	}
	quorum, min := storeQuorum(msg)
	if !quorum {
		t.Fatal("a field declaring quorum_write must gate the store")
	}
	if min != 2 {
		t.Fatalf("quorum_write with no min_durable_copies must default to 2, got %d", min)
	}
}

func TestStoreQuorumHonoursTheDeclaredSingleNodeFloor(t *testing.T) {
	msg := &parser.MessageDescriptor{
		Name:   "CredPair",
		Fields: []parser.FieldDescriptor{quorumField("client_secret", true, 1)},
	}
	quorum, min := storeQuorum(msg)
	if !quorum || min != 1 {
		t.Fatalf("min_durable_copies:1 is the deliberate single-node setting, got quorum=%v min=%d", quorum, min)
	}
}

// A row is written whole, so the strictest declared floor is the one the write
// has to clear; taking the first or the last would let a 3-copy field ship under
// a 2-copy gate depending on field order.
func TestStoreQuorumTakesTheStrictestFloor(t *testing.T) {
	msg := &parser.MessageDescriptor{
		Name: "CredPair",
		Fields: []parser.FieldDescriptor{
			quorumField("client_secret", true, 3),
			quorumField("refresh_token", true, 2),
		},
	}
	if _, min := storeQuorum(msg); min != 3 {
		t.Fatalf("strictest floor must govern, got %d", min)
	}
}

func TestStoreQuorumIsAbsentWithoutTheAnnotation(t *testing.T) {
	msg := &parser.MessageDescriptor{
		Name:   "CredPair",
		Fields: []parser.FieldDescriptor{quorumField("client_secret", false, 0)},
	}
	if quorum, min := storeQuorum(msg); quorum || min != 0 {
		t.Fatalf("a store declaring no quorum_write must get no gate, got quorum=%v min=%d", quorum, min)
	}
}

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
