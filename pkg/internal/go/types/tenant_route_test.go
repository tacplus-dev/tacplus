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
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// Issue #170: every rpc of a tenant-owned service declares the request field
// carrying the TARGET tenant id ((spec.rpc).route_field); that field alone
// decides which tenant's instance receives the call. Go's share: the typed
// client routes by it, the server wrapper backstops it, the authorizer learns
// it (and RPC_FLAG_ANY_TENANT) from the wrapper's registration, and the
// ServiceManager keeps per-tenant pools for a tenant-routed target. A platform
// service is unchanged.

func crewFixtureFiles() []model.ProtoFile {
	return []model.ProtoFile{{
		Package: "crew",
		Messages: []model.Message{
			{Name: "HailRequest", Fields: []model.Field{
				{Name: "target_tenant", Type: "string", Number: 1},
				{Name: "text", Type: "string", Number: 2},
			}},
			{Name: "HailResponse", Fields: []model.Field{
				{Name: "ok", Type: "bool", Number: 1},
			}},
		},
	}}
}

func crewMethod(name string, clientStreaming, serverStreaming bool) *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:            name,
		InputType:       "HailRequest",
		OutputType:      "HailResponse",
		FullInputType:   "crew.HailRequest",
		FullOutputType:  "crew.HailResponse",
		ClientStreaming: clientStreaming,
		ServerStreaming: serverStreaming,
		RouteField:      &parser.RouteFieldDescriptor{Field: "target_tenant", JSONName: "targetTenant", Number: 1},
	}
}

func crewWrapper(t *testing.T, method *parser.MethodInfo) string {
	t.Helper()
	svc := &parser.ServiceInfo{Name: "Crew", Methods: []*parser.MethodInfo{method}}
	return generateGRPCWrapper(svc, method, "crew", "services", "crew",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, newEgressSanitizer(crewFixtureFiles()))
}

// The unary wrapper of a routed rpc runs the routing backstop after request
// validation and before the handler; a platform rpc does not.
func TestGoWrapperRoutedUnaryChecksRouteBeforeHandler(t *testing.T) {
	src := crewWrapper(t, crewMethod("Hail", false, false))
	check := strings.Index(src, `p.checkRoute(`)
	validate := strings.Index(src, "ValidateHailRequest(req)")
	handler := strings.Index(src, "p.handler.Hail(")
	if check < 0 || validate < 0 || handler < 0 || !(validate < check && check < handler) {
		t.Fatalf("routed unary must validate, then check the route, then call the handler (validate=%d check=%d handler=%d):\n%s",
			validate, check, handler, src)
	}

	platform := crewMethod("Hail", false, false)
	platform.RouteField = nil
	if src := crewWrapper(t, platform); strings.Contains(src, "checkRoute") {
		t.Fatalf("a platform rpc must not carry the routing backstop:\n%s", src)
	}
}

// A server-streaming routed rpc checks its one request before the handler.
func TestGoWrapperRoutedServerStreamChecksRoute(t *testing.T) {
	src := crewWrapper(t, crewMethod("Watch", false, true))
	check := strings.Index(src, `p.checkRoute(`)
	handler := strings.Index(src, "p.handler.Watch(")
	if check < 0 || handler < 0 || check > handler {
		t.Fatalf("routed server stream must check the route before the handler:\n%s", src)
	}
}

// Client-streaming and bidi routed rpcs check EVERY inbound message: the
// stream wrapper is always installed and carries the per-message route check.
func TestGoWrapperRoutedRequestStreamsCheckEveryMessage(t *testing.T) {
	for _, m := range []*parser.MethodInfo{crewMethod("Upload", true, false), crewMethod("Chat", true, true)} {
		src := crewWrapper(t, m)
		if !strings.Contains(src, "routeReq: func(req *types.HailRequest) error { return p.checkRoute(") {
			t.Fatalf("%s: request stream must check the route of every message:\n%s", m.Name, src)
		}
	}
	platform := crewMethod("Chat", true, true)
	platform.RouteField = nil
	if src := crewWrapper(t, platform); strings.Contains(src, "checkRoute") {
		t.Fatalf("a platform request stream must not carry the routing backstop:\n%s", src)
	}
}

