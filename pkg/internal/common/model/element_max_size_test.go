// Copyright (c) 2026 Second Star Cavalier LLC

package model

import (
	"strings"
	"testing"
)

// All five validation emitters route (spec.assert_element_max_size) through
// these two helpers, so the accepted field shapes and the gen-time rejection
// are pinned once here.
func TestElementMaxSizeBytes(t *testing.T) {
	tests := []struct {
		name  string
		field Field
		want  int64
	}{
		{"repeated string", Field{Type: "string", IsRepeated: true, Annotations: map[string]AnnotationValue{
			"assert_element_max_size": {Type: AnnotationTypeInt, IntValue: 253},
		}}, 253},
		{"absent", Field{Type: "string", IsRepeated: true, Annotations: map[string]AnnotationValue{
			"assert_max_size": {Type: AnnotationTypeInt, IntValue: 32},
		}}, 0},
		{"no annotations", Field{Type: "string", IsRepeated: true}, 0},
		{"non-integer", Field{Type: "string", IsRepeated: true, Annotations: map[string]AnnotationValue{
			"assert_element_max_size": {Type: AnnotationTypeString, StringValue: "253"},
		}}, 0},
		{"zero", Field{Type: "string", IsRepeated: true, Annotations: map[string]AnnotationValue{
			"assert_element_max_size": {Type: AnnotationTypeInt, IntValue: 0},
		}}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.field.ElementMaxSizeBytes(); got != tc.want {
				t.Fatalf("ElementMaxSizeBytes() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestValidateElementMaxSize(t *testing.T) {
	intAnn := map[string]AnnotationValue{"assert_element_max_size": {Type: AnnotationTypeInt, IntValue: 253}}

	tests := []struct {
		name    string
		field   Field
		wantErr bool
	}{
		{"repeated string", Field{Name: "domains", Type: "string", IsRepeated: true, Annotations: intAnn}, false},
		{"repeated bytes", Field{Name: "blobs", Type: "bytes", IsRepeated: true, Annotations: intAnn}, false},
		{"map string value", Field{Name: "labels", IsMap: true, MapKeyType: "string", MapValueType: "string", Annotations: intAnn}, false},
		{"map bytes value", Field{Name: "blobs", IsMap: true, MapKeyType: "string", MapValueType: "bytes", Annotations: intAnn}, false},
		{"absent", Field{Name: "name", Type: "string"}, false},
		{"singular string", Field{Name: "name", Type: "string", Annotations: intAnn}, true},
		{"optional string", Field{Name: "name", Type: "string", IsOptional: true, Annotations: intAnn}, true},
		{"repeated int32", Field{Name: "ports", Type: "int32", IsRepeated: true, Annotations: intAnn}, true},
		{"repeated message", Field{Name: "items", Type: "Item", IsRepeated: true, IsMessage: true, Annotations: intAnn}, true},
		{"map int64 value", Field{Name: "counters", IsMap: true, MapKeyType: "string", MapValueType: "int64", Annotations: intAnn}, true},
		{"non-integer value", Field{Name: "domains", Type: "string", IsRepeated: true, Annotations: map[string]AnnotationValue{
			"assert_element_max_size": {Type: AnnotationTypeString, StringValue: "253"},
		}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.field.ValidateElementMaxSize("M")
			if tc.wantErr && err == nil {
				t.Fatalf("ValidateElementMaxSize() = nil, want an error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("ValidateElementMaxSize() = %v, want nil", err)
			}
			if err != nil && !strings.Contains(err.Error(), "M."+tc.field.Name) {
				t.Fatalf("error does not name the field: %v", err)
			}
		})
	}
}
