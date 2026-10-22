// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package strutil

import "testing"

func TestToSnakeCase(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"FooBar", "foo_bar"},
		{"fooBar", "foo_bar"},
		{"Foo", "foo"},
		{"foo", "foo"},
		{"", ""},
		{"A", "a"},
		{"AB", "ab"},
		{"OpenAIService", "open_ai_service"},
		{"HTTPServer", "http_server"},
		{"getHTTPResponse", "get_http_response"},
	}
	for _, tt := range tests {
		got := ToSnakeCase(tt.in)
		if got != tt.want {
			t.Errorf("ToSnakeCase(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestToKebabCase(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"FooBar", "foo-bar"},
		{"fooBar", "foo-bar"},
		{"foo", "foo"},
	}
	for _, tt := range tests {
		got := ToKebabCase(tt.in)
		if got != tt.want {
			t.Errorf("ToKebabCase(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestToCamelCase(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"foo_bar", "FooBar"},
		{"foo_bar_baz", "FooBarBaz"},
		{"foo", "Foo"},
		{"", ""},
	}
	for _, tt := range tests {
		got := ToCamelCase(tt.in)
		if got != tt.want {
			t.Errorf("ToCamelCase(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestToLowerCamelCase(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"FooBar", "fooBar"},
		{"foo_bar", "fooBar"},
		{"Foo", "foo"},
		{"", ""},
	}
	for _, tt := range tests {
		got := ToLowerCamelCase(tt.in)
		if got != tt.want {
			t.Errorf("ToLowerCamelCase(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestUpperSnakeCaseToPascalCase(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"FOO_BAR", "FooBar"},
		{"HTTP_STATUS", "HttpStatus"},
		{"SINGLE", "Single"},
	}
	for _, tt := range tests {
		got := UpperSnakeCaseToPascalCase(tt.in)
		if got != tt.want {
			t.Errorf("UpperSnakeCaseToPascalCase(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestUpperSnakeCaseToCamelCase(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"FOO_BAR", "fooBar"},
		{"HTTP_STATUS", "httpStatus"},
	}
	for _, tt := range tests {
		got := UpperSnakeCaseToCamelCase(tt.in)
		if got != tt.want {
			t.Errorf("UpperSnakeCaseToCamelCase(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSnakeToCamelCase(t *testing.T) {
	got := SnakeToCamelCase("foo_bar")
	if got != "fooBar" {
		t.Errorf("SnakeToCamelCase(foo_bar) = %q, want fooBar", got)
	}
}

func TestCapitalize(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"foo", "Foo"},
		{"fooBar", "FooBar"},
		{"", ""},
		{"A", "A"},
	}
	for _, tt := range tests {
		got := Capitalize(tt.in)
		if got != tt.want {
			t.Errorf("Capitalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestDecapitalize(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"Foo", "foo"},
		{"FooBar", "fooBar"},
		{"", ""},
	}
	for _, tt := range tests {
		got := Decapitalize(tt.in)
		if got != tt.want {
			t.Errorf("Decapitalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestToPascalCase(t *testing.T) {
	got := ToPascalCase("foo_bar")
	if got != "FooBar" {
		t.Errorf("ToPascalCase(foo_bar) = %q, want FooBar", got)
	}
}

func TestLcFirst(t *testing.T) {
	got := LcFirst("FooBar")
	if got != "fooBar" {
		t.Errorf("LcFirst(FooBar) = %q, want fooBar", got)
	}
}

func TestToLowerFirst(t *testing.T) {
	got := ToLowerFirst("FooBar")
	if got != "fooBar" {
		t.Errorf("ToLowerFirst(FooBar) = %q, want fooBar", got)
	}
}

// --- Expanded edge case tests ---

func TestToSnakeCase_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		in, want string
	}{
		{"already_snake_case", "foo_bar", "foo_bar"},
		{"single_lowercase", "x", "x"},
		{"single_uppercase", "X", "x"},
		{"consecutive_uppercase", "HTTPServer", "http_server"},
		{"all_caps", "URL", "url"},
		{"camelCase_with_numbers", "Foo2Bar", "foo2_bar"},
		{"number_at_end", "Foo2", "foo2"},
		{"number_at_start", "2Foo", "2_foo"},
		{"leading_lowercase", "myHTTPClient", "my_http_client"},
		{"three_words", "FooBarBaz", "foo_bar_baz"},
		{"all_lowercase", "foobar", "foobar"},
		{"unicode_lower", "fooBar", "foo_bar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToSnakeCase(tt.in)
			if got != tt.want {
				t.Errorf("ToSnakeCase(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestToKebabCase_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		in, want string
	}{
		{"empty", "", ""},
		{"single_char", "A", "a"},
		{"single_lower", "a", "a"},
		{"already_lower", "foo", "foo"},
		{"PascalCase", "FooBarBaz", "foo-bar-baz"},
		{"consecutive_upper", "HTTPServer", "http-server"},
		{"with_numbers", "Foo2Bar", "foo2-bar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToKebabCase(tt.in)
			if got != tt.want {
				t.Errorf("ToKebabCase(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestToCamelCase_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		in, want string
	}{
		{"single_char", "a", "A"},
		{"single_upper", "A", "A"},
		{"already_capitalized", "Foo", "Foo"},
		{"three_parts", "a_b_c", "ABC"},
		{"with_numbers", "foo_2_bar", "Foo2Bar"},
		{"leading_underscore", "_foo", "Foo"},
		{"trailing_underscore", "foo_", "Foo"},
		{"double_underscore", "foo__bar", "FooBar"},
		{"all_single_chars", "a_b_c_d", "ABCD"},
		{"upper_input", "FOO_BAR", "FOOBAR"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToCamelCase(tt.in)
			if got != tt.want {
				t.Errorf("ToCamelCase(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestToLowerCamelCase_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		in, want string
	}{
		{"single_char_upper", "A", "a"},
		{"single_char_lower", "a", "a"},
		{"already_lower_camel", "fooBar", "fooBar"},
		{"snake_three_parts", "foo_bar_baz", "fooBarBaz"},
		{"snake_single_char_parts", "a_b_c", "aBC"},
		{"leading_underscore", "_foo", "foo"},
		{"trailing_underscore", "foo_", "foo"},
		{"double_underscore", "foo__bar", "fooBar"},
		{"PascalCase_no_underscore", "FooBarBaz", "fooBarBaz"},
		{"all_caps_no_underscore", "URL", "uRL"},
		{"snake_with_numbers", "foo_2_bar", "foo2Bar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToLowerCamelCase(tt.in)
			if got != tt.want {
				t.Errorf("ToLowerCamelCase(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestUpperSnakeCaseToPascalCase_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		in, want string
	}{
		{"empty", "", ""},
		{"single_word", "HELLO", "Hello"},
		{"three_parts", "FOO_BAR_BAZ", "FooBarBaz"},
		{"single_char_parts", "A_B_C", "ABC"},
		{"with_numbers", "HTTP_2_STATUS", "Http2Status"},
		{"already_mixed", "Foo_Bar", "FooBar"},
		{"trailing_underscore", "FOO_", "Foo"},
		{"leading_underscore", "_FOO", "Foo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UpperSnakeCaseToPascalCase(tt.in)
			if got != tt.want {
				t.Errorf("UpperSnakeCaseToPascalCase(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestUpperSnakeCaseToCamelCase_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		in, want string
	}{
		{"empty", "", ""},
		{"single_word", "HELLO", "hello"},
		{"three_parts", "FOO_BAR_BAZ", "fooBarBaz"},
		{"single_char_parts", "A_B_C", "aBC"},
		{"trailing_underscore", "FOO_", "foo"},
		{"leading_underscore", "_FOO", "Foo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := UpperSnakeCaseToCamelCase(tt.in)
			if got != tt.want {
				t.Errorf("UpperSnakeCaseToCamelCase(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSnakeToCamelCase_EdgeCases(t *testing.T) {
	// SnakeToCamelCase is an alias for ToLowerCamelCase, verify consistent behavior
	tests := []struct {
		name     string
		in, want string
	}{
		{"empty", "", ""},
		{"basic", "foo_bar", "fooBar"},
		{"three_parts", "foo_bar_baz", "fooBarBaz"},
		{"single_word", "foo", "foo"},
		{"PascalCase_input", "FooBar", "fooBar"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SnakeToCamelCase(tt.in)
			if got != tt.want {
				t.Errorf("SnakeToCamelCase(%q) = %q, want %q", tt.in, got, tt.want)
			}
			// Verify alias consistency
			alt := ToLowerCamelCase(tt.in)
			if got != alt {
				t.Errorf("SnakeToCamelCase(%q) = %q but ToLowerCamelCase(%q) = %q; alias mismatch", tt.in, got, tt.in, alt)
			}
		})
	}
}

func TestCapitalize_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		in, want string
	}{
		{"single_lower", "a", "A"},
		{"already_upper", "A", "A"},
		{"already_capitalized", "Foo", "Foo"},
		{"all_upper", "FOO", "FOO"},
		{"numeric_start", "1foo", "1foo"},
		{"with_spaces", "hello world", "Hello world"},
		{"lowercase_single", "z", "Z"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Capitalize(tt.in)
			if got != tt.want {
				t.Errorf("Capitalize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDecapitalize_EdgeCases(t *testing.T) {
	tests := []struct {
		name     string
		in, want string
	}{
		{"single_upper", "A", "a"},
		{"single_lower", "a", "a"},
		{"URL", "URL", "uRL"},
		{"Hello", "Hello", "hello"},
		{"already_lower", "foo", "foo"},
		{"with_spaces", "Hello World", "hello World"},
		{"numeric_start", "1Foo", "1Foo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Decapitalize(tt.in)
			if got != tt.want {
				t.Errorf("Decapitalize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestToPascalCase_EdgeCases(t *testing.T) {
	// ToPascalCase is an alias for ToCamelCase
	tests := []struct {
		name     string
		in, want string
	}{
		{"empty", "", ""},
		{"basic", "foo_bar", "FooBar"},
		{"single_char", "a", "A"},
		{"three_parts", "foo_bar_baz", "FooBarBaz"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToPascalCase(tt.in)
			if got != tt.want {
				t.Errorf("ToPascalCase(%q) = %q, want %q", tt.in, got, tt.want)
			}
			// Verify alias consistency
			alt := ToCamelCase(tt.in)
			if got != alt {
				t.Errorf("ToPascalCase(%q) = %q but ToCamelCase(%q) = %q; alias mismatch", tt.in, got, tt.in, alt)
			}
		})
	}
}

func TestLcFirst_EdgeCases(t *testing.T) {
	// LcFirst is an alias for Decapitalize
	tests := []struct {
		name     string
		in, want string
	}{
		{"empty", "", ""},
		{"single_upper", "A", "a"},
		{"pascal", "FooBar", "fooBar"},
		{"already_lower", "foo", "foo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := LcFirst(tt.in)
			if got != tt.want {
				t.Errorf("LcFirst(%q) = %q, want %q", tt.in, got, tt.want)
			}
			alt := Decapitalize(tt.in)
			if got != alt {
				t.Errorf("LcFirst(%q) = %q but Decapitalize(%q) = %q; alias mismatch", tt.in, got, tt.in, alt)
			}
		})
	}
}

func TestToLowerFirst_EdgeCases(t *testing.T) {
	// ToLowerFirst is an alias for Decapitalize
	tests := []struct {
		name     string
		in, want string
	}{
		{"empty", "", ""},
		{"single_upper", "A", "a"},
		{"pascal", "FooBar", "fooBar"},
		{"already_lower", "foo", "foo"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ToLowerFirst(tt.in)
			if got != tt.want {
				t.Errorf("ToLowerFirst(%q) = %q, want %q", tt.in, got, tt.want)
			}
			alt := Decapitalize(tt.in)
			if got != alt {
				t.Errorf("ToLowerFirst(%q) = %q but Decapitalize(%q) = %q; alias mismatch", tt.in, got, tt.in, alt)
			}
		})
	}
}

// TestAliasConsistency verifies that all alias pairs return the same results
// for the same inputs across a comprehensive input set.
func TestAliasConsistency(t *testing.T) {
	inputs := []string{
		"", "a", "A", "foo", "Foo", "FooBar", "fooBar",
		"foo_bar", "FOO_BAR", "HTTP", "url", "URL",
		"foo_bar_baz", "a_b_c",
	}

	t.Run("ToPascalCase_vs_ToCamelCase", func(t *testing.T) {
		for _, in := range inputs {
			a := ToPascalCase(in)
			b := ToCamelCase(in)
			if a != b {
				t.Errorf("ToPascalCase(%q)=%q != ToCamelCase(%q)=%q", in, a, in, b)
			}
		}
	})

	t.Run("LcFirst_vs_Decapitalize", func(t *testing.T) {
		for _, in := range inputs {
			a := LcFirst(in)
			b := Decapitalize(in)
			if a != b {
				t.Errorf("LcFirst(%q)=%q != Decapitalize(%q)=%q", in, a, in, b)
			}
		}
	})

	t.Run("ToLowerFirst_vs_Decapitalize", func(t *testing.T) {
		for _, in := range inputs {
			a := ToLowerFirst(in)
			b := Decapitalize(in)
			if a != b {
				t.Errorf("ToLowerFirst(%q)=%q != Decapitalize(%q)=%q", in, a, in, b)
			}
		}
	})

	t.Run("SnakeToCamelCase_vs_ToLowerCamelCase", func(t *testing.T) {
		for _, in := range inputs {
			a := SnakeToCamelCase(in)
			b := ToLowerCamelCase(in)
			if a != b {
				t.Errorf("SnakeToCamelCase(%q)=%q != ToLowerCamelCase(%q)=%q", in, a, in, b)
			}
		}
	})
}

// TestRoundTrip verifies that certain conversions are consistent when chained.
func TestRoundTrip(t *testing.T) {
	// snake_case -> PascalCase -> snake_case should return the original for simple inputs
	// Note: single-letter parts like "a_b_c" don't round-trip because
	// ToCamelCase("a_b_c") = "ABC" which ToSnakeCase treats as one acronym → "abc".
	simpleSnake := []string{"foo_bar", "hello_world"}
	for _, s := range simpleSnake {
		pascal := ToCamelCase(s)
		back := ToSnakeCase(pascal)
		if back != s {
			t.Errorf("round trip snake->pascal->snake: %q -> %q -> %q (want %q)", s, pascal, back, s)
		}
	}

	// Capitalize then Decapitalize should return the original for lowercase inputs
	lowerInputs := []string{"foo", "hello", "world", "a"}
	for _, s := range lowerInputs {
		cap := Capitalize(s)
		back := Decapitalize(cap)
		if back != s {
			t.Errorf("round trip capitalize->decapitalize: %q -> %q -> %q (want %q)", s, cap, back, s)
		}
	}
}

// ToUpperSnakeCase replaced three byte-identical private helpers that had lived
// in pkg/internal/proto under three different names (upperSnake, toUpperSnake,
// pascalToUpperSnake). They agreed at the time they were merged; this pins the
// behaviour so the single remaining implementation cannot drift, and covers the
// acronym cases that NEW-20 showed are the ones that actually move.
func TestToUpperSnakeCase(t *testing.T) {
	tests := []struct{ in, want string }{
		{"FooBar", "FOO_BAR"},
		{"fooBar", "FOO_BAR"},
		{"Foo", "FOO"},
		{"", ""},
		{"HTTPServer", "HTTP_SERVER"},
		{"OpenAIService", "OPEN_AI_SERVICE"},
		{"getHTTPResponse", "GET_HTTP_RESPONSE"},
	}
	for _, tt := range tests {
		if got := ToUpperSnakeCase(tt.in); got != tt.want {
			t.Errorf("ToUpperSnakeCase(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
