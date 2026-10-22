// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"golang.org/x/tools/imports"
)

// generateGoHelpers generates a single helpers.go file containing:
// - Enum helper functions (parsers, ToString, etc.)
// - JSON/YAML serialization functions for messages
// - Constants and field-level default constants
// - TypeDefaults functions for applying default values
func generateGoHelpers(pf model.ProtoFile, baseDir, subdir string, config *tactype.Config, allFiles []model.ProtoFile) error {
	outputPath := filepath.Join(baseDir, "golang", subdir, "helpers.go")
	outputDir := filepath.Dir(outputPath)

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}

	var b strings.Builder

	// File header
	b.WriteString(header.GeneratedHeader(consts.LangGo, pf.RelPath))
	// Use the proto package name as the Go package name.
	// For sys, subdir ends with /types but we use the proto package name.
	goPackageName := pf.Package
	fmt.Fprintf(&b, "package %s\n", goPackageName)

	// When generating helpers for the errors package, error constructors like
	// NewErrMissingInvalidData are defined in the same package (no import needed).
	isSelfErrors := goPackageName == "errors"

	// Add all possible imports - goimports will remove unused ones
	b.WriteString("import (\n")
	b.WriteString("\"bytes\"\n")
	b.WriteString("\"crypto/ecdsa\"\n")
	b.WriteString("\"crypto/hmac\"\n")
	b.WriteString("\"crypto/ed25519\"\n")
	b.WriteString("\"crypto/rsa\"\n")
	b.WriteString("\"crypto/sha256\"\n")
	b.WriteString("\"encoding/base64\"\n")
	b.WriteString("\"encoding/hex\"\n")
	b.WriteString("\"encoding/json\"\n")
	b.WriteString("\"encoding/pem\"\n")
	b.WriteString("\"fmt\"\n")
	b.WriteString("\"net\"\n")
	b.WriteString("\"net/url\"\n")
	b.WriteString("\"os\"\n")
	b.WriteString("\"path/filepath\"\n")
	b.WriteString("\"regexp\"\n")
	b.WriteString("\"strconv\"\n")
	b.WriteString("\"strings\"\n")
	b.WriteString("\"time\"\n")
	b.WriteString("\"github.com/pelletier/go-toml/v2\"\n")
	b.WriteString("\"google.golang.org/protobuf/encoding/protojson\"\n")
	b.WriteString("\"google.golang.org/protobuf/proto\"\n")
	b.WriteString("\"gopkg.in/yaml.v3\"\n")

	// Add imports for cross-package types used in messages
	crossPackageImports := make(map[string]bool)
	addCrossPkgImport := func(pkg string) {
		if pkg == "" || crossPackageImports[pkg] {
			return
		}
		// Handle google.protobuf well-known types
		switch pkg {
		case "anypb":
			b.WriteString("anypb \"google.golang.org/protobuf/types/known/anypb\"\n")
			crossPackageImports[pkg] = true
			return
		case "structpb":
			b.WriteString("structpb \"google.golang.org/protobuf/types/known/structpb\"\n")
			crossPackageImports[pkg] = true
			return
		case "timestamppb":
			b.WriteString("timestamppb \"google.golang.org/protobuf/types/known/timestamppb\"\n")
			crossPackageImports[pkg] = true
			return
		case "durationpb":
			b.WriteString("durationpb \"google.golang.org/protobuf/types/known/durationpb\"\n")
			crossPackageImports[pkg] = true
			return
		case "emptypb":
			b.WriteString("emptypb \"google.golang.org/protobuf/types/known/emptypb\"\n")
			crossPackageImports[pkg] = true
			return
		case "fieldmaskpb":
			b.WriteString("fieldmaskpb \"google.golang.org/protobuf/types/known/fieldmaskpb\"\n")
			crossPackageImports[pkg] = true
			return
		case "wrapperspb":
			b.WriteString("wrapperspb \"google.golang.org/protobuf/types/known/wrapperspb\"\n")
			crossPackageImports[pkg] = true
			return
		}

		// Determine the import path for the cross-package type
		// Find which import this package comes from
		for _, imp := range pf.Imports {
			if imp.Package == pkg {
				// Construct the import path using proto package name for directory
				// e.g., "common2/product.proto" -> parentDir="common2", package="product"
				// Use the proto package name (not filename) as the source of truth.
				parentDir := filepath.Dir(imp.Path)
				dirName := imp.Package // use proto package name, not filename
				importPath := fmt.Sprintf("%s/types/%s/%s", config.TypesModulePath(), parentDir, dirName)
				fmt.Fprintf(&b, "%s \"%s\"\n", pkg, importPath)
				crossPackageImports[pkg] = true
				break
			}
		}
	}
	for _, msg := range pf.Messages {
		for _, field := range msg.Fields {
			addCrossPkgImport(field.TypePackage)
			addCrossPkgImport(field.MapValueTypePackage)
		}
	}

	// Add utils import for validation helpers (always needed unless self)
	fmt.Fprintf(&b, "utils \"%s\"\n", config.UtilsPath())
	// Add protoerrors import for error constructors (skip for errors package — they're in scope)
	if !isSelfErrors {
		fmt.Fprintf(&b, "protoerrors \"%s\"\n", config.ErrorsPath())
	}
	b.WriteString(")\n")

	// Generate enum helpers
	if len(pf.Enums) > 0 {
		b.WriteString("// ============================================================\n")
		b.WriteString("// Enum Helpers\n")
		b.WriteString("// ============================================================\n")
		for _, enum := range pf.Enums {
			if err := generateGoEnumHelpers(&b, enum); err != nil {
				return err
			}
		}
	}

	// Generate JSON/YAML/TOML serialization helpers for messages
	// Skip for the errors package — static errors.go already provides Error serialization
	if len(pf.Messages) > 0 && goPackageName != "errors" {
		b.WriteString("// ============================================================\n")
		b.WriteString("// JSON/YAML/TOML Serialization Helpers\n")
		b.WriteString("// ============================================================\n")
		for _, msg := range pf.Messages {
			if err := generateMessageSerial(&b, msg); err != nil {
				return err
			}
		}
		// Emit convertJSONNumbers once per file for ToYAML helpers
		b.WriteString("// convertJSONNumbers recursively walks a value decoded with json.UseNumber()\n")
		b.WriteString("// and converts json.Number back to int64 or float64 so YAML marshals them\n")
		b.WriteString("// as plain numbers instead of quoted strings.\n")
		b.WriteString("func convertJSONNumbers(v any) any {\n")
		b.WriteString("switch val := v.(type) {\n")
		b.WriteString("case json.Number:\n")
		b.WriteString("if i, err := val.Int64(); err == nil { return i }\n")
		b.WriteString("if f, err := val.Float64(); err == nil { return f }\n")
		b.WriteString("return val.String()\n")
		b.WriteString("case map[string]any:\n")
		b.WriteString("for k, v2 := range val { val[k] = convertJSONNumbers(v2) }\n")
		b.WriteString("return val\n")
		b.WriteString("case []any:\n")
		b.WriteString("for i, v2 := range val { val[i] = convertJSONNumbers(v2) }\n")
		b.WriteString("return val\n")
		b.WriteString("default:\n")
		b.WriteString("return v\n")
		b.WriteString("}\n")
		b.WriteString("}\n")
	}

	// Generate EnvVar helpers for messages.
	// The errors package skips the generic JSON/YAML/TOML helpers because static
	// errors.go owns Error serialization, but cross-package message helpers still
	// need errors.ErrorToEnvVar/ErrorFromEnvVar when a field references errors.Error.
	if len(pf.Messages) > 0 {
		b.WriteString("// ============================================================\n")
		b.WriteString("// Environment Variable Helpers\n")
		b.WriteString("// ============================================================\n")
		for _, msg := range pf.Messages {
			if err := generateMessageEnvVar(&b, msg, pf.RelPath); err != nil {
				return err
			}
		}
	}

	// Generate FromMap methods for messages with ParamMapConfig
	if len(pf.Messages) > 0 {
		hasParamMapMessages := false
		for _, msg := range pf.Messages {
			if msg.ParamMapConfig != nil {
				hasParamMapMessages = true
				break
			}
		}

		if hasParamMapMessages {
			b.WriteString("// ============================================================\n")
			b.WriteString("// Parameter Map Helpers\n")
			b.WriteString("// ============================================================\n")
			for _, msg := range pf.Messages {
				if msg.ParamMapConfig != nil {
					if err := generateFromMapMethod(&b, msg, pf); err != nil {
						return err
					}
				}
			}
		}
	}

	// Generate validation helpers for messages
	if len(pf.Messages) > 0 {
		b.WriteString("// ============================================================\n")
		b.WriteString("// Validation Helpers\n")
		b.WriteString("// ============================================================\n")

		// Check which validation helpers are needed
		hasEncValidation := false
		hasFilePathValidation := false

		for _, msg := range pf.Messages {
			for _, field := range msg.Fields {
				for annotationName := range field.Annotations {
					switch annotationName {
					case "assert_enc_type":
						hasEncValidation = true
					case "assert_file_path":
						hasFilePathValidation = true
					}
				}
			}
		}

		for _, msg := range pf.Messages {
			if err := generateValidationHelpers(&b, msg); err != nil {
				return err
			}
		}
		// Data-entitlement Sanitize<T> transforms. The <T>Entitlement / EntitlementLevel
		// TYPES are generated as proto; this emits the recursive behavior, recording
		// which HMAC primitives it references.
		resolver := parser.NewMessageResolver(allFiles)
		needs := &sanitizeNeeds{}
		if err := generateSanitizeHelpers(&b, pf, resolver, needs); err != nil {
			return err
		}
		generateSanitizeHmacHelpers(&b, needs)
		// Caller → <T>Entitlement builders: map the caller's token data grants onto
		// the entitlement shape Sanitize<T> consumes (fail-closed; RAW only from a
		// matching grant).
		if err := generateEntitlementBuilders(&b, pf, resolver); err != nil {
			return err
		}
		// <T>ToPublic for (spec.public_view) messages: T parsed as its SHOW-only <T>Public.
		generatePublicViewHelpers(&b, pf)

		// Add validation helper functions as needed
		// Validation helpers are now in utils package, no need to generate them inline
		if hasEncValidation {
			generateEncryptionValidationHelpers(&b)
		}
		// URL, email, FQDN, listen address, and base64 validations now use utils.ValidateAssert* functions
		// No need to generate inline helpers
		if hasFilePathValidation {
			generateFilePathValidationHelpers(&b)
		}
	}

	// Generate error helper functions for errors package
	if pf.Package == "errors" && len(pf.Messages) > 0 {
		b.WriteString("// ============================================================\n")
		b.WriteString("// Error Helper Functions\n")
		b.WriteString("// ============================================================\n")
		if err := generateErrorHelpers(&b, pf); err != nil {
			return err
		}
	}

	// Generate constants section (previously in separate const.go)
	if len(pf.Constants) > 0 || hasFieldDefaults(pf) {
		writeConstants(&b, pf)
	}

	// Generate capacity constants for bounded fields (MAX_SIZE_*, MAX_ITEMS_*)
	writeCapacityConstants(&b, pf)

	// Generate TypeDefaults functions for messages with default annotations
	writeDefaults(&b, pf)

	// Generate TypeUndefaults functions that strip default values back to zero
	writeUndefaults(&b, pf)

	// Generate Merge functions for all messages
	writeMerge(&b, pf)

	// Generate filesystem helpers (DirectoryFromFileSystem / DirectoryToFileSystem)
	if proto.EmitsFilesystemHelpers(pf.RelPath, pf.Package) {
		generateFilesystemHelpers(&b)
	}

	result := b.String()
	if isSelfErrors {
		// When generating for the errors package, error constructors like
		// NewErrMissingInvalidData are in the same package — strip the prefix.
		result = strings.ReplaceAll(result, "protoerrors.", "")
	}

	return writeAndFormat(outputPath, result)
}

