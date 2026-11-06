// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// #313: a stored value never keeps a trace. Save<T> and Record<T> (and so
// Snapshot<T>) persist a copy with the injected trace_id cleared, without
// touching the caller's message; a type with no injected trace_id renders as
// before.

func TestGoDiskStoreTraceFieldDetection(t *testing.T) {
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
		if got := diskStoreCarriesTraceID(tc.fields); got != tc.want {
			t.Errorf("%s: diskStoreCarriesTraceID = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestGoDiskStoresClearTheTraceOfATracedStore(t *testing.T) {
	store := goQuorumStore(false, 0)
	store.ClearTrace = true
	rendered := renderGoDiskStoresForTest(t, store)
	for _, want := range []string{
		`"google.golang.org/protobuf/proto"`,
		"// A stored value never keeps a trace (#313).",
		"msg = proto.Clone(msg).(*vault_pb.CredPair)",
		`msg.TraceId = ""`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered diskstore.go missing trace clear %q:\n%s", want, rendered)
		}
	}
	// Both write paths (Save and Record) clear; Snapshot goes through Record.
	if n := strings.Count(rendered, `msg.TraceId = ""`); n != 2 {
		t.Fatalf("want the trace cleared in Save and Record (2), got %d:\n%s", n, rendered)
	}
}

func TestGoDiskStoresLeaveAnUntracedStoreAlone(t *testing.T) {
	rendered := renderGoDiskStoresForTest(t, goQuorumStore(false, 0))
	for _, unwanted := range []string{"TraceId", "proto.Clone", "google.golang.org/protobuf/proto", "#313"} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("untraced store got trace surface %q:\n%s", unwanted, rendered)
		}
	}
}
