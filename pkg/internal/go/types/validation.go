// Copyright (c) 2026 Second Star Cavalier LLC

// This file is the Go validation emitter: it generates the per-type
// Validate<Type> functions (assert_* constraint checks, default
// collection/scalar size bounds, oneof scalar bounds, the nesting-depth
// guard, and enum vocabulary checks) plus the inline encryption and
// file-path validation helper functions.

package types

import (
	"fmt"
	"sort"
	"strings"

	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/collection"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
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
	needsFloatInHelper := make(map[string]struct {
		values  []float64
		epsilon float64
	})
	needsFloatNotInHelper := make(map[string]struct {
		values  []float64
		epsilon float64
	})
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
				errFunc := "protoerrors.NewErrMissingInvalidData"

				fmt.Fprintf(b, `	// Validate %s (assert_net_port)
	if m.%s < 0 || m.%s > 65535 {
		return %s(nil, "field %s must be a valid port number (0-65535), got: %%d", m.%s)
	}

`, goFieldName, goFieldName, goFieldName, errFunc, goFieldName, goFieldName)

			case "assert_url":
				// Generate URL validation
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

				errFunc := "protoerrors.NewErrMissingInvalidData"

				switch annotationValue.EnumValue {
				case "DURATION_TYPE_POSITIVE":
					fmt.Fprintf(b, `	// Validate %s (assert_duration: POSITIVE)
	if m.%s <= 0 {
		return %s(nil, "field %s must be positive (> 0), got: %%d", m.%s)
	}

`, goFieldName, goFieldName, errFunc, goFieldName, goFieldName)
				case "DURATION_TYPE_NON_NEGATIVE":
					fmt.Fprintf(b, `	// Validate %s (assert_duration: NON_NEGATIVE)
	if m.%s < 0 {
		return %s(nil, "field %s must be non-negative (>= 0), got: %%d", m.%s)
	}

`, goFieldName, goFieldName, errFunc, goFieldName, goFieldName)
				}

			case "assert_duration_min":
				// Generate minimum duration validation
				if annotationValue.Type != model.AnnotationTypeInt {
					return fmt.Errorf("assert_duration_min annotation on field %s.%s must be an integer value", msg.Name, goFieldName)
				}

				errFunc := "protoerrors.NewErrMissingInvalidData"

				fmt.Fprintf(b, `	// Validate %s (assert_duration_min: %d)
	if m.%s < %d {
		return %s(nil, "field %s must be >= %d, got: %%d", m.%s)
	}

`, goFieldName, annotationValue.IntValue, goFieldName, annotationValue.IntValue, errFunc, goFieldName, annotationValue.IntValue, goFieldName)

			case "assert_duration_max":
				// Generate maximum duration validation
				if annotationValue.Type != model.AnnotationTypeInt {
					return fmt.Errorf("assert_duration_max annotation on field %s.%s must be an integer value", msg.Name, goFieldName)
				}

				errFunc := "protoerrors.NewErrMissingInvalidData"

				fmt.Fprintf(b, `	// Validate %s (assert_duration_max: %d)
	if m.%s > %d {
		return %s(nil, "field %s must be <= %d, got: %%d", m.%s)
	}

`, goFieldName, annotationValue.IntValue, goFieldName, annotationValue.IntValue, errFunc, goFieldName, annotationValue.IntValue, goFieldName)

			case "assert_listen_addr":
				// Generate listen address validation (host:port format)
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
				errFunc := "protoerrors.NewErrMissingInvalidData"

				fmt.Fprintf(b, `	// Validate %s (assert_gt: %d)
	if m.%s <= %d {
		return %s(nil, "field %s must be > %d, got: %%v", m.%s)
	}
`, goFieldName, annotationValue.IntValue, goFieldName, annotationValue.IntValue, errFunc, goFieldName, annotationValue.IntValue, goFieldName)

			case "assert_ge":
				// Simple greater than or equal comparison
				errFunc := "protoerrors.NewErrMissingInvalidData"

				fmt.Fprintf(b, `	// Validate %s (assert_ge: %d)
	if m.%s < %d {
		return %s(nil, "field %s must be >= %d, got: %%v", m.%s)
	}
`, goFieldName, annotationValue.IntValue, goFieldName, annotationValue.IntValue, errFunc, goFieldName, annotationValue.IntValue, goFieldName)

			case "assert_lt":
				// Simple less than comparison
				errFunc := "protoerrors.NewErrMissingInvalidData"

				fmt.Fprintf(b, `	// Validate %s (assert_lt: %d)
	if m.%s >= %d {
		return %s(nil, "field %s must be < %d, got: %%v", m.%s)
	}
`, goFieldName, annotationValue.IntValue, goFieldName, annotationValue.IntValue, errFunc, goFieldName, annotationValue.IntValue, goFieldName)

			case "assert_le":
				// Simple less than or equal comparison
				errFunc := "protoerrors.NewErrMissingInvalidData"

				fmt.Fprintf(b, `	// Validate %s (assert_le: %d)
	if m.%s > %d {
		return %s(nil, "field %s must be <= %d, got: %%v", m.%s)
	}
`, goFieldName, annotationValue.IntValue, goFieldName, annotationValue.IntValue, errFunc, goFieldName, annotationValue.IntValue, goFieldName)

			case "assert_numeric":
				// Complex numeric constraint
				errFunc := "protoerrors.NewErrMissingInvalidData"

				if annotationValue.NumericConstraint != nil {
					fmt.Fprintf(b, "// Validate %s (assert_numeric)\n", goFieldName)
					constraint := annotationValue.NumericConstraint

					if constraint.Gt != nil {
						fmt.Fprintf(b, "if m.%s <= %d {\n", goFieldName, *constraint.Gt)
						fmt.Fprintf(b, "return %s(nil, \"field %s must be > %d, got: %%v\", m.%s)\n", errFunc, goFieldName, *constraint.Gt, goFieldName)
						b.WriteString("}\n")
					}
					if constraint.Ge != nil {
						fmt.Fprintf(b, "if m.%s < %d {\n", goFieldName, *constraint.Ge)
						fmt.Fprintf(b, "return %s(nil, \"field %s must be >= %d, got: %%v\", m.%s)\n", errFunc, goFieldName, *constraint.Ge, goFieldName)
						b.WriteString("}\n")
					}
					if constraint.Lt != nil {
						fmt.Fprintf(b, "if m.%s >= %d {\n", goFieldName, *constraint.Lt)
						fmt.Fprintf(b, "return %s(nil, \"field %s must be < %d, got: %%v\", m.%s)\n", errFunc, goFieldName, *constraint.Lt, goFieldName)
						b.WriteString("}\n")
					}
					if constraint.Le != nil {
						fmt.Fprintf(b, "if m.%s > %d {\n", goFieldName, *constraint.Le)
						fmt.Fprintf(b, "return %s(nil, \"field %s must be <= %d, got: %%v\", m.%s)\n", errFunc, goFieldName, *constraint.Le, goFieldName)
						b.WriteString("}\n")
					}
					if constraint.Eq != nil {
						fmt.Fprintf(b, "if m.%s != %d {\n", goFieldName, *constraint.Eq)
						fmt.Fprintf(b, "return %s(nil, \"field %s must equal %d, got: %%v\", m.%s)\n", errFunc, goFieldName, *constraint.Eq, goFieldName)
						b.WriteString("}\n")
					}
					if constraint.Ne != nil {
						fmt.Fprintf(b, "if m.%s == %d {\n", goFieldName, *constraint.Ne)
						fmt.Fprintf(b, "return %s(nil, \"field %s must not equal %d, got: %%v\", m.%s)\n", errFunc, goFieldName, *constraint.Ne, goFieldName)
						b.WriteString("}\n")
					}
					if len(constraint.In) > 0 {
						fmt.Fprintf(b, "if !isIn%s%s(m.%s) {\n", msg.Name, goFieldName, goFieldName)
						fmt.Fprintf(b, "return %s(nil, \"field %s must be one of %v, got: %%v\", m.%s)\n", errFunc, goFieldName, constraint.In, goFieldName)
						b.WriteString("}\n")
						needsInHelper[fmt.Sprintf("%s_%s", msg.Name, goFieldName)] = constraint.In
					}
					if len(constraint.NotIn) > 0 {
						fmt.Fprintf(b, "if isIn%s%sNotIn(m.%s) {\n", msg.Name, goFieldName, goFieldName)
						fmt.Fprintf(b, "return %s(nil, \"field %s must not be one of %v, got: %%v\", m.%s)\n", errFunc, goFieldName, constraint.NotIn, goFieldName)
						b.WriteString("}\n")
						needsNotInHelper[fmt.Sprintf("%s_%s", msg.Name, goFieldName)] = constraint.NotIn
					}
					if constraint.MultipleOf != nil {
						fmt.Fprintf(b, "if m.%s %% %d != 0 {\n", goFieldName, *constraint.MultipleOf)
						fmt.Fprintf(b, "return %s(nil, \"field %s must be a multiple of %d, got: %%v\", m.%s)\n", errFunc, goFieldName, *constraint.MultipleOf, goFieldName)
						b.WriteString("}\n")
					}
				}

			case "assert_float":
				// Complex float constraint
				errFunc := "protoerrors.NewErrMissingInvalidData"

				if annotationValue.FloatConstraint != nil {
					fmt.Fprintf(b, "// Validate %s (assert_float)\n", goFieldName)
					constraint := annotationValue.FloatConstraint
					epsilon := 1e-9
					if constraint.Epsilon != nil {
						epsilon = *constraint.Epsilon
					}

					// Determine field accessor based on whether it's optional
					var fieldAccessor string
					if field.IsOptional {
						fieldAccessor = fmt.Sprintf("*m.%s", goFieldName)
						fmt.Fprintf(b, "if m.%s != nil {\n", goFieldName)
					} else {
						fieldAccessor = fmt.Sprintf("m.%s", goFieldName)
					}

					if constraint.Gt != nil {
						fmt.Fprintf(b, "if %s <= %g {\n", fieldAccessor, *constraint.Gt)
						fmt.Fprintf(b, "return %s(nil, \"field %s must be > %g, got: %%v\", %s)\n", errFunc, goFieldName, *constraint.Gt, fieldAccessor)
						b.WriteString("}\n")
					}
					if constraint.Ge != nil {
						fmt.Fprintf(b, "if %s < %g {\n", fieldAccessor, *constraint.Ge)
						fmt.Fprintf(b, "return %s(nil, \"field %s must be >= %g, got: %%v\", %s)\n", errFunc, goFieldName, *constraint.Ge, fieldAccessor)
						b.WriteString("}\n")
					}
					if constraint.Lt != nil {
						fmt.Fprintf(b, "if %s >= %g {\n", fieldAccessor, *constraint.Lt)
						fmt.Fprintf(b, "return %s(nil, \"field %s must be < %g, got: %%v\", %s)\n", errFunc, goFieldName, *constraint.Lt, fieldAccessor)
						b.WriteString("}\n")
					}
					if constraint.Le != nil {
						fmt.Fprintf(b, "if %s > %g {\n", fieldAccessor, *constraint.Le)
						fmt.Fprintf(b, "return %s(nil, \"field %s must be <= %g, got: %%v\", %s)\n", errFunc, goFieldName, *constraint.Le, fieldAccessor)
						b.WriteString("}\n")
					}
					if len(constraint.In) > 0 {
						fmt.Fprintf(b, "if !isInFloat%s%s(%s, %g) {\n", msg.Name, goFieldName, fieldAccessor, epsilon)
						fmt.Fprintf(b, "return %s(nil, \"field %s must be one of %v, got: %%v\", %s)\n", errFunc, goFieldName, constraint.In, fieldAccessor)
						b.WriteString("}\n")
						needsFloatInHelper[fmt.Sprintf("%s_%s", msg.Name, goFieldName)] = struct {
							values  []float64
							epsilon float64
						}{constraint.In, epsilon}
					}
					if len(constraint.NotIn) > 0 {
						fmt.Fprintf(b, "if isInFloat%s%sNotIn(%s, %g) {\n", msg.Name, goFieldName, fieldAccessor, epsilon)
						fmt.Fprintf(b, "return %s(nil, \"field %s must not be one of %v, got: %%v\", %s)\n", errFunc, goFieldName, constraint.NotIn, fieldAccessor)
						b.WriteString("}\n")
						needsFloatNotInHelper[fmt.Sprintf("%s_%s", msg.Name, goFieldName)] = struct {
							values  []float64
							epsilon float64
						}{constraint.NotIn, epsilon}
					}

					// Close the nil check for optional fields
					if field.IsOptional {
						b.WriteString("}\n")
					}
				}

			case "assert_semver":
				// Generate semantic version validation
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
						fmt.Fprintf(b, `	// Validate %s (required_if_eq: %s=%s)
	if m.%s == "%s" && %s {
		return %s(nil, "field %s is required when %s is \"%s\"")
	}

`, goFieldName, condFieldSnake, condValue, condFieldGo, condValue, zeroCheck, errFunc, goFieldName, condFieldSnake, condValue)
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

		if field.IsRepeated && (field.Type == "string" || field.Type == "bytes") {
			hasValidations = true
			fmt.Fprintf(b, `	// Validate %s elements (default max size: %d bytes)
	for i, val := range m.%s {
		if len(val) > %d {
			return %s(nil, "field %s[%%d] exceeds max size of %d bytes, got: %%d", i, len(val))
		}
	}

`, goFieldName, defaultSize, goFieldName, defaultSize, errFunc, goFieldName, defaultSize)
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
				fmt.Fprintf(b, `	// Validate %s values (default max size: %d bytes)
	for key, val := range m.%s {
		if len(val) > %d {
			return %s(nil, "field %s[%%v] exceeds max size of %d bytes, got: %%d", key, len(val))
		}
	}

`, goFieldName, defaultSize, goFieldName, defaultSize, errFunc, goFieldName, defaultSize)
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
