// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"strings"
	"testing"
	"text/template"

	"github.com/tacplus-dev/tacplus/templates"
)

func TestRustServiceManagerMutexLockBlocksAreStatementTerminated(t *testing.T) {
	rendered := renderRustServiceManagerForTest(t, smTemplateData{
		ServiceLabel:        "demo",
		StateStoreServiceID: "statestore",
		HasStates:           true,
		GrpcServices: []grpcServiceEntry{{
			TraitName:                "DemoApi",
			HandleConfigName:         "demo_config",
			ConfigModPath:            "crate::types::services::demo::demo",
			ConfigStateGroupAccessor: "services",
			ConfigStateAccessor:      "demo_config_state",
			HasConfigState:           true,
		}},
	})

	if want := "        };\n        self.state_clients.close();"; !strings.Contains(rendered, want) {
		t.Fatalf("Rust ServiceManager template missing close() statement terminator %q:\n%s", want, rendered)
	}

	tmplStr, err := loadTemplate("service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	if want := "                        tasks.push(handle);\n                    };\n                }"; !strings.Contains(tmplStr, want) {
		t.Fatalf("Rust ServiceManager template missing config hot-reload statement terminator %q", want)
	}
}

func TestRustServiceManagerRendersDiskStoreRollSchedulers(t *testing.T) {
	rendered := renderRustServiceManagerForTest(t, smTemplateData{
		SmPrefix:            "s2sm",
		ServiceLabel:        "demo",
		StateStoreServiceID: "statestore",
		DiskStores: []rustDiskStoreRuntimeEntry{{
			StoreName:     "cred_pair",
			LogField:      "cred_pair_log",
			HandlerGetter: "get_cred_pair_store_grpc_impl",
		}},
		HasDiskStores: true,
	})

	for _, want := range []string{
		"pub struct DiskStoreLogs",
		"diskstore_stores::open_cred_pair_log(",
		"fn start_disk_store_roll_schedulers(",
		"diskstore::run_roll_scheduler(",
		`let scheduler_node_id = format!("{}/cred_pair", node_id);`,
		"h.get_cred_pair_store_grpc_impl().list(None).await",
		"diskstore_stores::snapshot_cred_pair(&response.items)",
		// The roll baseline is narrowed through the Handler's LocalRows seam.
		"Ok(records) => diskstore::local_records(\n                                            h.cred_pair_local_rows().as_deref(),",
		"self.start_disk_store_roll_schedulers(handler.clone())?;",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Rust ServiceManager missing diskstore scheduler marker %q:\n%s", want, rendered)
		}
	}
}

func TestRustDiskStoreSchedulerAcceptsAsyncSnapshots(t *testing.T) {
	src, err := templates.FS.ReadFile("rust/chassis/diskstore/src/schedule.rs.tmpl")
	if err != nil {
		t.Fatalf("read diskstore schedule template: %v", err)
	}
	rendered := string(src)
	for _, want := range []string{
		"use std::future::Future;",
		"pub async fn run_roll_scheduler<F, Fut>(",
		"F: FnMut() -> Fut,",
		"Fut: Future<Output = Vec<Record>>,",
		"let snap = snapshot().await;",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Rust diskstore scheduler must accept async snapshot suppliers; missing %q", want)
		}
	}
}

func TestRustServiceManagerRendersTracePublisherAlias(t *testing.T) {
	rendered := renderRustServiceManagerForTest(t, smTemplateData{
		ServiceLabel:     "gateway",
		TracePublisherFn: "create_telemetry_trace_event_bus_trace_events_publisher",
		Publishers: []publisherEntry{{
			FnName:        "create_telemetry_trace_event_bus_trace_events_publisher",
			LoopFn:        "telemetry_trace_event_bus_trace_events_publish_loop",
			VarPrefix:     "telemetry_trace_event_bus_trace_events",
			Label:         "telemetry.TraceEventBus.trace_events publisher",
			TargetService: "eventbus",
			EventTypeName: "TraceEvent",
			EventTypePath: "crate::types::sys::trace::TraceEvent",
		}},
		HasPublishers: true,
	})

	for _, want := range []string{
		"pub async fn create_trace_publisher(&self)",
		"self.create_telemetry_trace_event_bus_trace_events_publisher().await",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Rust ServiceManager missing trace publisher alias marker %q:\n%s", want, rendered)
		}
	}
}

