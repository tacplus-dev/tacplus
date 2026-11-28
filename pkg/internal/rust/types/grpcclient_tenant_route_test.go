// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/collection"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// Issue #170: the generated S2S client picks the target tenant's instance by
// the request's route field. Every routed call reads and checks the field
// (INVALID_ARGUMENT before any network call) and asks the channel provider for
// THAT tenant's channel; a platform call keeps the tenant-less channel.
//
// Issue #258 (S2S never crosses tenants): the field is checked against the
// caller's own token, from its token provider. A tenant's service fills it
// with its own tid when it is empty and is refused, PERMISSION_DENIED, before
// anything is dialled when it names another; the request sent carries the
// tenant routed to.

func renderCrewClient(t *testing.T, m *parser.MethodInfo) grpcMethodResult {
	t.Helper()
	res, err := generateRustGrpcClientMethod(&parser.ServiceInfo{Name: "CrewService", IsS2S: true}, m, "crew", map[string]string{})
	if err != nil {
		t.Fatalf("generateRustGrpcClientMethod(%s): %v", m.Name, err)
	}
	return res
}

func TestRustRoutedUnaryClientRoutesByTheField(t *testing.T) {
	got := renderCrewClient(t, crewMethod("GetCrew", false, false)).method
	assertOrdered(t, got,
		"let mut req_base = request.into_request();",
		"req_base.get_ref()\n            .validate()",
		ownRouteToken,
		`let route_tenant = servicemanager::tenant_route::caller_route_tenant("target_tenant", &req_base.get_ref().target_tenant, own_token.as_deref())?;`,
		"req_base.get_mut().target_tenant = route_tenant.clone();",
		"let req_message = req_base.get_ref().clone();",
		"self.unary_with_retry(",
		"let channel = self.channel_provider.channel_for_tenant(&route_tenant).await?;",
		"client.get_crew(req).await")
	if strings.Contains(got, "self.channel_provider.channel().await") {
		t.Fatalf("a routed call must never take the tenant-less channel:\n%s", got)
	}
}

func TestRustRoutedServerStreamAndPageClientsRouteByTheField(t *testing.T) {
	stream := crewMethod("WatchCrew", false, true)
	page := crewMethod("ListCrew", false, false)
	page.Resolved = &collection.Resolved{Mode: "PAGE", PageItemsField: "items", PageItemType: "crew.CrewResponse"}
	for _, m := range []*parser.MethodInfo{stream, page} {
		got := renderCrewClient(t, m).method
		assertOrdered(t, got,
			"let mut req = request.into_request();",
			ownRouteToken,
			`let route_tenant = servicemanager::tenant_route::caller_route_tenant("target_tenant", &req.get_ref().target_tenant, own_token.as_deref())?;`,
			"req.get_mut().target_tenant = route_tenant.clone();",
			"let channel = self.channel_provider.channel_for_tenant(&route_tenant).await?;")
	}
}

// A client stream's tenant is fixed when it is opened: the open method takes
// it explicitly and refuses a non-tid before anything is dialled, and the
// handle refuses a message routed to any other tenant. The open is not async,
// so the caller's own token is read in the stream's task, before the channel
// is asked for: a tenant's service opening a stream to another tenant gets
// PERMISSION_DENIED from finish(), and nothing is dialled.
func TestRustRoutedClientStreamFixesTheTenantAtOpen(t *testing.T) {
	res := renderCrewClient(t, crewMethod("UploadCrew", true, false))
	assertOrdered(t, res.method,
		"pub fn upload_crew(&self, tenant: &str) -> Result<UploadCrewClientStream, tonic::Status> {",
		`let tenant = servicemanager::tenant_route::route_tenant("tenant", tenant)?;`,
		"tokio::spawn(",
		"let own_token = match token_provider.as_ref() {",
		`servicemanager::tenant_route::caller_route_tenant("tenant", &stream_tenant, own_token.as_deref())?;`,
		"let channel = cp.channel_for_tenant(&stream_tenant).await?;")
	assertOrdered(t, res.extraType,
		"    tenant: String,",
		"pub async fn send(&self, msg: CrewRequest) -> Result<(), tonic::Status> {",
		`servicemanager::tenant_route::route_tenant("target_tenant", &msg.target_tenant)?;`,
		"if msg.target_tenant != self.tenant {",
		"self.tx\n            .send(msg)")
}

