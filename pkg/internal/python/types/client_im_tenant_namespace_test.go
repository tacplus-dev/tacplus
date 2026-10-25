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

// pyClientIMTenantNamespaceDriver drives the rendered ClientIdentityManager
// over a stub mint that records every TokenMintRequest it is handed.
const pyClientIMTenantNamespaceDriver = `import asyncio, sys, time
sys.path.insert(0, sys.argv[1])
import stubmint
from cim.manager import ClientIdentityManager


async def main():
    m = ClientIdentityManager.new_with_channel(object(), None)
    await m.set_refresh_token("RT")
    reqs = stubmint.REQUESTS

    # Before set_tenant_namespace: a plain personal exchange.
    assert m.access_token() is None
    assert await m.get_token_for("") == "AT1"
    assert reqs[0].input_token == "RT" and reqs[0].tenant_namespace == "", vars(reqs[0])
    assert m.access_token() == "AT1"
    await m.get_token_for("svc")
    assert reqs[1].input_token == "AT1"

    # Switching drops the cached access and service tokens.
    await m.set_tenant_namespace("/tenants/acme")
    assert m.access_token() is None, "switch must drop the cached access token"
    assert await m.get_token_for("svc") == "ST3:svc", "switch must drop cached service tokens"
    assert reqs[2].input_token == "RT" and reqs[2].tenant_namespace == "/tenants/acme", vars(reqs[2])
    assert m.access_token() == "AT3"
    await m.get_token_for("other")
    assert reqs[3].input_token == "AT3" and reqs[3].tenant_namespace == "", "access -> service mints carry no tenant_namespace"

    # None returns to the plain personal exchange.
    await m.set_tenant_namespace(None)
    assert m.access_token() is None
    assert await m.get_token_for("") == "AT5"
    assert reqs[4].input_token == "RT" and reqs[4].tenant_namespace == ""

    # A mint in flight across a switch must not repopulate the cache.
    stubmint.GATE = asyncio.Event()
    inflight = asyncio.create_task(m.get_token_for("svc2"))
    await asyncio.sleep(0)
    await m.set_tenant_namespace("/tenants/beta")
    stubmint.GATE.set()
    await inflight
    stubmint.GATE = None
    await m.get_token_for("svc2")
    assert reqs[-1].input_token == "RT", "a pre-switch service token must not be cached after the switch"
    assert reqs[-1].tenant_namespace == "/tenants/beta"
    print("ok")


asyncio.run(main())
`

// TestPythonClientIdentityManagerTenantNamespace renders the real Python
// ClientIdentityManager and drives it: set_tenant_namespace drops the cached
// tokens and the next refresh -> access mint carries tenant_namespace, and
// access_token() reads the cached access token without minting (#139).
func TestPythonClientIdentityManagerTenantNamespace(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3 on PATH; skipping ClientIdentityManager behaviour check")
	}
	tmplStr, err := tmplutil.LoadTemplate("python/chassis/client_im", "manager.py.tmpl")
	if err != nil {
		t.Fatalf("load manager.py.tmpl: %v", err)
	}
	tmpl, err := template.New("client_im").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse client_im manager template: %v", err)
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, map[string]any{
		"HasClientTokenExchanger": true,
		"PythonPackageBase":       "stubbase",
		"ClientMintGrpcModule":    "stubmint",
		"ClientMintServiceName":   "Mint",
		"ClientTokenMintMethod":   "MintToken",
	}); err != nil {
		t.Fatalf("execute client_im manager template: %v", err)
	}

	dir := t.TempDir()
	stubs := map[string]string{
		"grpc/__init__.py": "from . import aio\n\n\nclass StatusCode:\n    UNAUTHENTICATED = 16\n",
		"grpc/aio.py":      "class AioRpcError(Exception):\n    pass\n",
		"stubbase/types/sys/tokenrequest/tokenrequest_pb2.py": "class TokenMintRequest:\n" +
			"    def __init__(self, **kwargs):\n" +
			"        self.tenant_namespace = ''\n" +
			"        self.__dict__.update(kwargs)\n",
		"stubbase/types/sys/token/token_pb2.py": "TOKEN_PURPOSE_ACCESS = 2\n",
		"stubmint.py": `import time
from types import SimpleNamespace

REQUESTS = []
GATE = None
_n = 0


def _tok(b):
    return SimpleNamespace(base64=b, payload={"exp": str(int(time.time()) + 3600)})


class MintStub:
    def __init__(self, channel):
        pass

    async def MintToken(self, req, timeout=None):
        global _n
        REQUESTS.append(req)
        if GATE is not None:
            await GATE.wait()
        _n += 1
        resp = SimpleNamespace(access_token=_tok(f"AT{_n}"),
                               service_tokens=[_tok(f"ST{_n}:{a}") for a in req.service_auds])
        resp.HasField = lambda name: True
        return resp
`,
		"cim/__init__.py": "",
		"cim/source.py":   "class TokenSource:\n    pass\n",
		"cim/manager.py":  rendered.String(),
		"driver.py":       pyClientIMTenantNamespaceDriver,
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
	out, err := exec.CommandContext(ctx, py, "-I", filepath.Join(dir, "driver.py"), dir).CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("driver did not finish within 30s")
	}
	if err != nil || strings.TrimSpace(string(out)) != "ok" {
		t.Fatalf("driver failed: %v\n%s", err, out)
	}
}
