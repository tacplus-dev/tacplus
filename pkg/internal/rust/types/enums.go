// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"io/fs"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/tmplutil"
	"github.com/tacplus-dev/tacplus/templates"
)

// rustEnumValueData holds pre-computed data for a single enum value.
type rustEnumValueData struct {
	LowerName    string // normalized lowercase name with hyphens
	VariantName  string // prost variant name (PascalCase)
	DisplayName  string // display name (json value or lower name)
	FriendlyName string // friendly name for test assertions
	HasAlias     bool   // whether Go's friendly name differs from LowerName
	Alias        string // Go's friendly name, normalized (also accepted by FromStr)
}

// rustEnumTemplateData holds pre-computed data for the enums template.
type rustEnumTemplateData struct {
	EnumName          string
	LowerEnumName     string
	ValidValues       []rustEnumValueData
	HasSentinels      bool
	ValidCount        int
	FirstFriendlyName string
	FirstVariantName  string
}

// toProstEnumName converts proto enum name to prost's PascalCase format
// Example: ALPNProtocol -> AlpnProtocol, X509KeyUsage -> X509KeyUsage, TypeID -> TypeId,
// ContractS2SRpcId -> ContractS2sRpcId (a digit inside a capital run does not
// end the word, so the S after it is lowercased as heck does)
func toProstEnumName(protoName string) string {
	var result strings.Builder
	i := 0

	for i < len(protoName) {
		ch := protoName[i]

		// If this is an uppercase letter, check if it starts an acronym
		if ch >= 'A' && ch <= 'Z' {
			// Look ahead to see if this is an acronym (2+ uppercase letters followed by lowercase)
			// Pattern: XXXXYz where X are uppercase, Y is uppercase, z is lowercase
			// We want: XxxxYz (first X uppercase, middle X's lowercase, Y stays as start of next word)
			j := i
			for j < len(protoName) && (protoName[j] >= 'A' && protoName[j] <= 'Z' || protoName[j] >= '0' && protoName[j] <= '9') {
				j++
			}
			// A run ending in digits ends before them: the digits trail the
			// word (X509 stays one word with Key after it).
			for j > i+1 && protoName[j-1] >= '0' && protoName[j-1] <= '9' {
				j--
			}

			// j now points to first non-uppercase character (or end of string)
			numUppercase := j - i

			if numUppercase >= 2 && j < len(protoName) && protoName[j] >= 'a' && protoName[j] <= 'z' {
				// We have an acronym: 2+ uppercase followed by lowercase
				// Convert first letter (keep uppercase), middle letters (to lowercase)
				// Keep last uppercase letter as-is (it starts the next word)
				result.WriteByte(protoName[i]) // First letter stays uppercase
				for k := i + 1; k < j-1; k++ {
					result.WriteByte(toLowerASCII(protoName[k])) // Middle letters to lowercase
				}
				i = j - 1 // Position at last uppercase (which starts next word)
			} else if numUppercase >= 2 && j == len(protoName) {
				// A trailing acronym is one word to prost (heck): TypeID -> TypeId.
				result.WriteByte(protoName[i])
				for k := i + 1; k < j; k++ {
					result.WriteByte(toLowerASCII(protoName[k]))
				}
				i = j
			} else {
				// Not an acronym pattern, just write the character
				result.WriteByte(ch)
				i++
			}
		} else {
			// Lowercase or other character
			result.WriteByte(ch)
			i++
		}
	}

	return result.String()
}

func toLowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c - 'A' + 'a'
	}
	return c
}

// writeEnumHelpers writes enum helper implementations to the shared builder
func writeEnumHelpers(b *strings.Builder, pf model.ProtoFile) {
	if len(pf.Enums) == 0 {
		return
	}

	b.WriteString("// ---- Enum Helpers ----\n")

	for _, enum := range pf.Enums {
		if err := generateEnumImpl(b, enum); err != nil {
			// Fallback: write an error comment
			fmt.Fprintf(b, "// ERROR generating enum %s: %s\n", enum.Name, err)
		}
	}
}

