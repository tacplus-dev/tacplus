// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"strings"
	"testing"
	"text/template"
)

// F-14 pin: generated Rust services must shut down gracefully on SIGTERM
// (k8s/systemd) as well as SIGINT — ctrl_c() alone means SIGTERM kills the
// process with no drain.
func TestRustServiceManagerHandlesSigterm(t *testing.T) {
	tmplStr, err := loadTemplate("service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}

	for _, want := range []string{
		"async fn shutdown_signal()",
		"signal::unix::signal(signal::unix::SignalKind::terminate())",
	} {
		if !strings.Contains(tmplStr, want) {
			t.Fatalf("Rust ServiceManager template missing SIGTERM-aware shutdown %q", want)
		}
	}

	// No signal-wait site may listen for ctrl_c only — every one must go
	// through the SIGINT+SIGTERM helper. The only ctrl_c() call allowed is
	// the one inside shutdown_signal itself.
	if got := strings.Count(tmplStr, "signal::ctrl_c()"); got != 1 {
		t.Fatalf("expected exactly 1 signal::ctrl_c() call (inside shutdown_signal), found %d", got)
	}

	// Both serve_with_shutdown futures and the c2d-only wait use the helper,
	// and the second-signal escape during graceful shutdown does too.
	if got := strings.Count(tmplStr, "shutdown_signal().await"); got < 3 {
		t.Fatalf("expected at least 3 shutdown_signal().await wait sites, found %d", got)
	}
	if !strings.Contains(tmplStr, "_ = shutdown_signal() => {\n                info!(\"Second signal received, forcing exit\");") {
		t.Fatalf("second-signal escape must also be SIGTERM-aware")
	}
}

func TestRustServiceManagerBubblesFirstHealthPublishFailure(t *testing.T) {
	tmplStr, err := loadTemplate("service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	for _, want := range []string{
		"Ok(Err(e)) => return Err(format!(\"first health publish: {}\", e).into()),",
		"Err(_) => return Err(\"health reporter stopped before first successful publish\".into()),",
	} {
		if !strings.Contains(tmplStr, want) {
			t.Fatalf("Rust ServiceManager must fail startup on classified first health publish failure; missing %q", want)
		}
	}
}

func TestRustServiceManagerTreatsMissingTokenMinterAsPermanent(t *testing.T) {
	tmplStr, err := loadTemplate("service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	for _, want := range []string{
		"fn is_permanent_identity_init_error(err: &(dyn std::error::Error + 'static)) -> bool",
		"err.downcast_ref::<service_im::TokenMinterNotConfigured>().is_some()",
		"if is_permanent_identity_init_error(e.as_ref()) {",
		`return Err(format!("permanent identity startup failure: {}", e).into());`,
	} {
		if !strings.Contains(tmplStr, want) {
			t.Fatalf("Rust ServiceManager must fail missing-token-minter startup without retrying; missing %q", want)
		}
	}
}

func TestRustCertStreamsFollowSharedContractMarkers(t *testing.T) {
	tmplStr, err := loadTemplate("service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	for _, want := range []string{
		"let tls_conn = state_store_conn.clone();",
		"smb_authority_conn: Arc<ServiceConnection>,",
		"ServiceConnection::new_dynamic(",
		`"{{.SmbAuthorityServiceID}}"`,
		"let smb_conn = self.smb_authority_conn.clone();",
		"tls_cert_chain_proto: Arc<tokio::sync::RwLock<Option<x509_pb::X509CertChain>>>",
		"tls_server_certified_key: Arc<std::sync::RwLock<Option<Arc<CertifiedKey>>>>",
		"struct ReloadingServerCertResolver",
		"impl ResolvesServerCert for ReloadingServerCertResolver",
		"fn certified_key_from_pem(",
		"async fn reloading_tls_incoming(",
		"serve_with_incoming_shutdown(incoming",
		"handler.new_tls_cert(&initial_tls_chain, &initial_tls_key).await",
		"async fn tls_cert_stream_loop(\n    conn: Arc<ServiceConnection>,",
		"async fn smb_cert_stream_loop(\n    conn: Arc<ServiceConnection>,",
		"let mut init_attempts: u32 = 0;",
		"conn_provider.conn().await",
		"TLS cert stream failed before first delivery",
		"SMB cert stream failed before first delivery after {} attempts",
		"TLS cert stream ended cleanly, reconnecting",
		"SMB cert stream ended cleanly, reconnecting",
		"let mut pending_private_key_der: Option<Vec<u8>> = None;",
		"let mut pending_public_key_der: Option<Vec<u8>> = None;",
		"active_private_key_der = candidate_private_key_der.clone();",
		"active_public_key_der = candidate_public_key_der.clone();",
		"pending_private_key_der = Some(new_private_key_der);",
		"pending_public_key_der = Some(new_public_key_der);",
		"*guard = Some(server_certified_key);",
		"cert_leaf_matches_public_key",
	} {
		if !strings.Contains(tmplStr, want) {
			t.Fatalf("Rust cert-stream template must follow shared contract; missing %q", want)
		}
	}
	cargoTmpl, err := loadTemplate("cargo.toml.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate cargo.toml.tmpl: %v", err)
	}
	if !strings.Contains(cargoTmpl, "x509-cert = { workspace = true }") {
		t.Fatalf("Rust cert-stream Cargo template must include x509-cert workspace dependency")
	}
	if !strings.Contains(cargoTmpl, "rustls-pemfile = { workspace = true }") ||
		!strings.Contains(cargoTmpl, "tokio-rustls = { workspace = true }") {
		t.Fatalf("Rust cert-stream Cargo template must include reloadable TLS workspace dependencies")
	}
	if strings.Contains(tmplStr, "tonic::transport::ServerTlsConfig::new()") {
		t.Fatalf("auto-TLS server must use reloadable rustls incoming, not static ServerTlsConfig")
	}
	if strings.Contains(tmplStr, "let tls_conn = state_store_conn.conn().await") ||
		strings.Contains(tmplStr, "let smb_conn = self.state_store_conn.conn().await") {
		t.Fatalf("cert streams must not capture one Channel before the retry loop")
	}
}

