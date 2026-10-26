// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
)

// Issue #130, Go serving side: every read RPC of a tenant-pinned collection
// resolves the pin after the caller is bound and filters what it returns (or
// every frame it streams); nothing else of the state service changes. The
// runtime behaviour is pinned by the tenant-slice fixture's self-test
// (cross-test/current-generation/testdata/go/tenant_pinned_read_test.go).

var pinBoundary = proto.TenantPinnedBoundary{
	Package: "keeper", StateRoot: "KeeperState", ProtoFile: "services/keeper.proto",
	Field: "tenant_configs", KeyField: "tenant_name", ConfigField: "config",
	Slices: []proto.TenantPinnedSlice{{Caller: "gateway", SliceField: "gateway"}, {Caller: "opa", SliceField: "opa"}},
}

func pinSanitizer() *egressSanitizer {
	san := newEgressSanitizer([]model.ProtoFile{{Package: "keeper"}})
	san.tenantPins = map[string]proto.TenantPinnedRPC{}
	for _, rpc := range pinBoundary.ReadRPCs() {
		san.tenantPins["keeper.KeeperStateService/"+rpc.Name] = rpc
	}
	return san
}

func pinMethod(name, in, out string, stream bool) *parser.MethodInfo {
	return &parser.MethodInfo{Name: name, InputType: in, OutputType: out,
		FullInputType: "keeper." + in, FullOutputType: "keeper." + out, ServerStreaming: stream}
}

func renderPinned(m *parser.MethodInfo) string {
	svc := &parser.ServiceInfo{Name: "KeeperStateService", IsS2S: true, Methods: []*parser.MethodInfo{m}}
	return generateGRPCWrapper(svc, m, "keeper", "services", "", map[string]service.AccountingInfo{},
		map[string]string{}, proto.SkippedTraceTypes{}, pinSanitizer())
}

func TestGoTenantPin_EveryReadRPCResolvesAndFilters(t *testing.T) {
	const resolve = "__pin, err := resolve{{.ServerName}}TenantPinTenantConfigs(caller)"
	for _, c := range []struct {
		m    *parser.MethodInfo
		want []string
	}{
		{pinMethod("GetTenantConfigs", "TenantConfigsGetRequest", "TenantConfigsValueResponse", false),
			[]string{`if !__pin.admits(req.GetKey()) {`, `codes.PermissionDenied`, "resp = __pin.filterTenantConfigsValue(resp)"}},
		{pinMethod("ContainsKeyTenantConfigs", "TenantConfigsGetRequest", "TenantConfigsValueResponse", false),
			[]string{`if !__pin.admits(req.GetKey()) {`, "resp = __pin.filterTenantConfigsValue(resp)"}},
		{pinMethod("GetAllTenantConfigs", "TenantConfigsGetAllRequest", "TenantConfigsEntriesResponse", false),
			[]string{"resp = __pin.filterTenantConfigsEntries(resp)"}},
		{pinMethod("KeySetTenantConfigs", "TenantConfigsKeysRequest", "TenantConfigsKeysResponse", false),
			[]string{"resp = __pin.filterTenantConfigsKeys(resp)"}},
		{pinMethod("SizeTenantConfigs", "TenantConfigsSizeRequest", "TenantConfigsSizeResponse", false),
			[]string{"p.handler.KeySetTenantConfigs(sanitizeContext(ctx), caller, &types.TenantConfigsKeysRequest{})",
				"resp = &types.TenantConfigsSizeResponse{Size: int32(len(__pin.filterTenantConfigsKeys(keys).GetKeys()))}"}},
		{pinMethod("QueryTenantConfigs", "TenantConfigsQueryRequest", "TenantConfigsQueryResponse", false),
			[]string{"resp = __pin.filterTenantConfigsQuery(resp)"}},
		{pinMethod("WatchTenantConfigs", "TenantConfigsGetAllRequest", "TenantConfigsEntriesResponse", true),
			[]string{"{{.ServerName}}TenantPinServerStream[types.TenantConfigsEntriesResponse]{ServerStreamingServer: ", "filter: __pin.filterTenantConfigsEntries}"}},
		{pinMethod("WatchQueryTenantConfigs", "TenantConfigsQueryRequest", "TenantConfigsQueryResponse", true),
			[]string{"{{.ServerName}}TenantPinServerStream[types.TenantConfigsQueryResponse]{ServerStreamingServer: ", "filter: __pin.filterTenantConfigsQuery}"}},
	} {
		got := renderPinned(c.m)
		wantAll(t, got, append([]string{resolve}, c.want...)...)
		// The pin is resolved from the verified caller, before the handler runs.
		if strings.Index(got, resolve) > strings.Index(got, "p.handler."+c.m.Name+"(") {
			t.Fatalf("%s: the pin must be resolved before the handler runs\n%s", c.m.Name, got)
		}
	}
}

func TestGoTenantPin_MutatorsAndOtherBoundariesAreUntouched(t *testing.T) {
	for _, m := range []*parser.MethodInfo{
		pinMethod("PutTenantConfigs", "TenantConfigsPutRequest", "TenantConfigsValueResponse", false),
		pinMethod("GetAllTenants", "TenantsGetAllRequest", "TenantsEntriesResponse", false),
	} {
		wantNone(t, renderPinned(m), "__pin", "TenantPin")
	}
}