func TestRustServiceManagerCreatesActiveStateStoreBeforeAutoTLS(t *testing.T) {
	serviceManager, err := loadTemplate("service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.rs.tmpl: %v", err)
	}
	staticIdx := strings.Index(serviceManager, "let state_store_conn = Arc::new(ServiceConnection::new_static(")
	tlsIdx := strings.Index(serviceManager, "tls_cert_stream_loop(")
	if staticIdx < 0 {
		t.Fatalf("Rust ServiceManager must create the static state-store connection before startup RPCs")
	}
	if tlsIdx < 0 {
		t.Fatalf("Rust ServiceManager must start auto-TLS from the state-store connection")
	}
	if staticIdx > tlsIdx {
		t.Fatalf("Rust ServiceManager must create the active state-store connection before auto-TLS startup")
	}

	connectionSrc, err := templates.FS.ReadFile("rust/chassis/servicemanager/src/connection.rs.tmpl")
	if err != nil {
		t.Fatalf("read rust service connection template: %v", err)
	}
	connection := string(connectionSrc)
	for _, want := range []string{
		"// Initial ping to measure latency",
		"Self::do_select_active(&eps, &active_idx).await;",
		"// Start background tasks: ping + rebalance",
	} {
		if !strings.Contains(connection, want) {
			t.Fatalf("Rust ServiceConnection.new_static must establish initial active selection; missing %q", want)
		}
	}
	if strings.Index(connection, "Self::do_select_active(&eps, &active_idx).await;") >
		strings.Index(connection, "// Start background tasks: ping + rebalance") {
		t.Fatalf("Rust ServiceConnection.new_static must select an active endpoint before returning")
	}
}

// H1 / SEC-1: the state-store bootstrap connection carries the workload's
// projected identity token to the token authority, so its verifier must pin the
// SPIFFE trust domain (the base verifier, expected_service: None) rather than
// accept CA-only trust via .chain_only() — otherwise any CA-issued cert could
// impersonate the authority and harvest that token.
func TestRustServiceManagerBootstrapPinsTrustDomain(t *testing.T) {
	serviceManager, err := loadTemplate("service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.rs.tmpl: %v", err)
	}
	staticIdx := strings.Index(serviceManager, "let state_store_conn = Arc::new(ServiceConnection::new_static(")
	if staticIdx < 0 {
		t.Fatalf("Rust ServiceManager must create the static state-store connection")
	}
	assignIdx := strings.LastIndex(serviceManager[:staticIdx], "let state_store_verifier")
	if assignIdx < 0 {
		t.Fatalf("Rust ServiceManager must assign a state_store_verifier before the state-store connection")
	}
	assign := serviceManager[assignIdx:staticIdx]
	if strings.Contains(assign, ".chain_only()") {
		t.Fatalf("state-store bootstrap verifier must not use .chain_only() (CA-only, fail-open):\n%s", assign)
	}
	if !strings.Contains(assign, "spiffe_verifier_base.clone();") {
		t.Fatalf("state-store bootstrap verifier must use the trust-domain-pinning base verifier:\n%s", assign)
	}
}

