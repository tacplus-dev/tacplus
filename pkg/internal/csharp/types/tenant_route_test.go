// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// Issue #170: the C# lite client reaches tenant-owned services through the
// gateway, which routes each call by the rpc's (spec.rpc).route_field. Every
// generated C2S and W2S call of a routed rpc refuses an empty or malformed
// route field (not a tid: exactly 8 characters of [0-9a-z]) before anything is
// sent, with a message naming the field. Platform rpcs are untouched.

func routedW2SData() w2sClientTemplateData {
	return w2sClientTemplateData{
		PackageName: "MyTacplus",
		Services: []w2sServiceTemplateData{
			{
				ServiceName:    "CrewService",
				ClientTypeName: "CrewServiceW2SClient",
				PackageName:    "crew",
				Methods: []w2sMethodTemplateData{
					{
						MethodName:   "GetSkipper",
						HTTPMethod:   "POST",
						HTTPPath:     "/crew/GetSkipper",
						AuthRequired: true,
						InputType:    "Crew.GetSkipperRequest",
						OutputType:   "Crew.GetSkipperResponse",
						RPCName:      "crew.CrewService.GetSkipper",
						RouteField:   &parser.RouteFieldDescriptor{Field: "target_tenant", JSONName: "targetTenant", Number: 2},
					},
				},
			},
			{
				ServiceName:    "PierService",
				ClientTypeName: "PierServiceW2SClient",
				PackageName:    "pier",
				Methods: []w2sMethodTemplateData{
					{
						MethodName:   "GetPier",
						HTTPMethod:   "POST",
						HTTPPath:     "/pier/GetPier",
						AuthRequired: true,
						InputType:    "Pier.GetPierRequest",
						OutputType:   "Pier.GetPierResponse",
						RPCName:      "pier.PierService.GetPier",
					},
				},
			},
		},
	}
}

func TestRenderW2SClientRoutedMethodRefusesBadRouteField(t *testing.T) {
	out, err := executeCSharpTemplate("W2SClient.cs.tmpl", routedW2SData())
	if err != nil {
		t.Fatalf("render W2SClient: %v", err)
	}
	s := string(out)

	routedAt := strings.Index(s, "GetSkipperAsync(")
	platformAt := strings.Index(s, "GetPierAsync(")
	if routedAt < 0 || platformAt < 0 {
		t.Fatalf("W2SClient.cs missing a typed method\n%s", s)
	}
	routed := s[routedAt:platformAt]

	check := `TenantRoute.Refusal(request, 2, "target_tenant", "crew.CrewService.GetSkipper")`
	checkAt := strings.Index(routed, check)
	if checkAt < 0 {
		t.Fatalf("routed W2S method does not check its route field by number and name (missing %q)\n%s", check, routed)
	}
	refuse := "return Task.FromException<Crew.GetSkipperResponse>(new W2SClientException(refusal));"
	refuseAt := strings.Index(routed, refuse)
	if refuseAt < 0 {
		t.Fatalf("routed W2S method does not refuse with W2SClientException (missing %q)\n%s", refuse, routed)
	}
	invokeAt := strings.Index(routed, "_client.InvokeAsync<Crew.GetSkipperResponse>(")
	if invokeAt < 0 || invokeAt < checkAt || invokeAt < refuseAt {
		t.Fatalf("routed W2S method reaches InvokeAsync before the route-field check\n%s", routed)
	}

	// The platform method keeps today's expression-bodied shape, no check.
	platform := s[platformAt:]
	if strings.Contains(platform, "TenantRoute") {
		t.Fatalf("platform W2S method gained a route-field check\n%s", platform)
	}
	if !strings.Contains(platform, "System.Threading.CancellationToken cancellationToken = default) =>\n        _client.InvokeAsync<Pier.GetPierResponse>(") {
		t.Fatalf("platform W2S method changed shape\n%s", platform)
	}
}

func TestRenderW2SClientRoutedStreamingStaysStub(t *testing.T) {
	data := routedW2SData()
	m := &data.Services[0].Methods[0]
	m.IsStreaming = true
	out, err := executeCSharpTemplate("W2SClient.cs.tmpl", data)
	if err != nil {
		t.Fatalf("render W2SClient: %v", err)
	}
	if strings.Contains(string(out), "TenantRoute") {
		t.Fatalf("streaming W2S stub gained a route-field check; it is an unsupported stub\n%s", out)
	}
}

