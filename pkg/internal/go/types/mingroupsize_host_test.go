// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// (spec.record).min_group_size is enforced in the generated JAVA service
// wrapper. A Go host of a record query plane would serve the size-only RPCs
// (Get<T>Count, Subscribe<T>Count, Update<T>Query) with no guard at all, so
// generation fails rather than shipping the disclosure silently.
func TestGoMinGroupSizeHostIsAGenerationError(t *testing.T) {
	method := &parser.MethodInfo{
		Name:           "GetTaskActivityCount",
		InputType:      "QueryHandle",
		OutputType:     "RecordCount",
		FullInputType:  "query.QueryHandle",
		FullOutputType: "query.RecordCount",
		MinGroupSize:   5,
	}
	info := &parser.ProtoInfo{
		Package:  "query",
		Services: []*parser.ServiceInfo{{Name: "RecordQuery", Methods: []*parser.MethodInfo{method}}},
	}
	svc := ServiceInfo{
		ProtoFile:   "vservices/query.proto",
		PackageName: "query",
		DirName:     "query",
		ServiceName: "RecordQuery",
		GrpcName:    "query",
		ParentDir:   "vservices",
		Config:      &tactype.Config{OutputDir: t.TempDir()},
	}

	err := generateServiceWrapper(svc, info, map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, nil)
	if err == nil {
		t.Fatal("a Go host of a min_group_size record generated a wrapper; the size-only RPCs would be unguarded")
	}
	if !strings.Contains(err.Error(), "spec.min_group_size") || !strings.Contains(err.Error(), "RecordQuery.GetTaskActivityCount") {
		t.Fatalf("generation error does not name the annotation and RPC it refused on: %v", err)
	}
}
