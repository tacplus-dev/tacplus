// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/publicsuffix"
)

// The generated utils/publicsuffix.go, run with the real Go toolchain over
// the shared vectors (tacplus#224): a public suffix and a malformed name are
// refused with the shared texts, a registrable name accepted; an
// address-shaped value refused.
func TestGeneratedGoRegistrableDomainAndNotAddress(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not installed; skipping the executable assert_registrable_domain check")
	}
	src := strings.Replace(PublicSuffixGo(), "package utils", "package main", 1)
	src = strings.Replace(src, `protoerrors "ERRORS_IMPORT_PATH"`, `"errors"
	"fmt"
	"os"`, 1)
	src = strings.ReplaceAll(src, "protoerrors.NewErrMissingInvalidData(nil, ", "errors.New(")
	var in strings.Builder
	for _, v := range publicsuffix.Vectors {
		fmt.Fprintf(&in, "\t%q,\n", v.In)
	}
	var addr strings.Builder
	for _, v := range publicsuffix.AddressVectors {
		fmt.Fprintf(&addr, "\t%q,\n", v.In)
	}
	src += `
func main() {
	for _, v := range []string{
` + in.String() + `	} {
		if err := ValidateAssertRegistrableDomain(v); err != nil {
			fmt.Fprintln(os.Stdout, err.Error())
		} else {
			fmt.Fprintln(os.Stdout, "accept")
		}
	}
	for _, v := range []string{
` + addr.String() + `	} {
		fmt.Fprintln(os.Stdout, ValidateAssertNotAddress(v) != nil)
	}
}
`
	dir := t.TempDir()
	path := filepath.Join(dir, "main.go")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("go", "run", path).CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, out)
	}
	got := strings.Split(strings.TrimSpace(string(out)), "\n")
	var want []string
	for _, v := range publicsuffix.Vectors {
		want = append(want, v.Outcome())
	}
	for _, v := range publicsuffix.AddressVectors {
		want = append(want, fmt.Sprint(v.Refused))
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("outcomes differ\ngot:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// The field checks the Go validation emitter writes for the three
// annotations, and the shapes it refuses at generation.
func TestGoValidationEmitsSignInAsserts(t *testing.T) {
	msg := model.Message{Name: "Conf", Fields: []model.Field{
		{Name: "domain", Type: "string", Annotations: map[string]model.AnnotationValue{
			"assert_registrable_domain": {Type: model.AnnotationTypeBool, BoolValue: true}}},
		{Name: "subjects", Type: "string", IsRepeated: true, Annotations: map[string]model.AnnotationValue{
			"assert_not_address": {Type: model.AnnotationTypeBool, BoolValue: true}}},
		{Name: "protocol", Type: "IdpProtocol", IsEnum: true, Annotations: map[string]model.AnnotationValue{
			"assert_enum_not_in": {Type: model.AnnotationTypeIntSlice, IntSliceValue: []int32{2}}}},
	}}
	code, err := renderGoValidationForTest(msg)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"utils.ValidateAssertRegistrableDomain(m.Domain)",
		"field Domain must be a registrable domain name",
		"for i, v := range m.Subjects {",
		"utils.ValidateAssertNotAddress(v)",
		"if int32(m.Protocol) == 2 {",
		"field Protocol must not be %s",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("generated validation lacks %q\n%s", want, code)
		}
	}
	for _, bad := range []model.Field{
		{Name: "n", Type: "int64", Annotations: map[string]model.AnnotationValue{"assert_registrable_domain": {Type: model.AnnotationTypeBool, BoolValue: true}}},
		{Name: "n", Type: "string", Annotations: map[string]model.AnnotationValue{"assert_enum_not_in": {Type: model.AnnotationTypeIntSlice, IntSliceValue: []int32{1}}}},
		{Name: "e", Type: "E", IsEnum: true, Annotations: map[string]model.AnnotationValue{"assert_enum_not_in": {Type: model.AnnotationTypeIntSlice, IntSliceValue: []int32{0}}}},
	} {
		if _, err := renderGoValidationForTest(model.Message{Name: "Bad", Fields: []model.Field{bad}}); err == nil {
			t.Errorf("field %+v: no generation error", bad)
		}
	}
}

func renderGoValidationForTest(msg model.Message) (string, error) {
	var b strings.Builder
	err := generateValidationHelpers(&b, msg)
	return b.String(), err
}
