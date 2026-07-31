// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"reflect"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
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

func TestDaemonDirectory(t *testing.T) {
	got := daemonDirectory(&tactype.Config{SelfPackage: "blue_rock"}, nil)
	if got != "blue-rock" {
		t.Fatalf("daemonDirectory() = %q, want %q", got, "blue-rock")
	}
}
