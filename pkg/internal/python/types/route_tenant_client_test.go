// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// Issue #170: a routed rpc's request names its target tenant in its route
// field. Python clients reach services through the gateway, which routes by
// that field, so the client's share is refusing an empty or malformed tid
// BEFORE anything is sent, with the shared INVALID_ARGUMENT ProtoError naming
// the field. A platform rpc (no route field) renders exactly as before.

func pyRoutedMethod(clientStreaming, serverStreaming bool) *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:            "Send",
		InputType:       "SendRequest",
		OutputType:      "SendResponse",
		FullInputType:   "crew.SendRequest",
		FullOutputType:  "crew.SendResponse",
		HttpMethod:      "POST",
		HttpPath:        "/api/crew/v1/crew/send",
		ClientStreaming: clientStreaming,
		ServerStreaming: serverStreaming,
		RouteField:      &parser.RouteFieldDescriptor{Field: "tenant_id", JSONName: "tenantId", Number: 3},
	}
}

func pyPlatformCopy(m *parser.MethodInfo) *parser.MethodInfo {
	c := *m
	c.RouteField = nil
	return &c
}

func TestPythonGrpcClientRefusesBadRouteTenantBeforeSend(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Crew", IsC2S: true}

	unary := generatePyGrpcClientMethod(svc, pyRoutedMethod(false, false), "crew")
	check := `_route_tenant(request, "tenant_id")`
	if !strings.Contains(unary, check) {
		t.Fatalf("routed unary client must check the route field:\n%s", unary)
	}
	if strings.Index(unary, check) > strings.Index(unary, "self._call_unary(") {
		t.Fatalf("route check must precede the call:\n%s", unary)
	}

	server := generatePyGrpcClientMethod(svc, pyRoutedMethod(false, true), "crew")
	if strings.Index(server, check) < 0 || strings.Index(server, check) > strings.Index(server, "self._stub.Send(") {
		t.Fatalf("routed server-streaming client must check the route field before opening the stream:\n%s", server)
	}

	for _, m := range []*parser.MethodInfo{pyRoutedMethod(true, false), pyRoutedMethod(true, true)} {
		got := generatePyGrpcClientMethod(svc, m, "crew")
		if !strings.Contains(got, `_route_pinned_requests(self._validating_request_iterator(request_iterator, validate_send_request), "tenant_id")`) {
			t.Fatalf("routed client-streaming client must pin every message to one tenant:\n%s", got)
		}
	}

	for _, m := range []*parser.MethodInfo{pyRoutedMethod(false, false), pyRoutedMethod(false, true), pyRoutedMethod(true, false), pyRoutedMethod(true, true)} {
		if got := generatePyGrpcClientMethod(svc, pyPlatformCopy(m), "crew"); strings.Contains(got, "route") {
			t.Fatalf("platform rpc must not carry a route check:\n%s", got)
		}
	}
}

func TestPythonW2SClientRefusesBadRouteTenantBeforeSend(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Crew", IsC2S: true}

	for _, server := range []bool{false, true} {
		got := generatePyW2SClientMethod(svc, pyRoutedMethod(false, server), "crew", nil).method
		check := `_route_tenant(request, "tenant_id")`
		if strings.Index(got, check) < 0 || strings.Index(got, check) > strings.Index(got, "self._message_to_body(request)") {
			t.Fatalf("routed W2S call must check the route field before building the body:\n%s", got)
		}
	}

	cs := generatePyW2SClientMethod(svc, pyRoutedMethod(true, false), "crew", nil).method
	if !strings.Contains(cs, `route = _RoutePin("tenant_id")`) || !strings.Contains(cs, "route.check(req)") {
		t.Fatalf("routed W2S client stream must pin every message to one tenant:\n%s", cs)
	}
	if strings.Index(cs, "route.check(req)") > strings.Index(cs, "self._send_message_frame(") {
		t.Fatalf("route check must precede the frame send:\n%s", cs)
	}

	bidi := generatePyW2SClientMethod(svc, pyRoutedMethod(true, true), "crew", nil)
	if !strings.Contains(bidi.extraType, `self._route = _RoutePin("tenant_id")`) ||
		strings.Index(bidi.extraType, "self._route.check(msg)") > strings.Index(bidi.extraType, "await self._send_body(body)") ||
		!strings.Contains(bidi.extraType, "self._route.check(msg)") {
		t.Fatalf("routed W2S bidi handle must pin every message to one tenant:\n%s", bidi.extraType)
	}

	for _, m := range []*parser.MethodInfo{pyRoutedMethod(false, false), pyRoutedMethod(false, true), pyRoutedMethod(true, false), pyRoutedMethod(true, true)} {
		got := generatePyW2SClientMethod(svc, pyPlatformCopy(m), "crew", nil)
		if strings.Contains(got.method+got.extraType, "_route") {
			t.Fatalf("platform rpc must not carry a route check:\n%s%s", got.method, got.extraType)
		}
	}
}

