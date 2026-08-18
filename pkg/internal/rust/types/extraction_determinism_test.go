// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// TestRustGraphExtractionOneofVariantIsDeterministic is the rust leg of the
// same GEN-DET-1 guard as the Go emitter's: a graph projection referencing more
// than one oneof variant picked the variant for the emitted match arm by
// ranging a map, so the extractor's runtime behavior changed from build to
// build. It is now the first variant in proto field-declaration order.
func TestRustGraphExtractionOneofVariantIsDeterministic(t *testing.T) {
	const runs = 20

	projMsg := model.Message{
		Name:        "OrderNode",
		Annotations: map[string]model.AnnotationValue{"graph_type": {Type: model.AnnotationTypeBool, BoolValue: true}},
		Fields: []model.Field{
			rustProjectionField("order_id", "zebra_placed.order_id"),
			rustProjectionField("customer_id", "apple_cancelled.customer_id"),
		},
	}
	sourceMsg := model.Message{Name: "OrderEvent"}

	for i := 0; i < runs; i++ {
		data, err := buildRustExtractionTemplateData(projMsg, "orders", sourceMsg, "orders", nil)
		if err != nil {
			t.Fatalf("buildRustExtractionTemplateData: %v", err)
		}
		if !data.IsConditional {
			t.Fatalf("expected a conditional (graph_type + oneof) extraction, got %+v", data)
		}
		if data.OneofVariantSnake != "zebra_placed" {
			t.Fatalf("run %d: oneof variant is %q, want the first in field-declaration order (zebra_placed)", i, data.OneofVariantSnake)
		}
	}
}

func rustProjectionField(name, source string) model.Field {
	return model.Field{
		Name: name,
		Type: "string",
		Annotations: map[string]model.AnnotationValue{
			"projection_source": {Type: model.AnnotationTypeString, StringValue: source},
		},
	}
}
