// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// Issue #170: an rpc of a tenant-owned service is routed by its declared
// (spec.rpc).route_field. These pin the Java seams that read it: the S2S
// client (validate, then route by the field; streams fix the tenant at open),
// and the server wrapper (backstop after validation, before the handler, on
// every inbound message; RPC_FLAG_ANY_TENANT registered and honoured).

func routedMethodForTest(name string, clientStreaming, serverStreaming bool) *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:            name,
		InputType:       "HailRequest",
		OutputType:      "HailResponse",
		FullInputType:   "crew.HailRequest",
		FullOutputType:  "crew.HailResponse",
		ClientStreaming: clientStreaming,
		ServerStreaming: serverStreaming,
		RouteField:      &parser.RouteFieldDescriptor{Field: "target_tenant", JSONName: "targetTenant", Number: 3},
	}
}

func renderJavaClientMethodForTest(m *parser.MethodInfo) string {
	return generateJavaGrpcClientMethod(&parser.ServiceInfo{Name: "CrewService"}, m,
		"Crew.HailRequest", "Crew.HailResponse", "Helpers.validateHailRequest", "", nil)
}

func TestJavaGrpcClientRoutesUnaryByRouteField(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    *parser.MethodInfo
		call string
	}{
		{"unary", routedMethodForTest("Hail", false, false), "return stub.hail(request);"},
		{"server streaming", routedMethodForTest("Hail", false, true), "stub.hail(request, "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := renderJavaClientMethodForTest(tc.m)
			validate := strings.Index(got, "Helpers.validateHailRequest(request)")
			route := strings.Index(got, `final String __routeTenant = RouteTenant.require("target_tenant", request.getTargetTenant());`)
			option := strings.Index(got, ".withOption(RouteTenant.CALL_OPTION, __routeTenant);")
			call := strings.Index(got, tc.call)
			if validate < 0 || route < 0 || option < 0 || call < 0 || !(validate < route && route < option && option < call) {
				t.Fatalf("a routed client must validate, read the route field, then send to that tenant (validate %d, route %d, option %d, call %d):\n%s",
					validate, route, option, call, got)
			}
		})
	}
}

// A client-streaming / bidi stream's tenant is fixed at open: the method takes
// it explicitly, refuses a malformed one before dialling, and refuses any
// message whose route field differs from it.
func TestJavaGrpcClientStreamFixesTenantAtOpen(t *testing.T) {
	for _, tc := range []struct {
		name string
		m    *parser.MethodInfo
	}{
		{"client streaming", routedMethodForTest("Hail", true, false)},
		{"bidi", routedMethodForTest("Hail", true, true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := renderJavaClientMethodForTest(tc.m)
			for _, want := range []string{
				"public StreamObserver<Crew.HailRequest> hail(String tenant, StreamObserver<Crew.HailResponse> responseObserver) {",
				`final String __routeTenant = RouteTenant.require("target_tenant", tenant);`,
				"var stub = asyncStub.withOption(RouteTenant.CALL_OPTION, __routeTenant);",
				`if (!__routeTenant.equals(request.getTargetTenant())) {`,
			} {
				if !strings.Contains(got, want) {
					t.Fatalf("routed stream client missing %q:\n%s", want, got)
				}
			}
			if strings.Index(got, "RouteTenant.require(") > strings.Index(got, "stub.hail(") {
				t.Fatalf("the stream's tenant must be checked before the stream is opened:\n%s", got)
			}
			onNext := got[strings.Index(got, "public void onNext("):]
			if strings.Index(onNext, "__routeTenant.equals") > strings.Index(onNext, "delegate.onNext(request)") {
				t.Fatalf("each message's route field must be checked before it is sent:\n%s", onNext)
			}
		})
	}
}

// A platform service's client is byte-for-byte what it was: nothing reads a
// route field and no call carries a tenant.
func TestJavaGrpcClientPlatformMethodUnrouted(t *testing.T) {
	for _, shape := range [][2]bool{{false, false}, {false, true}, {true, false}, {true, true}} {
		m := routedMethodForTest("Hail", shape[0], shape[1])
		m.RouteField = nil
		got := renderJavaClientMethodForTest(m)
		if strings.Contains(got, "RouteTenant") || strings.Contains(got, "String tenant") {
			t.Fatalf("a platform method must not route by tenant:\n%s", got)
		}
	}
}