func TestRenderC2SClientRoutedInstallsTenantRouteInterceptor(t *testing.T) {
	out, err := executeCSharpTemplate("C2SClient.cs.tmpl", c2sClientTemplateData{
		PackageName:  "MyTacplus",
		TenantRouted: true,
		Routes: []c2sRouteTemplateData{
			{FullMethod: "/crew.CrewService/GetSkipper", FieldNumber: 1, FieldName: "tenant"},
			{FullMethod: "/crew.CrewService/SendMessage", FieldNumber: 2, FieldName: "target_tenant"},
		},
		Services: []c2dServiceTemplateData{
			{MethodName: "WithCrewService", ClientType: "Crew.CrewService.CrewServiceClient", TenantRouted: true},
			{MethodName: "WithPierService", ClientType: "Pier.PierService.PierServiceClient"},
		},
	})
	if err != nil {
		t.Fatalf("render C2SClient: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		"using Grpc.Core.Interceptors;",
		"_routedInvoker = _channel.Intercept(new TenantRouteInterceptor());",
		"public Crew.CrewService.CrewServiceClient WithCrewService() => new Crew.CrewService.CrewServiceClient(_routedInvoker);",
		// A platform service keeps the plain channel: no interceptor in its path.
		"public Pier.PierService.PierServiceClient WithPierService() => new Pier.PierService.PierServiceClient(_channel);",
		"internal sealed class TenantRouteInterceptor : Interceptor",
		`["/crew.CrewService/GetSkipper"] = (1, "tenant"),`,
		`["/crew.CrewService/SendMessage"] = (2, "target_tenant"),`,
		"new RpcException(new Status(StatusCode.InvalidArgument,",
		"TenantRoute.Refusal(",
		// Every call shape is covered: unary (blocking + async), server
		// streaming, and the request writer of client-streaming / bidi calls.
		"public override TResponse BlockingUnaryCall<TRequest, TResponse>(",
		"public override AsyncUnaryCall<TResponse> AsyncUnaryCall<TRequest, TResponse>(",
		"public override AsyncServerStreamingCall<TResponse> AsyncServerStreamingCall<TRequest, TResponse>(",
		"public override AsyncClientStreamingCall<TRequest, TResponse> AsyncClientStreamingCall<TRequest, TResponse>(",
		"public override AsyncDuplexStreamingCall<TRequest, TResponse> AsyncDuplexStreamingCall<TRequest, TResponse>(",
		"private sealed class TenantPinnedWriter<T> : IClientStreamWriter<T>",
		"public Task WriteAsync(T message, CancellationToken cancellationToken)",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("C2SClient.cs missing %q\n%s", want, s)
		}
	}

	// The unary check runs before the continuation (before anything is sent).
	unary := s[strings.Index(s, "AsyncUnaryCall<TRequest, TResponse>("):]
	unary = unary[:strings.Index(unary, "public override AsyncServerStreamingCall")]
	if c, k := strings.Index(unary, "Admit(context.Method.FullName, request);"), strings.Index(unary, "continuation(request, context)"); c < 0 || k < 0 || c > k {
		t.Errorf("AsyncUnaryCall does not check the route field before the continuation\n%s", unary)
	}
	// The stream writer checks a message before it is handed to the inner writer.
	writer := s[strings.Index(s, "private sealed class TenantPinnedWriter<T>"):]
	if c, k := strings.Index(writer, "Admit(message);"), strings.Index(writer, "_inner.WriteAsync(message)"); c < 0 || k < 0 || c > k {
		t.Errorf("TenantPinnedWriter writes a message before checking its route field\n%s", writer)
	}
}

func TestRenderC2SClientPlatformOnlyHasNoTenantRouting(t *testing.T) {
	out, err := executeCSharpTemplate("C2SClient.cs.tmpl", c2sClientTemplateData{
		PackageName: "MyTacplus",
		Services: []c2dServiceTemplateData{
			{MethodName: "WithContractApi", ClientType: "Contractclient.ContractApi.ContractApiClient"},
		},
	})
	if err != nil {
		t.Fatalf("render C2SClient: %v", err)
	}
	s := string(out)
	for _, unwanted := range []string{"Interceptors", "Intercept(", "_routedInvoker", "TenantRoute"} {
		if strings.Contains(s, unwanted) {
			t.Errorf("platform-only C2SClient.cs contains tenant-routing surface %q\n%s", unwanted, s)
		}
	}
}

