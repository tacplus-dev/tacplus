// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// Issue #475 (products-layer-2026-11-08): an S2S client whose target is a
// tenant's or a product's service carries its target's layer, and every call
// and stream open first checks the caller's own token against it
// (servicemanager::target_layer::check_target_layer), PERMISSION_DENIED before
// anything is sent. A /system target admits every layer: no check.

func renderLayerClient(t *testing.T, m *parser.MethodInfo, targetLayer string) grpcMethodResult {
	t.Helper()
	res, err := generateRustGrpcClientMethodRotating(&parser.ServiceInfo{Name: "CrewService", IsS2S: true}, m, "crew", map[string]string{}, false, targetLayer)
	if err != nil {
		t.Fatalf("generateRustGrpcClientMethodRotating(%s): %v", m.Name, err)
	}
	return res
}

// platformCrewMethod is crewMethod without a route field.
func platformCrewMethod(name string, client, server bool) *parser.MethodInfo {
	m := crewMethod(name, client, server)
	m.RouteField = nil
	return m
}

// The caller's own token for the layer check: no provider, or one that fails,
// is no token (the call then fails on its missing token, as in Go).
const ownLayerToken = `let own_token = match self.token_provider.as_ref() {
            Some(tp) => (tp)().await.ok(),
            None => None,
        };`

func layerCheck(target string) string {
	return `servicemanager::target_layer::check_target_layer("` + target + `", own_token.as_deref())?;`
}

func TestRustS2SClientChecksTheTargetLayerInEveryShape(t *testing.T) {
	const target = "product:ledger"
	assertOrdered(t, renderLayerClient(t, platformCrewMethod("GetCrew", false, false), target).method,
		"let req_base = request.into_request();",
		ownLayerToken,
		layerCheck(target),
		"self.unary_with_retry(",
		"client.get_crew(req).await")
	assertOrdered(t, renderLayerClient(t, platformCrewMethod("WatchCrew", false, true), target).method,
		"request.into_request();",
		ownLayerToken,
		layerCheck(target),
		"let channel = self.channel_provider.channel().await?;",
		"client.watch_crew(req).await")
	// A client stream's open is not async: the check runs in its task, before
	// the channel is asked for, and a refusal comes back from finish().
	assertOrdered(t, renderLayerClient(t, platformCrewMethod("UploadCrew", true, false), target).method,
		"tokio::spawn(",
		"let own_token = match token_provider.as_ref() {\n                    Some(tp) => (tp)().await.ok(),",
		"                "+layerCheck(target),
		"let channel = cp.channel().await?;")
	// A bidi stream checks on each connect, and closes when refused.
	for _, authDisabled := range []bool{false, true} {
		m := platformCrewMethod("ChatCrew", true, true)
		m.AuthDisabled = authDisabled
		assertOrdered(t, renderLayerClient(t, m, target).method,
			"Its target is a product:ledger service",
			"let tp = self.token_provider.clone();",
			"tokio::spawn(",
			"let own_token = match &tp {",
			"Ok(Ok(token)) => Some(token),\n                        _ => None,",
			`if let Err(e) = servicemanager::target_layer::check_target_layer("product:ledger", own_token.as_deref()) {`,
			"refused before dialling, stream closed",
			"return;",
			"cp.channel()")
	}
}

// A routed rpc (a tenant's target) reuses the route check's own token, and
// checks the layer before the route.
func TestRustRoutedS2SClientChecksTheTenantLayerFirst(t *testing.T) {
	assertOrdered(t, renderLayerClient(t, crewMethod("GetCrew", false, false), "tenant").method,
		ownRouteToken,
		layerCheck("tenant"),
		`servicemanager::tenant_route::caller_route_tenant("target_tenant", &req_base.get_ref().target_tenant, own_token.as_deref())?;`,
		"self.unary_with_retry(")
	assertOrdered(t, renderLayerClient(t, crewMethod("UploadCrew", true, false), "tenant").method,
		"let own_token = match token_provider.as_ref() {",
		"                "+layerCheck("tenant"),
		`servicemanager::tenant_route::caller_route_tenant("tenant", &stream_tenant, own_token.as_deref())?;`,
		"let channel = cp.channel_for_tenant(&stream_tenant).await?;")
	got := renderLayerClient(t, crewMethod("ChatCrew", true, true), "tenant").method
	assertOrdered(t, got,
		`if let Err(e) = servicemanager::tenant_route::caller_route_tenant("tenant", &stream_tenant, own_token.as_deref()) {`,
		`if let Err(e) = servicemanager::target_layer::check_target_layer("tenant", own_token.as_deref()) {`,
		"cp.channel_for_tenant(&stream_tenant)")
	if n := strings.Count(got, "let own_token"); n != 1 {
		t.Fatalf("a routed bidi stream binds its own token %d times, want once:\n%s", n, got)
	}
	for _, m := range []*parser.MethodInfo{crewMethod("GetCrew", false, false), crewMethod("UploadCrew", true, false)} {
		if got := renderLayerClient(t, m, "tenant").method; strings.Contains(got, "(tp)().await.ok()") {
			t.Fatalf("%s: a routed call must reuse the route check's own token:\n%s", m.Name, got)
		}
	}
}

// A /system target (and any client with no target layer) carries no check.
func TestRustSystemTargetClientHasNoLayerCheck(t *testing.T) {
	for _, m := range []*parser.MethodInfo{
		platformCrewMethod("GetCrew", false, false),
		platformCrewMethod("WatchCrew", false, true),
		platformCrewMethod("UploadCrew", true, false),
		platformCrewMethod("ChatCrew", true, true),
	} {
		res := renderLayerClient(t, m, "")
		for _, forbidden := range []string{"target_layer", "own_token"} {
			if strings.Contains(res.method+res.extraType, forbidden) {
				t.Fatalf("%s: a /system target's client must not carry %q:\n%s", m.Name, forbidden, res.method)
			}
		}
	}
}