// The wrapper registers the route field and RPC_FLAG_ANY_TENANT with the
// authorizer, and wires its backstop through a func the ServiceManager sets.
func TestGoWrapperRegistersRouteFieldAndAnyTenant(t *testing.T) {
	svc := approvalFixtureSvc(t)
	svc.PackageName, svc.DirName, svc.ServiceName, svc.GrpcName, svc.ProtoFile = "crew", "crew", "Crew", "crew", "services/crew.proto"
	routed := crewMethod("Hail", false, false)
	open := crewMethod("Visit", false, false)
	open.AnyTenant = true
	info := &parser.ProtoInfo{Package: "crew", Services: []*parser.ServiceInfo{{Name: "Crew", Methods: []*parser.MethodInfo{routed, open}}}}
	if err := generateServiceWrapper(svc, info, map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, newEgressSanitizer(crewFixtureFiles())); err != nil {
		t.Fatalf("generateServiceWrapper: %v", err)
	}
	src := readWrapper(t, svc)
	for _, want := range []string{
		`auth.SetMethodRouteField("crew.Crew/Hail", "target_tenant")`,
		`auth.SetMethodRouteField("crew.Crew/Visit", "target_tenant")`,
		`auth.SetMethodAnyTenant("crew.Crew/Visit")`,
		"func (p *Crew) SetRouteCheckFunc(f func(fullMethod string, req any) error)",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("wrapper is missing %q:\n%s", want, src)
		}
	}
	if strings.Contains(src, `auth.SetMethodAnyTenant("crew.Crew/Hail")`) {
		t.Fatalf("an rpc without RPC_FLAG_ANY_TENANT must keep the default door:\n%s", src)
	}

	anyUser, hasMethodAuth := deriveGoServiceAuthFlags(parser.ServiceDescriptor{
		Methods: []parser.MethodDescriptor{{Name: "Hail", RouteField: routed.RouteField}},
	})
	if anyUser || !hasMethodAuth {
		t.Fatalf("a routed rpc registers its route field, so the service has method auth (anyUser=%v hasMethodAuth=%v)", anyUser, hasMethodAuth)
	}
	if !goServiceHasTenantRoute(parser.ServiceDescriptor{Methods: []parser.MethodDescriptor{{Name: "Hail", RouteField: routed.RouteField}}}) {
		t.Fatal("a routed rpc makes its service need the routing backstop")
	}
	if goServiceHasTenantRoute(parser.ServiceDescriptor{Methods: []parser.MethodDescriptor{{Name: "Get"}}}) {
		t.Fatal("a platform service has no routing backstop")
	}
}

func readWrapper(t *testing.T, svc ServiceInfo) string {
	t.Helper()
	dir := filepath.Join(svc.Config.OutputDir, svc.Config.GetSmPrefix(), "grpc", "servers", svc.ParentDir, svc.ImportDirName())
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read wrapper dir: %v", err)
	}
	var b strings.Builder
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".go") {
			src, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			b.Write(src)
		}
	}
	return b.String()
}

// The typed client of a routed unary rpc reads the route field, refuses a
// missing or malformed tid before any network call, and sends to that
// tenant's instance; a platform rpc's client is unchanged.
func TestGoClientRoutedUnaryRoutesByTheField(t *testing.T) {
	src, err := generateGrpcClientMethod(&parser.ServiceInfo{Name: "Crew"}, crewMethod("Hail", false, false), "crew", "services")
	if err != nil {
		t.Fatal(err)
	}
	validate := strings.Index(src, "ValidateHailRequest(req)")
	route := strings.Index(src, `serviceconnection.RouteTenant("target_tenant", req.GetTargetTenant())`)
	with := strings.Index(src, "ctx = serviceconnection.WithRouteTenant(ctx, tenant)")
	call := strings.Index(src, "c.client.Hail(")
	if validate < 0 || route < 0 || with < 0 || call < 0 || !(validate < route && route < with && with < call) {
		t.Fatalf("routed unary client must validate, route, then call (validate=%d route=%d with=%d call=%d):\n%s", validate, route, with, call, src)
	}

	platform := crewMethod("Hail", false, false)
	platform.RouteField = nil
	src, err = generateGrpcClientMethod(&parser.ServiceInfo{Name: "Crew"}, platform, "crew", "services")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(src, "serviceconnection.") {
		t.Fatalf("a platform client method must not route by tenant:\n%s", src)
	}
}

// Every request-carrying shape routes: server streaming and PAGE read the
// field off their one request.
func TestGoClientRoutedServerStreamRoutes(t *testing.T) {
	src, err := generateGrpcClientMethod(&parser.ServiceInfo{Name: "Crew"}, crewMethod("Watch", false, true), "crew", "services")
	if err != nil {
		t.Fatal(err)
	}
	route := strings.Index(src, `serviceconnection.RouteTenant("target_tenant", req.GetTargetTenant())`)
	call := strings.Index(src, "c.client.Watch(")
	if route < 0 || call < 0 || route > call {
		t.Fatalf("routed server stream client must route before the call:\n%s", src)
	}
}

