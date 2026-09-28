// Copyright (c) 2026 Second Star Cavalier LLC

package boundaries

import (
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

func TestCollectionSafetyLimitFallsBackToTheSharedDefault(t *testing.T) {
	cases := map[string]struct {
		annotations map[string]model.AnnotationValue
		want        int64
	}{
		"declared": {
			annotations: map[string]model.AnnotationValue{
				"eviction_max_size": {Type: model.AnnotationTypeInt, IntValue: 25},
			},
			want: 25,
		},
		"absent":         {annotations: nil, want: DefaultCollectionSafetyLimit},
		"declared zero":  {annotations: map[string]model.AnnotationValue{"eviction_max_size": {Type: model.AnnotationTypeInt}}, want: DefaultCollectionSafetyLimit},
		"declared minus": {annotations: map[string]model.AnnotationValue{"eviction_max_size": {Type: model.AnnotationTypeInt, IntValue: -1}}, want: DefaultCollectionSafetyLimit},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := CollectionSafetyLimit(parser.FieldDescriptor{Name: "players", Annotations: tc.annotations})
			if got != tc.want {
				t.Fatalf("CollectionSafetyLimit = %d, want %d", got, tc.want)
			}
		})
	}
}
