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
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// Issue #170: a Swift client reaches a tenant-routed service through the
// gateway, which picks the tenant's instance by the rpc's route field. Every
// generated C2S and W2S call of a routed rpc must refuse an empty or malformed
// route field BEFORE sending, naming the field. Platform rpcs are untouched.

func routedMethod(name, field string, number int32) *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:           name,
		InputType:      "GetSkipperRequest",
		OutputType:     "GetSkipperResponse",
		FullInputType:  ".crew.GetSkipperRequest",
		FullOutputType: ".crew.GetSkipperResponse",
		HttpPath:       "/crew/CrewService/" + name,
		HttpMethod:     "POST",
		RouteField:     &parser.RouteFieldDescriptor{Field: field, JSONName: field, Number: number},
	}
}

func TestW2SMethodDataCarriesRouteField(t *testing.T) {
	svc := service.ServiceInfo{ServiceName: "CrewService"}
	target := &parser.ServiceInfo{Name: "CrewService"}

	got, err := w2sMethodData(svc, target, routedMethod("SendMessage", "target_tenant", 2), "crew")
	if err != nil {
		t.Fatalf("w2sMethodData(routed): %v", err)
	}
	if got.RouteField != "target_tenant" || got.RouteFieldNumber != 2 {
		t.Fatalf("routed W2S method route = (%q, %d), want (\"target_tenant\", 2)", got.RouteField, got.RouteFieldNumber)
	}

	streaming := routedMethod("WatchCrew", "tenant", 1)
	streaming.ServerStreaming = true
	got, err = w2sMethodData(svc, target, streaming, "crew")
	if err != nil {
		t.Fatalf("w2sMethodData(routed server-streaming): %v", err)
	}
	if got.RouteField != "tenant" || got.RouteFieldNumber != 1 {
		t.Fatalf("routed server-streaming W2S route = (%q, %d), want (\"tenant\", 1)", got.RouteField, got.RouteFieldNumber)
	}

	platform := routedMethod("GetHarborStatus", "", 0)
	platform.RouteField = nil
	got, err = w2sMethodData(svc, target, platform, "crew")
	if err != nil {
		t.Fatalf("w2sMethodData(platform): %v", err)
	}
	if got.RouteField != "" || got.RouteFieldNumber != 0 {
		t.Fatalf("platform W2S method carries a route (%q, %d)", got.RouteField, got.RouteFieldNumber)
	}

	broken := routedMethod("Broken", "tenant", 0)
	if _, err := w2sMethodData(svc, target, broken, "crew"); err == nil {
		t.Fatal("w2sMethodData accepted a route field with no wire number")
	}
}

func renderRoutedW2SFixture(t *testing.T) string {
	t.Helper()
	data := w2sClientTemplateData{
		HasRoutedMethods: true,
		Services: []w2sServiceTemplateData{{
			ServiceName:    "CrewService",
			ClientTypeName: "CrewServiceW2SClient",
			PackageName:    "crew",
			Methods: []w2sMethodTemplateData{
				{
					MethodName: "GetSkipper", SwiftMethodName: "getSkipper", HTTPMethod: "POST",
					HTTPPath: "/crew/CrewService/GetSkipper", AuthRequired: true,
					InputType: "Crew_GetSkipperRequest", OutputType: "Crew_GetSkipperResponse",
					RouteField: "tenant", RouteFieldNumber: 1,
				},
				{
					MethodName: "WatchCrew", SwiftMethodName: "watchCrew", HTTPMethod: "POST",
					HTTPPath: "/crew/CrewService/WatchCrew", AuthRequired: true,
					InputType: "Crew_WatchCrewRequest", OutputType: "Crew_CrewMember",
					IsStreaming: true, RouteField: "target_tenant", RouteFieldNumber: 3,
				},
			},
		}, {
			ServiceName:    "HarborService",
			ClientTypeName: "HarborServiceW2SClient",
			PackageName:    "harbor",
			Methods: []w2sMethodTemplateData{{
				MethodName: "GetHarborStatus", SwiftMethodName: "getHarborStatus", HTTPMethod: "POST",
				HTTPPath: "/harbor/HarborService/GetHarborStatus", AuthRequired: true,
				InputType: "Harbor_GetHarborStatusRequest", OutputType: "Harbor_GetHarborStatusResponse",
			}},
		}},
	}
	out, err := executeSwiftTemplate("W2SClient.swift.tmpl", data)
	if err != nil {
		t.Fatalf("executeSwiftTemplate(W2SClient.swift.tmpl): %v", err)
	}
	return string(out)
}