// A client-streaming routed rpc fixes its tenant at open: the open method takes
// it explicitly, refuses a non-tid before dialling, and refuses a message whose
// route field names another tenant.
func TestGoClientRoutedClientStreamFixesTenantAtOpen(t *testing.T) {
	src, err := generateGrpcClientMethod(&parser.ServiceInfo{Name: "Crew"}, crewMethod("Upload", true, false), "crew", "services")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"func (c *{{.ClientName}}) Upload(ctx context.Context, tenant string, opts ...grpc.CallOption)",
		`serviceconnection.RouteTenant("tenant", tenant)`,
		"ctx = serviceconnection.WithRouteTenant(ctx, tenant)",
		"routeReq:",
		"req.GetTargetTenant() != tenant",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("client-streaming routed open is missing %q:\n%s", want, src)
		}
	}
	if route, open := strings.Index(src, `serviceconnection.RouteTenant("tenant", tenant)`), strings.Index(src, "c.client.Upload("); route > open {
		t.Fatalf("the tenant must be checked before the stream opens:\n%s", src)
	}
}

// A bidi routed rpc fixes its tenant at open the same way.
func TestGoClientRoutedBidiFixesTenantAtOpen(t *testing.T) {
	src, err := generateGrpcClientMethod(&parser.ServiceInfo{Name: "Crew"}, crewMethod("Chat", true, true), "crew", "services")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"tenant string,",
		`serviceconnection.RouteTenant("tenant", tenant)`,
		"callCtx = serviceconnection.WithRouteTenant(callCtx, h.tenant)",
		"msg.GetTargetTenant() != h.tenant",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("bidi routed open is missing %q:\n%s", want, src)
		}
	}
	platform := crewMethod("Chat", true, true)
	platform.RouteField = nil
	src, err = generateGrpcClientMethod(&parser.ServiceInfo{Name: "Crew"}, platform, "crew", "services")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(src, "serviceconnection.") || strings.Contains(src, "tenant string") {
		t.Fatalf("a platform bidi open must be unchanged:\n%s", src)
	}
}

// The ServiceManager keeps per-tenant pools for a tenant-routed s2s target and
// wires each routed wrapper's backstop to the authorizer.
func TestGoServiceManagerTemplateRoutesTenantTargets(t *testing.T) {
	sm := goSMTemplate(t)
	for _, want := range []string{
		`s2sTenantConnections["{{.}}"] = serviceconnection.NewTenantDynamic("{{.}}", sc.ServiceDialOpts("{{.}}"))`,
		`s2sChannels["{{.}}"] = serviceconnection.NewTenantChannelProvider(s2sTenantConnections["{{.}}"])`,
		"for svcID, conn := range s2sTenantConnections {",
		"{{.WrapperVarName}}.SetRouteCheckFunc(sm.auth.CheckRoute)",
	} {
		if !strings.Contains(sm, want) {
			t.Fatalf("service manager template is missing %q", want)
		}
	}
}

// A target is tenant-routed when the services it is generated from are; a
// target mixing routed and platform services cannot be generated.
func TestGoS2STenantRoutedTargets(t *testing.T) {
	config := &tactype.Config{GrpcS2SClients: []tactype.GrpcEntry{
		{ProtoFile: "services/crew.proto", ServiceName: "Crew"},
		{ProtoFile: "services/catalog.proto", ServiceName: "Catalog"},
	}}
	routedPkgs := map[string]bool{"crew": true}
	platformPkgs := map[string]bool{"catalog": true}
	routed, err := goS2STenantRoutedTargets(config, routedPkgs, platformPkgs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !routed["crew"] || routed["catalog"] {
		t.Fatalf("routed targets = %v, want only crew", routed)
	}

	mixed := &tactype.Config{GrpcS2SClients: []tactype.GrpcEntry{
		{ProtoFile: "services/crew.proto", ServiceName: "Crew", TargetService: "host"},
		{ProtoFile: "services/catalog.proto", ServiceName: "Catalog", TargetService: "host"},
	}}
	if _, err := goS2STenantRoutedTargets(mixed, routedPkgs, platformPkgs, nil); err == nil {
		t.Fatal("a target hosting both tenant-routed and platform services must fail generation")
	}
}
