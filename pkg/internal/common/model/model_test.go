// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package model

import "testing"

func TestIsEnumSentinel(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{name: "UNDEFINED", input: "CLOUD_VENDOR_UNDEFINED", expected: true},
		{name: "UNSPECIFIED", input: "LOG_LEVEL_UNSPECIFIED", expected: true},
		{name: "NO_ERROR", input: "ERROR_CODE_TYPE_NO_ERROR", expected: true},
		{name: "lowercase undefined", input: "some_undefined_value", expected: true},
		{name: "mixed case Undefined", input: "Value_Undefined", expected: true},
		{name: "valid value AWS", input: "CLOUD_VENDOR_AWS", expected: false},
		{name: "valid value INFO", input: "LOG_LEVEL_INFO", expected: false},
		{name: "valid value H2", input: "ALPN_PROTOCOL_H2", expected: false},
		{name: "empty string", input: "", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsEnumSentinel(tt.input)
			if got != tt.expected {
				t.Errorf("IsEnumSentinel(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}

func TestAnnotationTypeConstants(t *testing.T) {
	// Verify iota ordering
	if AnnotationTypeBool != 0 {
		t.Errorf("AnnotationTypeBool = %d, want 0", AnnotationTypeBool)
	}
	if AnnotationTypeEnum != 1 {
		t.Errorf("AnnotationTypeEnum = %d, want 1", AnnotationTypeEnum)
	}
	if AnnotationTypeInt != 2 {
		t.Errorf("AnnotationTypeInt = %d, want 2", AnnotationTypeInt)
	}
	if AnnotationTypeNumericConstraint != 3 {
		t.Errorf("AnnotationTypeNumericConstraint = %d, want 3", AnnotationTypeNumericConstraint)
	}
	if AnnotationTypeFloatConstraint != 4 {
		t.Errorf("AnnotationTypeFloatConstraint = %d, want 4", AnnotationTypeFloatConstraint)
	}
}

func TestProtoFile_Fields(t *testing.T) {
	pf := ProtoFile{
		Package:    "test",
		SourcePath: "/path/to/test.proto",
		RelPath:    "test.proto",
		Enums: []Enum{
			{Name: "Status", Values: []EnumValue{{Name: "OK", Number: 0}}},
		},
		Constants: []Constant{
			{Name: "VERSION", Value: "1.0"},
		},
		Messages: []Message{
			{Name: "Request", Fields: []Field{{Name: "id", Type: "string"}}},
		},
		Imports: []Import{
			{Path: "other.proto", Package: "other"},
		},
	}

	if pf.Package != "test" {
		t.Errorf("Package = %q, want %q", pf.Package, "test")
	}
	if len(pf.Enums) != 1 || pf.Enums[0].Name != "Status" {
		t.Error("Enums not set correctly")
	}
	if len(pf.Enums[0].Values) != 1 || pf.Enums[0].Values[0].Name != "OK" {
		t.Error("EnumValues not set correctly")
	}
	if len(pf.Constants) != 1 || pf.Constants[0].Name != "VERSION" {
		t.Error("Constants not set correctly")
	}
	if len(pf.Messages) != 1 || pf.Messages[0].Name != "Request" {
		t.Error("Messages not set correctly")
	}
	if len(pf.Messages[0].Fields) != 1 || pf.Messages[0].Fields[0].Name != "id" {
		t.Error("Fields not set correctly")
	}
	if len(pf.Imports) != 1 || pf.Imports[0].Package != "other" {
		t.Error("Imports not set correctly")
	}
}

func TestField_Annotations(t *testing.T) {
	f := Field{
		Name: "email",
		Type: "string",
		Annotations: map[string]AnnotationValue{
			"assert_required": {Type: AnnotationTypeBool, BoolValue: true},
			"assert_email":    {Type: AnnotationTypeBool, BoolValue: true},
		},
	}
	if len(f.Annotations) != 2 {
		t.Errorf("expected 2 annotations, got %d", len(f.Annotations))
	}
	if !f.Annotations["assert_required"].BoolValue {
		t.Error("assert_required should be true")
	}
}

func TestField_MapType(t *testing.T) {
	f := Field{
		Name:         "metadata",
		IsMap:        true,
		MapKeyType:   "string",
		MapValueType: "string",
	}
	if !f.IsMap {
		t.Error("expected IsMap to be true")
	}
	if f.MapKeyType != "string" {
		t.Errorf("MapKeyType = %q, want %q", f.MapKeyType, "string")
	}
}

func TestNumericConstraint(t *testing.T) {
	gt := int64(0)
	le := int64(100)
	nc := NumericConstraint{
		Gt: &gt,
		Le: &le,
	}
	if *nc.Gt != 0 {
		t.Errorf("Gt = %d, want 0", *nc.Gt)
	}
	if *nc.Le != 100 {
		t.Errorf("Le = %d, want 100", *nc.Le)
	}
	if nc.Lt != nil {
		t.Error("Lt should be nil")
	}
}

func TestFloatConstraint(t *testing.T) {
	ge := 0.0
	lt := 1.0
	eps := 0.001
	fc := FloatConstraint{
		Ge:      &ge,
		Lt:      &lt,
		Epsilon: &eps,
	}
	if *fc.Ge != 0.0 {
		t.Errorf("Ge = %f, want 0.0", *fc.Ge)
	}
	if *fc.Epsilon != 0.001 {
		t.Errorf("Epsilon = %f, want 0.001", *fc.Epsilon)
	}
}

func TestParamMapConfig(t *testing.T) {
	pmc := ParamMapConfig{
		KeyEnum:          "TokenParamKey",
		Strict:           true,
		ValidateRequired: true,
	}
	if pmc.KeyEnum != "TokenParamKey" {
		t.Errorf("KeyEnum = %q, want %q", pmc.KeyEnum, "TokenParamKey")
	}
	if !pmc.Strict {
		t.Error("Strict should be true")
	}
}

func TestMessage_WithParamMapConfig(t *testing.T) {
	m := Message{
		Name:   "TokenRequest",
		Fields: []Field{{Name: "client_id", Type: "string"}},
		ParamMapConfig: &ParamMapConfig{
			KeyEnum: "TokenParamKey",
		},
	}
	if m.ParamMapConfig == nil {
		t.Fatal("ParamMapConfig should not be nil")
	}
	if m.ParamMapConfig.KeyEnum != "TokenParamKey" {
		t.Errorf("KeyEnum = %q, want %q", m.ParamMapConfig.KeyEnum, "TokenParamKey")
	}
}

func TestAnnotationValue_NumericConstraint(t *testing.T) {
	gt := int64(5)
	av := AnnotationValue{
		Type: AnnotationTypeNumericConstraint,
		NumericConstraint: &NumericConstraint{
			Gt: &gt,
		},
	}
	if av.Type != AnnotationTypeNumericConstraint {
		t.Error("wrong annotation type")
	}
	if *av.NumericConstraint.Gt != 5 {
		t.Errorf("Gt = %d, want 5", *av.NumericConstraint.Gt)
	}
}

// --- Expanded edge case tests ---

func TestAnnotationTypeConstants_Complete(t *testing.T) {
	// Verify all annotation type constants have expected iota values
	tests := []struct {
		name string
		got  AnnotationType
		want AnnotationType
	}{
		{"Bool", AnnotationTypeBool, 0},
		{"Enum", AnnotationTypeEnum, 1},
		{"Int", AnnotationTypeInt, 2},
		{"NumericConstraint", AnnotationTypeNumericConstraint, 3},
		{"FloatConstraint", AnnotationTypeFloatConstraint, 4},
		{"String", AnnotationTypeString, 5},
		{"Float", AnnotationTypeFloat, 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("%s = %d, want %d", tt.name, tt.got, tt.want)
			}
		})
	}
}

func TestAnnotationValue_AllTypes(t *testing.T) {
	t.Run("BoolValue", func(t *testing.T) {
		av := AnnotationValue{Type: AnnotationTypeBool, BoolValue: true}
		if av.Type != AnnotationTypeBool {
			t.Errorf("Type = %d, want %d", av.Type, AnnotationTypeBool)
		}
		if !av.BoolValue {
			t.Error("BoolValue should be true")
		}
	})

	t.Run("BoolValue_false", func(t *testing.T) {
		av := AnnotationValue{Type: AnnotationTypeBool, BoolValue: false}
		if av.BoolValue {
			t.Error("BoolValue should be false")
		}
	})

	t.Run("EnumValue", func(t *testing.T) {
		av := AnnotationValue{Type: AnnotationTypeEnum, EnumValue: "IP_ADDR_TYPE_IP_V4"}
		if av.Type != AnnotationTypeEnum {
			t.Errorf("Type = %d, want %d", av.Type, AnnotationTypeEnum)
		}
		if av.EnumValue != "IP_ADDR_TYPE_IP_V4" {
			t.Errorf("EnumValue = %q, want %q", av.EnumValue, "IP_ADDR_TYPE_IP_V4")
		}
	})

	t.Run("IntValue", func(t *testing.T) {
		av := AnnotationValue{Type: AnnotationTypeInt, IntValue: 42}
		if av.Type != AnnotationTypeInt {
			t.Errorf("Type = %d, want %d", av.Type, AnnotationTypeInt)
		}
		if av.IntValue != 42 {
			t.Errorf("IntValue = %d, want 42", av.IntValue)
		}
	})

	t.Run("IntValue_zero", func(t *testing.T) {
		av := AnnotationValue{Type: AnnotationTypeInt, IntValue: 0}
		if av.IntValue != 0 {
			t.Errorf("IntValue = %d, want 0", av.IntValue)
		}
	})

	t.Run("IntValue_negative", func(t *testing.T) {
		av := AnnotationValue{Type: AnnotationTypeInt, IntValue: -100}
		if av.IntValue != -100 {
			t.Errorf("IntValue = %d, want -100", av.IntValue)
		}
	})

	t.Run("StringValue", func(t *testing.T) {
		av := AnnotationValue{Type: AnnotationTypeString, StringValue: "hello"}
		if av.Type != AnnotationTypeString {
			t.Errorf("Type = %d, want %d", av.Type, AnnotationTypeString)
		}
		if av.StringValue != "hello" {
			t.Errorf("StringValue = %q, want %q", av.StringValue, "hello")
		}
	})

	t.Run("StringValue_empty", func(t *testing.T) {
		av := AnnotationValue{Type: AnnotationTypeString, StringValue: ""}
		if av.StringValue != "" {
			t.Errorf("StringValue = %q, want empty", av.StringValue)
		}
	})

	t.Run("FloatValue", func(t *testing.T) {
		av := AnnotationValue{Type: AnnotationTypeFloat, FloatValue: 3.14}
		if av.Type != AnnotationTypeFloat {
			t.Errorf("Type = %d, want %d", av.Type, AnnotationTypeFloat)
		}
		if av.FloatValue != 3.14 {
			t.Errorf("FloatValue = %f, want 3.14", av.FloatValue)
		}
	})

	t.Run("FloatConstraintValue", func(t *testing.T) {
		ge := 0.0
		lt := 100.0
		av := AnnotationValue{
			Type: AnnotationTypeFloatConstraint,
			FloatConstraint: &FloatConstraint{
				Ge: &ge,
				Lt: &lt,
			},
		}
		if av.Type != AnnotationTypeFloatConstraint {
			t.Errorf("Type = %d, want %d", av.Type, AnnotationTypeFloatConstraint)
		}
		if av.FloatConstraint == nil {
			t.Fatal("FloatConstraint should not be nil")
		}
		if *av.FloatConstraint.Ge != 0.0 {
			t.Errorf("Ge = %f, want 0.0", *av.FloatConstraint.Ge)
		}
	})
}

func TestNumericConstraint_AllFields(t *testing.T) {
	gt := int64(1)
	ge := int64(0)
	lt := int64(100)
	le := int64(99)
	eq := int64(50)
	ne := int64(42)
	mul := int64(5)

	nc := NumericConstraint{
		Gt:         &gt,
		Ge:         &ge,
		Lt:         &lt,
		Le:         &le,
		Eq:         &eq,
		Ne:         &ne,
		In:         []int64{1, 2, 3, 5, 8},
		NotIn:      []int64{0, -1},
		MultipleOf: &mul,
	}

	if *nc.Gt != 1 {
		t.Errorf("Gt = %d, want 1", *nc.Gt)
	}
	if *nc.Ge != 0 {
		t.Errorf("Ge = %d, want 0", *nc.Ge)
	}
	if *nc.Lt != 100 {
		t.Errorf("Lt = %d, want 100", *nc.Lt)
	}
	if *nc.Le != 99 {
		t.Errorf("Le = %d, want 99", *nc.Le)
	}
	if *nc.Eq != 50 {
		t.Errorf("Eq = %d, want 50", *nc.Eq)
	}
	if *nc.Ne != 42 {
		t.Errorf("Ne = %d, want 42", *nc.Ne)
	}
	if len(nc.In) != 5 {
		t.Errorf("In has %d elements, want 5", len(nc.In))
	}
	if len(nc.NotIn) != 2 {
		t.Errorf("NotIn has %d elements, want 2", len(nc.NotIn))
	}
	if *nc.MultipleOf != 5 {
		t.Errorf("MultipleOf = %d, want 5", *nc.MultipleOf)
	}
}

func TestNumericConstraint_AllNil(t *testing.T) {
	nc := NumericConstraint{}
	if nc.Gt != nil {
		t.Error("Gt should be nil")
	}
	if nc.Ge != nil {
		t.Error("Ge should be nil")
	}
	if nc.Lt != nil {
		t.Error("Lt should be nil")
	}
	if nc.Le != nil {
		t.Error("Le should be nil")
	}
	if nc.Eq != nil {
		t.Error("Eq should be nil")
	}
	if nc.Ne != nil {
		t.Error("Ne should be nil")
	}
	if nc.In != nil {
		t.Error("In should be nil")
	}
	if nc.NotIn != nil {
		t.Error("NotIn should be nil")
	}
	if nc.MultipleOf != nil {
		t.Error("MultipleOf should be nil")
	}
}

func TestFloatConstraint_AllFields(t *testing.T) {
	gt := 0.1
	ge := 0.0
	lt := 99.9
	le := 100.0
	eps := 0.0001
	in := []float64{1.0, 2.5, 3.14}
	notIn := []float64{0.0, -1.0}

	fc := FloatConstraint{
		Gt:      &gt,
		Ge:      &ge,
		Lt:      &lt,
		Le:      &le,
		Epsilon: &eps,
		In:      in,
		NotIn:   notIn,
	}

	if *fc.Gt != 0.1 {
		t.Errorf("Gt = %f, want 0.1", *fc.Gt)
	}
	if *fc.Ge != 0.0 {
		t.Errorf("Ge = %f, want 0.0", *fc.Ge)
	}
	if *fc.Lt != 99.9 {
		t.Errorf("Lt = %f, want 99.9", *fc.Lt)
	}
	if *fc.Le != 100.0 {
		t.Errorf("Le = %f, want 100.0", *fc.Le)
	}
	if *fc.Epsilon != 0.0001 {
		t.Errorf("Epsilon = %f, want 0.0001", *fc.Epsilon)
	}
	if len(fc.In) != 3 {
		t.Errorf("In has %d elements, want 3", len(fc.In))
	}
	if len(fc.NotIn) != 2 {
		t.Errorf("NotIn has %d elements, want 2", len(fc.NotIn))
	}
}

func TestFloatConstraint_AllNil(t *testing.T) {
	fc := FloatConstraint{}
	if fc.Gt != nil {
		t.Error("Gt should be nil")
	}
	if fc.Ge != nil {
		t.Error("Ge should be nil")
	}
	if fc.Lt != nil {
		t.Error("Lt should be nil")
	}
	if fc.Le != nil {
		t.Error("Le should be nil")
	}
	if fc.Epsilon != nil {
		t.Error("Epsilon should be nil")
	}
	if fc.In != nil {
		t.Error("In should be nil")
	}
	if fc.NotIn != nil {
		t.Error("NotIn should be nil")
	}
}

func TestField_AllBoolFlags(t *testing.T) {
	t.Run("repeated_field", func(t *testing.T) {
		f := Field{Name: "items", Type: "string", IsRepeated: true}
		if !f.IsRepeated {
			t.Error("IsRepeated should be true")
		}
		if f.IsOptional || f.IsMessage || f.IsEnum || f.IsMap || f.IsOneOf {
			t.Error("other flags should be false")
		}
	})

	t.Run("optional_field", func(t *testing.T) {
		f := Field{Name: "nickname", Type: "string", IsOptional: true}
		if !f.IsOptional {
			t.Error("IsOptional should be true")
		}
	})

	t.Run("message_field", func(t *testing.T) {
		f := Field{Name: "address", Type: "Address", IsMessage: true}
		if !f.IsMessage {
			t.Error("IsMessage should be true")
		}
	})

	t.Run("enum_field", func(t *testing.T) {
		f := Field{Name: "status", Type: "Status", IsEnum: true}
		if !f.IsEnum {
			t.Error("IsEnum should be true")
		}
	})

	t.Run("oneof_field", func(t *testing.T) {
		f := Field{
			Name:      "ip_v4",
			Type:      "string",
			IsOneOf:   true,
			OneOfName: "boundary",
		}
		if !f.IsOneOf {
			t.Error("IsOneOf should be true")
		}
		if f.OneOfName != "boundary" {
			t.Errorf("OneOfName = %q, want %q", f.OneOfName, "boundary")
		}
	})
}

func TestField_CrossPackageType(t *testing.T) {
	f := Field{
		Name:        "key_type",
		Type:        "KeyType",
		TypePackage: "key",
		IsEnum:      true,
	}
	if f.TypePackage != "key" {
		t.Errorf("TypePackage = %q, want %q", f.TypePackage, "key")
	}
	if f.Type != "KeyType" {
		t.Errorf("Type = %q, want %q", f.Type, "KeyType")
	}
}

func TestField_MapWithMessageValue(t *testing.T) {
	f := Field{
		Name:              "entries",
		IsMap:             true,
		MapKeyType:        "string",
		MapValueType:      "Entry",
		MapValueIsMessage: true,
	}
	if !f.IsMap {
		t.Error("IsMap should be true")
	}
	if !f.MapValueIsMessage {
		t.Error("MapValueIsMessage should be true")
	}
	if f.MapValueType != "Entry" {
		t.Errorf("MapValueType = %q, want %q", f.MapValueType, "Entry")
	}
}

func TestField_ProtoType(t *testing.T) {
	f := Field{
		Name:      "key_type",
		Type:      "KeyType",
		ProtoType: "key.KeyType",
	}
	if f.ProtoType != "key.KeyType" {
		t.Errorf("ProtoType = %q, want %q", f.ProtoType, "key.KeyType")
	}
}

func TestField_ParamKey(t *testing.T) {
	f := Field{
		Name:     "client_id",
		Type:     "string",
		ParamKey: "TOKEN_PARAM_KEY_CLIENT_ID",
	}
	if f.ParamKey != "TOKEN_PARAM_KEY_CLIENT_ID" {
		t.Errorf("ParamKey = %q, want %q", f.ParamKey, "TOKEN_PARAM_KEY_CLIENT_ID")
	}
}

func TestField_Number(t *testing.T) {
	f := Field{Name: "id", Type: "string", Number: 1}
	if f.Number != 1 {
		t.Errorf("Number = %d, want 1", f.Number)
	}
}

func TestField_EmptyAnnotations(t *testing.T) {
	f := Field{Name: "id", Type: "string"}
	if f.Annotations != nil {
		t.Error("Annotations should be nil when not set")
	}
	// Empty map should also work
	f2 := Field{Name: "id", Type: "string", Annotations: map[string]AnnotationValue{}}
	if len(f2.Annotations) != 0 {
		t.Errorf("expected 0 annotations, got %d", len(f2.Annotations))
	}
}

func TestField_MultipleAnnotations(t *testing.T) {
	minLen := int64(3)
	maxLen := int64(255)
	f := Field{
		Name: "email",
		Type: "string",
		Annotations: map[string]AnnotationValue{
			"assert_required":   {Type: AnnotationTypeBool, BoolValue: true},
			"assert_email":      {Type: AnnotationTypeBool, BoolValue: true},
			"assert_min_size": {Type: AnnotationTypeInt, IntValue: minLen},
			"assert_max_size": {Type: AnnotationTypeInt, IntValue: maxLen},
			"assert_ipaddr_type": {
				Type:      AnnotationTypeEnum,
				EnumValue: "IP_ADDR_TYPE_IP_V4",
			},
		},
	}
	if len(f.Annotations) != 5 {
		t.Errorf("expected 5 annotations, got %d", len(f.Annotations))
	}
	if f.Annotations["assert_min_size"].IntValue != 3 {
		t.Errorf("assert_min_size = %d, want 3", f.Annotations["assert_min_size"].IntValue)
	}
	if f.Annotations["assert_max_size"].IntValue != 255 {
		t.Errorf("assert_max_size = %d, want 255", f.Annotations["assert_max_size"].IntValue)
	}
}

func TestMessage_Annotations(t *testing.T) {
	m := Message{
		Name:   "ServerConfig",
		Fields: []Field{{Name: "port", Type: "int32", Number: 1}},
		Annotations: map[string]AnnotationValue{
			"service_state": {Type: AnnotationTypeString, StringValue: "ServerState"},
		},
	}
	if m.Annotations == nil {
		t.Fatal("Annotations should not be nil")
	}
	if m.Annotations["service_state"].StringValue != "ServerState" {
		t.Error("service_state should be ServerState")
	}
}

func TestMessage_NoParamMapConfig(t *testing.T) {
	m := Message{
		Name:   "SimpleRequest",
		Fields: []Field{{Name: "id", Type: "string"}},
	}
	if m.ParamMapConfig != nil {
		t.Error("ParamMapConfig should be nil")
	}
}

func TestMessage_EmptyFields(t *testing.T) {
	m := Message{Name: "Empty"}
	if len(m.Fields) != 0 {
		t.Errorf("expected 0 fields, got %d", len(m.Fields))
	}
}

func TestMessage_ManyFields(t *testing.T) {
	fields := []Field{
		{Name: "id", Type: "string", Number: 1},
		{Name: "name", Type: "string", Number: 2},
		{Name: "age", Type: "int32", Number: 3},
		{Name: "email", Type: "string", Number: 4, Annotations: map[string]AnnotationValue{
			"assert_email": {Type: AnnotationTypeBool, BoolValue: true},
		}},
		{Name: "addresses", Type: "Address", Number: 5, IsRepeated: true, IsMessage: true},
		{Name: "metadata", Type: "string", Number: 6, IsMap: true, MapKeyType: "string", MapValueType: "string"},
	}
	m := Message{Name: "User", Fields: fields}
	if len(m.Fields) != 6 {
		t.Errorf("expected 6 fields, got %d", len(m.Fields))
	}
	// Verify field 4 (email) has the email annotation
	if !m.Fields[3].Annotations["assert_email"].BoolValue {
		t.Error("email field should have assert_email annotation")
	}
	// Verify field 5 (addresses) is repeated message
	if !m.Fields[4].IsRepeated || !m.Fields[4].IsMessage {
		t.Error("addresses should be repeated message")
	}
	// Verify field 6 (metadata) is a map
	if !m.Fields[5].IsMap {
		t.Error("metadata should be a map")
	}
}

func TestParamMapConfig_Variations(t *testing.T) {
	t.Run("all_false", func(t *testing.T) {
		pmc := ParamMapConfig{
			KeyEnum:          "SomeKey",
			Strict:           false,
			ValidateRequired: false,
		}
		if pmc.Strict {
			t.Error("Strict should be false")
		}
		if pmc.ValidateRequired {
			t.Error("ValidateRequired should be false")
		}
	})

	t.Run("strict_only", func(t *testing.T) {
		pmc := ParamMapConfig{
			KeyEnum: "SomeKey",
			Strict:  true,
		}
		if !pmc.Strict {
			t.Error("Strict should be true")
		}
		if pmc.ValidateRequired {
			t.Error("ValidateRequired should be false")
		}
	})

	t.Run("validate_required_only", func(t *testing.T) {
		pmc := ParamMapConfig{
			KeyEnum:          "SomeKey",
			ValidateRequired: true,
		}
		if pmc.Strict {
			t.Error("Strict should be false")
		}
		if !pmc.ValidateRequired {
			t.Error("ValidateRequired should be true")
		}
	})
}

func TestEnum_ZeroValues(t *testing.T) {
	e := Enum{
		Name:    "Status",
		Comment: "Status codes",
		Values: []EnumValue{
			{Name: "UNKNOWN", Number: 0, Comment: "default unknown"},
			{Name: "OK", Number: 1, Comment: "success"},
			{Name: "ERROR", Number: 2, Comment: "error"},
		},
	}
	if len(e.Values) != 3 {
		t.Errorf("expected 3 values, got %d", len(e.Values))
	}
	if e.Values[0].Number != 0 {
		t.Errorf("first enum value should be 0, got %d", e.Values[0].Number)
	}
	if e.Comment != "Status codes" {
		t.Errorf("Comment = %q, want %q", e.Comment, "Status codes")
	}
}

func TestEnumValue_JsonValue(t *testing.T) {
	ev := EnumValue{
		Name:      "OK",
		Number:    1,
		JsonValue: "ok",
	}
	if ev.JsonValue != "ok" {
		t.Errorf("JsonValue = %q, want %q", ev.JsonValue, "ok")
	}
}

func TestEnumValue_EmptyJsonValue(t *testing.T) {
	ev := EnumValue{Name: "OK", Number: 1}
	if ev.JsonValue != "" {
		t.Errorf("JsonValue should be empty, got %q", ev.JsonValue)
	}
}

func TestConstant_WithType(t *testing.T) {
	c := Constant{
		Name:    "Timeout",
		Type:    "time.Duration",
		Value:   "30 * time.Second",
		Comment: "default timeout",
	}
	if c.Type != "time.Duration" {
		t.Errorf("Type = %q, want %q", c.Type, "time.Duration")
	}
	if c.Comment != "default timeout" {
		t.Errorf("Comment = %q, want %q", c.Comment, "default timeout")
	}
}

func TestConstant_WithoutType(t *testing.T) {
	c := Constant{
		Name:  "VERSION",
		Value: `"1.0.0"`,
	}
	if c.Type != "" {
		t.Errorf("Type should be empty, got %q", c.Type)
	}
}

func TestImport_Fields(t *testing.T) {
	imp := Import{
		Path:    "sys/key.proto",
		Package: "key",
	}
	if imp.Path != "sys/key.proto" {
		t.Errorf("Path = %q, want %q", imp.Path, "sys/key.proto")
	}
	if imp.Package != "key" {
		t.Errorf("Package = %q, want %q", imp.Package, "key")
	}
}

func TestProtoFile_JavaOuterClass(t *testing.T) {
	pf := ProtoFile{
		Package:        "auth",
		JavaOuterClass: "AuthProto",
		SourcePath:     "/path/to/auth.proto",
		RelPath:        "auth.proto",
	}
	if pf.JavaOuterClass != "AuthProto" {
		t.Errorf("JavaOuterClass = %q, want %q", pf.JavaOuterClass, "AuthProto")
	}
}

func TestProtoFile_EmptyJavaOuterClass(t *testing.T) {
	pf := ProtoFile{Package: "test"}
	if pf.JavaOuterClass != "" {
		t.Errorf("JavaOuterClass should be empty, got %q", pf.JavaOuterClass)
	}
}

func TestProtoFile_MultipleEnums(t *testing.T) {
	pf := ProtoFile{
		Package: "test",
		Enums: []Enum{
			{Name: "Status", Values: []EnumValue{{Name: "OK", Number: 0}}},
			{Name: "Priority", Values: []EnumValue{{Name: "LOW", Number: 0}, {Name: "HIGH", Number: 1}}},
			{Name: "Color", Values: []EnumValue{{Name: "RED", Number: 0}, {Name: "GREEN", Number: 1}, {Name: "BLUE", Number: 2}}},
		},
	}
	if len(pf.Enums) != 3 {
		t.Errorf("expected 3 enums, got %d", len(pf.Enums))
	}
	if len(pf.Enums[2].Values) != 3 {
		t.Errorf("Color enum should have 3 values, got %d", len(pf.Enums[2].Values))
	}
}

func TestProtoFile_MultipleMessages(t *testing.T) {
	pf := ProtoFile{
		Package: "test",
		Messages: []Message{
			{Name: "Request", Fields: []Field{{Name: "id", Type: "string", Number: 1}}},
			{Name: "Response", Fields: []Field{
				{Name: "data", Type: "bytes", Number: 1},
				{Name: "error", Type: "string", Number: 2, IsOptional: true},
			}},
		},
	}
	if len(pf.Messages) != 2 {
		t.Errorf("expected 2 messages, got %d", len(pf.Messages))
	}
	if len(pf.Messages[1].Fields) != 2 {
		t.Errorf("Response should have 2 fields, got %d", len(pf.Messages[1].Fields))
	}
	if !pf.Messages[1].Fields[1].IsOptional {
		t.Error("error field should be optional")
	}
}

func TestProtoFile_MultipleImports(t *testing.T) {
	pf := ProtoFile{
		Package: "auth",
		Imports: []Import{
			{Path: "sys/key.proto", Package: "key"},
			{Path: "sys/common.proto", Package: "common"},
			{Path: "sys/errors.proto", Package: "errors"},
		},
	}
	if len(pf.Imports) != 3 {
		t.Errorf("expected 3 imports, got %d", len(pf.Imports))
	}
	if pf.Imports[1].Package != "common" {
		t.Errorf("second import package = %q, want %q", pf.Imports[1].Package, "common")
	}
}

func TestProtoFile_Empty(t *testing.T) {
	pf := ProtoFile{}
	if pf.Package != "" {
		t.Errorf("Package should be empty, got %q", pf.Package)
	}
	if len(pf.Enums) != 0 {
		t.Errorf("Enums should be empty, got %d", len(pf.Enums))
	}
	if len(pf.Constants) != 0 {
		t.Errorf("Constants should be empty, got %d", len(pf.Constants))
	}
	if len(pf.Messages) != 0 {
		t.Errorf("Messages should be empty, got %d", len(pf.Messages))
	}
	if len(pf.Imports) != 0 {
		t.Errorf("Imports should be empty, got %d", len(pf.Imports))
	}
}

func TestProtoFile_FullyPopulated(t *testing.T) {
	gt := int64(0)
	le := int64(65535)
	pf := ProtoFile{
		Package:        "auth",
		SourcePath:     "/home/user/protos/auth.proto",
		RelPath:        "auth.proto",
		JavaOuterClass: "AuthProto",
		Enums: []Enum{
			{
				Name:    "AuthMethod",
				Comment: "Authentication methods",
				Values: []EnumValue{
					{Name: "AUTH_METHOD_UNSPECIFIED", Number: 0},
					{Name: "AUTH_METHOD_TOKEN", Number: 1, JsonValue: "token"},
					{Name: "AUTH_METHOD_CERT", Number: 2, JsonValue: "cert"},
				},
			},
		},
		Constants: []Constant{
			{Name: "DefaultTimeout", Type: "time.Duration", Value: "30 * time.Second", Comment: "default timeout"},
		},
		Messages: []Message{
			{
				Name: "AuthRequest",
				Fields: []Field{
					{Name: "method", Type: "AuthMethod", Number: 1, IsEnum: true},
					{Name: "token", Type: "string", Number: 2, IsOptional: true, Annotations: map[string]AnnotationValue{
						"assert_required": {Type: AnnotationTypeBool, BoolValue: true},
					}},
					{Name: "port", Type: "int32", Number: 3, Annotations: map[string]AnnotationValue{
						"assert_range": {
							Type: AnnotationTypeNumericConstraint,
							NumericConstraint: &NumericConstraint{
								Gt: &gt,
								Le: &le,
							},
						},
					}},
					{Name: "metadata", Type: "string", Number: 4, IsMap: true, MapKeyType: "string", MapValueType: "string"},
				},
				ParamMapConfig: &ParamMapConfig{
					KeyEnum:          "AuthParamKey",
					Strict:           true,
					ValidateRequired: true,
				},
				Annotations: map[string]AnnotationValue{
					"service_state": {Type: AnnotationTypeString, StringValue: "AuthState"},
				},
			},
		},
		Imports: []Import{
			{Path: "sys/key.proto", Package: "key"},
		},
	}

	if pf.Package != "auth" {
		t.Errorf("Package = %q, want %q", pf.Package, "auth")
	}
	if pf.JavaOuterClass != "AuthProto" {
		t.Errorf("JavaOuterClass = %q, want %q", pf.JavaOuterClass, "AuthProto")
	}
	if len(pf.Enums) != 1 {
		t.Fatalf("expected 1 enum, got %d", len(pf.Enums))
	}
	if len(pf.Enums[0].Values) != 3 {
		t.Errorf("expected 3 enum values, got %d", len(pf.Enums[0].Values))
	}
	if pf.Enums[0].Values[1].JsonValue != "token" {
		t.Errorf("JsonValue = %q, want %q", pf.Enums[0].Values[1].JsonValue, "token")
	}
	if len(pf.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(pf.Messages))
	}
	msg := pf.Messages[0]
	if len(msg.Fields) != 4 {
		t.Errorf("expected 4 fields, got %d", len(msg.Fields))
	}
	if msg.ParamMapConfig == nil {
		t.Fatal("ParamMapConfig should not be nil")
	}
	if !msg.ParamMapConfig.Strict {
		t.Error("Strict should be true")
	}
	if msg.Annotations["service_state"].StringValue != "AuthState" {
		t.Error("service_state should be AuthState")
	}
	// Verify the numeric constraint on port field
	portField := msg.Fields[2]
	if portField.Annotations["assert_range"].NumericConstraint == nil {
		t.Fatal("port field should have numeric constraint")
	}
	if *portField.Annotations["assert_range"].NumericConstraint.Le != 65535 {
		t.Errorf("port Le = %d, want 65535", *portField.Annotations["assert_range"].NumericConstraint.Le)
	}
}
