// Copyright (c) 2026 Seabeam LLC
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
	"github.com/tacplus-dev/tacplus/templates"
)

// approvalFixtureFiles is the proto model a requires_device RPC is generated
// against: a request that reaches no map, plus a variant that reaches one three
// messages down (the canonicalization recurses, so the refusal must too).
func approvalFixtureFiles() []model.ProtoFile {
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

func approvalFixtureMethod(name, input string) *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:           name,
		InputType:      input,
		OutputType:     "RotateKeyResponse",
		FullInputType:  "vault." + input,
		FullOutputType: "vault.RotateKeyResponse",
		RequiresDevice: true,
	}
}

func approvalFixtureSvc(t *testing.T) ServiceInfo {
	t.Helper()
	return ServiceInfo{
		ProtoFile:   "services/vault.proto",
		PackageName: "vault",
		DirName:     "vault",
		ServiceName: "VaultAdmin",
		GrpcName:    "vault",
		ParentDir:   "services",
		Config:      &tactype.Config{OutputDir: t.TempDir(), ModuleBasePath: "example.com/m"},
	}
}

// A requires_device RPC's generated wrapper must gate the handler call: read the
// two metadata keys, hash the request through the shared canonicalization, broker
// the approval, and only then call the handler. The ORDER is the point — a gate
// emitted after the handler call would be no gate at all.
func TestGoRequiresDeviceGatesTheHandlerCall(t *testing.T) {
	san := newEgressSanitizer(approvalFixtureFiles())
	method := approvalFixtureMethod("RotateKey", "RotateKeyRequest")
	svc := &parser.ServiceInfo{Name: "VaultAdmin", Methods: []*parser.MethodInfo{method}}

	src := generateGRPCWrapper(svc, method, "vault", "services", "vault",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, san)

	for _, want := range []string{
		"approval.FromIncomingContext(ctx)",
		"approval.RequestHash(req)",
		`p.brokerApproval(ctx, "vault.VaultAdmin/RotateKey", approvalID, requestHash, caller, deviceAssertion)`,
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("generated wrapper is missing %q:\n%s", want, src)
		}
	}

	gate := strings.Index(src, "p.brokerApproval(")
	handler := strings.Index(src, "p.handler.RotateKey(")
	if gate < 0 || handler < 0 || gate > handler {
		t.Fatalf("the device-approval gate must run BEFORE the handler call (gate=%d handler=%d):\n%s", gate, handler, src)
	}
	// The caller extraction the gate reads its identity from has to precede it.
	if extract := strings.Index(src, "servicemanager.NewCaller(authTok, requestID)"); extract < 0 || extract > gate {
		t.Fatalf("the gate must run after caller extraction, not before it:\n%s", src)
	}
	// An RPC that declares nothing gets no gate at all.
	plain := approvalFixtureMethod("PlainRotate", "RotateKeyRequest")
	plain.RequiresDevice = false
	plainSrc := generateGRPCWrapper(&parser.ServiceInfo{Name: "VaultAdmin"}, plain, "vault", "services", "vault",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{}, san)
	if strings.Contains(plainSrc, "brokerApproval") {
		t.Fatalf("an rpc without requires_device must carry no approval gate:\n%s", plainSrc)
	}
}

// The whole wrapper file must carry the wiring seam and the chassis import, so
// the gate the method emits actually compiles in the generated package.
func TestGoRequiresDeviceWrapperFileCarriesTheWiringSeam(t *testing.T) {
	svc := approvalFixtureSvc(t)
	method := approvalFixtureMethod("RotateKey", "RotateKeyRequest")
	info := &parser.ProtoInfo{
		Package:  "vault",
		Services: []*parser.ServiceInfo{{Name: "VaultAdmin", Methods: []*parser.MethodInfo{method}}},
	}
	san := newEgressSanitizer(approvalFixtureFiles())

	if err := generateServiceWrapper(svc, info, map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, san); err != nil {
		t.Fatalf("generateServiceWrapper: %v", err)
	}
	wrapperDir := filepath.Join(svc.Config.OutputDir, svc.Config.GetSmPrefix(), "grpc", "servers", "services", "vault")
	matches, err := filepath.Glob(filepath.Join(wrapperDir, "*.go"))
	if err != nil || len(matches) == 0 {
		t.Fatalf("no wrapper file written under %s (err=%v)", wrapperDir, err)
	}
	var src string
	for _, m := range matches {
		if strings.HasSuffix(m, "_mcp.go") || strings.HasSuffix(m, "_test.go") {
			continue
		}
		b, rerr := os.ReadFile(m)
		if rerr != nil {
			t.Fatalf("read %s: %v", m, rerr)
		}
		src = string(b)
	}
	for _, want := range []string{
		`"example.com/m/chassis/approval"`,
		"func (p *VaultAdmin) SetApprovalCheckFunc(",
		"func (p *VaultAdmin) brokerApproval(",
		// Fail-closed: an unwired check refuses rather than serving unguarded.
		"device-approval check not configured",
		"a device-approval rpc requires an authenticated caller",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("generated wrapper file is missing %q:\n%s", want, src)
		}
	}
}

