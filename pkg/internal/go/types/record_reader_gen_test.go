// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

// Go twin of rust/types/record_reader_gen_test.go: the typed readers a service
// gets over the stored rows of the records it declared (spec.record_read) for.

func goMessageReaderEntry() goRecordReaderEntry {
	return goRecordReaderEntry{
		StructName: "MessageRecordReader", AccessorName: "MessageRecordReader",
		RecordFQN: "messaging.Message", RecordName: "Message",
		RecordAlias: "messaging_pb", PlaneAlias: "query_pb", ClientAlias: "query_gc",
		ClientStruct: "RecordMapClient", GroupAccessor: "Vservices", ClientFn: "RecordMapClient",
		KeyField: "delivery_id", KeyGoField: "DeliveryId", KeyGoType: "string",
		CreateFn: "CreateMessageQuery", ListFn: "ListMessagePage", DeleteFn: "DeleteMessageQuery",
		FilterType: "MessageFilter", OpenReqType: "OpenMessageQueryRequest", PullReqType: "PullMessageRequest",
		HasKeyFilter: true,
	}
}

func goPrivateReaderEntry() goRecordReaderEntry {
	e := goMessageReaderEntry()
	e.StructName, e.AccessorName = "SecretRecordReader", "SecretRecordReader"
	e.RecordFQN, e.RecordName = "messaging.Secret", "Secret"
	e.ClientStruct, e.ClientFn = "RecordQueryClient", "RecordQueryClient"
	e.KeyField, e.KeyGoField = "secret_id", "SecretId"
	e.CreateFn, e.ListFn, e.DeleteFn = "CreateSecretQuery", "ListSecretPage", "DeleteSecretQuery"
	e.FilterType, e.OpenReqType, e.PullReqType = "SecretFilter", "OpenSecretQueryRequest", "PullSecretRequest"
	return e
}

func goReaderFile(entries ...goRecordReaderEntry) goRecordReaderFile {
	return goRecordReaderFile{
		RecordReaders: entries,
		Imports: []goRecordReaderImport{
			{Alias: "query_gc", Path: "example.com/model/s2sm/grpc/clients/vservices/query"},
			{Alias: "messaging_pb", Path: "example.com/model/types/services/messaging"},
			{Alias: "query_pb", Path: "example.com/model/types/vservices/query"},
		},
	}
}

func renderGoReaders(t *testing.T, data goRecordReaderFile) string {
	t.Helper()
	out, err := renderGoRecordReaders(data)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return string(out)
}

func TestGoRecordReadersRenderPerRecordPlaneClient(t *testing.T) {
	out := renderGoReaders(t, goReaderFile(goMessageReaderEntry(), goPrivateReaderEntry()))
	for _, want := range []string{
		"type MessageRecordReader struct {\n\tclient *query_gc.RecordMapClient\n}",
		"type SecretRecordReader struct {\n\tclient *query_gc.RecordQueryClient\n}",
		`query_gc "example.com/model/s2sm/grpc/clients/vservices/query"`,
		"func NewMessageRecordReader(client *query_gc.RecordMapClient) *MessageRecordReader",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated readers missing %q:\n%s", want, out)
		}
	}
}

func TestGoRecordReaderRendersGetAndList(t *testing.T) {
	out := renderGoReaders(t, goReaderFile(goMessageReaderEntry()))
	for _, want := range []string{
		"func (r *MessageRecordReader) Get(ctx context.Context, key string) (*messaging_pb.Message, error)",
		"filter := &query_pb.MessageFilter{DeliveryId: &k}",
		"func (r *MessageRecordReader) List(ctx context.Context, filter *query_pb.MessageFilter, pageSize int32, pageToken string) ([]*messaging_pb.Message, string, int64, error)",
		"r.client.CreateMessageQuery(ctx, &query_pb.OpenMessageQueryRequest{",
		"r.client.ListMessagePage(ctx, &query_pb.PullMessageRequest{",
		"r.client.DeleteMessageQuery(ctx, &query_pb.QueryHandle{QueryId: handle.GetQueryId()})",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated reader missing %q:\n%s", want, out)
		}
	}
}

func TestGoRecordReaderReleasesHandleOnFailurePath(t *testing.T) {
	out := renderGoReaders(t, goReaderFile(goMessageReaderEntry()))
	pull := strings.Index(out, "pullErr := r.client.ListMessagePage(")
	release := strings.Index(out, "_, _ = r.client.DeleteMessageQuery(")
	check := strings.Index(out, "if pullErr != nil {")
	if !(pull >= 0 && pull < release && release < check) {
		t.Fatalf("the handle must be released before the List result is inspected (pull=%d release=%d check=%d)", pull, release, check)
	}
}

func TestGoRecordReaderOmitsGetWithoutKeyFilter(t *testing.T) {
	e := goMessageReaderEntry()
	e.HasKeyFilter = false
	out := renderGoReaders(t, goReaderFile(e))
	if strings.Contains(out, ") Get(") {
		t.Fatalf("a record whose key is not a filter criterion must not get a point read:\n%s", out)
	}
	if !strings.Contains(out, ") List(") {
		t.Fatalf("List must still be generated")
	}
}

func TestGoRecordReaderNonStringKey(t *testing.T) {
	e := goMessageReaderEntry()
	e.KeyField, e.KeyGoField, e.KeyGoType = "seq", "Seq", "int64"
	out := renderGoReaders(t, goReaderFile(e))
	if !strings.Contains(out, "Get(ctx context.Context, key int64)") || !strings.Contains(out, "MessageFilter{Seq: &k}") {
		t.Fatalf("non-string key must be typed and passed by address:\n%s", out)
	}
}

func TestGoRecordReaderThreadsIncludeDeleted(t *testing.T) {
	e := goMessageReaderEntry()
	e.HasDeletedFlag = true
	out := renderGoReaders(t, goReaderFile(e))
	for _, want := range []string{
		"Get(ctx context.Context, key string, includeDeleted bool)",
		"pageToken string, includeDeleted bool) ([]*messaging_pb.Message, string, int64, error)",
		"IncludeDeleted: includeDeleted,",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated reader missing %q:\n%s", want, out)
		}
	}
	plain := renderGoReaders(t, goReaderFile(goMessageReaderEntry()))
	if strings.Contains(plain, "includeDeleted") {
		t.Fatalf("a record without (spec.deleted_flag) must not expose include_deleted")
	}
}

func TestGoServiceManagerExposesRecordReaderAccessor(t *testing.T) {
	sm := goSMTemplate(t)
	for _, want := range []string{
		"{{- if .HasRecordReaders}}",
		"func (sm *ServiceManager) {{.AccessorName}}() *{{.StructName}} {",
		"return New{{.StructName}}(sm.Grpc().Clients().{{.GroupAccessor}}().{{.ClientFn}}())",
	} {
		if !strings.Contains(sm, want) {
			t.Fatalf("service_manager missing record reader accessor %q", want)
		}
	}
}
