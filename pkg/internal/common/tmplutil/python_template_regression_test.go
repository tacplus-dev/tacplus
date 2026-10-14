// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package tmplutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPythonRuntimeTemplatesMatchProtoShapes(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	tests := []struct {
		path    string
		want    []string
		forbid  []string
		message string
	}{
		{
			path: "pkg/templates/python/chassis/x509/x509_cert.py.tmpl",
			want: []string{
				"class ImmutableCertChain:",
				"x509_pb2.X509CertChain",
			},
			forbid: []string{
				"X509CertPair",
				"ImmutableCertPair",
			},
			message: "Python x509 must track X509CertChain, not the removed X509CertPair proto shape",
		},
		{
			path: "pkg/templates/python/chassis/x509/__init__.py.tmpl",
			want: []string{
				"ImmutableCertChain",
			},
			forbid: []string{
				"ImmutableCertPair",
			},
			message: "Python x509 package exports must not expose stale cert-pair wrappers",
		},
		{
			path: "pkg/templates/python/chassis/token/token_wrapper.py.tmpl",
			want: []string{
				"from __future__ import annotations",
			},
			message: "Python token wrapper uses modern annotations and must work under the declared Python 3.8+ floor",
		},
		{
			path: "pkg/templates/python/types/exceptions.py.tmpl",
			want: []string{
				"def permission_denied(message: str) -> ProtoError:",
				"return ProtoError(CODE_PERMISSION_DENIED, message)",
			},
			message: "Python generated authorizer imports permission_denied, so generated exceptions must export it",
		},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			content, err := os.ReadFile(filepath.Join(root, tt.path))
			if err != nil {
				t.Fatalf("read template: %v", err)
			}
			src := string(content)
			for _, want := range tt.want {
				if !strings.Contains(src, want) {
					t.Fatalf("%s: missing %q", tt.message, want)
				}
			}
			for _, forbidden := range tt.forbid {
				if strings.Contains(src, forbidden) {
					t.Fatalf("%s: found forbidden %q", tt.message, forbidden)
				}
			}
		})
	}
}
