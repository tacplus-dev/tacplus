// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"reflect"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
)

func TestSwiftServiceNamespace(t *testing.T) {
	tests := []struct {
		name        string
		packageName string
		serviceName string
		want        string
	}{
		{
			name:        "myclient daemon",
			packageName: "myclient",
			serviceName: "DaemonControl",
			want:        "Myclient_DaemonControl",
		},
		{
			name:        "nested package",
			packageName: "foo.bar",
			serviceName: "HookReceiver",
			want:        "Foo_Bar_HookReceiver",
		},
		{
			name:        "empty package",
			packageName: "",
			serviceName: "LocalDaemon",
			want:        "LocalDaemon",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := swiftServiceNamespace(tt.packageName, tt.serviceName); got != tt.want {
				t.Fatalf("swiftServiceNamespace(%q, %q) = %q, want %q", tt.packageName, tt.serviceName, got, tt.want)
			}
		})
	}
}

func TestC2DServicesTemplateData(t *testing.T) {
	services := []service.ServiceInfo{
		{PackageName: "myclient", ServiceName: "DaemonControl"},
		{PackageName: "myclient", ServiceName: "HookReceiver"},
	}

	got := c2dServicesTemplateData(services)
	want := []c2dServiceTemplateData{
		{MethodName: "withDaemonControl", ClientType: "Myclient_DaemonControl"},
		{MethodName: "withHookReceiver", ClientType: "Myclient_HookReceiver"},
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("c2dServicesTemplateData() = %#v, want %#v", got, want)
	}
}