// isIntegerType checks if a field type is an integer type
// Uses exact matching to avoid false positives (e.g., "pointer" containing "int")
func isIntegerType(typ string) bool {
	return typ == "int32" || typ == "int64" ||
		typ == "uint32" || typ == "uint64" ||
		typ == "sint32" || typ == "sint64" ||
		typ == "fixed32" || typ == "fixed64" ||
		typ == "sfixed32" || typ == "sfixed64"
}

// isFloatType checks if a field type is a floating-point type
func isFloatType(typ string) bool {
	return typ == "float" || typ == "double"
}

// isNumericType checks if a field type is any numeric type (integer or float)
func isNumericType(typ string) bool {
	return isIntegerType(typ) || isFloatType(typ)
}

// generateErrorHelpers generates error creation and checking functions
func generateErrorHelpers(b *strings.Builder, pf model.ProtoFile) error {
	// Find the ErrorCodeType enum
	var errorCodeEnum *model.Enum
	for i := range pf.Enums {
		if pf.Enums[i].Name == "ErrorCodeType" {
			errorCodeEnum = &pf.Enums[i]
			break
		}
	}

	if errorCodeEnum == nil {
		return nil // No ErrorCodeType enum found
	}

	// Generate getMessage helper
	b.WriteString(`func getMessage(err error, format string, a ...any) string {
	if err == nil && format == "" {
		return ""
	}

	if blueErr, ok := err.(*BlueErr); ok {
		return blueErr.Error()
	}

	if err != nil && format != "" {
		return fmt.Sprintf("%s: wrappedError=%s", fmt.Sprintf(format, a...), err.Error())
	}

	if err != nil {
		return fmt.Sprintf("wrappedError=%s", err.Error())
	}

	return fmt.Sprintf(format, a...)
}

`)

	// Generate NewErr* functions for each error code.
	// Error code enum values follow the naming convention ERROR_CODE_TYPE_<NAME>.
	// We match on substrings (NO_ERROR, NOT_FOUND, EXPIRED, CANCELLED) because
	// these are well-known protobuf error code suffixes, not annotations.
	for _, val := range errorCodeEnum.Values {
		// Skip NO_ERROR
		if strings.Contains(val.Name, "NO_ERROR") {
			continue
		}

		// Convert ERROR_CODE_TYPE_MISSING_INVALID_DATA to MissingInvalidData
		goName := convertErrorCodeToGoName(val.Name)

		// Full enum constant name: ErrorCodeType_ERROR_CODE_TYPE_MISSING_INVALID_DATA
		fullConstName := errorCodeEnum.Name + "_" + val.Name

		// Determine if this error takes an error parameter
		takesError := !strings.Contains(val.Name, "NOT_FOUND") &&
			!strings.Contains(val.Name, "EXPIRED") &&
			!strings.Contains(val.Name, "CANCELLED")

		if takesError {
			fmt.Fprintf(b, `// NewErr%s creates a new error with the %s error code.
func NewErr%s(err error, format string, a ...any) *BlueErr {
	return &BlueErr{
		Message: getMessage(err, format, a...),
		Code:    %s,
	}
}

`, goName, strings.ToLower(goName), goName, fullConstName)
		} else {
			fmt.Fprintf(b, `// NewErr%s creates a new error with the %s error code.
func NewErr%s(format string, a ...any) *BlueErr {
	return &BlueErr{
		Message: getMessage(nil, format, a...),
		Code:    %s,
	}
}

`, goName, strings.ToLower(goName), goName, fullConstName)
		}
	}

	// Generate IsErr* functions
	for _, val := range errorCodeEnum.Values {
		// Skip NO_ERROR
		if strings.Contains(val.Name, "NO_ERROR") {
			continue
		}

		goName := convertErrorCodeToGoName(val.Name)

		// Full enum constant name: ErrorCodeType_ERROR_CODE_TYPE_MISSING_INVALID_DATA
		fullConstName := errorCodeEnum.Name + "_" + val.Name

		fmt.Fprintf(b, `// IsErr%s returns true if the error is a %s error.
func IsErr%s(err error) bool {
	if blueErr, ok := err.(*BlueErr); ok {
		return blueErr.Code == %s
	}
	return false
}

`, goName, goName, goName, fullConstName)
	}

	// Generate GetErrorCode function
	fmt.Fprintf(b, `// GetErrorCode returns the error code from a BlueErr, or ErrorCodeTypeNoError if not a BlueErr.
func GetErrorCode(err error) %s {
	if blueErr, ok := err.(*BlueErr); ok {
		return blueErr.Code
	}
	return %s_ERROR_CODE_TYPE_NO_ERROR
}
`, errorCodeEnum.Name, errorCodeEnum.Name)

	return nil
}

