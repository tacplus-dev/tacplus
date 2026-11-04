// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Redact<T> sets <field>_redacted on every (spec.sensitive) field and on every
// eligible field of a (spec.sensitive_message), so the injector must add exactly
// those indicators: not on a (spec.not_sensitive) field, nor on a non-string
// leaf of a sensitive message.
func TestInjectRedactIndicatorFields_Sensitive(t *testing.T) {
	dir := t.TempDir()
	copySpecProtoForConfigSliceTest(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, "uni"), 0o755); err != nil {
		t.Fatal(err)
	}
	src := `syntax = "proto3";
package uni;

import "sys/spec.proto";

message Secrets {
  option (spec.sensitive_message) = {};
  string api_token = 1;
  bytes signing_key = 2;
  string version = 3 [(spec.not_sensitive) = true];
  int64 rotated_at = 4;
  Pki pki = 5;
  string trace_id = 200;
}

message Pki {
  string pem = 1;
}

message Endpoint {
  string url = 1;
  string password = 2 [(spec.sensitive) = {required: true}];
  map<string, string> headers = 3 [(spec.sensitive) = {}];
}
`
	path := filepath.Join(dir, "uni", "uni.proto")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := InjectRedactIndicatorFields(dir, dir); err != nil {
		t.Fatalf("inject: %v", err)
	}
	out, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, want := range []string{
		"bool api_token_redacted = 50001;",
		"bool signing_key_redacted = 50002;",
		"bool pki_redacted = 50005;",
		"bool password_redacted = 50002;",
		"bool headers_redacted = 50003;",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	for _, absent := range []string{"version_redacted", "rotated_at_redacted", "trace_id_redacted", "pem_redacted", "url_redacted"} {
		if strings.Contains(got, absent) {
			t.Errorf("unexpected %q in:\n%s", absent, got)
		}
	}
}
