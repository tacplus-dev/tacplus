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

// The Go form of the same race (#713): a helper binds port 0, closes the
// listener on return and hands back only the port, which a generated
// GrpcServer then binds again. Another process (a parallel gate leg, another
// run on the runner) may take the port in between. A Go test keeps the
// listener it bound and serves on it.
func TestGoTestsKeepTheListenerTheyBind(t *testing.T) {
	bind := regexp.MustCompile(`(\w+),\s*\w+\s*:?=\s*net\.Listen\("tcp",\s*"[^"]*:0"\)`)
	var files []string
	for _, root := range []string{"../cross-test", "../pkg/templates/go"} {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() && (d.Name() == "target" || d.Name() == "node_modules") {
				return filepath.SkipDir
			}
			if !d.IsDir() && (strings.HasSuffix(path, ".go") || strings.HasSuffix(path, ".go.tmpl")) {
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", root, err)
		}
	}
	if len(files) == 0 {
		t.Fatal("no Go sources found under cross-test/ or pkg/templates/go/")
	}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		src := string(data)
		for _, m := range bind.FindAllStringSubmatch(src, -1) {
			closed := regexp.MustCompile(`defer\s+` + m[1] + `\.Close\(\)`)
			port := regexp.MustCompile(`return\s+` + m[1] + `\.Addr\(\)\.\(\*net\.TCPAddr\)\.Port`)
			if closed.MatchString(src) && port.MatchString(src) {
				t.Errorf("%s binds port 0 as %s, closes it and returns its port, so the server binds a port another process may hold; serve on the bound listener instead", path, m[1])
			}
		}
	}
}
