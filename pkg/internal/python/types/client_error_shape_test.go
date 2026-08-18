// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"
)

// PAR-9: the generated python clients were the only ones that reported an
// unauthenticable outbound call as a language-generic error — bare RuntimeError
// in the gRPC client, PermissionError in the W2S client — so a caller could not
// distinguish "no credential" from any other failure. go emits
// codes.Unauthenticated, java ProtoException.newErrUnauthorized, rust
// tonic::Status::unauthenticated and typescript grpc.status.UNAUTHENTICATED at
// the same seam. These pin the python side to the shared ProtoError shape.
func TestPythonGrpcClientRaisesStructuredUnauthenticated(t *testing.T) {
	tmpl := readPythonTemplate(t, "python/types/grpcclient.py.tmpl")

	for _, want := range []string{
		"from {{.ExceptionsImportFrom}}.exceptions import unauthenticated",
		`raise unauthenticated("auth token provider is required")`,
		`raise unauthenticated(f"auth token: {cause}") from cause`,
		`raise unauthenticated("auth token is empty")`,
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("grpcclient template missing %q", want)
		}
	}
	if strings.Contains(tmpl, "raise RuntimeError(") {
		t.Errorf("grpcclient template must not raise bare RuntimeError; use the exceptions helpers")
	}
}

func TestPythonW2SClientRaisesStructuredErrors(t *testing.T) {
	tmpl := readPythonTemplate(t, "python/types/w2sclient.py.tmpl")

	for _, want := range []string{
		"from {{.ExceptionsImportFrom}}.exceptions import from_http_status, internal, unauthenticated",
		`raise unauthenticated("W2S route requires a token provider")`,
		`raise unauthenticated("W2S token provider returned an empty token")`,
		// The W2S error envelope carries no code, so the HTTP status is the
		// structured signal: 401/403 must map to UNAUTHENTICATED.
		"self._raise_json_error(value, response.status_code)",
		"raise from_http_status(",
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("w2sclient template missing %q", want)
		}
	}
	if strings.Contains(tmpl, "raise PermissionError(") {
		t.Errorf("w2sclient template must not raise PermissionError at the auth seam")
	}
}

// The exceptions module the clients import must actually export the helpers
// they name, and must be emitted at the path the import prefix resolves to.
func TestPythonExceptionsModuleExportsClientHelpers(t *testing.T) {
	tmpl := readPythonTemplate(t, "python/types/exceptions.py.tmpl")
	for _, want := range []string{
		"def unauthenticated(message: str) -> ProtoError:",
		"def internal(message: str) -> ProtoError:",
		"def from_http_status(status_code: int, message: str = None) -> ProtoError:",
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("exceptions template missing %q", want)
		}
	}
	if got := pyExceptionsImportFrom("....."); got != ".....types.sys.errors" {
		t.Errorf("pyExceptionsImportFrom = %q, want %q", got, ".....types.sys.errors")
	}
}
