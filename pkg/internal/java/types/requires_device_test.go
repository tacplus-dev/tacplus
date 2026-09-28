// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// javaApprovalFixtureFiles is the proto model a requires_device RPC is generated
// against: a request that reaches no map, plus a variant that reaches one three
// messages down (the canonicalization recurses, so the refusal must too).
func javaApprovalFixtureFiles() []model.ProtoFile {
	return []model.ProtoFile{{
		Package: "vault",
		Messages: []model.Message{
			{Name: "RotateKeyRequest", Fields: []model.Field{
				{Name: "key_id", Type: "string", Number: 1},
				{Name: "detail", Type: "RotateDetail", IsMessage: true, Number: 2},
			}},
			{Name: "RotateDetail", Fields: []model.Field{
				{Name: "reason", Type: "string", Number: 1},
			}},
			{Name: "RotateKeyResponse", Fields: []model.Field{
				{Name: "revision", Type: "int64", Number: 1},
			}},
			{Name: "TagKeyRequest", Fields: []model.Field{
				{Name: "key_id", Type: "string", Number: 1},
				{Name: "detail", Type: "TagDetail", IsMessage: true, Number: 2},
			}},
			{Name: "TagDetail", Fields: []model.Field{
				{Name: "nested", Type: "TagLabels", IsMessage: true, Number: 1},
			}},
			{Name: "TagLabels", Fields: []model.Field{
				{Name: "labels", Type: "string", IsMap: true, MapKeyType: "string", MapValueType: "string", Number: 1},
			}},
		},
	}}
}

func javaApprovalFixtureMethod(name, input string) *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:           name,
		InputType:      input,
		OutputType:     "RotateKeyResponse",
		FullInputType:  "vault." + input,
		FullOutputType: "vault.RotateKeyResponse",
		RequiresDevice: true,
	}
}

func javaApprovalFixtureSvc(t *testing.T) ServiceInfo {
	t.Helper()
	outputDir := t.TempDir()
	return ServiceInfo{
		ProtoFile:      "services/vault.proto",
		PackageName:    "vault",
		ServiceName:    "VaultAdmin",
		GrpcName:       "vault",
		IsGrpc:         true,
		IsS2S:          true,
		ParentDir:      "services",
		OutputDir:      filepath.Join(outputDir, "wrapper"),
		TypesOutputDir: filepath.Join(outputDir, "types"),
		AuthServices:   []string{"identity"},
		Config: &tactype.Config{
			OutputDir:       outputDir,
			JavaPackageBase: "com.example",
		},
	}
}

func javaApprovalProtoInfo(method *parser.MethodInfo) *parser.ProtoInfo {
	return &parser.ProtoInfo{
		Package:  "vault",
		Services: []*parser.ServiceInfo{{Name: "VaultAdmin", IsS2S: true, Methods: []*parser.MethodInfo{method}}},
	}
}

// A requires_device RPC's generated wrapper must gate the handler call: read the
// two metadata keys, hash the request through the shared canonicalization, broker
// the approval, and only then call the handler. The ORDER is the point — a gate
// emitted after the handler call would be no gate at all.
func TestJavaRequiresDeviceGatesTheHandlerCall(t *testing.T) {
	san := newJavaEgressSanitizer(javaApprovalFixtureFiles())
	method := javaApprovalFixtureMethod("RotateKey", "RotateKeyRequest")

	src := generateJavaGRPCWrapper(method, "VaultAdmin", "vault",
		"Vault.RotateKeyRequest", "Vault.RotateKeyResponse", "Helpers", "Helpers",
		false, false, nil, "vault", "services", nil, proto.SkippedTraceTypes{}, san, nil, nil)

	for _, want := range []string{
		"Approval.Presented __approval = Approval.fromIncomingContext();",
		"byte[] __requestHash = Approval.requestHash(request);",
		`brokerApproval("vault.VaultAdmin/RotateKey", __approval, __requestHash, caller);`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("generated wrapper is missing %q:\n%s", want, src)
		}
	}

	// rpc_method is package-qualified, matching Go's qualifiedMethodAuthKey. It is
	// a WIRE value kernel compares against the approval, not this process's own
	// auth key, so the two languages must spell it identically.
	if strings.Contains(src, `brokerApproval("VaultAdmin/RotateKey"`) {
		t.Fatalf("rpc_method is not package-qualified; a Go service and a Java service serving the same rpc would report different rpc_method to the broker:\n%s", src)
	}

	gate := strings.Index(src, "brokerApproval(")
	handler := strings.Index(src, "handler.rotateKey(")
	if gate < 0 || handler < 0 || gate > handler {
		t.Fatalf("the device-approval gate must run BEFORE the handler call (gate=%d handler=%d):\n%s", gate, handler, src)
	}
	// The caller extraction the gate reads its identity from has to precede it.
	if extract := strings.Index(src, "Caller caller = Auth.callerFromContext();"); extract < 0 || extract > gate {
		t.Fatalf("the gate must run after caller extraction, not before it:\n%s", src)
	}
	// An RPC that declares nothing gets no gate at all.
	plain := javaApprovalFixtureMethod("PlainRotate", "RotateKeyRequest")
	plain.RequiresDevice = false
	plainSrc := generateJavaGRPCWrapper(plain, "VaultAdmin", "vault",
		"Vault.RotateKeyRequest", "Vault.RotateKeyResponse", "Helpers", "Helpers",
		false, false, nil, "vault", "services", nil, proto.SkippedTraceTypes{}, san, nil, nil)
	if strings.Contains(plainSrc, "brokerApproval") || strings.Contains(plainSrc, "Approval.") {
		t.Fatalf("an rpc without requires_device must carry no approval gate:\n%s", plainSrc)
	}
}

