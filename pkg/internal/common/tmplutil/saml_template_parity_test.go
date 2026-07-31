// Copyright (c) 2026 Second Star Cavalier LLC

package tmplutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSamlTemplatesExposeVerificationParity(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	tests := []struct {
		name string
		path string
		want []string
	}{
		{
			name: "go",
			path: "pkg/templates/go/chassis/saml/saml.go.tmpl",
			want: []string{
				"WithExpectedIssuer",
				"WithExpectedAudience",
				"WithExpectedRecipient",
				"WithExpectedInResponseTo",
				"SubjectNotOnOrAfter",
				"assertionReplayCache.checkAndMark",
				"if err != nil {\n\t\treturn true",
			},
		},
		{
			name: "typescript",
			path: "pkg/templates/typescript/chassis/saml/SamlAssertion.ts.tmpl",
			want: []string{
				"export interface SamlVerifyOptions",
				"expectedIssuer",
				"expectedAudience",
				"expectedRecipient",
				"expectedInResponseTo",
				"subjectNotOnOrAfter",
				"checkAndMarkReplay",
				"if (!this.notOnOrAfter) return false;",
				"if (!Number.isFinite(notOnOrAfter) || now >= notOnOrAfter) return false;",
			},
		},
		{
			name: "python",
			path: "pkg/templates/python/chassis/saml/saml_assertion.py.tmpl",
			want: []string{
				"class SamlVerifyOptions",
				"expected_issuer",
				"expected_audience",
				"expected_recipient",
				"expected_in_response_to",
				"subject_not_on_or_after",
				"_check_and_mark_replay",
				"if not self.not_on_or_after:",
			},
		},
		{
			name: "java",
			path: "pkg/templates/java/chassis/saml/SamlAssertion.java.tmpl",
			want: []string{
				"public static final class VerifyOptions",
				"expectedIssuer",
				"expectedAudience",
				"expectedRecipient",
				"expectedInResponseTo",
				"subjectNotOnOrAfter",
				"checkAndMarkReplay",
				"if (notOnOrAfter.isEmpty()) return false;",
				"b.notOnOrAfter = \"\";",
			},
		},
		{
			name: "rust",
			path: "pkg/templates/rust/chassis/saml/src/saml.rs.tmpl",
			want: []string{
				"pub struct SamlVerifyOptions",
				"expected_issuer",
				"expected_audience",
				"expected_recipient",
				"expected_in_response_to",
				"subject_not_on_or_after",
				"check_and_mark_replay",
				"let Ok(na) = parse_saml_time(&self.not_on_or_after)",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join(root, tt.path))
			if err != nil {
				t.Fatalf("read template: %v", err)
			}
			src := string(content)
			for _, want := range tt.want {
				if !strings.Contains(src, want) {
					t.Fatalf("%s missing %q", tt.path, want)
				}
			}
		})
	}
}
