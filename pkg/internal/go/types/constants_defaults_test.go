// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
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
	writeDefaults(&b, pf, messagesWithDefaults([]model.ProtoFile{pf}))
	got := b.String()

	if !strings.Contains(got, `if v.GitlabUrl == "" {`) {
		t.Errorf("expected string-typed default check `if v.GitlabUrl == \"\" {`, got:\n%s", got)
	}
	if strings.Contains(got, "v.GitlabUrl == 0") {
		t.Errorf("string field default must not use numeric zero `== 0`, got:\n%s", got)
	}

	var u strings.Builder
	writeUndefaults(&u, pf, messagesWithDefaults([]model.ProtoFile{pf}))
	gotU := u.String()
	if !strings.Contains(gotU, `v.GitlabUrl = ""`) {
		t.Errorf("expected string-typed undefault assignment `v.GitlabUrl = \"\"`, got:\n%s", gotU)
	}
	if strings.Contains(gotU, "v.GitlabUrl = 0") {
		t.Errorf("string field undefault must not assign numeric zero `= 0`, got:\n%s", gotU)
	}
}

// TestWriteDefaults_CrossPackageSubMessageAppliesDefaults is the grpcconfig
// defect: Config (package grpcconfig) holds a DiskPersistenceConfig from package
// diskstore, which carries defaults. Config's Defaults must call the diskstore
// Defaults, or the nested defaults never reach validation.
func TestWriteDefaults_CrossPackageSubMessageAppliesDefaults(t *testing.T) {
	allFiles := []model.ProtoFile{
		{
			Package: "diskstore",
			Messages: []model.Message{
				{
					Name: "DiskPersistenceConfig",
					Fields: []model.Field{
						{
							Name: "roll_interval_seconds", Type: "int64",
							Annotations: map[string]model.AnnotationValue{"default_int": {IntValue: 86400}},
						},
						{Name: "archive", Type: "ArchiveConfig", IsMessage: true},
					},
				},
				{
					Name: "ArchiveConfig",
					Fields: []model.Field{
						{
							Name: "checkpoint_interval_seconds", Type: "int64",
							Annotations: map[string]model.AnnotationValue{"default_int": {IntValue: 86400}},
						},
					},
				},
			},
		},
		{
			Package: "grpcconfig",
			Messages: []model.Message{
				{
					Name: "Config",
					Fields: []model.Field{
						{Name: "disk_persistence", Type: "DiskPersistenceConfig", TypePackage: "diskstore", IsMessage: true},
					},
				},
			},
		},
	}
	grpcconfig := allFiles[1]

	var b strings.Builder
	writeDefaults(&b, grpcconfig, messagesWithDefaults(allFiles))
	got := b.String()

	want := "if v.DiskPersistence != nil { diskstore.DiskPersistenceConfigDefaults(v.DiskPersistence) }"
	if !strings.Contains(got, want) {
		t.Errorf("expected cross-package sub-message call %q, got:\n%s", want, got)
	}
}
