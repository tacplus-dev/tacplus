// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/publicsuffix"
)

// tsTypeStrip strips the TypeScript-only syntax of the generated
// utils/publicsuffix.ts so the real node runs it as plain JavaScript.
var tsTypeStrip = strings.NewReplacer(
	"(name: string): void", "(name)",
	"(name: string): boolean", "(name)",
	"(s: string): void", "(s)",
	"const PUBLIC_SUFFIX_RULES: ReadonlySet<string> = new Set<string>([", "const PUBLIC_SUFFIX_RULES = new Set([",
	"export function", "function",
)

// The generated utils/publicsuffix.ts, run with the real node over the shared
// vectors (tacplus#224): a public suffix and a malformed name are refused with
// the shared texts, a registrable name accepted; an address-shaped value
// refused.
func TestGeneratedTypeScriptRegistrableDomainAndNotAddress(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not installed; skipping the executable assert_registrable_domain check")
	}
	src := PublicSuffixTs()
	const errImport = "import { newErrMissingInvalidData } from '../types/sys/errors/errors.js';\n"
	if !strings.Contains(src, errImport) {
		t.Fatalf("generated publicsuffix.ts lacks %q", errImport)
	}
	src = strings.Replace(src, errImport, "function newErrMissingInvalidData(_cause, message) { return new Error(message); }\n", 1)
	src = tsTypeStrip.Replace(src)
	var in, addr []string
	for _, v := range publicsuffix.Vectors {
		in = append(in, strconv.Quote(v.In))
	}
	for _, v := range publicsuffix.AddressVectors {
		addr = append(addr, strconv.Quote(v.In))
	}
	src += `
const out = [];
for (const v of [` + strings.Join(in, ", ") + `]) {
	try {
		validateAssertRegistrableDomain(v);
		out.push("accept");
	} catch (e) {
		out.push(e.message);
	}
}
for (const v of [` + strings.Join(addr, ", ") + `]) {
	try {
		validateAssertNotAddress(v);
		out.push("false");
	} catch (e) {
		out.push(e.message === "address-shaped" ? "true" : e.message);
	}
}
process.stdout.write(out.join("\n"));
`
	path := filepath.Join(t.TempDir(), "publicsuffix.mjs")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("node", path).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	var want []string
	for _, v := range publicsuffix.Vectors {
		want = append(want, v.Outcome())
	}
	for _, v := range publicsuffix.AddressVectors {
		want = append(want, fmt.Sprint(v.Refused))
	}
	if got := strings.TrimSpace(string(out)); got != strings.Join(want, "\n") {
		t.Fatalf("outcomes differ\ngot:\n%s\nwant:\n%s", got, strings.Join(want, "\n"))
	}
}

// The field checks the TypeScript validation emitter writes for the three
// annotations, the helpers import of the validators, and the shapes refused
// at generation.
func TestTypeScriptValidationEmitsSignInAsserts(t *testing.T) {
	msg := model.Message{Name: "Conf", Fields: []model.Field{
		{Name: "domain", Type: "string", Annotations: map[string]model.AnnotationValue{
			"assert_registrable_domain": {Type: model.AnnotationTypeBool, BoolValue: true}}},
		{Name: "subjects", Type: "string", IsRepeated: true, Annotations: map[string]model.AnnotationValue{
			"assert_not_address": {Type: model.AnnotationTypeBool, BoolValue: true}}},
		{Name: "protocol", Type: "IdpProtocol", IsEnum: true, Annotations: map[string]model.AnnotationValue{
			"assert_enum_not_in": {Type: model.AnnotationTypeIntSlice, IntSliceValue: []int32{2, 3}}}},
		{Name: "protocols", Type: "IdpProtocol", IsEnum: true, IsRepeated: true, Annotations: map[string]model.AnnotationValue{
			"assert_enum_not_in": {Type: model.AnnotationTypeIntSlice, IntSliceValue: []int32{2}}}},
		{Name: "maybe", Type: "IdpProtocol", IsEnum: true, IsOptional: true, Annotations: map[string]model.AnnotationValue{
			"assert_enum_not_in": {Type: model.AnnotationTypeIntSlice, IntSliceValue: []int32{4}}}},
	}}
	var b strings.Builder
	if err := generateTypeScriptValidationFunction(&b, msg); err != nil {
		t.Fatal(err)
	}
	code := b.String()
	for _, want := range []string{
		"if (m.domain) {",
		"validateAssertRegistrableDomain(m.domain);",
		"throw newErrMissingInvalidData(e as Error, `field domain must be a registrable domain name`);",
		"for (let i = 0; i < m.subjects.length; i++) {",
		"validateAssertNotAddress(val);",
		"`field subjects[${i}] must not be address-shaped`",
		"if (Number(m.protocol) === 2) {",
		"if (Number(m.protocol) === 3) {",
		"`field protocol must not be 2`",
		"if (Number(m.protocols[i]) === 2) {",
		"`field protocols[${i}] must not be 2`",
		"if (m.maybe !== undefined && Number(m.maybe) === 4) {",
	} {
		if !strings.Contains(code, want) {
			t.Errorf("generated validation lacks %q\n%s", want, code)
		}
	}

	var imports strings.Builder
	pf := model.ProtoFile{RelPath: "acme/conf.proto", Messages: []model.Message{msg}}
	writeCombinedImports(&imports, pf, "conf", buildTsEnvVarContext(pf, map[string]bool{}), map[string]bool{})
	if !strings.Contains(imports.String(), "import { validateAssertNotAddress, validateAssertRegistrableDomain } from '../../../utils/publicsuffix.js';") {
		t.Errorf("helpers imports lack the publicsuffix validators\n%s", imports.String())
	}
	plain := model.ProtoFile{RelPath: "acme/plain.proto", Messages: []model.Message{{Name: "P"}}}
	imports.Reset()
	writeCombinedImports(&imports, plain, "plain", buildTsEnvVarContext(plain, map[string]bool{}), map[string]bool{})
	if strings.Contains(imports.String(), "publicsuffix") {
		t.Errorf("a file without the annotations imports publicsuffix\n%s", imports.String())
	}

	for _, bad := range []model.Field{
		{Name: "n", Type: "int64", Annotations: map[string]model.AnnotationValue{"assert_registrable_domain": {Type: model.AnnotationTypeBool, BoolValue: true}}},
		{Name: "n", Type: "bytes", IsRepeated: true, Annotations: map[string]model.AnnotationValue{"assert_not_address": {Type: model.AnnotationTypeBool, BoolValue: true}}},
		{Name: "n", Type: "string", IsMap: true, Annotations: map[string]model.AnnotationValue{"assert_not_address": {Type: model.AnnotationTypeBool, BoolValue: true}}},
		{Name: "n", Type: "string", Annotations: map[string]model.AnnotationValue{"assert_enum_not_in": {Type: model.AnnotationTypeIntSlice, IntSliceValue: []int32{1}}}},
		{Name: "e", Type: "E", IsEnum: true, Annotations: map[string]model.AnnotationValue{"assert_enum_not_in": {Type: model.AnnotationTypeIntSlice, IntSliceValue: []int32{0}}}},
	} {
		var b strings.Builder
		if err := generateTypeScriptValidationFunction(&b, model.Message{Name: "Bad", Fields: []model.Field{bad}}); err == nil {
			t.Errorf("field %+v: no generation error", bad)
		}
	}
}
