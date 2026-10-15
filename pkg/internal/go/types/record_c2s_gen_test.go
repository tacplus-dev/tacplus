// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/templates"
)

// The generated handler behind the client-facing record read plane. Go twin of
// Rust's s2sm/record_c2s.rs: the author writes nothing for <Svc>Records.

func goRecordC2SFixture() *goRecordC2SFile {
	return &goRecordC2SFile{
		ModuleBasePath: "example.com/model",
		ServiceName:    "ScottyRecords",
		StructName:     "ScottyRecordsGeneratedHandler",
		HandlerType:    "ScottyRecordsHandler",
		SelfAlias:      "scotty_pb", SelfImport: "example.com/model/types/services/scotty",
		PlaneAlias: "query_pb", PlaneImport: "example.com/model/types/vservices/query",
		Methods: []goRecordC2SMethod{{
			RecordName: "ScottyActivity", RecordFQN: "scotty.ScottyActivity",
			GetMethodName: "GetScottyActivity", ListMethodName: "GetScottyActivityPage",
			GetReq: "GetScottyActivityRequest", GetResp: "GetScottyActivityResponse",
			ListReq: "GetScottyActivityPageRequest", ListResp: "GetScottyActivityPageResponse",
			FilterType: "ScottyActivityFilter", ReaderAccessor: "ScottyActivityRecordReader",
			KeyGoField: "ActivityId", PrincipalPinGoField: "OwnerSubject", NamespacePinGoField: "TenantNamespace",
			Criteria: []string{"UpdatedAtMsFrom", "UpdatedAtMsTo", "ActivityId"},
		}},
	}
}

func TestGoRecordC2SHandlerPinsCallerAndCopiesCriteria(t *testing.T) {
	out, err := renderGoRecordC2SPlane(goRecordC2SFixture())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	src := string(out)
	for _, want := range []string{
		"type ScottyRecordsGeneratedHandler struct {\n\tsm *ServiceManager\n}",
		`status.Error(codes.Unauthenticated, method+" requires an authenticated caller carrying a namespace claim")`,
		"namespace = caller.Token().GetNamespace()",
		"principal = caller.Token().GetSubject()",
		"func (g *ScottyRecordsGeneratedHandler) GetScottyActivity(ctx context.Context, caller *servicemanager.Caller, req *scotty_pb.GetScottyActivityRequest) (*scotty_pb.GetScottyActivityResponse, error)",
		"key := req.GetActivityId()",
		"ActivityId:      &key,",
		"OwnerSubject:    &principal,",
		"TenantNamespace: &namespace,",
		"g.sm.ScottyActivityRecordReader().List(ctx, filter, 1, \"\")",
		"UpdatedAtMsFrom: supplied.UpdatedAtMsFrom,",
		"UpdatedAtMsTo:   supplied.UpdatedAtMsTo,",
		"g.sm.ScottyActivityRecordReader().List(ctx, filter, req.GetPageSize(), req.GetPageToken())",
		"NextPageToken: next, TotalCount: total",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated plane handler missing %q:\n%s", want, src)
		}
	}
	// The request carries no namespace or principal field, so nothing the
	// client sends may reach the two pinned criteria.
	if strings.Contains(src, "supplied.OwnerSubject") || strings.Contains(src, "supplied.TenantNamespace") {
		t.Fatalf("pinned criteria must never be copied from the client's filter")
	}
}

func TestGoHandlerInterfaceOmitsGeneratedRecordC2SGetter(t *testing.T) {
	src := readGoTemplate(t, "go/types/handler.go.tmpl")
	if !strings.Contains(src, "{{- if .IsGeneratedRecordC2S}}") {
		t.Fatalf("handler.go.tmpl must skip the getter for the generated record read plane")
	}
	sm := goSMTemplate(t)
	if !strings.Contains(sm, "{{.WrapperVarName}} := {{.WrapperAlias}}.{{.ConstructorName}}(&{{.GeneratedImplStruct}}{sm: sm})") {
		t.Fatalf("service_manager.go.tmpl must bind the generated handler for the record read plane")
	}
}

func readGoTemplate(t *testing.T, path string) string {
	t.Helper()
	src, err := templates.FS.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(src)
}
