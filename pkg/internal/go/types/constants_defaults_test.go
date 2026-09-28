// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// TestWriteDefaults_StringConstUsesEmptyStringZero is a regression test for the
// gitlab-connector fanout break: a field whose (spec.default_const) references a
// string constant must generate a string-typed zero check/assignment (`== ""` /
// `= ""`), not the numeric `== 0` / `= 0`. The bug was that string constants were
// parsed with Type "" (instead of "string"), so constTypeToAnnKey fell through to
// default_int and emitted `v.GitlabUrl == 0` — a compile error for a string field
// in every consumer that pulled the connector model.
func TestWriteDefaults_StringConstUsesEmptyStringZero(t *testing.T) {
	// Type "" reproduces the original failure mode: the parser historically
	// encoded spec.string_const with an empty Type, which mis-mapped to the
	// numeric default. Generation must still emit a string-typed zero.
	pf := model.ProtoFile{
		Constants: []model.Constant{
			{Name: "DEFAULT_GITLAB_URL", Type: "", Value: `"https://gitlab.com"`},
		},
		Messages: []model.Message{
			{
				Name: "TenantConfig",
				Fields: []model.Field{
					{
						Name: "gitlab_url",
						Type: "string",
						Annotations: map[string]model.AnnotationValue{
							"default_const": {StringValue: "DEFAULT_GITLAB_URL"},
						},
					},
				},
			},
		},
	}

	var b strings.Builder
	writeDefaults(&b, pf)
	got := b.String()

	if !strings.Contains(got, `if v.GitlabUrl == "" {`) {
		t.Errorf("expected string-typed default check `if v.GitlabUrl == \"\" {`, got:\n%s", got)
	}
	if strings.Contains(got, "v.GitlabUrl == 0") {
		t.Errorf("string field default must not use numeric zero `== 0`, got:\n%s", got)
	}

	var u strings.Builder
	writeUndefaults(&u, pf)
	gotU := u.String()
	if !strings.Contains(gotU, `v.GitlabUrl = ""`) {
		t.Errorf("expected string-typed undefault assignment `v.GitlabUrl = \"\"`, got:\n%s", gotU)
	}
	if strings.Contains(gotU, "v.GitlabUrl = 0") {
		t.Errorf("string field undefault must not assign numeric zero `= 0`, got:\n%s", gotU)
	}
}
