// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// jsonKeyOption matches a (spec.json_key) field option and captures its value.
var jsonKeyOption = regexp.MustCompile(`\(spec\.json_key\)\s*=\s*("(?:[^"\\]|\\.)*")`)

// injectJsonNameForJsonKey gives every field declaring (spec.json_key) the
// protobuf json_name of the same value, in the proto tree Rust compiles.
//
// Rust's JSON is pbjson (protoc-gen-prost-serde), which writes a field under its
// json_name and reads it under its json_name or proto name. json_name is
// therefore exactly (spec.json_key)'s contract — the key Go's ToJSON and Java's
// toJson write — at every nesting depth, with nothing to rename at runtime. Go
// and Java honour json_key in their own serializers and compile no proto with
// it, so this runs for Rust only.
func injectJsonNameForJsonKey(protoDir string) error {
	return filepath.WalkDir(protoDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".proto") {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(content), "\n")
		changed := false
		for i, line := range lines {
			// A comment may quote the option (spec.proto's own example does);
			// only a declaration gets the json_name.
			if strings.HasPrefix(strings.TrimSpace(line), "//") || !jsonKeyOption.MatchString(line) {
				continue
			}
			lines[i] = jsonKeyOption.ReplaceAllString(line, `(spec.json_key) = $1, json_name = $1`)
			changed = true
		}
		if !changed {
			return nil
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
			return fmt.Errorf("inject json_name into %s: %w", path, err)
		}
		return nil
	})
}
