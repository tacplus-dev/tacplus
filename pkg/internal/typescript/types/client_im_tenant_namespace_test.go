// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/tmplutil"
)

// tsClientIMTenantNamespaceDriver drives the rendered ClientIdentityManager
// over a fake grpc client that records every TokenMintRequest it is handed.
const tsClientIMTenantNamespaceDriver = `import assert from 'node:assert/strict';
import { ClientIdentityManager } from './chassis/client_im/ClientIdentityManager.ts';

const exp = String(Math.floor(Date.now() / 1000) + 3600);
const requests: any[] = [];
let gate: Promise<void> | undefined;
let n = 0;
const client: any = {
  makeUnaryRequest(_method: string, _ser: any, _deser: any, req: any, _md: any, _opts: any, cb: any) {
    requests.push(req);
    Promise.resolve(gate).then(() => {
      n++;
      cb(null, {
        accessToken: { base64: 'AT' + n, payload: { exp } },
        serviceTokens: req.serviceAuds.map((aud: string) => ({ base64: 'ST' + n + ':' + aud, payload: { exp } })),
      });
    });
  },
  close() {},
};
const source: any = { supportsAutoAcquire: () => false, acquire: async () => { throw new Error('unused'); } };
const m = ClientIdentityManager.withClient(client, source);
m.setRefreshToken('RT');

// Before setTenantNamespace: a plain personal exchange.
assert.equal(m.getAccessToken(), undefined);
assert.equal(await m.getTokenFor(''), 'AT1');
assert.equal(requests[0].inputToken, 'RT');
assert.equal(requests[0].tenantNamespace ?? '', '', 'personal exchange must not carry tenant_namespace');
assert.equal(m.getAccessToken(), 'AT1');
await m.getTokenFor('svc');
assert.equal(requests[1].inputToken, 'AT1');

// Switching drops the cached access and service tokens.
m.setTenantNamespace('/tenants/acme');
assert.equal(m.getAccessToken(), undefined, 'switch must drop the cached access token');
assert.equal(await m.getTokenFor('svc'), 'ST3:svc', 'switch must drop cached service tokens');
assert.equal(requests[2].inputToken, 'RT');
assert.equal(requests[2].tenantNamespace, '/tenants/acme');
assert.equal(m.getAccessToken(), 'AT3');
await m.getTokenFor('other');
assert.equal(requests[3].inputToken, 'AT3');
assert.equal(requests[3].tenantNamespace ?? '', '', 'access -> service mints carry no tenant_namespace');

// null returns to the plain personal exchange.
m.setTenantNamespace(null);
assert.equal(m.getAccessToken(), undefined);
assert.equal(await m.getTokenFor(''), 'AT5');
assert.equal(requests[4].tenantNamespace ?? '', '');

// A mint in flight across a switch must not repopulate the cache.
let release!: () => void;
gate = new Promise<void>((r) => { release = r; });
const inflight = m.getTokenFor('svc2');
await Promise.resolve();
m.setTenantNamespace('/tenants/beta');
release();
await inflight;
gate = undefined;
await m.getTokenFor('svc2');
const last = requests[requests.length - 1];
assert.equal(last.inputToken, 'RT', 'a pre-switch service token must not be cached after the switch');
assert.equal(last.tenantNamespace, '/tenants/beta');
console.log('ok');
`

// TestTypeScriptClientIdentityManagerTenantNamespace renders the real TS
// ClientIdentityManager and drives it under node: setTenantNamespace drops the
// cached tokens and the next refresh -> access mint carries tenant_namespace,
// and getAccessToken reads the cached access token without minting (#139).
func TestTypeScriptClientIdentityManagerTenantNamespace(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; skipping ClientIdentityManager behaviour check")
	}
	tmplStr, err := tmplutil.LoadTemplate("typescript/chassis/client_im", "ClientIdentityManager.ts.tmpl")
	if err != nil {
		t.Fatalf("load ClientIdentityManager.ts.tmpl: %v", err)
	}
	tmpl, err := template.New("client_im").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse ClientIdentityManager template: %v", err)
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, map[string]any{
		"HasClientTokenExchanger":     true,
		"TokenRequestPackage":         "../../types/sys/tokenrequest/index.js",
		"ClientTokenExchangerPackage": "gateway",
		"ClientMintServiceName":       "Mint",
		"ClientTokenMintMethod":       "MintToken",
	}); err != nil {
		t.Fatalf("execute ClientIdentityManager template: %v", err)
	}

	dir := t.TempDir()
	stubs := map[string]string{
		"package.json": `{"type": "module"}`,
		"node_modules/@bufbuild/protobuf/package.json": `{"name": "@bufbuild/protobuf", "type": "module", "main": "index.js"}`,
		"node_modules/@bufbuild/protobuf/index.js":     "export function create(_s, init) { return init; }\nexport function toBinary() { return new Uint8Array(); }\nexport function fromBinary() { return {}; }\n",
		"node_modules/@grpc/grpc-js/package.json":      `{"name": "@grpc/grpc-js", "type": "module", "main": "index.js"}`,
		"node_modules/@grpc/grpc-js/index.js":          "export const status = { UNAUTHENTICATED: 16 };\nexport class Metadata {}\nexport class Client {}\nexport const credentials = {};\n",
		"types/sys/grpcconfig/index.js":                "export {};\n",
		"types/sys/tokenrequest/index.js":              "export const TokenMintRequestSchema = {};\nexport const TokenMintResponseSchema = {};\n",
		"chassis/client_im/TokenSource.js":             "export {};\n",
		"chassis/client_im/ClientIdentityManager.ts":   rendered.String(),
		"driver.ts": tsClientIMTenantNamespaceDriver,
	}
	for name, content := range stubs {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, filepath.Join(dir, "driver.ts"))
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("driver did not finish within 30s")
	}
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("driver failed: %v\n%s", err, out)
	}
}
