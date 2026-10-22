// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

// Same class as PAR-9, at the typescript state-client seam. grpcclient.ts.tmpl
// already stamps grpc.status.UNAUTHENTICATED on the error it throws when a
// token cannot be obtained; the state client threw a bare Error with no code,
// so a caller could not tell "no credential" from any other stream failure.
func TestTypeScriptStateClientThrowsUnauthenticatedCode(t *testing.T) {
	rendered := renderTSStateWrapperForTest(t, tsStateWrapperData{
		Header:               "// generated for test",
		TypesImport:          "../../types/services/demo/index.js",
		TraceImport:          "../../../types/sys/trace/index.js",
		ServiceManagerImport: "../../../chassis/servicemanager/index.js",
		ProtoPackage:         "demo",
		StateRootName:        "DemoState",
		StateServiceName:     "DemoStateService",
		Singletons: []tsSingletonBoundary{{
			FieldName:   "config",
			FieldPascal: "Config",
			TypeName:    "Config",
			TypePrefix:  "types",
		}},
	})

	for _, want := range []string{
		"err.code = grpc.status.UNAUTHENTICATED;",
		"function unauthenticatedError(message: string): Error & { code?: number } {",
		"throw unauthenticatedError('auth token is empty');",
		"throw unauthenticatedError(`auth token: ${cause instanceof Error ? cause.message : String(cause)}`);",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("rendered typescript state client missing %q:\n%s", want, rendered)
		}
	}
	if strings.Contains(rendered, "throw new Error('token provider returned an empty token')") {
		t.Errorf("typescript state client auth seam still throws an uncoded Error")
	}
}

// The helper lives inside the watches guard alongside its only caller and the
// grpc import it needs, so a watchless state root must not emit it.
func TestTypeScriptStateClientWithoutWatchesOmitsAuthHelper(t *testing.T) {
	rendered := renderTSStateWrapperForTest(t, tsStateWrapperData{
		Header:               "// generated for test",
		TypesImport:          "../../types/services/demo/index.js",
		TraceImport:          "../../../types/sys/trace/index.js",
		ServiceManagerImport: "../../../chassis/servicemanager/index.js",
		ProtoPackage:         "demo",
		StateRootName:        "DemoState",
		StateServiceName:     "DemoStateService",
	})

	if strings.Contains(rendered, "function unauthenticatedError") {
		t.Errorf("watchless typescript state client must not emit the auth helper:\n%s", rendered)
	}
	if strings.Contains(rendered, "import * as grpc from '@grpc/grpc-js';") {
		t.Errorf("watchless typescript state client must not import grpc:\n%s", rendered)
	}
}