func TestRenderTenantRouteHelper(t *testing.T) {
	out, err := executeCSharpTemplate("TenantRoute.cs.tmpl", tenantRouteTemplateData{PackageName: "MyTacplus"})
	if err != nil {
		t.Fatalf("render TenantRoute: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		"namespace MyTacplus;",
		"internal static class TenantRoute",
		"internal static bool IsTenantId(string? value)",
		"value.Length != 8",
		"(c >= '0' && c <= '9') || (c >= 'a' && c <= 'z')",
		"FindFieldByNumber(fieldNumber)",
		"internal static string? Refusal(IMessage? message, int fieldNumber, string fieldName, string rpc)",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("TenantRoute.cs missing %q\n%s", want, s)
		}
	}
	// .NET Regex `$` matches before a trailing newline and \d matches
	// non-ASCII digits: the tid check must be an explicit ASCII char loop.
	for _, unwanted := range []string{"Regex", "char.IsDigit", "char.IsLetter", "Trim", "ToLower"} {
		if strings.Contains(s, unwanted) {
			t.Errorf("TenantRoute.cs tid check uses %q; it must be an exact ASCII char loop\n%s", unwanted, s)
		}
	}
}

// tenantRoutedFixture writes a proto input dir with a tenant-owned service
// (crew: unary, server-streaming, client-streaming and bidi rpcs, each routed),
// a platform C2S service (pier), and a client package (dock) declaring C2S and
// W2S clients of both.
func tenantRoutedFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"registry.yaml": `services:
    - name: crew
      id: 1
    - name: pier
      id: 2
    - name: dock
      id: 3
`,
		"services/crew.proto": `syntax = "proto3";

package crew;

import "sys/spec.proto";

option (spec.service) = {name: "crew", tenant_owned: true};

service CrewService {
  option (spec.service_c2s) = {auth_entitled_users: true};

  rpc GetSkipper(GetSkipperRequest) returns (GetSkipperResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER, exposure: RPC_EXPOSURE_EXTERNAL, route_field: "tenant"};
  }
  rpc SendMessage(SendMessageRequest) returns (SendMessageResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER, exposure: RPC_EXPOSURE_EXTERNAL, route_field: "target_tenant",
                         flags: [RPC_FLAG_AUTH_ANY_USER, RPC_FLAG_ANY_TENANT]};
  }
  rpc GenerateRoster(GetSkipperRequest) returns (stream GetSkipperResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER, exposure: RPC_EXPOSURE_EXTERNAL, route_field: "tenant"};
  }
  rpc ExecuteBatch(stream SendMessageRequest) returns (SendMessageResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER, exposure: RPC_EXPOSURE_EXTERNAL, route_field: "target_tenant"};
  }
  rpc SendChat(stream SendMessageRequest) returns (stream SendMessageResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER, exposure: RPC_EXPOSURE_EXTERNAL, route_field: "target_tenant"};
  }
}

message GetSkipperRequest {
  string tenant = 1 [(spec.assert_max_size) = 8];
}

message GetSkipperResponse {
  string name = 1 [(spec.assert_max_size) = 128];
}

message SendMessageRequest {
  string text = 1 [(spec.assert_max_size) = 1024];
  string target_tenant = 2 [(spec.assert_max_size) = 8];
}

message SendMessageResponse {
  bool delivered = 1;
}
`,
		"services/pier.proto": `syntax = "proto3";

package pier;

import "sys/spec.proto";

option (spec.service) = {name: "pier"};

service PierService {
  option (spec.service_c2s) = {auth_entitled_users: true};

  rpc GetPier(GetPierRequest) returns (GetPierResponse) {
    option (spec.rpc) = {principal: RPC_PRINCIPAL_USER, exposure: RPC_EXPOSURE_EXTERNAL};
  }
}

message GetPierRequest {
  string name = 1 [(spec.assert_max_size) = 64];
}

message GetPierResponse {
  bool open = 1;
}
`,
		"services/dock.proto": `syntax = "proto3";

package dock;

import "sys/spec.proto";

option (spec.service) = {name: "dock"};
option (spec.grpc_c2s_client) = {service: "crew.CrewService"};
option (spec.grpc_w2s_client) = {service: "crew.CrewService"};
option (spec.grpc_c2s_client) = {service: "pier.PierService"};
option (spec.grpc_w2s_client) = {service: "pier.PierService"};

message DockNote {
  string text = 1 [(spec.assert_max_size) = 64];
}
`,
	}
	for rel, content := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
	return dir
}