// F-4c pin: the EventBus Subscribe stream must carry an audience-scoped
// bearer token (minted fresh per reconnect attempt), and the SM's outer
// resolve→subscribe loop must back off instead of hot-spinning when the
// subscriber returns on a non-retryable error.
func TestRustSubscriberSendsBearerTokenAndOuterLoopBacksOff(t *testing.T) {
	subTmpl, err := loadTemplate("subscriber.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate subscriber: %v", err)
	}

	// Subscriber fetches a token per connect attempt and attaches it before
	// opening the stream.
	for _, want := range []string{
		"pub type TokenSource",
		"let token = (self.token_source)().await?;",
		`request.metadata_mut().insert("authorization", token_value);`,
		"struct SubscribeRecord",
		"type Decode = Bytes;",
		"decode_subscribe_record(raw.as_ref())",
		`"{{.SourcePackage}}.{{.SourceField}} handler error"`,
		"return Err(e);",
	} {
		if !strings.Contains(subTmpl, want) {
			t.Fatalf("Rust subscriber template missing %q", want)
		}
	}
	tokenIdx := strings.Index(subTmpl, "let token = (self.token_source)().await?;")
	attachIdx := strings.Index(subTmpl, `request.metadata_mut().insert("authorization", token_value);`)
	streamIdx := strings.Index(subTmpl, "grpc.streaming(request, path, codec).await?")
	if !(tokenIdx < attachIdx && attachIdx < streamIdx) {
		t.Fatalf("subscriber must mint and attach the token before opening the Subscribe stream")
	}
	if strings.Contains(subTmpl, "let meta = RecordMeta::default();") {
		t.Fatalf("Rust subscriber must decode EventBus RecordMeta envelopes instead of hard-coding default metadata")
	}

	// SM wires an audience-scoped token source into the subscriber and backs
	// off between outer resolve→subscribe iterations.
	rendered := renderRustServiceManagerForTest(t, smTemplateData{
		ServiceLabel:        "demo",
		StateStoreServiceID: "statestore",
		HasSubscriptions:    true,
		Subscriptions: []subscriptionEntry{{
			FieldSnake:        "event_stream",
			HandlerMethodName: "on_event_stream",
			SubscriberMod:     "aegis_event_stream",
			SourcePackage:     "aegis",
			TypeName:          "TopologyEvent",
			TargetService:     "eventbus",
		}},
	})

	for _, want := range []string{
		`im.get_token_for("eventbus")`,
		"subscribe::aegis_event_stream::TokenSource",
		"sub_token_source.clone(),",
		"let mut outer_backoff = servicemanager::RECONNECT_INITIAL_BACKOFF;",
		"tokio::time::sleep(outer_backoff)",
		"outer_backoff = servicemanager::next_reconnect_backoff(outer_backoff);",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Rust ServiceManager subscription wiring missing %q:\n%s", want, rendered)
		}
	}

	// The backoff sleep must sit between subscribe() ending and the next
	// resolve, and the per-reconnect re-resolution of the eventbus instance
	// must stay inside the loop (that behavior is correct — do not hoist it).
	loopIdx := strings.Index(rendered, "let mut outer_backoff = servicemanager::RECONNECT_INITIAL_BACKOFF;")
	resolveIdx := strings.Index(rendered[loopIdx:], `sub_sm.resolve_service("eventbus")`)
	subscribeIdx := strings.Index(rendered[loopIdx:], "subscriber.subscribe(handler_fn.clone())")
	sleepIdx := strings.Index(rendered[loopIdx:], "tokio::time::sleep(outer_backoff)")
	if resolveIdx < 0 || subscribeIdx < 0 || sleepIdx < 0 ||
		!(resolveIdx < subscribeIdx && subscribeIdx < sleepIdx) {
		t.Fatalf("outer loop must resolve, subscribe, then back off before re-resolving:\n%s", rendered)
	}
}

// Guard: the subscriber template must still parse and render standalone.
func TestRustSubscriberTemplateRenders(t *testing.T) {
	tmplStr, err := loadTemplate("subscriber.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	tmpl, err := template.New("subscriber").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse subscriber template: %v", err)
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, subscriberTemplateData{
		SourcePackage:   "aegis",
		SourceStateRoot: "AegisState",
		SourceField:     "event_stream",
		SourceProtoDir:  "common",
		TypeProtoDir:    "common",
		TypePackage:     "sensor",
		FieldPascal:     "EventStream",
		TypeName:        "TopologyEvent",
		GrpcService:     "eventbus.EventBusTypedService",
		GrpcMethod:      "SubscribeAegisAegisStateEventStream",
	}); err != nil {
		t.Fatalf("execute subscriber template: %v", err)
	}
	if !strings.Contains(out.String(), "token_source,") {
		t.Fatalf("rendered subscriber missing token_source field:\n%s", out.String())
	}
}
