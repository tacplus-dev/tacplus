// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"
)

func TestValidateVolumeGroups(t *testing.T) {
	skipIfNoProtoc(t)

	cases := []struct {
		name       string
		body       string
		wantErr    bool
		errSubstrs []string
	}{
		{
			name: "no volume group is fine",
			body: `
service Plain { rpc Get(Ping) returns (Ping); }
`,
		},
		{
			name: "one volume group is fine",
			body: `
service Docs {
  option (spec.volume_group) = {
    name: "documents"
    max_blob_bytes: 52428800
    content_types: ["application/pdf", "image/jpeg", "text/plain; charset=utf-8"]
    tenant_quota_bytes: 53687091200
  };
  rpc Get(Ping) returns (Ping);
}
`,
		},
		{
			name: "two services with distinct names is fine",
			body: `
service Docs { option (spec.volume_group) = {name: "documents"}; rpc Get(Ping) returns (Ping); }
service Media { option (spec.volume_group) = {name: "photos"}; rpc Get(Ping) returns (Ping); }
`,
		},
		{
			name: "duplicate name across two services is refused",
			body: `
service Docs { option (spec.volume_group) = {name: "documents"}; rpc Get(Ping) returns (Ping); }
service Archive { option (spec.volume_group) = {name: "documents"}; rpc Get(Ping) returns (Ping); }
`,
			wantErr:    true,
			errSubstrs: []string{`duplicate volume group name "documents"`, "vgtest.Docs", "vgtest.Archive"},
		},
		{
			name: "duplicate name on one service is refused",
			body: `
service Docs {
  option (spec.volume_group) = {name: "documents"};
  option (spec.volume_group) = {name: "documents"};
  rpc Get(Ping) returns (Ping);
}
`,
			wantErr:    true,
			errSubstrs: []string{"duplicate volume group name"},
		},
		{
			name: "malformed content type is refused",
			body: `
service Docs {
  option (spec.volume_group) = {name: "documents" content_types: ["not a mime type!!"]};
  rpc Get(Ping) returns (Ping);
}
`,
			wantErr:    true,
			errSubstrs: []string{`content_types entry "not a mime type!!"`, "well-formed media type"},
		},
		{
			name: "empty group name is refused",
			body: `
service Docs { option (spec.volume_group) = {max_blob_bytes: 1024}; rpc Get(Ping) returns (Ping); }
`,
			wantErr:    true,
			errSubstrs: []string{"empty name"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := volumeGroupParser(t, tc.body)
			err := ValidateVolumeGroups(p)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("ValidateVolumeGroups: unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("ValidateVolumeGroups: expected error, got nil")
			}
			for _, want := range tc.errSubstrs {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("ValidateVolumeGroups error %q does not contain %q", err.Error(), want)
				}
			}
		})
	}
}

// The collision is fleet-wide, not per-file: two services in two different
// protos are exactly the case a single-file check would miss, and the error has
// to name both files so the operator knows which two declarations to reconcile.
func TestValidateVolumeGroups_DuplicateAcrossFiles(t *testing.T) {
	skipIfNoProtoc(t)

	p := volumeGroupParserFiles(t, map[string]string{
		"docmanager.proto": `
service Docs { option (spec.volume_group) = {name: "documents"}; rpc Get(Ping) returns (Ping); }
`,
		"archiver.proto": `
service Archive { option (spec.volume_group) = {name: "documents"}; rpc Get(Ping) returns (Ping); }
`,
	})

	err := ValidateVolumeGroups(p)
	if err == nil {
		t.Fatalf("ValidateVolumeGroups: expected a fleet-wide duplicate error, got nil")
	}
	for _, want := range []string{
		`duplicate volume group name "documents"`,
		"docmanager.Docs", "docmanager.proto",
		"archiver.Archive", "archiver.proto",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ValidateVolumeGroups error %q does not contain %q", err.Error(), want)
		}
	}
}
