// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package verification

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const helpersSource = `package com.example.ks.model.types.services.kitchensink;
import java.util.*;
import com.example.ks.model.types.sys.errors.ProtoException;

public class Helpers {
    //@ requires msg != null;
    //@ ensures msg.getPort() >= 0 && msg.getPort() <= 65535;
    //@ ensures (msg.getLeThousand() >= 0 && msg.getLeThousand() <= 1000L);
    public static void validateBounds(Kitchensink.Bounds msg) throws ProtoException {
        validateBoundsDepth(msg, 0);
    }
    //@ requires msg != null;
    //@ ensures msg.getPort() >= 0 && msg.getPort() <= 65535;
    //@ ensures (msg.getLeThousand() >= 0 && msg.getLeThousand() <= 1000L);
    public static void validateBoundsDepth(Kitchensink.Bounds msg, int depth) throws ProtoException {
    }
    public static void validateBoundsAtRest(Kitchensink.Bounds msg) throws ProtoException {
    }
}
`

func TestParseJmlSourceReadsTheSpecifiedValidators(t *testing.T) {
	src, ok := parseJmlSource("Helpers.java", helpersSource)
	if !ok {
		t.Fatal("no validator with JML specs found")
	}
	if src.Package != "com.example.ks.model.types.services.kitchensink" {
		t.Errorf("package = %q", src.Package)
	}
	if src.ProtoException != "com.example.ks.model.types.sys.errors.ProtoException" {
		t.Errorf("ProtoException = %q", src.ProtoException)
	}
	var names []string
	for _, m := range src.Methods {
		names = append(names, m.Name+"("+m.Param+")")
	}
	if got, want := strings.Join(names, " "), "validateBounds(Kitchensink.Bounds) validateBoundsDepth(Kitchensink.Bounds)"; got != want {
		t.Errorf("methods = %q, want %q (the at-rest validator has no specs)", got, want)
	}
	if got := src.Methods[1].Clauses; len(got) != 3 || got[0] != "requires msg != null" || got[2] != "ensures (msg.getLeThousand() >= 0 && msg.getLeThousand() <= 1000L)" {
		t.Errorf("clauses = %q", got)
	}
}

func TestParseJavapReadsDeclarationAndMembers(t *testing.T) {
	out := `Compiled from "Kitchensink.java"
public final class com.example.Kitchensink$Sizes extends com.google.protobuf.GeneratedMessage implements com.example.Kitchensink$SizesOrBuilder {
  public static final int NAME_FIELD_NUMBER;
  public java.lang.String getName();
  public int getTagsCount();
  public java.lang.String getTags(int);
  public java.util.Map<java.lang.String, java.lang.String> getLabelsMap();
  public static com.example.Kitchensink$Sizes getDefaultInstance();
}
`
	header, members, err := parseJavap(out, "com.example.Kitchensink$Sizes")
	if err != nil {
		t.Fatal(err)
	}
	if want := "public final class Sizes extends com.google.protobuf.GeneratedMessage implements com.example.Kitchensink.SizesOrBuilder"; header != want {
		t.Errorf("header = %q, want %q", header, want)
	}
	got := map[string]javapMember{}
	for _, m := range members {
		got[m.Name] = m
	}
	if m := got["getLabelsMap"]; m.Return != "java.util.Map<java.lang.String, java.lang.String>" || m.Params != "" {
		t.Errorf("getLabelsMap = %+v", m)
	}
	if m := got["getTags"]; m.Params != "int" {
		t.Errorf("getTags = %+v", m)
	}
	if m := got["getDefaultInstance"]; !m.Static || m.Return != "com.example.Kitchensink.Sizes" {
		t.Errorf("getDefaultInstance = %+v", m)
	}

	_, ctors, err := parseJavap(`public class com.example.errors.ProtoException extends java.lang.RuntimeException {
  public static final int INVALID_ARGUMENT;
  public com.example.errors.ProtoException(java.lang.String);
  public com.example.errors.ProtoException(int, java.lang.String);
}
`, "com.example.errors.ProtoException")
	if err != nil {
		t.Fatal(err)
	}
	var decls []string
	for _, m := range ctors {
		if m.Constructor {
			decls = append(decls, m.declare("/*@ pure @*/", m.Params))
		}
	}
	if want := "public /*@ pure @*/ ProtoException(int, java.lang.String);\n"; len(decls) != 2 || decls[1] != want {
		t.Errorf("constructors = %q, want the second %q", decls, want)
	}
}

func TestParseOpenJmlOutputGivesEachValidatorItsVerdict(t *testing.T) {
	src, _ := parseJmlSource("/m/Helpers.java", helpersSource)
	output := `Proving methods in com.example.ks.model.types.services.kitchensink.Helpers
Starting proof of com.example.ks.model.types.services.kitchensink.Helpers.validateBounds(com.example.ks.model.types.services.kitchensink.Kitchensink.Bounds) with prover z3_4_7
Completed proof of com.example.ks.model.types.services.kitchensink.Helpers.validateBounds(com.example.ks.model.types.services.kitchensink.Kitchensink.Bounds) with prover z3_4_7 - no warnings [3.10 secs]
Starting proof of com.example.ks.model.types.services.kitchensink.Helpers.validateBoundsDepth(com.example.ks.model.types.services.kitchensink.Kitchensink.Bounds,int) with prover z3_4_7
/m/Helpers.java:15: verify: The prover cannot establish an assertion (Postcondition: /m/Helpers.java:14:) in method validateBoundsDepth
/m/Helpers.java:14: verify: Associated declaration: /m/Helpers.java:15:
Completed proof of com.example.ks.model.types.services.kitchensink.Helpers.validateBoundsDepth(com.example.ks.model.types.services.kitchensink.Kitchensink.Bounds,int) with prover z3_4_7 - with warnings [9.80 secs]
`
	hs := parseOpenJmlOutput(output, src)
	if len(hs) != 2 {
		t.Fatalf("harnesses = %d, want 2", len(hs))
	}
	if hs[0].HarnessName != "com.example.ks.model.types.services.kitchensink.Helpers.validateBounds" || hs[0].Status != "verified" {
		t.Errorf("validateBounds = %s %s %q", hs[0].HarnessName, hs[0].Status, hs[0].Detail)
	}
	if hs[1].Status != "failed" || !strings.Contains(hs[1].Detail, "Postcondition") {
		t.Errorf("validateBoundsDepth = %s %q", hs[1].Status, hs[1].Detail)
	}

	// A source OpenJML could not typecheck proves nothing.
	hs = parseOpenJmlOutput("/m/Helpers.java:9: error: cannot find symbol\n1 error\n", src)
	for _, h := range hs {
		if h.Status != "failed" || !strings.Contains(h.Detail, "cannot find symbol") {
			t.Errorf("%s = %s %q, want failed on the type error", h.HarnessName, h.Status, h.Detail)
		}
	}
}