func TestRustRoutedBidiFixesTheTenantAtOpen(t *testing.T) {
	res := renderCrewClient(t, crewMethod("ChatCrew", true, true))
	assertOrdered(t, res.method,
		"tenant: &str,",
		") -> Result<ChatCrewBidiHandle, tonic::Status>",
		`let tenant = servicemanager::tenant_route::route_tenant("tenant", tenant)?;`,
		"let tp = self.token_provider.clone();",
		"tokio::spawn(",
		"let own_token = match &tp {",
		`if let Err(e) = servicemanager::tenant_route::caller_route_tenant("tenant", &stream_tenant, own_token.as_deref()) {`,
		"refused before dialling",
		"return;",
		"tokio::time::timeout(CONNECT_TIMEOUT, cp.channel_for_tenant(&stream_tenant)).await",
		"Ok(handle)")
	if strings.Contains(res.method, "cp.channel()") {
		t.Fatalf("a routed bidi stream must never take the tenant-less channel:\n%s", res.method)
	}
	assertOrdered(t, res.extraType,
		"    tenant: String,",
		`servicemanager::tenant_route::route_tenant("target_tenant", &msg.target_tenant)?;`,
		"if msg.target_tenant != self.tenant {",
		"self.tx.send(msg)")
}

// The caller's own token, read off its token provider for the route check: no
// provider is no token (the field must then name the tenant), and a provider
// that fails refuses the call rather than skip the check.
const ownRouteToken = `let own_token = match self.token_provider.as_ref() {
            Some(tp) => Some((tp)().await.map_err(|e| tonic::Status::unauthenticated(format!("auth token: {e}")))?),
            None => None,
        };`

// A routed call made without authentication still checks the caller's own
// token when it has a provider: the check is not part of the auth snippet.
func TestRustRoutedUnauthenticatedClientStillChecksTheCallersTenant(t *testing.T) {
	m := crewMethod("GetCrew", false, false)
	m.AuthDisabled = true
	got := renderCrewClient(t, m).method
	assertOrdered(t, got,
		ownRouteToken,
		`servicemanager::tenant_route::caller_route_tenant("target_tenant", &req_base.get_ref().target_tenant, own_token.as_deref())?;`,
		"let channel = self.channel_provider.channel_for_tenant(&route_tenant).await?;")
	if strings.Contains(got, `"authorization"`) {
		t.Fatalf("an unauthenticated rpc must not send a bearer token:\n%s", got)
	}
}

// A platform service's client is untouched: same signatures, same channel.
func TestRustPlatformClientIsNotRouted(t *testing.T) {
	for _, m := range []*parser.MethodInfo{crewMethod("GetCrew", false, false), crewMethod("WatchCrew", false, true), crewMethod("UploadCrew", true, false), crewMethod("ChatCrew", true, true)} {
		m.RouteField = nil
		res := renderCrewClient(t, m)
		for _, forbidden := range []string{"route_tenant", "channel_for_tenant", "tenant: &str", "own_token"} {
			if strings.Contains(res.method+res.extraType, forbidden) {
				t.Fatalf("%s: a platform client must not carry %q:\n%s\n%s", m.Name, forbidden, res.method, res.extraType)
			}
		}
	}
}

// The W2S client reaches a routed rpc through the gateway, which routes by the
// field; the client refuses an empty or malformed one before sending, exactly
// as the client-only languages do.
func TestRustW2SClientRefusesAMalformedRouteFieldBeforeSending(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "CrewApi", IsC2S: true}
	for _, m := range []*parser.MethodInfo{crewMethod("GetCrew", false, false), crewMethod("BrokerCrew", true, true)} {
		m.HttpPath = "/crew/CrewApi/" + m.Name
		res := generateRustW2SClientMethod(svc, m, "crew", map[string]string{})
		got := res.method + res.extraType
		if !strings.Contains(got, `return Err(tonic::Status::invalid_argument("route field \"target_tenant\" is empty or not a tenant id (exactly 8 characters of [0-9a-z])"));`) {
			t.Fatalf("%s: W2S client must check its route field before sending:\n%s", m.Name, got)
		}
		m.RouteField = nil
		res = generateRustW2SClientMethod(svc, m, "crew", map[string]string{})
		if strings.Contains(res.method+res.extraType, "target_tenant") {
			t.Fatalf("%s: a platform W2S client must not check a route field", m.Name)
		}
	}
}
