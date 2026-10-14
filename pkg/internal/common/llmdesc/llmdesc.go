// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

// Package llmdesc provides shared logic for building type descriptors
// from parsed proto model types. Used by all language generators to produce
// JSON schema descriptions and registry metadata for proto messages.
package llmdesc

import (
	"fmt"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/collection"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// FieldDescriptor describes a single field for LLM consumption.
type FieldDescriptor struct {
	Name             string                 `json:"name"`
	Type             string                 `json:"type"`
	Description      string                 `json:"description,omitempty"`
	Required         bool                   `json:"required,omitempty"`
	Repeated         bool                   `json:"repeated,omitempty"`
	OneOf            bool                   `json:"oneof,omitempty"`
	OneOfName        string                 `json:"oneof_name,omitempty"`
	StoreKey         bool                   `json:"store_key,omitempty"`
	QuorumWrite      bool                   `json:"quorum_write,omitempty"`
	Default          any                    `json:"default,omitempty"`
	Constraints      []string               `json:"constraints,omitempty"`
	ConstraintInfos  []ConstraintDescriptor `json:"constraint_infos,omitempty"`
	Message          *TypeDescriptor        `json:"message,omitempty"`
	EnumValues       []EnumValue            `json:"enum_values,omitempty"`
	MapKey           string                 `json:"map_key_type,omitempty"`
	MapValue         string                 `json:"map_value_type,omitempty"`
	ProjectionSource string                 `json:"projection_source,omitempty"`
	// DataEntitlementMode is the field's own (spec.redact) mode string
	// ("drop"/"pseudonymize"/"may_redact"/"may_anonymize"), "" if unannotated.
	DataEntitlementMode string `json:"data_entitlement_mode,omitempty"`
	// FilterMode is the field's own (spec.query_field) op string ("eq"/"range"),
	// "" if unannotated (not filterable — the field is absent from <T>Filter).
	FilterMode string `json:"filter_mode,omitempty"`
	// Store-boundary role markers. A registry consumer that needs to know which
	// field is the record's principal, namespace or key reads these rather than
	// matching field NAMES — a service that spells its principal field anything
	// but "recipient" is otherwise invisible to a name-matching heuristic, and a
	// boundary that depends on spelling is not a boundary.
	StorePrincipal bool `json:"store_principal,omitempty"` // field carries (spec.store_principal)
	StoreNamespace bool `json:"store_namespace,omitempty"` // field carries (spec.store_namespace)
	RecordKey      bool `json:"record_key,omitempty"`      // field is named by (spec.record).key
	// Timeseries column annotations (store-neutral)
	Timeseries             bool  `json:"timeseries,omitempty"`                  // true if field has (spec.timeseries_column)
	TimeseriesOrderKey     int   `json:"timeseries_order_key,omitempty"`        // store-neutral sort-key position (1-indexed)
	TimeseriesPartitionKey bool  `json:"timeseries_partition_key,omitempty"`    // store-neutral partition role
	TimeseriesCardinality  int32 `json:"timeseries_cardinality_hint,omitempty"` // store-neutral distinct-value hint (0/1/2)
}

// ConstraintDescriptor is the machine-readable form of a field validation rule.
type ConstraintDescriptor struct {
	Rule        string    `json:"rule"`
	Target      string    `json:"target,omitempty"`
	ValueKind   string    `json:"value_kind,omitempty"`
	BoolValue   *bool     `json:"bool_value,omitempty"`
	IntValue    *int64    `json:"int_value,omitempty"`
	FloatValue  *float64  `json:"float_value,omitempty"`
	StringValue string    `json:"string_value,omitempty"`
	EnumValue   string    `json:"enum_value,omitempty"`
	IntValues   []int64   `json:"int_values,omitempty"`
	FloatValues []float64 `json:"float_values,omitempty"`
	Operator    string    `json:"operator,omitempty"`
	Defaulted   bool      `json:"defaulted,omitempty"`
	Unit        string    `json:"unit,omitempty"`
	Source      string    `json:"source,omitempty"`
}

// EnumValue describes a single enum value.
type EnumValue struct {
	Name        string `json:"name"`
	Number      int    `json:"number"`
	Description string `json:"description,omitempty"`
}

// TypeDescriptor describes a message type for LLM consumption.
type TypeDescriptor struct {
	Name        string            `json:"name"`
	Package     string            `json:"package,omitempty"`
	Description string            `json:"description,omitempty"`
	Fields      []FieldDescriptor `json:"fields"`
	// DataEntitlementDefault is the message-level (spec.data_entitlement) default
	// mode string, "" if none.
	DataEntitlementDefault string `json:"data_entitlement_default,omitempty"`
}

// MessageRef maps "package.MessageName" -> model.Message + its package.
type MessageRef struct {
	Msg     model.Message
	Package string
}

// BuildMessageIndex builds a global message index keyed by "package.MessageName".
func BuildMessageIndex(allFiles []model.ProtoFile) map[string]MessageRef {
	idx := make(map[string]MessageRef)
	for _, pf := range allFiles {
		for _, msg := range pf.Messages {
			key := pf.Package + "." + msg.Name
			idx[key] = MessageRef{Msg: msg, Package: pf.Package}
		}
	}
	return idx
}

// BuildEnumIndex builds a global enum index keyed by "package.EnumName".
func BuildEnumIndex(allFiles []model.ProtoFile) map[string]model.Enum {
	idx := make(map[string]model.Enum)
	for _, pf := range allFiles {
		for _, enum := range pf.Enums {
			key := pf.Package + "." + enum.Name
			idx[key] = enum
		}
	}
	return idx
}

// BuildTypeDescriptor builds a TypeDescriptor recursively for a message.
func BuildTypeDescriptor(msg model.Message, pkg string, msgIdx map[string]MessageRef, enumIdx map[string]model.Enum, visited map[string]bool) TypeDescriptor {
	fqn := pkg + "." + msg.Name
	desc := TypeDescriptor{
		Name:        msg.Name,
		Package:     pkg,
		Description: msg.Comment,
		Fields:      make([]FieldDescriptor, 0, len(msg.Fields)),
	}
	// Message-level data-entitlement default (spec.data_entitlement).
	if ann, ok := msg.Annotations["data_entitlement"]; ok {
		desc.DataEntitlementDefault = ann.StringValue
	}

	// Mark as visited to prevent infinite recursion on self-referencing types
	visited[fqn] = true

	for _, field := range msg.Fields {
		fd := FieldDescriptor{
			Name:        field.Name,
			Description: field.Comment,
			Repeated:    field.IsRepeated,
			OneOf:       field.IsOneOf,
			OneOfName:   field.OneOfName,
		}
		if ann, ok := field.Annotations["store_key"]; ok && ann.BoolValue {
			fd.StoreKey = true
		}
		if ann, ok := field.Annotations["quorum_write"]; ok && ann.BoolValue {
			fd.QuorumWrite = true
		}
		// Store-boundary role markers. store_principal/store_namespace are the
		// field's own bool annotations; the record key is named by the MESSAGE's
		// (spec.record).key, so it is resolved against msg, not field.
		if ann, ok := field.Annotations["store_principal"]; ok && ann.BoolValue {
			fd.StorePrincipal = true
		}
		if ann, ok := field.Annotations["store_namespace"]; ok && ann.BoolValue {
			fd.StoreNamespace = true
		}
		if ann, ok := msg.Annotations["record_key"]; ok && ann.StringValue == field.Name {
			fd.RecordKey = true
		}

		// Determine type string
		if field.IsMap {
			fd.Type = "map"
			fd.MapKey = field.MapKeyType
			fd.MapValue = field.MapValueType
		} else if field.IsEnum {
			fd.Type = "enum"
			// Resolve enum values
			enumFQN := ResolveTypeFQN(field.Type, field.TypePackage, pkg)
			if e, ok := enumIdx[enumFQN]; ok {
				for _, v := range e.Values {
					if model.IsEnumSentinel(v.Name) {
						continue
					}
					fd.EnumValues = append(fd.EnumValues, EnumValue{
						Name:        v.Name,
						Number:      v.Number,
						Description: v.Comment,
					})
				}
			}
		} else if field.IsMessage {
			fd.Type = "message"
			// Recursively describe the nested message if not already visited
			msgFQN := ResolveTypeFQN(field.Type, field.TypePackage, pkg)
			if !visited[msgFQN] {
				if ref, ok := msgIdx[msgFQN]; ok {
					nested := BuildTypeDescriptor(ref.Msg, ref.Package, msgIdx, enumIdx, visited)
					fd.Message = &nested
				}
			} else {
				fd.Type = "message (recursive reference: " + msgFQN + ")"
			}
		} else {
			fd.Type = field.Type
		}

		// Extract constraints from annotations
		fd.Required, fd.Default, fd.Constraints = ExtractFieldConstraints(field)
		fd.ConstraintInfos = ExtractFieldConstraintInfos(field)

		// Extract projection annotations
		if field.Annotations != nil {
			if ann, ok := field.Annotations["projection_source"]; ok && ann.StringValue != "" {
				fd.ProjectionSource = ann.StringValue
			}
			// Data-entitlement field mode (spec.redact). Recorded whenever the
			// annotation is present so the registry carries the per-field mode and
			// the drift guard can detect a proto/registry mismatch.
			if ann, ok := field.Annotations["redact"]; ok {
				fd.DataEntitlementMode = ann.StringValue
			}
			// RecordMap filterability (spec.query_field). The descriptor parser
			// stamps "eq"/"range" — the same op the record-map injector builds its
			// recordFilter from — so the registry carries the filter shape a
			// consumer would otherwise have to read off the generated <T>Filter.
			if ann, ok := field.Annotations["query_field"]; ok && ann.StringValue != "" {
				fd.FilterMode = ann.StringValue
			}
			// Timeseries field annotations (store-neutral)
			if _, ok := field.Annotations["timeseries_column"]; ok {
				fd.Timeseries = true
			}
			if ann, ok := field.Annotations["timeseries_order_key"]; ok && ann.IntValue > 0 {
				fd.TimeseriesOrderKey = int(ann.IntValue)
			}
			if ann, ok := field.Annotations["timeseries_partition_key"]; ok && ann.BoolValue {
				fd.TimeseriesPartitionKey = true
			}
			if ann, ok := field.Annotations["timeseries_cardinality_hint"]; ok && ann.IntValue > 0 {
				fd.TimeseriesCardinality = int32(ann.IntValue)
			}
		}

		desc.Fields = append(desc.Fields, fd)
	}

	return desc
}

// ResolveTypeFQN resolves a type name to its fully qualified name.
func ResolveTypeFQN(typeName, typePackage, currentPackage string) string {
	if typePackage != "" {
		return typePackage + "." + typeName
	}
	return currentPackage + "." + typeName
}

// ExtractFieldConstraints extracts constraint info from field annotations
// into a form useful for LLM prompts.
func ExtractFieldConstraints(field model.Field) (required bool, defaultVal any, constraints []string) {
	hasExplicitMaxSize := false
	if field.Annotations != nil {
		for _, name := range model.SortedAnnotationNames(field.Annotations) {
			ann := field.Annotations[name]
			switch name {
			case "assert_required":
				if ann.BoolValue {
					required = true
				}
			case "default_string":
				defaultVal = ann.StringValue
			case "default_int":
				defaultVal = ann.IntValue
			case "default_float":
				defaultVal = ann.FloatValue
			case "default_bool":
				defaultVal = ann.BoolValue
			case "assert_max_size":
				if ann.Type == model.AnnotationTypeInt {
					hasExplicitMaxSize = true
					constraints = append(constraints, maxSizeConstraint(field, ann.IntValue, false))
				}
			case "assert_url":
				constraints = append(constraints, "must be a valid URL (scheme: "+ann.EnumValue+")")
			case "assert_email":
				if ann.BoolValue {
					constraints = append(constraints, "must be a valid email address")
				}
			case "assert_fqdn":
				if ann.BoolValue {
					constraints = append(constraints, "must be a fully qualified domain name")
				}
			case "assert_net_port":
				if ann.BoolValue {
					constraints = append(constraints, "must be a valid network port (1-65535)")
				}
			case "assert_listen_addr":
				if ann.BoolValue {
					constraints = append(constraints, "must be a valid listen address (host:port)")
				}
			case "assert_base64":
				if ann.BoolValue {
					constraints = append(constraints, "must be base64 encoded")
				}
			case "assert_semver":
				if ann.BoolValue {
					constraints = append(constraints, "must be a valid semantic version")
				}
			case "assert_host":
				if ann.BoolValue {
					constraints = append(constraints, "must be a valid hostname or IP address")
				}
			case "assert_ipaddr_type":
				constraints = append(constraints, "must be an IP address of type: "+ann.EnumValue)
			case "assert_duration":
				constraints = append(constraints, "duration constraint: "+ann.EnumValue)
			case "assert_duration_min":
				constraints = append(constraints, fmt.Sprintf("minimum value: %d", ann.IntValue))
			case "assert_duration_max":
				constraints = append(constraints, fmt.Sprintf("maximum value: %d", ann.IntValue))
			case "assert_min_size":
				constraints = append(constraints, fmt.Sprintf("minimum size: %d", ann.IntValue))
			case "assert_gt":
				constraints = append(constraints, fmt.Sprintf("must be > %d", ann.IntValue))
			case "assert_ge":
				constraints = append(constraints, fmt.Sprintf("must be >= %d", ann.IntValue))
			case "assert_lt":
				constraints = append(constraints, fmt.Sprintf("must be < %d", ann.IntValue))
			case "assert_le":
				constraints = append(constraints, fmt.Sprintf("must be <= %d", ann.IntValue))
			case "assert_file_path":
				constraints = append(constraints, "must be a valid file path (type: "+ann.EnumValue+")")
			case "assert_enc_type":
				constraints = append(constraints, "encoding type: "+ann.EnumValue)
			case "assert_sql":
				constraints = append(constraints, "must be valid SQL (permit: "+ann.EnumValue+")")
			case "required_if_true":
				constraints = append(constraints, "required if "+ann.StringValue+" is true")
			case "required_if_false":
				constraints = append(constraints, "required if "+ann.StringValue+" is false")
			case "required_if_eq":
				constraints = append(constraints, "required if "+ann.StringValue)
			case "assert_numeric":
				if ann.NumericConstraint != nil {
					nc := ann.NumericConstraint
					if nc.Gt != nil {
						constraints = append(constraints, fmt.Sprintf("must be > %d", *nc.Gt))
					}
					if nc.Ge != nil {
						constraints = append(constraints, fmt.Sprintf("must be >= %d", *nc.Ge))
					}
					if nc.Lt != nil {
						constraints = append(constraints, fmt.Sprintf("must be < %d", *nc.Lt))
					}
					if nc.Le != nil {
						constraints = append(constraints, fmt.Sprintf("must be <= %d", *nc.Le))
					}
					if nc.Eq != nil {
						constraints = append(constraints, fmt.Sprintf("must be == %d", *nc.Eq))
					}
					if nc.Ne != nil {
						constraints = append(constraints, fmt.Sprintf("must be != %d", *nc.Ne))
					}
					if nc.MultipleOf != nil {
						constraints = append(constraints, fmt.Sprintf("must be a multiple of %d", *nc.MultipleOf))
					}
				}
			case "assert_float":
				if ann.FloatConstraint != nil {
					fc := ann.FloatConstraint
					if fc.Gt != nil {
						constraints = append(constraints, fmt.Sprintf("must be > %f", *fc.Gt))
					}
					if fc.Ge != nil {
						constraints = append(constraints, fmt.Sprintf("must be >= %f", *fc.Ge))
					}
					if fc.Lt != nil {
						constraints = append(constraints, fmt.Sprintf("must be < %f", *fc.Lt))
					}
					if fc.Le != nil {
						constraints = append(constraints, fmt.Sprintf("must be <= %f", *fc.Le))
					}
				}
			}
		}
	}

	constraints = appendDefaultBoundConstraints(constraints, field, hasExplicitMaxSize)
	return
}

// ExtractFieldConstraintInfos extracts machine-readable constraint metadata from field annotations.
func ExtractFieldConstraintInfos(field model.Field) []ConstraintDescriptor {
	var constraints []ConstraintDescriptor
	hasExplicitMaxSize := false
	if field.Annotations != nil {
		if ann, ok := field.Annotations["assert_required"]; ok && ann.BoolValue {
			constraints = append(constraints, boolConstraintInfo("assert_required", "field", true))
		}
		if ann, ok := field.Annotations["assert_max_size"]; ok && ann.Type == model.AnnotationTypeInt {
			hasExplicitMaxSize = true
			constraints = append(constraints, intConstraintInfo("assert_max_size", "field", ann.IntValue, false, maxSizeUnit(field), "annotation"))
		}
		if ann, ok := field.Annotations["assert_min_size"]; ok && ann.Type == model.AnnotationTypeInt {
			constraints = append(constraints, intConstraintInfo("assert_min_size", "field", ann.IntValue, false, maxSizeUnit(field), "annotation"))
		}
		if ann, ok := field.Annotations["assert_url"]; ok && ann.EnumValue != "" {
			constraints = append(constraints, enumConstraintInfo("assert_url", "field", ann.EnumValue))
		}
		for _, name := range []string{
			"assert_email",
			"assert_fqdn",
			"assert_net_port",
			"assert_listen_addr",
			"assert_base64",
			"assert_semver",
			"assert_host",
			"assert_enum_defined",
		} {
			if ann, ok := field.Annotations[name]; ok && ann.BoolValue {
				constraints = append(constraints, boolConstraintInfo(name, "field", true))
			}
		}
		for _, name := range []string{"assert_ipaddr_type", "assert_duration", "assert_file_path", "assert_enc_type", "assert_sql"} {
			if ann, ok := field.Annotations[name]; ok && ann.EnumValue != "" {
				constraints = append(constraints, enumConstraintInfo(name, "field", ann.EnumValue))
			}
		}
		for _, item := range []struct {
			name string
			unit string
		}{
			{name: "assert_duration_min", unit: "seconds"},
			{name: "assert_duration_max", unit: "seconds"},
			{name: "assert_gt"},
			{name: "assert_ge"},
			{name: "assert_lt"},
			{name: "assert_le"},
		} {
			if ann, ok := field.Annotations[item.name]; ok && ann.Type == model.AnnotationTypeInt {
				constraints = append(constraints, intConstraintInfo(item.name, "field", ann.IntValue, false, item.unit, "annotation"))
			}
		}
		for _, name := range []string{"required_if_true", "required_if_false", "required_if_eq"} {
			if ann, ok := field.Annotations[name]; ok && ann.StringValue != "" {
				constraints = append(constraints, stringConstraintInfo(name, "field", ann.StringValue))
			}
		}
		if ann, ok := field.Annotations["assert_numeric"]; ok && ann.NumericConstraint != nil {
			constraints = appendNumericConstraintInfos(constraints, ann.NumericConstraint)
		}
		if ann, ok := field.Annotations["assert_float"]; ok && ann.FloatConstraint != nil {
			constraints = appendFloatConstraintInfos(constraints, ann.FloatConstraint)
		}
	}
	return appendDefaultConstraintInfos(constraints, field, hasExplicitMaxSize)
}

func appendDefaultBoundConstraints(constraints []string, field model.Field, hasExplicitMaxSize bool) []string {
	if !hasExplicitMaxSize {
		switch {
		case field.IsMap:
			constraints = append(constraints, maxSizeConstraint(field, collection.DefaultCollCount, true))
		case field.IsRepeated:
			constraints = append(constraints, maxSizeConstraint(field, collection.DefaultCollCount, true))
		case field.Type == "string" || field.Type == "bytes":
			constraints = append(constraints, maxSizeConstraint(field, collection.DefaultScalarBytes, true))
		}
	}

	// (spec.assert_element_max_size) replaces the default per-element bound for
	// repeated elements and map values; map keys keep the default.
	elemSize, elemSuffix := collection.DefaultScalarBytes, " (default)"
	if bound := field.ElementMaxSizeBytes(); bound > 0 {
		elemSize, elemSuffix = bound, ""
	}

	if field.IsRepeated && (field.Type == "string" || field.Type == "bytes") {
		constraints = append(constraints, fmt.Sprintf("maximum element size: %d bytes%s", elemSize, elemSuffix))
	}
	if field.IsMap {
		if field.MapKeyType == "string" {
			constraints = append(constraints, fmt.Sprintf("maximum key size: %d bytes (default)", collection.DefaultScalarBytes))
		}
		if field.MapValueType == "string" || field.MapValueType == "bytes" {
			constraints = append(constraints, fmt.Sprintf("maximum value size: %d bytes%s", elemSize, elemSuffix))
		}
	}
	return constraints
}

func appendDefaultConstraintInfos(constraints []ConstraintDescriptor, field model.Field, hasExplicitMaxSize bool) []ConstraintDescriptor {
	if !hasExplicitMaxSize {
		switch {
		case field.IsMap:
			constraints = append(constraints, intConstraintInfo("default_max_size", "field", collection.DefaultCollCount, true, "entries", "default_bound"))
		case field.IsRepeated:
			constraints = append(constraints, intConstraintInfo("default_max_size", "field", collection.DefaultCollCount, true, "elements", "default_bound"))
		case field.Type == "string" || field.Type == "bytes":
			constraints = append(constraints, intConstraintInfo("default_max_size", "field", collection.DefaultScalarBytes, true, "bytes", "default_bound"))
		}
	}

	// (spec.assert_element_max_size) replaces the default per-element bound for
	// repeated elements and map values; map keys keep the default.
	elemSize, elemName, elemIsDefault, elemSource := collection.DefaultScalarBytes, "default_max_element_size", true, "default_bound"
	valName := "default_max_value_size"
	if bound := field.ElementMaxSizeBytes(); bound > 0 {
		elemSize, elemName, elemIsDefault, elemSource = bound, "assert_element_max_size", false, "annotation"
		valName = "assert_element_max_size"
	}

	if field.IsRepeated && (field.Type == "string" || field.Type == "bytes") {
		constraints = append(constraints, intConstraintInfo(elemName, "element", elemSize, elemIsDefault, "bytes", elemSource))
	}
	if field.IsMap {
		if field.MapKeyType == "string" {
			constraints = append(constraints, intConstraintInfo("default_max_key_size", "key", collection.DefaultScalarBytes, true, "bytes", "default_bound"))
		}
		if field.MapValueType == "string" || field.MapValueType == "bytes" {
			constraints = append(constraints, intConstraintInfo(valName, "value", elemSize, elemIsDefault, "bytes", elemSource))
		}
	}
	return constraints
}

func appendNumericConstraintInfos(constraints []ConstraintDescriptor, nc *model.NumericConstraint) []ConstraintDescriptor {
	for _, item := range []struct {
		operator string
		value    *int64
	}{
		{operator: "gt", value: nc.Gt},
		{operator: "ge", value: nc.Ge},
		{operator: "lt", value: nc.Lt},
		{operator: "le", value: nc.Le},
		{operator: "eq", value: nc.Eq},
		{operator: "ne", value: nc.Ne},
		{operator: "multiple_of", value: nc.MultipleOf},
	} {
		if item.value != nil {
			constraints = append(constraints, intOperatorConstraintInfo("assert_numeric", item.operator, *item.value))
		}
	}
	if len(nc.In) > 0 {
		constraints = append(constraints, intListConstraintInfo("assert_numeric", "in", nc.In))
	}
	if len(nc.NotIn) > 0 {
		constraints = append(constraints, intListConstraintInfo("assert_numeric", "not_in", nc.NotIn))
	}
	return constraints
}

func appendFloatConstraintInfos(constraints []ConstraintDescriptor, fc *model.FloatConstraint) []ConstraintDescriptor {
	for _, item := range []struct {
		operator string
		value    *float64
	}{
		{operator: "gt", value: fc.Gt},
		{operator: "ge", value: fc.Ge},
		{operator: "lt", value: fc.Lt},
		{operator: "le", value: fc.Le},
		{operator: "epsilon", value: fc.Epsilon},
	} {
		if item.value != nil {
			constraints = append(constraints, floatOperatorConstraintInfo("assert_float", item.operator, *item.value))
		}
	}
	if len(fc.In) > 0 {
		constraints = append(constraints, floatListConstraintInfo("assert_float", "in", fc.In))
	}
	if len(fc.NotIn) > 0 {
		constraints = append(constraints, floatListConstraintInfo("assert_float", "not_in", fc.NotIn))
	}
	return constraints
}

func boolConstraintInfo(rule, target string, value bool) ConstraintDescriptor {
	return ConstraintDescriptor{
		Rule:      rule,
		Target:    target,
		ValueKind: "bool",
		BoolValue: boolPtr(value),
		Source:    "annotation",
	}
}

func enumConstraintInfo(rule, target, value string) ConstraintDescriptor {
	return ConstraintDescriptor{
		Rule:      rule,
		Target:    target,
		ValueKind: "enum",
		EnumValue: value,
		Source:    "annotation",
	}
}

func stringConstraintInfo(rule, target, value string) ConstraintDescriptor {
	return ConstraintDescriptor{
		Rule:        rule,
		Target:      target,
		ValueKind:   "string",
		StringValue: value,
		Source:      "annotation",
	}
}

func intConstraintInfo(rule, target string, value int64, defaulted bool, unit, source string) ConstraintDescriptor {
	return ConstraintDescriptor{
		Rule:      rule,
		Target:    target,
		ValueKind: "int",
		IntValue:  int64Ptr(value),
		Defaulted: defaulted,
		Unit:      unit,
		Source:    source,
	}
}

func intOperatorConstraintInfo(rule, operator string, value int64) ConstraintDescriptor {
	c := intConstraintInfo(rule, "field", value, false, "", "annotation")
	c.Operator = operator
	return c
}

func floatOperatorConstraintInfo(rule, operator string, value float64) ConstraintDescriptor {
	return ConstraintDescriptor{
		Rule:       rule,
		Target:     "field",
		ValueKind:  "float",
		FloatValue: float64Ptr(value),
		Operator:   operator,
		Source:     "annotation",
	}
}

func intListConstraintInfo(rule, operator string, values []int64) ConstraintDescriptor {
	return ConstraintDescriptor{
		Rule:      rule,
		Target:    "field",
		ValueKind: "int_list",
		IntValues: append([]int64(nil), values...),
		Operator:  operator,
		Source:    "annotation",
	}
}

func floatListConstraintInfo(rule, operator string, values []float64) ConstraintDescriptor {
	return ConstraintDescriptor{
		Rule:        rule,
		Target:      "field",
		ValueKind:   "float_list",
		FloatValues: append([]float64(nil), values...),
		Operator:    operator,
		Source:      "annotation",
	}
}

func maxSizeUnit(field model.Field) string {
	if field.IsMap {
		return "entries"
	}
	if field.IsRepeated {
		return "elements"
	}
	return "bytes"
}

func boolPtr(v bool) *bool {
	return &v
}

func int64Ptr(v int64) *int64 {
	return &v
}

func float64Ptr(v float64) *float64 {
	return &v
}

func maxSizeConstraint(field model.Field, size int64, isDefault bool) string {
	unit := "bytes"
	if field.IsMap {
		unit = "entries"
	} else if field.IsRepeated {
		unit = "elements"
	}
	suffix := ""
	if isDefault {
		suffix = " (default)"
	}
	return fmt.Sprintf("maximum size: %d %s%s", size, unit, suffix)
}