// generateEnumImpl generates implementation for a single enum
func generateEnumImpl(b *strings.Builder, enum model.Enum) error {
	enumName := toProstEnumName(enum.Name)

	// Filter out sentinel values (UNDEFINED/NO_ERROR/UNSPECIFIED)
	var validValues []rustEnumValueData
	for _, v := range enum.Values {
		if model.IsEnumSentinel(v.Name) {
			continue
		}

		lowerName := strings.ToLower(strings.ReplaceAll(v.Name, "_", "-"))
		variantName := toProstEnumVariant(v.Name, enumName)

		displayName := lowerName
		if v.JsonValue != "" {
			displayName = v.JsonValue
		}

		friendlyName := displayName

		alias := goEnumFriendlyAlias(enum, v)

		validValues = append(validValues, rustEnumValueData{
			LowerName:    lowerName,
			VariantName:  variantName,
			DisplayName:  displayName,
			FriendlyName: friendlyName,
			HasAlias:     alias != lowerName,
			Alias:        alias,
		})
	}

	if len(validValues) == 0 {
		return nil
	}

	data := rustEnumTemplateData{
		EnumName:     enumName,
		ValidValues:  validValues,
		HasSentinels: len(validValues) < len(enum.Values),
	}

	typeFS, err := fs.Sub(templates.FS, "rust/types")
	if err != nil {
		return err
	}

	funcMap := tmplutil.BaseFuncMap()
	result, err := tmplutil.ExecuteTemplate(typeFS, "enums.rs.tmpl", funcMap, data)
	if err != nil {
		return err
	}

	b.Write(result)
	return nil
}

// writeEnumTestContent writes enum test functions into the combined test module (no #[cfg(test)] wrapper)
func writeEnumTestContent(b *strings.Builder, enum model.Enum) {
	// Filter out sentinel values (UNDEFINED/NO_ERROR/UNSPECIFIED)
	var validValues []rustEnumValueData
	for _, v := range enum.Values {
		if model.IsEnumSentinel(v.Name) {
			continue
		}

		enumName := toProstEnumName(enum.Name)
		lowerName := strings.ToLower(strings.ReplaceAll(v.Name, "_", "-"))
		variantName := toProstEnumVariant(v.Name, enumName)

		displayName := lowerName
		if v.JsonValue != "" {
			displayName = v.JsonValue
		}

		alias := goEnumFriendlyAlias(enum, v)

		validValues = append(validValues, rustEnumValueData{
			LowerName:    lowerName,
			VariantName:  variantName,
			DisplayName:  displayName,
			FriendlyName: displayName,
			HasAlias:     alias != lowerName,
			Alias:        alias,
		})
	}

	if len(validValues) == 0 {
		return
	}

	enumName := toProstEnumName(enum.Name)
	lowerEnumName := strings.ToLower(enumName)

	firstFriendlyName := ""
	firstVariantName := ""
	if len(validValues) > 0 {
		firstFriendlyName = validValues[0].FriendlyName
		firstVariantName = validValues[0].VariantName
	}

	data := rustEnumTemplateData{
		EnumName:          enumName,
		LowerEnumName:     lowerEnumName,
		ValidValues:       validValues,
		ValidCount:        len(validValues),
		FirstFriendlyName: firstFriendlyName,
		FirstVariantName:  firstVariantName,
	}

	typeFS, err := fs.Sub(templates.FS, "rust/types")
	if err != nil {
		fmt.Fprintf(b, "// ERROR loading template FS: %s\n", err)
		return
	}

	funcMap := tmplutil.BaseFuncMap()
	result, err := tmplutil.ExecuteTemplate(typeFS, "enums_test.rs.tmpl", funcMap, data)
	if err != nil {
		fmt.Fprintf(b, "// ERROR executing enum test template: %s\n", err)
		return
	}

	b.Write(result)
}

