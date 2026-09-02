// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/collection"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
)

// A PAGE method's response is the SAME kind of thing a unary response is —
// a message with a repeated field of (spec.record) rows — and until this fix
// only the unary branch spliced the (spec.store_principal)/(spec.store_namespace)
// egress filter into the response. This pins that the PAGE branch now gets the
// identical backstop, over the assembled page envelope (method.FullOutputType),
// the same way TestRustEgress_PageWrapper_SanitizesItems already pins the
// sanitize half of PAGE's egress handling.
func rustPrincipalNamespaceTestSanitizer() *rustEgressSanitizer {
	files := []model.ProtoFile{{Package: "catalog", Messages: []model.Message{
		{Name: "Item", Fields: []model.Field{
			{Name: "owner", Type: "string", Number: 1,
				Annotations: map[string]model.AnnotationValue{"store_principal": {Type: model.AnnotationTypeBool, BoolValue: true}}},
			{Name: "namespace", Type: "string", Number: 2,
				Annotations: map[string]model.AnnotationValue{"store_namespace": {Type: model.AnnotationTypeBool, BoolValue: true}}},
		}},
		{Name: "ListItemsPage", Fields: []model.Field{
			{Name: "items", Type: "Item", Number: 1, IsMessage: true, IsRepeated: true},
			{Name: "next_page_token", Type: "string", Number: 2},
		}},
	}}}
	return newRustEgressSanitizer(files)
}

func TestRustPageWrapperAppliesPrincipalAndNamespaceEgressFilter(t *testing.T) {
	m := &parser.MethodInfo{
		Name: "ListItems", InputType: "ListItemsRequest", OutputType: "ListItemsPage",
		FullInputType: "catalog.ListItemsRequest", FullOutputType: "catalog.ListItemsPage",
		Collection: &parser.CollectionDescriptor{Mode: "PAGE", OrderBy: []string{"id"}, DefaultPageSize: 50},
		Resolved: &collection.Resolved{
			Mode: "PAGE", OrderBy: []string{"id"}, DefaultPageSize: 50, MaxPageSize: 10000,
			PageItemType: "catalog.Item", PageItemsField: "items",
		},
	}
	got := mustGenerateRustGRPCWrapper(
		t,
		&parser.ServiceInfo{Name: "Catalog", IsS2S: true},
		m, "catalog", "services", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, rustPrincipalNamespaceTestSanitizer())

	for _, want := range []string{
		// (spec.store_principal): rows the caller does not own are dropped.
		"resp.items.retain(|__row| __row.owner == __read_principal);",
		// (spec.store_namespace): a foreign tenant's row aborts the process.
		"resp.items.retain(|__row| __row.namespace == __read_namespace);",
		"std::process::abort();",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("PAGE wrapper missing egress filter %q:\n%s", want, got)
		}
	}
	// Principal filter runs before namespace, same order as the unary leg.
	pIdx := strings.Index(got, "resp.items.retain(|__row| __row.owner")
	nIdx := strings.Index(got, "resp.items.retain(|__row| __row.namespace")
	if pIdx == -1 || nIdx == -1 || pIdx > nIdx {
		t.Fatalf("principal filter must precede namespace filter in the PAGE wrapper:\n%s", got)
	}
}
