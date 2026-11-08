// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// #313: a stored value never keeps a trace. save<T> and record<T> (and so
// snapshot<T>) persist a copy built with the injected trace_id cleared, without
// touching the caller's message; a type with no injected trace_id renders as
// before.

func TestJavaDiskStoreTraceFieldDetection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields []parser.FieldDescriptor
		want   bool
	}{
		{"injected", []parser.FieldDescriptor{{Name: "id", Number: 1}, {Name: "trace_id", Number: 200}, {Name: "noop", Number: 201}}, true},
		{"none", []parser.FieldDescriptor{{Name: "id", Number: 1}}, false},
		{"own trace_id", []parser.FieldDescriptor{{Name: "trace_id", Number: 3}}, false},
		{"noop only", []parser.FieldDescriptor{{Name: "noop", Number: 201}}, false},
	} {
		if got := javaDiskStoreCarriesTraceID(tc.fields); got != tc.want {
			t.Errorf("%s: javaDiskStoreCarriesTraceID = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestJavaDiskStoresClearTheTraceOfATracedStore(t *testing.T) {
	store := quorumStore(false, 0)
	store.ClearTrace = true
	rendered := renderDiskStoresForTest(t, []javaDiskStore{store})
	for _, want := range []string{
		"// A stored value never keeps a trace (#313);",
		"com.example.types.Helpers.credPairToJson(msg.toBuilder().clearTraceId().build())",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered DiskStores missing trace clear %q:\n%s", want, rendered)
		}
	}
	// Both write paths (save and record) clear; snapshot goes through record.
	if n := strings.Count(rendered, ".clearTraceId()"); n != 2 {
		t.Fatalf("want the trace cleared in save and record (2), got %d:\n%s", n, rendered)
	}
}

func TestJavaDiskStoresLeaveAnUntracedStoreAlone(t *testing.T) {
	rendered := renderDiskStoresForTest(t, []javaDiskStore{quorumStore(false, 0)})
	for _, unwanted := range []string{"clearTraceId", "toBuilder", "#313"} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("untraced store got trace surface %q:\n%s", unwanted, rendered)
		}
	}
}
