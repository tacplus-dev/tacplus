// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// publicViewFile is a file with one public_view message carrying a BACKGROUND
// field and one plain message.
func publicViewFile() model.ProtoFile {
	return model.ProtoFile{Package: "prospectdemo", Messages: []model.Message{
		{Name: "Prospect", Annotations: map[string]model.AnnotationValue{"public_view": {Type: model.AnnotationTypeBool, BoolValue: true}},
			Fields: []model.Field{{Name: "company_name", Type: "string", Number: 1}, {Name: "deal_notes", Type: "string", Number: 2}}},
		{Name: "Plain", Fields: []model.Field{{Name: "a", Type: "string", Number: 1}}},
	}}
}

// ProspectToPublic is serialize + parse-with-DiscardUnknown and names no field,
// so no BACKGROUND field can appear in it; a plain message gets nothing.
func TestGoPublicViewHelper(t *testing.T) {
	var b strings.Builder
	generatePublicViewHelpers(&b, publicViewFile())
	got := b.String()
	for _, want := range []string{
		"func ProspectToPublic(m *Prospect) (*ProspectPublic, error) {",
		"b, err := proto.Marshal(m)",
		"out := &ProspectPublic{}",
		"(proto.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(b, out)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, leak := range []string{"deal_notes", "DealNotes", "Plain"} {
		if strings.Contains(got, leak) {
			t.Errorf("%q must not appear in the public view helper:\n%s", leak, got)
		}
	}

	b.Reset()
	generatePublicViewHelpers(&b, model.ProtoFile{Messages: publicViewFile().Messages[1:]})
	if b.Len() != 0 {
		t.Fatalf("a file with no public_view message must emit nothing, got:\n%s", b.String())
	}
}
