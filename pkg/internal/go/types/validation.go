// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

// This file is the Go validation emitter: it generates the per-type
// Validate<Type> functions (assert_* constraint checks, default
// collection/scalar size bounds, oneof scalar bounds, the nesting-depth
// guard, and enum vocabulary checks) plus the inline encryption and
// file-path validation helper functions.

package types

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/collection"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// requiredZeroCheck returns the Go expression to check if a field is at its zero
// value. It drives the whole requirement family — assert_required plus the
// conditional required_if_true / required_if_false / required_if_eq — so the same
// field shape yields the same test whichever annotation asks for it. Each arm has
// to be type-correct: `== ""` compiles only for a string field, so a requirement
// on a numeric, bool, bytes, enum, map or proto3-optional field used to emit
// generated Go that does not build.
func requiredZeroCheck(field model.Field, goFieldName string) string {
	switch {
	case field.IsRepeated || field.IsMap:
		return fmt.Sprintf("len(m.%s) == 0", goFieldName)
	case field.IsOptional:
		// proto3 `optional` scalars are pointers in generated Go.
		return fmt.Sprintf("m.%s == nil", goFieldName)
	case field.IsMessage:
		return fmt.Sprintf("m.%s == nil", goFieldName)
	case field.Type == "bytes":
		return fmt.Sprintf("len(m.%s) == 0", goFieldName)
	case field.Type == "bool":
		return fmt.Sprintf("!m.%s", goFieldName)
	case field.Type == "string":
		return fmt.Sprintf(`m.%s == ""`, goFieldName)
	default:
		// Numeric scalars and enums (generated as int32) compare against 0.
		return fmt.Sprintf("m.%s == 0", goFieldName)
	}
}

