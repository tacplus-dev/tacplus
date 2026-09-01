// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"
)

const wantCarrierStream = "std::pin::Pin<Box<dyn tokio_stream::Stream<Item = Result<String, tonic::Status>> + Send>>"

// The generated MCP session carrier is a plain bidi handler-trait method over
// JSON-RPC text frames — the same stream shape every other bidi handler method
// uses, with String in place of a prost message.
func TestRustMcpCarrierHandlerMethod(t *testing.T) {
	got := generateRustMcpCarrierHandlerMethod("Scotty")

	want := "    async fn broker_scotty_mcp_session(&self, caller: Option<&servicemanager::auth::Caller>, req: tonic::Request<" +
		wantCarrierStream + ">) -> Result<tonic::Response<" + wantCarrierStream + ">, tonic::Status>;"
	if !strings.HasSuffix(got, want) {
		t.Errorf("signature = %q,\nwant it to end with %q", got, want)
	}
	if !strings.Contains(got, "/// ") {
		t.Errorf("carrier method has no doc comment:\n%s", got)
	}
}

// The stub impl must stay total, so it supplies the same Unimplemented body
// every other stub method has.
func TestRustMcpCarrierStubMethod(t *testing.T) {
	got := generateRustMcpCarrierStubMethod("Scotty")

	if !strings.HasPrefix(got, "    async fn broker_scotty_mcp_session(&self, _caller:") {
		t.Errorf("stub signature = %q", got)
	}
	if !strings.Contains(got, `Err(tonic::Status::unimplemented("Scotty.BrokerScottyMcpSession: handler not implemented"))`) {
		t.Errorf("stub body does not return Unimplemented:\n%s", got)
	}
}