// The stubs are rendered from the compiled classes; they must model each
// message getter as a pure method returning a ghost field.
func TestWriteJmlStubsModelsGettersAsGhostFields(t *testing.T) {
	for _, tool := range []string{"javac", "javap"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed", tool)
		}
	}
	dir := t.TempDir()
	write := func(rel, text string) {
		path := filepath.Join(dir, "src", rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("p/Outer.java", `package p;
public final class Outer {
  public static final class Msg {
    public int getPort() { return 0; }
    public String getName() { return ""; }
    public String getTags(int i) { return ""; }
    public boolean hasName() { return false; }
    public static Msg getDefaultInstance() { return null; }
  }
}
`)
	write("p/errors/ProtoException.java", `package p.errors;
public class ProtoException extends RuntimeException {
  public ProtoException(int code, String message) { super(message); }
}
`)
	write("com/google/protobuf/ByteString.java", `package com.google.protobuf;
public abstract class ByteString implements java.io.Serializable {
  public abstract int size();
}
`)
	classes := filepath.Join(dir, "classes")
	args := []string{"-d", classes}
	for _, f := range []string{"p/Outer.java", "p/errors/ProtoException.java", "com/google/protobuf/ByteString.java"} {
		args = append(args, filepath.Join(dir, "src", f))
	}
	if out, err := exec.Command("javac", args...).CombinedOutput(); err != nil {
		t.Fatalf("javac: %v\n%s", err, out)
	}

	src := jmlSource{Package: "p", ProtoException: "p.errors.ProtoException", Methods: []jmlMethod{{Name: "validateMsg", Param: "Outer.Msg"}}}
	specs := filepath.Join(dir, "specs")
	if err := writeJmlStubs(specs, classes, src); err != nil {
		t.Fatal(err)
	}
	read := func(rel string) string {
		data, err := os.ReadFile(filepath.Join(specs, rel))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	outer := read("p/Outer.jml")
	for _, want := range []string{
		"package p;",
		"public final class Outer {",
		"public static final class Msg {",
		"//@ public final ghost int _getPort;\n//@ public normal_behavior\n//@   ensures \\result == _getPort;\npublic /*@ pure @*/ int getPort();",
		"public /*@ pure @*/ String getName();",
		"public /*@ pure @*/ boolean hasName();",
	} {
		if !strings.Contains(outer, want) {
			t.Errorf("Outer.jml lacks %q:\n%s", want, outer)
		}
	}
	for _, bad := range []string{"getTags", "getDefaultInstance"} {
		if strings.Contains(outer, bad) {
			t.Errorf("Outer.jml models %s, which takes an argument or is static:\n%s", bad, outer)
		}
	}
	if pe := read("p/errors/ProtoException.jml"); !strings.Contains(pe, "//@ public normal_behavior\n//@   ensures true;\npublic /*@ pure @*/ ProtoException(int code, java.lang.String message);") {
		t.Errorf("ProtoException.jml:\n%s", pe)
	}
	if bs := read("com/google/protobuf/ByteString.jml"); !strings.Contains(bs, "public abstract /*@ pure @*/ int size();") {
		t.Errorf("ByteString.jml:\n%s", bs)
	}
}

// `model` is a JML keyword, and tacplus's default output directory: a type
// qualified through a package named model cannot be written in a JML comment.
// The stubs import each type's top-level class and name it short.
func TestJmlTypeNamesAvoidQualifiedNames(t *testing.T) {
	n := newTypeNamer("com.example.ks.model.types.services.ks")
	for _, c := range []struct{ in, want string }{
		{"int", "int"},
		{"com.example.ks.model.types.services.ks.Kitchensink.Mode", "Kitchensink.Mode"},
		{"java.lang.String", "String"},
		{"java.util.Map<java.lang.String, com.example.ks.model.types.sys.trace.Trace.Span>", "Map<String, Trace.Span>"},
		{"com.google.protobuf.Parser<com.example.ks.model.types.services.ks.Kitchensink.Bounds>", "Parser<Kitchensink.Bounds>"},
		// A second Kitchensink: its simple name is taken, it stays qualified.
		{"com.example.ks.model.types.other.Kitchensink.Mode", "com.example.ks.model.types.other.Kitchensink.Mode"},
	} {
		if got := n.short(c.in); got != c.want {
			t.Errorf("short(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if got, want := n.importLines(), "import com.example.ks.model.types.sys.trace.Trace;\nimport com.google.protobuf.Parser;\nimport java.util.Map;\n\n"; got != want {
		t.Errorf("imports =\n%s\nwant\n%s", got, want)
	}
}