// swiftFuncBody returns the text of the generated function `name` up to the
// next generated function, so a test can assert on one method in isolation.
func swiftFuncBody(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "public func "+name+"(")
	if start < 0 {
		t.Fatalf("generated Swift has no func %s\n%s", name, src)
	}
	rest := src[start+1:]
	if end := strings.Index(rest, "public func "); end >= 0 {
		return src[start : start+1+end]
	}
	return src[start:]
}

func TestSwiftW2SRoutedMethodChecksRouteFieldBeforeSending(t *testing.T) {
	rendered := renderRoutedW2SFixture(t)

	for _, want := range []string{
		"case invalidArgument(String)",
		"static func checkRouteField<Request: SwiftProtobuf.Message>(",
		"throw W2SClientError.invalidArgument(refusal.message)",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("routed W2S client missing %q", want)
		}
	}

	for _, tt := range []struct {
		fn, check, send string
	}{
		{"getSkipper", `try W2SClient.checkRouteField(request, field: "tenant", number: 1)`, "return try await self.client.invoke("},
		{"watchCrew", `try W2SClient.checkRouteField(request, field: "target_tenant", number: 3)`, "return try await self.client.invokeStream("},
	} {
		body := swiftFuncBody(t, rendered, tt.fn)
		check := strings.Index(body, tt.check)
		send := strings.Index(body, tt.send)
		if check < 0 || send < 0 {
			t.Fatalf("%s: want route check %q and send %q\n%s", tt.fn, tt.check, tt.send, body)
		}
		if check > send {
			t.Errorf("%s: route check comes after the send\n%s", tt.fn, body)
		}
	}

	platform := swiftFuncBody(t, rendered, "getHarborStatus")
	if strings.Contains(platform, "checkRouteField") {
		t.Errorf("platform W2S method checks a route field\n%s", platform)
	}
	if !strings.Contains(platform, "        try await self.client.invoke(") {
		t.Errorf("platform W2S method shape changed\n%s", platform)
	}
}

func TestSwiftW2SUnroutedBuildHasNoRouteSurface(t *testing.T) {
	rendered := renderW2SFixture(t)
	for _, unwanted := range []string{"invalidArgument", "checkRouteField", "TacplusTenantRoute", "return try await"} {
		if strings.Contains(rendered, unwanted) {
			t.Errorf("unrouted W2S client contains %q", unwanted)
		}
	}
}

