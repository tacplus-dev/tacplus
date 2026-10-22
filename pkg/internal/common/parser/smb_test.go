// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
)

// Pins SMB (SPIFFE plan D7) service-option extraction and the authority gate:
// `(spec.smb)` lands as ServiceDescriptor.IsSMB, `(spec.smb_authority)` lands as
// IsSMBAuthority, SmbRoot carries the selected trust profile, and
// ValidateSmbAuthority requires exactly one authority once any SMB service
// exists. These are what the per-SMB CA wiring and soundness checks key off, so
// a regression here would silently un-mark SMB services or let an SMB service
// generate with no authority to issue its CA.

// copySpecProto copies the real sys/spec.proto into <dir>/sys/spec.proto so a
// temp proto can `import "sys/spec.proto"` and use spec annotations under
// protoc. spec.proto only imports the well-known descriptor.proto, so no other
// files are needed.
func copySpecProto(t *testing.T, dir string) {
	t.Helper()
	// Test CWD is the package dir (pkg/internal/common/parser); the proto lib
	// lives at pkg/proto/sys/spec.proto, three levels up.
	src := filepath.Join("..", "..", "..", "proto", "sys", "spec.proto")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read spec.proto (%s): %v", src, err)
	}
	sysDir := filepath.Join(dir, "sys")
	if err := os.MkdirAll(sysDir, 0o755); err != nil {
		t.Fatalf("mkdir sys: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sysDir, "spec.proto"), data, 0o644); err != nil {
		t.Fatalf("write spec.proto: %v", err)
	}
}

// smbParser writes a proto using spec annotations into a temp dir (alongside a
// copy of spec.proto) and returns a DescriptorParser over it.
func smbParser(t *testing.T, protoBody string) *DescriptorParser {
	t.Helper()
	dir := t.TempDir()
	copySpecProto(t, dir)
	content := `syntax = "proto3";

package smbtest;

import "sys/spec.proto";

message Ping { string m = 1; }
` + protoBody
	writeTempProtoFile(t, dir, "smbtest.proto", content)
	p, err := NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	return p
}

func TestParseServiceOptions_SMB(t *testing.T) {
	skipIfNoProtoc(t)

	p := smbParser(t, `
// Cache fronts a black-box peer protocol among its own instances.
service Cache {
  option (spec.smb) = {};
  rpc Get(Ping) returns (Ping);
}

// KernelCache uses a cluster-rooted per-SMB intermediate.
service KernelCache {
  option (spec.smb) = {root: SMB_ROOT_CLUSTER};
  rpc Get(Ping) returns (Ping);
}

// Authority issues per-SMB CAs + member certs.
service Authority {
  option (spec.smb_authority) = {};
  rpc Get(Ping) returns (Ping);
}

// Plain is an ordinary service with no SMB boundary.
service Plain {
  rpc Get(Ping) returns (Ping);
}
`)

	fd, err := p.GetFileDescriptor("smbtest.proto")
	if err != nil {
		t.Fatalf("GetFileDescriptor: %v", err)
	}

	smb := map[string]bool{}
	roots := map[string]spec.SmbRoot{}
	auth := map[string]bool{}
	for _, svc := range fd.Services {
		smb[svc.Name] = svc.IsSMB
		roots[svc.Name] = svc.SmbRoot
		auth[svc.Name] = svc.IsSMBAuthority
	}

	if !smb["Cache"] {
		t.Errorf("Cache: IsSMB = false, want true (option (spec.smb) = {})")
	}
	if roots["Cache"] != spec.SmbRoot_SMB_ROOT_ISOLATED {
		t.Errorf("Cache: SmbRoot = %s, want %s", roots["Cache"], spec.SmbRoot_SMB_ROOT_ISOLATED)
	}
	if !smb["KernelCache"] {
		t.Errorf("KernelCache: IsSMB = false, want true (option (spec.smb) = {root: SMB_ROOT_CLUSTER})")
	}
	if roots["KernelCache"] != spec.SmbRoot_SMB_ROOT_CLUSTER {
		t.Errorf("KernelCache: SmbRoot = %s, want %s", roots["KernelCache"], spec.SmbRoot_SMB_ROOT_CLUSTER)
	}
	if smb["Plain"] || smb["Authority"] {
		t.Errorf("non-SMB service marked IsSMB: %v", smb)
	}
	if !auth["Authority"] {
		t.Errorf("Authority: IsSMBAuthority = false, want true (option (spec.smb_authority))")
	}
	if auth["Cache"] || auth["Plain"] {
		t.Errorf("non-authority service marked IsSMBAuthority: %v", auth)
	}
}

func TestValidateSmbAuthority(t *testing.T) {
	skipIfNoProtoc(t)

	cases := []struct {
		name      string
		body      string
		wantErr   bool
		errSubstr string
	}{
		{
			name: "smb with one authority is valid",
			body: `
service Cache { option (spec.smb) = {}; rpc Get(Ping) returns (Ping); }
service Authority { option (spec.smb_authority) = {}; rpc Get(Ping) returns (Ping); }
`,
			wantErr: false,
		},
		{
			name: "smb with no authority fails",
			body: `
service Cache { option (spec.smb) = {root: SMB_ROOT_CLUSTER}; rpc Get(Ping) returns (Ping); }
`,
			wantErr:   true,
			errSubstr: "no (spec.smb_authority)",
		},
		{
			name: "smb with two authorities fails",
			body: `
service Cache { option (spec.smb) = {}; rpc Get(Ping) returns (Ping); }
service AuthorityA { option (spec.smb_authority) = {}; rpc Get(Ping) returns (Ping); }
service AuthorityB { option (spec.smb_authority) = {}; rpc Get(Ping) returns (Ping); }
`,
			wantErr:   true,
			errSubstr: "exactly one",
		},
		{
			name: "no smb service needs no authority",
			body: `
service Plain { rpc Get(Ping) returns (Ping); }
`,
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := smbParser(t, tc.body)
			err := ValidateSmbAuthority(p)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ValidateSmbAuthority: expected error, got nil")
				}
				if tc.errSubstr != "" && !strings.Contains(err.Error(), tc.errSubstr) {
					t.Fatalf("ValidateSmbAuthority error %q does not contain %q", err.Error(), tc.errSubstr)
				}
			} else if err != nil {
				t.Fatalf("ValidateSmbAuthority: unexpected error: %v", err)
			}
		})
	}
}

func TestSmbAuthorityServiceID(t *testing.T) {
	skipIfNoProtoc(t)

	p := smbParser(t, `
service Cache { option (spec.smb) = {}; rpc Get(Ping) returns (Ping); }
service Authority { option (spec.smb_authority) = {}; rpc Get(Ping) returns (Ping); }
`)
	got, err := SmbAuthorityServiceID(p)
	if err != nil {
		t.Fatalf("SmbAuthorityServiceID: %v", err)
	}
	if got != "smbtest" {
		t.Fatalf("SmbAuthorityServiceID = %q, want %q", got, "smbtest")
	}
}
