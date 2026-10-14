// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

func TestTypeScriptPublicViewHelper(t *testing.T) {
	pf := model.ProtoFile{Package: "prospectdemo", Messages: []model.Message{
		{Name: "Prospect", Annotations: map[string]model.AnnotationValue{"public_view": {Type: model.AnnotationTypeBool, BoolValue: true}},
			Fields: []model.Field{{Name: "company_name", Type: "string", Number: 1}, {Name: "deal_notes", Type: "string", Number: 2}}},
		{Name: "Plain", Fields: []model.Field{{Name: "a", Type: "string", Number: 1}}},
	}}
	var b strings.Builder
	writePublicView(&b, pf)
	got := b.String()
	for _, want := range []string{
		"export function prospectToPublic(m: Prospect): ProspectPublic {",
		"return fromBinary(ProspectPublicSchema, toBinary(ProspectSchema, m), { readUnknownFields: false });",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, leak := range []string{"deal_notes", "dealNotes", "Plain", "plain"} {
		if strings.Contains(got, leak) {
			t.Errorf("%q must not appear in the public view helper:\n%s", leak, got)
		}
	}

	b.Reset()
	writePublicView(&b, model.ProtoFile{Messages: pf.Messages[1:]})
	if b.Len() != 0 {
		t.Fatalf("a file with no public_view message must emit nothing, got:\n%s", b.String())
	}
}