func TestC2SRouteMethodsData(t *testing.T) {
	route := func(field string, number int32) *parser.RouteFieldDescriptor {
		return &parser.RouteFieldDescriptor{Field: field, JSONName: field, Number: number}
	}
	fd := &parser.FileDescriptor{
		Package: "crew",
		Services: []parser.ServiceDescriptor{
			{Name: "CrewService", Methods: []parser.MethodDescriptor{
				{Name: "SendMessage", ClientStreaming: true, RouteField: route("target_tenant", 2)},
				{Name: "GetSkipper", RouteField: route("tenant", 1)},
			}},
			{Name: "Other", Methods: []parser.MethodDescriptor{{Name: "Nope", RouteField: route("tenant", 1)}}},
		},
	}
	got, err := c2sRouteMethodsForService(fd, "CrewService")
	if err != nil {
		t.Fatalf("c2sRouteMethodsForService: %v", err)
	}
	want := []c2sRouteMethodTemplateData{
		{FullMethod: "crew.CrewService/GetSkipper", RouteField: "tenant", RouteFieldNumber: 1},
		{FullMethod: "crew.CrewService/SendMessage", RouteField: "target_tenant", RouteFieldNumber: 2, ClientStreaming: true},
	}
	if len(got) != len(want) {
		t.Fatalf("c2s route methods = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("c2s route method %d = %#v, want %#v", i, got[i], want[i])
		}
	}

	platform := &parser.FileDescriptor{Package: "harbor", Services: []parser.ServiceDescriptor{{
		Name:    "HarborService",
		Methods: []parser.MethodDescriptor{{Name: "GetHarborStatus"}},
	}}}
	got, err = c2sRouteMethodsForService(platform, "HarborService")
	if err != nil {
		t.Fatalf("c2sRouteMethodsForService(platform): %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("platform service yields route methods %#v", got)
	}

	if _, err := c2sRouteMethodsForService(fd, "Missing"); err == nil {
		t.Fatal("c2sRouteMethodsForService accepted a service absent from its proto")
	}
	broken := &parser.FileDescriptor{Package: "crew", Services: []parser.ServiceDescriptor{{
		Name:    "CrewService",
		Methods: []parser.MethodDescriptor{{Name: "GetSkipper", RouteField: route("tenant", 0)}},
	}}}
	if _, err := c2sRouteMethodsForService(broken, "CrewService"); err == nil {
		t.Fatal("c2sRouteMethodsForService accepted a route field with no wire number")
	}
}

func TestSwiftC2SRoutedClientInstallsRouteInterceptor(t *testing.T) {
	out, err := executeSwiftTemplate("C2SClient.swift.tmpl", c2sClientTemplateData{
		PackageName: "HarborClient",
		Services:    []c2dServiceTemplateData{{MethodName: "withCrewService", ClientType: "Crew_CrewService"}},
		RouteMethods: []c2sRouteMethodTemplateData{
			{FullMethod: "crew.CrewService/GetSkipper", RouteField: "tenant", RouteFieldNumber: 1},
			{FullMethod: "crew.CrewService/SendMessage", RouteField: "target_tenant", RouteFieldNumber: 2, ClientStreaming: true},
		},
	})
	if err != nil {
		t.Fatalf("executeSwiftTemplate(C2SClient.swift.tmpl): %v", err)
	}
	s := string(out)
	for _, want := range []string{
		"import SwiftProtobuf",
		"struct C2STenantRouteInterceptor: ClientInterceptor {",
		`"crew.CrewService/GetSkipper": C2SRouteField(name: "tenant", number: 1, clientStreaming: false),`,
		`"crew.CrewService/SendMessage": C2SRouteField(name: "target_tenant", number: 2, clientStreaming: true),`,
		"C2STenantRouteInterceptor.routes[context.descriptor.fullyQualifiedMethod]",
		"RPCError(code: .invalidArgument, message: refusal.message)",
		"var interceptors: [any ClientInterceptor] = [C2STenantRouteInterceptor()]",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("routed C2SClient.swift missing %q\n%s", want, s)
		}
	}
	// A single-request rpc is checked in full before next() is called, so a
	// refused call never writes its request.
	intercept := s[strings.Index(s, "struct C2STenantRouteInterceptor"):]
	drain := strings.Index(intercept, "try await request.producer(RPCWriter(wrapping: C2SRouteBufferWriter(buffer: buffer)))")
	check := strings.Index(intercept, "try C2STenantRouteInterceptor.tenant(of: message, route: route)")
	next := strings.LastIndex(intercept, "return try await next(request, context)")
	if drain < 0 || check < 0 || next < 0 || !(drain < check && check < next) {
		t.Errorf("C2S route interceptor must drain, check, then call next (drain %d, check %d, next %d)\n%s", drain, check, next, intercept)
	}
	// The route check runs before the bearer token is fetched.
	routeAt := strings.Index(s, "[C2STenantRouteInterceptor()]")
	bearerAt := strings.Index(s, "interceptors.append(C2SBearerTokenInterceptor(tokenProvider: tokenProvider))")
	if routeAt < 0 || bearerAt < 0 || routeAt > bearerAt {
		t.Errorf("C2S route interceptor must precede the bearer interceptor\n%s", s)
	}
}

