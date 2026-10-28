// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/tacplus-dev/tacplus/templates"
)

// tsAuthorizerHarness renders the real generated Authorizer.ts into a
// throw-away tree with stubbed token/jwk/spiffe/grpc seams. It returns node,
// the tree root and a writer for further files (the check script).
func tsAuthorizerHarness(t *testing.T) (string, string, func(rel, body string)) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node found; skipping behavioral check")
	}
	if out, err := exec.Command(node, "--experimental-strip-types", "-e", "0").CombinedOutput(); err != nil {
		t.Skipf("node cannot strip types (%v: %s); skipping behavioral check", err, out)
	}

	src, err := templates.FS.ReadFile("typescript/chassis/grpcserver/Authorizer.ts.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	tpl, err := template.New("Authorizer.ts").Parse(string(src))
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := tpl.Execute(&buf, map[string]any{
		"ServiceLabel":      "demo",
		"AcceptedAudiences": []string{"demo"},
	}); err != nil {
		t.Fatal(err)
	}

	root := t.TempDir()
	put := func(rel, body string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	put("package.json", `{"type":"module"}`)
	put("chassis/grpcserver/Authorizer.ts", buf.String())
	put("chassis/token/index.js", `
export class TokenWrapper {
  constructor(c) { this.c = c; }
  static parse(s) { return new TokenWrapper(JSON.parse(s)); }
  getKid() { return 'k'; }
  async verify() {}
  getTokenUse() { return ''; }
  getCustomField() { return ''; }
  getAudience() { return this.c.aud; }
  getNamespace() { return this.c.ns ?? ''; }
  getSubType() { return 'service'; }
  getAct() { return ''; }
  getSubject() { return this.c.sub; }
  servicePath() { return this.c.sub; }
}
`)
	put("chassis/jwk/index.js", "export class ImmutableJwk { static async create() { return new ImmutableJwk(); } }\n")
	put("chassis/spiffe/index.js", `
export function parseSpiffeId() { throw new Error('not a uri'); }
export function isAtOrBelow(caller, target) {
  if (caller === target) return true;
  if (caller === '/') return target.startsWith('/');
  return target.startsWith(caller + '/');
}
`)
	put("types/sys/trace/index.js", "export const TraceRequestSchema = {};\n")
	put("types/sys/jwkview/index.js", "export const JwkViewResponseSchema = {};\n")
	put("grants/grants.js", "export function normalize(x) { return x; }\n")
	put("node_modules/@grpc/grpc-js/package.json", `{"name":"@grpc/grpc-js","type":"module","main":"index.js"}`)
	put("node_modules/@grpc/grpc-js/index.js", "export const status = { UNAUTHENTICATED: 16, PERMISSION_DENIED: 7 };\n")
	put("node_modules/@bufbuild/protobuf/package.json", `{"name":"@bufbuild/protobuf","type":"module","main":"index.js"}`)
	put("node_modules/@bufbuild/protobuf/index.js", "export const create = () => ({}); export const fromBinary = () => ({}); export const toBinary = () => new Uint8Array();\n")

	return node, root, put
}

// A tenant-owned instance (own namespace /tenants/beta) must refuse another
// tenant's caller at the door, on an RPC with no target_namespace_field: a
// token's aud is the service name, which every tenant's instance accepts.
//
// TypeScript has no generated-code test runner, so this renders the REAL
// generated Authorizer.ts into a throw-away tree, stubs only the
// token/jwk/spiffe/grpc seams (no signing), and drives authorize/authenticate
// under Node's built-in type stripping.
func TestTypeScriptAuthorizerRefusesOtherTenantAtTenantOwnedDoor(t *testing.T) {
	node, root, put := tsAuthorizerHarness(t)
	put("check.mjs", `
import { Authorizer } from './chassis/grpcserver/Authorizer.ts';

const PERMISSION_DENIED = 7;
const KEY = 'demo.Demo/Do';
const METHOD = '/demo.Demo/Do';

function make(host) {
  const a = new Authorizer();
  a.jwkCache().get = () => ({});
  a.setMethodAuth(KEY, ['caller-svc']);
  if (host !== undefined) a.setHostNamespace(host);
  return a;
}
const md = (ns) => ({ authorization: 'Bearer ' + JSON.stringify({ aud: 'demo', sub: 'caller-svc', ns }) });

async function run(call) {
  try { await call(); return null; } catch (e) { return e; }
}
async function passes(a, ns) {
  for (const call of [() => a.authorize(md(ns), METHOD, undefined), () => a.authenticate(md(ns))]) {
    const e = await run(call);
    if (e) throw new Error('caller in ' + ns + ' must pass: ' + e.message);
  }
}
async function denied(a, ns) {
  for (const call of [() => a.authorize(md(ns), METHOD, undefined), () => a.authenticate(md(ns))]) {
    const e = await run(call);
    if (!e) throw new Error('caller in ' + ns + ' must be denied');
    if (e.code !== PERMISSION_DENIED) throw new Error('caller in ' + ns + ': want PERMISSION_DENIED, got ' + e.code + ' ' + e.message);
  }
}

const beta = make('/tenants/beta');
for (const ns of ['/tenants/beta', '/system', '/system/us-east', '/']) await passes(beta, ns);
for (const ns of ['/tenants/acme', '/tenants/beta2', '/tenants/beta/sub', '/users/u1']) await denied(beta, ns);

// Shared instances and an unset host namespace admit every tenant, as before.
for (const host of [undefined, '', '/system', '/system/us-east', '/tenants', '/tenants/a/b']) {
  await passes(make(host), '/tenants/acme');
}
console.log('ok');
`)

	out, err := exec.Command(node, "--experimental-strip-types", "--no-warnings", filepath.Join(root, "check.mjs")).CombinedOutput()
	if err != nil {
		t.Fatalf("tenant-owned door check is broken under TypeScript: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "ok") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

// Issue #170: RPC_FLAG_ANY_TENANT opens the tenant-owned door for that rpc
// only ("any authenticated user of any tenant"); the default keeps it ("members
// of the target tenant only"). Token, audience and auth_services still hold on
// the opened rpc, and authenticate() honours it when told the method.
func TestTypeScriptAuthorizerAnyTenantOpensTheDoorForThatRpcOnly(t *testing.T) {
	node, root, put := tsAuthorizerHarness(t)
	put("check.mjs", `
import { Authorizer } from './chassis/grpcserver/Authorizer.ts';

const a = new Authorizer();
a.jwkCache().get = () => ({});
a.setMethodAuth('demo.Demo/Do', ['caller-svc']);
a.setMethodAuth('demo.Demo/Open', ['caller-svc']);
a.setMethodAuth('other.Demo/Open', ['caller-svc']);
a.setMethodAnyTenant('demo.Demo/Open');
a.setHostNamespace('/tenants/beta');

const md = (ns, aud = 'demo', sub = 'caller-svc') => ({ authorization: 'Bearer ' + JSON.stringify({ aud, sub, ns }) });
async function code(call) {
  try { await call(); return null; } catch (e) { return e.code ?? e.message; }
}
function expect(got, want, what) {
  if (got !== want) throw new Error(what + ': want ' + want + ', got ' + got);
}

for (const ns of ['/tenants/acme', '/tenants/beta', '/users/u1', '/system']) {
  expect(await code(() => a.authorize(md(ns), '/demo.Demo/Open', undefined)), null, 'authorize Open ' + ns);
  expect(await code(() => a.authenticate(md(ns), undefined, '/demo.Demo/Open')), null, 'authenticate Open ' + ns);
}
expect(await code(() => a.authorize(md('/tenants/acme'), '/demo.Demo/Do', undefined)), 7, 'default door on Do');
expect(await code(() => a.authenticate(md('/tenants/acme'), undefined, '/demo.Demo/Do')), 7, 'default door on authenticate Do');
expect(await code(() => a.authenticate(md('/tenants/acme'))), 7, 'default door without a method');
expect(await code(() => a.authorize(md('/tenants/acme'), '/other.Demo/Open', undefined)), 7, 'same-named rpc of another service');
expect(await code(() => a.authorize(md('/tenants/acme', 'other'), '/demo.Demo/Open', undefined)), 7, 'audience still enforced');
expect(await code(() => a.authorize(md('/tenants/acme', 'demo', 'intruder'), '/demo.Demo/Open', undefined)), 7, 'auth_services still enforced');
expect(await code(() => a.authenticate(md('/tenants/acme'), '/tenants/zeta', '/demo.Demo/Open')), 7, 'target namespace still enforced');
console.log('ok');
`)
	out, err := exec.Command(node, "--experimental-strip-types", "--no-warnings", filepath.Join(root, "check.mjs")).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ok") {
		t.Fatalf("RPC_FLAG_ANY_TENANT door check is broken under TypeScript: %v\n%s", err, out)
	}
}