// The whole wrapper file must carry the wiring seam and the chassis import, so
// the gate the method emits actually compiles in the generated package.
func TestJavaRequiresDeviceWrapperFileCarriesTheWiringSeam(t *testing.T) {
	svc := javaApprovalFixtureSvc(t)
	method := javaApprovalFixtureMethod("RotateKey", "RotateKeyRequest")

	if _, err := generateServiceWrapper(svc, javaApprovalProtoInfo(method), nil, proto.SkippedTraceTypes{},
		newJavaEgressSanitizer(javaApprovalFixtureFiles())); err != nil {
		t.Fatalf("generateServiceWrapper: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(svc.OutputDir, "VaultAdmin.java"))
	if err != nil {
		t.Fatalf("read generated wrapper: %v", err)
	}
	src := string(raw)
	for _, want := range []string{
		"import com.example.chassis.approval.Approval;",
		"public void setApprovalCheckFunc(Approval.Check check)",
		"private void brokerApproval(String rpcMethod, Approval.Presented presented, byte[] requestHash, Caller caller)",
		// Fail-closed: an unwired check refuses rather than serving unguarded.
		"device-approval check not configured",
		"a device-approval rpc requires an authenticated caller",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("generated wrapper file is missing %q:\n%s", want, src)
		}
	}

	// A service with no guarded rpc generates exactly what it generated before:
	// no seam, no chassis import.
	plainSvc := javaApprovalFixtureSvc(t)
	plain := javaApprovalFixtureMethod("PlainRotate", "RotateKeyRequest")
	plain.RequiresDevice = false
	if _, err := generateServiceWrapper(plainSvc, javaApprovalProtoInfo(plain), nil, proto.SkippedTraceTypes{},
		newJavaEgressSanitizer(javaApprovalFixtureFiles())); err != nil {
		t.Fatalf("generateServiceWrapper (plain): %v", err)
	}
	plainRaw, err := os.ReadFile(filepath.Join(plainSvc.OutputDir, "VaultAdmin.java"))
	if err != nil {
		t.Fatalf("read generated wrapper (plain): %v", err)
	}
	if strings.Contains(string(plainRaw), "Approval") {
		t.Fatalf("an ungated service's wrapper must not mention the approval chassis:\n%s", plainRaw)
	}
}

// A requires_device RPC whose request reaches a map field cannot be hashed
// identically on both ends, so the build fails with a message that names the
// field — never a panic and never a silently ungated RPC.
func TestJavaRequiresDeviceMapFieldRefusesToBuild(t *testing.T) {
	svc := javaApprovalFixtureSvc(t)
	method := javaApprovalFixtureMethod("TagKey", "TagKeyRequest")

	_, err := generateServiceWrapper(svc, javaApprovalProtoInfo(method), nil, proto.SkippedTraceTypes{},
		newJavaEgressSanitizer(javaApprovalFixtureFiles()))
	if err == nil {
		t.Fatal("a requires_device rpc whose request reaches a map field generated a wrapper; the approval hash is not canonical across languages")
	}
	for _, want := range []string{"requires_device", "VaultAdmin.TagKey", "TagLabels.labels", "map"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal message does not mention %q: %v", want, err)
		}
	}
}