// convertErrorCodeToGoName converts ERROR_CODE_TYPE_MISSING_INVALID_DATA to MissingInvalidData
func convertErrorCodeToGoName(protoName string) string {
	// Remove ERROR_CODE_TYPE_ prefix
	name := strings.TrimPrefix(protoName, "ErrorCodeType_")
	name = strings.TrimPrefix(name, "ERROR_CODE_TYPE_")

	// Split by underscores and capitalize each word
	parts := strings.Split(name, "_")
	var result strings.Builder
	for _, part := range parts {
		if part == "" {
			continue
		}
		result.WriteString(strings.ToUpper(part[:1]))
		result.WriteString(strings.ToLower(part[1:]))
	}
	return result.String()
}

// protoGeneratedMethods contains Go method names generated by protoc-gen-go on every
// message type. If a proto field's PascalCase name collides with one of these,
// protoc-gen-go appends an underscore (e.g., "reset" → "Reset_"). We must match.
var protoGeneratedMethods = map[string]bool{
	"Reset":        true,
	"String":       true,
	"ProtoMessage": true,
	"ProtoReflect": true,
	"Descriptor":   true,
}

// toGoFieldName converts a proto field name (snake_case) to Go field name (PascalCase)
// Example: "ip_addr" -> "IpAddr", "server_name" -> "ServerName"
// If the result collides with a protobuf generated method, an underscore is appended
// to match protoc-gen-go's behavior (e.g., "reset" -> "Reset_").
func toGoFieldName(protoName string) string {
	// Must match protoc-gen-go's struct field naming EXACTLY, or generated helpers
	// reference fields that don't exist. A naive split-on-"_" + Pascal-case diverges
	// at digit boundaries: e.g. "requests_per_second_5m" -> protoc names the field
	// "RequestsPerSecond_5M" (keeps the underscore before a digit segment and
	// upper-cases the letter after the digit), not "RequestsPerSecond5m".
	name := goCamelCase(protoName)
	if protoGeneratedMethods[name] {
		return name + "_"
	}
	return name
}

