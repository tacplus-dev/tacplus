// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/mod/semver"
)

// goDependencyFloors are the lowest versions of Go modules the cross-test
// harnesses and the example may pin: below them the module carries
// advisories the generated chassis reaches (#199). A harness go.mod pin is
// raised by `go mod tidy` only to what its other requirements need, so a
// stale pin there is what the generated module builds with.
var goDependencyFloors = map[string]struct {
	floor string
	// withGrpc: a file that pins grpc pins this module too.
	withGrpc bool
}{
	// GO-2026-6603/6611/6612/6617 (HTTP/2 server, fixed in v0.60.0) and
	// GO-2026-6860/6866 (html, fixed in v0.62.0).
	"golang.org/x/net": {"v0.62.0", true},
	// GHSA-5cgq-3rg8-m6cv, GHSA-x527-x647-q7gg and eleven more (fixed in
	// v0.56.0), and GO-2026-5932 (openpgp), fixed only after v0.58.0, the
	// newest tag (#312). Left unpinned, x/net v0.62.0 requires v0.58.0, which
	// clears all but GO-2026-5932, and nothing generated imports openpgp.
	"golang.org/x/crypto": {"v0.58.1-0.20261009181718-2f1f834330b2", false},
}

func TestGoDependencyPinsMeetTheirFloors(t *testing.T) {
	var files []string
	for _, pattern := range []string{
		"../cross-test/*/run.sh",
		"../cross-test/*/proto/dependencies.yaml",
		"../example/run.sh",
		"../example/proto/dependencies.yaml",
		"../example/server/go.mod",
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matches...)
	}
	if len(files) == 0 {
		t.Fatal("no cross-test or example dependency files found")
	}
	// go.mod require lines ("module v1.2.3") and dependencies.yaml entries
	// ("module: v1.2.3").
	pinOf := func(module string) *regexp.Regexp {
		return regexp.MustCompile(`(?m)^\s*` + regexp.QuoteMeta(module) + `:?\s+"?(v[0-9][^\s"]*)`)
	}
	// A file that pins grpc writes (or declares) the dependencies of a
	// generated Go chassis, so it pins each withGrpc module too: left out,
	// tidy resolves the lowest version grpc requires.
	grpc := pinOf("google.golang.org/grpc")
	for module, d := range goDependencyFloors {
		floor := d.floor
		pin := pinOf(module)
		for _, path := range files {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if d.withGrpc && grpc.Match(data) && !pin.Match(data) {
				t.Errorf("%s pins google.golang.org/grpc but not %s (floor %s)", path, module, floor)
			}
			for _, m := range pin.FindAllStringSubmatchIndex(string(data), -1) {
				version := string(data[m[2]:m[3]])
				if semver.Compare(version, floor) < 0 {
					line := strings.Count(string(data[:m[0]]), "\n") + 1
					t.Errorf("%s:%d pins %s %s, below its floor %s", path, line, module, version, floor)
				}
			}
		}
	}
}
