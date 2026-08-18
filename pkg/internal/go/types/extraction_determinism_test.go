// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// TestGoGraphExtractionOneofVariantIsDeterministic is a GEN-DET-1 regression
// guard. A graph projection whose projection_source fields reference more than
// one oneof variant picked the variant for the emitted nil guard by ranging the
// oneofVariants map: the guard the extractor compiles against — and therefore
// which events the projection extracts at RUNTIME — changed from build to
// build. The comment claimed "use the first one"; a map has no first. It is now
// the first in proto field-declaration order.
func TestGoGraphExtractionOneofVariantIsDeterministic(t *testing.T) {
	const runs = 20

	projMsg := model.Message{
		Name:        "OrderNode",
		Annotations: map[string]model.AnnotationValue{"graph_type": {Type: model.AnnotationTypeBool, BoolValue: true}},
		Fields: []model.Field{
			projectionField("order_id", "zebra_placed.order_id"),
			projectionField("customer_id", "apple_cancelled.customer_id"),
		},
	}
	sourceMsg := model.Message{Name: "OrderEvent"}

	for i := 0; i < runs; i++ {
		data, err := buildExtractionTemplateData(projMsg, "orders", sourceMsg, "orders", nil)
		if err != nil {
			t.Fatalf("buildExtractionTemplateData: %v", err)
		}
		if !data.IsConditional {
			t.Fatalf("expected a conditional (graph_type + oneof) extraction, got %+v", data)
		}
		// Declaration order, not map order: the first projection_source field
		// references zebra_placed.
		if data.OneofVariantSnake != "zebra_placed" {
			t.Fatalf("run %d: oneof variant is %q, want the first in field-declaration order (zebra_placed)", i, data.OneofVariantSnake)
		}
	}
}

func projectionField(name, source string) model.Field {
	return model.Field{
		Name: name,
		Type: "string",
		Annotations: map[string]model.AnnotationValue{
			"projection_source": {Type: model.AnnotationTypeString, StringValue: source},
		},
	}
}