// A requires_device RPC whose request reaches a map field cannot be hashed
// identically on both ends, so the build fails with a message that names the
// field — never a panic and never a silently ungated RPC.
func TestGoRequiresDeviceMapFieldRefusesToBuild(t *testing.T) {
	svc := approvalFixtureSvc(t)
	method := approvalFixtureMethod("TagKey", "TagKeyRequest")
	info := &parser.ProtoInfo{
		Package:  "vault",
		Services: []*parser.ServiceInfo{{Name: "VaultAdmin", Methods: []*parser.MethodInfo{method}}},
	}

	err := generateServiceWrapper(svc, info, map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, newEgressSanitizer(approvalFixtureFiles()))
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
func TestGoRequiresDeviceStructuralRefusals(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*parser.MethodInfo)
		want   string
	}{
		{"auth disabled", func(m *parser.MethodInfo) { m.AuthDisabled = true }, "auth is disabled"},
		{"server streaming", func(m *parser.MethodInfo) { m.ServerStreaming = true }, "not a plain unary rpc"},
		{"client streaming", func(m *parser.MethodInfo) { m.ClientStreaming = true }, "not a plain unary rpc"},
		{"collection", func(m *parser.MethodInfo) { m.Collection = &parser.CollectionDescriptor{Mode: "PAGE"} }, "not a plain unary rpc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := approvalFixtureSvc(t)
			method := approvalFixtureMethod("RotateKey", "RotateKeyRequest")
			tc.mutate(method)
			info := &parser.ProtoInfo{
				Package:  "vault",
				Services: []*parser.ServiceInfo{{Name: "VaultAdmin", Methods: []*parser.MethodInfo{method}}},
			}
			err := generateServiceWrapper(svc, info, map[string]service.AccountingInfo{}, proto.SkippedTraceTypes{}, newEgressSanitizer(approvalFixtureFiles()))
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
func TestGoRequiresDeviceClientHelpers(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "VaultAdmin"}
	method := approvalFixtureMethod("RotateKey", "RotateKeyRequest")

	src, err := generateGrpcClientMethod(svc, method, "vault", "services")
	if err != nil {
		t.Fatalf("generateGrpcClientMethod: %v", err)
	}
	for _, want := range []string{
		"func (c *{{.ClientName}}) RotateKeyApprovalHash(req *types.RotateKeyRequest) ([]byte, error)",
		"return approval.RequestHash(req)",
		"func (c *{{.ClientName}}) RotateKeyWithApproval(ctx context.Context, req *types.RotateKeyRequest, approvalID string, deviceAssertion []byte, opts ...grpc.CallOption) (*types.RotateKeyResponse, error)",
		"return c.RotateKey(approval.AttachOutgoing(ctx, approvalID, deviceAssertion), req, opts...)",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("generated client is missing %q:\n%s", want, src)
		}
	}

	plain := approvalFixtureMethod("PlainRotate", "RotateKeyRequest")
	plain.RequiresDevice = false
	plainSrc, err := generateGrpcClientMethod(svc, plain, "vault", "services")
	if err != nil {
		t.Fatalf("generateGrpcClientMethod: %v", err)
	}
	if strings.Contains(plainSrc, "WithApproval") || strings.Contains(plainSrc, "ApprovalHash") {
		t.Fatalf("an rpc without requires_device must get no approval helpers:\n%s", plainSrc)
	}
}

// The ServiceManager half: the approval check is built in Run's scope and wired
// into every wrapper that carries the gate. The gRPC aggregator is reached as
// sm.grpcPkg there — a bare grpcPkg is a local of the CONSTRUCTOR, not of Run,
// so it compiles nowhere and no unit test that only renders a method body would
// notice. This pin is the one the generated-project compile caught.
func TestServiceManagerApprovalBrokerTemplate(t *testing.T) {
	src, err := templates.FS.ReadFile("go/types/service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	tmpl := string(src)
	for _, want := range []string{
		"sm.grpcPkg.Clients().{{.ApprovalBroker.GroupAccessor}}().{{.ApprovalBroker.ClientAccessor}}().{{.ApprovalBroker.MethodName}}(",
		"{{.ApprovalBroker.TypePkgAlias}}.BrokerPendingApprovalRequest{",
		"{{.ApprovalBroker.TypePkgAlias}}.WebauthnAssertion{",
		"{{.WrapperVarName}}.SetApprovalCheckFunc(approvalCheck)",
	} {
		if !strings.Contains(tmpl, want) {
			t.Fatalf("service_manager.go.tmpl is missing %q", want)
		}
	}
	if strings.Contains(tmpl, "\ngrpcPkg.Clients()") {
		t.Fatal("the approval check runs in Run, where the aggregator is sm.grpcPkg; a bare grpcPkg does not exist there")
	}
}
