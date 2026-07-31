// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
	"text/template"

	commonparser "gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

// TestBidiTemplateRenders verifies the bidi lifecycle template loads,
// parses, executes against representative data, and produces the expected
// API surface (handle struct, Send/Close, lifecycle goroutine).
func TestBidiTemplateRenders(t *testing.T) {
	tmplStr, err := loadTemplate("grpcclient_bidi.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	tmpl, err := template.New("bidi").Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	data := struct {
		MethodName, ServiceName, ClientName, GoInput, GoOutput, HandleType, LoopFnName, RunFnName, MinBackoff, MaxBackoff string
		ValidateInput, ValidateOutput                                                                                     string
		AuthRequired                                                                                                      bool
	}{
		"ManageCert", "TlsCertService", "MyClient",
		"types.ClientMsg", "types.ServerMsg",
		"TlsCertServiceManageCertBidiHandle", "manageCertBidiLoop", "manageCertBidiRun",
		"1 * time.Second", "30 * time.Second",
		"types.ValidateClientMsg", "types.ValidateServerMsg",
		true,
	}
	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		t.Fatalf("execute: %v", err)
	}
	out := sb.String()
	for _, want := range []string{
		"func (c *MyClient) ManageCert(",
		"type TlsCertServiceManageCertBidiHandle struct",
		"func (h *TlsCertServiceManageCertBidiHandle) Send(msg *types.ClientMsg) error",
		"func (h *TlsCertServiceManageCertBidiHandle) Close()",
		"go c.manageCertBidiLoop(",
		"func (c *MyClient) manageCertBidiRun(",
		"const minBackoff = 1 * time.Second",
		"const maxBackoff = 30 * time.Second",
		"onConnect(send)",
		"validateMessage(msg)",
		"onMessage(msg)",
		"response validation failed",
		"stream.Send(msg)",
		"stream.Recv()",
		"stream.CloseSend()",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered template missing %q", want)
		}
	}

	for _, bad := range []string{
		"tokenProvider func() (string, error)",
		"authRequired  bool",
		"func (h *TlsCertServiceManageCertBidiHandle) validateAndAuthorize",
		"token, err := h.tokenProvider()",
	} {
		if strings.Contains(out, bad) {
			t.Errorf("bidi send handle must only validate and enqueue messages; found %q", bad)
		}
	}

	if !strings.Contains(out, `callCtx = metadata.AppendToOutgoingContext(callCtx, "authorization", "Bearer "+token)`) {
		t.Errorf("bidi stream must authenticate at stream open")
	}
}

func TestGenerateGoBidiMethodUsesServiceQualifiedHandle(t *testing.T) {
	svc := &commonparser.ServiceInfo{Name: "ContractApi"}
	method := &commonparser.MethodInfo{Name: "Chat"}
	out, err := generateGoBidiMethod(svc, method, "types.ChatIn", "types.ChatOut", true, "types.ValidateChatIn", "types.ValidateChatOut")
	if err != nil {
		t.Fatalf("generateGoBidiMethod: %v", err)
	}
	if !strings.Contains(out, "type ContractApiChatBidiHandle struct") {
		t.Fatalf("bidi handle should include service name to avoid same-package collisions:\n%s", out)
	}
	if strings.Contains(out, "type ChatBidiHandle struct") {
		t.Fatalf("bidi handle should not be method-only:\n%s", out)
	}
}

// TestOuterGrpcClientTemplateParses verifies the outer grpcclient.go.tmpl
// is well-formed Go template syntax and produces a parseable Go file with
// the expected imports when HasBidi=true.
func TestOuterGrpcClientTemplateParses(t *testing.T) {
	tmplStr, err := loadTemplate("grpcclient.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	tmpl, err := template.New("grpc").Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	data := struct {
		GrpcPackageName     string
		TypesModulePath     string
		CrossPackageImports string
		ServiceName         string
		ClientName          string
		GRPCMethods         string
		HasMethods          bool
		HasValidation       bool
		HasBidi             bool
		HasNonBidi          bool
		HasClientStreaming  bool
		HasAuth             bool
		HasCollectionStream bool
		HasCollectionCursor bool
		HasPage             bool
	}{
		GrpcPackageName:     "mysvc",
		TypesModulePath:     "example.com/types/mysvc",
		ServiceName:         "MyService",
		ClientName:          "MyServiceClient",
		GRPCMethods:         "// methods would go here\n",
		HasMethods:          true,
		HasValidation:       false,
		HasBidi:             true,
		HasNonBidi:          false,
		HasClientStreaming:  false,
		HasAuth:             true,
		HasCollectionStream: true,
		HasCollectionCursor: true,
	}
	var sb strings.Builder
	if err := tmpl.Execute(&sb, data); err != nil {
		t.Fatalf("execute: %v", err)
	}
	src := sb.String()

	// Validate the package declaration parses (imports won't all resolve in
	// this test environment, but the file should at least be syntactically
	// valid Go up through the imports block).
	fset := token.NewFileSet()
	if _, err := parser.ParseFile(fset, "grpcclient.go", src, parser.ImportsOnly); err != nil {
		t.Errorf("rendered outer template not parseable: %v\n%s", err, src)
	}

	// In bidi-only mode, codes/status should be absent; zap should be present.
	for _, banned := range []string{"google.golang.org/grpc/codes", "google.golang.org/grpc/status"} {
		if strings.Contains(src, banned) {
			t.Errorf("bidi-only client should not import %q (would be unused)", banned)
		}
	}
	for _, required := range []string{"go.uber.org/zap", "google.golang.org/grpc/metadata"} {
		if !strings.Contains(src, required) {
			t.Errorf("bidi-only client should import %q", required)
		}
	}
}
