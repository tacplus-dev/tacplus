// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
)

// Issue #130, Java serving side: the twin of the Go wrapper's render test. The
// runtime behaviour is pinned by the tenant-slice fixture's self-test
// (cross-test/current-generation/testdata/java/TenantPinnedReadTest.java).

var javaPinBoundary = proto.TenantPinnedBoundary{
	Package: "keeper", StateRoot: "KeeperState", ProtoFile: "services/keeper.proto",
	Field: "tenant_configs", KeyField: "tenant_name", ConfigField: "config",
	Slices: []proto.TenantPinnedSlice{{Caller: "gateway", SliceField: "gateway"}},
}

func javaPinSanitizer() *javaEgressSanitizer {
	san := newJavaEgressSanitizer([]model.ProtoFile{{Package: "keeper"}})
	san.tenantPins = map[string]proto.TenantPinnedRPC{}
	for _, rpc := range javaPinBoundary.ReadRPCs() {
		san.tenantPins["keeper.KeeperStateService/"+rpc.Name] = rpc
	}
	return san
}

func renderJavaPinned(name, in, out string, stream bool) string {
	m := &parser.MethodInfo{Name: name, InputType: in, OutputType: out,
		FullInputType: "keeper." + in, FullOutputType: "keeper." + out, ServerStreaming: stream}
	return generateJavaGRPCWrapperSurface(m, "KeeperStateService", "keeper", "Keeper."+in, "Keeper."+out,
		"Helpers", "Helpers", false, false, map[string]service.AccountingInfo{}, "keeper", "services",
		map[string]string{}, proto.SkippedTraceTypes{}, javaPinSanitizer(), nil, nil, false, false)
}

func TestJavaTenantPin_EveryReadRPCResolvesAndFilters(t *testing.T) {
	const resolve = "final TenantPin __pin = TenantPin.resolveTenantConfigs(Auth.verifiedTokenFromContext());"
	for _, c := range []struct {
		name, in, out string
		stream        bool
		want          []string
	}{
		{"GetTenantConfigs", "TenantConfigsGetRequest", "TenantConfigsValueResponse", false,
			[]string{"if (!TenantPin.admits(__pin, request.getKey())) {", "ProtoException.newErrPermissionDenied(", "setValue(TenantPin.project(__pin, response.getValue()))"}},
		{"ContainsKeyTenantConfigs", "TenantConfigsGetRequest", "TenantConfigsValueResponse", false,
			[]string{"if (!TenantPin.admits(__pin, request.getKey())) {", "setValue(TenantPin.project(__pin, response.getValue()))"}},
		{"GetAllTenantConfigs", "TenantConfigsGetAllRequest", "TenantConfigsEntriesResponse", false,
			[]string{"response = TenantPin.filterEntries(__pin, response);"}},
		{"KeySetTenantConfigs", "TenantConfigsKeysRequest", "TenantConfigsKeysResponse", false,
			[]string{"clearKeys().addAllKeys(TenantPin.filterKeys(__pin, response.getKeysList()))"}},
		{"SizeTenantConfigs", "TenantConfigsSizeRequest", "TenantConfigsSizeResponse", false,
			[]string{"handler.keySetTenantConfigs(caller, Keeper.TenantConfigsKeysRequest.getDefaultInstance())",
				"setSize(TenantPin.filterKeys(__pin, __keys.getKeysList()).size())"}},
		{"QueryTenantConfigs", "TenantConfigsQueryRequest", "TenantConfigsQueryResponse", false,
			[]string{"response = TenantPin.filterQuery(__pin, response);"}},
		{"WatchTenantConfigs", "TenantConfigsGetAllRequest", "TenantConfigsEntriesResponse", true,
			[]string{"TenantPin.filtering(validatingResponseObserver(", "value -> TenantPin.filterEntries(__pin, value))"}},
		{"WatchQueryTenantConfigs", "TenantConfigsQueryRequest", "TenantConfigsQueryResponse", true,
			[]string{"TenantPin.filtering(validatingResponseObserver(", "value -> TenantPin.filterQuery(__pin, value))"}},
	} {
		got := renderJavaPinned(c.name, c.in, c.out, c.stream)
		for _, w := range append([]string{resolve}, c.want...) {
			if !strings.Contains(got, w) {
				t.Fatalf("%s: wrapper missing %q\n%s", c.name, w, got)
			}
		}
		handler := "handler." + strings.ToLower(c.name[:1]) + c.name[1:] + "("
		if strings.Index(got, resolve) > strings.Index(got, handler) {
			t.Fatalf("%s: the pin must be resolved before the handler runs\n%s", c.name, got)
		}
		// The filter runs before the response is sent (unary) — on a stream it
		// is the observer the handler sends into.
		if !c.stream && strings.Index(got, "__pin, response") > strings.Index(got, "responseObserver.onNext(response);") {
			t.Fatalf("%s: the filter must run before the response is sent\n%s", c.name, got)
		}
	}
}