func generateTenantRoutedFixture(t *testing.T) string {
	t.Helper()
	outputDir := filepath.Join(t.TempDir(), "csharp")
	cfg := &tactype.Config{
		Language:          "csharp",
		InputDir:          tenantRoutedFixture(t),
		OutputDir:         outputDir,
		SelfPackage:       "dock",
		CSharpPackageBase: "DockClient",
	}
	if err := Generate(cfg); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return outputDir
}

func readGenerated(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func TestGenerateTenantRoutedCSharpClients(t *testing.T) {
	out := generateTenantRoutedFixture(t)

	helper := readGenerated(t, out, "TenantRoute.cs")
	if !strings.Contains(helper, "namespace DockClient;") {
		t.Fatalf("TenantRoute.cs not in the package namespace\n%s", helper)
	}

	c2s := readGenerated(t, out, "C2SClient.cs")
	for _, want := range []string{
		"_routedInvoker = _channel.Intercept(new TenantRouteInterceptor());",
		"public Crew.CrewService.CrewServiceClient WithCrewService() => new Crew.CrewService.CrewServiceClient(_routedInvoker);",
		"public Pier.PierService.PierServiceClient WithPierService() => new Pier.PierService.PierServiceClient(_channel);",
		`["/crew.CrewService/GetSkipper"] = (1, "tenant"),`,
		`["/crew.CrewService/SendMessage"] = (2, "target_tenant"),`,
		`["/crew.CrewService/GenerateRoster"] = (1, "tenant"),`,
		`["/crew.CrewService/ExecuteBatch"] = (2, "target_tenant"),`,
		`["/crew.CrewService/SendChat"] = (2, "target_tenant"),`,
	} {
		if !strings.Contains(c2s, want) {
			t.Errorf("C2SClient.cs missing %q\n%s", want, c2s)
		}
	}
	if strings.Contains(c2s, "/pier.PierService/") {
		t.Errorf("C2SClient.cs routes a platform rpc\n%s", c2s)
	}

	w2s := readGenerated(t, out, "W2SClient.cs")
	for _, want := range []string{
		`TenantRoute.Refusal(request, 1, "tenant", "crew.CrewService.GetSkipper")`,
		`TenantRoute.Refusal(request, 2, "target_tenant", "crew.CrewService.SendMessage")`,
	} {
		if !strings.Contains(w2s, want) {
			t.Errorf("W2SClient.cs missing %q\n%s", want, w2s)
		}
	}
	if strings.Count(w2s, "TenantRoute.Refusal(") != 2 {
		t.Errorf("W2SClient.cs should check exactly the two unary routed rpcs (streaming stays a stub, platform unchecked)\n%s", w2s)
	}
}

// A build with no tenant-routed rpc emits no routing helper and no interceptor.
func TestGeneratePlatformOnlyCSharpHasNoTenantRouting(t *testing.T) {
	outputDir := filepath.Join(t.TempDir(), "csharp")
	cfg := &tactype.Config{
		Language:          "csharp",
		InputDir:          filepath.Join("..", "..", "proto", "testdata", "eventbus_c2s_contract"),
		OutputDir:         outputDir,
		SelfPackage:       "tasks",
		CSharpPackageBase: "TasksClient",
	}
	if err := Generate(cfg); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "TenantRoute.cs")); !os.IsNotExist(err) {
		t.Fatalf("platform-only build emitted TenantRoute.cs (stat err %v)", err)
	}
	c2s := readGenerated(t, outputDir, "C2SClient.cs")
	if strings.Contains(c2s, "TenantRoute") || strings.Contains(c2s, "_routedInvoker") {
		t.Fatalf("platform-only C2SClient.cs carries tenant routing\n%s", c2s)
	}
}