// goCamelCase mirrors protoc-gen-go's internal strs.GoCamelCase so generated
// helper field names match the protoc-gen-go struct field names exactly.
func goCamelCase(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '.' && i+1 < len(s) && isASCIILower(s[i+1]):
			// Skip over '.' in ".{{lowercase}}".
		case c == '.':
			b = append(b, '_') // convert '.' to '_'
		case c == '_' && (i == 0 || s[i-1] == '.'):
			b = append(b, 'X') // prefix leading '_' to create exported identifier
		case c == '_' && i+1 < len(s) && isASCIILower(s[i+1]):
			// Skip over '_' in "_{{lowercase}}".
		case isASCIIDigit(c):
			b = append(b, c)
		default:
			// Assume we have a letter now - if not, it's a bogus identifier.
			// The next word is a sequence of characters that must start upper case.
			if isASCIILower(c) {
				c -= 'a' - 'A' // convert lowercase to uppercase
			}
			b = append(b, c)
			// Accept lower case sequence that follows.
			for ; i+1 < len(s) && isASCIILower(s[i+1]); i++ {
				b = append(b, s[i+1])
			}
		}
	}
	return string(b)
}

func isASCIILower(c byte) bool { return 'a' <= c && c <= 'z' }
func isASCIIDigit(c byte) bool { return '0' <= c && c <= '9' }

