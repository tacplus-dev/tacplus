// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A Rust fixture that binds port 0, reads the port, drops the listener and
// lets its server bind that port again races every other test running in
// parallel: the kernel may hand the freed port to another test, whose client
// then reaches this test's server (#531). A fixture keeps the listener it bound
// and hands it to its server (TcpIncoming / serve_with_incoming).
func TestCrossTestRustFixturesKeepTheListenerTheyBind(t *testing.T) {
	bind := regexp.MustCompile(`let\s+(?:mut\s+)?(\w+)\s*=\s*(?:std::net::)?TcpListener::bind\("[^"]*:0"\)`)
	var files []string
	err := filepath.WalkDir("../cross-test", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "target" || d.Name() == "node_modules") {
			return filepath.SkipDir
		}
		if !d.IsDir() && strings.HasSuffix(path, ".rs") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil || len(files) == 0 {
		t.Fatalf("no cross-test Rust sources found: %v", err)
	}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		src := string(data)
		for _, m := range bind.FindAllStringSubmatch(src, -1) {
			if strings.Contains(src, "drop("+m[1]+")") {
				t.Errorf("%s binds port 0 as %s and drops it, so its server binds a port another test may hold; hand the bound listener to the server instead", path, m[1])
			}
		}
	}
}
