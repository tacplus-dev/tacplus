// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
)

// (spec.record).min_group_size is enforced in the generated JAVA service
// wrapper. The record plane is hosted by a Java service today; a Rust host would
// serve Get<T>Count / Subscribe<T>Count / Update<T>Query with no guard at all.
// That must fail the build, not ship the disclosure — the same fail-closed
// stance this file already takes for (spec.store_principal) on a shape its
// egress filter cannot reach.
func TestRustMinGroupSizeHostIsAGenerationError(t *testing.T) {
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
		ServiceName: "RecordQuery",
		GrpcName:    "query",
		IsGrpc:      true,
		OutputDir:   t.TempDir(),
		ParentDir:   "vservices",
	}

	err := generateServiceWrapper(svc, info, map[string]string{}, map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, nil)
	if err == nil {
		t.Fatal("a Rust host of a min_group_size record generated a wrapper; the size-only RPCs would be unguarded")
	}
	if !strings.Contains(err.Error(), "spec.min_group_size") {
		t.Fatalf("generation error does not name the annotation it refused on: %v", err)
	}
	if !strings.Contains(err.Error(), "RecordQuery.GetTaskActivityCount") {
		t.Fatalf("generation error does not name the RPC it refused on: %v", err)
	}
	// The refusal must be reached before anything else this generator would
	// need (a full tactype.Config among it), so the build fails on the guard
	// rather than on incidental plumbing.
}
