// Copyright (c) 2026 Second Star Cavalier LLC

package llmdesc

import (
	"slices"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

func TestExtractFieldConstraintsIncludesExplicitAndDefaultBounds(t *testing.T) {
	tests := []struct {
		name        string
		field       model.Field
		want        []string
		doesNotWant []string
	}{
		{
			name:  "scalar string default",
			field: model.Field{Name: "title", Type: "string"},
			want:  []string{"maximum size: 32768 bytes (default)"},
		},
		{
			name:  "repeated string default",
			field: model.Field{Name: "tags", Type: "string", IsRepeated: true},
			want: []string{
				"maximum size: 1024 elements (default)",
				"maximum element size: 32768 bytes (default)",
			},
		},
		{
			name:  "map string defaults",
			field: model.Field{Name: "labels", IsMap: true, MapKeyType: "string", MapValueType: "string"},
			want: []string{
				"maximum size: 1024 entries (default)",
				"maximum key size: 32768 bytes (default)",
				"maximum value size: 32768 bytes (default)",
			},
		},
		{
			name: "explicit scalar max",
			field: model.Field{
				Name: "small",
				Type: "string",
				Annotations: map[string]model.AnnotationValue{
					"assert_max_size": {Type: model.AnnotationTypeInt, IntValue: 8},
				},
			},
			want:        []string{"maximum size: 8 bytes"},
			doesNotWant: []string{"maximum size: 32768 bytes (default)"},
		},
		{
			name: "explicit repeated max still has default element bound",
			field: model.Field{
				Name:       "tags",
				Type:       "string",
				IsRepeated: true,
				Annotations: map[string]model.AnnotationValue{
					"assert_max_size": {Type: model.AnnotationTypeInt, IntValue: 8},
				},
			},
			want: []string{
				"maximum size: 8 elements",
				"maximum element size: 32768 bytes (default)",
			},
			doesNotWant: []string{"maximum size: 1024 elements (default)"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, constraints := ExtractFieldConstraints(tt.field)
			for _, want := range tt.want {
				if !slices.Contains(constraints, want) {
					t.Fatalf("constraints missing %q: %#v", want, constraints)
				}
			}
			for _, banned := range tt.doesNotWant {
				if slices.Contains(constraints, banned) {
					t.Fatalf("constraints unexpectedly contained %q: %#v", banned, constraints)
				}
			}
		})
	}
}

func TestExtractFieldConstraintInfosIncludesStructuredRules(t *testing.T) {
	gt := int64(5)
	field := model.Field{
		Name: "name",
		Type: "string",
		Annotations: map[string]model.AnnotationValue{
			"assert_required": {Type: model.AnnotationTypeBool, BoolValue: true},
			"assert_max_size": {Type: model.AnnotationTypeInt, IntValue: 8},
			"assert_url":      {Type: model.AnnotationTypeEnum, EnumValue: "URL_SCHEME_HTTPS"},
			"assert_numeric": {
				Type: model.AnnotationTypeNumericConstraint,
				NumericConstraint: &model.NumericConstraint{
					Gt: &gt,
					In: []int64{7, 9},
				},
			},
		},
	}

	infos := ExtractFieldConstraintInfos(field)
	required := findConstraintInfo(t, infos, "assert_required", "")
	if required.BoolValue == nil || !*required.BoolValue || required.ValueKind != "bool" {
		t.Fatalf("assert_required = %#v, want bool true", required)
	}
	maxSize := findConstraintInfo(t, infos, "assert_max_size", "")
	if maxSize.IntValue == nil || *maxSize.IntValue != 8 || maxSize.Unit != "bytes" || maxSize.Defaulted {
		t.Fatalf("assert_max_size = %#v, want int 8 bytes explicit", maxSize)
	}
	url := findConstraintInfo(t, infos, "assert_url", "")
	if url.EnumValue != "URL_SCHEME_HTTPS" || url.ValueKind != "enum" {
		t.Fatalf("assert_url = %#v, want URL_SCHEME_HTTPS enum", url)
	}
	numericGt := findConstraintInfo(t, infos, "assert_numeric", "gt")
	if numericGt.IntValue == nil || *numericGt.IntValue != 5 {
		t.Fatalf("assert_numeric gt = %#v, want 5", numericGt)
	}
	numericIn := findConstraintInfo(t, infos, "assert_numeric", "in")
	if !slices.Equal(numericIn.IntValues, []int64{7, 9}) || numericIn.ValueKind != "int_list" {
		t.Fatalf("assert_numeric in = %#v, want [7 9]", numericIn)
	}
	for _, info := range infos {
		if info.Rule == "default_max_size" {
			t.Fatalf("constraint infos unexpectedly contained default max size: %#v", infos)
		}
	}
}

func TestExtractFieldConstraintInfosIncludesDefaultBounds(t *testing.T) {
	infos := ExtractFieldConstraintInfos(model.Field{Name: "tags", Type: "string", IsRepeated: true})
	fieldMax := findConstraintInfo(t, infos, "default_max_size", "")
	if fieldMax.IntValue == nil || *fieldMax.IntValue != 1024 || fieldMax.Unit != "elements" || !fieldMax.Defaulted {
		t.Fatalf("default field max = %#v, want 1024 elements defaulted", fieldMax)
	}
	elementMax := findConstraintInfo(t, infos, "default_max_element_size", "")
	if elementMax.IntValue == nil || *elementMax.IntValue != 32768 || elementMax.Target != "element" || elementMax.Unit != "bytes" {
		t.Fatalf("default element max = %#v, want 32768 element bytes", elementMax)
	}
}

func findConstraintInfo(t *testing.T, infos []ConstraintDescriptor, rule, operator string) ConstraintDescriptor {
	t.Helper()
	for _, info := range infos {
		if info.Rule == rule && info.Operator == operator {
			return info
		}
	}
	t.Fatalf("missing constraint rule %q operator %q in %#v", rule, operator, infos)
	return ConstraintDescriptor{}
}

func TestBuildTypeDescriptorPreservesOneofMetadata(t *testing.T) {
	msg := model.Message{
		Name: "WatchEnvelope",
		Fields: []model.Field{
			{Name: "snapshot_item", Type: "Item", IsOneOf: true, OneOfName: "event"},
			{Name: "delta", Type: "Delta", IsOneOf: true, OneOfName: "event"},
			{
				Name: "cursor",
				Type: "string",
				Annotations: map[string]model.AnnotationValue{
					"store_key":    {Type: model.AnnotationTypeBool, BoolValue: true},
					"quorum_write": {Type: model.AnnotationTypeBool, BoolValue: true},
				},
			},
		},
	}

	got := BuildTypeDescriptor(msg, "events", nil, nil, map[string]bool{})
	byName := map[string]FieldDescriptor{}
	for _, field := range got.Fields {
		byName[field.Name] = field
	}

	if !byName["snapshot_item"].OneOf || byName["snapshot_item"].OneOfName != "event" {
		t.Fatalf("snapshot_item oneof metadata = (%v, %q), want (true, event)",
			byName["snapshot_item"].OneOf, byName["snapshot_item"].OneOfName)
	}
	if !byName["delta"].OneOf || byName["delta"].OneOfName != "event" {
		t.Fatalf("delta oneof metadata = (%v, %q), want (true, event)",
			byName["delta"].OneOf, byName["delta"].OneOfName)
	}
	if byName["cursor"].OneOf || byName["cursor"].OneOfName != "" {
		t.Fatalf("cursor oneof metadata = (%v, %q), want empty",
			byName["cursor"].OneOf, byName["cursor"].OneOfName)
	}
	if !byName["cursor"].StoreKey || !byName["cursor"].QuorumWrite {
		t.Fatalf("cursor store metadata = store_key %v quorum_write %v, want both true",
			byName["cursor"].StoreKey, byName["cursor"].QuorumWrite)
	}
}

func TestCollectRegistryTypesPreservesServiceStoreMetadata(t *testing.T) {
	files := []model.ProtoFile{
		{
			Package: "storepkg",
			RelPath: "services/store/cache.proto",
			Messages: []model.Message{
				{
					Name: "CredPair",
					Annotations: map[string]model.AnnotationValue{
						"service_store":       {Type: model.AnnotationTypeString, StringValue: "CredPair"},
						"service_store_label": {Type: model.AnnotationTypeString, StringSliceValue: []string{"auth"}},
					},
				},
			},
		},
	}

	got := CollectRegistryTypes(files)
	if len(got) != 1 {
		t.Fatalf("CollectRegistryTypes returned %d types, want 1", len(got))
	}
	if !got[0].ServiceStore || got[0].ServiceStoreName != "CredPair" || len(got[0].ServiceStoreLabels) != 1 || got[0].ServiceStoreLabels[0] != "auth" {
		t.Fatalf("service_store metadata = (%v, %q, %q), want (true, CredPair, auth)",
			got[0].ServiceStore, got[0].ServiceStoreName, got[0].ServiceStoreLabels)
	}
}

// A record with no (spec.record).key resolves RecordKey to the ONE canonical
// default (model.DefaultRecordKeyField), never empty; a declared key wins.
// Regression guard for NEW-6 (the resolved default was unpinned + hardcoded).
func TestCollectRegistryTypesResolvesRecordKeyDefault(t *testing.T) {
	files := []model.ProtoFile{
		{
			Package: "recpkg",
			RelPath: "vservices/sensor.proto",
			Messages: []model.Message{
				{
					Name: "DefaultKeyed",
					Annotations: map[string]model.AnnotationValue{
						"record": {Type: model.AnnotationTypeBool, BoolValue: true},
					},
				},
				{
					Name: "CustomKeyed",
					Annotations: map[string]model.AnnotationValue{
						"record":     {Type: model.AnnotationTypeBool, BoolValue: true},
						"record_key": {Type: model.AnnotationTypeString, StringValue: "trace_id"},
					},
				},
			},
		},
	}

	got := CollectRegistryTypes(files)
	byName := map[string]RegistryTypeInfo{}
	for _, rti := range got {
		byName[rti.TypeName] = rti
	}
	if byName["DefaultKeyed"].RecordKey != model.DefaultRecordKeyField {
		t.Fatalf("default RecordKey = %q, want %q", byName["DefaultKeyed"].RecordKey, model.DefaultRecordKeyField)
	}
	if byName["CustomKeyed"].RecordKey != "trace_id" {
		t.Fatalf("declared RecordKey = %q, want trace_id", byName["CustomKeyed"].RecordKey)
	}
}