// goZeroExpr returns the Go zero-value literal used to DROP a field.
func goZeroExpr(f model.Field) string {
	switch {
	case f.IsRepeated, f.IsMap, f.IsMessage, f.IsOptional:
		return "nil"
	case f.Type == "string":
		return `""`
	case f.Type == "bool":
		return "false"
	case f.Type == "bytes":
		return "nil"
	default:
		return "0" // numeric scalar or enum
	}
}

// generatePseudonymizeHelper emits the once-per-package pseudonymize primitive.
func generatePseudonymizeHelper(b *strings.Builder) {
	b.WriteString(`// tacplusPseudonymize returns a keyed, non-reversible pseudonym for s — an
// HMAC-SHA256 over s under key, hex-encoded with a "pseud:" prefix. The same
// (s, key) yields the same token so pseudonymized records still join on the field,
// but without the per-deployment key the token cannot be reversed by hashing the
// (possibly enumerable) input space. key must be non-empty; an empty key is a
// misconfiguration and panics rather than emitting a dictionary-reversible token.
func tacplusPseudonymize(s string, key []byte) string {
	if len(key) == 0 {
		panic("tacplusPseudonymize: empty pseudonym key (store must supply a per-deployment secret)")
	}
	if s == "" {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(s))
	return "pseud:" + hex.EncodeToString(mac.Sum(nil)[:16])
}

`)
}

