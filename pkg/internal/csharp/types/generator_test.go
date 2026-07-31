// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
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
