package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

func TestGenerateGraphHelpersFailsWhenPredicateTargetMissingHelpers(t *testing.T) {
	files := []model.ProtoFile{{
		Package: "graph",
		RelPath: "graph/session.proto",
		Messages: []model.Message{{
			Name: "SessionNode",
			Annotations: map[string]model.AnnotationValue{
				"graph_type": {},
			},
			Fields: []model.Field{{
				Name:      "agent",
				Type:      "AgentNode",
				IsMessage: true,
			}},
		}},
	}}

	err := generateGraphHelpers(files, t.TempDir())
	if err == nil {
		t.Fatal("generateGraphHelpers succeeded with an unresolved predicate target")
	}
	if !strings.Contains(err.Error(), "predicate field agent references message AgentNode without graph helpers") {
		t.Fatalf("generateGraphHelpers error = %v, want unresolved predicate target context", err)
	}
}