// SEC: additional_services routes MUST be wrapped by the auth-enforcing tower
// layer before reaching the handler. Without this, handler-provided routes
// accept any-audience tokens (the same hole fixed in go/java by e429f147).
// This test pin-checks:
//  1. The template applies AuthEnforcingLayer to the server before add_routes.
//  2. No code path calls add_routes(additional_routes) without the layer.
func TestRustServiceManagerEnforcesAuthOnAdditionalServices(t *testing.T) {
	tmpl, err := loadTemplate("service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.rs.tmpl: %v", err)
	}

	// The enforcing layer must be applied to the server before additional
	// routes are added (main TLS server path).
	// Match the constructor generically. The assertion here is about ORDERING --
	// the layer must wrap the server before additional routes are added -- not
	// about which constructor is used. Pinning "::new(" made this fail when the
	// layer gained with_exempt_paths() for auth_disabled methods, even though the
	// ordering it guards was unchanged.
	layerIdx := strings.Index(tmpl, "server.layer(AuthEnforcingLayer::")
	if layerIdx < 0 {
		t.Fatalf("Rust ServiceManager must apply AuthEnforcingLayer to the server (not found in template)")
	}

	// Every add_routes(additional_routes) call in the TLS server path must
	// come AFTER the AuthEnforcingLayer application. Find all occurrences
	// and verify ordering.
	addRoutesNeedle := "add_routes(additional_routes)"
	for offset := 0; ; {
		idx := strings.Index(tmpl[offset:], addRoutesNeedle)
		if idx < 0 {
			break
		}
		absIdx := offset + idx
		offset = absIdx + len(addRoutesNeedle)

		// Check if this occurrence is in the main TLS server path
		// (inside HasTlsGrpc block) — that path must have the layer
		// applied before it. The health-only path (else branch, no
		// authorizer in scope) is a separate concern.
		preceding := tmpl[:absIdx]
		if strings.Contains(preceding, "AuthEnforcingLayer") {
			// Layer was applied before this add_routes — correct.
			continue
		}
		// Check if this is in the health-only path (no authorizer).
		// The health-only path is guarded by {{- else}} after HasTlsGrpc.
		if strings.LastIndex(preceding, "{{- else}}") > strings.LastIndex(preceding, "{{- if .HasTlsGrpc}}") {
			// Health-only path — no authorizer available. Separate concern.
			continue
		}
		t.Fatalf("Rust ServiceManager has an unprotected add_routes(additional_routes) call at offset %d — "+
			"AuthEnforcingLayer must be applied to the server before adding handler-provided routes", absIdx)
	}

	// The authorizer template must export the enforcing layer types.
	authTmpl, err := loadAuthorizerTemplate()
	if err != nil {
		t.Fatalf("loadAuthorizerTemplate: %v", err)
	}
	for _, want := range []string{
		"pub struct AuthEnforcingLayer",
		"pub struct AuthEnforcingService",
		"pub fn authenticate(&self",
		"fn call(&mut self",
		"/grpc.health.v1.Health/",
	} {
		if !strings.Contains(authTmpl, want) {
			t.Fatalf("Rust authorizer template must contain %q for the auth-enforcing tower layer", want)
		}
	}
}

func loadAuthorizerTemplate() (string, error) {
	content, err := templates.FS.ReadFile("rust/chassis/grpcserver/src/authorizer.rs.tmpl")
	if err != nil {
		return "", fmt.Errorf("read authorizer template: %w", err)
	}
	return string(content), nil
}

func renderRustServiceManagerForTest(t *testing.T, data smTemplateData) string {
	t.Helper()
	tmplStr, err := loadTemplate("service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	tmpl, err := template.New("servicemanager").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse service_manager template: %v", err)
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute service_manager template: %v", err)
	}
	return out.String()
}

// auth_disabled methods must bypass the server-side AuthEnforcingLayer.
//
// That layer runs BEFORE the per-method wrappers that the chassis comments call
// the authoritative gate, and it rejects any request without an authorization
// header. So an auth_disabled method was honoured by the gateway (which
// generates AuthPolicy::NoAuth and forwards the call unauthenticated) and then
// refused by the service itself with 401. Login could not bootstrap at all,
// because ListProviders, Authorize and MintCallbackTokens must all be callable
// before any token exists.
//
// Caught only by deploying and clicking sign-in; no gate covered it.
func TestRustServiceManagerExemptsAuthDisabledMethods(t *testing.T) {
	tmpl, err := loadTemplate("service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.rs.tmpl: %v", err)
	}
	if !strings.Contains(tmpl, "AuthEnforcingLayer::with_exempt_paths(") {
		t.Error("ServiceManager must build the auth layer with exempt paths; " +
			"AuthEnforcingLayer::new() enforces a token on EVERY method and makes " +
			"auth_disabled unreachable on the server")
	}
	if !strings.Contains(tmpl, "AuthExemptPaths") {
		t.Error("ServiceManager must emit .AuthExemptPaths from the model; without " +
			"it the exempt list renders empty and auth_disabled is silently ignored")
	}
	// The list must come from the model, not be hardcoded to health checks.
	if !strings.Contains(tmpl, "auth_exempt_paths") {
		t.Error("expected an auth_exempt_paths binding passed to the layer")
	}
}