func renderJavaWrapperMethodForTest(m *parser.MethodInfo, authAnyUser bool) string {
	return generateJavaGRPCWrapper(m, "CrewService", "crew", "Crew.HailRequest", "Crew.HailResponse",
		"Helpers", "", authAnyUser, false, map[string]service.AccountingInfo{}, "crew", "services",
		map[string]string{}, proto.SkippedTraceTypes{}, nil, nil, nil)
}

// The backstop runs after request validation and before the handler.
func TestJavaWrapperRouteTenantBackstop(t *testing.T) {
	for _, tc := range []struct {
		name    string
		m       *parser.MethodInfo
		handler string
	}{
		{"unary", routedMethodForTest("Hail", false, false), "handler.hail(caller, request)"},
		{"server streaming", routedMethodForTest("Hail", false, true), "handler.hail(caller, request, "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := renderJavaWrapperMethodForTest(tc.m, false)
			validate := strings.Index(got, "Helpers.validateHailRequest(request);")
			backstop := strings.Index(got, `checkRouteTenant("target_tenant", request.getTargetTenant());`)
			handler := strings.Index(got, tc.handler)
			if validate < 0 || backstop < 0 || handler < 0 || !(validate < backstop && backstop < handler) {
				t.Fatalf("the backstop must run after validation and before the handler (validate %d, backstop %d, handler %d):\n%s",
					validate, backstop, handler, got)
			}
		})
	}
	for _, tc := range []struct {
		name string
		m    *parser.MethodInfo
	}{
		{"client streaming", routedMethodForTest("Hail", true, false)},
		{"bidi", routedMethodForTest("Hail", true, true)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := renderJavaWrapperMethodForTest(tc.m, false)
			perMessage := got[strings.Index(got, "validatingRequestObserver("):]
			validate := strings.Index(perMessage, "Helpers.validateHailRequest(value);")
			backstop := strings.Index(perMessage, `checkRouteTenant("target_tenant", value.getTargetTenant());`)
			if validate < 0 || backstop < 0 || validate > backstop {
				t.Fatalf("every inbound message must be validated, then checked by the backstop:\n%s", got)
			}
		})
	}
	m := routedMethodForTest("Hail", false, false)
	m.RouteField = nil
	if got := renderJavaWrapperMethodForTest(m, false); strings.Contains(got, "checkRouteTenant") {
		t.Fatalf("a platform method has no route backstop:\n%s", got)
	}
}

// RPC_FLAG_ANY_TENANT on an auth_any_user rpc: the token-only check runs in
// the context that names the rpc, which is what lets the authorizer open the
// door for it; the default keeps the plain call.
func TestJavaWrapperAnyTenantAuthenticate(t *testing.T) {
	m := routedMethodForTest("Hail", false, false)
	m.AuthAnyUser = true
	m.AnyTenant = true
	got := renderJavaWrapperMethodForTest(m, true)
	if !strings.Contains(got, `callerIdentity = checkAuthenticateAnyTenant("CrewService/Hail");`) {
		t.Fatalf("an ANY_TENANT auth_any_user rpc must authenticate under its method key:\n%s", got)
	}
	m.AnyTenant = false
	if got := renderJavaWrapperMethodForTest(m, true); !strings.Contains(got, "callerIdentity = checkAuthenticate();") || strings.Contains(got, "AnyTenant") {
		t.Fatalf("without ANY_TENANT the wrapper keeps the plain token check:\n%s", got)
	}
}

