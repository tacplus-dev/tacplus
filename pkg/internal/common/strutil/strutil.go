// Copyright (c) 2026 Second Star Cavalier LLC

package strutil

import (
	"strings"
	"unicode"
)

// ToSnakeCase converts PascalCase or camelCase to snake_case.
// Consecutive uppercase letters are treated as acronyms (matching tonic/heck behavior).
// Examples:
//   - "FooBar" -> "foo_bar"
//   - "fooBar" -> "foo_bar"
//   - "HTTPServer" -> "http_server"
//   - "OpenAIService" -> "open_ai_service"
//   - "getHTTPResponse" -> "get_http_response"
func ToSnakeCase(s string) string {
	runes := []rune(s)
	var result strings.Builder
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			// Insert underscore when:
			// 1. Previous char is lowercase (transition from word to new word/acronym)
			// 2. Previous char is uppercase AND next char is lowercase (end of acronym)
			// 3. Previous char is a digit (transition from number to new word)
			if unicode.IsLower(prev) || unicode.IsDigit(prev) {
				result.WriteRune('_')
			} else if unicode.IsUpper(prev) && i+1 < len(runes) && unicode.IsLower(runes[i+1]) {
				result.WriteRune('_')
			}
		}
		result.WriteRune(unicode.ToLower(r))
	}
	return result.String()
}

// ToKebabCase converts PascalCase or camelCase to kebab-case
// Examples:
//   - "FooBar" -> "foo-bar"
//   - "fooBar" -> "foo-bar"
func ToKebabCase(s string) string {
	return strings.ReplaceAll(ToSnakeCase(s), "_", "-")
}

// ToCamelCase converts snake_case or kebab-case to PascalCase (upper camel case)
// Examples:
//   - "foo_bar" -> "FooBar"
//   - "foo_bar_baz" -> "FooBarBaz"
//   - "iron-maiden" -> "IronMaiden"
func ToCamelCase(s string) string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' })
	for i := range parts {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

// ToLowerCamelCase converts PascalCase to lowerCamelCase or snake_case to lowerCamelCase
// Examples:
//   - "FooBar" -> "fooBar"
//   - "foo_bar" -> "fooBar"
func ToLowerCamelCase(s string) string {
	if len(s) == 0 {
		return s
	}

	// If it contains underscores or hyphens, split and convert
	if strings.ContainsAny(s, "_-") {
		parts := strings.FieldsFunc(s, func(r rune) bool { return r == '_' || r == '-' })
		for i := range parts {
			if len(parts[i]) > 0 {
				if i == 0 {
					parts[i] = strings.ToLower(parts[i])
				} else {
					parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
				}
			}
		}
		return strings.Join(parts, "")
	}

	// Otherwise just lowercase the first character
	return strings.ToLower(s[:1]) + s[1:]
}

// UpperSnakeCaseToPascalCase converts UPPER_SNAKE_CASE to PascalCase
// Examples:
//   - "FOO_BAR" -> "FooBar"
//   - "HTTP_STATUS" -> "HttpStatus"
func UpperSnakeCaseToPascalCase(s string) string {
	parts := strings.Split(s, "_")
	for i := range parts {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + strings.ToLower(parts[i][1:])
		}
	}
	return strings.Join(parts, "")
}

// UpperSnakeCaseToCamelCase converts UPPER_SNAKE_CASE to lowerCamelCase
// Examples:
//   - "FOO_BAR" -> "fooBar"
//   - "HTTP_STATUS" -> "httpStatus"
func UpperSnakeCaseToCamelCase(s string) string {
	parts := strings.Split(s, "_")
	for i := range parts {
		if len(parts[i]) > 0 {
			if i == 0 {
				parts[i] = strings.ToLower(parts[i])
			} else {
				parts[i] = strings.ToUpper(parts[i][:1]) + strings.ToLower(parts[i][1:])
			}
		}
	}
	return strings.Join(parts, "")
}

// SnakeToCamelCase converts snake_case to lowerCamelCase (alias for ToLowerCamelCase)
func SnakeToCamelCase(s string) string {
	return ToLowerCamelCase(s)
}

// Capitalize capitalizes the first letter of a string
// Examples:
//   - "foo" -> "Foo"
//   - "fooBar" -> "FooBar"
func Capitalize(s string) string {
	if len(s) == 0 {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// Decapitalize lowercases the first letter of a string
// Examples:
//   - "Foo" -> "foo"
//   - "FooBar" -> "fooBar"
func Decapitalize(s string) string {
	if len(s) == 0 {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// ToPascalCase is an alias for ToCamelCase (both produce upper camel case)
func ToPascalCase(s string) string {
	return ToCamelCase(s)
}

// ToTonicCamelCase normalizes PascalCase to match tonic/heck's CamelCase convention.
// This round-trips through snake_case so that acronyms are split and recombined:
// "OpenAIService" -> "open_ai_service" -> "OpenAiService"
// This matches the struct names tonic generates from proto service names.
func ToTonicCamelCase(s string) string {
	return ToCamelCase(ToSnakeCase(s))
}

// LcFirst is an alias for Decapitalize (lowercase first character)
func LcFirst(s string) string {
	return Decapitalize(s)
}

// ToLowerFirst is an alias for Decapitalize
func ToLowerFirst(s string) string {
	return Decapitalize(s)
}

// DefaultConstName returns the constant name for a field's default value.
// When multiple messages in the same file have the same field name with defaults,
// the message name is prefixed to avoid collisions.
// E.g., "retention_days" in both ClickHouseConfig and DgraphConfig produces
// "DEFAULT_CLICKHOUSECONFIG_RETENTION_DAYS" and "DEFAULT_DGRAPHCONFIG_RETENTION_DAYS"
// instead of duplicate "DEFAULT_RETENTION_DAYS".
func DefaultConstName(msgName, fieldName string, duplicateFields map[string]bool) string {
	upper := strings.ToUpper(fieldName)
	if duplicateFields[fieldName] {
		return "DEFAULT_" + strings.ToUpper(msgName) + "_" + upper
	}
	return "DEFAULT_" + upper
}

// FindDuplicateDefaultFields scans messages for field names that appear in
// multiple messages with default annotations. Returns a set of duplicated names.
func FindDuplicateDefaultFields(fieldsByMsg []struct {
	MsgName    string
	FieldNames []string
}) map[string]bool {
	counts := make(map[string]int)
	for _, m := range fieldsByMsg {
		for _, f := range m.FieldNames {
			counts[f]++
		}
	}
	dups := make(map[string]bool)
	for name, count := range counts {
		if count > 1 {
			dups[name] = true
		}
	}
	return dups
}

// StripPackagePrefix removes a redundant package prefix from a snake_case filename stem.
// When a service file lives inside a directory named after its package, the package name
// in the filename is redundant. E.g. for package "identity":
//   - "identity_service" → "service"
//   - "identity_config_state_service" → "config_state_service"
//   - "mint_service" → "mint_service" (no match, returned as-is)
func StripPackagePrefix(snake, packageName string) string {
	prefix := ToSnakeCase(packageName) + "_"
	if strings.HasPrefix(snake, prefix) {
		return snake[len(prefix):]
	}
	return snake
}