func TestGoTenantPin_RefusesAShapeItDoesNotFilter(t *testing.T) {
	rpc := pinBoundary.ReadRPCs()[0] // a unary Get
	if err := goTenantPinShapeError(pinMethod(rpc.Name, "TenantConfigsGetRequest", "TenantConfigsValueResponse", true), rpc); err == nil {
		t.Fatal("a pinned read rendered as a stream has no filter here and must fail generation")
	}
}

func TestGoTenantPin_HelperFileStatesTheRuleAndFormats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state_service_tenant_pin.go")
	if err := writeGoTenantPinFile(path, "keeper", "KeeperStateService", "example.com/m/types/services/keeper",
		"example.com/m/chassis/servicemanager", pinBoundary.ReadRPCs()); err != nil {
		t.Fatalf("writeGoTenantPinFile: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, string(b),
		`slice, pinned := slices[path]`,
		`if ns == "/system" || strings.HasPrefix(ns, "/system"+"/") {`,
		`if t, ok := strings.CutPrefix(ns, "/tenants/"); ok && t != "" && !strings.Contains(t, "/") {`,
		`return nil, status.Errorf(codes.PermissionDenied,`,
		`map[string]string{"gateway": "gateway", "opa": "opa"}, "tenant_name", "config")`,
		`keeperStateServiceTenantPinKeepOnly(r, p.keyField, p.configField)`,
		`keeperStateServiceTenantPinKeepOnly(r.Mutable(cfd).Message(), p.sliceField)`,
		`r.SetUnknown(nil)`,
		`out.TotalCount = int32(len(out.Results))`,
	)
	if err := writeGoTenantPinFile(filepath.Join(t.TempDir(), "x.go"), "keeper", "KeeperStateService", "", "", nil); err != nil {
		t.Fatalf("no pins must write nothing: %v", err)
	}
}

// Issue #135: a grpc_config Config holding the tenant map. Its two reads,
// GetConfig and WatchConfig, resolve the pin and filter the Config; SetConfig
// is untouched. Runtime: testdata/go/tenant_pinned_config_read_test.go.

var pinConfigBoundary = proto.TenantPinnedBoundary{
	Package: "keeper", StateRoot: "KeeperConfigState", ProtoFile: "services/keeper.proto",
	Field: "config", MapField: "tenants", ConfigType: "KeeperConfig",
	Slices: []proto.TenantPinnedSlice{{Caller: "gateway", SliceField: "gateway"}},
}

func renderPinnedConfig(m *parser.MethodInfo) string {
	san := newEgressSanitizer([]model.ProtoFile{{Package: "keeper"}})
	san.tenantPins = map[string]proto.TenantPinnedRPC{}
	for _, rpc := range pinConfigBoundary.ReadRPCs() {
		san.tenantPins["keeper.KeeperConfigStateService/"+rpc.Name] = rpc
	}
	svc := &parser.ServiceInfo{Name: "KeeperConfigStateService", IsS2S: true, Methods: []*parser.MethodInfo{m}}
	return generateGRPCWrapper(svc, m, "keeper", "services", "", map[string]service.AccountingInfo{},
		map[string]string{}, proto.SkippedTraceTypes{}, san)
}

func TestGoTenantPin_ConfigReadsResolveAndFilterTheTenantMap(t *testing.T) {
	const resolve = "__pin, err := resolve{{.ServerName}}TenantPinConfig(caller)"
	get := renderPinnedConfig(pinMethod("GetConfig", "TraceRequest", "KeeperConfig", false))
	wantAll(t, get, resolve, "resp = __pin.filterConfigMap(resp)")
	if strings.Index(get, resolve) > strings.Index(get, "p.handler.GetConfig(") {
		t.Fatalf("the pin must be resolved before the handler runs\n%s", get)
	}
	wantAll(t, renderPinnedConfig(pinMethod("WatchConfig", "TraceRequest", "KeeperConfig", true)),
		resolve, "{{.ServerName}}TenantPinServerStream[types.KeeperConfig]{ServerStreamingServer: ", "filter: __pin.filterConfigMap}")
	wantNone(t, renderPinnedConfig(pinMethod("SetConfig", "KeeperConfig", "KeeperConfig", false)), "__pin", "TenantPin")
}

func TestGoTenantPin_ConfigHelperFilterStatesTheRule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config_state_service_tenant_pin.go")
	if err := writeGoTenantPinFile(path, "keeper", "KeeperConfigStateService", "example.com/m/types/services/keeper",
		"example.com/m/chassis/servicemanager", pinConfigBoundary.ReadRPCs()); err != nil {
		t.Fatalf("writeGoTenantPinFile: %v", err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	wantAll(t, string(b),
		`map[string]string{"gateway": "gateway"}, "", "")`,
		`pin.mapField = protoreflect.Name("tenants")`,
		`func (p *KeeperConfigStateServiceTenantPin) filterConfigMap(r *types.KeeperConfig) *types.KeeperConfig {`,
		`keeperConfigStateServiceTenantPinKeepOnly(m, p.mapField)`,
		`if !p.admits(k.String()) {`,
		`keeperConfigStateServiceTenantPinKeepOnly(v.Message(), p.sliceField)`,
	)
}
