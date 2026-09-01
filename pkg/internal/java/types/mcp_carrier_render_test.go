// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"
)

// The generated MCP session carrier is a plain bidi handler-interface method
// over JSON-RPC text frames. It must use the StreamObserver shape the wrapper
// template already imports, so the interface compiles with no new import.
func TestJavaMcpCarrierHandlerMethod(t *testing.T) {
	got := generateJavaMcpCarrierHandlerMethod("Scotty")

	want := "        StreamObserver<String> brokerScottyMcpSession(Caller caller, StreamObserver<String> responseObserver) throws ProtoException;\n"
	if !strings.HasSuffix(got, want) {
		t.Errorf("signature = %q, want it to end with %q", got, want)
	}
	if !strings.Contains(got, "Model Context Protocol") {
		t.Errorf("carrier method has no doc comment explaining it:\n%s", got)
	}
	// The doc comment must close exactly once: a description carrying a stray
	// close would break out of the javadoc it is emitted into.
	if strings.Count(got, "*/") != 1 {
		t.Errorf("doc comment closes %d times, want 1:\n%s", strings.Count(got, "*/"), got)
	}
}
