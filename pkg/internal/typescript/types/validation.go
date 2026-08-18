// Copyright (c) 2026 Second Star Cavalier LLC

// TypeScript validation emission: per-message validate functions, assert_*
// constraint checks, default collection/scalar size bounds, oneof scalar
// bounds, the nesting-depth guard, and enum vocabulary checks. Moved from
// helpers_gen.go; shared (non-validation) helpers remain there.

package types

import (
	"fmt"
	"os"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/collection"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// generateTypeScriptValidationFunction generates a validate function for a message
func generateTypeScriptValidationFunction(b *strings.Builder, msg model.Message) error {
	funcName := fmt.Sprintf("validate%s", msg.Name)

	fmt.Fprintf(b, `/**
 * Validates all fields in %s according to their annotations.
 * @throws {BlueErr} if validation fails
 */
export function %s(m: %s | undefined, depth = 0): void {
	if (!m) {
		return;
	}
	if (depth > 24) {
		throw newErrMissingInvalidData(undefined, 'message nesting exceeds max depth of 24');
	}

`, msg.Name, funcName, msg.Name)

	// Generate validation code for each field with annotations
	hasValidations := false
	for _, field := range msg.Fields {
		if field.IsOneOf {
			if len(field.Annotations) == 0 {
				continue
			}
			fieldName := upperSnakeCaseToCamelCase(field.Name)
			for _, annotationName := range model.SortedAnnotationNames(field.Annotations) {
				annotationValue := field.Annotations[annotationName]
				if annotationValue.Type == model.AnnotationTypeBool && !annotationValue.BoolValue {
					continue
				}
				if annotationName != "assert_max_size" {
					continue
				}
				if annotationValue.Type != model.AnnotationTypeInt {
					return fmt.Errorf("assert_max_size annotation on field %s.%s must be an integer value", msg.Name, fieldName)
				}
				if field.Type != "string" && field.Type != "bytes" {
					continue
				}
				hasValidations = true
				writeTypeScriptOneofScalarMaxSize(b, field, fieldName, annotationValue.IntValue, "assert_max_size")
			}
			continue
		}
		if len(field.Annotations) == 0 {
			continue
		}

		hasValidations = true
		// Convert field name to camelCase for TypeScript
		fieldName := upperSnakeCaseToCamelCase(field.Name)

		for _, annotationName := range model.SortedAnnotationNames(field.Annotations) {
			annotationValue := field.Annotations[annotationName]
			// Skip boolean annotations that are set to false
			if annotationValue.Type == model.AnnotationTypeBool && !annotationValue.BoolValue {
				continue
			}

			// Generate validation code based on annotation type
			switch annotationName {
			case "assert_required":
				// Generate required field validation
				fmt.Fprintf(b, `	// Validate %s (assert_required)
	if (!m.%s) {
		throw newErrMissingInvalidData(undefined, `+"`field %s is required`"+`);
	}

`, fieldName, fieldName, fieldName)

			case "assert_ipaddr":
				// Simple IP address validation (IPv4 or IPv6)
				fmt.Fprintf(b, `	// Validate %s (assert_ipaddr)
	if (m.%s) {
		const ipv4Regex = /^((25[0-5]|(2[0-4]|1\d|[1-9]|)\d)\.?\b){4}$/;
		const ipv6Regex = /^(([0-9a-fA-F]{1,4}:){7}[0-9a-fA-F]{1,4}|([0-9a-fA-F]{1,4}:){1,7}:|([0-9a-fA-F]{1,4}:){1,6}:[0-9a-fA-F]{1,4}|([0-9a-fA-F]{1,4}:){1,5}(:[0-9a-fA-F]{1,4}){1,2}|([0-9a-fA-F]{1,4}:){1,4}(:[0-9a-fA-F]{1,4}){1,3}|([0-9a-fA-F]{1,4}:){1,3}(:[0-9a-fA-F]{1,4}){1,4}|([0-9a-fA-F]{1,4}:){1,2}(:[0-9a-fA-F]{1,4}){1,5}|[0-9a-fA-F]{1,4}:((:[0-9a-fA-F]{1,4}){1,6})|:((:[0-9a-fA-F]{1,4}){1,7}|:)|fe80:(:[0-9a-fA-F]{0,4}){0,4}%%[0-9a-zA-Z]{1,}|::(ffff(:0{1,4}){0,1}:){0,1}((25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9])\.){3}(25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9])|([0-9a-fA-F]{1,4}:){1,4}:((25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9])\.){3}(25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9]))$/;
		if (!ipv4Regex.test(m.%s) && !ipv6Regex.test(m.%s)) {
			throw newErrMissingInvalidData(undefined, `+"`field %s must be a valid IPv4 or IPv6 address`"+`);
		}
	}

`, fieldName, fieldName, fieldName, fieldName, fieldName)

			case "assert_ipaddr_type":
				if annotationValue.Type != model.AnnotationTypeEnum {
					return fmt.Errorf("assert_ipaddr_type annotation on field %s.%s must be an enum value", msg.Name, fieldName)
				}
				if err := writeTsIPAddrTypeValidation(b, msg, field, fieldName, annotationValue.EnumValue); err != nil {
					return err
				}

			case "assert_duration":
				// Generate duration validation
				if annotationValue.Type != model.AnnotationTypeEnum {
					return fmt.Errorf("assert_duration annotation on field %s.%s must be an enum value", msg.Name, fieldName)
				}

				switch annotationValue.EnumValue {
				case "DURATION_TYPE_POSITIVE":
					fmt.Fprintf(b, `	// Validate %s (assert_duration: POSITIVE)
	if (m.%s !== undefined && m.%s <= 0) {
		throw newErrMissingInvalidData(undefined, `+"`field %s must be positive (> 0), got: ${m.%s}`"+`);
	}

`, fieldName, fieldName, fieldName, fieldName, fieldName)
				case "DURATION_TYPE_NON_NEGATIVE":
					fmt.Fprintf(b, `	// Validate %s (assert_duration: NON_NEGATIVE)
	if (m.%s !== undefined && m.%s < 0) {
		throw newErrMissingInvalidData(undefined, `+"`field %s must be non-negative (>= 0), got: ${m.%s}`"+`);
	}

`, fieldName, fieldName, fieldName, fieldName, fieldName)
				}

			case "assert_duration_min":
				// Generate minimum duration validation
				if annotationValue.Type != model.AnnotationTypeInt {
					return fmt.Errorf("assert_duration_min annotation on field %s.%s must be an integer value", msg.Name, fieldName)
				}

				fmt.Fprintf(b, `	// Validate %s (assert_duration_min: %d)
	if (m.%s !== undefined && m.%s < %d) {
		throw newErrMissingInvalidData(undefined, `+"`field %s must be >= %d, got: ${m.%s}`"+`);
	}

`, fieldName, annotationValue.IntValue, fieldName, fieldName, annotationValue.IntValue, fieldName, annotationValue.IntValue, fieldName)

			case "assert_duration_max":
				// Generate maximum duration validation
				if annotationValue.Type != model.AnnotationTypeInt {
					return fmt.Errorf("assert_duration_max annotation on field %s.%s must be an integer value", msg.Name, fieldName)
				}

				fmt.Fprintf(b, `	// Validate %s (assert_duration_max: %d)
	if (m.%s !== undefined && m.%s > %d) {
		throw newErrMissingInvalidData(undefined, `+"`field %s must be <= %d, got: ${m.%s}`"+`);
	}

`, fieldName, annotationValue.IntValue, fieldName, fieldName, annotationValue.IntValue, fieldName, annotationValue.IntValue, fieldName)

			case "assert_gt":
				// Simple greater than comparison
				if annotationValue.Type != model.AnnotationTypeInt {
					return fmt.Errorf("assert_gt annotation on field %s.%s must be an integer value", msg.Name, fieldName)
				}

				fmt.Fprintf(b, `	// Validate %s (assert_gt: %d)
	if (m.%s !== undefined && m.%s <= %d) {
		throw newErrMissingInvalidData(undefined, `+"`field %s must be > %d, got: ${m.%s}`"+`);
	}

`, fieldName, annotationValue.IntValue, fieldName, fieldName, annotationValue.IntValue, fieldName, annotationValue.IntValue, fieldName)

			case "assert_ge":
				// Simple greater than or equal comparison
				if annotationValue.Type != model.AnnotationTypeInt {
					return fmt.Errorf("assert_ge annotation on field %s.%s must be an integer value", msg.Name, fieldName)
				}

				fmt.Fprintf(b, `	// Validate %s (assert_ge: %d)
	if (m.%s !== undefined && m.%s < %d) {
		throw newErrMissingInvalidData(undefined, `+"`field %s must be >= %d, got: ${m.%s}`"+`);
	}

`, fieldName, annotationValue.IntValue, fieldName, fieldName, annotationValue.IntValue, fieldName, annotationValue.IntValue, fieldName)

			case "assert_lt":
				// Simple less than comparison
				if annotationValue.Type != model.AnnotationTypeInt {
					return fmt.Errorf("assert_lt annotation on field %s.%s must be an integer value", msg.Name, fieldName)
				}

				fmt.Fprintf(b, `	// Validate %s (assert_lt: %d)
	if (m.%s !== undefined && m.%s >= %d) {
		throw newErrMissingInvalidData(undefined, `+"`field %s must be < %d, got: ${m.%s}`"+`);
	}

`, fieldName, annotationValue.IntValue, fieldName, fieldName, annotationValue.IntValue, fieldName, annotationValue.IntValue, fieldName)

			case "assert_le":
				// Simple less than or equal comparison
				if annotationValue.Type != model.AnnotationTypeInt {
					return fmt.Errorf("assert_le annotation on field %s.%s must be an integer value", msg.Name, fieldName)
				}

				fmt.Fprintf(b, `	// Validate %s (assert_le: %d)
	if (m.%s !== undefined && m.%s > %d) {
		throw newErrMissingInvalidData(undefined, `+"`field %s must be <= %d, got: ${m.%s}`"+`);
	}

`, fieldName, annotationValue.IntValue, fieldName, fieldName, annotationValue.IntValue, fieldName, annotationValue.IntValue, fieldName)

			case "assert_email":
				// Generate email validation
				fmt.Fprintf(b, `	// Validate %s (assert_email)
	if (m.%s) {
		const emailRegex = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
		if (!emailRegex.test(m.%s)) {
			throw newErrMissingInvalidData(undefined, `+"`field %s must be a valid email address`"+`);
		}
	}

`, fieldName, fieldName, fieldName, fieldName)

			case "assert_url":
				if field.Type != "string" {
					return fmt.Errorf("assert_url annotation on field %s.%s requires string type", msg.Name, fieldName)
				}
				if annotationValue.Type != model.AnnotationTypeEnum {
					return fmt.Errorf("assert_url annotation on field %s.%s must be an enum value", msg.Name, fieldName)
				}
				writeTsURLValidation(b, fieldName, annotationValue.EnumValue, field.IsRepeated)

			case "assert_net_port":
				// Generate port validation
				fmt.Fprintf(b, `	// Validate %s (assert_net_port)
	if (m.%s !== undefined && (m.%s < 0 || m.%s > 65535)) {
		throw newErrMissingInvalidData(undefined, `+"`field %s must be a valid port number (0-65535), got: ${m.%s}`"+`);
	}

`, fieldName, fieldName, fieldName, fieldName, fieldName, fieldName)

			case "assert_fqdn":
				// Generate FQDN validation
				fmt.Fprintf(b, `	// Validate %s (assert_fqdn)
	if (m.%s) {
		const fqdnRegex = /^([a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$/;
		if (!fqdnRegex.test(m.%s)) {
			throw newErrMissingInvalidData(undefined, `+"`field %s must be a valid FQDN`"+`);
		}
	}

`, fieldName, fieldName, fieldName, fieldName)

			case "assert_host":
				// Generate host validation (IP address or FQDN)
				fmt.Fprintf(b, `	// Validate %s (assert_host: IP address or FQDN)
	if (m.%s) {
		const ipv4Regex = /^((25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)\.){3}(25[0-5]|2[0-4][0-9]|[01]?[0-9][0-9]?)$/;
		const ipv6Regex = /^([0-9a-fA-F]{1,4}:){7}[0-9a-fA-F]{1,4}$|^::$|^([0-9a-fA-F]{1,4}:){1,7}:$|^:(:([0-9a-fA-F]{1,4})){1,7}$/;
		const fqdnRegex = /^([a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$/;
		if (!ipv4Regex.test(m.%s) && !ipv6Regex.test(m.%s) && !fqdnRegex.test(m.%s)) {
			throw newErrMissingInvalidData(undefined, `+"`field %s must be a valid IP address or FQDN`"+`);
		}
	}

`, fieldName, fieldName, fieldName, fieldName, fieldName, fieldName)

			case "assert_base64":
				// Generate base64 validation
				fmt.Fprintf(b, `	// Validate %s (assert_base64)
	if (m.%s) {
		const base64Regex = /^[A-Za-z0-9+/]*={0,2}$/;
		if (!base64Regex.test(m.%s)) {
			throw newErrMissingInvalidData(undefined, `+"`field %s must be valid base64`"+`);
		}
	}

`, fieldName, fieldName, fieldName, fieldName)

			case "assert_listen_addr":
				if field.Type != "string" {
					return fmt.Errorf("assert_listen_addr annotation on field %s.%s requires string type", msg.Name, fieldName)
				}
				if field.IsRepeated {
					fmt.Fprintf(b, `	// Validate %s (assert_listen_addr)
	if (m.%s) {
		const listenAddrRegex = /^[^:]+:\d+$/;
		for (let i = 0; i < m.%s.length; i++) {
			const val = m.%s[i];
			if (val && !listenAddrRegex.test(val)) {
				throw newErrMissingInvalidData(undefined, `+"`field %s[${i}] must be in host:port format`"+`);
			}
		}
	}

`, fieldName, fieldName, fieldName, fieldName, fieldName)
				} else {
					// Generate listen address validation (host:port format)
					fmt.Fprintf(b, `	// Validate %s (assert_listen_addr)
	if (m.%s) {
		const listenAddrRegex = /^[^:]+:\d+$/;
		if (!listenAddrRegex.test(m.%s)) {
			throw newErrMissingInvalidData(undefined, `+"`field %s must be in host:port format`"+`);
		}
	}

`, fieldName, fieldName, fieldName, fieldName)
				}

			case "assert_file_path":
				// Generate file path validation
				if annotationValue.Type != model.AnnotationTypeEnum {
					return fmt.Errorf("assert_file_path annotation on field %s.%s must be an enum value", msg.Name, fieldName)
				}
				writeTsFilePathValidation(b, fieldName, annotationValue.EnumValue, field.IsRepeated)

			case "assert_semver":
				// Generate semantic version validation
				fmt.Fprintf(b, `	// Validate %s (assert_semver)
	if (m.%s) {
		const semverRegex = /^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$/;
		if (!semverRegex.test(m.%s)) {
			throw newErrMissingInvalidData(undefined, `+"`field %s must be a valid semantic version (e.g., '1.0.0' or 'v2.3.4')`"+`);
		}
	}

`, fieldName, fieldName, fieldName, fieldName)

			case "assert_enc_type":
				if field.Type != "string" {
					return fmt.Errorf("assert_enc_type annotation on field %s.%s requires string type", msg.Name, fieldName)
				}
				if annotationValue.Type != model.AnnotationTypeEnum {
					return fmt.Errorf("assert_enc_type annotation on field %s.%s must be an enum value", msg.Name, fieldName)
				}
				writeTsEncTypeValidation(b, fieldName, annotationValue.EnumValue, field.IsRepeated)

			case "epoch_expiration":
				if isBigIntProtoType(field.Type) {
					fmt.Fprintf(b, `	// Validate %s (epoch_expiration)
	if (m.%s !== 0n) {
		const now = BigInt(Math.floor(Date.now() / 1000));
		if (m.%s <= now) {
			throw newErrMissingInvalidData(undefined, `+"`field %s must be a future epoch timestamp, got: ${m.%s} (now: ${now})`"+`);
		}
		if (m.%s > now + 31536000n) {
			throw newErrMissingInvalidData(undefined, `+"`field %s must be within 1 year from now, got: ${m.%s} (max: ${now + 31536000n})`"+`);
		}
	}

`, fieldName, fieldName, fieldName, fieldName, fieldName, fieldName, fieldName, fieldName)
				} else {
					fmt.Fprintf(b, `	// Validate %s (epoch_expiration)
	if (m.%s !== 0) {
		const now = Math.floor(Date.now() / 1000);
		if (m.%s <= now) {
			throw newErrMissingInvalidData(undefined, `+"`field %s must be a future epoch timestamp, got: ${m.%s} (now: ${now})`"+`);
		}
		if (m.%s > now + 31536000) {
			throw newErrMissingInvalidData(undefined, `+"`field %s must be within 1 year from now, got: ${m.%s} (max: ${now + 31536000})`"+`);
		}
	}

`, fieldName, fieldName, fieldName, fieldName, fieldName, fieldName, fieldName, fieldName)
				}

			case "assert_sql":
				// Generate SQL validation
				if annotationValue.Type != model.AnnotationTypeEnum {
					return fmt.Errorf("assert_sql annotation on field %s.%s must be an enum value", msg.Name, fieldName)
				}
				sqlPermit := annotationValue.EnumValue
				// Build allowed prefixes based on permit type
				var prefixCheck string
				switch sqlPermit {
				case "SQL_PERMIT_SELECT":
					prefixCheck = `!norm.startsWith('SELECT') && !norm.startsWith('WITH')`
				case "SQL_PERMIT_DML":
					prefixCheck = `!norm.startsWith('SELECT') && !norm.startsWith('INSERT') && !norm.startsWith('UPDATE') && !norm.startsWith('DELETE') && !norm.startsWith('WITH')`
				case "SQL_PERMIT_DDL":
					prefixCheck = `!norm.startsWith('CREATE') && !norm.startsWith('ALTER') && !norm.startsWith('DROP') && !norm.startsWith('TRUNCATE')`
				case "SQL_PERMIT_ANY":
					prefixCheck = "" // no keyword check
				}

				if field.IsRepeated {
					fmt.Fprintf(b, `	// Validate %s (assert_sql: %s)
	if (m.%s) {
		for (const val of m.%s) {
			if (val) {
				if (val.includes(';')) { throw newErrMissingInvalidData(undefined, `+"`field %s: SQL contains semicolon (possible injection)`"+`); }
				if (val.includes('--')) { throw newErrMissingInvalidData(undefined, `+"`field %s: SQL contains line comment (possible injection)`"+`); }
				if (val.includes('/*')) { throw newErrMissingInvalidData(undefined, `+"`field %s: SQL contains block comment (possible injection)`"+`); }
				if (val.includes('\\')) { throw newErrMissingInvalidData(undefined, `+"`field %s: SQL contains escape character (possible injection)`"+`); }
`, fieldName, sqlPermit, fieldName, fieldName, fieldName, fieldName, fieldName, fieldName)
					if prefixCheck != "" {
						fmt.Fprintf(b, `				const norm = val.trim().toUpperCase();
				if (%s) { throw newErrMissingInvalidData(undefined, `+"`field %s: SQL statement type not permitted for %s`"+`); }
`, prefixCheck, fieldName, sqlPermit)
					}
					fmt.Fprintf(b, `			}
		}
	}

`)
				} else {
					fmt.Fprintf(b, `	// Validate %s (assert_sql: %s)
	if (m.%s) {
		if (m.%s.includes(';')) { throw newErrMissingInvalidData(undefined, `+"`field %s: SQL contains semicolon (possible injection)`"+`); }
		if (m.%s.includes('--')) { throw newErrMissingInvalidData(undefined, `+"`field %s: SQL contains line comment (possible injection)`"+`); }
		if (m.%s.includes('/*')) { throw newErrMissingInvalidData(undefined, `+"`field %s: SQL contains block comment (possible injection)`"+`); }
		if (m.%s.includes('\\')) { throw newErrMissingInvalidData(undefined, `+"`field %s: SQL contains escape character (possible injection)`"+`); }
`, fieldName, sqlPermit, fieldName, fieldName, fieldName, fieldName, fieldName, fieldName, fieldName, fieldName, fieldName)
					if prefixCheck != "" {
						fmt.Fprintf(b, `		const norm = m.%s.trim().toUpperCase();
		if (%s) { throw newErrMissingInvalidData(undefined, `+"`field %s: SQL statement type not permitted for %s`"+`); }
`, fieldName, prefixCheck, fieldName, sqlPermit)
					}
					fmt.Fprintf(b, `	}

`)
				}

			case "assert_enum_defined":
				if err := writeTsEnumDefinedValidation(b, msg, field, fieldName); err != nil {
					return err
				}

			case "assert_numeric":
				if err := writeTsNumericConstraintValidation(b, msg, field, fieldName, annotationValue.NumericConstraint); err != nil {
					return err
				}

			case "assert_float":
				if err := writeTsFloatConstraintValidation(b, msg, field, fieldName, annotationValue.FloatConstraint); err != nil {
					return err
				}

			case "required_if_true":
				condFieldName := upperSnakeCaseToCamelCase(annotationValue.StringValue)
				fmt.Fprintf(b, `	// Validate %s (required_if_true: %s)
	if (m.%s && !m.%s) {
		throw newErrMissingInvalidData(undefined, `+"`field %s is required when %s is true`"+`);
	}

`, fieldName, annotationValue.StringValue, condFieldName, fieldName, fieldName, condFieldName)

			case "required_if_false":
				condFieldName := upperSnakeCaseToCamelCase(annotationValue.StringValue)
				fmt.Fprintf(b, `	// Validate %s (required_if_false: %s)
	if (!m.%s && !m.%s) {
		throw newErrMissingInvalidData(undefined, `+"`field %s is required when %s is false`"+`);
	}

`, fieldName, annotationValue.StringValue, condFieldName, fieldName, fieldName, condFieldName)

			case "required_if_eq":
				parts := strings.SplitN(annotationValue.StringValue, "=", 2)
				if len(parts) == 2 {
					condFieldSnake := parts[0]
					condValue := parts[1]
					condFieldTS := upperSnakeCaseToCamelCase(condFieldSnake)

					// Find the sibling field to determine if it's an enum
					var siblingField *model.Field
					for i := range msg.Fields {
						if msg.Fields[i].Name == condFieldSnake {
							siblingField = &msg.Fields[i]
							break
						}
					}

					if siblingField != nil && siblingField.IsEnum {
						enumType := siblingField.Type
						fmt.Fprintf(b, `	// Validate %s (required_if_eq: %s=%s)
	if (m.%s === %s.%s && !m.%s) {
		throw newErrMissingInvalidData(undefined, `+"`field %s is required when %s is %s`"+`);
	}

`, fieldName, condFieldSnake, condValue, condFieldTS, enumType, condValue, fieldName, fieldName, condFieldSnake, condValue)
					} else {
						fmt.Fprintf(b, `	// Validate %s (required_if_eq: %s=%s)
	if (m.%s === "%s" && !m.%s) {
		throw newErrMissingInvalidData(undefined, `+"`field %s is required when %s is %s`"+`);
	}

`, fieldName, condFieldSnake, condValue, condFieldTS, condValue, fieldName, fieldName, condFieldSnake, condValue)
					}
				}

			case "assert_max_size":
				// Generate maximum size validation for string/bytes/repeated/map fields
				if annotationValue.Type != model.AnnotationTypeInt {
					return fmt.Errorf("assert_max_size annotation on field %s.%s must be an integer value", msg.Name, fieldName)
				}

				if field.IsMap {
					fmt.Fprintf(b, `	// Validate %s (assert_max_size: %d)
	if (m.%s && Object.keys(m.%s).length > %d) {
		throw newErrMissingInvalidData(undefined, `+"`field %s exceeds max size of %d, got: ${Object.keys(m.%s).length}`"+`);
	}

`, fieldName, annotationValue.IntValue, fieldName, fieldName, annotationValue.IntValue, fieldName, annotationValue.IntValue, fieldName)
				} else if field.IsRepeated {
					fmt.Fprintf(b, `	// Validate %s (assert_max_size: %d)
	if (m.%s && m.%s.length > %d) {
		throw newErrMissingInvalidData(undefined, `+"`field %s exceeds max size of %d, got: ${m.%s.length}`"+`);
	}

`, fieldName, annotationValue.IntValue, fieldName, fieldName, annotationValue.IntValue, fieldName, annotationValue.IntValue, fieldName)
				} else if field.Type == "bytes" {
					fmt.Fprintf(b, `	// Validate %s (assert_max_size: %d bytes)
	if (m.%s && m.%s.length > %d) {
		throw newErrMissingInvalidData(undefined, `+"`field %s exceeds max size of %d bytes, got: ${m.%s.length}`"+`);
	}

`, fieldName, annotationValue.IntValue, fieldName, fieldName, annotationValue.IntValue, fieldName, annotationValue.IntValue, fieldName)
				} else {
					fmt.Fprintf(b, `	// Validate %s (assert_max_size: %d bytes)
	if (m.%s && new TextEncoder().encode(m.%s).length > %d) {
		throw newErrMissingInvalidData(undefined, `+"`field %s exceeds max size of %d bytes, got: ${new TextEncoder().encode(m.%s).length}`"+`);
	}

`, fieldName, annotationValue.IntValue, fieldName, fieldName, annotationValue.IntValue, fieldName, annotationValue.IntValue, fieldName)
				}

			case "assert_min_size":
				// Generate minimum size validation for string/bytes/repeated/map fields
				if annotationValue.Type != model.AnnotationTypeInt {
					return fmt.Errorf("assert_min_size annotation on field %s.%s must be an integer value", msg.Name, fieldName)
				}

				if field.IsMap {
					fmt.Fprintf(b, `	// Validate %s (assert_min_size: %d)
	if (!m.%s || Object.keys(m.%s).length < %d) {
		throw newErrMissingInvalidData(undefined, `+"`field %s below min size of %d, got: ${m.%s ? Object.keys(m.%s).length : 0}`"+`);
	}

`, fieldName, annotationValue.IntValue, fieldName, fieldName, annotationValue.IntValue, fieldName, annotationValue.IntValue, fieldName, fieldName)
				} else if field.IsRepeated {
					fmt.Fprintf(b, `	// Validate %s (assert_min_size: %d)
	if (!m.%s || m.%s.length < %d) {
		throw newErrMissingInvalidData(undefined, `+"`field %s below min size of %d, got: ${m.%s ? m.%s.length : 0}`"+`);
	}

`, fieldName, annotationValue.IntValue, fieldName, fieldName, annotationValue.IntValue, fieldName, annotationValue.IntValue, fieldName, fieldName)
				} else if field.Type == "bytes" {
					fmt.Fprintf(b, `	// Validate %s (assert_min_size: %d bytes)
	if (m.%s && m.%s.length < %d) {
		throw newErrMissingInvalidData(undefined, `+"`field %s below min size of %d bytes, got: ${m.%s.length}`"+`);
	}

`, fieldName, annotationValue.IntValue, fieldName, fieldName, annotationValue.IntValue, fieldName, annotationValue.IntValue, fieldName)
				} else {
					fmt.Fprintf(b, `	// Validate %s (assert_min_size: %d bytes)
	if (m.%s && new TextEncoder().encode(m.%s).length < %d) {
		throw newErrMissingInvalidData(undefined, `+"`field %s below min size of %d bytes, got: ${new TextEncoder().encode(m.%s).length}`"+`);
	}

`, fieldName, annotationValue.IntValue, fieldName, fieldName, annotationValue.IntValue, fieldName, annotationValue.IntValue, fieldName)
				}

			case "assert_invariant":
				if annotationValue.Type == model.AnnotationTypeString && annotationValue.StringValue != "" {
					fmt.Fprintf(b, "	// Formal invariant for %s (assert_invariant): %s\n", fieldName, annotationValue.StringValue)
				}

			case "default_int", "default_string", "default_float", "default_bool", "default_const",
				"default_host",
				"persistence", "indexed", "index_type",
				"view_member", "view_sources", "instance_keyed",
				"queue", "priority_queue", "topic", "leader", "command",
				"sync_backup_count", "async_backup_count",
				"eviction_policy", "eviction_max_size", "eviction_max_size_policy",
				"eviction_ttl_seconds", "eviction_max_idle_seconds",
				"private", "state_import", "refresh_on_miss",
				"boundary_callers",
				"reads_state", "key", "param_key", "json_return_value",
				"store_key", "quorum_write", "collected_health",
				"graph_index", "projection_source", "projection_record_key", "graph_edge",
				"query_field", "redact",
				"graph_mode", "graph_field_index", "graph_upsert",
				"timeseries_column", "timeseries_order_key",
				"timeseries_partition_key", "timeseries_cardinality_hint",
				"json_key", "json_aliases",
				"cache", "timeseries", "archive",
				"record", "record_key", "record_owner",
				"graph_reverse", "graph_xid_field", "subscribe",
				"identity_namespace", "identity_subject",
				"identity_spiffe_subject", "identity_spiffe_resource":
				// Non-validation annotations - handled by other generators or informational only.

			default:
				// An unknown VALIDATION annotation is a hard error, matching the
				// go/java/python/rust emitters (OF-26d): the generator has no
				// template for it, so the declared constraint would be dropped and
				// the TypeScript client would accept requests every other target
				// rejects. A stderr warning is not enough — nothing fails on it.
				if strings.HasPrefix(annotationName, "assert_") || strings.HasPrefix(annotationName, "required_if_") {
					return fmt.Errorf("unsupported validation annotation %q on field %s.%s (no TypeScript validation template)", annotationName, msg.Name, fieldName)
				}
				// Truly unknown non-validation annotation - warn so new annotations get noticed.
				fmt.Fprintf(os.Stderr, "WARNING: skipping unknown annotation '%s' on field %s.%s\n",
					annotationName, msg.Name, fieldName)
			}
		}
	}

	// Default max size enforcement for repeated/map fields without explicit assert_max_size.
	for _, field := range msg.Fields {
		if field.IsOneOf {
			continue
		}
		if !field.IsRepeated && !field.IsMap {
			continue
		}
		if _, hasMaxSize := field.Annotations["assert_max_size"]; hasMaxSize {
			continue
		}

		fieldName := upperSnakeCaseToCamelCase(field.Name)
		defaultCount := collection.DefaultCollCount

		hasValidations = true
		if field.IsMap {
			fmt.Fprintf(b, `	// Validate %s (default max size: %d elements)
	if (m.%s && Object.keys(m.%s).length > %d) {
		throw newErrMissingInvalidData(undefined, `+"`field %s exceeds max size of %d elements, got: ${Object.keys(m.%s).length}`"+`);
	}

`, fieldName, defaultCount, fieldName, fieldName, defaultCount, fieldName, defaultCount, fieldName)
		} else {
			fmt.Fprintf(b, `	// Validate %s (default max size: %d elements)
	if (m.%s && m.%s.length > %d) {
		throw newErrMissingInvalidData(undefined, `+"`field %s exceeds max size of %d elements, got: ${m.%s.length}`"+`);
	}

`, fieldName, defaultCount, fieldName, fieldName, defaultCount, fieldName, defaultCount, fieldName)
		}
	}

	// Default per-element max size enforcement for repeated/map string/bytes values.
	for _, field := range msg.Fields {
		if field.IsOneOf {
			continue
		}

		fieldName := upperSnakeCaseToCamelCase(field.Name)
		defaultSize := collection.DefaultScalarBytes

		if field.IsRepeated && field.Type == "string" {
			hasValidations = true
			fmt.Fprintf(b, `	// Validate %s elements (default max size: %d bytes)
	if (m.%s) {
		for (let i = 0; i < m.%s.length; i++) {
			const val = m.%s[i];
			if (new TextEncoder().encode(val).length > %d) {
				throw newErrMissingInvalidData(undefined, `+"`field %s[${i}] exceeds max size of %d bytes, got: ${new TextEncoder().encode(val).length}`"+`);
			}
		}
	}

`, fieldName, defaultSize, fieldName, fieldName, fieldName, defaultSize, fieldName, defaultSize)
		} else if field.IsRepeated && field.Type == "bytes" {
			hasValidations = true
			fmt.Fprintf(b, `	// Validate %s elements (default max size: %d bytes)
	if (m.%s) {
		for (let i = 0; i < m.%s.length; i++) {
			const val = m.%s[i];
			if (val.length > %d) {
				throw newErrMissingInvalidData(undefined, `+"`field %s[${i}] exceeds max size of %d bytes, got: ${val.length}`"+`);
			}
		}
	}

`, fieldName, defaultSize, fieldName, fieldName, fieldName, defaultSize, fieldName, defaultSize)
		}

		if field.IsMap {
			if field.MapKeyType == "string" {
				hasValidations = true
				fmt.Fprintf(b, `	// Validate %s keys (default max size: %d bytes)
	if (m.%s) {
		for (const key of Object.keys(m.%s)) {
			if (new TextEncoder().encode(key).length > %d) {
				throw newErrMissingInvalidData(undefined, `+"`field %s key exceeds max size of %d bytes, got: ${new TextEncoder().encode(key).length}`"+`);
			}
		}
	}

`, fieldName, defaultSize, fieldName, fieldName, defaultSize, fieldName, defaultSize)
			}
			if field.MapValueType == "string" {
				hasValidations = true
				fmt.Fprintf(b, `	// Validate %s values (default max size: %d bytes)
	if (m.%s) {
		for (const val of Object.values(m.%s)) {
			if (new TextEncoder().encode(val).length > %d) {
				throw newErrMissingInvalidData(undefined, `+"`field %s value exceeds max size of %d bytes, got: ${new TextEncoder().encode(val).length}`"+`);
			}
		}
	}

`, fieldName, defaultSize, fieldName, fieldName, defaultSize, fieldName, defaultSize)
			} else if field.MapValueType == "bytes" {
				hasValidations = true
				fmt.Fprintf(b, `	// Validate %s values (default max size: %d bytes)
	if (m.%s) {
		for (const val of Object.values(m.%s)) {
			if (val.length > %d) {
				throw newErrMissingInvalidData(undefined, `+"`field %s value exceeds max size of %d bytes, got: ${val.length}`"+`);
			}
		}
	}

`, fieldName, defaultSize, fieldName, fieldName, defaultSize, fieldName, defaultSize)
			}
		}
	}

	// Default max size enforcement for singular string/bytes fields without explicit assert_max_size.
	for _, field := range msg.Fields {
		if field.Type != "string" && field.Type != "bytes" {
			continue
		}
		if field.IsRepeated || field.IsMap {
			continue
		}
		if _, hasMaxSize := field.Annotations["assert_max_size"]; hasMaxSize {
			continue
		}

		fieldName := upperSnakeCaseToCamelCase(field.Name)
		defaultSize := collection.DefaultScalarBytes

		hasValidations = true
		if field.IsOneOf {
			writeTypeScriptOneofScalarMaxSize(b, field, fieldName, defaultSize, "default max size")
		} else if field.Type == "bytes" {
			fmt.Fprintf(b, `	// Validate %s (default max size: %d bytes)
	if (m.%s && m.%s.length > %d) {
		throw newErrMissingInvalidData(undefined, `+"`field %s exceeds max size of %d bytes, got: ${m.%s.length}`"+`);
	}

`, fieldName, defaultSize, fieldName, fieldName, defaultSize, fieldName, defaultSize, fieldName)
		} else {
			fmt.Fprintf(b, `	// Validate %s (default max size: %d bytes)
	if (m.%s && new TextEncoder().encode(m.%s).length > %d) {
		throw newErrMissingInvalidData(undefined, `+"`field %s exceeds max size of %d bytes, got: ${new TextEncoder().encode(m.%s).length}`"+`);
	}

`, fieldName, defaultSize, fieldName, fieldName, defaultSize, fieldName, defaultSize, fieldName)
		}
	}

	// Validate enum fields against the generated enum vocabulary. This rejects
	// undefined numeric enum values while still allowing the declared zero value
	// unless assert_enum_defined is also present.
	for _, field := range msg.Fields {
		fieldName := upperSnakeCaseToCamelCase(field.Name)

		if field.MapValueIsEnum && field.MapValueTypePackage == "" {
			hasValidations = true
			fmt.Fprintf(b, `	// Validate %s enum values are known
	if (m.%s) {
		for (const [key, val] of Object.entries(m.%s)) {
			if (!Object.values(%s).includes(Number(val) as any)) {
				throw newErrMissingInvalidData(undefined, `+"`field %s[${key}] has unknown enum value ${Number(val)}`"+`);
			}
		}
	}

`, fieldName, fieldName, fieldName, field.MapValueType, fieldName)
			continue
		}

		if !field.IsEnum || field.TypePackage != "" {
			continue
		}

		hasValidations = true
		if field.IsOneOf {
			oneofName := snakeToCamelCase(field.OneOfName)
			caseName := snakeToCamelCase(field.Name)
			fmt.Fprintf(b, `	// Validate %s enum value is known
	if (m.%s.case === '%s') {
		const val = m.%s.value;
		if (!Object.values(%s).includes(Number(val) as any)) {
			throw newErrMissingInvalidData(undefined, `+"`field %s has unknown enum value ${Number(val)}`"+`);
		}
	}

`, fieldName, oneofName, caseName, oneofName, field.Type, fieldName)
		} else if field.IsRepeated {
			fmt.Fprintf(b, `	// Validate %s enum values are known
	if (m.%s) {
		for (let i = 0; i < m.%s.length; i++) {
			const val = m.%s[i];
			if (!Object.values(%s).includes(Number(val) as any)) {
				throw newErrMissingInvalidData(undefined, `+"`field %s[${i}] has unknown enum value ${Number(val)}`"+`);
			}
		}
	}

`, fieldName, fieldName, fieldName, fieldName, field.Type, fieldName)
		} else if field.IsOptional {
			fmt.Fprintf(b, `	// Validate %s enum value is known
	if (m.%s !== undefined && !Object.values(%s).includes(Number(m.%s) as any)) {
		throw newErrMissingInvalidData(undefined, `+"`field %s has unknown enum value ${Number(m.%s)}`"+`);
	}

`, fieldName, fieldName, field.Type, fieldName, fieldName, fieldName)
		} else {
			fmt.Fprintf(b, `	// Validate %s enum value is known
	if (!Object.values(%s).includes(Number(m.%s) as any)) {
		throw newErrMissingInvalidData(undefined, `+"`field %s has unknown enum value ${Number(m.%s)}`"+`);
	}

`, fieldName, field.Type, fieldName, fieldName, fieldName)
		}
	}

	// Recursive validation for nested generated messages, including oneof variants.
	for _, field := range msg.Fields {
		fieldName := upperSnakeCaseToCamelCase(field.Name)

		if field.MapValueIsMessage {
			if !typeScriptMessageHasValidate(field.MapValueType, field.MapValueTypePackage) {
				continue
			}
			validateFunc := "validate" + field.MapValueType
			hasValidations = true
			fmt.Fprintf(b, `	// Validate %s message values
	if (m.%s) {
		for (const [key, val] of Object.entries(m.%s)) {
			try {
				%s(val, depth + 1);
			} catch (err) {
				throw newErrMissingInvalidData(err instanceof Error ? err : undefined, `+"`field %s[${key}] validation failed`"+`);
			}
		}
	}

`, fieldName, fieldName, fieldName, validateFunc, fieldName)
			continue
		}

		if !field.IsMessage || !typeScriptMessageHasValidate(field.Type, field.TypePackage) {
			continue
		}

		validateFunc := "validate" + field.Type
		hasValidations = true
		if field.IsOneOf {
			oneofName := snakeToCamelCase(field.OneOfName)
			caseName := snakeToCamelCase(field.Name)
			fmt.Fprintf(b, `	// Validate %s nested message
	if (m.%s.case === '%s') {
		try {
			%s(m.%s.value, depth + 1);
		} catch (err) {
			throw newErrMissingInvalidData(err instanceof Error ? err : undefined, `+"`field %s validation failed`"+`);
		}
	}

`, fieldName, oneofName, caseName, validateFunc, oneofName, fieldName)
		} else if field.IsRepeated {
			fmt.Fprintf(b, `	// Validate %s nested messages
	if (m.%s) {
		for (let i = 0; i < m.%s.length; i++) {
			try {
				%s(m.%s[i], depth + 1);
			} catch (err) {
				throw newErrMissingInvalidData(err instanceof Error ? err : undefined, `+"`field %s[${i}] validation failed`"+`);
			}
		}
	}

`, fieldName, fieldName, fieldName, validateFunc, fieldName, fieldName)
		} else {
			fmt.Fprintf(b, `	// Validate %s nested message
	if (m.%s) {
		try {
			%s(m.%s, depth + 1);
		} catch (err) {
			throw newErrMissingInvalidData(err instanceof Error ? err : undefined, `+"`field %s validation failed`"+`);
		}
	}

`, fieldName, fieldName, validateFunc, fieldName, fieldName)
		}
	}

	if !hasValidations {
		b.WriteString("// No field validations defined\n")
	}

	b.WriteString("}\n")
	return nil
}

func typeScriptMessageHasValidate(typeName, typePackage string) bool {
	return typeName != "" && typePackage == ""
}

func writeTypeScriptOneofScalarMaxSize(b *strings.Builder, field model.Field, fieldName string, maxSize int64, label string) {
	oneofName := snakeToCamelCase(field.OneOfName)
	caseName := snakeToCamelCase(field.Name)
	fmt.Fprintf(b, `	// Validate %s (%s: %d bytes)
`, fieldName, label, maxSize)
	if field.Type == "bytes" {
		fmt.Fprintf(b, `	if (m.%s.case === '%s' && m.%s.value.length > %d) {
		throw newErrMissingInvalidData(undefined, `+"`field %s exceeds max size of %d bytes, got: ${m.%s.value.length}`"+`);
	}

`, oneofName, caseName, oneofName, maxSize, fieldName, maxSize, oneofName)
		return
	}
	fmt.Fprintf(b, `	if (m.%s.case === '%s' && new TextEncoder().encode(m.%s.value).length > %d) {
		throw newErrMissingInvalidData(undefined, `+"`field %s exceeds max size of %d bytes, got: ${new TextEncoder().encode(m.%s.value).length}`"+`);
	}

`, oneofName, caseName, oneofName, maxSize, fieldName, maxSize, oneofName)
}

func writeTsURLValidation(b *strings.Builder, fieldName, scheme string, isRepeated bool) {
	fmt.Fprintf(b, `	// Validate %s (assert_url: %s)
	{
`, fieldName, scheme)
	if isRepeated {
		fmt.Fprintf(b, `		for (let i = 0; i < m.%s.length; i++) {
			const val = m.%s[i];
			if (val) {
				let parsed: URL;
				try {
					parsed = new URL(val);
				} catch (e) {
					throw newErrMissingInvalidData(undefined, `+"`field %s[${i}] must be a valid URL`"+`);
				}
				if (!parsed.hostname) {
					throw newErrMissingInvalidData(undefined, `+"`field %s[${i}] URL must include a host`"+`);
				}
`, fieldName, fieldName, fieldName, fieldName)
		writeTsURLSchemeCheck(b, "parsed", fieldName, scheme, true)
		b.WriteString(`			}
		}
`)
	} else {
		fmt.Fprintf(b, `		if (m.%s) {
			let parsed: URL;
			try {
				parsed = new URL(m.%s);
			} catch (e) {
				throw newErrMissingInvalidData(undefined, `+"`field %s must be a valid URL`"+`);
			}
			if (!parsed.hostname) {
				throw newErrMissingInvalidData(undefined, `+"`field %s URL must include a host`"+`);
			}
`, fieldName, fieldName, fieldName, fieldName)
		writeTsURLSchemeCheck(b, "parsed", fieldName, scheme, false)
		b.WriteString(`		}
`)
	}
	b.WriteString("	}\n\n")
}

func writeTsURLSchemeCheck(b *strings.Builder, urlVar, fieldName, scheme string, repeated bool) {
	var protocol, message string
	switch scheme {
	case "URL_SCHEME_HTTP":
		protocol = "http:"
		message = "must use http:// scheme"
	case "URL_SCHEME_HTTPS":
		protocol = "https:"
		message = "must use https:// scheme"
	case "URL_SCHEME_HTTP_OR_HTTPS":
		if repeated {
			fmt.Fprintf(b, "				if (%s.protocol !== 'http:' && %s.protocol !== 'https:') {\n", urlVar, urlVar)
			fmt.Fprintf(b, "					throw newErrMissingInvalidData(undefined, `field %s[${i}] must use http:// or https:// scheme`);\n", fieldName)
			b.WriteString("				}\n")
			return
		}
		fmt.Fprintf(b, "			if (%s.protocol !== 'http:' && %s.protocol !== 'https:') {\n", urlVar, urlVar)
		fmt.Fprintf(b, "				throw newErrMissingInvalidData(undefined, `field %s must use http:// or https:// scheme`);\n", fieldName)
		b.WriteString("			}\n")
		return
	default:
		return
	}

	if repeated {
		fmt.Fprintf(b, "				if (%s.protocol !== '%s') {\n", urlVar, protocol)
		fmt.Fprintf(b, "					throw newErrMissingInvalidData(undefined, `field %s[${i}] %s`);\n", fieldName, message)
		b.WriteString("				}\n")
		return
	}
	fmt.Fprintf(b, "			if (%s.protocol !== '%s') {\n", urlVar, protocol)
	fmt.Fprintf(b, "				throw newErrMissingInvalidData(undefined, `field %s %s`);\n", fieldName, message)
	b.WriteString("			}\n")
}

func writeTsFilePathValidation(b *strings.Builder, fieldName, pathType string, isRepeated bool) {
	fmt.Fprintf(b, `	// Validate %s (assert_file_path: %s)
	{
		const isAbsolutePath = (path: string): boolean => path.startsWith('/') || /^[A-Za-z]:[\\/]/.test(path);
`, fieldName, pathType)
	if isRepeated {
		fmt.Fprintf(b, `		for (let i = 0; i < m.%s.length; i++) {
			const path = m.%s[i];
			if (path) {
`, fieldName, fieldName)
		writeTsFilePathCheck(b, fieldName, pathType, true)
		b.WriteString(`			}
		}
	}

`)
		return
	}

	fmt.Fprintf(b, `		if (m.%s) {
			const path = m.%s;
`, fieldName, fieldName)
	writeTsFilePathCheck(b, fieldName, pathType, false)
	b.WriteString(`		}
	}

`)
}

func writeTsFilePathCheck(b *strings.Builder, fieldName, pathType string, repeated bool) {
	indent := "\t\t\t"
	if repeated {
		indent = "\t\t\t\t"
	}
	switch pathType {
	case "PATH_ABSOLUTE":
		fmt.Fprintf(b, "%sif (!isAbsolutePath(path)) {\n", indent)
		if repeated {
			fmt.Fprintf(b, "%sthrow newErrMissingInvalidData(undefined, `field %s[${i}] must be an absolute path`);\n", indent+"\t", fieldName)
		} else {
			fmt.Fprintf(b, "%sthrow newErrMissingInvalidData(undefined, `field %s must be an absolute path`);\n", indent+"\t", fieldName)
		}
		fmt.Fprintf(b, "%s}\n", indent)
	case "PATH_RELATIVE":
		fmt.Fprintf(b, "%sif (isAbsolutePath(path)) {\n", indent)
		if repeated {
			fmt.Fprintf(b, "%sthrow newErrMissingInvalidData(undefined, `field %s[${i}] must be a relative path`);\n", indent+"\t", fieldName)
		} else {
			fmt.Fprintf(b, "%sthrow newErrMissingInvalidData(undefined, `field %s must be a relative path`);\n", indent+"\t", fieldName)
		}
		fmt.Fprintf(b, "%s}\n", indent)
	case "PATH_ANY", "PATH_DIR":
		fmt.Fprintf(b, "%sif (path.trim() === '') {\n", indent)
		if repeated {
			fmt.Fprintf(b, "%sthrow newErrMissingInvalidData(undefined, `field %s[${i}] cannot be empty`);\n", indent+"\t", fieldName)
		} else {
			fmt.Fprintf(b, "%sthrow newErrMissingInvalidData(undefined, `field %s cannot be empty`);\n", indent+"\t", fieldName)
		}
		fmt.Fprintf(b, "%s}\n", indent)
	}
}

func writeTsEncTypeValidation(b *strings.Builder, fieldName, encType string, isRepeated bool) {
	fmt.Fprintf(b, `	// Validate %s (assert_enc_type: %s)
	{
		const validatePem = (pem: string, label: string): void => {
			if (!pem.includes('-----BEGIN') || !pem.includes('-----END')) {
				throw newErrMissingInvalidData(undefined, `+"`field ${label} must be in PEM format`"+`);
			}
			const isCertificate = pem.includes('-----BEGIN CERTIFICATE-----');
			const isPrivate = pem.includes('PRIVATE KEY-----');
			const isPublic = pem.includes('PUBLIC KEY-----');
`, fieldName, encType)
	writeTsEncTypeCheck(b, encType)
	if isRepeated {
		fmt.Fprintf(b, `		};
		for (let i = 0; i < m.%s.length; i++) {
			const val = m.%s[i];
			if (val) {
				validatePem(val, `+"`%s[${i}]`"+`);
			}
		}
	}

`, fieldName, fieldName, fieldName)
		return
	}

	fmt.Fprintf(b, `		};
		if (m.%s) {
			validatePem(m.%s, '%s');
		}
	}

`, fieldName, fieldName, fieldName)
}

func writeTsEncTypeCheck(b *strings.Builder, encType string) {
	switch {
	case strings.HasPrefix(encType, "ENC_TYPE_X509_CERT"):
		b.WriteString(`			if (!isCertificate) {
				throw newErrMissingInvalidData(undefined, ` + "`field ${label} must be an X.509 certificate PEM`" + `);
			}
`)
	case strings.HasPrefix(encType, "ENC_TYPE_KEY_PRIVATE"):
		b.WriteString(`			if (!isPrivate) {
				throw newErrMissingInvalidData(undefined, ` + "`field ${label} must be a private key PEM`" + `);
			}
`)
	case strings.HasPrefix(encType, "ENC_TYPE_KEY_PUBLIC"):
		b.WriteString(`			if (!isPublic) {
				throw newErrMissingInvalidData(undefined, ` + "`field ${label} must be a public key PEM`" + `);
			}
`)
	case strings.HasPrefix(encType, "ENC_TYPE_KEY_ANY"):
		b.WriteString(`			if (!isPrivate && !isPublic) {
				throw newErrMissingInvalidData(undefined, ` + "`field ${label} must be a key PEM`" + `);
			}
`)
	}
}

func writeTsIPAddrTypeValidation(b *strings.Builder, msg model.Message, field model.Field, fieldName, ipType string) error {
	var predicate, expected string
	switch ipType {
	case "IP_ADDR_TYPE_IP_V4":
		predicate = "isIPv4(value)"
		expected = "a valid IPv4 address"
	case "IP_ADDR_TYPE_IP_V6":
		predicate = "isIPv6(value)"
		expected = "a valid IPv6 address"
	case "IP_ADDR_TYPE_IP_ANY":
		predicate = "isIPv4(value) || isIPv6(value)"
		expected = "a valid IPv4 or IPv6 address"
	case "IP_ADDR_TYPE_MASK_V4":
		predicate = "isMask(value, 32)"
		expected = "a valid IPv4 mask (0-32)"
	case "IP_ADDR_TYPE_MASK_V6":
		predicate = "isMask(value, 128)"
		expected = "a valid IPv6 mask (0-128)"
	case "IP_ADDR_TYPE_MASK_ANY":
		predicate = "isMask(value, 128)"
		expected = "a valid IP mask (0-128)"
	case "IP_ADDR_TYPE_NET_V4":
		predicate = "isCIDR(value, 'v4')"
		expected = "a valid IPv4 network (CIDR notation)"
	case "IP_ADDR_TYPE_NET_V6":
		predicate = "isCIDR(value, 'v6')"
		expected = "a valid IPv6 network (CIDR notation)"
	case "IP_ADDR_TYPE_NET_ANY":
		predicate = "isCIDR(value, 'any')"
		expected = "a valid IPv4 or IPv6 network (CIDR notation)"
	default:
		return fmt.Errorf("unknown IP address type %q for field %s.%s", ipType, msg.Name, fieldName)
	}

	fmt.Fprintf(b, `	// Validate %s (assert_ipaddr_type: %s)
	{
		const ipv4Regex = /^(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])\.(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])\.(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])\.(25[0-5]|2[0-4][0-9]|1[0-9][0-9]|[1-9]?[0-9])$/;
		const ipv6Regex = /^(([0-9a-fA-F]{1,4}:){7}[0-9a-fA-F]{1,4}|([0-9a-fA-F]{1,4}:){1,7}:|([0-9a-fA-F]{1,4}:){1,6}:[0-9a-fA-F]{1,4}|([0-9a-fA-F]{1,4}:){1,5}(:[0-9a-fA-F]{1,4}){1,2}|([0-9a-fA-F]{1,4}:){1,4}(:[0-9a-fA-F]{1,4}){1,3}|([0-9a-fA-F]{1,4}:){1,3}(:[0-9a-fA-F]{1,4}){1,4}|([0-9a-fA-F]{1,4}:){1,2}(:[0-9a-fA-F]{1,4}){1,5}|[0-9a-fA-F]{1,4}:((:[0-9a-fA-F]{1,4}){1,6})|:((:[0-9a-fA-F]{1,4}){1,7}|:)|::(ffff(:0{1,4}){0,1}:){0,1}((25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9])\.){3}(25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9])|([0-9a-fA-F]{1,4}:){1,4}:((25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9])\.){3}(25[0-5]|(2[0-4]|1{0,1}[0-9]){0,1}[0-9]))$/;
		const isIPv4 = (value: string): boolean => ipv4Regex.test(value);
		const isIPv6 = (value: string): boolean => ipv6Regex.test(value);
		const isMask = (value: string, max: number): boolean => /^\d+$/.test(value) && Number.isInteger(Number(value)) && Number(value) >= 0 && Number(value) <= max;
		const isCIDR = (value: string, family: 'v4' | 'v6' | 'any'): boolean => {
			const parts = value.split('/');
			if (parts.length !== 2 || !/^\d+$/.test(parts[1])) {
				return false;
			}
			const mask = Number(parts[1]);
			if (isIPv4(parts[0])) {
				return (family === 'v4' || family === 'any') && mask >= 0 && mask <= 32;
			}
			if (isIPv6(parts[0])) {
				return (family === 'v6' || family === 'any') && mask >= 0 && mask <= 128;
			}
			return false;
		};
		const validateValue = (value: string): void => {
			if (!(%s)) {
				throw newErrMissingInvalidData(undefined, `+"`field %s must be %s, got: ${value}`"+`);
			}
		};
`, fieldName, ipType, predicate, fieldName, expected)

	if field.IsRepeated {
		fmt.Fprintf(b, `		for (const value of m.%s) {
			if (value !== '') {
				validateValue(String(value));
			}
		}
	}

`, fieldName)
	} else {
		fmt.Fprintf(b, `		if (m.%s !== undefined && m.%s !== '') {
			validateValue(String(m.%s));
		}
	}

`, fieldName, fieldName, fieldName)
	}

	return nil
}

func writeTsEnumDefinedValidation(b *strings.Builder, msg model.Message, field model.Field, fieldName string) error {
	if !field.IsEnum {
		return fmt.Errorf("assert_enum_defined annotation on field %s.%s requires an enum field", msg.Name, fieldName)
	}

	if field.IsRepeated {
		fmt.Fprintf(b, `	// Validate %s (assert_enum_defined)
	if (m.%s) {
		for (let i = 0; i < m.%s.length; i++) {
			const val = m.%s[i];
			if (Number(val) === 0) {
				throw newErrMissingInvalidData(undefined, `+"`field %s[${i}] must be defined`"+`);
			}
		}
	}

`, fieldName, fieldName, fieldName, fieldName, fieldName)
	} else if field.IsOptional {
		fmt.Fprintf(b, `	// Validate %s (assert_enum_defined)
	if (m.%s !== undefined && Number(m.%s) === 0) {
		throw newErrMissingInvalidData(undefined, `+"`field %s must be defined`"+`);
	}

`, fieldName, fieldName, fieldName, fieldName)
	} else {
		fmt.Fprintf(b, `	// Validate %s (assert_enum_defined)
	if (Number(m.%s) === 0) {
		throw newErrMissingInvalidData(undefined, `+"`field %s must be defined`"+`);
	}

`, fieldName, fieldName, fieldName)
	}

	return nil
}

func writeTsNumericConstraintValidation(b *strings.Builder, msg model.Message, field model.Field, fieldName string, constraint *model.NumericConstraint) error {
	if constraint == nil {
		return nil
	}
	if field.IsMap || !isIntegerType(field.Type) {
		return fmt.Errorf("assert_numeric annotation on field %s.%s requires an integer field", msg.Name, fieldName)
	}
	if constraint.MultipleOf != nil && *constraint.MultipleOf == 0 {
		return fmt.Errorf("assert_numeric multiple_of annotation on field %s.%s must be non-zero", msg.Name, fieldName)
	}

	fmt.Fprintf(b, "	// Validate %s (assert_numeric)\n", fieldName)
	if field.IsRepeated {
		fmt.Fprintf(b, `	if (m.%s) {
		for (let i = 0; i < m.%s.length; i++) {
			const val = m.%s[i];
`, fieldName, fieldName, fieldName)
		writeTsNumericConstraintChecks(b, field, fieldName+"[${i}]", "val", constraint, "\t\t\t")
		b.WriteString("		}\n")
		b.WriteString("	}\n\n")
		return nil
	}
	if field.IsOptional {
		fmt.Fprintf(b, "	if (m.%s !== undefined) {\n", fieldName)
		fmt.Fprintf(b, "		const val = m.%s;\n", fieldName)
		writeTsNumericConstraintChecks(b, field, fieldName, "val", constraint, "\t\t")
		b.WriteString("	}\n\n")
		return nil
	}

	b.WriteString("	{\n")
	fmt.Fprintf(b, "		const val = m.%s;\n", fieldName)
	writeTsNumericConstraintChecks(b, field, fieldName, "val", constraint, "\t\t")
	b.WriteString("	}\n\n")
	return nil
}

func writeTsNumericConstraintChecks(b *strings.Builder, field model.Field, fieldLabel, valueExpr string, constraint *model.NumericConstraint, indent string) {
	if constraint.Gt != nil {
		lit := tsIntLiteral(*constraint.Gt, field)
		fmt.Fprintf(b, "%sif (%s <= %s) {\n", indent, valueExpr, lit)
		fmt.Fprintf(b, "%s\tthrow newErrMissingInvalidData(undefined, `field %s must be > %d, got: ${%s}`);\n", indent, fieldLabel, *constraint.Gt, valueExpr)
		fmt.Fprintf(b, "%s}\n", indent)
	}
	if constraint.Ge != nil {
		lit := tsIntLiteral(*constraint.Ge, field)
		fmt.Fprintf(b, "%sif (%s < %s) {\n", indent, valueExpr, lit)
		fmt.Fprintf(b, "%s\tthrow newErrMissingInvalidData(undefined, `field %s must be >= %d, got: ${%s}`);\n", indent, fieldLabel, *constraint.Ge, valueExpr)
		fmt.Fprintf(b, "%s}\n", indent)
	}
	if constraint.Lt != nil {
		lit := tsIntLiteral(*constraint.Lt, field)
		fmt.Fprintf(b, "%sif (%s >= %s) {\n", indent, valueExpr, lit)
		fmt.Fprintf(b, "%s\tthrow newErrMissingInvalidData(undefined, `field %s must be < %d, got: ${%s}`);\n", indent, fieldLabel, *constraint.Lt, valueExpr)
		fmt.Fprintf(b, "%s}\n", indent)
	}
	if constraint.Le != nil {
		lit := tsIntLiteral(*constraint.Le, field)
		fmt.Fprintf(b, "%sif (%s > %s) {\n", indent, valueExpr, lit)
		fmt.Fprintf(b, "%s\tthrow newErrMissingInvalidData(undefined, `field %s must be <= %d, got: ${%s}`);\n", indent, fieldLabel, *constraint.Le, valueExpr)
		fmt.Fprintf(b, "%s}\n", indent)
	}
	if constraint.Eq != nil {
		lit := tsIntLiteral(*constraint.Eq, field)
		fmt.Fprintf(b, "%sif (%s !== %s) {\n", indent, valueExpr, lit)
		fmt.Fprintf(b, "%s\tthrow newErrMissingInvalidData(undefined, `field %s must equal %d, got: ${%s}`);\n", indent, fieldLabel, *constraint.Eq, valueExpr)
		fmt.Fprintf(b, "%s}\n", indent)
	}
	if constraint.Ne != nil {
		lit := tsIntLiteral(*constraint.Ne, field)
		fmt.Fprintf(b, "%sif (%s === %s) {\n", indent, valueExpr, lit)
		fmt.Fprintf(b, "%s\tthrow newErrMissingInvalidData(undefined, `field %s must not equal %d, got: ${%s}`);\n", indent, fieldLabel, *constraint.Ne, valueExpr)
		fmt.Fprintf(b, "%s}\n", indent)
	}
	if len(constraint.In) > 0 {
		list := tsIntListLiteral(constraint.In, field)
		msgList := tsIntMessageList(constraint.In)
		fmt.Fprintf(b, "%sif (!%s.some(candidate => %s === candidate)) {\n", indent, list, valueExpr)
		fmt.Fprintf(b, "%s\tthrow newErrMissingInvalidData(undefined, `field %s must be one of %s, got: ${%s}`);\n", indent, fieldLabel, msgList, valueExpr)
		fmt.Fprintf(b, "%s}\n", indent)
	}
	if len(constraint.NotIn) > 0 {
		list := tsIntListLiteral(constraint.NotIn, field)
		msgList := tsIntMessageList(constraint.NotIn)
		fmt.Fprintf(b, "%sif (%s.some(candidate => %s === candidate)) {\n", indent, list, valueExpr)
		fmt.Fprintf(b, "%s\tthrow newErrMissingInvalidData(undefined, `field %s must not be one of %s, got: ${%s}`);\n", indent, fieldLabel, msgList, valueExpr)
		fmt.Fprintf(b, "%s}\n", indent)
	}
	if constraint.MultipleOf != nil {
		lit := tsIntLiteral(*constraint.MultipleOf, field)
		fmt.Fprintf(b, "%sif (%s %% %s !== %s) {\n", indent, valueExpr, lit, tsIntLiteral(0, field))
		fmt.Fprintf(b, "%s\tthrow newErrMissingInvalidData(undefined, `field %s must be a multiple of %d, got: ${%s}`);\n", indent, fieldLabel, *constraint.MultipleOf, valueExpr)
		fmt.Fprintf(b, "%s}\n", indent)
	}
}

func writeTsFloatConstraintValidation(b *strings.Builder, msg model.Message, field model.Field, fieldName string, constraint *model.FloatConstraint) error {
	if constraint == nil {
		return nil
	}
	if field.IsMap || !isFloatType(field.Type) {
		return fmt.Errorf("assert_float annotation on field %s.%s requires a float or double field", msg.Name, fieldName)
	}

	epsilon := 1e-9
	if constraint.Epsilon != nil {
		epsilon = *constraint.Epsilon
	}

	fmt.Fprintf(b, "	// Validate %s (assert_float)\n", fieldName)
	if field.IsRepeated {
		fmt.Fprintf(b, `	if (m.%s) {
		for (let i = 0; i < m.%s.length; i++) {
			const val = m.%s[i];
`, fieldName, fieldName, fieldName)
		writeTsFloatConstraintChecks(b, fieldName+"[${i}]", "val", constraint, epsilon, "\t\t\t")
		b.WriteString("		}\n")
		b.WriteString("	}\n\n")
		return nil
	}
	if field.IsOptional {
		fmt.Fprintf(b, "	if (m.%s !== undefined) {\n", fieldName)
		fmt.Fprintf(b, "		const val = m.%s;\n", fieldName)
		writeTsFloatConstraintChecks(b, fieldName, "val", constraint, epsilon, "\t\t")
		b.WriteString("	}\n\n")
		return nil
	}

	b.WriteString("	{\n")
	fmt.Fprintf(b, "		const val = m.%s;\n", fieldName)
	writeTsFloatConstraintChecks(b, fieldName, "val", constraint, epsilon, "\t\t")
	b.WriteString("	}\n\n")
	return nil
}

func writeTsFloatConstraintChecks(b *strings.Builder, fieldLabel, valueExpr string, constraint *model.FloatConstraint, epsilon float64, indent string) {
	if constraint.Gt != nil {
		lit := formatTsFloat(*constraint.Gt)
		fmt.Fprintf(b, "%sif (%s <= %s) {\n", indent, valueExpr, lit)
		fmt.Fprintf(b, "%s\tthrow newErrMissingInvalidData(undefined, `field %s must be > %s, got: ${%s}`);\n", indent, fieldLabel, lit, valueExpr)
		fmt.Fprintf(b, "%s}\n", indent)
	}
	if constraint.Ge != nil {
		lit := formatTsFloat(*constraint.Ge)
		fmt.Fprintf(b, "%sif (%s < %s) {\n", indent, valueExpr, lit)
		fmt.Fprintf(b, "%s\tthrow newErrMissingInvalidData(undefined, `field %s must be >= %s, got: ${%s}`);\n", indent, fieldLabel, lit, valueExpr)
		fmt.Fprintf(b, "%s}\n", indent)
	}
	if constraint.Lt != nil {
		lit := formatTsFloat(*constraint.Lt)
		fmt.Fprintf(b, "%sif (%s >= %s) {\n", indent, valueExpr, lit)
		fmt.Fprintf(b, "%s\tthrow newErrMissingInvalidData(undefined, `field %s must be < %s, got: ${%s}`);\n", indent, fieldLabel, lit, valueExpr)
		fmt.Fprintf(b, "%s}\n", indent)
	}
	if constraint.Le != nil {
		lit := formatTsFloat(*constraint.Le)
		fmt.Fprintf(b, "%sif (%s > %s) {\n", indent, valueExpr, lit)
		fmt.Fprintf(b, "%s\tthrow newErrMissingInvalidData(undefined, `field %s must be <= %s, got: ${%s}`);\n", indent, fieldLabel, lit, valueExpr)
		fmt.Fprintf(b, "%s}\n", indent)
	}
	if len(constraint.In) > 0 {
		list := tsFloatListLiteral(constraint.In)
		epsilonLit := formatTsFloat(epsilon)
		msgList := tsFloatMessageList(constraint.In)
		fmt.Fprintf(b, "%sif (!%s.some(candidate => Math.abs(%s - candidate) <= %s)) {\n", indent, list, valueExpr, epsilonLit)
		fmt.Fprintf(b, "%s\tthrow newErrMissingInvalidData(undefined, `field %s must be one of %s, got: ${%s}`);\n", indent, fieldLabel, msgList, valueExpr)
		fmt.Fprintf(b, "%s}\n", indent)
	}
	if len(constraint.NotIn) > 0 {
		list := tsFloatListLiteral(constraint.NotIn)
		epsilonLit := formatTsFloat(epsilon)
		msgList := tsFloatMessageList(constraint.NotIn)
		fmt.Fprintf(b, "%sif (%s.some(candidate => Math.abs(%s - candidate) <= %s)) {\n", indent, list, valueExpr, epsilonLit)
		fmt.Fprintf(b, "%s\tthrow newErrMissingInvalidData(undefined, `field %s must not be one of %s, got: ${%s}`);\n", indent, fieldLabel, msgList, valueExpr)
		fmt.Fprintf(b, "%s}\n", indent)
	}
}

func tsIntLiteral(value int64, field model.Field) string {
	if isBigIntProtoType(field.Type) {
		return fmt.Sprintf("%dn", value)
	}
	return fmt.Sprintf("%d", value)
}

func tsIntListLiteral(values []int64, field model.Field) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, tsIntLiteral(value, field))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func tsIntMessageList(values []int64) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, fmt.Sprintf("%d", value))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func tsFloatListLiteral(values []float64) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, formatTsFloat(value))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func tsFloatMessageList(values []float64) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		parts = append(parts, formatTsFloat(value))
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func upperSnakeCaseToCamelCase(s string) string {
	return tsProtoFieldName(s)
}