// generatePseudonymizeBytesHelper emits the once-per-package bytes pseudonymize
// primitive. HMAC operates on bytes natively, so a bytes field gets the raw
// 128-bit HMAC as its surrogate (no "pseud:" prefix, no hex — the field is already
// bytes). Same (b, key) yields the same surrogate so bytes fields still join.
func generatePseudonymizeBytesHelper(b *strings.Builder) {
	b.WriteString(`// tacplusPseudonymizeBytes returns a keyed, non-reversible pseudonym for b — the
// first 128 bits of HMAC-SHA256 over b under key. The same (b, key) yields the same
// surrogate so pseudonymized records still join on the field, but without the
// per-deployment key it cannot be reversed. key must be non-empty; an empty key is
// a misconfiguration and panics rather than emitting a dictionary-reversible token.
func tacplusPseudonymizeBytes(b []byte, key []byte) []byte {
	if len(key) == 0 {
		panic("tacplusPseudonymizeBytes: empty pseudonym key (store must supply a per-deployment secret)")
	}
	if len(b) == 0 {
		return b
	}
	mac := hmac.New(sha256.New, key)
	mac.Write(b)
	return mac.Sum(nil)[:16]
}

`)
}

// writeAndFormat writes content to a file and formats it using goimports
func writeAndFormat(outputPath, content string) error {
	// Use goimports to format and fix imports
	formatted, err := imports.Process(outputPath, []byte(content), nil)
	if err != nil {
		// If goimports fails, write the unformatted content and return the error
		if writeErr := os.WriteFile(outputPath, []byte(content), 0644); writeErr != nil {
			return fmt.Errorf("failed to write file: %w (goimports error: %v)", writeErr, err)
		}
		return fmt.Errorf("goimports failed (file written anyway): %w", err)
	}

	return os.WriteFile(outputPath, formatted, 0644)
}

