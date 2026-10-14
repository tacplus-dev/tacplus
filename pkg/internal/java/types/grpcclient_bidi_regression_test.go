// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

func TestJavaGrpcBidiAuthenticatesAtStreamOpenOnly(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "ContractApi"}
	method := &parser.MethodInfo{
		Name:            "Chat",
		InputType:       "ContractRequest",
		OutputType:      "ContractResponse",
		FullInputType:   "contract.ContractRequest",
		FullOutputType:  "contract.ContractResponse",
		ClientStreaming: true,
		ServerStreaming: true,
	}

	got := generateJavaGrpcClientMethod(
		svc,
		method,
		"ContractOuterClass.ContractRequest",
		"ContractOuterClass.ContractResponse",
		"",
		"",
		nil,
	)

	for _, want := range []string{
		"var stub = asyncStub;",
		"headers.put(AUTH_KEY, \"Bearer \" + token);",
		"stub = stub.withInterceptors(MetadataUtils.newAttachHeadersInterceptor(headers));",
		"StreamObserver<ContractOuterClass.ContractRequest> delegate = stub.chat(responseObserver);",
		"delegate.onNext(request);",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated Java bidi client missing %q:\n%s", want, got)
		}
	}

	if count := strings.Count(got, "tokenProvider.get()"); count != 1 {
		t.Fatalf("Java bidi client must obtain one token for stream-open auth, got %d calls:\n%s", count, got)
	}
	for _, bad := range []string{
		"freshToken",
		"auth token provider is required\");\n            }\n            String freshToken",
	} {
		if strings.Contains(got, bad) {
			t.Fatalf("Java bidi onNext must not perform per-message auth preflight; found %q:\n%s", bad, got)
		}
	}
}
