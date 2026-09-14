// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import "testing"

// ConvertToProtoInfo records ONE file per referenced cross-package as that
// package's import location. A proto package legitimately spans several files,
// and CrossPackageFiles feeds the emitted Python import line verbatim
// (python/types/proto_imports.go pyCrossProtoFile → pyProtoFileModuleName), so
// the raw-map "first descriptor whose Package matches" made two identical
// builds emit different import statements. p.descriptors is a map, so Go
// randomizes that walk on every parse.
func TestCrossPackageFilesStableAcrossPackageSpanningFiles(t *testing.T) {
	skipIfNoProtoc(t)

	dir := t.TempDir()
	copySpecProto(t, dir)

	// One package, two files. The consumer references a type from each.
	writeTempProtoFile(t, dir, "aaa_shared.proto", `syntax = "proto3";

package shared;

message Alpha { string m = 1; }
`)
	writeTempProtoFile(t, dir, "zzz_shared.proto", `syntax = "proto3";

package shared;

message Omega { string m = 1; }
`)
	writeTempProtoFile(t, dir, "consumer.proto", `syntax = "proto3";

package consumer;

import "aaa_shared.proto";
import "zzz_shared.proto";

service ConsumerApi {
  rpc Do(shared.Alpha) returns (shared.Omega);
}
`)

	// Go randomizes map order per range, so repeat: an unsorted walk flips this
	// with overwhelming probability across this many parses.
	for i := 0; i < 12; i++ {
		p, err := NewDescriptorParser(dir)
		if err != nil {
			t.Fatalf("attempt %d: NewDescriptorParser: %v", i, err)
		}
		info, err := p.ConvertToProtoInfo("consumer.proto")
		if err != nil {
			t.Fatalf("attempt %d: ConvertToProtoInfo: %v", i, err)
		}
		if got := info.CrossPackageFiles["shared"]; got != "aaa_shared.proto" {
			t.Fatalf("attempt %d: CrossPackageFiles[shared] = %q, want aaa_shared.proto (the lowest-sorting file of the package)", i, got)
		}
	}
}