// requires_device has two more structural preconditions: a verified caller (the
// check must name the user being served) and exactly one request to hash.
func TestJavaRequiresDeviceStructuralRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*parser.MethodInfo)
		want   string
	}{
		{"auth disabled", func(m *parser.MethodInfo) { m.AuthDisabled = true }, "auth is disabled"},
		{"server streaming", func(m *parser.MethodInfo) { m.ServerStreaming = true }, "not a plain unary rpc"},
		{"client streaming", func(m *parser.MethodInfo) { m.ClientStreaming = true }, "not a plain unary rpc"},
		{"collection", func(m *parser.MethodInfo) { m.Collection = &parser.CollectionDescriptor{Mode: "PAGE"} }, "not a plain unary rpc"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := javaApprovalFixtureSvc(t)
			method := javaApprovalFixtureMethod("RotateKey", "RotateKeyRequest")
			tc.mutate(method)
			_, err := generateServiceWrapper(svc, javaApprovalProtoInfo(method), nil, proto.SkippedTraceTypes{},
				newJavaEgressSanitizer(javaApprovalFixtureFiles()))
			if err == nil {
				t.Fatalf("%s: generation succeeded; the rpc would be ungated or unhashable", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("%s: refusal message does not say %q: %v", tc.name, tc.want, err)
			}
		})
	}
}

// The caller-side half: the same shared hash function, and metadata attachment
// the caller never writes by hand.
func TestJavaRequiresDeviceClientHelpers(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "VaultAdmin"}
	method := javaApprovalFixtureMethod("RotateKey", "RotateKeyRequest")

	src := javaRequiresDeviceClientHelpers(svc, method, "Vault.RotateKeyRequest", "Vault.RotateKeyResponse")
	for _, want := range []string{
		"public byte[] rotateKeyApprovalHash(Vault.RotateKeyRequest request) {",
		"return Approval.requestHash(request);",
		"public Vault.RotateKeyResponse rotateKeyWithApproval(Vault.RotateKeyRequest request, String approvalId, byte[] deviceAssertion) {",
		"return Approval.callWithApproval(approvalId, deviceAssertion, () -> rotateKey(request));",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("generated client helpers are missing %q:\n%s", want, src)
		}
	}

	plain := javaApprovalFixtureMethod("PlainRotate", "RotateKeyRequest")
	plain.RequiresDevice = false
	if got := javaRequiresDeviceClientHelpers(svc, plain, "Vault.RotateKeyRequest", "Vault.RotateKeyResponse"); got != "" {
		t.Fatalf("an rpc without requires_device must get no approval helpers:\n%s", got)
	}
}

// The ServiceManager half: the approval check is built where the wrappers are
// created and wired into every wrapper that carries the gate. The broker is
// reached over the auto-injected S2S client through the aggregator FIELD
// (grpcClients), which is what exists in that scope.
func TestJavaServiceManagerApprovalBrokerTemplate(t *testing.T) {
	raw, err := templates.FS.ReadFile("java/types/ServiceManager.java.tmpl")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	tmpl := string(raw)
	for _, want := range []string{
		"Approval.Check approvalCheck = (rpcMethod, approvalId, requestHash, callerNamespace, callerSubject, deviceAssertion) -> {",
		"grpcClients.{{.GroupAccessor}}().{{.ClientAccessor}}().{{.CallName}}(",
		"{{.JavaTypeFQN}}.BrokerPendingApprovalRequest.newBuilder()",
		"{{.JavaTypeFQN}}.WebauthnAssertion.parseFrom(deviceAssertion)",
		"{{.WrapperVarName}}.setApprovalCheckFunc(approvalCheck);",
	} {
		if !strings.Contains(tmpl, want) {
			t.Fatalf("ServiceManager.java.tmpl is missing %q", want)
		}
	}
}

// The two metadata keys are a WIRE CONTRACT: kernel and every other language's
// chassis spell them exactly this way, and a rename here silently refuses every
// cross-language guarded call. Pinned in the Java chassis as it is in Go's.
func TestJavaApprovalMetadataKeysAreTheWireContract(t *testing.T) {
	raw, err := templates.FS.ReadFile("java/chassis/approval/Approval.java.tmpl")
	if err != nil {
		t.Fatalf("read Approval.java template: %v", err)
	}
	src := string(raw)
	for _, want := range []string{
		`METADATA_APPROVAL_ID = "x-tacplus-approval-id"`,
		`METADATA_DEVICE_ASSERTION = "x-tacplus-device-assertion-bin"`,
		"Metadata.BINARY_BYTE_MARSHALLER",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("Approval.java template is missing %q", want)
		}
	}

	// gRPC-Java does not put incoming metadata on the Context by itself, so the
	// one interceptor every generated server installs must copy these two in.
	// Without it the gate reads null and refuses every guarded rpc.
	authRaw, err := templates.FS.ReadFile("java/chassis/grpcserver/Authorizer.java.tmpl")
	if err != nil {
		t.Fatalf("read Authorizer.java template: %v", err)
	}
	if !strings.Contains(string(authRaw), "Approval.capture(") {
		t.Fatal("Authorizer.headerCaptureInterceptor no longer captures the approval metadata; " +
			"every (spec.rpc).requires_device rpc would be refused for absent headers")
	}
}
