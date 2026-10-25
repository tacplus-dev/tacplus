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

// A tenant-owned instance (own namespace /tenants/beta) must refuse another
// tenant's caller at the door, on an RPC with no target_namespace_field: a
// token's aud is the service name, which every tenant's instance accepts.
//
// TypeScript has no generated-code test runner, so this renders the REAL
// generated Authorizer.ts into a throw-away tree, stubs only the
// token/jwk/spiffe/grpc seams (no signing), and drives authorize/authenticate
// under Node's built-in type stripping.
func TestTypeScriptAuthorizerRefusesOtherTenantAtTenantOwnedDoor(t *testing.T) {
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