// generateFromMapMethod generates a FromMap method for a message with ParamMapConfig
func generateFromMapMethod(b *strings.Builder, msg model.Message, pf model.ProtoFile) error {
	if msg.ParamMapConfig == nil {
		return nil
	}

	config := msg.ParamMapConfig
	keyEnumType := config.KeyEnum

	// Find the key enum in the proto file
	var keyEnum *model.Enum
	for i := range pf.Enums {
		if pf.Enums[i].Name == keyEnumType {
			keyEnum = &pf.Enums[i]
			break
		}
	}

	if keyEnum == nil {
		return fmt.Errorf("key enum %s not found for message %s", keyEnumType, msg.Name)
	}

	// Note: We rely on fields having explicit param_key annotations that map to enum values

	// Generate function signature
	fmt.Fprintf(b, "// %sFromMap creates a %s from a map of URL parameters.\n", msg.Name, msg.Name)
	fmt.Fprintf(b, "// The parameter keys are validated against %s enum values.\n", keyEnumType)
	if config.Strict {
		b.WriteString("// Unknown parameter keys will return an error (strict mode).\n")
	} else {
		b.WriteString("// Unknown parameter keys are silently ignored (non-strict mode).\n")
	}
	fmt.Fprintf(b, "func %sFromMap(params map[string]string) (*%s, error) {\n", msg.Name, msg.Name)
	fmt.Fprintf(b, "result := &%s{}\n", msg.Name)

	// Generate parsing logic
	b.WriteString("for key, value := range params {\n")
	fmt.Fprintf(b, "paramKey, err := Parse%s(key)\n", keyEnumType)
	b.WriteString("if err != nil {\n")
	if config.Strict {
		b.WriteString("return nil, protoerrors.NewErrMissingInvalidData(nil, \"invalid parameter key: %s\", key)\n")
	} else {
		b.WriteString("continue // Ignore unknown keys in non-strict mode\n")
	}
	b.WriteString("}\n")

	b.WriteString("switch paramKey {\n")

	// Generate case for each field that has a param_key annotation
	for _, field := range msg.Fields {
		if field.ParamKey == "" {
			continue // Skip fields without param_key annotation
		}

		fmt.Fprintf(b, "case %s_%s:\n", keyEnumType, field.ParamKey)

		// Generate field assignment based on type
		if field.IsRepeated {
			// For repeated fields, split by comma
			b.WriteString("if value != \"\" {\n")
			fmt.Fprintf(b, "result.%s = strings.Split(value, \",\")\n", strutil.ToCamelCase(field.Name))
			b.WriteString("}\n")
		} else if isNumericType(field.Type) {
			// For numeric fields, parse the value
			if isFloatType(field.Type) {
				b.WriteString("if floatVal, err := strconv.ParseFloat(value, 64); err == nil {\n")
				fmt.Fprintf(b, "result.%s = &floatVal\n", strutil.ToCamelCase(field.Name))
				b.WriteString("}\n")
			} else {
				b.WriteString("if intVal, err := strconv.ParseInt(value, 10, 64); err == nil {\n")
				if field.Type == "int32" {
					b.WriteString("int32Val := int32(intVal)\n")
					fmt.Fprintf(b, "result.%s = &int32Val\n", strutil.ToCamelCase(field.Name))
				} else {
					fmt.Fprintf(b, "result.%s = &intVal\n", strutil.ToCamelCase(field.Name))
				}
				b.WriteString("}\n")
			}
		} else if field.IsEnum {
			// For enum fields, parse using the Parse<EnumType> function
			fmt.Fprintf(b, "if enumVal, err := Parse%s(value); err == nil {\n", field.Type)
			fmt.Fprintf(b, "result.%s = &enumVal\n", strutil.ToCamelCase(field.Name))
			b.WriteString("}\n")
		} else {
			// For string fields
			fmt.Fprintf(b, "result.%s = &value\n", strutil.ToCamelCase(field.Name))
		}
	}

	b.WriteString("}\n")
	b.WriteString("}\n")

	b.WriteString("return result, nil\n")
	b.WriteString("}\n")

	// Generate ToMap method
	if err := generateToMapMethod(b, msg, pf); err != nil {
		return err
	}

	return nil
}

