// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"strings"
	"testing"
	"text/template"
)

// rest_dispatch.rs.tmpl drops HttpPath, GrpcPath and ServiceID between the
// quotes of Rust string literals. All three are derived from proto annotations
// ((spec.rest), (spec.service)),
// so a proto author can put a double quote or a backslash in any of them. These
// pin that the generator escapes them instead of emitting broken Rust.

// escapedLiteralSites are the emitted Rust string literals fed from
// proto-annotation-derived strings. Indentation anchors the ROUTES-table
// entries so the prefixes do not match the RouteInfo struct declaration.
var escapedLiteralSites = []string{
	"\n        http_path: ",
	"\n        grpc_path: ",
	"\n        service_id: ",
	"dispatcher.channel(",
	"dispatcher.auth_header(",
}

// hostileRestDispatchData is one route and one unary arm whose every
// annotation-derived string carries a quote and a backslash.
func hostileRestDispatchData() (restDispatchData, string) {
	const hostile = `/v1/a"b\c`
	return restDispatchData{
		Header: "// generated",
		Routes: []routeEntry{{
			HttpPath:           hostile,
			GrpcPath:           hostile,
			ServiceID:          hostile,
			EntitlementService: "None",
			HttpMethodsLiteral: `"POST"`,
			AuthPolicy:         "NoAuth",
			EntitlementScopes:  "&[]",
			CapabilityScope:    "None",
			TargetNamespace:    "None",
		}},
		UnaryArms: []dispatchArm{{
			GrpcPath:     hostile,
			ServiceID:    hostile,
			MethodSnake:  "get_thing",
			ClientPath:   "crate::c::Client",
			FromJsonPath: "crate::t::from_json",
			ToJsonPath:   "crate::t::to_json",
		}},
	}, hostile
}

// rustLiteralAfter finds prefix in src and decodes the Rust string literal that
// immediately follows it. ok is false when the literal is malformed — which is
// exactly what an unescaped quote produces.
func rustLiteralAfter(t *testing.T, src, prefix string) (value string, ok bool) {
	t.Helper()
	i := strings.Index(src, prefix)
	if i < 0 {
		t.Fatalf("prefix %q not found in generated source:\n%s", prefix, src)
	}
	rest := src[i+len(prefix):]
	if !strings.HasPrefix(rest, `"`) {
		t.Fatalf("no string literal after %q, got %.40q", prefix, rest)
	}
	var b strings.Builder
	for j := 1; j < len(rest); j++ {
		switch rest[j] {
		case '\\':
			j++
			if j >= len(rest) {
				return "", false
			}
			switch rest[j] {
			case '\\':
				b.WriteByte('\\')
			case '"':
				b.WriteByte('"')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case '0':
				b.WriteByte(0)
			default:
				return "", false // not a Rust escape we emit
			}
		case '"':
			return b.String(), true
		case '\n':
			return "", false // literal never closed on its own line
		default:
			b.WriteByte(rest[j])
		}
	}
	return "", false
}

// The escaped literals round-trip to the annotation values they came from.
func TestRestDispatchEscapesAnnotationStrings(t *testing.T) {
	data, hostile := hostileRestDispatchData()
	src := renderRustRestDispatchForTest(t, data)

	for _, prefix := range escapedLiteralSites {
		got, ok := rustLiteralAfter(t, src, prefix)
		if !ok {
			t.Errorf("literal after %q is not a well-formed Rust string literal", prefix)
			continue
		}
		if got != hostile {
			t.Errorf("literal after %q = %q, want %q", prefix, got, hostile)
		}
	}
}

// The control: the same template with escaping removed emits literals that a
// Rust lexer would reject, so the test above is not passing vacuously.
func TestRestDispatchUnescapedIsMalformed(t *testing.T) {
	tmplStr, err := loadTemplate("rest_dispatch.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	raw, err := template.New("rest_dispatch").
		Delims("<<", ">>").
		Funcs(template.FuncMap{"rustStr": func(s string) string { return s }}).
		Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	data, hostile := hostileRestDispatchData()
	var buf bytes.Buffer
	if err := raw.Execute(&buf, data); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	src := buf.String()

	for _, prefix := range escapedLiteralSites {
		// Without escaping the emitted literal either fails to lex at all or
		// terminates early at the author's quote and denotes a different
		// string, with the remainder left as stray Rust tokens.
		if got, ok := rustLiteralAfter(t, src, prefix); ok && got == hostile {
			t.Errorf("literal after %q round-tripped without escaping: %q", prefix, got)
		}
	}
}

// The escape helper itself, on the characters Rust string literals care about.
func TestRustStringEscape(t *testing.T) {
	cases := []struct{ in, want string }{
		{`plain/path`, `plain/path`},
		{`a"b`, `a\"b`},
		{`a\b`, `a\\b`},
		{"a\nb", `a\nb`},
		{"a\tb", `a\tb`},
		{"a\rb", `a\rb`},
		{"a\x00b", `a\0b`},
		{"a\x01b", `a\u{1}b`},
		{"a\x7fb", `a\u{7f}b`},
		{`héllo`, `héllo`},
	}
	for _, tc := range cases {
		if got := rustStringEscape(tc.in); got != tc.want {
			t.Errorf("rustStringEscape(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