// Item 7 / Piece B: SetEmitter was generated on every wrapper and called by
// nobody, so the emitter slot was None on every request in every deployed
// service and accounting produced neither a durable record nor a log line.
// This asserts the ServiceManager actually constructs the sink and wires it,
// and that it does so with the NON-DROPPING send contract (a Sender the sink
// awaits), not the bounded try-send closure the trace publisher uses.
func TestRustServiceManagerWiresAccountingEmitter(t *testing.T) {
	accounting := publisherEntry{
		FnName:        "create_accountingrecord_accounting_record_bus_stream_publisher",
		LoopFn:        "accountingrecord_accounting_record_bus_stream_publish_loop",
		VarPrefix:     "accountingrecord_accounting_record_bus_stream",
		Label:         "accountingrecord.AccountingRecordBus.stream publisher",
		TargetService: "eventbus",
		EventTypeFQN:  "accountingrecord.AccountingRecord",
		EventTypeName: "AccountingRecord",
		EventTypePath: "crate::types::vservices::accountingrecord::AccountingRecord",
	}
	rendered := renderRustServiceManagerForTest(t, smTemplateData{
		ServiceLabel:          "tasks",
		HasGrpc:               true,
		HasTlsGrpc:            true,
		HasServiceHealthState: true,
		GrpcServices: []grpcServiceEntry{{
			TraitName:  "Tasks",
			WrapperVar: "wrapper_tasks",
		}},
		Publishers:          []publisherEntry{accounting},
		HasPublishers:       true,
		AccountingPublisher: &accounting,
	})

	for _, want := range []string{
		"async fn create_accounting_publisher_sender(&self) -> tokio::sync::mpsc::Sender<AccountingRecord>",
		"let accounting_tx = self.create_accounting_publisher_sender().await;",
		"am::EventBusSink::new(accounting_tx, source_id)",
		"am::Emitter::new(",
		"wrapper_tasks.set_emitter(Some(emitter.clone())).await;",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Rust ServiceManager missing accounting emitter wiring %q:\n%s", want, rendered)
		}
	}

	// The accounting queue must not hand out a try_send closure: dropping an
	// audit record on a full queue is exactly what the item-9 zero-loss ruling
	// forbids for this producer.
	senderFn := rendered[strings.Index(rendered, "async fn create_accounting_publisher_sender"):]
	senderFn = senderFn[:strings.Index(senderFn, "\n    }")]
	if strings.Contains(senderFn, "try_send") {
		t.Fatalf("the accounting publisher must return a blocking Sender, never a try_send closure:\n%s", senderFn)
	}
}

// A proto set with no accounting record (client repos) must render no emitter
// wiring at all rather than naming a type that was never generated.
func TestRustServiceManagerOmitsAccountingEmitterWithoutRecord(t *testing.T) {
	rendered := renderRustServiceManagerForTest(t, smTemplateData{
		ServiceLabel:          "demo",
		HasGrpc:               true,
		HasTlsGrpc:            true,
		HasServiceHealthState: true,
		GrpcServices:          []grpcServiceEntry{{TraitName: "Demo", WrapperVar: "wrapper_demo"}},
	})
	for _, unwanted := range []string{"create_accounting_publisher_sender", "EventBusSink", "set_emitter"} {
		if strings.Contains(rendered, unwanted) {
			t.Fatalf("ServiceManager must not reference %q when the proto set declares no accounting record:\n%s", unwanted, rendered)
		}
	}
}
