// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"math"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

func timeseriesMsg(fieldAnn map[string]model.AnnotationValue, dedupKeyFields []string) model.ProtoFile {
	msgAnn := map[string]model.AnnotationValue{
		"timeseries_type": {Type: model.AnnotationTypeBool, BoolValue: true},
	}
	if dedupKeyFields != nil {
		msgAnn["timeseries_dedup"] = model.AnnotationValue{
			TimeseriesDedup: &model.DedupPolicyEntry{KeyFields: dedupKeyFields},
		}
	}
	return model.ProtoFile{
		Package: "test",
		Messages: []model.Message{
			{
				Name:        "Record",
				Annotations: msgAnn,
				Fields: []model.Field{
					{
						Name:        "occurred_at_ms",
						Annotations: fieldAnn,
					},
				},
			},
		},
	}
}

func TestValidateTimeseriesAnnotations_OrderKeyConflictsWithDedupKeyFields(t *testing.T) {
	pf := timeseriesMsg(map[string]model.AnnotationValue{
		"timeseries_column":    {Type: model.AnnotationTypeBool, BoolValue: true},
		"timeseries_order_key": {Type: model.AnnotationTypeInt, IntValue: 1},
	}, []string{"activity_id"})

	err := ValidateTimeseriesAnnotations([]model.ProtoFile{pf})
	if err == nil {
		t.Fatal("expected error for order_key alongside dedup.key_fields, got nil")
	}
	if !strings.Contains(err.Error(), "conflicts with dedup.key_fields") {
		t.Errorf("error = %q, want it to mention the order_key/key_fields conflict", err.Error())
	}
}

func TestValidateTimeseriesAnnotations_DedupKeyFieldsWithoutOrderKeyIsFine(t *testing.T) {
	pf := timeseriesMsg(map[string]model.AnnotationValue{
		"timeseries_column": {Type: model.AnnotationTypeBool, BoolValue: true},
	}, []string{"activity_id"})

	if err := ValidateTimeseriesAnnotations([]model.ProtoFile{pf}); err != nil {
		t.Errorf("expected no error when dedup.key_fields is set and no order_key is present, got %v", err)
	}
}

func TestValidateTimeseriesAnnotations_OrderKeyWithoutDedupIsFine(t *testing.T) {
	pf := timeseriesMsg(map[string]model.AnnotationValue{
		"timeseries_column":    {Type: model.AnnotationTypeBool, BoolValue: true},
		"timeseries_order_key": {Type: model.AnnotationTypeInt, IntValue: 1},
	}, nil)

	if err := ValidateTimeseriesAnnotations([]model.ProtoFile{pf}); err != nil {
		t.Errorf("expected no error for order_key without dedup, got %v", err)
	}
}

// order_key is an int32: a key past it (only expressible in a model built
// outside protoc) is refused by name rather than wrapped into another sort-key
// position in the registry.
func TestValidateTimeseriesAnnotations_OrderKeyPastInt32IsRefused(t *testing.T) {
	for _, key := range []int64{math.MaxInt32 + 1, 1<<32 + 1} {
		pf := timeseriesMsg(map[string]model.AnnotationValue{
			"timeseries_column":    {Type: model.AnnotationTypeBool, BoolValue: true},
			"timeseries_order_key": {Type: model.AnnotationTypeInt, IntValue: key},
		}, nil)
		err := ValidateTimeseriesAnnotations([]model.ProtoFile{pf})
		if err == nil {
			t.Fatalf("order_key %d: expected an error, got nil", key)
		}
		if !strings.Contains(err.Error(), "test.Record.occurred_at_ms") ||
			!strings.Contains(err.Error(), "order_key") || !strings.Contains(err.Error(), "cannot exceed 2147483647") {
			t.Errorf("order_key %d: error = %q, want it to name the field, the annotation and the bound", key, err.Error())
		}
	}
}
