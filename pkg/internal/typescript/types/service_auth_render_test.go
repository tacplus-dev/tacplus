// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
	"gitlab.com/secondstarcavalier/schematic/tacplus/templates"
)

func TestTypeScriptServiceWrapperAuthWiringRendersMetadataAndMethodPolicies(t *testing.T) {
	outputDir := t.TempDir()
	cfg := &tactype.Config{
		OutputDir:     outputDir,
		TsPackageBase: "demo-tacplus",
	}
	svc := ServiceInfo{
		ProtoFile:    "services/demo.proto",
		PackageName:  "demo",
		ServiceName:  "DemoService",
		GrpcName:     "demo",
		IsGrpc:       true,
		OutputDir:    outputDir,
		ParentDir:    "services",
		Config:       cfg,
		AuthServices: []string{"identity", "opa"},
		AuthAnyUser:  true,
	}
	info := &parser.ProtoInfo{
		Package: "demo",
		Services: []*parser.ServiceInfo{{
			Name: "DemoService",
			Methods: []*parser.MethodInfo{
				{
					Name:                 "Read",
					InputType:            "ReadRequest",
					OutputType:           "ReadResponse",
					FullInputType:        "demo.ReadRequest",
					FullOutputType:       "demo.ReadResponse",
					TargetNamespaceField: "tenant_namespace",
				},
				{
					Name:            "Tail",
					InputType:       "TailRequest",
					OutputType:      "TailEvent",
					FullInputType:   "demo.TailRequest",
					FullOutputType:  "demo.TailEvent",
					ServerStreaming: true,
				},
				{
					Name:            "Upload",
					InputType:       "UploadChunk",
					OutputType:      "UploadResponse",
					FullInputType:   "demo.UploadChunk",
					FullOutputType:  "demo.UploadResponse",
					ClientStreaming: true,
				},
				{
					Name:            "Chat",
					InputType:       "ChatMessage",
					OutputType:      "ChatMessage",
					FullInputType:   "demo.ChatMessage",
					FullOutputType:  "demo.ChatMessage",
					ClientStreaming: true,
					ServerStreaming: true,
				},
				{
					Name:           "PingAny",
					InputType:      "PingRequest",
					OutputType:     "PingResponse",
					FullInputType:  "demo.PingRequest",
					FullOutputType: "demo.PingResponse",
					AuthAnyUser:    true,
				},
				{
					Name:           "Health",
					InputType:      "HealthRequest",
					OutputType:     "HealthResponse",
					FullInputType:  "demo.HealthRequest",
					FullOutputType: "demo.HealthResponse",
					AuthDisabled:   true,
				},
				{
					Name:           "Override",
					InputType:      "OverrideRequest",
					OutputType:     "OverrideResponse",
					FullInputType:  "demo.OverrideRequest",
					FullOutputType: "demo.OverrideResponse",
					AuthServices:   []string{"eventbus"},
				},
			},
		}},
	}

	if _, err := generateServiceWrapper(svc, info, nil, map[string]service.AccountingInfo{}); err != nil {
		t.Fatalf("generateServiceWrapper: %v", err)
	}
	rendered := readOnlyFileInDir(t, outputDir)

	for _, want := range []string{
		"private authorizeFunc: ((fullMethod: string, metadata: Record<string, string>, req: unknown) => void | Promise<void>) | null;",
		"private authenticateFunc: ((metadata: Record<string, string>, targetNamespace?: string) => void | Promise<void>) | null;",
		"await this.authorizeFunc(fullMethod, this.metadataToRecord(metadata), req);",
		"await this.authenticateFunc(this.metadataToRecord(metadata), targetNamespace);",
		"authorizer.setMethodAuth(\"Read\", [\"identity\", \"opa\"]);",
		"authorizer.setMethodTargetNamespaceField(\"Read\", \"tenant_namespace\");",
		"authorizer.setMethodAuth(\"Override\", [\"eventbus\"]);",
		"await this.authorize('/demo.DemoService/Read', call.metadata, call.request);",
		"await this.authorize('/demo.DemoService/Tail', call.metadata, call.request);",
		"await this.authorize('/demo.DemoService/Upload', call.metadata, null);",
		"await this.authorize('/demo.DemoService/Chat', stream.metadata, null);",
		"await this.authenticate(call.metadata, \"/\");",
		"call.emit('error', this.toGrpcError(err));",
		"stream.emit('error', this.toGrpcError(err));",
		"stream.emit('error', this.toGrpcErrorWithCode(err, grpc.status.INTERNAL));",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered TypeScript service wrapper missing %q:\n%s", want, rendered)
		}
	}

	for _, notWant := range []string{
		"authorize('/demo.DemoService/Health'",
		"authenticate('/demo.DemoService/Health'",
		"authorizer.setMethodAuth(\"PingAny\"",
		"authorizer.setMethodAuth(\"Health\"",
		"call.destroy(this.toGrpcError(err));",
		"stream.destroy(this.toGrpcError(err));",
		"stream.destroy(this.toGrpcErrorWithCode(err, grpc.status.INTERNAL));",
	} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("auth-disabled method rendered auth wiring %q:\n%s", notWant, rendered)
		}
	}
}

