// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

func TestRustGrpcBidiOnConnectUsesStreamLocalHandle(t *testing.T) {
	method := &parser.MethodInfo{
		Name:            "BrokerStream",
		InputType:       "CliTokenBrokerRequest",
		OutputType:      "CliTokenBrokerResponse",
		FullInputType:   "kernel.CliTokenBrokerRequest",
		FullOutputType:  "kernel.CliTokenBrokerResponse",
		ClientStreaming: true,
		ServerStreaming: true,
	}

	res, err := generateRustGrpcClientMethod(
		&parser.ServiceInfo{Name: "CliTokenBroker", IsS2S: true},
		method,
		"kernel",
		map[string]string{},
	)
	if err != nil {
		t.Fatalf("client gen error: %v", err)
	}

	got := res.method
	for _, want := range []string{
		"let connect_handle = BrokerStreamBidiHandle {",
		"tx: stream_tx.clone(),",
		"on_connect(connect_handle).await;",
		"req.metadata_mut().insert(\"authorization\", val);",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated bidi client missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "on_connect(task_handle.clone()).await") {
		t.Fatalf("on_connect must not send through the outer user queue before the stream is open:\n%s", got)
	}

	if strings.Contains(got, "on_connect_token_provider") {
		t.Fatalf("on_connect handle must not fetch auth tokens per message:\n%s", got)
	}

	extra := res.extraType
	for _, bad := range []string{
		"token_provider: Option<TokenProvider>",
		"auth_required: bool",
		"auth token provider is required",
		"let token = (tp)().await",
	} {
		if strings.Contains(extra, bad) {
			t.Fatalf("bidi send handle must only validate and enqueue messages; found %q:\n%s", bad, extra)
		}
	}
}