func TestJavaTenantPin_MutatorsAndOtherBoundariesAreUntouched(t *testing.T) {
	for _, got := range []string{
		renderJavaPinned("PutTenantConfigs", "TenantConfigsPutRequest", "TenantConfigsValueResponse", false),
		renderJavaPinned("GetAllTenants", "TenantsGetAllRequest", "TenantsEntriesResponse", false),
	} {
		if strings.Contains(got, "TenantPin") {
			t.Fatalf("only the pinned collection's reads are filtered\n%s", got)
		}
	}
}

func TestJavaTenantPin_HelperClassStatesTheRule(t *testing.T) {
	got := javaTenantPinClass(javaPinBoundary.ReadRPCs())
	for _, w := range []string{
		`resolve(token, java.util.Map.ofEntries(java.util.Map.entry("gateway", "gateway")), "tenant_name", "config", "")`,
		`String slice = slices.get(path);`,
		`if (ns.equals("/system") || ns.startsWith("/system" + "/")) {`,
		`if (ns.startsWith("/tenants/")) {`,
		`if (!t.isEmpty() && t.indexOf('/') < 0) {`,
		`keepOnly(b, p.keyField, p.configField);`,
		`keepOnly(cb, p.sliceField);`,
		`b.setUnknownFields(com.google.protobuf.UnknownFieldSet.getDefaultInstance());`,
		`b.setField(b.getDescriptorForType().findFieldByName("total_count"), kept.size());`,
	} {
		if !strings.Contains(got, w) {
			t.Fatalf("TenantPin helper missing %q\n%s", w, got)
		}
	}
	if javaTenantPinClass(nil) != "" {
		t.Fatal("a wrapper with no pinned RPC carries no TenantPin helper")
	}
	m := &parser.MethodInfo{Name: "GetTenantConfigs", ServerStreaming: true}
	if err := javaTenantPinShapeError(m, javaPinBoundary.ReadRPCs()[0]); err == nil {
		t.Fatal("a pinned read rendered as a stream has no filter here and must fail generation")
	}
}

// Issue #135: a grpc_config Config holding the tenant map. Runtime:
// testdata/java/TenantPinnedConfigReadTest.java.

func TestJavaTenantPin_ConfigReadsResolveAndFilterTheTenantMap(t *testing.T) {
	cfg := proto.TenantPinnedBoundary{
		Package: "keeper", StateRoot: "KeeperConfigState", ProtoFile: "services/keeper.proto",
		Field: "config", MapField: "tenants", ConfigType: "KeeperConfig",
		Slices: []proto.TenantPinnedSlice{{Caller: "gateway", SliceField: "gateway"}},
	}
	san := newJavaEgressSanitizer([]model.ProtoFile{{Package: "keeper"}})
	san.tenantPins = map[string]proto.TenantPinnedRPC{}
	for _, rpc := range cfg.ReadRPCs() {
		san.tenantPins["keeper.KeeperConfigStateService/"+rpc.Name] = rpc
	}
	render := func(name, in string, stream bool) string {
		m := &parser.MethodInfo{Name: name, InputType: in, OutputType: "KeeperConfig",
			FullInputType: "keeper." + in, FullOutputType: "keeper.KeeperConfig", ServerStreaming: stream}
		return generateJavaGRPCWrapperSurface(m, "KeeperConfigStateService", "keeper", "Keeper."+in, "Keeper.KeeperConfig",
			"Helpers", "Helpers", false, false, map[string]service.AccountingInfo{}, "keeper", "services",
			map[string]string{}, proto.SkippedTraceTypes{}, san, nil, nil, false, false)
	}
	const resolve = "final TenantPin __pin = TenantPin.resolveConfig(Auth.verifiedTokenFromContext());"
	for _, c := range []struct {
		got  string
		want string
	}{
		{render("GetConfig", "TraceRequest", false), "response = TenantPin.filterConfigMap(__pin, response);"},
		{render("WatchConfig", "TraceRequest", true), "value -> TenantPin.filterConfigMap(__pin, value))"},
	} {
		for _, w := range []string{resolve, c.want} {
			if !strings.Contains(c.got, w) {
				t.Fatalf("wrapper missing %q\n%s", w, c.got)
			}
		}
	}
	if got := render("SetConfig", "KeeperConfig", false); strings.Contains(got, "__pin") || strings.Contains(got, "TenantPin") {
		t.Fatalf("SetConfig is a write and must not be pinned\n%s", got)
	}
	class := javaTenantPinClass(cfg.ReadRPCs())
	for _, w := range []string{`resolveConfig(TokenWrapper token)`, `"tenants"`, `static <R extends com.google.protobuf.Message> R filterConfigMap(TenantPin p, R response)`, `keepOnly(b, p.mapField);`} {
		if !strings.Contains(class, w) {
			t.Fatalf("TenantPin helper missing %q\n%s", w, class)
		}
	}
}