func TestC2SServicesTemplateData(t *testing.T) {
	// C2S reuses the C2D method-shaping helper: each service becomes a
	// with<Service> entrypoint bound to the swift-protobuf service namespace.
	services := []service.ServiceInfo{
		{PackageName: "contractclient", ServiceName: "ContractApi"},
	}
	got := c2dServicesTemplateData(services)
	want := []c2dServiceTemplateData{
		{MethodName: "withContractApi", ClientType: "Contractclient_ContractApi"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("c2s services template data = %#v, want %#v", got, want)
	}
}

func TestSwiftLowerIdentifier(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"GetContract", "getContract"},
		{"GetPublicContract", "getPublicContract"},
		{"get_local", "getLocal"},
	}
	for _, tt := range tests {
		if got := swiftLowerIdentifier(tt.in); got != tt.want {
			t.Fatalf("swiftLowerIdentifier(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestW2SMethodTypeNamespacing(t *testing.T) {
	// W2S request/response types are referenced by their swift-protobuf name
	// (<Package>_<Message>), the same namespacing as service clients.
	if got := swiftServiceNamespace("contractclient", "ContractRequest"); got != "Contractclient_ContractRequest" {
		t.Fatalf("W2S input type namespace = %q, want %q", got, "Contractclient_ContractRequest")
	}
	if got := swiftServiceNamespace("contractclient", "ContractResponse"); got != "Contractclient_ContractResponse" {
		t.Fatalf("W2S output type namespace = %q, want %q", got, "Contractclient_ContractResponse")
	}
}

// W2S server-streaming methods carry the same route metadata as unary methods
// (the gateway serves them as SSE on that route). Only client-streaming/bidi
// are stubs. Before this, ALL streaming methods dropped their route.
func TestW2SMethodDataStreaming(t *testing.T) {
	svc := service.ServiceInfo{ServiceName: "contractclient.ContractApi"}
	target := &parser.ServiceInfo{Name: "ContractApi"}

	serverStreaming := &parser.MethodInfo{
		Name:            "ListEvents",
		ServerStreaming: true,
		InputType:       "ContractRequest",
		OutputType:      "ContractEvent",
		FullInputType:   ".contractclient.ContractRequest",
		FullOutputType:  ".contractclient.ContractEvent",
		HttpPath:        "/contractclient/ContractApi/ListEvents",
		HttpMethod:      "POST",
	}
	got, err := w2sMethodData(svc, target, serverStreaming, "contractclient")
	if err != nil {
		t.Fatalf("w2sMethodData(server-streaming): %v", err)
	}
	if !got.IsStreaming || got.IsUnsupportedStream {
		t.Fatalf("server-streaming flags = (IsStreaming %v, IsUnsupportedStream %v), want (true, false)", got.IsStreaming, got.IsUnsupportedStream)
	}
	if got.HTTPPath != serverStreaming.HttpPath {
		t.Errorf("server-streaming HTTPPath = %q, want %q", got.HTTPPath, serverStreaming.HttpPath)
	}
	if got.HTTPMethod != "POST" {
		t.Errorf("server-streaming HTTPMethod = %q, want POST", got.HTTPMethod)
	}
	if !got.AuthRequired {
		t.Error("server-streaming AuthRequired = false, want true")
	}

	// GET is used only when the input is google.protobuf.Empty, exactly as for
	// unary methods.
	emptyInput := *serverStreaming
	emptyInput.FullInputType = ".google.protobuf.Empty"
	emptyInput.HttpMethod = "GET"
	got, err = w2sMethodData(svc, target, &emptyInput, "contractclient")
	if err != nil {
		t.Fatalf("w2sMethodData(empty input): %v", err)
	}
	if got.HTTPMethod != "GET" {
		t.Errorf("Empty-input server-streaming HTTPMethod = %q, want GET", got.HTTPMethod)
	}
	nonEmpty := *serverStreaming
	nonEmpty.HttpMethod = "GET"
	got, err = w2sMethodData(svc, target, &nonEmpty, "contractclient")
	if err != nil {
		t.Fatalf("w2sMethodData(non-empty input): %v", err)
	}
	if got.HTTPMethod != "POST" {
		t.Errorf("non-Empty input with rest GET: HTTPMethod = %q, want POST", got.HTTPMethod)
	}

	// Client-streaming and bidi stay stubs: no route, marked unsupported.
	for _, tt := range []struct {
		name   string
		method *parser.MethodInfo
	}{
		{"client-streaming", &parser.MethodInfo{Name: "SendEvents", ClientStreaming: true, HttpPath: "/x"}},
		{"bidi", &parser.MethodInfo{Name: "AssistEvents", ClientStreaming: true, ServerStreaming: true, HttpPath: "/x"}},
	} {
		got, err := w2sMethodData(svc, target, tt.method, "contractclient")
		if err != nil {
			t.Fatalf("w2sMethodData(%s): %v", tt.name, err)
		}
		if !got.IsUnsupportedStream || got.IsStreaming {
			t.Errorf("%s flags = (IsStreaming %v, IsUnsupportedStream %v), want (false, true)", tt.name, got.IsStreaming, got.IsUnsupportedStream)
		}
		if got.HTTPPath != "" || got.HTTPMethod != "" {
			t.Errorf("%s must carry no route, got path %q method %q", tt.name, got.HTTPPath, got.HTTPMethod)
		}
	}

	// A routeless unary/server-streaming method is still a hard error.
	if _, err := w2sMethodData(svc, target, &parser.MethodInfo{Name: "NoRoute", ServerStreaming: true}, "contractclient"); err == nil {
		t.Error("w2sMethodData with no HTTP path: expected error, got nil")
	}
}

func TestDaemonDirectory(t *testing.T) {
	got := daemonDirectory(&tactype.Config{SelfPackage: "blue_rock"}, nil)
	if got != "blue-rock" {
		t.Fatalf("daemonDirectory() = %q, want %q", got, "blue-rock")
	}
}

// TestRenderC2SClientRejectsBearerTokenOverPlaintext pins the loopback restriction
// on the C2S bearer token: a token provider combined with useTLS=false is only
// allowed for a loopback host. There is no C#/Swift execution harness in this repo
// (cross-test builds Go/Java/Python/Rust/TypeScript only), so — as with the other
// C2SClient tests here — the contract is asserted on the emitted source.
func TestRenderC2SClientRejectsBearerTokenOverPlaintext(t *testing.T) {
	out, err := executeSwiftTemplate("C2SClient.swift.tmpl", c2sClientTemplateData{
		PackageName: "TasksClient",
		Services: []c2dServiceTemplateData{
			{MethodName: "withContractApi", ClientType: "Contractclient_ContractApi"},
		},
	})
	if err != nil {
		t.Fatalf("executeSwiftTemplate(C2SClient.swift.tmpl): %v", err)
	}
	s := string(out)

	// Plaintext + tokenProvider + non-loopback host must fail construction, so
	// `init` has to be a throwing initializer.
	if !strings.Contains(s, "        tokenProvider: C2STokenProvider? = nil\n    ) throws {\n") {
		t.Fatalf("C2SClient.swift init is not a throwing initializer\n%s", s)
	}
	guard := "if tokenProvider != nil, !useTLS, !C2SClient.isLoopbackHost(host) {"
	if !strings.Contains(s, guard) {
		t.Fatalf("C2SClient.swift does not gate the bearer token on TLS/loopback (missing %q)\n%s", guard, s)
	}
	for _, want := range []string{
		"public enum C2SClientError: Error {",
		"case bearerTokenOverPlaintext(host: String)",
		"throw C2SClientError.bearerTokenOverPlaintext(host: host)",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("C2SClient.swift missing %q\n%s", want, s)
		}
	}

	// Loopback hosts stay allowed (local dev / integration tests), and only the
	// "localhost" name is matched case-insensitively.
	want := `        host.lowercased() == "localhost" || host == "127.0.0.1" || host == "::1"`
	if !strings.Contains(s, want) {
		t.Errorf("C2SClient.swift loopback check missing %q\n%s", want, s)
	}

	// The TLS path is untouched: the interceptor is still installed whenever a
	// token provider survives the guard, and transport security still keys off
	// useTLS — but nothing is stored or dialed before the guard runs.
	guardAt := strings.Index(s, guard)
	for _, after := range []string{
		"self.tokenProvider = tokenProvider",
		"return [C2SBearerTokenInterceptor(tokenProvider: tokenProvider)]",
		"self.useTLS ? .tls : .plaintext",
	} {
		at := strings.Index(s, after)
		if at < 0 {
			t.Fatalf("C2SClient.swift missing %q\n%s", after, s)
		}
		if at < guardAt {
			t.Errorf("C2SClient.swift reaches %q before the plaintext-bearer guard\n%s", after, s)
		}
	}
}