// goEnumFriendlyAlias returns the string Go's generated <Enum>ToString emits for
// a value, normalized the way FromStr normalizes its input (lowercase, hyphens).
// It mirrors enumFriendlyString in pkg/internal/go/types/enums.go: the JSON value
// when annotated, otherwise the value name with the enum-name prefix stripped
// (PROFILE_GATE -> "gate"). FromStr accepts it so a value a Go writer produced
// parses in Rust.
func goEnumFriendlyAlias(enum model.Enum, value model.EnumValue) string {
	name := value.JsonValue
	if name == "" {
		prefix := strings.ToUpper(strutil.ToSnakeCase(enum.Name) + "_")
		name = strings.TrimPrefix(value.Name, prefix)
	}
	return strings.ToLower(strings.ReplaceAll(name, "_", "-"))
}

// toProsto converts proto enum value names to prost's Rust variant names
// Example: "X509_EXT_KEY_USAGE_SERVER_AUTH" -> "ServerAuth"
// Example: "IP_ADDR_TYPE_IP_V4" -> "IpAddrTypeIpV4"
// Prost strips the prefix only if the value starts with the enum name in SCREAMING_SNAKE_CASE
func toProstEnumVariant(protoValue string, enumName string) string {
	// Prost's strategy:
	// 1. Convert enum name to SCREAMING_SNAKE_CASE
	// 2. If proto value starts with that prefix + "_", strip it
	// 3. Otherwise, keep the full proto value
	// 4. Convert result to PascalCase

	// Convert CamelCase enum name to SCREAMING_SNAKE_CASE
	var enumSnake strings.Builder
	for i, r := range enumName {
		if i > 0 && r >= 'A' && r <= 'Z' {
			enumSnake.WriteRune('_')
		}
		enumSnake.WriteRune(r)
	}
	expectedPrefix := strings.ToUpper(enumSnake.String()) + "_"

	// Check if proto value starts with expected prefix
	parts := strings.Split(protoValue, "_")
	startIdx := 0

	if strings.HasPrefix(protoValue, expectedPrefix) {
		// Strip the prefix - count how many parts to skip
		prefixLen := len(strings.Split(strings.TrimSuffix(expectedPrefix, "_"), "_"))
		startIdx = prefixLen
	}

	// Convert remaining parts to PascalCase
	if startIdx >= len(parts) {
		startIdx = 0
	}

	valueParts := parts[startIdx:]

	// prost strips the enum-name prefix only when the remainder starts with an
	// uppercase character (strip_enum_prefix in prost-build's src/ident.rs), so
	// a value like AUTH_METHOD_2FA keeps the enum name instead of reducing to a
	// variant that starts with a digit.
	for _, part := range valueParts {
		if len(part) == 0 {
			continue
		}
		if part[0] >= '0' && part[0] <= '9' {
			valueParts = parts
		}
		break
	}

	var result strings.Builder

	for _, part := range valueParts {
		if len(part) == 0 {
			continue
		}
		// Convert to title case: first letter uppercase, rest lowercase
		result.WriteString(strings.ToUpper(string(part[0])))
		if len(part) > 1 {
			result.WriteString(strings.ToLower(part[1:]))
		}
	}

	return escapeRustVariantLeadingDigit(escapeRustVariantKeyword(result.String()))
}

// escapeRustVariantKeyword applies prost's identifier sanitizing to a
// PascalCase enum variant name. Rust's keywords are lowercase, so the only one
// a PascalCase variant can collide with is "Self" (from a proto value like
// AUTH_METHOD_SELF). "Self" is one of the keywords Rust refuses as a raw
// identifier, so r#Self does not compile and escapeRustKeyword's r# form is
// wrong here; prost suffixes an underscore instead. See prost-build's
// sanitize_identifier in src/ident.rs.
func escapeRustVariantKeyword(variant string) string {
	if variant == "Self" {
		return "Self_"
	}
	return variant
}

// escapeRustVariantLeadingDigit applies prost's identifier sanitizing to an
// enum variant name that starts with a digit (from a proto value like _2FA,
// whose leading underscore is dropped by the case conversion). Rust
// identifiers cannot start with a digit, so prost prefixes an underscore.
// Proto identifiers are ASCII, so an ASCII digit test covers prost's
// is_numeric check. See prost-build's sanitize_identifier in src/ident.rs.
func escapeRustVariantLeadingDigit(variant string) string {
	if len(variant) > 0 && variant[0] >= '0' && variant[0] <= '9' {
		return "_" + variant
	}
	return variant
}