func TestTypeScriptServiceManagerInstallsGrpcAuthorizerCallbacks(t *testing.T) {
	outputDir := t.TempDir()
	inputDir := t.TempDir()
	cfg := &tactype.Config{
		OutputDir:     outputDir,
		InputDir:      inputDir,
		TsPackageBase: "demo-tacplus",
		SelfPackage:   "demo",
	}
	svcs := []ServiceInfo{{
		ProtoFile:    "services/demo.proto",
		PackageName:  "demo",
		ServiceName:  "DemoService",
		GrpcName:     "demo",
		IsGrpc:       true,
		OutputDir:    filepath.Join(outputDir, "s2sm", "internal", "grpc", "servers", "services", "demo"),
		ParentDir:    "services",
		Config:       cfg,
		AuthServices: []string{"identity"},
	}}

	if err := generateTsServiceManager(cfg, svcs, nil); err != nil {
		t.Fatalf("generateTsServiceManager: %v", err)
	}
	renderedBytes, err := os.ReadFile(filepath.Join(outputDir, "s2sm", "index.ts"))
	if err != nil {
		t.Fatalf("read generated service manager: %v", err)
	}
	rendered := string(renderedBytes)

	for _, want := range []string{
		"import { Authorizer } from '../chassis/grpcserver/index.js';",
		"const authorizer = new Authorizer(this.stateStoreConn.conn());",
		"const wrapperDemo = new wrapperDemoClass(handler.demoGrpcImpl());",
		"wrapperDemo.registerMethodAuth(authorizer);",
		"wrapperDemo.setAuthorizeFunc((fullMethod, metadata, req) => authorizer.authorize(metadata, fullMethod, req));",
		"wrapperDemo.setAuthenticateFunc((metadata, targetNamespace) => authorizer.authenticate(metadata, targetNamespace));",
		"wrapperDemo.registerServices(server);",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered TypeScript service manager missing %q:\n%s", want, rendered)
		}
	}
}

func TestTypeScriptGrpcAuthorizerJwkWatchUsesJwkViewStreamWithBoundedReconnect(t *testing.T) {
	renderedBytes, err := fs.ReadFile(templates.FS, "typescript/chassis/grpcserver/Authorizer.ts.tmpl")
	if err != nil {
		t.Fatalf("load Authorizer.ts.tmpl: %v", err)
	}
	rendered := string(renderedBytes)

	for _, want := range []string{
		"client.makeServerStreamRequest(",
		"'/jwkview.JwkVerifyingKeysViewService/Watch'",
		"fromBinary(JwkViewResponseSchema, new Uint8Array(buf))",
		"export class AuthError extends Error {",
		"static unauthenticated(message: string): AuthError {",
		"static permissionDenied(message: string): AuthError {",
		"throw AuthError.unauthenticated('unknown signing key: ' + kid);",
		"throw AuthError.permissionDenied(`caller namespace \"${callerNamespace}\" is not at-or-above target \"${target}\"`);",
		"private _methodTargetNamespaceFields: Map<string, string>;",
		"setMethodTargetNamespaceField(methodName: string, fieldName: string): void {",
		"async authorize(metadata: Record<string, string>, fullMethod: string, request?: unknown): Promise<void> {",
		"this.checkTargetNamespace(methodName, tw.getNamespace(), request);",
		"function readStringField(request: Record<string, unknown>, protoFieldName: string): unknown {",
		"function isAtOrBelow(caller: string, target: string): boolean {",
		"private readonly maxReconnectBackoffMs = 30000;",
		"this.currentStream?.cancel();",
		"this.reconnectTimer = setTimeout(() => {",
		"this.reconnectAttempt = 0;",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("TypeScript Authorizer template missing %q:\n%s", want, rendered)
		}
	}
}

// SEC-3: a refresh token carrying typ:refresh but no token_use claim must be
// rejected on BOTH the authorize and authenticate paths. The typ fallback must
// appear twice — one path having it while the other fails open is the hole.
func TestTypeScriptAuthorizerRejectsTypRefreshOnBothPaths(t *testing.T) {
	renderedBytes, err := fs.ReadFile(templates.FS, "typescript/chassis/grpcserver/Authorizer.ts.tmpl")
	if err != nil {
		t.Fatalf("load Authorizer.ts.tmpl: %v", err)
	}
	rendered := string(renderedBytes)
	if n := strings.Count(rendered, "const typ = tw.getCustomField('typ');"); n < 2 {
		t.Fatalf("TypeScript Authorizer must reject typ:refresh on both authorize and authenticate; found %d typ fallbacks, want >= 2", n)
	}
}

