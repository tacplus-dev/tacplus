// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// Issue #170: a routed rpc's request names its target tenant in its route
// field. TypeScript clients reach services through the gateway, which routes by
// that field, so the client's share is refusing an empty or malformed tid
// BEFORE anything is sent, with an INVALID_ARGUMENT-coded error naming the
// field. A platform rpc (no route field) renders exactly as before.

func tsRoutedMethod(clientStreaming, serverStreaming bool) *parser.MethodInfo {
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

func tsPlatformCopy(m *parser.MethodInfo) *parser.MethodInfo {
	c := *m
	c.RouteField = nil
	return &c
}

const tsWantRouteCheck = `tacplusRouteTenant(request.tenantId, 'tenant_id');`

// before asserts check occurs in code, ahead of every marker that is present.
func tsAssertBefore(t *testing.T, code, check string, markers ...string) {
	t.Helper()
	at := strings.Index(code, check)
	if at < 0 {
		t.Fatalf("missing %q:\n%s", check, code)
	}
	for _, m := range markers {
		if i := strings.Index(code, m); i >= 0 && i < at {
			t.Fatalf("%q must precede %q:\n%s", check, m, code)
		}
	}
}

func TestTypeScriptGrpcClientRefusesBadRouteTenantBeforeSend(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Crew", IsC2S: true}
	render := func(m *parser.MethodInfo) string {
		t.Helper()
		r, err := generateTsGrpcClientMethod(svc, m, "crew")
		if err != nil {
			t.Fatal(err)
		}
		return r.method + r.extraType
	}

	tsAssertBefore(t, render(tsRoutedMethod(false, false)), tsWantRouteCheck, "this.unaryWithRetry")
	tsAssertBefore(t, render(tsRoutedMethod(false, true)), tsWantRouteCheck, "makeServerStreamRequest")

	cs := render(tsRoutedMethod(true, false))
	tsAssertBefore(t, cs, "const routePin = new TacplusRoutePin('tenant_id');", "makeClientStreamRequest")
	tsAssertBefore(t, cs, "routePin.check(request.tenantId);", "stream.write(request)")

	bidi := render(tsRoutedMethod(true, true))
	tsAssertBefore(t, bidi, "const routePin = new TacplusRoutePin('tenant_id');", "makeBidiStreamRequest")
	tsAssertBefore(t, bidi, "routePin.check(msg.tenantId);", "stream.write(msg)")

	for _, m := range []*parser.MethodInfo{tsRoutedMethod(false, false), tsRoutedMethod(false, true), tsRoutedMethod(true, false), tsRoutedMethod(true, true)} {
		if got := render(tsPlatformCopy(m)); strings.Contains(got, "acplusRoute") || strings.Contains(got, "routePin") {
			t.Fatalf("platform rpc must not carry a route check:\n%s", got)
		}
	}
}

func TestTypeScriptW2SClientRefusesBadRouteTenantBeforeSend(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Crew", IsC2S: true}
	info := &parser.ProtoInfo{Package: "crew"}

	tsAssertBefore(t, generateTsGrpcOverRestMethod(svc, tsRoutedMethod(false, false), "crew", true), tsWantRouteCheck, "this.requestJson(")
	tsAssertBefore(t, generateTsGrpcOverRestStreamMethod(svc, tsRoutedMethod(false, true), info, true), tsWantRouteCheck, "this.transport.postStream(")
	tsAssertBefore(t, generateTsGrpcOverRestLegacyMethod(svc, tsRoutedMethod(false, false), "crew"), tsWantRouteCheck, "this.transport.postJson(")
	tsAssertBefore(t, generateTsGrpcOverRestLegacyStreamMethod(svc, tsRoutedMethod(false, true), info), tsWantRouteCheck, "this.transport.postStream(")

	cs := generateTsGrpcOverRestClientStreamMethod(svc, tsRoutedMethod(true, false), "crew", true)
	tsAssertBefore(t, cs, "const routePin = new TacplusRoutePin('tenant_id');", "this.openWebSocket(")
	tsAssertBefore(t, cs, "routePin.check(request.tenantId);", "this.sendWsMessage(")

	bidi := generateTsGrpcOverRestBidiMethod(svc, tsRoutedMethod(true, true), "crew", true).method
	tsAssertBefore(t, bidi, "const routePin = new TacplusRoutePin('tenant_id');", "this.runWsBidi(")
	tsAssertBefore(t, bidi, "routePin.check(message.tenantId);", "return toJson(")

	for _, got := range []string{
		generateTsGrpcOverRestMethod(svc, tsPlatformCopy(tsRoutedMethod(false, false)), "crew", true),
		generateTsGrpcOverRestStreamMethod(svc, tsPlatformCopy(tsRoutedMethod(false, true)), info, true),
		generateTsGrpcOverRestLegacyMethod(svc, tsPlatformCopy(tsRoutedMethod(false, false)), "crew"),
		generateTsGrpcOverRestLegacyStreamMethod(svc, tsPlatformCopy(tsRoutedMethod(false, true)), info),
		generateTsGrpcOverRestClientStreamMethod(svc, tsPlatformCopy(tsRoutedMethod(true, false)), "crew", true),
		generateTsGrpcOverRestBidiMethod(svc, tsPlatformCopy(tsRoutedMethod(true, true)), "crew", true).method,
	} {
		if strings.Contains(got, "acplusRoute") || strings.Contains(got, "routePin") {
			t.Fatalf("platform rpc must not carry a route check:\n%s", got)
		}
	}
}

func TestTypeScriptRestClientRefusesBadRouteTenantBeforeSend(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "Crew", IsC2S: true}
	info := &parser.ProtoInfo{Package: "crew"}
	for _, verb := range []string{"GET", "POST", "PUT", "PATCH", "DELETE"} {
		m := tsRoutedMethod(false, false)
		m.HttpMethod = verb
		tsAssertBefore(t, generateTsRestClientMethod(svc, m, "crew"), tsWantRouteCheck, "this.transport.")
		if got := generateTsRestClientMethod(svc, tsPlatformCopy(m), "crew"); strings.Contains(got, "acplusRoute") {
			t.Fatalf("platform rpc must not carry a route check:\n%s", got)
		}
	}
	for _, verb := range []string{"GET", "POST"} {
		m := tsRoutedMethod(false, true)
		m.HttpMethod = verb
		tsAssertBefore(t, generateTsRestClientStreamMethod(svc, m, info), tsWantRouteCheck, "this.transport.", "const params")
		if got := generateTsRestClientStreamMethod(svc, tsPlatformCopy(m), info); strings.Contains(got, "acplusRoute") {
			t.Fatalf("platform rpc must not carry a route check:\n%s", got)
		}
	}
}

