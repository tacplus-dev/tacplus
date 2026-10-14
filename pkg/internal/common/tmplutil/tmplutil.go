// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package tmplutil

import (
	"bytes"
	"fmt"
	"io/fs"
	"strings"
	"text/template"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// LoadTemplate reads a template from the embedded templates filesystem under the
// given subdirectory (e.g. "python/types") and returns its contents.
//
// Every language generator needs exactly this, differing only in the
// subdirectory, so it lives here once rather than as a per-language copy. See
// docs/NAMING_CONVENTIONS.md rule 2.
func LoadTemplate(subdir, name string) (string, error) {
	sub, err := fs.Sub(templates.FS, subdir)
	if err != nil {
		return "", fmt.Errorf("failed to create sub filesystem for %s: %w", subdir, err)
	}
	content, err := fs.ReadFile(sub, name)
	if err != nil {
		return "", fmt.Errorf("failed to read template %s: %w", name, err)
	}
	return string(content), nil
}

// ExecuteTemplate loads a template from the given filesystem, applies the FuncMap,
// executes it with data, and returns the rendered bytes.
func ExecuteTemplate(fsys fs.FS, path string, funcMap template.FuncMap, data any) ([]byte, error) {
	content, err := fs.ReadFile(fsys, path)
	if err != nil {
		return nil, fmt.Errorf("failed to read template %s: %w", path, err)
	}

	tmpl, err := template.New(path).Funcs(funcMap).Parse(string(content))
	if err != nil {
		return nil, fmt.Errorf("failed to parse template %s: %w", path, err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("failed to execute template %s: %w", path, err)
	}

	return buf.Bytes(), nil
}

// BaseFuncMap returns common template functions shared across all languages.
func BaseFuncMap() template.FuncMap {
	return template.FuncMap{
		// Case conversions
		"toSnakeCase":                strutil.ToSnakeCase,
		"toKebabCase":                strutil.ToKebabCase,
		"toCamelCase":                strutil.ToCamelCase,
		"toLowerCamelCase":           strutil.ToLowerCamelCase,
		"toPascalCase":               strutil.ToPascalCase,
		"upperSnakeCaseToPascalCase": strutil.UpperSnakeCaseToPascalCase,
		"upperSnakeCaseToCamelCase":  strutil.UpperSnakeCaseToCamelCase,
		"capitalize":                 strutil.Capitalize,
		"decapitalize":               strutil.Decapitalize,
		"lcFirst":                    strutil.LcFirst,

		// String operations
		"toLower":    strings.ToLower,
		"toUpper":    strings.ToUpper,
		"trimPrefix": strings.TrimPrefix,
		"trimSuffix": strings.TrimSuffix,
		"contains":   strings.Contains,
		"hasPrefix":  strings.HasPrefix,
		"hasSuffix":  strings.HasSuffix,
		"replaceAll": strings.ReplaceAll,
		"join":       strings.Join,
		"split":      strings.Split,
		"trimSpace":  strings.TrimSpace,
		"repeat":     strings.Repeat,

		// Model helpers
		"isEnumSentinel": model.IsEnumSentinel,

		// Formatting helpers
		"quote": func(s string) string {
			return fmt.Sprintf("%q", s)
		},

		// Arithmetic
		"add": func(a, b int) int { return a + b },
		"sub": func(a, b int) int { return a - b },

		// Conditional helpers — use Go's built-in template not/and/or
		// (they handle any type: nil, empty slice, false, 0, "" are falsy)

		// Collection helpers
		"last": func(i int, a any) bool {
			switch v := a.(type) {
			case []string:
				return i == len(v)-1
			case []model.EnumValue:
				return i == len(v)-1
			case []model.Field:
				return i == len(v)-1
			case []model.Message:
				return i == len(v)-1
			case []model.Enum:
				return i == len(v)-1
			default:
				return false
			}
		},
	}
}
