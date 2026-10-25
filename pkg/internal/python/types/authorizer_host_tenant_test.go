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

	commonspiffe "github.com/tacplus-dev/tacplus/pkg/internal/common/spiffe"
	"github.com/tacplus-dev/tacplus/templates"
)

// A tenant-owned instance (own namespace /tenants/beta) must refuse another
// tenant's caller at the door, on an RPC with no target_namespace_field: a
// token's aud is the service name, which every tenant's instance accepts.
//
// Python has no generated-code test runner, so this renders the REAL generated
// authorizer.py and the real generated spiffe helpers into a throw-away
// package, stubs only the token/jwk/grants/exceptions seams (no signing), and
// drives Authorizer.authorize/authenticate under the system interpreter.
func TestPythonAuthorizerRefusesOtherTenantAtTenantOwnedDoor(t *testing.T) {
	py := ""
	for _, name := range []string{"python3", "python"} {
		if p, err := exec.LookPath(name); err == nil {
			py = p
			break
		}
	}
	if py == "" {
		t.Skip("no python interpreter found; skipping behavioral check")
	}

	root := t.TempDir()
	render := func(tmplPath, dest string, data any) {
		t.Helper()
		src, err := templates.FS.ReadFile(tmplPath)
		if err != nil {
			t.Fatalf("read %s: %v", tmplPath, err)
		}
		tpl, err := template.New(tmplPath).Parse(string(src))
		if err != nil {
			t.Fatalf("parse %s: %v", tmplPath, err)
		}
		var buf bytes.Buffer
		if err := tpl.Execute(&buf, data); err != nil {
			t.Fatalf("render %s: %v", tmplPath, err)
		}
		write(t, filepath.Join(root, dest), buf.String())
	}
	data := map[string]any{
		"ServiceLabel":      "demo",
		"AcceptedAudiences": []string{"demo"},
		"PythonPackageBase": "pb",
		"Spiffe":            commonspiffe.TemplateData(),
	}
	for _, d := range []string{"pb", "pb/chassis", "pb/chassis/token", "pb/chassis/jwk", "pb/types", "pb/types/sys", "pb/types/sys/errors", "pb/grants"} {
		write(t, filepath.Join(root, d, "__init__.py"), "")
	}
	render("python/chassis/spiffe/spiffe.py.tmpl", "pb/chassis/spiffe.py", data)
	render("python/chassis/grpcserver/spiffe.py.tmpl", "pb/chassis/grpcserver/spiffe.py", data)
	render("python/chassis/grpcserver/authorizer.py.tmpl", "pb/chassis/grpcserver/authorizer.py", data)
	write(t, filepath.Join(root, "pb/chassis/grpcserver/__init__.py"), "")
	write(t, filepath.Join(root, "pb/chassis/token/__init__.py"), `
import json
class TokenWrapper:
    """Stub: a token string is a JSON claim set; signature checks are the stub's no-ops."""
    def __init__(self, c): self.c = c
    @classmethod
    def parse(cls, s): return cls(json.loads(s))
    def get_kid(self): return "k"
    def verify(self, jwk): pass
    def get_token_use(self): return ""
    def get_custom_field(self, n): return ""
    def get_audience(self): return self.c["aud"]
    def get_namespace(self): return self.c.get("ns", "")
    def get_sub_type(self): return "service"
    def service_path(self): return self.c["sub"]
    def get_act(self): return ""
`)
	write(t, filepath.Join(root, "pb/chassis/jwk/__init__.py"), "class ImmutableJwk: pass\n")
	write(t, filepath.Join(root, "pb/grants/__init__.py"), "def normalize(x): return x\n")
	write(t, filepath.Join(root, "pb/types/sys/errors/exceptions.py"), `
class ProtoError(Exception):
    def __init__(self, code, message):
        super().__init__(message); self.code = code
def permission_denied(m): return ProtoError("PERMISSION_DENIED", m)
def unauthenticated(m): return ProtoError("UNAUTHENTICATED", m)
`)

	script := `
import json, sys
sys.path.insert(0, sys.argv[1])
from pb.chassis.grpcserver.authorizer import Authorizer
from pb.types.sys.errors.exceptions import ProtoError

class Jwks:
    def get(self, kid): return object()
    def close(self): pass

def make(host):
    a = Authorizer()
    a._jwk_cache = Jwks()
    a.set_method_auth("demo.Demo/Do", ["caller-svc"])
    if host is not None:
        a.set_host_namespace(host)
    return a

def md(ns):
    return {"authorization": "Bearer " + json.dumps({"aud": "demo", "sub": "caller-svc", "ns": ns})}

def passes(a, ns):
    a.authorize(md(ns), "/demo.Demo/Do", None)
    a.authenticate(md(ns))

def denied(a, ns):
    for call in (lambda: a.authorize(md(ns), "/demo.Demo/Do", None), lambda: a.authenticate(md(ns))):
        try:
            call()
        except ProtoError as e:
            assert e.code == "PERMISSION_DENIED", e.code
        else:
            raise AssertionError("caller in %r must be denied" % ns)

beta = make("/tenants/beta")
for ns in ("/tenants/beta", "/system", "/system/us-east", "/"):
    passes(beta, ns)
for ns in ("/tenants/acme", "/tenants/beta2", "/tenants/beta/sub", "/users/u1"):
    denied(beta, ns)

# Shared instances and an unset host namespace admit every tenant, as before.
for host in (None, "", "/system", "/system/us-east", "/tenants", "/tenants/a/b"):
    passes(make(host), "/tenants/acme")
print("ok")
`
	mod := filepath.Join(t.TempDir(), "host_tenant_check.py")
	write(t, mod, script)
	out, err := exec.Command(py, "-I", mod, root).CombinedOutput()
	if err != nil {
		t.Fatalf("tenant-owned door check is broken under Python: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "ok") {
		t.Fatalf("unexpected output:\n%s", out)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
