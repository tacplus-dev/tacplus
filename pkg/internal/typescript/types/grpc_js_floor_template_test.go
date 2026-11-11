// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The TypeScript chassis package.json templates declare @grpc/grpc-js with a
// literal floor. ^1.9.0 and ^1.10.1 admit GHSA-5375-pq7m-f5r2,
// GHSA-99f4-grh7-6pcq (malformed messages crash a server or client) and
// GHSA-m9gg-hp2v-232j (getAuthContext returns unauthorized certificates), all
// fixed in 1.14.5 (#338). Every template must declare that floor.
func TestTypeScriptChassisGrpcJsFloor(t *testing.T) {
	const dir = "../../../templates/typescript"
	const want = "^1.14.5"
	decl := regexp.MustCompile(`"@grpc/grpc-js": *"([^"]*)"`)
	found := 0
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || d.Name() != "package.json.tmpl" {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, m := range decl.FindAllStringSubmatch(string(data), -1) {
			found++
			if m[1] != want {
				t.Errorf("%s declares @grpc/grpc-js %q, want %q", path, m[1], want)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", dir, err)
	}
	if found == 0 {
		t.Fatalf("no @grpc/grpc-js declaration under %s", dir)
	}
}
