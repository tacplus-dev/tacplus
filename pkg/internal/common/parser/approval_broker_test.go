// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"
)

// Pins (spec.approval_broker) discovery (#161). The approval broker the
// generated (spec.rpc).requires_device check calls used to be looked for only
// on the authoritative (spec.token_minter). That minter is now always the sys
// minter.Minter, which has no broker rpc and must not grow one, so the token
// authority declares the broker on a policy service of its own with an explicit
// annotation. Exactly one per run; never inferred from a signature alone.

const brokerMessages = `
message BrokerPendingApprovalRequest { string approval_id = 1; }
message BrokerPendingApprovalResponse { string person_id = 1; }
`

func TestDiscoverApprovalBroker_AnnotatedServiceAnywhere(t *testing.T) {
	skipIfNoProtoc(t)

	// The authoritative minter carries no broker rpc; the broker lives on a
	// separate service in the proto set, the way the token authority's policy
	// service carries it next to the sys minter.Minter.
	dir := tokenMinterDir(t, brokerMessages+`
service AuthMint {
  option (spec.service_s2s) = {};
  option (spec.token_minter) = {};
  rpc Mint(Ping) returns (Ping);
}

service Policy {
  option (spec.service_s2s) = {};
  option (spec.approval_broker) = {};
  rpc Other(Ping) returns (Ping);
  rpc Consume(BrokerPendingApprovalRequest) returns (BrokerPendingApprovalResponse);
}
`)

	broker, err := DiscoverApprovalBroker(dir)
	if err != nil {
		t.Fatalf("DiscoverApprovalBroker: %v", err)
	}
	if broker == nil {
		t.Fatal("expected the (spec.approval_broker) service, got nil")
	}
	want := ApprovalBrokerInfo{Package: "minttest", Dir: "minttest", ServiceName: "Policy", MethodName: "Consume"}
	if *broker != want {
		t.Errorf("broker = %+v, want %+v", *broker, want)
	}
}

func TestDiscoverApprovalBroker_SignatureAloneIsNotABroker(t *testing.T) {
	skipIfNoProtoc(t)

	// A broker-shaped rpc on the authoritative minter, without the annotation,
	// is no longer discovered: the broker is declared, not inferred.
	dir := tokenMinterDir(t, brokerMessages+`
service AuthMint {
  option (spec.service_s2s) = {};
  option (spec.token_minter) = {};
  rpc Mint(Ping) returns (Ping);
  rpc BrokerPendingApproval(BrokerPendingApprovalRequest) returns (BrokerPendingApprovalResponse);
}
`)

	broker, err := DiscoverApprovalBroker(dir)
	if err != nil {
		t.Fatalf("DiscoverApprovalBroker: %v", err)
	}
	if broker != nil {
		t.Errorf("broker = %+v, want nil (no (spec.approval_broker) declared)", *broker)
	}
}

func TestDiscoverApprovalBroker_Refusals(t *testing.T) {
	skipIfNoProtoc(t)

	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "two brokers",
			body: `
service PolicyA {
  option (spec.service_s2s) = {};
  option (spec.approval_broker) = {};
  rpc Consume(BrokerPendingApprovalRequest) returns (BrokerPendingApprovalResponse);
}
service PolicyB {
  option (spec.service_s2s) = {};
  option (spec.approval_broker) = {};
  rpc Consume(BrokerPendingApprovalRequest) returns (BrokerPendingApprovalResponse);
}
`,
			want: "2 (spec.approval_broker) services declared [minttest.PolicyA, minttest.PolicyB]",
		},
		{
			name: "no broker rpc",
			body: `
service Policy {
  option (spec.service_s2s) = {};
  option (spec.approval_broker) = {};
  rpc Other(Ping) returns (Ping);
}
`,
			want: "declares no rpc taking BrokerPendingApprovalRequest and returning BrokerPendingApprovalResponse",
		},
		{
			name: "two broker rpcs",
			body: `
service Policy {
  option (spec.service_s2s) = {};
  option (spec.approval_broker) = {};
  rpc ConsumeA(BrokerPendingApprovalRequest) returns (BrokerPendingApprovalResponse);
  rpc ConsumeB(BrokerPendingApprovalRequest) returns (BrokerPendingApprovalResponse);
}
`,
			want: "declares 2 rpcs taking BrokerPendingApprovalRequest",
		},
		{
			name: "not s2s",
			body: `
service Policy {
  option (spec.service_c2s) = {};
  option (spec.approval_broker) = {};
  rpc Consume(BrokerPendingApprovalRequest) returns (BrokerPendingApprovalResponse);
}
`,
			want: "is not a (spec.service_s2s) service",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := tokenMinterDir(t, brokerMessages+tc.body)
			broker, err := DiscoverApprovalBroker(dir)
			if err == nil {
				t.Fatalf("DiscoverApprovalBroker = %+v, want an error containing %q", broker, tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error = %q, want it to contain %q", err, tc.want)
			}

			// The fleet build runs the same check over the merged tree.
			dp, perr := NewDescriptorParser(dir)
			if perr != nil {
				t.Fatalf("NewDescriptorParser: %v", perr)
			}
			if verr := ValidateApprovalBroker(dp); verr == nil || !strings.Contains(verr.Error(), tc.want) {
				t.Errorf("ValidateApprovalBroker = %v, want an error containing %q", verr, tc.want)
			}
		})
	}
}