// generateValidationHelpers generates Validate functions for message types
func generateValidationHelpers(b *strings.Builder, msg model.Message) error {
	// Always generate a Validate function, even if no annotations
	fmt.Fprintf(b, `// Validate%s validates all fields in %s according to their annotations.
// Returns a BlueErr if validation fails, or nil if all validations pass.
func Validate%s(m *%s) error {
	return Validate%sWithDepth(m, 0)
}

// Validate%sWithDepth validates %s and carries nested-message depth across
// generated package boundaries.
func Validate%sWithDepth(m *%s, depth int) error {
	if m == nil {
		return nil
	}
	if depth > 24 {
		return protoerrors.NewErrMissingInvalidData(nil, "message nesting exceeds max depth of 24")
	}

`, msg.Name, msg.Name, msg.Name, msg.Name, msg.Name, msg.Name, msg.Name, msg.Name, msg.Name)

	// Generate validation code for each field with annotations
	hasValidations := false

	// Track which helper functions we need to generate
	needsInHelper := make(map[string][]int64)
	needsNotInHelper := make(map[string][]int64)
	needsFloatInHelper := make(map[string]goFloatMembership)
	needsFloatNotInHelper := make(map[string]goFloatMembership)
	messageValidateRef := func(typeName, typePackage string) string {
		if typeName == "" {
			return ""
		}
		switch typePackage {
		case "anypb", "structpb", "timestamppb", "durationpb", "emptypb", "fieldmaskpb", "wrapperspb":
			return ""
		case "":
			return fmt.Sprintf("Validate%sWithDepth", typeName)
		default:
			return fmt.Sprintf("%s.Validate%sWithDepth", typePackage, typeName)
		}
	}
	enumNameMapRef := func(typeName, typePackage string) string {
		if typeName == "" {
			return ""
		}
		if typePackage == "" {
			return fmt.Sprintf("%s_name", typeName)
		}
		return fmt.Sprintf("%s.%s_name", typePackage, typeName)
	}

	for _, field := range msg.Fields {
		if field.IsOneOf {
			if len(field.Annotations) == 0 {
				continue
			}
			goFieldName := toGoFieldName(field.Name)
			for _, annotationName := range model.SortedAnnotationNames(field.Annotations) {
				annotationValue := field.Annotations[annotationName]
				if annotationValue.Type == model.AnnotationTypeBool && !annotationValue.BoolValue {
					continue
				}
				if annotationName != "assert_max_size" {
					continue
				}
				if annotationValue.Type != model.AnnotationTypeInt {
					return fmt.Errorf("assert_max_size annotation on field %s.%s must be an integer value", msg.Name, goFieldName)
				}
				if field.Type != "string" && field.Type != "bytes" {
					continue
				}
				hasValidations = true
				errFunc := "protoerrors.NewErrMissingInvalidData"
				fmt.Fprintf(b, `	// Validate %s (assert_max_size: %d bytes)
	if len(m.Get%s()) > %d {
		return %s(nil, "field %s exceeds max size of %d bytes, got: %%d", len(m.Get%s()))
	}

`, goFieldName, annotationValue.IntValue, goFieldName, annotationValue.IntValue, errFunc, goFieldName, annotationValue.IntValue, goFieldName)
			}
			continue
		}
		if len(field.Annotations) == 0 {
			continue
		}

		hasValidations = true
		// Convert proto field name (snake_case) to Go field name (PascalCase)
		goFieldName := toGoFieldName(field.Name)

		for _, annotationName := range model.SortedAnnotationNames(field.Annotations) {
			annotationValue := field.Annotations[annotationName]
			// Skip boolean annotations that are set to false
			if annotationValue.Type == model.AnnotationTypeBool && !annotationValue.BoolValue {
				continue
			}

			// Generate validation code based on annotation type
			switch annotationName {
			case "assert_required":
				// Generate required field validation. The zero test is shared with the
				// conditional-requirement family so that (spec.assert_required) and
				// (spec.required_if_true) mean the same thing on the same field.
				errFunc := "protoerrors.NewErrMissingInvalidData"
				fmt.Fprintf(b, `	// Validate %s (assert_required)
	if %s {
		return %s(nil, "field %s is required")
	}

`, goFieldName, requiredZeroCheck(field, goFieldName), errFunc, goFieldName)

			case "assert_ipaddr":
				// Simple IP address validation (IPv4 or IPv6)
				if field.Type != "string" {
					return fmt.Errorf("assert_ipaddr annotation on field %s.%s requires string type", msg.Name, goFieldName)
				}

				errFunc := "protoerrors.NewErrMissingInvalidData"

				if field.IsRepeated {
					fmt.Fprintf(b, `	// Validate %s (assert_ipaddr)
	for _, v := range m.%s {
		if v != "" {
			ip := net.ParseIP(v)
			if ip == nil {
				return %s(nil, "field %s must contain valid IPv4 or IPv6 addresses")
			}
		}
	}

`, goFieldName, goFieldName, errFunc, goFieldName)
				} else {
					fmt.Fprintf(b, `	// Validate %s (assert_ipaddr)
	if m.%s != "" {
		ip := net.ParseIP(m.%s)
		if ip == nil {
			return %s(nil, "field %s must be a valid IPv4 or IPv6 address")
		}
	}

`, goFieldName, goFieldName, goFieldName, errFunc, goFieldName)
				}

			case "assert_ipaddr_type":
				// Generate IP address validation based on enum type
				if field.Type != "string" {
					return fmt.Errorf("assert_ipaddr_type annotation on field %s.%s requires string type", msg.Name, goFieldName)
				}

				if annotationValue.Type != model.AnnotationTypeEnum {
					return fmt.Errorf("assert_ipaddr_type annotation on field %s.%s must be an enum value", msg.Name, goFieldName)
				}

				errFunc := "protoerrors.NewErrMissingInvalidData"

				// Generate validation based on IP address type
				switch annotationValue.EnumValue {
				case "IP_ADDR_TYPE_IP_V4":
					if field.IsRepeated {
						fmt.Fprintf(b, `	// Validate %s (assert_ipaddr_type: IPv4)
	for _, v := range m.%s {
		if v != "" {
			ip := net.ParseIP(v)
			if ip == nil || ip.To4() == nil {
				return %s(nil, "field %s must contain valid IPv4 addresses, got: %%s", v)
			}
		}
	}

`, goFieldName, goFieldName, errFunc, goFieldName)
					} else {
						fmt.Fprintf(b, `	// Validate %s (assert_ipaddr_type: IPv4)
	if m.%s != "" {
		ip := net.ParseIP(m.%s)
		if ip == nil || ip.To4() == nil {
			return %s(nil, "field %s must be a valid IPv4 address, got: %%s", m.%s)
		}
	}

`, goFieldName, goFieldName, goFieldName, errFunc, goFieldName, goFieldName)
					}

				case "IP_ADDR_TYPE_IP_V6":
					if field.IsRepeated {
						fmt.Fprintf(b, `	// Validate %s (assert_ipaddr_type: IPv6)
	for _, v := range m.%s {
		if v != "" {
			ip := net.ParseIP(v)
			if ip == nil || !strings.Contains(v, ":") {
				return %s(nil, "field %s must contain valid IPv6 addresses, got: %%s", v)
			}
		}
	}

`, goFieldName, goFieldName, errFunc, goFieldName)
					} else {
						fmt.Fprintf(b, `	// Validate %s (assert_ipaddr_type: IPv6)
	if m.%s != "" {
		ip := net.ParseIP(m.%s)
		if ip == nil || !strings.Contains(m.%s, ":") {
			return %s(nil, "field %s must be a valid IPv6 address, got: %%s", m.%s)
		}
	}

`, goFieldName, goFieldName, goFieldName, goFieldName, errFunc, goFieldName, goFieldName)
					}

				case "IP_ADDR_TYPE_IP_ANY":
					if field.IsRepeated {
						fmt.Fprintf(b, `	// Validate %s (assert_ipaddr_type: IPv4 or IPv6)
	for _, v := range m.%s {
		if v != "" {
			ip := net.ParseIP(v)
			if ip == nil {
				return %s(nil, "field %s must contain valid IP addresses, got: %%s", v)
			}
		}
	}

`, goFieldName, goFieldName, errFunc, goFieldName)
					} else {
						fmt.Fprintf(b, `	// Validate %s (assert_ipaddr_type: IPv4 or IPv6)
	if m.%s != "" {
		ip := net.ParseIP(m.%s)
		if ip == nil {
			return %s(nil, "field %s must be a valid IP address, got: %%s", m.%s)
		}
	}

`, goFieldName, goFieldName, goFieldName, errFunc, goFieldName, goFieldName)
					}

				case "IP_ADDR_TYPE_MASK_V4":
					if field.IsRepeated {
						fmt.Fprintf(b, `	// Validate %s (assert_ipaddr_type: IPv4 mask)
	for _, v := range m.%s {
		if v != "" {
			mask, err := strconv.Atoi(v)
			if err != nil || mask < 0 || mask > 32 {
				return %s(nil, "field %s must contain valid IPv4 masks (0-32), got: %%s", v)
			}
		}
	}

`, goFieldName, goFieldName, errFunc, goFieldName)
					} else {
						fmt.Fprintf(b, `	// Validate %s (assert_ipaddr_type: IPv4 mask)
	if m.%s != "" {
		mask, err := strconv.Atoi(m.%s)
		if err != nil || mask < 0 || mask > 32 {
			return %s(nil, "field %s must be a valid IPv4 mask (0-32), got: %%s", m.%s)
		}
	}

`, goFieldName, goFieldName, goFieldName, errFunc, goFieldName, goFieldName)
					}

				case "IP_ADDR_TYPE_MASK_V6":
					if field.IsRepeated {
						fmt.Fprintf(b, `	// Validate %s (assert_ipaddr_type: IPv6 mask)
	for _, v := range m.%s {
		if v != "" {
			mask, err := strconv.Atoi(v)
			if err != nil || mask < 0 || mask > 128 {
				return %s(nil, "field %s must contain valid IPv6 masks (0-128), got: %%s", v)
			}
		}
	}

`, goFieldName, goFieldName, errFunc, goFieldName)
					} else {
						fmt.Fprintf(b, `	// Validate %s (assert_ipaddr_type: IPv6 mask)
	if m.%s != "" {
		mask, err := strconv.Atoi(m.%s)
		if err != nil || mask < 0 || mask > 128 {
			return %s(nil, "field %s must be a valid IPv6 mask (0-128), got: %%s", m.%s)
		}
	}

`, goFieldName, goFieldName, goFieldName, errFunc, goFieldName, goFieldName)
					}

				case "IP_ADDR_TYPE_MASK_ANY":
					if field.IsRepeated {
						fmt.Fprintf(b, `	// Validate %s (assert_ipaddr_type: IPv4 or IPv6 mask)
	for _, v := range m.%s {
		if v != "" {
			mask, err := strconv.Atoi(v)
			if err != nil || mask < 0 || mask > 128 {
				return %s(nil, "field %s must contain valid IP masks (0-128), got: %%s", v)
			}
		}
	}

`, goFieldName, goFieldName, errFunc, goFieldName)
					} else {
						fmt.Fprintf(b, `	// Validate %s (assert_ipaddr_type: IPv4 or IPv6 mask)
	if m.%s != "" {
		mask, err := strconv.Atoi(m.%s)
		if err != nil || mask < 0 || mask > 128 {
			return %s(nil, "field %s must be a valid IP mask (0-128), got: %%s", m.%s)
		}
	}

`, goFieldName, goFieldName, goFieldName, errFunc, goFieldName, goFieldName)
					}

				case "IP_ADDR_TYPE_NET_V4":
					if field.IsRepeated {
						fmt.Fprintf(b, `	// Validate %s (assert_ipaddr_type: IPv4 network)
	for _, v := range m.%s {
		if v != "" {
			_, _, err := net.ParseCIDR(v)
			if err != nil {
				return %s(nil, "field %s must contain valid IPv4 networks (CIDR notation), got: %%s", v)
			}
			// Verify it's IPv4
			ip, _, _ := net.ParseCIDR(v)
			if ip.To4() == nil {
				return %s(nil, "field %s must contain IPv4 networks, got: %%s", v)
			}
		}
	}

`, goFieldName, goFieldName, errFunc, goFieldName, errFunc, goFieldName)
					} else {
						fmt.Fprintf(b, `	// Validate %s (assert_ipaddr_type: IPv4 network)
	if m.%s != "" {
		_, _, err := net.ParseCIDR(m.%s)
		if err != nil {
			return %s(nil, "field %s must be a valid IPv4 network (CIDR notation), got: %%s", m.%s)
		}
		// Verify it's IPv4
		ip, _, _ := net.ParseCIDR(m.%s)
		if ip.To4() == nil {
			return %s(nil, "field %s must be an IPv4 network, got: %%s", m.%s)
		}
	}

`, goFieldName, goFieldName, goFieldName, errFunc, goFieldName, goFieldName, goFieldName, errFunc, goFieldName, goFieldName)
					}

				case "IP_ADDR_TYPE_NET_V6":
					if field.IsRepeated {
						fmt.Fprintf(b, `	// Validate %s (assert_ipaddr_type: IPv6 network)
	for _, v := range m.%s {
		if v != "" {
			_, _, err := net.ParseCIDR(v)
			if err != nil {
				return %s(nil, "field %s must contain valid IPv6 networks (CIDR notation), got: %%s", v)
			}
			// Verify it's IPv6
			ip, _, _ := net.ParseCIDR(v)
			if ip.To4() != nil {
				return %s(nil, "field %s must contain IPv6 networks, got: %%s", v)
			}
		}
	}

`, goFieldName, goFieldName, errFunc, goFieldName, errFunc, goFieldName)
					} else {
						fmt.Fprintf(b, `	// Validate %s (assert_ipaddr_type: IPv6 network)
	if m.%s != "" {
		_, _, err := net.ParseCIDR(m.%s)
		if err != nil {
			return %s(nil, "field %s must be a valid IPv6 network (CIDR notation), got: %%s", m.%s)
		}
		// Verify it's IPv6
		ip, _, _ := net.ParseCIDR(m.%s)
		if ip.To4() != nil {
			return %s(nil, "field %s must be an IPv6 network, got: %%s", m.%s)
		}
	}

`, goFieldName, goFieldName, goFieldName, errFunc, goFieldName, goFieldName, goFieldName, errFunc, goFieldName, goFieldName)
					}

				case "IP_ADDR_TYPE_NET_ANY":
					if field.IsRepeated {
						fmt.Fprintf(b, `	// Validate %s (assert_ipaddr_type: IPv4 or IPv6 network)
	for _, v := range m.%s {
		if v != "" {
			_, _, err := net.ParseCIDR(v)
			if err != nil {
				return %s(nil, "field %s must contain valid IP networks (CIDR notation), got: %%s", v)
			}
		}
	}

`, goFieldName, goFieldName, errFunc, goFieldName)
					} else {
						fmt.Fprintf(b, `	// Validate %s (assert_ipaddr_type: IPv4 or IPv6 network)
	if m.%s != "" {
		_, _, err := net.ParseCIDR(m.%s)
		if err != nil {
			return %s(nil, "field %s must be a valid IP network (CIDR notation), got: %%s", m.%s)
		}
	}

`, goFieldName, goFieldName, goFieldName, errFunc, goFieldName, goFieldName)
					}

				default:
					return fmt.Errorf("unknown IP address type '%s' for field %s.%s", annotationValue.EnumValue, msg.Name, goFieldName)
				}

			case "assert_enc_type":
				// Generate encryption key/certificate validation
				if field.Type != "string" {
					return fmt.Errorf("assert_enc_type annotation on field %s.%s requires string type", msg.Name, goFieldName)
				}

				if annotationValue.Type != model.AnnotationTypeEnum {
					return fmt.Errorf("assert_enc_type annotation on field %s.%s must be an enum value", msg.Name, goFieldName)
				}

				errFunc := "protoerrors.NewErrMissingInvalidData"

				// Generate validation based on encryption type
				if field.IsRepeated {
					fmt.Fprintf(b, `	// Validate %s (assert_enc_type: %s)
	for _, v := range m.%s {
		if v != "" {
			if err := validate%s(normalizePEMData(v)); err != nil {
				return %s(err, "field %s validation failed")
			}
		}
	}

`, goFieldName, annotationValue.EnumValue, goFieldName, annotationValue.EnumValue, errFunc, goFieldName)
				} else {
					fmt.Fprintf(b, `	// Validate %s (assert_enc_type: %s)
	if m.%s != "" {
		if err := validate%s(normalizePEMData(m.%s)); err != nil {
			return %s(err, "field %s validation failed")
		}
	}

`, goFieldName, annotationValue.EnumValue, goFieldName, annotationValue.EnumValue, goFieldName, errFunc, goFieldName)
				}

			case "assert_net_port":
				// Generate port validation
				if err := writeGoDurationBoundValidation(b, msg, field, goFieldName, "assert_net_port", "assert_net_port", "%[1]s < 0 || %[1]s > 65535", "a valid port number (0-65535)"); err != nil {
					return err
				}

			case "assert_url":
				// Generate URL validation
				if field.Type != "string" {
					return fmt.Errorf("assert_url annotation on field %s.%s requires string type", msg.Name, goFieldName)
				}

				if annotationValue.Type != model.AnnotationTypeEnum {
					return fmt.Errorf("assert_url annotation on field %s.%s must be an enum value", msg.Name, goFieldName)
				}

				errFunc := "protoerrors.NewErrMissingInvalidData"

				if field.IsRepeated {
					fmt.Fprintf(b, `	// Validate %s (assert_url: %s)
	for _, v := range m.%s {
		if v != "" {
			if err := utils.ValidateAssertURL_%s(v); err != nil {
				return %s(err, "field %s URL validation failed")
			}
		}
	}

`, goFieldName, annotationValue.EnumValue, goFieldName, annotationValue.EnumValue, errFunc, goFieldName)
				} else {
					fmt.Fprintf(b, `	// Validate %s (assert_url: %s)
	if m.%s != "" {
		if err := utils.ValidateAssertURL_%s(m.%s); err != nil {
			return %s(err, "field %s URL validation failed")
		}
	}

`, goFieldName, annotationValue.EnumValue, goFieldName, annotationValue.EnumValue, goFieldName, errFunc, goFieldName)
				}

			case "assert_sql":
				// Generate SQL validation
				if field.Type != "string" {
					return fmt.Errorf("assert_sql annotation on field %s.%s requires string type", msg.Name, goFieldName)
				}

				if annotationValue.Type != model.AnnotationTypeEnum {
					return fmt.Errorf("assert_sql annotation on field %s.%s must be an enum value", msg.Name, goFieldName)
				}

				errFunc := "protoerrors.NewErrMissingInvalidData"

				if field.IsRepeated {
					fmt.Fprintf(b, `	// Validate %s (assert_sql: %s)
	for _, v := range m.%s {
		if v != "" {
			if err := utils.ValidateAssertSQL_%s(v); err != nil {
				return %s(err, "field %s SQL validation failed")
			}
		}
	}

`, goFieldName, annotationValue.EnumValue, goFieldName, annotationValue.EnumValue, errFunc, goFieldName)
				} else {
					fmt.Fprintf(b, `	// Validate %s (assert_sql: %s)
	if m.%s != "" {
		if err := utils.ValidateAssertSQL_%s(m.%s); err != nil {
			return %s(err, "field %s SQL validation failed")
		}
	}

`, goFieldName, annotationValue.EnumValue, goFieldName, annotationValue.EnumValue, goFieldName, errFunc, goFieldName)
				}

			case "assert_email":
				// Generate email validation
				if field.Type != "string" {
					return fmt.Errorf("assert_email annotation on field %s.%s requires string type", msg.Name, goFieldName)
				}

				errFunc := "protoerrors.NewErrMissingInvalidData"

				if field.IsRepeated {
					fmt.Fprintf(b, `	// Validate %s (assert_email)
	for _, v := range m.%s {
		if v != "" {
			if err := utils.ValidateAssertEmail(v); err != nil {
				return %s(err, "field %s email validation failed")
			}
		}
	}

`, goFieldName, goFieldName, errFunc, goFieldName)
				} else {
					fmt.Fprintf(b, `	// Validate %s (assert_email)
	if m.%s != "" {
		if err := utils.ValidateAssertEmail(m.%s); err != nil {
			return %s(err, "field %s email validation failed")
		}
	}

`, goFieldName, goFieldName, goFieldName, errFunc, goFieldName)
				}

			case "assert_fqdn":
				// Generate FQDN validation
				if field.Type != "string" {
					return fmt.Errorf("assert_fqdn annotation on field %s.%s requires string type", msg.Name, goFieldName)
				}

				errFunc := "protoerrors.NewErrMissingInvalidData"

				if field.IsRepeated {
					fmt.Fprintf(b, `	// Validate %s (assert_fqdn)
	for _, v := range m.%s {
		if v != "" {
			if err := utils.ValidateAssertFQDN(v); err != nil {
				return %s(err, "field %s FQDN validation failed")
			}
		}
	}

`, goFieldName, goFieldName, errFunc, goFieldName)
				} else {
					fmt.Fprintf(b, `	// Validate %s (assert_fqdn)
	if m.%s != "" {
		if err := utils.ValidateAssertFQDN(m.%s); err != nil {
			return %s(err, "field %s FQDN validation failed")
		}
	}

`, goFieldName, goFieldName, goFieldName, errFunc, goFieldName)
				}

			case "assert_host":
				// Generate host validation (IP address OR FQDN)
				if field.Type != "string" {
					return fmt.Errorf("assert_host annotation on field %s.%s requires string type", msg.Name, goFieldName)
				}

				errFunc := "protoerrors.NewErrMissingInvalidData"

				if field.IsRepeated {
					fmt.Fprintf(b, `	// Validate %s (assert_host: IP address or FQDN)
	for _, v := range m.%s {
		if v != "" {
			if ip := net.ParseIP(v); ip == nil {
				if err := utils.ValidateAssertFQDN(v); err != nil {
					return %s(nil, "field %s must be a valid IP address or FQDN, got: %%s", v)
				}
			}
		}
	}

`, goFieldName, goFieldName, errFunc, goFieldName)
				} else {
					fmt.Fprintf(b, `	// Validate %s (assert_host: IP address or FQDN)
	if m.%s != "" {
		if ip := net.ParseIP(m.%s); ip == nil {
			if err := utils.ValidateAssertFQDN(m.%s); err != nil {
				return %s(nil, "field %s must be a valid IP address or FQDN, got: %%s", m.%s)
			}
		}
	}

`, goFieldName, goFieldName, goFieldName, goFieldName, errFunc, goFieldName, goFieldName)
				}

			case "assert_duration":
				// Generate duration validation
				if annotationValue.Type != model.AnnotationTypeEnum {
					return fmt.Errorf("assert_duration annotation on field %s.%s must be an enum value", msg.Name, goFieldName)
				}

				switch annotationValue.EnumValue {
				case "DURATION_TYPE_POSITIVE":
					if err := writeGoDurationBoundValidation(b, msg, field, goFieldName, "assert_duration", "assert_duration: POSITIVE", "%[1]s <= 0", "positive (> 0)"); err != nil {
						return err
					}
				case "DURATION_TYPE_NON_NEGATIVE":
					if err := writeGoDurationBoundValidation(b, msg, field, goFieldName, "assert_duration", "assert_duration: NON_NEGATIVE", "%[1]s < 0", "non-negative (>= 0)"); err != nil {
						return err
					}
				}

			case "assert_duration_min":
				// Generate minimum duration validation
				if annotationValue.Type != model.AnnotationTypeInt {
					return fmt.Errorf("assert_duration_min annotation on field %s.%s must be an integer value", msg.Name, goFieldName)
				}

				if err := writeGoDurationBoundValidation(b, msg, field, goFieldName, "assert_duration_min",
					fmt.Sprintf("assert_duration_min: %d", annotationValue.IntValue),
					fmt.Sprintf("%%[1]s < %d", annotationValue.IntValue),
					fmt.Sprintf(">= %d", annotationValue.IntValue)); err != nil {
					return err
				}

			case "assert_duration_max":
				// Generate maximum duration validation
				if annotationValue.Type != model.AnnotationTypeInt {
					return fmt.Errorf("assert_duration_max annotation on field %s.%s must be an integer value", msg.Name, goFieldName)
				}

				if err := writeGoDurationBoundValidation(b, msg, field, goFieldName, "assert_duration_max",
					fmt.Sprintf("assert_duration_max: %d", annotationValue.IntValue),
					fmt.Sprintf("%%[1]s > %d", annotationValue.IntValue),
					fmt.Sprintf("<= %d", annotationValue.IntValue)); err != nil {
					return err
				}

			case "assert_listen_addr":
				// Generate listen address validation (host:port format)
				if field.Type != "string" {
					return fmt.Errorf("assert_listen_addr annotation on field %s.%s requires string type", msg.Name, goFieldName)
				}

				errFunc := "protoerrors.NewErrMissingInvalidData"

				if field.IsRepeated {
					fmt.Fprintf(b, `	// Validate %s (assert_listen_addr)
	for _, v := range m.%s {
		if v != "" {
			if err := utils.ValidateAssertListenAddr(v); err != nil {
				return %s(err, "field %s listen address validation failed")
			}
		}
	}

`, goFieldName, goFieldName, errFunc, goFieldName)
				} else {
					fmt.Fprintf(b, `	// Validate %s (assert_listen_addr)
	if m.%s != "" {
		if err := utils.ValidateAssertListenAddr(m.%s); err != nil {
			return %s(err, "field %s listen address validation failed")
		}
	}

`, goFieldName, goFieldName, goFieldName, errFunc, goFieldName)
				}

			case "assert_base64":
				// Generate base64 validation
				if field.Type != "string" {
					return fmt.Errorf("assert_base64 annotation on field %s.%s requires string type", msg.Name, goFieldName)
				}

				errFunc := "protoerrors.NewErrMissingInvalidData"

				if field.IsRepeated {
					fmt.Fprintf(b, `	// Validate %s (assert_base64)
	for _, v := range m.%s {
		if v != "" {
			if err := utils.ValidateAssertBase64(v); err != nil {
				return %s(err, "field %s base64 validation failed")
			}
		}
	}

`, goFieldName, goFieldName, errFunc, goFieldName)
				} else {
					fmt.Fprintf(b, `	// Validate %s (assert_base64)
	if m.%s != "" {
		if err := utils.ValidateAssertBase64(m.%s); err != nil {
			return %s(err, "field %s base64 validation failed")
		}
	}

`, goFieldName, goFieldName, goFieldName, errFunc, goFieldName)
				}

			case "assert_file_path":
				// Generate file path validation
				if field.Type != "string" {
					return fmt.Errorf("assert_file_path annotation on field %s.%s requires string type", msg.Name, goFieldName)
				}

				if annotationValue.Type != model.AnnotationTypeEnum {
					return fmt.Errorf("assert_file_path annotation on field %s.%s must be an enum value", msg.Name, goFieldName)
				}

				errFunc := "protoerrors.NewErrMissingInvalidData"

				if field.IsRepeated {
					// For repeated fields, validate each element in the slice
					fmt.Fprintf(b, `	// Validate %s (assert_file_path: %s)
	for i, path := range m.%s {
		if path != "" {
			if err := validateFilePath%s(path); err != nil {
				return %s(err, "field %s[%%d] file path validation failed", i)
			}
		}
	}

`, goFieldName, annotationValue.EnumValue, goFieldName, annotationValue.EnumValue, errFunc, goFieldName)
				} else {
					// For non-repeated fields, validate the single value
					fmt.Fprintf(b, `	// Validate %s (assert_file_path: %s)
	if m.%s != "" {
		if err := validateFilePath%s(m.%s); err != nil {
			return %s(err, "field %s file path validation failed")
		}
	}

`, goFieldName, annotationValue.EnumValue, goFieldName, annotationValue.EnumValue, goFieldName, errFunc, goFieldName)
				}

			case "assert_invariant":
				if annotationValue.Type == model.AnnotationTypeString && annotationValue.StringValue != "" {
					fmt.Fprintf(b, "	// Formal invariant for %s (assert_invariant): %s\n", goFieldName, annotationValue.StringValue)
				}

			case "assert_enum_defined":
				if !field.IsEnum {
					return fmt.Errorf("assert_enum_defined annotation on field %s.%s requires an enum field", msg.Name, goFieldName)
				}

				errFunc := "protoerrors.NewErrMissingInvalidData"
				if field.IsRepeated {
					fmt.Fprintf(b, `	// Validate %s (assert_enum_defined)
	for i, val := range m.%s {
		if val == 0 {
			return %s(nil, "field %s[%%d] must be defined", i)
		}
	}

`, goFieldName, goFieldName, errFunc, goFieldName)
				} else if field.IsOptional {
					fmt.Fprintf(b, `	// Validate %s (assert_enum_defined)
	if m.%s != nil && *m.%s == 0 {
		return %s(nil, "field %s must be defined")
	}

`, goFieldName, goFieldName, goFieldName, errFunc, goFieldName)
				} else {
					fmt.Fprintf(b, `	// Validate %s (assert_enum_defined)
	if m.%s == 0 {
		return %s(nil, "field %s must be defined")
	}

`, goFieldName, goFieldName, errFunc, goFieldName)
				}

			case "assert_gt":
				// Simple greater than comparison
				if err := writeGoComparisonValidation(b, msg, field, goFieldName, "assert_gt", "<=", ">", annotationValue.IntValue); err != nil {
					return err
				}

			case "assert_ge":
				// Simple greater than or equal comparison
				if err := writeGoComparisonValidation(b, msg, field, goFieldName, "assert_ge", "<", ">=", annotationValue.IntValue); err != nil {
					return err
				}

			case "assert_lt":
				// Simple less than comparison
				if err := writeGoComparisonValidation(b, msg, field, goFieldName, "assert_lt", ">=", "<", annotationValue.IntValue); err != nil {
					return err
				}

			case "assert_le":
				// Simple less than or equal comparison
				if err := writeGoComparisonValidation(b, msg, field, goFieldName, "assert_le", ">", "<=", annotationValue.IntValue); err != nil {
					return err
				}

			case "assert_numeric":
				// Complex numeric constraint
				if annotationValue.NumericConstraint != nil {
					constraint := annotationValue.NumericConstraint
					if field.IsMap || !isIntegerType(field.Type) {
						return fmt.Errorf("assert_numeric annotation on field %s.%s requires an integer field", msg.Name, goFieldName)
					}
					if constraint.MultipleOf != nil && *constraint.MultipleOf == 0 {
						return fmt.Errorf("assert_numeric multiple_of annotation on field %s.%s must be non-zero", msg.Name, goFieldName)
					}

					var checks strings.Builder
					switch {
					case field.IsRepeated:
						writeGoNumericConstraintChecks(&checks, msg.Name, goFieldName, "val", "i", constraint, needsInHelper, needsNotInHelper)
					case field.IsOptional:
						writeGoNumericConstraintChecks(&checks, msg.Name, goFieldName, "*m."+goFieldName, "", constraint, needsInHelper, needsNotInHelper)
					default:
						writeGoNumericConstraintChecks(&checks, msg.Name, goFieldName, "m."+goFieldName, "", constraint, needsInHelper, needsNotInHelper)
					}

					if checks.Len() > 0 {
						fmt.Fprintf(b, "// Validate %s (assert_numeric)\n", goFieldName)
						switch {
						case field.IsRepeated:
							fmt.Fprintf(b, "for i, val := range m.%s {\n", goFieldName)
							b.WriteString(checks.String())
							b.WriteString("}\n")
						case field.IsOptional:
							fmt.Fprintf(b, "if m.%s != nil {\n", goFieldName)
							b.WriteString(checks.String())
							b.WriteString("}\n")
						default:
							b.WriteString(checks.String())
						}
					}
				}

			case "assert_float":
				// Complex float constraint
				if annotationValue.FloatConstraint != nil {
					constraint := annotationValue.FloatConstraint
					if field.IsMap || !isFloatType(field.Type) {
						return fmt.Errorf("assert_float annotation on field %s.%s requires a float or double field", msg.Name, goFieldName)
					}
					epsilon := 1e-9
					if constraint.Epsilon != nil {
						epsilon = *constraint.Epsilon
					}

					// Determine field accessor based on the field's shape.
					fieldAccessor := "m." + goFieldName
					indexVar := ""
					switch {
					case field.IsRepeated:
						fieldAccessor = "val"
						indexVar = "i"
					case field.IsOptional:
						fieldAccessor = "*m." + goFieldName
					}

					var checks strings.Builder
					writeGoFloatConstraintChecks(&checks, msg.Name, goFieldName, fieldAccessor, indexVar, constraint, epsilon, needsFloatInHelper, needsFloatNotInHelper)

					if checks.Len() > 0 {
						fmt.Fprintf(b, "// Validate %s (assert_float)\n", goFieldName)
						switch {
						case field.IsRepeated:
							fmt.Fprintf(b, "for i, val := range m.%s {\n", goFieldName)
							b.WriteString(checks.String())
							b.WriteString("}\n")
						case field.IsOptional:
							fmt.Fprintf(b, "if m.%s != nil {\n", goFieldName)
							b.WriteString(checks.String())
							b.WriteString("}\n")
						default:
							b.WriteString(checks.String())
						}
					}
				}

			case "assert_semver":
				// Generate semantic version validation
				if field.Type != "string" {
					return fmt.Errorf("assert_semver annotation on field %s.%s requires string type", msg.Name, goFieldName)
				}

				errFunc := "protoerrors.NewErrMissingInvalidData"

				if field.IsRepeated {
					fmt.Fprintf(b, `	// Validate %s (assert_semver)
	for _, v := range m.%s {
		if v != "" {
			if err := utils.ValidateAssertSemver(v); err != nil {
				return %s(err, "field %s must be a valid semantic version (e.g., '1.0.0' or 'v2.3.4')")
			}
		}
	}

`, goFieldName, goFieldName, errFunc, goFieldName)
				} else {
					fmt.Fprintf(b, `	// Validate %s (assert_semver)
	if m.%s != "" {
		if err := utils.ValidateAssertSemver(m.%s); err != nil {
			return %s(err, "field %s must be a valid semantic version (e.g., '1.0.0' or 'v2.3.4')")
		}
	}

`, goFieldName, goFieldName, goFieldName, errFunc, goFieldName)
				}

			case "epoch_expiration":
				errFunc := "protoerrors.NewErrMissingInvalidData"
				fmt.Fprintf(b, `	// Validate %s (epoch_expiration)
	if m.%s != 0 {
		now := time.Now().Unix()
		if m.%s <= now {
			return %s(nil, "field %s must be a future epoch timestamp, got: %%d (now: %%d)", m.%s, now)
		}
		if m.%s > now+31536000 {
			return %s(nil, "field %s must be within 1 year from now, got: %%d (max: %%d)", m.%s, now+31536000)
		}
	}

`, goFieldName, goFieldName, goFieldName, errFunc, goFieldName, goFieldName, goFieldName, errFunc, goFieldName, goFieldName)

			case "required_if_true":
				condFieldName := toGoFieldName(annotationValue.StringValue)
				errFunc := "protoerrors.NewErrMissingInvalidData"
				zeroCheck := requiredZeroCheck(field, goFieldName)
				fmt.Fprintf(b, `	// Validate %s (required_if_true: %s)
	if m.%s && %s {
		return %s(nil, "field %s is required when %s is true")
	}

`, goFieldName, annotationValue.StringValue, condFieldName, zeroCheck, errFunc, goFieldName, condFieldName)

			case "required_if_false":
				condFieldName := toGoFieldName(annotationValue.StringValue)
				errFunc := "protoerrors.NewErrMissingInvalidData"
				zeroCheck := requiredZeroCheck(field, goFieldName)
				fmt.Fprintf(b, `	// Validate %s (required_if_false: %s)
	if !m.%s && %s {
		return %s(nil, "field %s is required when %s is false")
	}

`, goFieldName, annotationValue.StringValue, condFieldName, zeroCheck, errFunc, goFieldName, condFieldName)

			case "required_if_eq":
				errFunc := "protoerrors.NewErrMissingInvalidData"
				zeroCheck := requiredZeroCheck(field, goFieldName)
				parts := strings.SplitN(annotationValue.StringValue, "=", 2)
				if len(parts) == 2 {
					condFieldSnake := parts[0]
					condValue := parts[1]
					condFieldGo := toGoFieldName(condFieldSnake)

					// Find the sibling field to determine if it's an enum
					var siblingField *model.Field
					for i := range msg.Fields {
						if msg.Fields[i].Name == condFieldSnake {
							siblingField = &msg.Fields[i]
							break
						}
					}

					if siblingField != nil && siblingField.IsEnum {
						// Enum comparison: use the enum type prefix + value name
						enumType := siblingField.Type
						if siblingField.TypePackage != "" {
							enumType = siblingField.TypePackage + "." + siblingField.Type
						}
						fmt.Fprintf(b, `	// Validate %s (required_if_eq: %s=%s)
	if m.%s == %s_%s && %s {
		return %s(nil, "field %s is required when %s is %s")
	}

`, goFieldName, condFieldSnake, condValue, condFieldGo, enumType, condValue, zeroCheck, errFunc, goFieldName, condFieldSnake, condValue)
					} else {
						// String comparison
						// condValue is quoted with %q, the way the Java backend
						// quotes it, and escaped where it lands inside the
						// message literal. Parse-time charset validation
						// already rejects breakout characters; this keeps the
						// emitted literal well-formed if that ever loosens.
						fmt.Fprintf(b, `	// Validate %s (required_if_eq: %s=%s)
	if m.%s == %q && %s {
		return %s(nil, "field %s is required when %s is \"%s\"")
	}

`, goFieldName, condFieldSnake, condValue, condFieldGo, condValue, zeroCheck, errFunc, goFieldName, condFieldSnake, goEscapeInStringLiteral(condValue))
					}
				}

			case "assert_max_size":
				// Generate maximum size validation for string/bytes/repeated/map fields
				if annotationValue.Type != model.AnnotationTypeInt {
					return fmt.Errorf("assert_max_size annotation on field %s.%s must be an integer value", msg.Name, goFieldName)
				}

				errFunc := "protoerrors.NewErrMissingInvalidData"

				if field.IsRepeated || field.IsMap {
					fmt.Fprintf(b, `	// Validate %s (assert_max_size: %d)
	if len(m.%s) > %d {
		return %s(nil, "field %s exceeds max size of %d, got: %%d", len(m.%s))
	}

`, goFieldName, annotationValue.IntValue, goFieldName, annotationValue.IntValue, errFunc, goFieldName, annotationValue.IntValue, goFieldName)
				} else if field.IsOptional {
					// Optional fields are pointers (*string) — must nil-check and dereference
					fmt.Fprintf(b, `	// Validate %s (assert_max_size: %d bytes)
	if m.%s != nil && len(*m.%s) > %d {
		return %s(nil, "field %s exceeds max size of %d bytes, got: %%d", len(*m.%s))
	}

`, goFieldName, annotationValue.IntValue, goFieldName, goFieldName, annotationValue.IntValue, errFunc, goFieldName, annotationValue.IntValue, goFieldName)
				} else {
					fmt.Fprintf(b, `	// Validate %s (assert_max_size: %d bytes)
	if len(m.%s) > %d {
		return %s(nil, "field %s exceeds max size of %d bytes, got: %%d", len(m.%s))
	}

`, goFieldName, annotationValue.IntValue, goFieldName, annotationValue.IntValue, errFunc, goFieldName, annotationValue.IntValue, goFieldName)
				}

			case "assert_element_max_size":
				// Per-element byte bound. The emission lives in the per-element
				// loop below, which applies this bound in place of the default
				// one it already enforces on every repeated/map string/bytes
				// element; here we only reject a misapplied annotation.
				if err := field.ValidateElementMaxSize(msg.Name); err != nil {
					return err
				}

			case "assert_min_size":
				// Generate minimum size validation for string/bytes/repeated/map fields
				if annotationValue.Type != model.AnnotationTypeInt {
					return fmt.Errorf("assert_min_size annotation on field %s.%s must be an integer value", msg.Name, goFieldName)
				}

				errFunc := "protoerrors.NewErrMissingInvalidData"

				if field.IsRepeated || field.IsMap {
					fmt.Fprintf(b, `	// Validate %s (assert_min_size: %d)
	if len(m.%s) < %d {
		return %s(nil, "field %s below min size of %d, got: %%d", len(m.%s))
	}

`, goFieldName, annotationValue.IntValue, goFieldName, annotationValue.IntValue, errFunc, goFieldName, annotationValue.IntValue, goFieldName)
				} else if field.IsOptional {
					fmt.Fprintf(b, `	// Validate %s (assert_min_size: %d bytes)
	if m.%s != nil && len(*m.%s) < %d {
		return %s(nil, "field %s below min size of %d bytes, got: %%d", len(*m.%s))
	}

`, goFieldName, annotationValue.IntValue, goFieldName, goFieldName, annotationValue.IntValue, errFunc, goFieldName, annotationValue.IntValue, goFieldName)
				} else {
					fmt.Fprintf(b, `	// Validate %s (assert_min_size: %d bytes)
	if len(m.%s) < %d {
		return %s(nil, "field %s below min size of %d bytes, got: %%d", len(m.%s))
	}

`, goFieldName, annotationValue.IntValue, goFieldName, annotationValue.IntValue, errFunc, goFieldName, annotationValue.IntValue, goFieldName)
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
				"key", "param_key", "json_return_value",
				"store_key", "quorum_write", "collected_health",
				"projection_source", "projection_record_key",
				"query_field", "redact",
				"timeseries_column", "timeseries_order_key",
				"timeseries_partition_key", "timeseries_cardinality_hint",
				"json_key", "json_aliases",
				"cache", "timeseries", "archive",
				"record", "record_key", "record_owner",
				"subscribe",
				"identity_namespace", "identity_subject",
				"identity_spiffe_subject", "identity_spiffe_resource",
				"principal", "store_principal", "namespace", "store_namespace":

				// Non-validation annotations — used by other code generators (statedb, constants, etc.)
				// Silently skip: these do not produce validation code.

			default:
				// An unknown VALIDATION annotation (assert_* or required_if_*) is a hard
				// error: the
				// generator has no template for it, so the declared constraint would
				// be silently dropped (OF-26d). Non-validation annotations are
				// allow-listed above and skipped.
				if strings.HasPrefix(annotationName, "assert_") || strings.HasPrefix(annotationName, "required_if_") {
					return fmt.Errorf("unsupported validation annotation %q on field %s.%s (no Go validation template)", annotationName, msg.Name, goFieldName)
				}
				// Truly unknown non-validation annotation - warn so new annotations get noticed
				zap.L().Warn("Skipping unknown annotation - no validation template available",
					zap.String("annotation", annotationName),
					zap.String("message", msg.Name),
					zap.String("field", goFieldName))
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

		goFieldName := toGoFieldName(field.Name)
		errFunc := "protoerrors.NewErrMissingInvalidData"

		defaultCount := collection.DefaultCollCount

		hasValidations = true
		fmt.Fprintf(b, `	// Validate %s (default max size: %d elements)
	if len(m.%s) > %d {
		return %s(nil, "field %s exceeds max size of %d elements, got: %%d", len(m.%s))
	}

`, goFieldName, defaultCount, goFieldName, defaultCount, errFunc, goFieldName, defaultCount, goFieldName)
	}

	// Default per-element max size enforcement for repeated/map string/bytes values.
	for _, field := range msg.Fields {
		if field.IsOneOf {
			continue
		}

		goFieldName := toGoFieldName(field.Name)
		errFunc := "protoerrors.NewErrMissingInvalidData"
		defaultSize := collection.DefaultScalarBytes

		// An explicit (spec.assert_element_max_size) replaces the default bound
		// for repeated elements and map values; map keys keep the default.
		elemSize, elemLabel := defaultSize, "default max size"
		if bound := field.ElementMaxSizeBytes(); bound > 0 {
			elemSize, elemLabel = bound, "assert_element_max_size"
		}

		if field.IsRepeated && (field.Type == "string" || field.Type == "bytes") {
			hasValidations = true
			fmt.Fprintf(b, `	// Validate %s elements (%s: %d bytes)
	for i, val := range m.%s {
		if len(val) > %d {
			return %s(nil, "field %s[%%d] exceeds max size of %d bytes, got: %%d", i, len(val))
		}
	}

`, goFieldName, elemLabel, elemSize, goFieldName, elemSize, errFunc, goFieldName, elemSize)
		}

		if field.IsMap {
			if field.MapKeyType == "string" {
				hasValidations = true
				fmt.Fprintf(b, `	// Validate %s keys (default max size: %d bytes)
	for key := range m.%s {
		if len(key) > %d {
			return %s(nil, "field %s key exceeds max size of %d bytes, got: %%d", len(key))
		}
	}

`, goFieldName, defaultSize, goFieldName, defaultSize, errFunc, goFieldName, defaultSize)
			}
			if field.MapValueType == "string" || field.MapValueType == "bytes" {
				hasValidations = true
				fmt.Fprintf(b, `	// Validate %s values (%s: %d bytes)
	for key, val := range m.%s {
		if len(val) > %d {
			return %s(nil, "field %s[%%v] exceeds max size of %d bytes, got: %%d", key, len(val))
		}
	}

`, goFieldName, elemLabel, elemSize, goFieldName, elemSize, errFunc, goFieldName, elemSize)
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

		goFieldName := toGoFieldName(field.Name)
		errFunc := "protoerrors.NewErrMissingInvalidData"

		defaultSize := collection.DefaultScalarBytes

		hasValidations = true
		if field.IsOneOf {
			fmt.Fprintf(b, `	// Validate %s (default max size: %d bytes)
	if len(m.Get%s()) > %d {
		return %s(nil, "field %s exceeds max size of %d bytes, got: %%d", len(m.Get%s()))
	}

`, goFieldName, defaultSize, goFieldName, defaultSize, errFunc, goFieldName, defaultSize, goFieldName)
		} else if field.IsOptional {
			// Optional fields are pointers (*string) — must nil-check and dereference
			fmt.Fprintf(b, `	// Validate %s (default max size: %d bytes)
	if m.%s != nil && len(*m.%s) > %d {
		return %s(nil, "field %s exceeds max size of %d bytes, got: %%d", len(*m.%s))
	}

`, goFieldName, defaultSize, goFieldName, goFieldName, defaultSize, errFunc, goFieldName, defaultSize, goFieldName)
		} else {
			fmt.Fprintf(b, `	// Validate %s (default max size: %d bytes)
	if len(m.%s) > %d {
		return %s(nil, "field %s exceeds max size of %d bytes, got: %%d", len(m.%s))
	}

`, goFieldName, defaultSize, goFieldName, defaultSize, errFunc, goFieldName, defaultSize, goFieldName)
		}
	}

	// Validate enum fields against the generated enum vocabulary. This rejects
	// undefined numeric enum values while still allowing the declared zero value
	// unless assert_enum_defined is also present.
	for _, field := range msg.Fields {
		goFieldName := toGoFieldName(field.Name)
		errFunc := "protoerrors.NewErrMissingInvalidData"

		if field.MapValueIsEnum {
			enumMap := enumNameMapRef(field.MapValueType, field.MapValueTypePackage)
			if enumMap == "" {
				continue
			}
			hasValidations = true
			fmt.Fprintf(b, `	// Validate %s enum values are known
	for key, val := range m.%s {
		if _, ok := %s[int32(val)]; !ok {
			return %s(nil, "field %s[%%v] has unknown enum value %%d", key, int32(val))
		}
	}

`, goFieldName, goFieldName, enumMap, errFunc, goFieldName)
			continue
		}

		if !field.IsEnum {
			continue
		}
		enumMap := enumNameMapRef(field.Type, field.TypePackage)
		if enumMap == "" {
			continue
		}
		hasValidations = true
		if field.IsRepeated {
			fmt.Fprintf(b, `	// Validate %s enum values are known
	for i, val := range m.%s {
		if _, ok := %s[int32(val)]; !ok {
			return %s(nil, "field %s[%%d] has unknown enum value %%d", i, int32(val))
		}
	}

`, goFieldName, goFieldName, enumMap, errFunc, goFieldName)
		} else if field.IsOptional {
			fmt.Fprintf(b, `	// Validate %s enum value is known
	if m.%s != nil {
		if _, ok := %s[int32(*m.%s)]; !ok {
			return %s(nil, "field %s has unknown enum value %%d", int32(*m.%s))
		}
	}

`, goFieldName, goFieldName, enumMap, goFieldName, errFunc, goFieldName, goFieldName)
		} else {
			accessor := fmt.Sprintf("m.%s", goFieldName)
			if field.IsOneOf {
				accessor = fmt.Sprintf("m.Get%s()", goFieldName)
			}
			fmt.Fprintf(b, `	// Validate %s enum value is known
	if _, ok := %s[int32(%s)]; !ok {
		return %s(nil, "field %s has unknown enum value %%d", int32(%s))
	}

`, goFieldName, enumMap, accessor, errFunc, goFieldName, accessor)
		}
	}

	// Recursively validate nested generated messages, including oneof variants.
	for _, field := range msg.Fields {
		goFieldName := toGoFieldName(field.Name)
		errFunc := "protoerrors.NewErrMissingInvalidData"

		if field.MapValueIsMessage {
			validateRef := messageValidateRef(field.MapValueType, field.MapValueTypePackage)
			if validateRef == "" {
				continue
			}
			hasValidations = true
			fmt.Fprintf(b, `	// Validate %s message values
	for key, val := range m.%s {
		if err := %s(val, depth+1); err != nil {
			return %s(err, "field %s[%%v] validation failed", key)
		}
	}

`, goFieldName, goFieldName, validateRef, errFunc, goFieldName)
			continue
		}

		if !field.IsMessage {
			continue
		}

		validateRef := messageValidateRef(field.Type, field.TypePackage)
		if validateRef == "" {
			continue
		}

		hasValidations = true
		if field.IsOneOf {
			fmt.Fprintf(b, `	// Validate %s nested message
	if val := m.Get%s(); val != nil {
		if err := %s(val, depth+1); err != nil {
			return %s(err, "field %s validation failed")
		}
	}

`, goFieldName, goFieldName, validateRef, errFunc, goFieldName)
		} else if field.IsRepeated {
			fmt.Fprintf(b, `	// Validate %s nested messages
	for i, val := range m.%s {
		if err := %s(val, depth+1); err != nil {
			return %s(err, "field %s[%%d] validation failed", i)
		}
	}

`, goFieldName, goFieldName, validateRef, errFunc, goFieldName)
		} else {
			fmt.Fprintf(b, `	// Validate %s nested message
	if m.%s != nil {
		if err := %s(m.%s, depth+1); err != nil {
			return %s(err, "field %s validation failed")
		}
	}

`, goFieldName, goFieldName, validateRef, goFieldName, errFunc, goFieldName)
		}
	}

	if !hasValidations {
		b.WriteString("// No field validations defined\n")
	}

	b.WriteString(`	return nil
}

`)

	// Generate helper functions for "in" and "not_in" checks. Sort the keys of
	// each map below — these loops emit top-level func definitions, which gofmt
	// does NOT reorder, so ranging the maps directly byte-diffs the generated
	// helpers file across otherwise-identical runs.
	inKeys := make([]string, 0, len(needsInHelper))
	for k := range needsInHelper {
		inKeys = append(inKeys, k)
	}
	sort.Strings(inKeys)
	for _, key := range inKeys {
		values := needsInHelper[key]
		parts := strings.Split(key, "_")
		msgName := parts[0]
		fieldName := parts[1]
		fmt.Fprintf(b, `// isIn%s%s checks if value is in the allowed list
func isIn%s%s(val int64) bool {
`, msgName, fieldName, msgName, fieldName)
		for _, v := range values {
			fmt.Fprintf(b, "if val == %d { return true }\n", v)
		}
		b.WriteString("return false}\n")
	}

	notInKeys := make([]string, 0, len(needsNotInHelper))
	for k := range needsNotInHelper {
		notInKeys = append(notInKeys, k)
	}
	sort.Strings(notInKeys)
	for _, key := range notInKeys {
		values := needsNotInHelper[key]
		parts := strings.Split(key, "_")
		msgName := parts[0]
		fieldName := parts[1]
		fmt.Fprintf(b, `// isIn%s%sNotIn checks if value is in the forbidden list
func isIn%s%sNotIn(val int64) bool {
`, msgName, fieldName, msgName, fieldName)
		for _, v := range values {
			fmt.Fprintf(b, "if val == %d { return true }\n", v)
		}
		b.WriteString("return false}\n")
	}

	floatInKeys := make([]string, 0, len(needsFloatInHelper))
	for k := range needsFloatInHelper {
		floatInKeys = append(floatInKeys, k)
	}
	sort.Strings(floatInKeys)
	for _, key := range floatInKeys {
		data := needsFloatInHelper[key]
		parts := strings.Split(key, "_")
		msgName := parts[0]
		fieldName := parts[1]
		fmt.Fprintf(b, `// isInFloat%s%s checks if value is in the allowed list (within epsilon)
func isInFloat%s%s(val float64, epsilon float64) bool {
`, msgName, fieldName, msgName, fieldName)
		for _, v := range data.values {
			fmt.Fprintf(b, "if math.Abs(val - %g) < epsilon { return true }\n", v)
		}
		b.WriteString("return false}\n")
	}

	floatNotInKeys := make([]string, 0, len(needsFloatNotInHelper))
	for k := range needsFloatNotInHelper {
		floatNotInKeys = append(floatNotInKeys, k)
	}
	sort.Strings(floatNotInKeys)
	for _, key := range floatNotInKeys {
		data := needsFloatNotInHelper[key]
		parts := strings.Split(key, "_")
		msgName := parts[0]
		fieldName := parts[1]
		fmt.Fprintf(b, `// isInFloat%s%sNotIn checks if value is in the forbidden list (within epsilon)
func isInFloat%s%sNotIn(val float64, epsilon float64) bool {
`, msgName, fieldName, msgName, fieldName)
		for _, v := range data.values {
			fmt.Fprintf(b, "if math.Abs(val - %g) < epsilon { return true }\n", v)
		}
		b.WriteString("return false}\n")
	}

	// Validation functions should only validate the message object itself.
	// Users can read/fetch from any source, deserialize, then call Validate(msg).
	// No need for ValidateFromBytes, ValidateFromFile, etc.

	return nil
}

// goFloatMembership captures one assert_float in/not_in list together with the
// epsilon its comparisons use, so the emitted isInFloat* helper matches the
// call site that registered it.
type goFloatMembership struct {
	values  []float64
	epsilon float64
}

// writeGoComparisonValidation emits one of the simple bound annotations
// (assert_gt / assert_ge / assert_lt / assert_le). failOp is the operator that
// makes the generated `if` fire and wantOp the one quoted back to the caller in
// the error message. spec.proto scopes the family to scalar numeric fields, so
// a map, repeated or non-numeric field is an authoring mistake: emitting the
// comparison anyway produced Go that does not build (`if m.Name < 5` against a
// string, `if m.Ports < 5` against a []int32). Proto3-optional fields render as
// a pointer, so the value has to be dereferenced behind a nil guard the way
// assert_numeric already does.
func writeGoComparisonValidation(b *strings.Builder, msg model.Message, field model.Field, goFieldName, annotation, failOp, wantOp string, bound int64) error {
	if field.IsMap || field.IsRepeated || !(isNumericType(field.Type) || field.IsEnum) {
		return fmt.Errorf("%s annotation on field %s.%s requires a scalar numeric field", annotation, msg.Name, goFieldName)
	}

	errFunc := "protoerrors.NewErrMissingInvalidData"
	valueExpr := "m." + goFieldName
	if field.IsOptional {
		valueExpr = "*m." + goFieldName
	}

	fmt.Fprintf(b, "	// Validate %s (%s: %d)\n", goFieldName, annotation, bound)
	if field.IsOptional {
		fmt.Fprintf(b, "	if m.%s != nil {\n", goFieldName)
	}
	fmt.Fprintf(b, `	if %s %s %d {
		return %s(nil, "field %s must be %s %d, got: %%v", %s)
	}
`, valueExpr, failOp, bound, errFunc, goFieldName, wantOp, bound, valueExpr)
	if field.IsOptional {
		b.WriteString("	}\n")
	}

	return nil
}

// writeGoDurationBoundValidation emits one of the duration/port bound
// annotations (assert_net_port / assert_duration / assert_duration_min /
// assert_duration_max). condFmt is the failing comparison with %[1]s standing
// for the value expression, and want is the phrase quoted back to the caller in
// the error message. All four compare the field against an integer, so a map,
// repeated or non-numeric field is an authoring mistake: emitting the
// comparison anyway produced Go that does not build (`if m.Name < 0` against a
// string, `if m.Ports < 0` against a []int32). Enums are rejected too, because
// java renders this family through the bare getter, which does not compile
// against a protobuf enum. Proto3-optional fields render as a pointer, so the
// value has to be dereferenced behind a nil guard the way assert_numeric
// already does.
func writeGoDurationBoundValidation(b *strings.Builder, msg model.Message, field model.Field, goFieldName, annotation, comment, condFmt, want string) error {
	if field.IsMap || field.IsRepeated || !isNumericType(field.Type) {
		return fmt.Errorf("%s annotation on field %s.%s requires a scalar numeric field", annotation, msg.Name, goFieldName)
	}

	errFunc := "protoerrors.NewErrMissingInvalidData"
	valueExpr := "m." + goFieldName
	if field.IsOptional {
		valueExpr = "*m." + goFieldName
	}

	fmt.Fprintf(b, "	// Validate %s (%s)\n", goFieldName, comment)
	if field.IsOptional {
		fmt.Fprintf(b, "	if m.%s != nil {\n", goFieldName)
	}
	fmt.Fprintf(b, "	if "+condFmt+" {\n", valueExpr)
	fmt.Fprintf(b, "		return %s(nil, \"field %s must be %s, got: %%d\", %s)\n", errFunc, goFieldName, want, valueExpr)
	b.WriteString("	}\n")
	if field.IsOptional {
		b.WriteString("	}\n")
	}
	b.WriteString("\n")

	return nil
}

// writeGoNumericConstraintChecks emits the assert_numeric comparisons for a
// single integer value. valueExpr is the expression holding that value and
// indexVar is the loop index for repeated fields, or "" for scalar and
// optional fields. Nothing is written when the constraint carries no checks,
// which lets the caller skip an otherwise-empty (and uncompilable) loop.
func writeGoNumericConstraintChecks(b *strings.Builder, msgName, goFieldName, valueExpr, indexVar string, constraint *model.NumericConstraint, needsInHelper, needsNotInHelper map[string][]int64) {
	errFunc := "protoerrors.NewErrMissingInvalidData"
	label := goFieldName
	idxArg := ""
	if indexVar != "" {
		label = goFieldName + "[%d]"
		idxArg = indexVar + ", "
	}

	if constraint.Gt != nil {
		fmt.Fprintf(b, "if %s <= %d {\n", valueExpr, *constraint.Gt)
		fmt.Fprintf(b, "return %s(nil, \"field %s must be > %d, got: %%v\", %s%s)\n", errFunc, label, *constraint.Gt, idxArg, valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Ge != nil {
		fmt.Fprintf(b, "if %s < %d {\n", valueExpr, *constraint.Ge)
		fmt.Fprintf(b, "return %s(nil, \"field %s must be >= %d, got: %%v\", %s%s)\n", errFunc, label, *constraint.Ge, idxArg, valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Lt != nil {
		fmt.Fprintf(b, "if %s >= %d {\n", valueExpr, *constraint.Lt)
		fmt.Fprintf(b, "return %s(nil, \"field %s must be < %d, got: %%v\", %s%s)\n", errFunc, label, *constraint.Lt, idxArg, valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Le != nil {
		fmt.Fprintf(b, "if %s > %d {\n", valueExpr, *constraint.Le)
		fmt.Fprintf(b, "return %s(nil, \"field %s must be <= %d, got: %%v\", %s%s)\n", errFunc, label, *constraint.Le, idxArg, valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Eq != nil {
		fmt.Fprintf(b, "if %s != %d {\n", valueExpr, *constraint.Eq)
		fmt.Fprintf(b, "return %s(nil, \"field %s must equal %d, got: %%v\", %s%s)\n", errFunc, label, *constraint.Eq, idxArg, valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Ne != nil {
		fmt.Fprintf(b, "if %s == %d {\n", valueExpr, *constraint.Ne)
		fmt.Fprintf(b, "return %s(nil, \"field %s must not equal %d, got: %%v\", %s%s)\n", errFunc, label, *constraint.Ne, idxArg, valueExpr)
		b.WriteString("}\n")
	}
	if len(constraint.In) > 0 {
		fmt.Fprintf(b, "if !isIn%s%s(int64(%s)) {\n", msgName, goFieldName, valueExpr)
		fmt.Fprintf(b, "return %s(nil, \"field %s must be one of %v, got: %%v\", %s%s)\n", errFunc, label, constraint.In, idxArg, valueExpr)
		b.WriteString("}\n")
		needsInHelper[fmt.Sprintf("%s_%s", msgName, goFieldName)] = constraint.In
	}
	if len(constraint.NotIn) > 0 {
		fmt.Fprintf(b, "if isIn%s%sNotIn(int64(%s)) {\n", msgName, goFieldName, valueExpr)
		fmt.Fprintf(b, "return %s(nil, \"field %s must not be one of %v, got: %%v\", %s%s)\n", errFunc, label, constraint.NotIn, idxArg, valueExpr)
		b.WriteString("}\n")
		needsNotInHelper[fmt.Sprintf("%s_%s", msgName, goFieldName)] = constraint.NotIn
	}
	if constraint.MultipleOf != nil {
		fmt.Fprintf(b, "if %s %% %d != 0 {\n", valueExpr, *constraint.MultipleOf)
		fmt.Fprintf(b, "return %s(nil, \"field %s must be a multiple of %d, got: %%v\", %s%s)\n", errFunc, label, *constraint.MultipleOf, idxArg, valueExpr)
		b.WriteString("}\n")
	}
}

// writeGoFloatConstraintChecks emits the assert_float comparisons for a single
// float value, following the same valueExpr/indexVar contract as
// writeGoNumericConstraintChecks.
func writeGoFloatConstraintChecks(b *strings.Builder, msgName, goFieldName, valueExpr, indexVar string, constraint *model.FloatConstraint, epsilon float64, needsFloatInHelper, needsFloatNotInHelper map[string]goFloatMembership) {
	errFunc := "protoerrors.NewErrMissingInvalidData"
	label := goFieldName
	idxArg := ""
	if indexVar != "" {
		label = goFieldName + "[%d]"
		idxArg = indexVar + ", "
	}

	if constraint.Gt != nil {
		fmt.Fprintf(b, "if %s <= %g {\n", valueExpr, *constraint.Gt)
		fmt.Fprintf(b, "return %s(nil, \"field %s must be > %g, got: %%v\", %s%s)\n", errFunc, label, *constraint.Gt, idxArg, valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Ge != nil {
		fmt.Fprintf(b, "if %s < %g {\n", valueExpr, *constraint.Ge)
		fmt.Fprintf(b, "return %s(nil, \"field %s must be >= %g, got: %%v\", %s%s)\n", errFunc, label, *constraint.Ge, idxArg, valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Lt != nil {
		fmt.Fprintf(b, "if %s >= %g {\n", valueExpr, *constraint.Lt)
		fmt.Fprintf(b, "return %s(nil, \"field %s must be < %g, got: %%v\", %s%s)\n", errFunc, label, *constraint.Lt, idxArg, valueExpr)
		b.WriteString("}\n")
	}
	if constraint.Le != nil {
		fmt.Fprintf(b, "if %s > %g {\n", valueExpr, *constraint.Le)
		fmt.Fprintf(b, "return %s(nil, \"field %s must be <= %g, got: %%v\", %s%s)\n", errFunc, label, *constraint.Le, idxArg, valueExpr)
		b.WriteString("}\n")
	}
	if len(constraint.In) > 0 {
		fmt.Fprintf(b, "if !isInFloat%s%s(float64(%s), %g) {\n", msgName, goFieldName, valueExpr, epsilon)
		fmt.Fprintf(b, "return %s(nil, \"field %s must be one of %v, got: %%v\", %s%s)\n", errFunc, label, constraint.In, idxArg, valueExpr)
		b.WriteString("}\n")
		needsFloatInHelper[fmt.Sprintf("%s_%s", msgName, goFieldName)] = goFloatMembership{constraint.In, epsilon}
	}
	if len(constraint.NotIn) > 0 {
		fmt.Fprintf(b, "if isInFloat%s%sNotIn(float64(%s), %g) {\n", msgName, goFieldName, valueExpr, epsilon)
		fmt.Fprintf(b, "return %s(nil, \"field %s must not be one of %v, got: %%v\", %s%s)\n", errFunc, label, constraint.NotIn, idxArg, valueExpr)
		b.WriteString("}\n")
		needsFloatNotInHelper[fmt.Sprintf("%s_%s", msgName, goFieldName)] = goFloatMembership{constraint.NotIn, epsilon}
	}
}

// generateEncryptionValidationHelpers generates helper functions for encryption validation
func generateEncryptionValidationHelpers(b *strings.Builder) {
	b.WriteString(`
// Encryption validation helper functions

func normalizePEMData(value string) string {
	if strings.Contains(value, "-----BEGIN") && strings.Contains(value, "-----END") {
		return value
	}
	decoded, err := utils.DecodeBase64(value)
	if err != nil {
		return value
	}
	if !bytes.Contains(decoded, []byte("-----BEGIN")) || !bytes.Contains(decoded, []byte("-----END")) {
		return value
	}
	return string(decoded)
}

func validateENC_TYPE_KEY_PRIVATE_RSA(pemData string) error {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return protoerrors.NewErrMissingInvalidData(nil, "failed to decode PEM block")
	}
	if block.Type != "RSA PRIVATE KEY" && block.Type != "PRIVATE KEY" {
		return protoerrors.NewErrMissingInvalidData(nil,"not an RSA private key, got: %s", block.Type)
	}
	if block.Type == "PRIVATE KEY" {
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return protoerrors.NewErrMissingInvalidData(err, "failed to parse private key")
		}
		if _, ok := key.(*rsa.PrivateKey); !ok {
			return protoerrors.NewErrMissingInvalidData(nil,"not an RSA private key")
		}
	} else {
		if _, err := x509.ParsePKCS1PrivateKey(block.Bytes); err != nil {
			return protoerrors.NewErrMissingInvalidData(err, "failed to parse RSA private key")
		}
	}
	return nil
}

func validateENC_TYPE_KEY_PUBLIC_RSA(pemData string) error {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return protoerrors.NewErrMissingInvalidData(nil, "failed to decode PEM block")
	}
	if block.Type != "RSA PUBLIC KEY" && block.Type != "PUBLIC KEY" {
		return protoerrors.NewErrMissingInvalidData(nil,"not an RSA public key, got: %s", block.Type)
	}
	if block.Type == "PUBLIC KEY" {
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return protoerrors.NewErrMissingInvalidData(err, "failed to parse public key")
		}
		if _, ok := key.(*rsa.PublicKey); !ok {
			return protoerrors.NewErrMissingInvalidData(nil,"not an RSA public key")
		}
	} else {
		if _, err := x509.ParsePKCS1PublicKey(block.Bytes); err != nil {
			return protoerrors.NewErrMissingInvalidData(err, "failed to parse RSA public key")
		}
	}
	return nil
}

func validateENC_TYPE_KEY_ANY_RSA(pemData string) error {
	if err := validateENC_TYPE_KEY_PRIVATE_RSA(pemData); err == nil {
		return nil
	}
	return validateENC_TYPE_KEY_PUBLIC_RSA(pemData)
}

func validateENC_TYPE_KEY_PRIVATE_EC(pemData string) error {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return protoerrors.NewErrMissingInvalidData(nil, "failed to decode PEM block")
	}
	if block.Type != "EC PRIVATE KEY" && block.Type != "PRIVATE KEY" {
		return protoerrors.NewErrMissingInvalidData(nil,"not an EC private key, got: %s", block.Type)
	}
	if block.Type == "PRIVATE KEY" {
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return protoerrors.NewErrMissingInvalidData(err, "failed to parse private key")
		}
		if _, ok := key.(*ecdsa.PrivateKey); !ok {
			return protoerrors.NewErrMissingInvalidData(nil,"not an EC private key")
		}
	} else {
		if _, err := x509.ParseECPrivateKey(block.Bytes); err != nil {
			return protoerrors.NewErrMissingInvalidData(err, "failed to parse EC private key")
		}
	}
	return nil
}

func validateENC_TYPE_KEY_PUBLIC_EC(pemData string) error {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return protoerrors.NewErrMissingInvalidData(nil, "failed to decode PEM block")
	}
	if block.Type != "PUBLIC KEY" {
		return protoerrors.NewErrMissingInvalidData(nil,"not a public key, got: %s", block.Type)
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return protoerrors.NewErrMissingInvalidData(err, "failed to parse public key")
	}
	if _, ok := key.(*ecdsa.PublicKey); !ok {
		return protoerrors.NewErrMissingInvalidData(nil,"not an EC public key")
	}
	return nil
}

func validateENC_TYPE_KEY_ANY_EC(pemData string) error {
	if err := validateENC_TYPE_KEY_PRIVATE_EC(pemData); err == nil {
		return nil
	}
	return validateENC_TYPE_KEY_PUBLIC_EC(pemData)
}

func validateENC_TYPE_KEY_PRIVATE_ED25519(pemData string) error {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return protoerrors.NewErrMissingInvalidData(nil, "failed to decode PEM block")
	}
	if block.Type != "PRIVATE KEY" {
		return protoerrors.NewErrMissingInvalidData(nil,"not a private key, got: %s", block.Type)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return protoerrors.NewErrMissingInvalidData(err, "failed to parse private key")
	}
	if _, ok := key.(ed25519.PrivateKey); !ok {
		return protoerrors.NewErrMissingInvalidData(nil,"not an Ed25519 private key")
	}
	return nil
}

func validateENC_TYPE_KEY_PUBLIC_ED25519(pemData string) error {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return protoerrors.NewErrMissingInvalidData(nil, "failed to decode PEM block")
	}
	if block.Type != "PUBLIC KEY" {
		return protoerrors.NewErrMissingInvalidData(nil,"not a public key, got: %s", block.Type)
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return protoerrors.NewErrMissingInvalidData(err, "failed to parse public key")
	}
	if _, ok := key.(ed25519.PublicKey); !ok {
		return protoerrors.NewErrMissingInvalidData(nil,"not an Ed25519 public key")
	}
	return nil
}

func validateENC_TYPE_KEY_ANY_ED25519(pemData string) error {
	if err := validateENC_TYPE_KEY_PRIVATE_ED25519(pemData); err == nil {
		return nil
	}
	return validateENC_TYPE_KEY_PUBLIC_ED25519(pemData)
}

func validateENC_TYPE_KEY_PRIVATE_ANY(pemData string) error {
	if err := validateENC_TYPE_KEY_PRIVATE_RSA(pemData); err == nil {
		return nil
	}
	if err := validateENC_TYPE_KEY_PRIVATE_EC(pemData); err == nil {
		return nil
	}
	return validateENC_TYPE_KEY_PRIVATE_ED25519(pemData)
}

func validateENC_TYPE_KEY_PUBLIC_ANY(pemData string) error {
	if err := validateENC_TYPE_KEY_PUBLIC_RSA(pemData); err == nil {
		return nil
	}
	if err := validateENC_TYPE_KEY_PUBLIC_EC(pemData); err == nil {
		return nil
	}
	return validateENC_TYPE_KEY_PUBLIC_ED25519(pemData)
}

func validateENC_TYPE_KEY_ANY(pemData string) error {
	if err := validateENC_TYPE_KEY_PRIVATE_ANY(pemData); err == nil {
		return nil
	}
	return validateENC_TYPE_KEY_PUBLIC_ANY(pemData)
}

func validateENC_TYPE_X509_CERT_RSA(pemData string) error {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return protoerrors.NewErrMissingInvalidData(nil, "failed to decode PEM block")
	}
	if block.Type != "CERTIFICATE" {
		return protoerrors.NewErrMissingInvalidData(nil,"not a certificate, got: %s", block.Type)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return protoerrors.NewErrMissingInvalidData(err, "failed to parse certificate")
	}
	if _, ok := cert.PublicKey.(*rsa.PublicKey); !ok {
		return protoerrors.NewErrMissingInvalidData(nil,"certificate does not contain an RSA public key")
	}
	return nil
}

func validateENC_TYPE_X509_CERT_EC(pemData string) error {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return protoerrors.NewErrMissingInvalidData(nil, "failed to decode PEM block")
	}
	if block.Type != "CERTIFICATE" {
		return protoerrors.NewErrMissingInvalidData(nil,"not a certificate, got: %s", block.Type)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return protoerrors.NewErrMissingInvalidData(err, "failed to parse certificate")
	}
	if _, ok := cert.PublicKey.(*ecdsa.PublicKey); !ok {
		return protoerrors.NewErrMissingInvalidData(nil,"certificate does not contain an EC public key")
	}
	return nil
}

func validateENC_TYPE_X509_CERT_ED25519(pemData string) error {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return protoerrors.NewErrMissingInvalidData(nil, "failed to decode PEM block")
	}
	if block.Type != "CERTIFICATE" {
		return protoerrors.NewErrMissingInvalidData(nil,"not a certificate, got: %s", block.Type)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return protoerrors.NewErrMissingInvalidData(err, "failed to parse certificate")
	}
	if _, ok := cert.PublicKey.(ed25519.PublicKey); !ok {
		return protoerrors.NewErrMissingInvalidData(nil,"certificate does not contain an Ed25519 public key")
	}
	return nil
}

func validateENC_TYPE_X509_CERT_ANY(pemData string) error {
	block, _ := pem.Decode([]byte(pemData))
	if block == nil {
		return protoerrors.NewErrMissingInvalidData(nil, "failed to decode PEM block")
	}
	if block.Type != "CERTIFICATE" {
		return protoerrors.NewErrMissingInvalidData(nil,"not a certificate, got: %s", block.Type)
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return protoerrors.NewErrMissingInvalidData(err, "failed to parse certificate")
	}
	return nil
}
`)
}

// generateFilePathValidationHelpers generates helper functions for file path validation
func generateFilePathValidationHelpers(b *strings.Builder) {
	b.WriteString("// File path validation helpers\n")
	b.WriteString("func validateFilePathPATH_ABSOLUTE(path string) error {\n")
	b.WriteString("if !filepath.IsAbs(path) {\n")
	b.WriteString("return protoerrors.NewErrMissingInvalidData(nil,\"path must be absolute, got: %s\", path)\n")
	b.WriteString("}\n")
	b.WriteString("return nil\n")
	b.WriteString("}\n")

	b.WriteString("func validateFilePathPATH_RELATIVE(path string) error {\n")
	b.WriteString("if filepath.IsAbs(path) {\n")
	b.WriteString("return protoerrors.NewErrMissingInvalidData(nil,\"path must be relative, got: %s\", path)\n")
	b.WriteString("}\n")
	b.WriteString("return nil\n")
	b.WriteString("}\n")

	b.WriteString("func validateFilePathPATH_ANY(path string) error {\n")
	b.WriteString("// Any path is valid as long as it's not empty\n")
	b.WriteString("if path == \"\" {\n")
	b.WriteString("return protoerrors.NewErrMissingInvalidData(nil,\"path cannot be empty\")\n")
	b.WriteString("}\n")
	b.WriteString("return nil\n")
	b.WriteString("}\n")

	b.WriteString("\nfunc validateFilePathPATH_DIR(path string) error {\n")
	b.WriteString("// Directory path — must not be empty\n")
	b.WriteString("if path == \"\" {\n")
	b.WriteString("return protoerrors.NewErrMissingInvalidData(nil,\"directory path cannot be empty\")\n")
	b.WriteString("}\n")
	b.WriteString("return nil\n")
	b.WriteString("}\n")

	// Alias for numeric enum value fallback (PATH_DIR = 4)
	b.WriteString("\nvar validateFilePath4 = validateFilePathPATH_DIR\n")
}

// goEscapeInStringLiteral escapes s for placement INSIDE an already-open Go
// string literal in the emitted source, where %q cannot be used because the
// surrounding quotes are part of the emitted message text.
func goEscapeInStringLiteral(s string) string {
	q := strconv.Quote(s)
	return q[1 : len(q)-1]
}
