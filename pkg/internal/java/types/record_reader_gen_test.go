// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

// Java twin of rust/types/record_reader_gen_test.go.

func javaMessageReaderEntry() javaRecordReaderEntry {
	return javaRecordReaderEntry{
		PackageBase: "com.example", SmPrefix: "s2sm",
		ReaderClass: "MessageRecordReader", AccessorName: "messageRecordReader",
		RecordFQN:     "messaging.Message",
		RecordFqn:     "com.example.types.services.messaging.Messaging.Message",
		PlaneOuter:    "com.example.types.vservices.query.Query",
		ClientFqn:     "com.example.s2sm.internal.grpc.clients.vservices.query.RecordMapGrpcClient",
		ClientChain:   "vservices().recordMap()",
		PageResultFqn: "com.example.chassis.collection.CollectionRuntime.PageResult",
		KeyField:      "delivery_id", KeySetter: "setDeliveryId", KeyJavaType: "String",
		CreateFn: "createMessageQuery", ListFn: "listMessagePage", DeleteFn: "deleteMessageQuery", RowsFn: "getMessageRows", RowsReqType: "GetMessageRowsRequest",
		FilterType: "MessageFilter", OpenReqType: "OpenMessageQueryRequest", PullReqType: "PullMessageRequest",
		HasKeyFilter: true,
	}
}

func renderJavaReader(t *testing.T, e javaRecordReaderEntry) string {
	t.Helper()
	out, err := renderJavaRecordReader(e)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return string(out)
}

func TestJavaRecordReaderRendersGetAndList(t *testing.T) {
	out := renderJavaReader(t, javaMessageReaderEntry())
	for _, want := range []string{
		"package com.example.s2sm;",
		"public final class MessageRecordReader {",
		"private final com.example.s2sm.internal.grpc.clients.vservices.query.RecordMapGrpcClient client;",
		"public Optional<com.example.types.services.messaging.Messaging.Message> get(String key)",
		"com.example.types.vservices.query.Query.MessageFilter.newBuilder().setDeliveryId(key).build()",
		"public com.example.chassis.collection.CollectionRuntime.PageResult<com.example.types.services.messaging.Messaging.Message> list(com.example.types.vservices.query.Query.MessageFilter filter, int pageSize, String pageToken)",
		"client.createMessageQuery(open.build())",
		"client.listMessagePage(com.example.types.vservices.query.Query.PullMessageRequest.newBuilder()",
		"client.deleteMessageQuery(com.example.types.vservices.query.Query.QueryHandle.newBuilder().setQueryId(handle.getQueryId()).build())",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated reader missing %q:\n%s", want, out)
		}
	}
}

func TestJavaRecordReaderReleasesHandleOnFailurePath(t *testing.T) {
	out := renderJavaReader(t, javaMessageReaderEntry())
	pull := strings.Index(out, "return client.listMessagePage(")
	finally := strings.Index(out, "} finally {")
	release := strings.Index(out, "client.deleteMessageQuery(")
	if !(pull >= 0 && pull < finally && finally < release) {
		t.Fatalf("the handle must be released in a finally block around the List (pull=%d finally=%d release=%d)", pull, finally, release)
	}
}

func TestJavaRecordReaderOmitsGetWithoutKeyFilter(t *testing.T) {
	e := javaMessageReaderEntry()
	e.HasKeyFilter = false
	out := renderJavaReader(t, e)
	if strings.Contains(out, "> get(") {
		t.Fatalf("a record whose key is not a filter criterion must not get a point read")
	}
	if !strings.Contains(out, "> list(") {
		t.Fatalf("list must still be generated")
	}
}

func TestJavaRecordReaderNonStringKey(t *testing.T) {
	e := javaMessageReaderEntry()
	e.KeyField, e.KeySetter, e.KeyJavaType = "seq", "setSeq", "long"
	out := renderJavaReader(t, e)
	if !strings.Contains(out, "get(long key)") || !strings.Contains(out, ".setSeq(key)") {
		t.Fatalf("non-string key must be typed:\n%s", out)
	}
}

func TestJavaRecordReaderThreadsIncludeDeleted(t *testing.T) {
	e := javaMessageReaderEntry()
	e.HasDeletedFlag = true
	out := renderJavaReader(t, e)
	for _, want := range []string{"get(String key, boolean includeDeleted)", "String pageToken, boolean includeDeleted)", "open.setIncludeDeleted(includeDeleted);"} {
		if !strings.Contains(out, want) {
			t.Errorf("generated reader missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(renderJavaReader(t, javaMessageReaderEntry()), "includeDeleted") {
		t.Fatalf("a record without (spec.deleted_flag) must not expose include_deleted")
	}
}

func TestJavaScalarKeyType(t *testing.T) {
	for in, want := range map[string]string{"string": "String", "int64": "long", "uint32": "int", "bool": "boolean"} {
		if got := javaScalarKeyType(in); got != want {
			t.Errorf("javaScalarKeyType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJavaServiceManagerExposesRecordReaderAccessor(t *testing.T) {
	sm := renderJavaTemplateForTest(t, "ServiceManager.java.tmpl", javaSmTemplateData{
		PackageBase: "com.example", ChassisBase: "com.example.chassis", TypesBase: "com.example.types",
		SmPrefix: "s2sm", ServiceLabel: "scotty", HasGrpc: true, HasGrpcS2SClients: true,
		RecordReaders: []javaRecordReaderEntry{javaMessageReaderEntry()}, HasRecordReaders: true,
	})
	if !strings.Contains(sm, "public MessageRecordReader messageRecordReader() {\n        return new MessageRecordReader(grpcClients.vservices().recordMap());\n    }") {
		t.Fatalf("ServiceManager must expose the record reader accessor")
	}
}

// On a plane routed by tenant (#207) every method takes the target tenant
// first and sets the route field on every request it sends: the Rows read, and
// the Create, List and Delete of a query alike.
func TestJavaRecordReaderRoutedSetsTheTenantOnEveryCall(t *testing.T) {
	e := javaMessageReaderEntry()
	e.Routed, e.RouteSetter = true, "setTargetTenant"
	out := renderJavaReader(t, e)
	for _, want := range []string{
		"get(String tenant, String key)",
		"req.setTargetTenant(tenant);",
		"list(String tenant, com.example.types.vservices.query.Query.MessageFilter filter, int pageSize, String pageToken)",
		"return queryPage(tenant, filter,",
		"queryPage(String tenant, com.example.types.vservices.query.Query.MessageFilter filter,",
		"open.setTargetTenant(tenant);",
		".setPageToken(pageToken)\n                    .setTargetTenant(tenant)\n                    .build());",
		"QueryHandle.newBuilder().setQueryId(handle.getQueryId()).setTargetTenant(tenant).build()",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("routed reader missing %q:\n%s", want, out)
		}
	}
	if n := strings.Count(out, "setTargetTenant(tenant)"); n != 4 {
		t.Errorf("want the route field set on all 4 requests, got %d:\n%s", n, out)
	}
	if strings.Contains(renderJavaReader(t, javaMessageReaderEntry()), "tenant") {
		t.Errorf("an unrouted reader takes no tenant")
	}
}
