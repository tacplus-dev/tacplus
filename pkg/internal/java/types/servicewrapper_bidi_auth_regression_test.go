// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
)

func TestJavaBidiWrapperAuthorizesStreamOpenFailClosed(t *testing.T) {
	method := &parser.MethodInfo{
		Name:            "Chat",
		InputType:       "ContractRequest",
		OutputType:      "ContractResponse",
		FullInputType:   "contract.ContractRequest",
		FullOutputType:  "contract.ContractResponse",
		ClientStreaming: true,
		ServerStreaming: true,
	}

	rendered := generateJavaGRPCWrapper(
		method,
		"ContractApi",
		"contract",
		"ContractOuterClass.ContractRequest",
		"ContractOuterClass.ContractResponse",
		"Helpers",
		"Helpers",
		false,
		false,
		nil,
		"contract",
		"contract",
		nil,
		proto.SkippedTraceTypes{},
		nil,
		nil,
		nil,
	)

	authIdx := strings.Index(rendered, `checkAuth("ContractApi/Chat", null);`)
	handlerIdx := strings.Index(rendered, "handler.chat(")
	if authIdx < 0 {
		t.Fatalf("Java bidi wrapper missing stream-open checkAuth:\n%s", rendered)
	}
	if handlerIdx < 0 {
		t.Fatalf("Java bidi wrapper missing handler dispatch:\n%s", rendered)
	}
	if authIdx > handlerIdx {
		t.Fatalf("Java bidi wrapper must authorize before handler dispatch:\n%s", rendered)
	}

	for _, want := range []string{
		"responseObserver.onError(toGrpcError(e));",
		"return new StreamObserver<ContractOuterClass.ContractRequest>()",
		"@Override public void onNext(ContractOuterClass.ContractRequest value) {}",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("Java bidi wrapper auth failure must fail closed with noop observer; missing %q:\n%s", want, rendered)
		}
	}
}