// generateToMapMethod generates a ToMap method for a message with ParamMapConfig
func generateToMapMethod(b *strings.Builder, msg model.Message, pf model.ProtoFile) error {
	if msg.ParamMapConfig == nil {
		return nil
	}

	config := msg.ParamMapConfig
	keyEnumType := config.KeyEnum

	// Find the key enum in the proto file
	var keyEnum *model.Enum
	for i := range pf.Enums {
		if pf.Enums[i].Name == keyEnumType {
			keyEnum = &pf.Enums[i]
			break
		}
	}

	if keyEnum == nil {
		return fmt.Errorf("key enum %s not found for message %s", keyEnumType, msg.Name)
	}

	// Build a map of enum value name -> json_return_value
	enumValueToJsonValue := make(map[string]string)
	for _, enumVal := range keyEnum.Values {
		if enumVal.JsonValue != "" {
			enumValueToJsonValue[enumVal.Name] = enumVal.JsonValue
		}
	}

	// Generate function signature
	fmt.Fprintf(b, "// %sToMap converts a %s to a map of URL parameters.\n", msg.Name, msg.Name)
	fmt.Fprintf(b, "// The map keys are the json_return_value of %s enum values.\n", keyEnumType)
	b.WriteString("// Only non-nil/non-empty fields are included in the map.\n")
	fmt.Fprintf(b, "func %sToMap(m *%s) (map[string]string, error) {\n", msg.Name, msg.Name)
	b.WriteString("if m == nil {\n")
	b.WriteString("return make(map[string]string), nil\n")
	b.WriteString("}\n")
	b.WriteString("result := make(map[string]string)\n")

	// Generate conversion logic for each field with param_key annotation
	for _, field := range msg.Fields {
		if field.ParamKey == "" {
			continue // Skip fields without param_key annotation
		}

		// Get the json_return_value for this enum value
		jsonKey, hasJsonKey := enumValueToJsonValue[field.ParamKey]
		if !hasJsonKey {
			continue // Skip if enum value doesn't have json_return_value
		}

		camelName := strutil.ToCamelCase(field.Name)

		// Generate field conversion based on type
		if field.IsRepeated {
			// For repeated fields, join with comma
			fmt.Fprintf(b, "if len(m.%s) > 0 {\n", camelName)
			fmt.Fprintf(b, "result[\"%s\"] = strings.Join(m.%s, \",\")\n", jsonKey, camelName)
			b.WriteString("}\n")
		} else if isNumericType(field.Type) {
			// For numeric fields, convert to string
			fmt.Fprintf(b, "if m.%s != nil {\n", camelName)
			if isFloatType(field.Type) {
				fmt.Fprintf(b, "result[\"%s\"] = fmt.Sprintf(\"%%g\", *m.%s)\n", jsonKey, camelName)
			} else {
				fmt.Fprintf(b, "result[\"%s\"] = fmt.Sprintf(\"%%d\", *m.%s)\n", jsonKey, camelName)
			}
			b.WriteString("}\n")
		} else if field.IsEnum {
			// For enum fields, convert to string using <EnumType>ToString function
			// Check against the UNDEFINED value (convention: <EnumType>_<ENUM_TYPE>_UNDEFINED)
			undefinedValue := fmt.Sprintf("%s_%s_UNDEFINED", field.Type, strings.ToUpper(strutil.ToSnakeCase(field.Type)))
			fmt.Fprintf(b, "if m.%s != nil && *m.%s != %s {\n", camelName, camelName, undefinedValue)
			fmt.Fprintf(b, "result[\"%s\"] = %sToString(*m.%s)\n", jsonKey, field.Type, camelName)
			b.WriteString("}\n")
		} else {
			// For string fields
			fmt.Fprintf(b, "if m.%s != nil && *m.%s != \"\" {\n", camelName, camelName)
			fmt.Fprintf(b, "result[\"%s\"] = *m.%s\n", jsonKey, camelName)
			b.WriteString("}\n")
		}
	}

	b.WriteString("return result, nil\n")
	b.WriteString("}\n")

	return nil
}
