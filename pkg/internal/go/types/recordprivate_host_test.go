// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// The (spec.record).private owner pin is not generated for ANY language yet.
// A Go host of a private record's rows would therefore serve every owner's
// rows to every caller on ANY surface — unlike (spec.store_namespace), which
// Java/Rust still filter on a plain s2s surface, private has no such
// exception: generation refuses everywhere.

func goPrivateRowsSanitizer(private bool) *egressSanitizer {
	recAnn := map[string]model.AnnotationValue{"record": {BoolValue: true}}
	if private {
		recAnn["record_private"] = model.AnnotationValue{BoolValue: true}
	}
	return newEgressSanitizer([]model.ProtoFile{{Package: "profile", Messages: []model.Message{
		{Name: "ListRulesRequest", Fields: []model.Field{
			{Name: "page_size", Type: "int32", Number: 1},
		}},
		{Name: "ListRulesResponse", Fields: []model.Field{
			{Name: "rules", Type: "RuleNode", Number: 1, IsRepeated: true, IsMessage: true},
		}},
		{Name: "RuleNode", Annotations: recAnn,
			Fields: []model.Field{
				{Name: "namespace", Type: "string", Number: 1},
				{Name: "title", Type: "string", Number: 2},
			}},
	}}})
}

func goPrivateRowsMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:           "ListRules",
		InputType:      "ListRulesRequest",
		OutputType:     "ListRulesResponse",
		FullInputType:  "profile.ListRulesRequest",
		FullOutputType: "profile.ListRulesResponse",
	}
}

func goPrivateRowsWrapperSvc(t *testing.T) ServiceInfo {
	t.Helper()
	return ServiceInfo{
		ProtoFile:   "services/profile.proto",
		PackageName: "profile",
		DirName:     "profile",
		ServiceName: "ProfileApi",
		GrpcName:    "profile",
		ParentDir:   "services",
		Config:      &tactype.Config{OutputDir: t.TempDir()},
	}
}

func TestGoPrivateRecordC2SHostIsAGenerationError(t *testing.T) {
	info := &parser.ProtoInfo{
		Package: "profile",
		Services: []*parser.ServiceInfo{{
			Name:    "ProfileApi",
			IsC2S:   true,
			Methods: []*parser.MethodInfo{goPrivateRowsMethod()},
		}},
	}

	err := generateServiceWrapper(goPrivateRowsWrapperSvc(t), info, map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, goPrivateRowsSanitizer(true))
	if err == nil {
		t.Fatal("a Go c2s host of (spec.record).private rows generated a wrapper; the owner pin is not generated for any language yet")
	}
	if !strings.Contains(err.Error(), "spec.record).private") || !strings.Contains(err.Error(), "ProfileApi.ListRules") {
		t.Fatalf("generation error does not name the annotation and RPC it refused on: %v", err)
	}
}

// Unlike store_namespace, private has NO plain-s2s exception: the owner pin
// is missing there too, and a private record's whole point is that no
// service identity may read it unpinned, admin or peer alike.
func TestGoPrivateRecordPlainS2SIsAlsoAGenerationError(t *testing.T) {
	info := &parser.ProtoInfo{
		Package: "profile",
		Services: []*parser.ServiceInfo{{
			Name:    "ProfileApi",
			IsS2S:   true,
			Methods: []*parser.MethodInfo{goPrivateRowsMethod()},
		}},
	}

	err := generateServiceWrapper(goPrivateRowsWrapperSvc(t), info, map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, goPrivateRowsSanitizer(true))
	if err == nil {
		t.Fatal("a Go plain-s2s host of (spec.record).private rows generated a wrapper; private has no plain-s2s exception")
	}
	if !strings.Contains(err.Error(), "spec.record).private") {
		t.Fatalf("generation error does not name the annotation it refused on: %v", err)
	}
}

// Paired positive control: the IDENTICAL fixture WITHOUT private still
// generates on every surface — this refusal is private's alone.
func TestGoNonPrivateRecordStillGenerates(t *testing.T) {
	info := &parser.ProtoInfo{
		Package: "profile",
		Services: []*parser.ServiceInfo{{
			Name:    "ProfileApi",
			IsC2S:   true,
			Methods: []*parser.MethodInfo{goPrivateRowsMethod()},
		}},
	}

	if err := generateServiceWrapper(goPrivateRowsWrapperSvc(t), info, map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, goPrivateRowsSanitizer(false)); err != nil {
		t.Fatalf("a non-private record was refused by the private-record gate: %v", err)
	}
}