func TestPythonRestClientRefusesBadRouteTenantBeforeSend(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Crew", IsC2S: true}
	for _, server := range []bool{false, true} {
		got := generatePyRestClientMethod(svc, pyRoutedMethod(false, server), "crew")
		check := `_route_tenant(request, "tenant_id")`
		if strings.Index(got, check) < 0 || strings.Index(got, check) > strings.Index(got, "self._") {
			t.Fatalf("routed REST call must check the route field before the request:\n%s", got)
		}
		if plat := generatePyRestClientMethod(svc, pyPlatformCopy(pyRoutedMethod(false, server)), "crew"); strings.Contains(plat, "route") {
			t.Fatalf("platform rpc must not carry a route check:\n%s", plat)
		}
	}
}

// The helper block is emitted only into a client file with a routed rpc, so a
// platform-only build stays byte-for-byte what it was.
func TestPythonRouteHelpersOnlyForRoutedServices(t *testing.T) {
	routed := &parser.ServiceInfo{Name: "Crew", Methods: []*parser.MethodInfo{pyRoutedMethod(false, false)}}
	platform := &parser.ServiceInfo{Name: "Crew", Methods: []*parser.MethodInfo{pyPlatformCopy(pyRoutedMethod(false, false))}}
	if got := pyRouteHelpers(routed, "...types.sys.errors"); !strings.Contains(got, "def _route_tenant(") ||
		!strings.Contains(got, "from ...types.sys.errors.exceptions import invalid_argument as _route_invalid_argument") {
		t.Fatalf("routed service must get the route helpers:\n%s", got)
	}
	if got := pyRouteHelpers(platform, "...types.sys.errors"); got != "" {
		t.Fatalf("platform service must get no route helpers, got:\n%s", got)
	}
	for _, tmpl := range []string{"python/types/grpcclient.py.tmpl", "python/types/w2sclient.py.tmpl", "python/types/restclient.py.tmpl"} {
		if !strings.Contains(readPythonTemplate(t, tmpl), "{{.CrossPackageImports}}{{.RouteHelpers}}\n") {
			t.Errorf("%s must splice RouteHelpers onto the imports line (no blank line for platform builds)", tmpl)
		}
	}
}

// Behaviour, under the system interpreter: the rendered helpers refuse empty
// and malformed tids with INVALID_ARGUMENT naming the field, admit a tid, and
// pin a stream to the tenant its first message names.
func TestPythonRouteHelpersBehaviour(t *testing.T) {
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
	write(t, filepath.Join(root, "pb", "__init__.py"), "")
	write(t, filepath.Join(root, "pb", "errors", "__init__.py"), "")
	write(t, filepath.Join(root, "pb", "errors", "exceptions.py"), `
class ProtoError(Exception):
    def __init__(self, code, message):
        super().__init__(message); self.code = code
def invalid_argument(m): return ProtoError("INVALID_ARGUMENT", m)
`)
	routed := &parser.ServiceInfo{Name: "Crew", Methods: []*parser.MethodInfo{pyRoutedMethod(false, false)}}
	write(t, filepath.Join(root, "pb", "client.py"), pyRouteHelpers(routed, "pb.errors"))
	script := `
import sys
sys.path.insert(0, sys.argv[1])
import asyncio
from pb.client import _route_tenant, _RoutePin, _route_pinned_requests
from pb.errors.exceptions import ProtoError

class Req:
    def __init__(self, v): self.tenant_id = v

def refused(fn, needle="tenant_id"):
    try:
        fn()
    except ProtoError as e:
        assert e.code == "INVALID_ARGUMENT", e.code
        assert needle in str(e), str(e)
    else:
        raise AssertionError("must be refused")

for bad in ("", "ABCDEFGH", "abc", "abcdefghi", "abcdefg!", "abcdefg\n", " abcdefg", "abcdefg٣", "tenants/a"):
    refused(lambda: _route_tenant(Req(bad), "tenant_id"))
assert _route_tenant(Req("aaaaaaaa"), "tenant_id") == "aaaaaaaa"
assert _route_tenant(Req("00000001"), "tenant_id") == "00000001"

pin = _RoutePin("tenant_id")
assert pin.check(Req("aaaaaaaa")) == "aaaaaaaa"
assert pin.check(Req("aaaaaaaa")) == "aaaaaaaa"
refused(lambda: pin.check(Req("bbbbbbbb")))
refused(lambda: pin.check(Req("")))

async def stream(vals):
    for v in vals:
        yield Req(v)
async def drain(vals):
    return [r.tenant_id async for r in _route_pinned_requests(stream(vals), "tenant_id")]
assert asyncio.run(drain(["aaaaaaaa", "aaaaaaaa"])) == ["aaaaaaaa", "aaaaaaaa"]
refused(lambda: asyncio.run(drain(["aaaaaaaa", "bbbbbbbb"])))
refused(lambda: asyncio.run(drain(["bad"])))
print("ok")
`
	mod := filepath.Join(t.TempDir(), "route_check.py")
	write(t, mod, script)
	out, err := exec.Command(py, "-I", mod, root).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ok") {
		t.Fatalf("route helpers broken under Python: %v\n%s", err, out)
	}
}
