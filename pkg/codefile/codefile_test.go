// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package codefile

import (
	"strings"
	"testing"
)

func TestNewFile(t *testing.T) {
	for _, lang := range []Language{Go, Rust, Java, TypeScript} {
		f := New(lang)
		if f == nil {
			t.Fatalf("New(%d) returned nil", lang)
		}
		if f.Depth() != 0 {
			t.Errorf("initial depth should be 0, got %d", f.Depth())
		}
	}
}

func TestP_SimpleLines(t *testing.T) {
	f := New(Go)
	f.P("package main")
	f.Blank()
	f.P("func main() {")
	f.P("fmt.Println(\"hello\")")
	f.P("}")

	want := "package main\n\nfunc main() {\n\tfmt.Println(\"hello\")\n}\n"
	got := f.String()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestP_AutoIndentBraces(t *testing.T) {
	f := New(Go)
	f.P("if x > 0 {")
	f.P("return x")
	f.P("}")

	want := "if x > 0 {\n\treturn x\n}\n"
	got := f.String()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestP_NestedBraces(t *testing.T) {
	f := New(Go)
	f.P("func foo() {")
	f.P("if true {")
	f.P("bar()")
	f.P("}")
	f.P("}")

	want := "func foo() {\n\tif true {\n\t\tbar()\n\t}\n}\n"
	got := f.String()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestOpenClose(t *testing.T) {
	f := New(Go)
	f.Open("func foo()")
	f.P("bar()")
	f.Close()

	want := "func foo() {\n\tbar()\n}\n"
	got := f.String()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestOpenClose_WithSuffix(t *testing.T) {
	f := New(TypeScript)
	f.Open("const obj =")
	f.P("name: \"test\",")
	f.Close(";")

	want := "const obj = {\n  name: \"test\",\n};\n"
	got := f.String()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestCloseOpen(t *testing.T) {
	f := New(Java)
	f.Open("if (x > 0)")
	f.P("return x;")
	f.CloseOpen("else")
	f.P("return -x;")
	f.Close()

	want := "if (x > 0) {\n    return x;\n} else {\n    return -x;\n}\n"
	got := f.String()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestComment(t *testing.T) {
	f := New(Rust)
	f.Comment("This is a comment")
	f.Open("fn main()")
	f.Comment("inner comment")
	f.Close()

	want := "// This is a comment\nfn main() {\n    // inner comment\n}\n"
	got := f.String()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestBlockComment(t *testing.T) {
	f := New(Go)
	f.BlockComment("Line one", "Line two")

	want := "/*\n * Line one\n * Line two\n */\n"
	got := f.String()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestSection(t *testing.T) {
	f := New(Go)
	f.Section("Constants")

	got := f.String()
	if got == "" {
		t.Error("Section produced no output")
	}
	if !contains(got, "Constants") {
		t.Error("Section output does not contain title")
	}
	if !contains(got, "====") {
		t.Error("Section output does not contain separator")
	}
}

func TestRaw(t *testing.T) {
	f := New(Go)
	f.Open("func foo()")
	f.Raw("// raw line\n")
	f.Close()

	want := "func foo() {\n// raw line\n}\n"
	got := f.String()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestIndentDedent(t *testing.T) {
	// Manual Indent/Dedent for non-brace contexts (e.g. continuation lines)
	f := New(Rust)
	f.P("let x =")
	f.Indent()
	f.P("some_long_value")
	f.P("+ another_value;")
	f.Dedent()
	f.P("let y = 0;")

	want := "let x =\n    some_long_value\n    + another_value;\nlet y = 0;\n"
	got := f.String()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestDedentFloor(t *testing.T) {
	f := New(Go)
	f.Dedent() // should not panic or go negative
	if f.Depth() != 0 {
		t.Errorf("depth after extra Dedent: %d, want 0", f.Depth())
	}
}

func TestRustIndent(t *testing.T) {
	f := New(Rust)
	f.Open("pub struct Foo")
	f.P("bar: i32,")
	f.Close()

	want := "pub struct Foo {\n    bar: i32,\n}\n"
	got := f.String()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestTypeScriptIndent(t *testing.T) {
	f := New(TypeScript)
	f.Open("export interface User")
	f.P("name: string;")
	f.P("age: number;")
	f.Close()

	want := "export interface User {\n  name: string;\n  age: number;\n}\n"
	got := f.String()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestJavaClass(t *testing.T) {
	f := New(Java)
	f.P("package com.example;")
	f.Blank()
	f.Open("public class Helpers")
	f.Open("public static int add(int a, int b)")
	f.P("return a + b;")
	f.Close()
	f.Close()

	want := "package com.example;\n\npublic class Helpers {\n    public static int add(int a, int b) {\n        return a + b;\n    }\n}\n"
	got := f.String()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestPn_PartialLine(t *testing.T) {
	f := New(Go)
	f.Open("func foo()")
	f.Pn("x := ")
	f.Raw("1 + 2\n")
	f.Close()

	want := "func foo() {\n\tx := 1 + 2\n}\n"
	got := f.String()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestBuild(t *testing.T) {
	f := New(Go)
	f.P("package main")
	b := f.Build()
	if string(b) != "package main\n" {
		t.Errorf("Build() returned %q", string(b))
	}
}

func TestCloseComma(t *testing.T) {
	// P() auto-indents after trailing {, no manual Indent needed
	f := New(Go)
	f.P("items := []string{")
	f.P("\"a\",")
	f.P("\"b\",")
	f.P("}")

	want := "items := []string{\n\t\"a\",\n\t\"b\",\n}\n"
	got := f.String()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestP_CloseBraceWithSuffix(t *testing.T) {
	// When using P() with "})", the auto-dedent should trigger on the leading }
	f := New(Go)
	f.P("foo(func() {")
	f.P("bar()")
	f.P("})")

	want := "foo(func() {\n\tbar()\n})\n"
	got := f.String()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestCommentFormatArgs(t *testing.T) {
	f := New(Go)
	f.Comment("Value: %d, Name: %s", 42, "test")

	want := "// Value: 42, Name: test\n"
	got := f.String()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestOpenAlreadyHasBrace(t *testing.T) {
	f := New(Go)
	f.Open("func foo() {")
	f.P("bar()")
	f.Close()

	want := "func foo() {\n\tbar()\n}\n"
	got := f.String()
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestIndentCodeRust(t *testing.T) {
	input := `impl Config {
pub fn validate(&self) -> Result<(), String> {
if self.host.is_empty() {
return Err("host is required".to_string());
}
if self.port < 1 || self.port > 65535 {
return Err(format!("port must be between 1 and 65535, got {}", self.port));
}
Ok(())
}
}
`
	want := `impl Config {
    pub fn validate(&self) -> Result<(), String> {
        if self.host.is_empty() {
            return Err("host is required".to_string());
        }
        if self.port < 1 || self.port > 65535 {
            return Err(format!("port must be between 1 and 65535, got {}", self.port));
        }
        Ok(())
    }
}
`
	got := IndentCode(Rust, input)
	if got != want {
		t.Errorf("IndentCode(Rust) mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestIndentCodeElse(t *testing.T) {
	input := `if cond {
x = 1;
} else {
x = 2;
}
`
	want := `if cond {
    x = 1;
} else {
    x = 2;
}
`
	got := IndentCode(Rust, input)
	if got != want {
		t.Errorf("IndentCode else mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestIndentCodePreservesBlankLines(t *testing.T) {
	input := `fn foo() {
let x = 1;

let y = 2;
}
`
	want := `fn foo() {
    let x = 1;

    let y = 2;
}
`
	got := IndentCode(Rust, input)
	if got != want {
		t.Errorf("IndentCode blank lines mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestIndentCodeTypeScript(t *testing.T) {
	input := `export function validate(msg: Config): string[] {
const errors: string[] = [];
if (!msg.host) {
errors.push("host required");
}
return errors;
}
`
	want := `export function validate(msg: Config): string[] {
  const errors: string[] = [];
  if (!msg.host) {
    errors.push("host required");
  }
  return errors;
}
`
	got := IndentCode(TypeScript, input)
	if got != want {
		t.Errorf("IndentCode(TypeScript) mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestIndentCodeMultiBraceClose(t *testing.T) {
	// Pattern like ];}  — net -1 brace, but } is not the first char
	input := `switch (v) {
case Foo.BAR:
return "bar";
default:
return "";
}
}
`
	want := `switch (v) {
  case Foo.BAR:
  return "bar";
  default:
  return "";
}
}
`
	// Note: switch/case indentation isn't perfect (case not indented relative
	// to switch body) but this tests that }} closes two levels correctly.
	// In practice, the generators emit each } on its own line.
	got := IndentCode(TypeScript, input)
	if got != want {
		t.Errorf("IndentCode multi-brace close mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestIndentCodeCloseBraceSemicolon(t *testing.T) {
	// Pattern: ];}  — net -1 brace, dedents before writing.
	// The } closes the function body, so ];}  lands at depth 0.
	input := `export function values(): Foo[] {
return [
Foo.A,
Foo.B,
];}
`
	want := `export function values(): Foo[] {
  return [
    Foo.A,
    Foo.B,
];}
`
	got := IndentCode(TypeScript, input)
	if got != want {
		t.Errorf("IndentCode close-brace-semicolon mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestIndentCodeDoubleBraceClose(t *testing.T) {
	// Pattern: }}  — two closing braces on one line, should dedent by 2
	input := `class Foo {
constructor() {
this.x = 1;
}}
`
	want := `class Foo {
  constructor() {
    this.x = 1;
}}
`
	got := IndentCode(TypeScript, input)
	if got != want {
		t.Errorf("IndentCode double-brace close mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func TestIndentCodeNetBraceBalance(t *testing.T) {
	// Full realistic TypeScript pattern from tacplus output.
	// ];}  has net -1 (closes function body → depth 0).
	// }}   has net -2 (closes switch + function → depth 0).
	// Key: subsequent functions start at depth 0, no cascading.
	input := `export function fooValues(): Foo[] {
return [
Foo.A,
Foo.B,
];}
export function parseFoo(s: string): Foo {
if (!s) {
return Foo.UNSPECIFIED;
}
switch (s) {
case 'a':
return Foo.A;
default:
throw new Error('invalid');
}}
`
	want := `export function fooValues(): Foo[] {
  return [
    Foo.A,
    Foo.B,
];}
export function parseFoo(s: string): Foo {
  if (!s) {
    return Foo.UNSPECIFIED;
  }
  switch (s) {
    case 'a':
    return Foo.A;
    default:
    throw new Error('invalid');
}}
`
	got := IndentCode(TypeScript, input)
	if got != want {
		t.Errorf("IndentCode net brace balance mismatch\ngot:\n%s\nwant:\n%s", got, want)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsAt(s, sub))
}

func containsAt(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// A multi-line argument list or array literal is a continuation of its
// statement: the lines inside the open parenthesis keep one extra level, and
// the line that closes it returns to the statement's indent. Before this, the
// brace-only pass flattened every continuation line to the block's indent.
func TestIndentCodeKeepsContinuationLinesInsideParentheses(t *testing.T) {
	raw := strings.Join([]string{
		"class A {",
		"public static final List<R> ROUTES = List.of(",
		"new R(",
		"\"a\",",
		"\"b\")",
		");",
		"void m() {",
		"call(",
		"x,",
		"y);",
		"}",
		"}",
	}, "\n")
	want := strings.Join([]string{
		"class A {",
		"    public static final List<R> ROUTES = List.of(",
		"        new R(",
		"            \"a\",",
		"            \"b\")",
		"    );",
		"    void m() {",
		"        call(",
		"            x,",
		"            y);",
		"    }",
		"}",
		"",
	}, "\n")
	if got := IndentCode(Java, raw); got != want {
		t.Fatalf("IndentCode continuation indent wrong:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestIndentCodeRustCharAndStringLiterals(t *testing.T) {
	// Braces, parens and brackets inside char or string literals and line
	// comments are not code. Counting them left the depth one level deeper
	// after every generated from_bytes helper (b'{' on an if line), so the
	// indent grew with each helper until helpers.rs held millions of spaces.
	input := `pub fn from_bytes(data: &[u8]) -> Result<Msg, String> {
let trimmed = data.iter().copied().collect::<Vec<_>>();
if trimmed[0] == b'{' || trimmed[0] == b'[' {
if let Ok(msg) = from_json(data) {
return Ok(msg);
}
}
let s = "open { and ( in a string";
let c = '}';
// a comment with { and (
Ok(Msg::default())
}
pub fn next(data: &[u8]) -> Result<Msg, String> {
Ok(Msg::default())
}
`
	want := `pub fn from_bytes(data: &[u8]) -> Result<Msg, String> {
    let trimmed = data.iter().copied().collect::<Vec<_>>();
    if trimmed[0] == b'{' || trimmed[0] == b'[' {
        if let Ok(msg) = from_json(data) {
            return Ok(msg);
        }
    }
    let s = "open { and ( in a string";
    let c = '}';
    // a comment with { and (
    Ok(Msg::default())
}
pub fn next(data: &[u8]) -> Result<Msg, String> {
    Ok(Msg::default())
}
`
	if got := IndentCode(Rust, input); got != want {
		t.Fatalf("IndentCode(Rust) literal handling wrong:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestIndentCodeRustLifetimeIsNotCharLiteral(t *testing.T) {
	// The quote in 'a is a lifetime, not the start of a char literal, so the
	// braces after it still count.
	input := `fn first<'a>(xs: &'a [u8]) -> Option<&'a u8> {
if let Some(x) = xs.first() {
return Some(x);
}
None
}
`
	want := `fn first<'a>(xs: &'a [u8]) -> Option<&'a u8> {
    if let Some(x) = xs.first() {
        return Some(x);
    }
    None
}
`
	if got := IndentCode(Rust, input); got != want {
		t.Fatalf("IndentCode(Rust) lifetime handling wrong:\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestIndentCodeBracesInLiteralsJavaAndTypeScript(t *testing.T) {
	javaInput := `class A {
String s = "}";
char c = '{';
void m() {
call("(");
}
}
`
	javaWant := `class A {
    String s = "}";
    char c = '{';
    void m() {
        call("(");
    }
}
`
	if got := IndentCode(Java, javaInput); got != javaWant {
		t.Fatalf("IndentCode(Java) literal handling wrong:\n got:\n%s\nwant:\n%s", got, javaWant)
	}
	tsInput := "export function f(): string {\nconst a = '{';\nconst b = `}`;\nreturn a + b;\n}\n"
	tsWant := "export function f(): string {\n  const a = '{';\n  const b = `}`;\n  return a + b;\n}\n"
	if got := IndentCode(TypeScript, tsInput); got != tsWant {
		t.Fatalf("IndentCode(TypeScript) literal handling wrong:\n got:\n%s\nwant:\n%s", got, tsWant)
	}
}

func TestIndentCodeJavadocIsNotCode(t *testing.T) {
	// Javadoc prose is not code: an unclosed [ or ( in a comment must not leave
	// the continuation depth raised for the rest of the file.
	input := `class A {
/**
* <code>int32 n = 2 [(.spec.assert_numeric) = { ... }</code>
*/
int getN();
/* open ( comment
still a comment } */
void m() {
call();
}
}
`
	want := `class A {
    /**
    * <code>int32 n = 2 [(.spec.assert_numeric) = { ... }</code>
    */
    int getN();
    /* open ( comment
    still a comment } */
    void m() {
        call();
    }
}
`
	if got := IndentCode(Java, input); got != want {
		t.Fatalf("IndentCode(Java) comment handling wrong:\n got:\n%s\nwant:\n%s", got, want)
	}
}