func TestSwiftC2SUnroutedClientUnchanged(t *testing.T) {
	out, err := executeSwiftTemplate("C2SClient.swift.tmpl", c2sClientTemplateData{
		PackageName: "TasksClient",
		Services:    []c2dServiceTemplateData{{MethodName: "withContractApi", ClientType: "Contractclient_ContractApi"}},
	})
	if err != nil {
		t.Fatalf("executeSwiftTemplate(C2SClient.swift.tmpl): %v", err)
	}
	s := string(out)
	for _, unwanted := range []string{"SwiftProtobuf", "C2STenantRouteInterceptor", "C2SRouteField", "TacplusTenantRoute", "invalidArgument"} {
		if strings.Contains(s, unwanted) {
			t.Errorf("unrouted C2SClient.swift contains %q", unwanted)
		}
	}
	if !strings.Contains(s, "        return [C2SBearerTokenInterceptor(tokenProvider: tokenProvider)]\n") {
		t.Errorf("unrouted C2SClient.swift interceptor list changed\n%s", s)
	}
}

func TestSwiftTenantRouteHelperRenders(t *testing.T) {
	out, err := executeSwiftTemplate("TenantRoute.swift.tmpl", nil)
	if err != nil {
		t.Fatalf("executeSwiftTemplate(TenantRoute.swift.tmpl): %v", err)
	}
	s := string(out)
	for _, want := range []string{
		"enum TacplusTenantRoute {",
		"static func isTenantID(_ value: String) -> Bool",
		"static func fieldValue(wire: [UInt8], number: Int) throws -> String",
		"static func tenant(wire: [UInt8], field: String, number: Int) throws -> String",
		"#if canImport(SwiftProtobuf)",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("TenantRoute.swift missing %q", want)
		}
	}
}

