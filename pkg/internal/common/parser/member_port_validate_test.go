// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"
)

func TestParseServiceOptions_MemberPort(t *testing.T) {
	skipIfNoProtoc(t)

	p := volumeGroupParser(t, `
service Grid { option (spec.member_port) = {}; rpc Get(Ping) returns (Ping); }
service Plain { rpc Get(Ping) returns (Ping); }
`)
	got := map[string]bool{}
	for _, fd := range p.GetAllFiles() {
		for _, svc := range fd.Services {
			got[svc.Name] = svc.IsMemberPort
		}
	}
	if !got["Grid"] {
		t.Fatalf("Grid declares (spec.member_port) but IsMemberPort is false")
	}
	if got["Plain"] {
		t.Fatalf("Plain declares no (spec.member_port) but IsMemberPort is true")
	}
}

func TestValidateMemberPort(t *testing.T) {
	skipIfNoProtoc(t)

	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name: "member port alone is fine",
			body: `service Grid { option (spec.member_port) = {}; rpc Get(Ping) returns (Ping); }`,
		},
		{
			name: "smb alone is still accepted (deprecated)",
			body: `service Grid { option (spec.smb) = {}; rpc Get(Ping) returns (Ping); }`,
		},
		{
			name:    "member port and smb on one service are refused",
			body:    `service Grid { option (spec.member_port) = {}; option (spec.smb) = {}; rpc Get(Ping) returns (Ping); }`,
			wantErr: "mptest.Grid declares both (spec.member_port) and the deprecated (spec.smb)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := volumeGroupParserFiles(t, map[string]string{"mptest.proto": tc.body})
			err := ValidateMemberPort(p)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("ValidateMemberPort: unexpected error: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("ValidateMemberPort error = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestRefuseMemberPort(t *testing.T) {
	skipIfNoProtoc(t)

	p := volumeGroupParserFiles(t, map[string]string{
		"grid.proto":  `service Grid { option (spec.member_port) = {}; rpc Get(Ping) returns (Ping); }`,
		"plain.proto": `service Plain { rpc Get(Ping) returns (Ping); }`,
	})
	if err := RefuseMemberPort(p, "plain", "go", "tacplus#236"); err != nil {
		t.Fatalf("a service without (spec.member_port) is refused: %v", err)
	}
	err := RefuseMemberPort(p, "grid", "go", "tacplus#236")
	if err == nil || !strings.Contains(err.Error(), "grid.Grid declares (spec.member_port)") || !strings.Contains(err.Error(), "tacplus#236") {
		t.Fatalf("RefuseMemberPort error = %v, want the service and the follow-up named", err)
	}
}
