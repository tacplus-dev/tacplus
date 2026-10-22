// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

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

// Same defect class as PAR-9, at the two python auth seams PAR-9 did not
// reach. The state client raised a bare RuntimeError on an empty token and let
// a provider exception escape unwrapped; the event-bus subscriber had no
// empty-token check at all and attached a literal `Bearer ` header, pushing the
// diagnosis onto the broker. Both now raise the shared ProtoError helpers.
func TestPythonStateClientRaisesStructuredUnauthenticated(t *testing.T) {
	rendered := renderPyStateWrapperForTest(t, pyStateWrapperData{
		Header:               "# generated for test",
		PackageName:          "demo",
		PbImportFrom:         "......types.services.demo",
		TraceImportFrom:      "......types.sys.trace",
		ExceptionsImportFrom: pyExceptionsImportFrom("......"),
		StateRootName:        "DemoState",
		StateServiceName:     "DemoStateService",
		Singletons: []pySingletonBoundary{{
			FieldName:   "config",
			FieldPascal: "Config",
			TypeName:    "Config",
		}},
	})

	for _, want := range []string{
		"from ......types.sys.errors.exceptions import CODE_FAILED_PRECONDITION, ProtoError, unauthenticated",
		`raise unauthenticated(f"auth token: {cause}") from cause`,
		`raise unauthenticated("auth token is empty")`,
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered python state client missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, `raise RuntimeError("token provider returned an empty token")`) {
		t.Errorf("python state client auth seam still raises a bare RuntimeError")
	}
}

// The auth seam only exists when the state root has watches, so the exceptions
// import must be emitted under the same condition — an unconditional import
// would name a module the watchless wrapper never uses.
func TestPythonStateClientWithoutWatchesOmitsExceptionsImport(t *testing.T) {
	rendered := renderPyStateWrapperForTest(t, pyStateWrapperData{
		Header:               "# generated for test",
		PackageName:          "demo",
		PbImportFrom:         "......types.services.demo",
		TraceImportFrom:      "......types.sys.trace",
		ExceptionsImportFrom: pyExceptionsImportFrom("......"),
		StateRootName:        "DemoState",
		StateServiceName:     "DemoStateService",
	})

	if strings.Contains(rendered, "exceptions import unauthenticated") {
		t.Errorf("watchless python state client must not import the exceptions module:\n%s", rendered)
	}
	if strings.Contains(rendered, "async def _metadata") {
		t.Errorf("watchless python state client must not emit an auth seam:\n%s", rendered)
	}
}

// subscriber.py.tmpl is PARKED, not live: generatePySubscribers has no call
// site and generator.go:812-815 records why (client-side subscription support
// needs a subscription-only ServiceManager). It is kept correct and pinned the
// same way the PC-C-5 envelope machinery in the same template is — this test is
// the pin, deliberately NOT a soundness row, which would probe a dead template.
func TestPythonSubscriberRaisesStructuredUnauthenticated(t *testing.T) {
	tmpl := readPythonTemplate(t, "python/types/subscriber.py.tmpl")

	for _, want := range []string{
		"from {{.ExceptionsImportFrom}}.exceptions import unauthenticated",
		`raise unauthenticated(f"auth token: {cause}") from cause`,
		`raise unauthenticated("auth token is empty")`,
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("subscriber template missing %q", want)
		}
	}
	// The pre-fix seam attached the header with no guard at all.
	if strings.Contains(tmpl, "            token = await self._token_provider()\n            metadata =") {
		t.Errorf("subscriber attaches a bearer header without validating the token")
	}
	if got := pyExceptionsImportFrom("svc_tacplus."); got != "svc_tacplus.types.sys.errors" {
		t.Errorf("pyExceptionsImportFrom(absolute base) = %q, want %q", got, "svc_tacplus.types.sys.errors")
	}
}
