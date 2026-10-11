// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

func TestPythonPublicViewHelper(t *testing.T) {
	pf := model.ProtoFile{Package: "prospectdemo", Messages: []model.Message{
		{Name: "Prospect", Annotations: map[string]model.AnnotationValue{"public_view": {Type: model.AnnotationTypeBool, BoolValue: true}},
			Fields: []model.Field{{Name: "company_name", Type: "string", Number: 1}, {Name: "deal_notes", Type: "string", Number: 2}}},
		{Name: "Plain", Fields: []model.Field{{Name: "a", Type: "string", Number: 1}}},
	}}
	var b strings.Builder
	writePublicView(&b, pf, "prospect")
	got := b.String()
	for _, want := range []string{
		"def prospect_to_public(msg: prospect_pb2.Prospect) -> prospect_pb2.ProspectPublic:",
		"    out = prospect_pb2.ProspectPublic()\n    out.ParseFromString(msg.SerializeToString())\n    out.DiscardUnknownFields()\n    return out\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, leak := range []string{"deal_notes", "Plain", "plain"} {
		if strings.Contains(got, leak) {
			t.Errorf("%q must not appear in the public view helper:\n%s", leak, got)
		}
	}

	b.Reset()
	writePublicView(&b, model.ProtoFile{Messages: pf.Messages[1:]}, "prospect")
	if b.Len() != 0 {
		t.Fatalf("a file with no public_view message must emit nothing, got:\n%s", b.String())
	}
}
