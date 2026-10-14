// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"testing"
)

// Pins (spec.volume_group) service-option extraction: each declaration lands as
// one ServiceDescriptor.VolumeGroups entry carrying all four fields, the
// annotation is repeatable on one service, and a service that declares none
// gets none. registry.json's volume_groups — the only thing the operator reads
// to render the blob store's group config — is built straight off this, so a
// regression here silently drops a group and every upload naming it is refused
// at runtime.

// volumeGroupParser writes a proto using spec annotations into a temp dir
// (alongside a copy of spec.proto) and returns a DescriptorParser over it.
func volumeGroupParser(t *testing.T, protoBody string) *DescriptorParser {
	t.Helper()
	return volumeGroupParserFiles(t, map[string]string{"vgtest.proto": protoBody})
}

// volumeGroupParserFiles is volumeGroupParser for a fleet of more than one
// file. Each entry's key is the file name and its value the body appended after
// a header whose package is the file's base name.
func volumeGroupParserFiles(t *testing.T, bodies map[string]string) *DescriptorParser {
	t.Helper()
	dir := t.TempDir()
	copySpecProto(t, dir)
	for name, body := range bodies {
		pkg := name[:len(name)-len(".proto")]
		content := `syntax = "proto3";

package ` + pkg + `;

import "sys/spec.proto";

message Ping { string m = 1; }
` + body
		writeTempProtoFile(t, dir, name, content)
	}
	p, err := NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	return p
}

func TestParseServiceOptions_VolumeGroup(t *testing.T) {
	skipIfNoProtoc(t)

	p := volumeGroupParser(t, `
// Docs owns the documents volume group.
service Docs {
  option (spec.volume_group) = {
    name: "documents"
    max_blob_bytes: 52428800
    content_types: ["application/pdf", "image/jpeg"]
    tenant_quota_bytes: 53687091200
  };
  rpc Get(Ping) returns (Ping);
}

// Media owns two groups, declared with a repeated option.
service Media {
  option (spec.volume_group) = {name: "photos" max_blob_bytes: 26214400 content_types: ["image/png"]};
  option (spec.volume_group) = {name: "clips" max_blob_bytes: 1048576};
  rpc Get(Ping) returns (Ping);
}

// Plain stores no blobs.
service Plain {
  rpc Get(Ping) returns (Ping);
}
`)

	fd, err := p.GetFileDescriptor("vgtest.proto")
	if err != nil {
		t.Fatalf("GetFileDescriptor: %v", err)
	}

	groups := map[string][]VolumeGroupInfo{}
	for _, svc := range fd.Services {
		groups[svc.Name] = svc.VolumeGroups
	}

	docs := groups["Docs"]
	if len(docs) != 1 {
		t.Fatalf("Docs: got %d volume groups, want 1 (%+v)", len(docs), docs)
	}
	if docs[0].Name != "documents" {
		t.Errorf("Docs group name = %q, want %q", docs[0].Name, "documents")
	}
	if docs[0].MaxBlobBytes != 52428800 {
		t.Errorf("Docs max_blob_bytes = %d, want 52428800", docs[0].MaxBlobBytes)
	}
	if docs[0].TenantQuotaBytes != 53687091200 {
		t.Errorf("Docs tenant_quota_bytes = %d, want 53687091200", docs[0].TenantQuotaBytes)
	}
	if len(docs[0].ContentTypes) != 2 ||
		docs[0].ContentTypes[0] != "application/pdf" || docs[0].ContentTypes[1] != "image/jpeg" {
		t.Errorf("Docs content_types = %v, want [application/pdf image/jpeg]", docs[0].ContentTypes)
	}

	media := groups["Media"]
	if len(media) != 2 {
		t.Fatalf("Media: got %d volume groups, want 2 (repeated option) (%+v)", len(media), media)
	}
	if media[0].Name != "photos" || media[1].Name != "clips" {
		t.Errorf("Media group names = [%q %q], want [photos clips]", media[0].Name, media[1].Name)
	}
	if media[1].TenantQuotaBytes != 0 {
		t.Errorf("Media clips tenant_quota_bytes = %d, want 0 (unset)", media[1].TenantQuotaBytes)
	}

	if len(groups["Plain"]) != 0 {
		t.Errorf("Plain: got %d volume groups, want 0", len(groups["Plain"]))
	}
}
