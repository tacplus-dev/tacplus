// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"io/fs"
	"math"
	"regexp"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/templates"
)

// genericAPIKey is gitleaks' default generic-api-key rule (v8.30): a name
// carrying a secret keyword, an assignment, then a value of at least 3.5 bits
// of entropy per character. Every project that generates a chassis gets its
// tests too, and a project's secret scan does not read tacplus's .gitleaks.toml
// allowlist: a test fixture this rule matches is a finding in every one of
// them (tacplus#628, looper#145).
var genericAPIKey = regexp.MustCompile(`(?i)[\w.-]{0,50}?(?:access|auth|(?-i:[Aa]pi|API)|credential|creds|key|passw(?:or)?d|secret|token)(?:[ \t\w.-]{0,20})[\s'"]{0,3}(?:=|>|:{1,3}=|\|\||:|=>|\?=|,)[\x60'"\s=]{0,5}([\w.=-]{10,150}|[a-z0-9][a-z0-9+/]{11,}={0,3})(?:[\x60'"\s;]|\\[nr]|$)`)

const genericAPIKeyEntropy = 3.5

func shannonEntropy(s string) float64 {
	counts := map[rune]int{}
	n := 0
	for _, r := range s {
		counts[r]++
		n++
	}
	e := 0.0
	for _, c := range counts {
		p := float64(c) / float64(n)
		e -= p * math.Log2(p)
	}
	return e
}

// tokenShaped reports whether a match is a literal that reads as a credential
// rather than code: a quoted value of at least 20 characters, high entropy and
// a digit. gitleaks drops code-shaped matches (keyUsage = x509.KeyUsage...)
// with filters this test does not copy; it keeps to what a fixture looks like.
func tokenShaped(match, v string) bool {
	quoted := strings.Contains(match, `"`+v) || strings.Contains(match, "'"+v) || strings.Contains(match, "`"+v)
	return quoted && len(v) >= 20 && shannonEntropy(v) >= genericAPIKeyEntropy && strings.ContainsAny(v, "0123456789")
}

// TestNoTokenShapedLiteralInTemplates: no template carries a literal a secret
// scan reads as a key. A fixture that must look like a token (a redaction
// test's JWT) is assembled from parts at test run time; golden vectors are
// named for what they are (a page vector), not as a token.
func TestNoTokenShapedLiteralInTemplates(t *testing.T) {
	err := fs.WalkDir(templates.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := fs.ReadFile(templates.FS, path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(data), "\n") {
			for _, m := range genericAPIKey.FindAllStringSubmatch(line, -1) {
				if tokenShaped(m[0], m[1]) {
					t.Errorf("%s:%d: token-shaped literal %q (gitleaks generic-api-key): assemble it at run time or rename it", path, i+1, m[0])
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