func TestTypeScriptRouteHelpersOnlyForRoutedServices(t *testing.T) {
	routed := &parser.ServiceInfo{Name: "Crew", Methods: []*parser.MethodInfo{tsRoutedMethod(false, false)}}
	platform := &parser.ServiceInfo{Name: "Crew", Methods: []*parser.MethodInfo{tsPlatformCopy(tsRoutedMethod(false, false))}}
	if got := tsRouteHelpers(routed); !strings.Contains(got, "function tacplusRouteTenant(") || !strings.Contains(got, "class TacplusRoutePin") {
		t.Fatalf("routed service must get the route helpers:\n%s", got)
	}
	if got := tsRouteHelpers(platform); got != "" {
		t.Fatalf("platform service must get no route helpers, got:\n%s", got)
	}
}

// Behaviour, under Node's type stripping: the rendered helpers refuse empty and
// malformed tids with code 3 (INVALID_ARGUMENT) naming the field, admit a tid,
// and pin a stream to the tenant its first message names.
func TestTypeScriptRouteHelpersBehaviour(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node found; skipping behavioral check")
	}
	if out, err := exec.Command(node, "--experimental-strip-types", "-e", "0").CombinedOutput(); err != nil {
		t.Skipf("node cannot strip types (%v: %s); skipping behavioral check", err, out)
	}
	root := t.TempDir()
	routed := &parser.ServiceInfo{Name: "Crew", Methods: []*parser.MethodInfo{tsRoutedMethod(false, false)}}
	files := map[string]string{
		"package.json": `{"type":"module"}`,
		"route.ts":     tsRouteHelpers(routed) + "\nexport { tacplusRouteTenant, TacplusRoutePin };\n",
		"check.mjs": `
import { tacplusRouteTenant, TacplusRoutePin } from './route.ts';
function refused(fn) {
  try { fn(); } catch (e) {
    if (e.code !== 3) throw new Error('want code 3, got ' + e.code + ': ' + e.message);
    if (!e.message.includes('tenant_id')) throw new Error('message must name the field: ' + e.message);
    return;
  }
  throw new Error('must be refused');
}
for (const bad of ['', 'ABCDEFGH', 'abc', 'abcdefghi', 'abcdefg!', 'abcdefg\n', ' abcdefg', 'abcdefg\u0663', 'tenants/a', undefined, null, 12345678]) {
  refused(() => tacplusRouteTenant(bad, 'tenant_id'));
}
if (tacplusRouteTenant('aaaaaaaa', 'tenant_id') !== 'aaaaaaaa') throw new Error('tid refused');
if (tacplusRouteTenant('00000001', 'tenant_id') !== '00000001') throw new Error('platform tid refused');
const pin = new TacplusRoutePin('tenant_id');
pin.check('aaaaaaaa'); pin.check('aaaaaaaa');
refused(() => pin.check('bbbbbbbb'));
refused(() => pin.check(''));
console.log('ok');
`,
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := exec.Command(node, "--experimental-strip-types", "--no-warnings", filepath.Join(root, "check.mjs")).CombinedOutput()
	if err != nil || !strings.Contains(string(out), "ok") {
		t.Fatalf("route helpers broken under TypeScript: %v\n%s", err, out)
	}
}
