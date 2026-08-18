// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/templates"
)

// PC-L-4: the rust auto-TLS ready channel used to be a bare
// oneshot::Sender<()>, so the only thing startup could learn was "no cert
// arrived" — the give-up reason and a handler that rejected the first cert were
// both swallowed into a log line. Go reports the cause on readyCh
// (errTlsFirstCertFatal / "TLS cert stream failed before first delivery after N
// attempts: %w") and rust's own SMB stream already used
// oneshot::Sender<Result<(), String>>; the TLS stream was the outlier.
//
// These are template-text pins. The behavioural half lives in
// cross-test/cert-stream/testdata/cert_stream_behavior_test.rs
// (rust_tls_first_handler_failure_is_fatal), which drives the real generated
// stream against a fixture authority.
func TestRustTlsReadyChannelCarriesFirstDeliveryOutcome(t *testing.T) {
	rendered := readRustServiceManagerTemplate(t)

	if strings.Contains(rendered, "oneshot::channel::<()>()") {
		t.Fatalf("the auto-TLS ready channel must carry Result<(), String>, not a bare (): a give-up or a rejected first cert has to name its cause to startup")
	}
	for _, want := range []string{
		"let (tls_ready_tx, tls_ready_rx) = tokio::sync::oneshot::channel::<Result<(), String>>();",
		"Ok(Err(e)) => return Err(format!(\"first TLS certificate delivery: {}\", e).into()),",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rust service_manager must wire the TLS ready outcome into startup; missing:\n%s", want)
		}
	}
}

// A handler that rejects the FIRST delivered certificate must fail startup with
// that error, not spin the reconnect loop: the service cannot serve without a
// cert, so retrying only delays the diagnosis. Parity with go
// (errTlsFirstCertFatal) and with rust's own smb_cert_stream_loop.
func TestRustTlsFirstDeliveryHandlerRejectionIsFatal(t *testing.T) {
	rendered := readRustServiceManagerTemplate(t)

	for _, want := range []string{
		"\"handler new_tls_cert failed on first delivery: {}\",",
		"if waiting_for_first && !delivered && ready_tx.is_none() {",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rust TLS stream must treat a rejected FIRST cert delivery as fatal; missing:\n%s", want)
		}
	}
}

// The give-up path must hand startup the last transport/protocol error, not a
// bare attempt count — that string is the only thing an operator sees when a
// deployment never gets a cert.
func TestRustTlsGiveUpReportsCause(t *testing.T) {
	rendered := readRustServiceManagerTemplate(t)

	const want = "\"TLS cert stream failed before first delivery after {} attempts: {}\","
	if !strings.Contains(rendered, want) {
		t.Fatalf("rust TLS give-up must include the underlying reason; missing:\n%s", want)
	}
	if !strings.Contains(rendered, "let _ = tx.send(Err(give_up));") {
		t.Fatalf("rust TLS give-up must send its reason on the ready channel, not only log + degrade")
	}
}

func readRustServiceManagerTemplate(t *testing.T) string {
	t.Helper()
	src, err := templates.FS.ReadFile("rust/types/service_manager.rs.tmpl")
	if err != nil {
		t.Fatalf("read rust service_manager template: %v", err)
	}
	return string(src)
}