// swiftTenantRouteBehaviour exercises the generated pure-Swift helper (tid
// check + protobuf wire walker) by compiling it with a driver.
const swiftTenantRouteBehaviour = `
var failures = 0

func varint(_ v: UInt64) -> [UInt8] {
    var v = v
    var out: [UInt8] = []
    while v >= 0x80 {
        out.append(UInt8(v & 0x7f) | 0x80)
        v >>= 7
    }
    out.append(UInt8(v))
    return out
}

func len(_ number: UInt64, _ bytes: [UInt8]) -> [UInt8] {
    varint(number << 3 | 2) + varint(UInt64(bytes.count)) + bytes
}

func str(_ number: UInt64, _ s: String) -> [UInt8] {
    len(number, Array(s.utf8))
}

@MainActor
func expectTenant(_ label: String, _ wire: [UInt8], _ want: String) {
    do {
        let got = try TacplusTenantRoute.tenant(wire: wire, field: "tenant", number: 1)
        if got != want {
            print("FAIL \(label): got \(got), want \(want)")
            failures += 1
        }
    } catch {
        print("FAIL \(label): refused: \(error)")
        failures += 1
    }
}

@MainActor
func expectRefused(_ label: String, _ wire: [UInt8]) {
    do {
        let got = try TacplusTenantRoute.tenant(wire: wire, field: "tenant", number: 1)
        print("FAIL \(label): accepted \(got)")
        failures += 1
    } catch let refusal as TacplusTenantRouteRefusal {
        if !refusal.message.contains("\"tenant\"") {
            print("FAIL \(label): refusal does not name the route field: \(refusal.message)")
            failures += 1
        }
    } catch {
        print("FAIL \(label): unexpected error type: \(error)")
        failures += 1
    }
}

// The tid check itself.
for ok in ["aaaaaaaa", "0a1b2c3d", "00000000", "zzzzzzzz"] {
    if !TacplusTenantRoute.isTenantID(ok) {
        print("FAIL isTenantID(\(ok)) = false")
        failures += 1
    }
}
for bad in ["", "ABCDEFGH", "abc", "abcdefghi", "abcdefg!", "abcdefg\n", " aaaaaaa", "aaaaaaa ", "aaaaaaaA", "aaaaaaa\u{e9}", "aaaa-aaa", "\u{ff41}aaaaaaa"] {
    if TacplusTenantRoute.isTenantID(bad) {
        print("FAIL isTenantID(\(bad.debugDescription)) = true")
        failures += 1
    }
}

// Empty and malformed values are refused, naming the field.
expectRefused("missing field", [])
expectRefused("empty field", str(1, ""))
for bad in ["ABCDEFGH", "abc", "abcdefghi", "abcdefg!", "abcdefg\n"] {
    expectRefused("malformed \(bad.debugDescription)", str(1, bad))
}
expectRefused("invalid utf-8", len(1, [0x61, 0x61, 0x61, 0x61, 0x61, 0x61, 0x61, 0xff]))
expectRefused("route number with varint wire type only", varint(1 << 3 | 0) + varint(5))

// A valid tid is accepted, around other fields of every wire type.
expectTenant("valid", str(1, "aaaaaaaa"), "aaaaaaaa")
expectTenant("valid among fields",
    varint(2 << 3 | 0) + varint(300)
        + varint(3 << 3 | 1) + [UInt8](repeating: 7, count: 8)
        + str(1, "aaaaaaaa")
        + varint(4 << 3 | 5) + [UInt8](repeating: 9, count: 4)
        + str(6, "zzzzzzzz"),
    "aaaaaaaa")

// Proto3 last-one-wins.
expectTenant("last occurrence wins", str(1, "ABCDEFGH") + str(1, "aaaaaaaa"), "aaaaaaaa")
expectRefused("last occurrence wins (bad last)", str(1, "aaaaaaaa") + str(1, "bad"))

// A field 1 nested in a group is not the top-level route field.
expectTenant("group nested field ignored",
    str(1, "aaaaaaaa") + varint(5 << 3 | 3) + str(1, "bbbbbbbb") + varint(5 << 3 | 4),
    "aaaaaaaa")

// Malformed wire encoding is refused.
expectRefused("truncated varint", [0x08, 0x80])
expectRefused("truncated tag", [0x80])
expectRefused("varint too long", [0x08] + [UInt8](repeating: 0xff, count: 10) + [0x01])
expectRefused("len overrun", [0x0a, 0x05, 0x61])
expectRefused("truncated fixed64", [0x19, 0x01, 0x02])
expectRefused("truncated fixed32", [0x25, 0x01])
expectRefused("wire type 6", [0x0e])
expectRefused("wire type 7", [0x0f])
expectRefused("field number 0", [0x02, 0x00])
expectRefused("unmatched end group", [0x2c])
expectRefused("unterminated group", [0x2b])
expectRefused("mismatched end group", [0x2b, 0x34])
expectRefused("malformed after a valid route", str(1, "aaaaaaaa") + [0x0a, 0x09])

if failures == 0 {
    print("TENANT-ROUTE-OK")
} else {
    print("\(failures) failure(s)")
}
`

