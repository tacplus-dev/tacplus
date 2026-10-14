// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/service"
)

func TestCSharpIdentifier(t *testing.T) {
	cases := map[string]string{
		"contract-api": "ContractApi",
		"foo_bar":      "FooBar",
		"ProductApi":   "ProductApi",
		"common2":      "Common2",
		"":             "Generated",
	}
	for in, want := range cases {
		if got := csharpIdentifier(in); got != want {
			t.Errorf("csharpIdentifier(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCSharpNamespace(t *testing.T) {
	cases := map[string]string{
		"common2.product": "Common2.Product",
		"gateway":         "Gateway",
		"":                "",
	}
	for in, want := range cases {
		if got := csharpNamespace(in); got != want {
			t.Errorf("csharpNamespace(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCSharpServiceClientType(t *testing.T) {
	if got := csharpServiceClientType("common2.product", "ProductApi"); got != "Common2.Product.ProductApi.ProductApiClient" {
		t.Errorf("packaged client type = %q", got)
	}
	if got := csharpServiceClientType("", "Foo"); got != "Foo.FooClient" {
		t.Errorf("unpackaged client type = %q", got)
	}
}

func TestRenderCsprojTemplate(t *testing.T) {
	out, err := executeCSharpTemplate("Tacplus.csproj.tmpl", projectTemplateData{
		PackageName:          "MyTacplus",
		ProtobufVersion:      "3.27.2",
		GrpcNetClientVersion: "2.65.0",
		GrpcCoreApiVersion:   "2.65.0",
		GrpcToolsVersion:     "2.65.0",
		ClientProtoFiles:     []string{"contract/contract.proto"},
	})
	if err != nil {
		t.Fatalf("render csproj: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		"<AssemblyName>MyTacplus</AssemblyName>",
		"<RootNamespace>MyTacplus</RootNamespace>",
		`Include="Google.Protobuf" Version="3.27.2"`,
		`Include="Grpc.Net.Client" Version="2.65.0"`,
		`Include="Grpc.Tools" Version="2.65.0" PrivateAssets="All"`,
		`<Protobuf Include="Protos/**/*.proto" GrpcServices="None" ProtoRoot="Protos" />`,
		`<Protobuf Update="Protos/contract/contract.proto" GrpcServices="Client" />`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("csproj missing %q\n%s", want, s)
		}
	}
}

func TestC2DClientProtoFiles(t *testing.T) {
	files := c2dClientProtoFiles([]service.ServiceInfo{
		{ProtoFile: "zeta/service.proto"},
		{ProtoFile: "alpha/service.proto"},
		{ProtoFile: "zeta/service.proto"},
		{ProtoFile: ""},
	})
	got := strings.Join(files, ",")
	if got != "alpha/service.proto,zeta/service.proto" {
		t.Fatalf("c2dClientProtoFiles = %q", got)
	}
}

func TestRenderC2DClientTemplate(t *testing.T) {
	out, err := executeCSharpTemplate("C2DClient.cs.tmpl", c2dClientTemplateData{
		PackageName:     "MyTacplus",
		DaemonDirectory: "gateway",
		Services: []c2dServiceTemplateData{
			{MethodName: "WithProductApi", ClientType: "Common2.Product.ProductApi.ProductApiClient"},
		},
	})
	if err != nil {
		t.Fatalf("render C2DClient: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		"namespace MyTacplus;",
		"public sealed class C2DClient",
		"NamedPipesConnectionFactory.Create(PipeName)",
		`"gateway-daemon"`,
		"public Common2.Product.ProductApi.ProductApiClient WithProductApi() => new Common2.Product.ProductApi.ProductApiClient(_channel);",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("C2DClient.cs missing %q\n%s", want, s)
		}
	}
	// The C# client is named-pipe only: no unix-domain-socket transport or
	// transport-selection surface should leak into the generated client.
	for _, unwanted := range []string{
		"C2DTransport",
		"UnixDomainSocket",
		"DefaultUnixSocketPath",
		"DefaultTransport",
	} {
		if strings.Contains(s, unwanted) {
			t.Errorf("C2DClient.cs unexpectedly contains UDS/transport surface %q\n%s", unwanted, s)
		}
	}
}

func TestC2SServicesTemplateData(t *testing.T) {
	// C2S reuses the C2D method-shaping helper: each service becomes a
	// With<Service> entrypoint bound to the Grpc.Tools client type.
	services := []service.ServiceInfo{
		{PackageName: "contractclient", ServiceName: "ContractApi"},
	}
	got := c2dServicesTemplateData(services)
	if len(got) != 1 ||
		got[0].MethodName != "WithContractApi" ||
		got[0].ClientType != "Contractclient.ContractApi.ContractApiClient" {
		t.Fatalf("c2s services template data = %#v", got)
	}
}

func TestCSharpProtoTypeName(t *testing.T) {
	cases := []struct {
		full, simple, pkg, want string
	}{
		{"contractclient.ContractRequest", "ContractRequest", "contractclient", "Contractclient.ContractRequest"},
		{".contractclient.ContractResponse", "ContractResponse", "contractclient", "Contractclient.ContractResponse"},
		{"", "Local", "gateway", "Gateway.Local"},
		{"Bare", "Bare", "", "Bare"},
	}
	for _, c := range cases {
		if got := csharpProtoTypeName(c.full, c.simple, c.pkg); got != c.want {
			t.Errorf("csharpProtoTypeName(%q,%q,%q) = %q, want %q", c.full, c.simple, c.pkg, got, c.want)
		}
	}
}

func TestRenderC2SClientTemplate(t *testing.T) {
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
	for _, want := range []string{
		"namespace MyTacplus;",
		"public sealed class C2SClient",
		"Func<CancellationToken, Task<string>>? tokenProvider",
		"CallCredentials.FromInterceptor",
		`metadata.Add("authorization", $"Bearer {token}");`,
		"public Contractclient.ContractApi.ContractApiClient WithContractApi() => new Contractclient.ContractApi.ContractApiClient(_channel);",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("C2SClient.cs missing %q\n%s", want, s)
		}
	}
}

func TestRenderW2SClientTemplate(t *testing.T) {
	out, err := executeCSharpTemplate("W2SClient.cs.tmpl", w2sClientTemplateData{
		PackageName: "MyTacplus",
		Services: []w2sServiceTemplateData{
			{
				ServiceName:    "ContractApi",
				ClientTypeName: "ContractApiW2SClient",
				PackageName:    "contractclient",
				Methods: []w2sMethodTemplateData{
					{
						MethodName:   "GetContract",
						HTTPMethod:   "GET",
						HTTPPath:     "/contract/read",
						AuthRequired: true,
						InputType:    "Contractclient.ContractRequest",
						OutputType:   "Contractclient.ContractResponse",
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("render W2SClient: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		"namespace MyTacplus;",
		"public sealed class W2SClient",
		"JsonFormatter.Default.Format(request)",
		"JsonParser.Default.Parse<TResponse>(body)",
		"public sealed class ContractApiW2SClient",
		"public Task<Contractclient.ContractResponse> GetContractAsync(",
		"Contractclient.ContractRequest request,",
		"_client.InvokeAsync<Contractclient.ContractResponse>(",
		`"GET",`,
		`"/contract/read",`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("W2SClient.cs missing %q\n%s", want, s)
		}
	}
}

func TestRenderNamedPipesConnectionFactoryTemplate(t *testing.T) {
	out, err := executeCSharpTemplate("NamedPipesConnectionFactory.cs.tmpl", c2dClientTemplateData{
		PackageName: "MyTacplus",
	})
	if err != nil {
		t.Fatalf("render NamedPipesConnectionFactory: %v", err)
	}
	s := string(out)
	for _, want := range []string{
		"namespace MyTacplus;",
		"internal static class NamedPipesConnectionFactory",
		"NamedPipeClientStream",
		`@"\\.\pipe\"`,
		"NormalizePipeName",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("NamedPipesConnectionFactory.cs missing %q\n%s", want, s)
		}
	}
}

// TestRenderC2SClientRejectsBearerTokenOverPlaintext pins the loopback restriction
// on the C2S bearer token: a token provider combined with useTls=false is only
// allowed for a loopback host. There is no C#/Swift execution harness in this repo
// (cross-test builds Go/Java/Python/Rust/TypeScript only), so — as with the other
// C2SClient tests here — the contract is asserted on the emitted source.
func TestRenderC2SClientRejectsBearerTokenOverPlaintext(t *testing.T) {
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

	// Plaintext + tokenProvider + non-loopback host must throw before any channel
	// or credential is built.
	guard := "if (tokenProvider != null && !useTls && !IsLoopbackHost(host))"
	if !strings.Contains(s, guard) {
		t.Fatalf("C2SClient.cs does not gate the bearer token on TLS/loopback (missing %q)\n%s", guard, s)
	}
	if !strings.Contains(s, "throw new ArgumentException(") {
		t.Errorf("C2SClient.cs loopback guard does not throw ArgumentException\n%s", s)
	}

	// Loopback hosts stay allowed (local dev / integration tests), and only the
	// "localhost" name is matched case-insensitively.
	for _, want := range []string{
		"private static bool IsLoopbackHost(string host) =>",
		`string.Equals(host, "localhost", StringComparison.OrdinalIgnoreCase)`,
		`|| host == "127.0.0.1"`,
		`|| host == "::1";`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("C2SClient.cs loopback check missing %q\n%s", want, s)
		}
	}

	// The TLS path is untouched: credentials are still composed and the insecure
	// escape hatch is still keyed off !useTls — but it is now only reachable past
	// the guard above.
	guardAt := strings.Index(s, guard)
	for _, after := range []string{
		"options.UnsafeUseInsecureChannelCallCredentials = !useTls;",
		"CallCredentials.FromInterceptor",
		"GrpcChannel.ForAddress(",
	} {
		at := strings.Index(s, after)
		if at < 0 {
			t.Fatalf("C2SClient.cs missing %q\n%s", after, s)
		}
		if at < guardAt {
			t.Errorf("C2SClient.cs reaches %q before the plaintext-bearer guard\n%s", after, s)
		}
	}
}

// TestRenderC2SClientTokenProviderHonorsCallCancellation pins that the bearer
// token is fetched with the RPC's own cancellation token, not
// CancellationToken.None. Grpc.Core.AuthInterceptorContext exposes
// CancellationToken as "the cancellation token of the RPC being called"; passing
// None instead leaves a blocked token provider (and the call waiting on it)
// running after the caller cancels or the deadline expires. As with the other
// C2SClient tests here, there is no C# execution harness in this repo, so the
// contract is asserted on the emitted source.
func TestRenderC2SClientTokenProviderHonorsCallCancellation(t *testing.T) {
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

	want := "await tokenProvider(context.CancellationToken).ConfigureAwait(false)"
	if !strings.Contains(s, want) {
		t.Fatalf("C2SClient.cs does not thread the per-call cancellation token into the token provider (missing %q)\n%s", want, s)
	}
	if strings.Contains(s, "tokenProvider(CancellationToken.None)") {
		t.Errorf("C2SClient.cs still acquires the bearer token with CancellationToken.None\n%s", s)
	}
}