func TestTypeScriptAuthorizerNormalizesGeneratedGrantTokens(t *testing.T) {
	renderedBytes, err := fs.ReadFile(templates.FS, "typescript/chassis/grpcserver/Authorizer.ts.tmpl")
	if err != nil {
		t.Fatalf("load Authorizer.ts.tmpl: %v", err)
	}
	rendered := string(renderedBytes)
	for _, want := range []string{
		"import { normalize as normalizeGrants } from '../../grants/grants.js';",
		"return normalizeGrants(raw).includes(required);",
		"export function methodGrantToken(fallbackService: string, fullMethod: string): string",
		"const serviceSlash = serviceFqn.lastIndexOf('/');",
		"function grantTokenPart(value: string): string",
		".replace(/[-./]/g, '_')",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("TypeScript Authorizer must authorize via generated grant normalization; missing %q", want)
		}
	}
}

func TestTypeScriptGrpcServerIndexExportsAuthError(t *testing.T) {
	renderedBytes, err := fs.ReadFile(templates.FS, "typescript/chassis/grpcserver/index.ts.tmpl")
	if err != nil {
		t.Fatalf("load grpcserver/index.ts.tmpl: %v", err)
	}
	rendered := string(renderedBytes)
	if !strings.Contains(rendered, "export { AuthError, Authorizer } from './Authorizer.js';") {
		t.Fatalf("TypeScript grpcserver index does not export AuthError:\n%s", rendered)
	}
}

func TestTypeScriptGrpcServerPackageIsRunnableEsm(t *testing.T) {
	renderedBytes, err := fs.ReadFile(templates.FS, "typescript/chassis/grpcserver/package.json.tmpl")
	if err != nil {
		t.Fatalf("load grpcserver/package.json.tmpl: %v", err)
	}
	rendered := string(renderedBytes)
	for _, want := range []string{
		`"main": "dist/index.js"`,
		`"types": "dist/index.d.ts"`,
		`"type": "module"`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("TypeScript grpcserver package template missing %q:\n%s", want, rendered)
		}
	}
}

func tsTestMethod(name, inputType, outputType string) *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:           name,
		InputType:      inputType,
		OutputType:     outputType,
		FullInputType:  "demo." + inputType,
		FullOutputType: "demo." + outputType,
	}
}

func readOnlyFileInDir(t *testing.T, dir string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %s: %v", dir, err)
	}
	var files []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".ts") {
			files = append(files, filepath.Join(dir, entry.Name()))
		}
	}
	if len(files) != 1 {
		t.Fatalf("expected one generated .ts file in %s, got %v", dir, files)
	}
	b, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read generated file %s: %v", files[0], err)
	}
	return string(b)
}

// An auth_services pattern containing alternation must still match the WHOLE
// caller identity. Compiling it as `^${p}$` is wrong: `|` binds looser than the
// anchors, so "billing|payments" becomes "^billing|payments$" — "starts with
// billing, OR ends with payments" — admitting callers like "evil-payments".
// TypeScript has no generated-code test runner (the cross-test gates only tsc-
// build the TS output), so this lifts the pattern-compilation expression
// verbatim out of the template and evaluates it under the real ECMAScript
// engine (node), which is what actually decides the semantics at runtime.
func TestTypeScriptAuthorizerAnchorsWholeAuthServicesPattern(t *testing.T) {
	renderedBytes, err := fs.ReadFile(templates.FS, "typescript/chassis/grpcserver/Authorizer.ts.tmpl")
	if err != nil {
		t.Fatalf("load Authorizer.ts.tmpl: %v", err)
	}
	rendered := string(renderedBytes)

	// Lift setMethodAuth's pattern-compilation expression: "patterns.map(" up to
	// the end of the enclosing statement.
	const exprStart = "patterns.map("
	i := strings.Index(rendered, exprStart)
	if i < 0 {
		t.Fatalf("Authorizer.ts.tmpl no longer compiles auth_services patterns via %q", exprStart)
	}
	j := strings.Index(rendered[i:], "),\n")
	if j < 0 {
		t.Fatalf("could not find the end of setMethodAuth's pattern-compilation expression")
	}
	compileExpr := strings.Join(strings.Fields(rendered[i:i+j+1]), " ")

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node binary found; skipping ECMAScript semantics check")
	}
	script := "const patterns = ['billing|payments'];\n" +
		"const compiled = " + compileExpr + ";\n" +
		"const match = (c) => compiled.some((re) => re.test(c));\n" +
		"for (const c of ['billing', 'payments']) { if (!match(c)) { console.log('exact match denied: ' + c); process.exit(1); } }\n" +
		"for (const c of ['evil-payments', 'billing-but-not-really']) { if (match(c)) { console.log('partial match accepted: ' + c); process.exit(1); } }\n"
	if out, err := exec.Command(node, "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("auth_services pattern %s is not fully anchored under ECMAScript: %v\n%s", compileExpr, err, out)
	}
}
