// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

func javaRecordC2SFixture() *javaRecordC2SFile {
	return &javaRecordC2SFile{
		PackageBase: "com.example", ChassisBase: "com.example.chassis", SmPrefix: "s2sm",
		ServiceName: "ScottyRecords", ClassName: "ScottyRecordsGeneratedHandler",
		HandlerIfaceFqn:   "com.example.s2sm.internal.grpc.servers.services.scotty.ScottyRecords.ScottyRecordsHandler",
		SelfOuter:         "com.example.types.services.scotty.Scotty",
		PlaneOuter:        "com.example.types.vservices.query.Query",
		PageResultFqn:     "com.example.chassis.collection.CollectionRuntime.PageResult",
		ProtoExceptionFqn: "com.example.types.sys.errors.ProtoException",
		Methods: []javaRecordC2SMethod{{
			RecordName: "ScottyActivity", RecordFQN: "scotty.ScottyActivity",
			RecordFqn:     "com.example.types.services.scotty.Scotty.ScottyActivity",
			GetMethodName: "getScottyActivity", ListMethodName: "getScottyActivityPage",
			GetRpcName: "GetScottyActivity", ListRpcName: "GetScottyActivityPage",
			GetReq: "GetScottyActivityRequest", GetResp: "GetScottyActivityResponse",
			ListReq: "GetScottyActivityPageRequest", ListResp: "GetScottyActivityPageResponse",
			FilterType: "ScottyActivityFilter", ReaderAccessor: "scottyActivityRecordReader",
			KeyGetter: "getActivityId", KeySetter: "setActivityId",
			PrincipalPinSetter: "setOwnerSubject", NamespacePinSetter: "setTenantNamespace",
			Criteria: []string{"UpdatedAtMsFrom", "UpdatedAtMsTo", "ActivityId"},
		}},
	}
}

func TestJavaRecordC2SHandlerPinsCallerAndCopiesCriteria(t *testing.T) {
	out, err := renderJavaRecordC2SPlane(javaRecordC2SFixture())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	src := string(out)
	for _, want := range []string{
		"public final class ScottyRecordsGeneratedHandler implements com.example.s2sm.internal.grpc.servers.services.scotty.ScottyRecords.ScottyRecordsHandler {",
		`throw new ProtoException(ProtoException.UNAUTHENTICATED,
                    method + " requires an authenticated caller carrying a namespace claim");`,
		"public com.example.types.services.scotty.Scotty.GetScottyActivityResponse getScottyActivity(Caller caller, com.example.types.services.scotty.Scotty.GetScottyActivityRequest request) throws ProtoException",
		".setActivityId(request.getActivityId())",
		".setOwnerSubject(principal)",
		".setTenantNamespace(namespace)",
		"sm.scottyActivityRecordReader().list(filter, 1, \"\")",
		"if (supplied.hasUpdatedAtMsFrom()) {\n            filter.setUpdatedAtMsFrom(supplied.getUpdatedAtMsFrom());",
		"sm.scottyActivityRecordReader().list(filter.build(), request.getPageSize(), request.getPageToken())",
		".setTotalCount(page.total)",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("generated plane handler missing %q:\n%s", want, src)
		}
	}
	if strings.Contains(src, "supplied.getOwnerSubject") || strings.Contains(src, "supplied.getTenantNamespace") {
		t.Fatalf("pinned criteria must never be copied from the client's filter")
	}
}

func TestJavaHandlerOmitsGeneratedRecordC2SGetterAndSMBindsIt(t *testing.T) {
	entry := javaGrpcServiceEntry{
		GetterName: "getScottyRecordsGrpcImpl", ServiceName: "ScottyRecords", HandlerType: "ScottyRecordsHandler",
		ServerClassName: "ScottyRecords", WrapperVarName: "wrapperScottyRecords",
		IsGeneratedRecordC2S: true, GeneratedImplClass: "ScottyRecordsGeneratedHandler",
	}
	data := javaSmTemplateData{
		PackageBase: "com.example", ChassisBase: "com.example.chassis", TypesBase: "com.example.types",
		SmPrefix: "s2sm", ServiceLabel: "scotty", HasGrpc: true, GrpcServices: []javaGrpcServiceEntry{entry},
	}
	handler := renderJavaTemplateForTest(t, "Handler.java.tmpl", data)
	if strings.Contains(handler, "getScottyRecordsGrpcImpl()") {
		t.Fatalf("Handler.java must not ask the author for the generated record read plane:\n%s", handler)
	}
	sm := renderJavaTemplateForTest(t, "ServiceManager.java.tmpl", data)
	if !strings.Contains(sm, "new ScottyRecords(new ScottyRecordsGeneratedHandler(this), authorizer)") {
		t.Fatalf("ServiceManager.java must bind the generated handler for the record read plane")
	}
}
