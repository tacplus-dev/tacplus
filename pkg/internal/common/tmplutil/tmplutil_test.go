// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package tmplutil

import (
	"strings"
	"testing"
	"testing/fstest"
	"text/template"
)

func TestExecuteTemplate(t *testing.T) {
	tests := []struct {
		name    string
		tmpl    string
		data    any
		funcMap template.FuncMap
		want    string
		wantErr bool
	}{
		{
			name: "simple substitution",
			tmpl: "Hello, {{.Name}}!",
			data: struct{ Name string }{"World"},
			want: "Hello, World!",
		},
		{
			name: "with func map",
			tmpl: "{{upper .Name}}",
			data: struct{ Name string }{"hello"},
			funcMap: template.FuncMap{
				"upper": strings.ToUpper,
			},
			want: "HELLO",
		},
		{
			name:    "invalid template syntax",
			tmpl:    "{{.Unclosed",
			data:    nil,
			wantErr: true,
		},
		{
			name: "empty template",
			tmpl: "",
			data: nil,
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := fstest.MapFS{
				"test.tmpl": &fstest.MapFile{Data: []byte(tt.tmpl)},
			}

			fm := template.FuncMap{}
			if tt.funcMap != nil {
				fm = tt.funcMap
			}

			got, err := ExecuteTemplate(fsys, "test.tmpl", fm, tt.data)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", string(got), tt.want)
			}
		})
	}
}

func TestExecuteTemplate_MissingFile(t *testing.T) {
	fsys := fstest.MapFS{}
	_, err := ExecuteTemplate(fsys, "nonexistent.tmpl", nil, nil)
	if err == nil {
		t.Error("expected error for missing file")
	}
}

func TestBaseFuncMap(t *testing.T) {
	fm := BaseFuncMap()

	// Verify key functions exist
	expectedFuncs := []string{
		"toSnakeCase", "toKebabCase", "toCamelCase", "toLowerCamelCase",
		"toPascalCase", "capitalize", "decapitalize", "lcFirst",
		"toLower", "toUpper", "trimPrefix", "trimSuffix",
		"contains", "hasPrefix", "hasSuffix", "replaceAll",
		"join", "split", "trimSpace", "repeat",
		"quote", "add", "sub", "last",
		"isEnumSentinel",
		"upperSnakeCaseToPascalCase", "upperSnakeCaseToCamelCase",
	}

	for _, name := range expectedFuncs {
		if _, ok := fm[name]; !ok {
			t.Errorf("BaseFuncMap missing function %q", name)
		}
	}
}

func TestBaseFuncMap_IntegrationWithTemplate(t *testing.T) {
	fm := BaseFuncMap()

	tests := []struct {
		name string
		tmpl string
		data any
		want string
	}{
		{
			name: "toSnakeCase",
			tmpl: `{{toSnakeCase .}}`,
			data: "FooBar",
			want: "foo_bar",
		},
		{
			name: "toCamelCase",
			tmpl: `{{toCamelCase .}}`,
			data: "foo_bar",
			want: "FooBar",
		},
		{
			name: "toLowerCamelCase",
			tmpl: `{{toLowerCamelCase .}}`,
			data: "foo_bar",
			want: "fooBar",
		},
		{
			name: "toKebabCase",
			tmpl: `{{toKebabCase .}}`,
			data: "FooBar",
			want: "foo-bar",
		},
		{
			name: "quote",
			tmpl: `{{quote .}}`,
			data: "hello",
			want: `"hello"`,
		},
		{
			name: "add",
			tmpl: `{{add .A .B}}`,
			data: struct{ A, B int }{3, 4},
			want: "7",
		},
		{
			name: "sub",
			tmpl: `{{sub .A .B}}`,
			data: struct{ A, B int }{10, 3},
			want: "7",
		},
		{
			name: "not",
			tmpl: `{{not .}}`,
			data: false,
			want: "true",
		},
		{
			name: "join",
			tmpl: `{{join .Items .Sep}}`,
			data: struct {
				Items []string
				Sep   string
			}{[]string{"a", "b", "c"}, ", "},
			want: "a, b, c",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := fstest.MapFS{
				"test.tmpl": &fstest.MapFile{Data: []byte(tt.tmpl)},
			}

			got, err := ExecuteTemplate(fsys, "test.tmpl", fm, tt.data)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(got) != tt.want {
				t.Errorf("got %q, want %q", string(got), tt.want)
			}
		})
	}
}
