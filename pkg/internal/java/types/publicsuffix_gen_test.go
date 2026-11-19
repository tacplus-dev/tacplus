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
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// The generated Validation.java and PublicSuffixList.java, compiled and run
// with the real JDK over the shared vectors (tacplus#224): a public suffix
// and a malformed name are refused with the shared texts, a registrable name
// accepted; an address-shaped value refused. Refusals are ProtoException.
func TestGeneratedJavaRegistrableDomainAndNotAddress(t *testing.T) {
	if _, err := exec.LookPath("javac"); err != nil {
		t.Skip("javac not installed; skipping the executable assert_registrable_domain check")
	}
	if _, err := exec.LookPath("java"); err != nil {
		t.Skip("java not installed; skipping the executable assert_registrable_domain check")
	}
	dir := t.TempDir()
	if err := generateValidators(dir, &tactype.Config{JavaPackageBase: "com.example"}); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(dir, "src", "main", "java")
	errDir := filepath.Join(src, "com", "example", "types", "sys", "errors")
	if err := os.MkdirAll(errDir, 0o755); err != nil {
		t.Fatal(err)
	}
	stub := `package com.example.types.sys.errors;
public class ProtoException extends RuntimeException {
    public static final int INVALID_ARGUMENT = 3;
    public ProtoException(int code, String message) { super(message); }
    public ProtoException(int code, String message, Throwable cause) { super(message, cause); }
}
`
	if err := os.WriteFile(filepath.Join(errDir, "ProtoException.java"), []byte(stub), 0o644); err != nil {
		t.Fatal(err)
	}
	var in, addr strings.Builder
	for _, v := range publicsuffix.Vectors {
		fmt.Fprintf(&in, "            %s,\n", javaStringLiteral(v.In))
	}
	for _, v := range publicsuffix.AddressVectors {
		fmt.Fprintf(&addr, "            %s,\n", javaStringLiteral(v.In))
	}
	main := `package com.example.utils;
import com.example.types.sys.errors.ProtoException;
public class Main {
    public static void main(String[] args) throws Exception {
        java.io.PrintStream out = new java.io.PrintStream(System.out, true, "UTF-8");
        for (String v : new String[] {
` + in.String() + `        }) {
            String verdict = "accept";
            try {
                Validation.validateRegistrableDomain(v, "domain");
            } catch (ProtoException e) {
                String m = e.getMessage();
                String prefix = "field domain must be a registrable domain name: ";
                verdict = m.startsWith(prefix) ? m.substring(prefix.length()) : "bad message: " + m;
            }
            out.println(verdict);
        }
        for (String v : new String[] {
` + addr.String() + `        }) {
            boolean refused = false;
            try {
                Validation.validateNotAddress(v, "subject");
            } catch (ProtoException e) {
                refused = e.getMessage().contains("address-shaped");
            }
            out.println(refused);
        }
    }
}
`
	utilsDir := filepath.Join(src, "com", "example", "utils")
	if err := os.WriteFile(filepath.Join(utilsDir, "Main.java"), []byte(main), 0o644); err != nil {
		t.Fatal(err)
	}
	classes := filepath.Join(dir, "classes")
	files := []string{
		filepath.Join(errDir, "ProtoException.java"),
		filepath.Join(utilsDir, "Validation.java"),
		filepath.Join(utilsDir, "PublicSuffixList.java"),
		filepath.Join(utilsDir, "Main.java"),
	}
	if out, err := exec.Command("javac", append([]string{"-encoding", "UTF-8", "-d", classes}, files...)...).CombinedOutput(); err != nil {
		t.Fatalf("javac: %v\n%s", err, out)
	}
	out, err := exec.Command("java", "-cp", classes, "com.example.utils.Main").CombinedOutput()
	if err != nil {
		t.Fatalf("java: %v\n%s", err, out)
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

// Every string constant of PublicSuffixList.java fits a class-file constant,
// and together they carry every rule.
func TestPublicSuffixListJavaChunks(t *testing.T) {
	src := PublicSuffixListJava("com.example.utils")
	total := 0
	for _, line := range strings.Split(src, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, `"`) || !strings.HasSuffix(line, `",`) {
			continue
		}
		body := strings.TrimSuffix(strings.TrimPrefix(line, `"`), `",`)
		if n := len(strings.ReplaceAll(body, `\n`, "\n")); n > 65535 {
			t.Errorf("a constant of %d bytes", n)
		}
		total += strings.Count(body, `\n`)
	}
	if want := len(publicsuffix.Rules()); total != want {
		t.Errorf("table carries %d rules, want %d", total, want)
	}
}

// The field checks the Java validation emitter writes for the three
// annotations, and the shapes it refuses at generation.
func TestJavaValidationEmitsSignInAsserts(t *testing.T) {
	boolAnn := model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
	msg := model.Message{Name: "Conf", Fields: []model.Field{
		{Name: "domain", Type: "string", Annotations: map[string]model.AnnotationValue{"assert_registrable_domain": boolAnn}},
		{Name: "domains", Type: "string", IsRepeated: true, Annotations: map[string]model.AnnotationValue{"assert_registrable_domain": boolAnn}},
		{Name: "subject", Type: "string", Annotations: map[string]model.AnnotationValue{"assert_not_address": boolAnn}},
		{Name: "subjects", Type: "string", IsRepeated: true, Annotations: map[string]model.AnnotationValue{"assert_not_address": boolAnn}},
		{Name: "protocol", Type: "IdpProtocol", IsEnum: true, Annotations: map[string]model.AnnotationValue{
			"assert_enum_not_in": {Type: model.AnnotationTypeIntSlice, IntSliceValue: []int32{2, 3}}}},
		{Name: "maybe_protocol", Type: "IdpProtocol", IsEnum: true, IsOptional: true, Annotations: map[string]model.AnnotationValue{
			"assert_enum_not_in": {Type: model.AnnotationTypeIntSlice, IntSliceValue: []int32{2}}}},
		{Name: "protocols", Type: "IdpProtocol", IsEnum: true, IsRepeated: true, Annotations: map[string]model.AnnotationValue{
			"assert_enum_not_in": {Type: model.AnnotationTypeIntSlice, IntSliceValue: []int32{2}}}},
	}}
	var b strings.Builder
	if err := writeMessageValidation(&b, msg, "Outer", nil); err != nil {
		t.Fatal(err)
	}
	code := b.String()
	for _, want := range []string{
		"if (!msg.getDomain().isEmpty()) {\nValidation.validateRegistrableDomain(msg.getDomain(), \"domain\");",
		"String val = msg.getDomainsList().get(i);\nif (!val.isEmpty()) {\nValidation.validateRegistrableDomain(val, \"domains[\" + i + \"]\");",
		"Validation.validateNotAddress(msg.getSubject(), \"subject\");",
		"Validation.validateNotAddress(val, \"subjects[\" + i + \"]\");",
		"if (msg.getProtocolValue() == 2) {",
		"if (msg.getProtocolValue() == 3) {",
		`"field protocol must not be %s"`,
		"if (msg.hasMaybeProtocol() && msg.getMaybeProtocolValue() == 2) {",
		"if (msg.getProtocolsValueList().get(i) == 2) {",
		"String name = msg.getProtocolsList().get(i).name();",
		`"field protocols[%d] must not be %s"`,
	} {
		if !strings.Contains(code, want) {
			t.Errorf("generated validation lacks %q\n%s", want, code)
		}
	}
	for _, bad := range []model.Field{
		{Name: "n", Type: "int64", Annotations: map[string]model.AnnotationValue{"assert_registrable_domain": boolAnn}},
		{Name: "n", Type: "bytes", Annotations: map[string]model.AnnotationValue{"assert_not_address": boolAnn}},
		{Name: "m", Type: "string", IsMap: true, Annotations: map[string]model.AnnotationValue{"assert_not_address": boolAnn}},
		{Name: "n", Type: "string", Annotations: map[string]model.AnnotationValue{"assert_enum_not_in": {Type: model.AnnotationTypeIntSlice, IntSliceValue: []int32{1}}}},
		{Name: "e", Type: "E", IsEnum: true, Annotations: map[string]model.AnnotationValue{"assert_enum_not_in": {Type: model.AnnotationTypeIntSlice, IntSliceValue: []int32{0}}}},
	} {
		var bb strings.Builder
		if err := writeMessageValidation(&bb, model.Message{Name: "Bad", Fields: []model.Field{bad}}, "Outer", nil); err == nil {
			t.Errorf("field %+v: no generation error", bad)
		}
	}
}
