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

// The (spec.store_namespace) egress row filter is generated in the JAVA and
// RUST service wrappers. A Go host of a record-bearing read renders none, so it
// would return every tenant's rows to every authenticated caller — generation
// fails instead, on the same gate Java emits the filter under.

// goPinnedRowsSanitizer is one read response carrying repeated pinned rows.
func goPinnedRowsSanitizer() *egressSanitizer {
	return newEgressSanitizer([]model.ProtoFile{{Package: "tasks", Messages: []model.Message{
		{Name: "ListTasksRequest", Fields: []model.Field{
			{Name: "page_size", Type: "int32", Number: 1},
		}},
		{Name: "ListTasksResponse", Fields: []model.Field{
			{Name: "tasks", Type: "Task", Number: 1, IsRepeated: true, IsMessage: true},
		}},
		{Name: "Task", Annotations: map[string]model.AnnotationValue{"record": {BoolValue: true}},
			Fields: []model.Field{
				{Name: "namespace", Type: "string", Number: 1,
					Annotations: map[string]model.AnnotationValue{"store_namespace": {BoolValue: true}}},
				{Name: "title", Type: "string", Number: 2},
			}},
	}}})
}

func goPinnedRowsMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:           "ListTasks",
		InputType:      "ListTasksRequest",
		OutputType:     "ListTasksResponse",
		FullInputType:  "tasks.ListTasksRequest",
		FullOutputType: "tasks.ListTasksResponse",
	}
}

func goPinnedRowsWrapperSvc(t *testing.T) ServiceInfo {
	t.Helper()
	return ServiceInfo{
		ProtoFile:   "services/tasks.proto",
		PackageName: "tasks",
		DirName:     "tasks",
		ServiceName: "TaskApi",
		GrpcName:    "tasks",
		ParentDir:   "services",
		Config:      &tactype.Config{OutputDir: t.TempDir()},
	}
}

func TestGoStoreNamespaceC2SHostIsAGenerationError(t *testing.T) {
	info := &parser.ProtoInfo{
		Package: "tasks",
		Services: []*parser.ServiceInfo{{
			Name:    "TaskApi",
			IsC2S:   true,
			Methods: []*parser.MethodInfo{goPinnedRowsMethod()},
		}},
	}

	err := generateServiceWrapper(goPinnedRowsWrapperSvc(t), info, map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, goPinnedRowsSanitizer())
	if err == nil {
		t.Fatal("a Go c2s host of (spec.store_namespace) rows generated a wrapper; the read would return every tenant's rows")
	}
	if !strings.Contains(err.Error(), "spec.store_namespace") || !strings.Contains(err.Error(), "TaskApi.ListTasks") {
		t.Fatalf("generation error does not name the annotation and RPC it refused on: %v", err)
	}
}

func TestGoStoreNamespaceRecordPlaneS2SHostIsAGenerationError(t *testing.T) {
	info := &parser.ProtoInfo{
		Package: "tasks",
		Services: []*parser.ServiceInfo{{
			Name:            "TaskApi",
			SynthesizedKind: parser.SynthesizedKindRecordMap,
			Methods:         []*parser.MethodInfo{goPinnedRowsMethod()},
		}},
	}

	err := generateServiceWrapper(goPinnedRowsWrapperSvc(t), info, map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, goPinnedRowsSanitizer())
	if err == nil {
		t.Fatal("a Go host of the synthesized record plane generated a wrapper over pinned rows")
	}
	if !strings.Contains(err.Error(), "spec.store_namespace") || !strings.Contains(err.Error(), "TaskApi.ListTasks") {
		t.Fatalf("generation error does not name the annotation and RPC it refused on: %v", err)
	}
}

// The gate is Java's, not a blanket refusal: a plain s2s service returning the
// same rows is legitimate — Java emits no row filter there either — so it must
// still generate.
func TestGoStoreNamespacePlainS2SStillGenerates(t *testing.T) {
	info := &parser.ProtoInfo{
		Package: "tasks",
		Services: []*parser.ServiceInfo{{
			Name:    "TaskApi",
			IsS2S:   true,
			Methods: []*parser.MethodInfo{goPinnedRowsMethod()},
		}},
	}

	if err := generateServiceWrapper(goPinnedRowsWrapperSvc(t), info, map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, goPinnedRowsSanitizer()); err != nil {
		t.Fatalf("a plain s2s Go service over record rows was refused; the refusal gate is wider than Java's emission gate: %v", err)
	}
}
