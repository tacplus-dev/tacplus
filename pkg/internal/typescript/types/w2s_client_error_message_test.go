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

const tsW2SErrorMessageDriver = `
import assert from 'node:assert/strict';
import { FetchTransport, W2SError } from './w2sm/index.ts';

let answer;
globalThis.fetch = async () => answer();

async function failure(status, statusText, body, headers = {}) {
  answer = () => new Response(body, { status, statusText, headers });
  const t = new FetchTransport('http://gw.test');
  try {
    await t.requestJson('POST', '/x', {}, { auth: false });
  } catch (e) {
    assert.ok(e instanceof W2SError, 'requestJson must throw W2SError');
    return e;
  }
  assert.fail('requestJson did not throw');
}

// The gateway's answer to kernel's DeleteAccount FAILED_PRECONDITION: the
// person reads the refusal, not the HTTP status line.
const refusal = 'deleting this account removes its 2 sign-in(s) and 1 tenant membership(s) and signs out every session; it cannot be undone — confirm to proceed';
let e = await failure(500, 'Internal Server Error', JSON.stringify({ error: refusal, code: 9 }));
assert.equal(e.code, 9);
assert.equal(e.message, refusal);
assert.equal(e.status, 500);

// Another front's 'message' field serves when 'error' is absent; 'error' wins
// when both are there.
e = await failure(403, 'Forbidden', JSON.stringify({ message: 'no live proof vouches for it' }));
assert.equal(e.code, 7);
assert.equal(e.message, 'no live proof vouches for it');
e = await failure(400, 'Bad Request', JSON.stringify({ error: 'from error', message: 'from message' }));
assert.equal(e.message, 'from error');

// The gateway decoded grpc-message already: a literal '%' is not decoded again.
e = await failure(500, 'Internal Server Error', JSON.stringify({ error: '100%25 sure', code: 9 }));
assert.equal(e.message, '100%25 sure');

// No usable message: today's status line.
const fallback = 'gRPC-over-REST error: 502 Bad Gateway';
for (const body of ['<html>bad gateway</html>', '', 'null', '[]', '{"error":""}', '{"error":{"x":1}}', '{"error":42,"message":""}']) {
  e = await failure(502, 'Bad Gateway', body);
  assert.equal(e.message, fallback, 'body ' + body);
  assert.equal(e.code, 2);
}
console.log('ok');
`

// TestTypeScriptW2SErrorCarriesBodyMessage renders the real w2sm template and
// drives FetchTransport.requestJson under node with a stubbed fetch. The
// gateway answers a backend's gRPC error as {"error": <message>, "code": <code>},
// and the TS client dropped the message for "gRPC-over-REST error: 500 ...",
// unlike the Go, Python, Rust and Java W2S clients (#953). W2SError.message is
// now the body's non-empty `error`, else its non-empty `message`, else the
// status line.
func TestTypeScriptW2SErrorCarriesBodyMessage(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; skipping W2SError message behaviour check")
	}
	dir := t.TempDir()
	if err := generateW2SServiceManager(&tactype.Config{OutputDir: dir}, ""); err != nil {
		t.Fatalf("generateW2SServiceManager: %v", err)
	}
	stubs := map[string]string{
		"package.json": `{"type": "module"}`,
		"w2sm/internal/grpc_over_rest/grpc_over_rest.js": "export class W2SClients {}\n",
		"driver.ts": tsW2SErrorMessageDriver,
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