// The whole wrapper: ANY_TENANT is registered beside auth_services under the
// key authorize() uses, and the backstop helper is emitted only for a service
// with a routed rpc.
func TestJavaServiceWrapperRegistersAnyTenantAndBackstop(t *testing.T) {
	render := func(t *testing.T, routed bool) string {
		outputDir := t.TempDir()
		wrapperDir := filepath.Join(outputDir, "wrapper")
		svc := ServiceInfo{
			ProtoFile: "services/crew.proto", PackageName: "crew", ServiceName: "CrewService", GrpcName: "crew",
			IsGrpc: true, IsS2S: true, OutputDir: wrapperDir, TypesOutputDir: filepath.Join(outputDir, "types"),
			ParentDir: "services", AuthServices: []string{"caller"}, HasMethodAuth: true,
			Config: &tactype.Config{OutputDir: outputDir, JavaPackageBase: "com.example"},
		}
		hail := routedMethodForTest("Hail", false, false)
		hail.AnyTenant = true
		muster := routedMethodForTest("Muster", false, false)
		if !routed {
			hail.RouteField, hail.AnyTenant, muster.RouteField = nil, false, nil
		}
		info := &parser.ProtoInfo{
			Package:  "crew",
			Services: []*parser.ServiceInfo{{Name: "CrewService", Methods: []*parser.MethodInfo{hail, muster}}},
		}
		if _, err := generateServiceWrapper(svc, info, nil, proto.SkippedTraceTypes{}, nil); err != nil {
			t.Fatalf("generateServiceWrapper: %v", err)
		}
		raw, err := os.ReadFile(filepath.Join(wrapperDir, "CrewService.java"))
		if err != nil {
			t.Fatalf("read generated wrapper: %v", err)
		}
		return string(raw)
	}
	got := render(t, true)
	for _, want := range []string{
		`authorizer.setMethodAnyTenant("CrewService/Hail");`,
		"private void checkRouteTenant(String field, String value) throws ProtoException {",
		"authorizer.checkRouteTenant(field, value);",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("routed wrapper missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, `setMethodAnyTenant("CrewService/Muster")`) {
		t.Fatalf("only the rpc declaring RPC_FLAG_ANY_TENANT opens the door:\n%s", got)
	}
	platform := render(t, false)
	for _, notWant := range []string{"setMethodAnyTenant", "checkRouteTenant", "checkAuthenticateAnyTenant"} {
		if strings.Contains(platform, notWant) {
			t.Fatalf("a platform wrapper must not carry %q:\n%s", notWant, platform)
		}
	}
}

// A tenant-routed S2S target gets per-tenant pools (TenantConnections) fed by
// the same health poller, and is not a readiness gate; a platform target keeps
// its single ServiceConnection exactly as before.
func TestJavaServiceManagerTenantRoutedTarget(t *testing.T) {
	data := javaSmTemplateData{
		PackageBase: "com.example", ChassisBase: "com.example.chassis", TypesBase: "com.example.types",
		SmPrefix: "s2sm", ServiceLabel: "harbor", HasGrpcS2SClients: true, S2STargetsRequired: true, HasServiceHealthState: true,
		HealthStateOuterClass: "Healthstate", HealthStateJavaPkg: "sys.healthstate",
		TargetServices: []javaTargetServiceEntry{
			{ServiceID: "crew", ParamName: "crewChannel", FieldName: "crewConn", TenantRouted: true},
			{ServiceID: "identity", ParamName: "identityChannel", FieldName: "identityConn"},
		},
	}
	sm := renderJavaTemplateForTest(t, "ServiceManager.java.tmpl", data)
	for _, want := range []string{
		"private final com.example.chassis.serviceconnection.TenantConnections crewConn;",
		`this.crewConn = new com.example.chassis.serviceconnection.TenantConnections("crew", channelFactory);`,
		"private final ServiceConnection identityConn;",
		`this.identityConn = new ServiceConnection("identity", channelFactory);`,
		"new ServiceConnection.ChannelProvider(crewConn), new ServiceConnection.ChannelProvider(identityConn)",
		"crewConn.reconcileFromHealth(filtered);",
	} {
		if !strings.Contains(sm, want) {
			t.Fatalf("ServiceManager missing %q", want)
		}
	}
	readiness := sm[strings.Index(sm, "RequiredChannels required = new RequiredChannels("):]
	readiness = readiness[:strings.Index(readiness, "), streams);")]
	if strings.Contains(readiness, "crewConn") || !strings.Contains(readiness, "identityConn") {
		t.Fatalf("only platform targets gate readiness:\n%s", readiness)
	}
}

// javaTenantRoutedTargets reads routing off the parsed model: crew's s2s
// surface is tenant-routed (crew is tenant_owned), harbor's is not.
func TestJavaTenantRoutedTargetsFromModel(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := t.TempDir()
	copyFile := func(src, dst string) {
		t.Helper()
		raw, err := os.ReadFile(src)
		if err != nil {
			t.Fatalf("read %s: %v", src, err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join("..", "..", "..", "..")
	copyFile(filepath.Join(root, "pkg", "proto", "sys", "spec.proto"), filepath.Join(dir, "sys", "spec.proto"))
	copyFile(filepath.Join(root, "cross-test", "proto-tenantroute", "services", "crew.proto"), filepath.Join(dir, "services", "crew.proto"))
	copyFile(filepath.Join(root, "cross-test", "proto-tenantroute", "services", "harbor.proto"), filepath.Join(dir, "services", "harbor.proto"))

	cfg := &tactype.Config{}
	routed, err := javaTenantRoutedTargets(cfg, []ServiceInfo{
		{ProtoFile: "services/crew.proto", PackageName: "crew", ServiceName: "CrewPeerService"},
		{ProtoFile: "services/harbor.proto", PackageName: "harbor", ServiceName: "HarborService"},
	}, dir)
	if err != nil {
		t.Fatalf("javaTenantRoutedTargets: %v", err)
	}
	if !routed["crew"] || routed["harbor"] || len(routed) != 1 {
		t.Fatalf("routed targets = %v, want only crew", routed)
	}
}

// The Java W2S and REST clients reach a routed rpc through the gateway, which
// picks the instance by the route field; like every client-only language they
// refuse an empty or malformed one before sending.
func TestJavaW2SAndRestClientsRefuseBadRouteField(t *testing.T) {
	routedHTTP := func(cs, ss bool) *parser.MethodInfo {
		m := routedMethodForTest("Hail", cs, ss)
		m.HttpPath, m.HttpMethod = "/crew/CrewService/Hail", "POST"
		return m
	}
	svc := &parser.ServiceInfo{Name: "CrewService"}
	for _, tc := range []struct {
		name, want, send string
		m                *parser.MethodInfo
	}{
		{"unary", `RouteTenant.require("target_tenant", request.getTargetTenant());`, "doJson(", routedHTTP(false, false)},
		{"server streaming", `RouteTenant.require("target_tenant", request.getTargetTenant());`, "streamSse(", routedHTTP(false, true)},
		{"client streaming", `RouteTenant.require("target_tenant", msg.getTargetTenant());`, "clientStream(", routedHTTP(true, false)},
		{"bidi", `RouteTenant.require("target_tenant", message.getTargetTenant());`, "outbound.offer(", routedHTTP(true, true)},
	} {
		t.Run("w2s "+tc.name, func(t *testing.T) {
			r := generateJavaW2SClientMethod(svc, tc.m, "Crew.HailRequest", "Crew.HailResponse", "CrewServiceW2SClient", true, "Helpers.validateHailRequest", "")
			got := r.Method + r.ExtraType
			check, send := strings.Index(got, tc.want), strings.LastIndex(got, tc.send)
			if check < 0 || send < 0 || (tc.name != "client streaming" && check > send) {
				t.Fatalf("W2S %s client must check the route field before sending (check %d, send %d):\n%s", tc.name, check, send, got)
			}
		})
	}
	for _, ss := range []bool{false, true} {
		got := generateJavaRestClientMethod(routedHTTP(false, ss), "Crew.HailRequest", "Crew.HailResponse")
		check := strings.Index(got, `RouteTenant.require("target_tenant", request.getTargetTenant());`)
		send := strings.Index(got, "return ")
		if check < 0 || check > send {
			t.Fatalf("REST client must check the route field before sending:\n%s", got)
		}
	}
	platform := routedHTTP(false, false)
	platform.RouteField = nil
	if r := generateJavaW2SClientMethod(svc, platform, "Crew.HailRequest", "Crew.HailResponse", "CrewServiceW2SClient", true, "", ""); strings.Contains(r.Method, "RouteTenant") {
		t.Fatalf("a platform W2S method reads no route field:\n%s", r.Method)
	}
	if got := generateJavaRestClientMethod(platform, "Crew.HailRequest", "Crew.HailResponse"); strings.Contains(got, "RouteTenant") {
		t.Fatalf("a platform REST method reads no route field:\n%s", got)
	}
}
