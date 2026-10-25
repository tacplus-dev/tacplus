// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// tsMintTenantNamespaceDriver drives the rendered MintTokenProvider against a
// fake minter that records every TokenMintRequest and stamps the requested
// tenant namespace into the minted access token's claims.
const tsMintTenantNamespaceDriver = `import assert from 'node:assert/strict';
import { MintTokenProvider } from './w2sm/index.ts';

const store = new Map<string, string>([['w2sm.refresh_token', 'RT']]);
const storage = {
  getItem: (k: string) => store.get(k) ?? null,
  setItem: (k: string, v: string) => { store.set(k, v); },
  removeItem: (k: string) => { store.delete(k); },
};
const exp = String(Math.floor(Date.now() / 1000) + 3600);
const requests: any[] = [];
let gate: Promise<void> | undefined;
let n = 0;
const mint = {
  async mintToken(req: any) {
    requests.push(req);
    if (gate) await gate;
    n++;
    const ns = req.tenantNamespace ?? '';
    return {
      accessToken: { base64: 'AT' + n, payload: { exp, ns } },
      serviceTokens: req.serviceAuds.map((aud: string) => ({ base64: 'ST' + n + ':' + aud, payload: { exp, ns } })),
    };
  },
};
const p = new MintTokenProvider(mint, storage);

// Before setTenantNamespace: a plain personal exchange.
assert.equal(await p.getTokenFor(''), 'AT1');
assert.equal(requests[0].inputToken, 'RT');
assert.equal(requests[0].tenantNamespace ?? '', '', 'personal exchange must not carry tenant_namespace');
assert.equal(p.getAccessToken(), 'AT1');
assert.equal(p.getAccessClaims()?.ns, '');
await p.getTokenFor('svc');
assert.equal(requests[1].inputToken, 'AT1');
assert.equal(requests[1].tenantNamespace ?? '', '');

// Switching drops the cached access and service tokens.
p.setTenantNamespace('/tenants/acme');
assert.equal(p.getAccessToken(), undefined, 'switch must drop the cached access token');
assert.equal(p.getAccessClaims(), undefined);
assert.equal(await p.getTokenFor('svc'), 'ST3:svc', 'switch must drop cached service tokens');
assert.equal(requests.length, 3);
assert.equal(requests[2].inputToken, 'RT', 'after a switch the refresh token mints a fresh access token');
assert.equal(requests[2].tenantNamespace, '/tenants/acme');
assert.deepEqual(requests[2].serviceAuds, ['svc']);
assert.equal(p.getAccessToken(), 'AT3');
assert.equal(p.getAccessClaims()?.ns, '/tenants/acme');
assert.equal(await p.getTokenFor('svc'), 'ST3:svc');
assert.equal(requests.length, 3, 'tokens minted for the namespace stay cached');
await p.getTokenFor('other');
assert.equal(requests[3].inputToken, 'AT3');
assert.equal(requests[3].tenantNamespace ?? '', '', 'access -> service mints carry no tenant_namespace');

// Setting the same namespace again keeps the cache.
p.setTenantNamespace('/tenants/acme');
assert.equal(p.getAccessToken(), 'AT3');

// null returns to the plain personal exchange.
p.setTenantNamespace(null);
assert.equal(p.getAccessToken(), undefined);
assert.equal(await p.getTokenFor(''), 'AT5');
assert.equal(requests[4].inputToken, 'RT');
assert.equal(requests[4].tenantNamespace ?? '', '');
assert.equal(p.getAccessClaims()?.ns, '');

// A mint in flight across a switch must not repopulate the cache.
let release!: () => void;
gate = new Promise<void>((r) => { release = r; });
const inflight = p.getTokenFor('svc2');
await Promise.resolve();
p.setTenantNamespace('/tenants/beta');
release();
await inflight;
gate = undefined;
assert.equal(p.getAccessToken(), undefined, 'a pre-switch mint must not be cached after the switch');
await p.getTokenFor('svc2');
const last = requests[requests.length - 1];
assert.equal(last.inputToken, 'RT');
assert.equal(last.tenantNamespace, '/tenants/beta');
assert.equal(p.getAccessClaims()?.ns, '/tenants/beta');
console.log('ok');
`

// TestTypeScriptW2SMintTokenProviderTenantNamespace renders the real w2sm
// template and drives MintTokenProvider under node: setTenantNamespace drops
// the cached access and service tokens and the next refresh -> access mint
// carries tenant_namespace (none before it, none after null), and
// getAccessToken/getAccessClaims expose the namespace the app is in (#139).
func TestTypeScriptW2SMintTokenProviderTenantNamespace(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; skipping MintTokenProvider behaviour check")
	}
	dir := t.TempDir()
	if err := generateW2SServiceManager(&tactype.Config{OutputDir: dir}, "mintToken"); err != nil {
		t.Fatalf("generateW2SServiceManager: %v", err)
	}
	stubs := map[string]string{
		"package.json": `{"type": "module"}`,
		"node_modules/@bufbuild/protobuf/package.json":   `{"name": "@bufbuild/protobuf", "type": "module", "main": "index.js"}`,
		"node_modules/@bufbuild/protobuf/index.js":       "export function create(_schema, init) { return init; }\n",
		"types/sys/tokenrequest/index.js":                "export const TokenMintRequestSchema = {};\n",
		"types/sys/token/index.js":                       "export const TokenPurpose = { ACCESS: 1 };\n",
		"w2sm/internal/grpc_over_rest/grpc_over_rest.js": "export class W2SClients {}\n",
		"driver.ts": tsMintTenantNamespaceDriver,
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