func TestSwiftTenantRouteHelperBehaviour(t *testing.T) {
	swiftc, err := exec.LookPath("swiftc")
	if err != nil {
		t.Skip("swiftc not on PATH")
	}
	out, err := executeSwiftTemplate("TenantRoute.swift.tmpl", nil)
	if err != nil {
		t.Fatalf("executeSwiftTemplate(TenantRoute.swift.tmpl): %v", err)
	}
	dir := t.TempDir()
	helper := filepath.Join(dir, "TenantRoute.swift")
	driver := filepath.Join(dir, "main.swift")
	bin := filepath.Join(dir, "tenantroute")
	if err := os.WriteFile(helper, out, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(driver, []byte(swiftTenantRouteBehaviour), 0o644); err != nil {
		t.Fatal(err)
	}
	build := exec.Command(swiftc, "-swift-version", "6", helper, driver, "-o", bin)
	build.Dir = dir
	if msg, err := build.CombinedOutput(); err != nil {
		t.Fatalf("swiftc failed: %v\n%s", err, msg)
	}
	run := exec.Command(bin)
	msg, err := run.CombinedOutput()
	if err != nil || !strings.Contains(string(msg), "TENANT-ROUTE-OK") {
		t.Fatalf("Swift tenant-route behaviour failed: %v\n%s", err, msg)
	}
}

// End to end: a client of a tenant-routed service gets the route helper, a C2S
// route table holding every routed rpc (and no platform rpc), and W2S methods
// that check the route field; a platform-only build gets none of it.
func TestGenerateTenantRoutedSwiftClient(t *testing.T) {
	if _, err := exec.LookPath("protoc-gen-swift"); err != nil && os.Getenv("PROTOC_SWIFT_BINARY") == "" {
		t.Skip("protoc-gen-swift not on PATH")
	}
	outputDir := filepath.Join(t.TempDir(), "swift")
	cfg := &tactype.Config{
		Language:         "swift",
		InputDir:         filepath.Join("testdata", "tenantroute"),
		OutputDir:        outputDir,
		SelfPackage:      "harbor",
		SwiftPackageBase: "HarborClient",
	}
	if err := Generate(cfg); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	sources := filepath.Join(outputDir, "Sources", "HarborClient")
	if _, err := os.Stat(filepath.Join(sources, "TenantRoute.swift")); err != nil {
		t.Fatalf("routed build has no TenantRoute.swift: %v", err)
	}
	c2s, err := os.ReadFile(filepath.Join(sources, "C2SClient.swift"))
	if err != nil {
		t.Fatalf("read C2SClient.swift: %v", err)
	}
	for _, want := range []string{
		`"crew.CrewService/GetSkipper": C2SRouteField(name: "tenant", number: 1, clientStreaming: false),`,
		`"crew.CrewService/SendMessages": C2SRouteField(name: "target_tenant", number: 2, clientStreaming: true),`,
		`"crew.CrewService/WatchCrew": C2SRouteField(name: "tenant", number: 1, clientStreaming: false),`,
		"withHarborService",
	} {
		if !strings.Contains(string(c2s), want) {
			t.Errorf("C2SClient.swift missing %q\n%s", want, c2s)
		}
	}
	if strings.Contains(string(c2s), "harbor.HarborService/") {
		t.Errorf("C2SClient.swift route table holds a platform rpc\n%s", c2s)
	}

	w2s, err := os.ReadFile(filepath.Join(sources, "W2SClient.swift"))
	if err != nil {
		t.Fatalf("read W2SClient.swift: %v", err)
	}
	w2sText := string(w2s)
	for fn, want := range map[string]string{
		"getSkipper": `try W2SClient.checkRouteField(request, field: "tenant", number: 1)`,
		"watchCrew":  `try W2SClient.checkRouteField(request, field: "tenant", number: 1)`,
	} {
		if !strings.Contains(swiftFuncBody(t, w2sText, fn), want) {
			t.Errorf("W2S %s missing %q\n%s", fn, want, w2sText)
		}
	}
	for _, fn := range []string{"getHarborStatus", "sendMessages"} {
		if strings.Contains(swiftFuncBody(t, w2sText, fn), "checkRouteField") {
			t.Errorf("W2S %s checks a route field (platform rpc or unsupported stub)\n%s", fn, w2sText)
		}
	}

	platformDir := filepath.Join(t.TempDir(), "swift")
	platform := &tactype.Config{
		Language:         "swift",
		InputDir:         filepath.Join("..", "..", "proto", "testdata", "eventbus_c2s_contract"),
		OutputDir:        platformDir,
		SelfPackage:      "tasks",
		SwiftPackageBase: "TasksClient",
	}
	if err := Generate(platform); err != nil {
		t.Fatalf("Generate(platform): %v", err)
	}
	platformSources := filepath.Join(platformDir, "Sources", "TasksClient")
	if _, err := os.Stat(filepath.Join(platformSources, "TenantRoute.swift")); !os.IsNotExist(err) {
		t.Errorf("platform build emitted TenantRoute.swift (stat err %v)", err)
	}
	facade, err := os.ReadFile(filepath.Join(platformSources, "C2SClient.swift"))
	if err != nil {
		t.Fatalf("read platform C2SClient.swift: %v", err)
	}
	if strings.Contains(string(facade), "C2STenantRouteInterceptor") {
		t.Error("platform C2SClient.swift installs the tenant route interceptor")
	}
}
