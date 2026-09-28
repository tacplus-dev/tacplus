package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

func TestGenerateRustExtractionHelpersFailsWhenSourceMissing(t *testing.T) {
	files := []model.ProtoFile{{
		Package: "projection",
		RelPath: "projection/metric.proto",
		Messages: []model.Message{{
			Name: "MetricProjection",
			Annotations: map[string]model.AnnotationValue{
				"timeseries_table_source": {StringValue: "events.MetricEvent"},
			},
		}},
	}}

	err := generateRustExtractionHelpers(files, t.TempDir())
	if err == nil {
		t.Fatal("generateRustExtractionHelpers succeeded with a missing source message")
	}
	if !strings.Contains(err.Error(), `Rust extraction source "events.MetricEvent" for projection projection.MetricProjection not found`) {
		t.Fatalf("generateRustExtractionHelpers error = %v, want missing source context", err)
	}
}
