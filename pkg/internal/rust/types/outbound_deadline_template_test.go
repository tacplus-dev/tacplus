// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// A Rust handler that calls a peer used to overwrite the caller's grpc-timeout
// with the client's own fixed 30 s, so a hop widened its caller's budget and
// kept working after the caller gave up (issue #38). Go derives the outbound
// deadline from the handler's context. Rust now reads the inbound budget from
// servicemanager::deadline, which every gRPC server registration wraps, and
// clamps each outbound attempt to it. The cross-test/outbound-deadline gate
// checks the wire budget end to end.

func TestRustOutboundTimeoutClampsToInboundDeadline(t *testing.T) {
	tmpl, err := loadTemplate("grpcclient.rs.tmpl")
	if err != nil {
		t.Fatalf("load grpcclient template: %v", err)
	}
	for _, want := range []string{
		"fn outbound_timeout(&self) -> Option<Duration> {",
		"servicemanager::deadline::remaining()",
		"(Some(own), Some(left)) => Some(own.min(left)),",
		"if let Some(timeout) = self.outbound_timeout() {",
		"match self.outbound_timeout() {",
	} {
		if !strings.Contains(tmpl, want) {
			t.Fatalf("grpc client template missing %q", want)
		}
	}
	if strings.Contains(tmpl, "timeout(self.timeout, op())") {
		t.Fatalf("unary retry must bound each attempt by the clamped outbound timeout, not the client's own timeout")
	}
}

func TestRustOutboundCallSitesApplyClampedTimeout(t *testing.T) {
	method := &parser.MethodInfo{
		Name:           "GetPublicContract",
		InputType:      "ContractRequest",
		OutputType:     "ContractResponse",
		FullInputType:  "contract.ContractRequest",
		FullOutputType: "contract.ContractResponse",
	}
	res, err := generateRustGrpcClientMethod(
		&parser.ServiceInfo{Name: "ContractApi", IsS2S: true},
		method,
		"contract",
		map[string]string{},
	)
	if err != nil {
		t.Fatalf("client gen error: %v", err)
	}
	if !strings.Contains(res.method, "self.apply_outbound_timeout(&mut req);") {
		t.Fatalf("unary outbound call must apply the clamped timeout:\n%s", res.method)
	}
	if !res.usesOutboundTimeout {
		t.Fatalf("unary outbound call must mark the client as using the outbound timeout, or the clamp is not emitted")
	}
}

func TestRustServiceManagerRegistersGrpcServersWithInboundDeadline(t *testing.T) {
	rendered := renderRustServiceManagerForTest(t, smTemplateData{
		ServiceLabel:        "demo",
		StateStoreServiceID: "statestore",
		HasStates:           true,
		HasTlsGrpc:          true,
		HasS2SGrpc:          true,
		GrpcServices: []grpcServiceEntry{{
			TraitName:                "DemoApi",
			HandleConfigName:         "demo_config",
			ConfigModPath:            "crate::types::services::demo::demo",
			ConfigStateGroupAccessor: "services",
			ConfigStateAccessor:      "demo_config_state",
			HasConfigState:           true,
			ServerType:               "DemoApiServer",
			ServerModName:            "demo_api_server",
			TypesBasePath:            "crate::types::services::demo",
			WrapperVar:               "wrapper_demo",
		}},
	})
	want := "servicemanager::deadline::InboundDeadline::new(\n                    crate::types::services::demo::demo_api_server::DemoApiServer::new("
	if !strings.Contains(rendered, want) {
		t.Fatalf("gRPC server registration must wrap the generated server in InboundDeadline:\n%s", rendered)
	}
}
