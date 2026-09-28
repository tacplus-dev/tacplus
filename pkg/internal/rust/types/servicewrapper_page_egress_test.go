// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/collection"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/proto"
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
		&parser.ServiceInfo{Name: "Catalog", IsC2S: true},
		m, "catalog", "services", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, rustPrincipalNamespaceTestSanitizer())

	for _, want := range []string{
		// (spec.store_principal): rows the caller does not own are dropped.
		"resp.items.retain(|__row| __row.owner == __read_principal);",
		// (spec.store_namespace): a foreign tenant's row aborts the process,
		// unless it is in a namespace the caller's token is linked to.
		"resp.items.retain(|__row| __row.namespace == __read_namespace || __read_namespace_linked.iter().any(|__ns| __ns == &__row.namespace));",
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

// namespacePageMethod is the PAGE method the three gate arms below render. It is
// the same fixture the c2s test above uses; only the SERVICE differs.
func namespacePageMethodRust() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name: "ListItems", InputType: "ListItemsRequest", OutputType: "ListItemsPage",
		FullInputType: "catalog.ListItemsRequest", FullOutputType: "catalog.ListItemsPage",
		Collection: &parser.CollectionDescriptor{Mode: "PAGE", OrderBy: []string{"id"}, DefaultPageSize: 50},
		Resolved: &collection.Resolved{
			Mode: "PAGE", OrderBy: []string{"id"}, DefaultPageSize: 50, MaxPageSize: 10000,
			PageItemType: "catalog.Item", PageItemsField: "items",
		},
	}
}

func namespaceUnaryMethodRust() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name: "SampleItems", InputType: "SampleItemsRequest", OutputType: "ListItemsPage",
		FullInputType: "catalog.SampleItemsRequest", FullOutputType: "catalog.ListItemsPage",
	}
}

// renderNamespaceServiceRust renders one method through the FULL wrapper for a
// service of the given shape. recordPlane is the synthesized RecordMap s2s
// plane; shared is a plain s2s service.
func renderNamespaceServiceRust(t *testing.T, m *parser.MethodInfo, svc *parser.ServiceInfo) string {
	t.Helper()
	return mustGenerateRustGRPCWrapper(
		t, svc, m, "catalog", "services", map[string]string{}, "",
		map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, rustPrincipalNamespaceTestSanitizer())
}

func rustRecordPlaneS2SService() *parser.ServiceInfo {
	return &parser.ServiceInfo{Name: "Catalog", IsS2S: true, SynthesizedKind: parser.SynthesizedKindRecordMap}
}

// The s2s record plane carries the seam under a RUNTIME gate: the filter is
// emitted, and __ns_pinned — read from the caller's own claim — is what decides
// whether it drops anything. Both the PAGE and unary seams.
func TestRustTenantS2S_RecordPlaneEmitsTheRuntimeGate(t *testing.T) {
	for _, m := range []*parser.MethodInfo{namespacePageMethodRust(), namespaceUnaryMethodRust()} {
		got := renderNamespaceServiceRust(t, m, rustRecordPlaneS2SService())
		for _, want := range []string{
			`let __ns_pinned = !__read_namespace.starts_with("/system");`,
			"resp.items.retain(|__row| !__ns_pinned || __row.namespace == __read_namespace || __read_namespace_linked.iter().any(|__ns| __ns == &__row.namespace));",
			"std::process::abort();",
		} {
			if !strings.Contains(got, want) {
				t.Fatalf("%s: the s2s record plane must carry %q:\n%s", m.Name, want, got)
			}
		}
		// The gate is read from the caller's token, never from the host's config
		// or a request field.
		if strings.Contains(got, "tenant_owned") {
			t.Fatalf("%s: the gate must be caller-keyed, not host-keyed:\n%s", m.Name, got)
		}
	}
}

// A plain s2s service is not the record plane and emits NOTHING — byte-identical
// to what it generated before the seam existed. This is the emission gate, and
// it is what keeps an aborting tenant filter off every non-record-plane peer
// surface in the fleet.
func TestRustTenantS2S_SharedServiceEmitsNoNamespaceSeam(t *testing.T) {
	for _, m := range []*parser.MethodInfo{namespacePageMethodRust(), namespaceUnaryMethodRust()} {
		shared := renderNamespaceServiceRust(t, m, &parser.ServiceInfo{Name: "Catalog", IsS2S: true})
		for _, forbidden := range []string{"__ns_pinned", "__read_namespace", "(spec.store_namespace)"} {
			if strings.Contains(shared, forbidden) {
				t.Fatalf("%s: a shared service's s2s wrapper must not carry %s\n%s", m.Name, forbidden, shared)
			}
		}
		if plane := renderNamespaceServiceRust(t, m, rustRecordPlaneS2SService()); plane == shared {
			t.Fatalf("%s: the record-plane s2s wrapper must differ from the shared one", m.Name)
		}
	}
}

// And the c2s half pins unconditionally, with no runtime gate to get wrong.
func TestRustTenantS2S_C2SHalfHasNoRuntimeGate(t *testing.T) {
	for _, m := range []*parser.MethodInfo{namespacePageMethodRust(), namespaceUnaryMethodRust()} {
		got := renderNamespaceServiceRust(t, m, &parser.ServiceInfo{Name: "Catalog", IsC2S: true})
		if !strings.Contains(got, "resp.items.retain(|__row| __row.namespace == __read_namespace || __read_namespace_linked.iter().any(|__ns| __ns == &__row.namespace));") {
			t.Fatalf("%s: the c2s wrapper must pin unconditionally:\n%s", m.Name, got)
		}
		if strings.Contains(got, "__ns_pinned") || strings.Contains(got, `starts_with("/system")`) {
			t.Fatalf("%s: the c2s wrapper must carry no runtime gate:\n%s", m.Name, got)
		}
	}
}
