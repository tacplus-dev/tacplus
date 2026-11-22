// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// `just floors` (tacplus#949) generates the cross-test models and checks every
// floor their manifests and the checkout's inventories declare against OSV.

// fakeFloorsTacplus stands in for tacplus: `build` writes each language's
// manifest, a Rust model with the chassis crates FAKE_CHASSIS names, or fails
// for FAKE_FAIL (a language).
const fakeFloorsTacplus = `#!/usr/bin/env bash
set -euo pipefail
lang="$(sed -n 's/^language: *"\{0,1\}\([a-z]*\)"\{0,1\}$/\1/p' tacplus.yaml)"
echo "tacplus $1 $lang $PWD" >>"$FAKE_LOG"
if [[ "${FAKE_FAIL:-}" == "$lang" ]]; then echo "fake generation failed for $lang" >&2; exit 1; fi
m=tacplus/model
mkdir -p "$m"
case "$lang" in
  rust)
    printf '[package]\nname = "model"\n\n[dependencies]\nrand = "%s"\n' "${FAKE_RAND:-0.8.6}" >"$m/Cargo.toml"
    for crate in $FAKE_CHASSIS; do
      mkdir -p "$m/chassis/$crate"
      printf '[package]\nname = "%s"\n\n[dependencies]\nserde = { workspace = true }\n' "$crate" >"$m/chassis/$crate/Cargo.toml"
    done
    ;;
  python) printf '[project]\ndependencies = ["protobuf>=6.33.6"]\n' >"$m/pyproject.toml" ;;
  typescript) echo '{"dependencies": {"axios": "^1.20.0"}}' >"$m/package.json" ;;
  csharp) echo '<Project><ItemGroup><PackageReference Include="Google.Protobuf" Version="3.27.2" /></ItemGroup></Project>' >tacplus/Model.csproj ;;
  swift) echo '.package(url: "https://github.com/apple/swift-protobuf.git", from: "1.38.0"),' >tacplus/Package.swift ;;
esac
`

type floorsRun struct {
	out, calls, queried, work string
	err                       error
}

// runFloors runs floors.sh with the fake tacplus against a fake OSV that
// reports an advisory for each "ecosystem name@version" in vulnerable.
func runFloors(t *testing.T, vulnerable map[string]string, env ...string) floorsRun {
	t.Helper()
	var queried strings.Builder
	osv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/querybatch" {
			var req struct {
				Queries []struct {
					Package struct{ Name, Ecosystem string }
					Version string
				}
			}
			json.NewDecoder(r.Body).Decode(&req)
			var results []map[string]any
			for _, q := range req.Queries {
				key := q.Package.Ecosystem + " " + q.Package.Name + "@" + q.Version
				queried.WriteString(key + "\n")
				res := map[string]any{}
				if id, ok := vulnerable[key]; ok {
					res["vulns"] = []map[string]string{{"id": id}}
				}
				results = append(results, res)
			}
			json.NewEncoder(w).Encode(map[string]any{"results": results})
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/v1/vulns/")
		json.NewEncoder(w).Encode(map[string]any{"id": id, "summary": "fake advisory"})
	}))
	defer osv.Close()

	tmp := t.TempDir()
	tacplus := filepath.Join(tmp, "tacplus")
	if err := os.WriteFile(tacplus, []byte(fakeFloorsTacplus), 0o755); err != nil {
		t.Fatal(err)
	}
	templates, err := filepath.Glob(filepath.Join("..", "pkg", "templates", "rust", "chassis", "*", "Cargo.toml.tmpl"))
	if err != nil || len(templates) == 0 {
		t.Fatalf("no Rust chassis templates (%v)", err)
	}
	var crates []string
	for _, tmpl := range templates {
		crates = append(crates, filepath.Base(filepath.Dir(tmpl)))
	}
	log := filepath.Join(tmp, "calls.log")
	cmd := exec.Command("bash", "floors.sh")
	cmd.Env = append(os.Environ(),
		"TACPLUS_BIN="+tacplus,
		"TACPLUS_FLOORS_WORK="+filepath.Join(tmp, "work"),
		"TACPLUS_OSV="+osv.URL,
		"FAKE_LOG="+log,
		"FAKE_CHASSIS="+strings.Join(crates, " "))
	cmd.Env = append(cmd.Env, env...)
	out, runErr := cmd.CombinedOutput()
	calls, _ := os.ReadFile(log)
	return floorsRun{string(out), string(calls), queried.String(), filepath.Join(tmp, "work"), runErr}
}

func TestFloors(t *testing.T) {
	t.Run("checks every generated manifest's floors and the inventories'", func(t *testing.T) {
		before := trackedStatus(t)
		r := runFloors(t, nil)
		if r.err != nil {
			t.Fatalf("floors.sh: %v\n%s", r.err, r.out)
		}
		for _, want := range []string{
			// The generated manifests'.
			"crates.io rand@0.8.6", "PyPI protobuf@6.33.6", "npm axios@1.20.0",
			"NuGet Google.Protobuf@3.27.2", "SwiftURL github.com/apple/swift-protobuf@1.38.0",
			// The checkout's inventories: Go and Java are read there alone.
			"Go google.golang.org/grpc@", "Maven com.google.protobuf:protobuf-java@",
		} {
			if !strings.Contains(r.queried, want) {
				t.Errorf("OSV was not asked about %s; asked:\n%s", want, r.queried)
			}
		}
		for _, want := range []string{"tacplus build rust", "tacplus build python", "tacplus build typescript", "tacplus build csharp", "tacplus build swift"} {
			if !strings.Contains(r.calls, want) {
				t.Errorf("floors.sh did not run %q; calls:\n%s", want, r.calls)
			}
		}
		if !strings.Contains(r.out, "### Checked against") {
			t.Errorf("the output does not say what it checked against:\n%s", r.out)
		}
		// The models hold generated test keys: none stays for the checkout's
		// secret scan.
		if _, err := os.Stat(filepath.Join(r.work, "trees")); err == nil {
			t.Errorf("the generated models are left in %s", r.work)
		}
		if after := trackedStatus(t); after != before {
			t.Errorf("floors.sh changed tracked files:\nbefore:\n%s\nafter:\n%s", before, after)
		}
	})
	t.Run("a floor OSV has an advisory for fails it", func(t *testing.T) {
		r := runFloors(t, map[string]string{"crates.io rand@0.8.0": "RUSTSEC-0000-0001"}, "FAKE_RAND=0.8")
		if r.err == nil {
			t.Fatalf("floors.sh passed with an advisory at a floor\n%s", r.out)
		}
		if !strings.Contains(r.out, "FAIL crates.io rand@0.8.0: RUSTSEC-0000-0001") {
			t.Errorf("the failure does not name the floor and its advisory:\n%s", r.out)
		}
	})
	t.Run("a Rust chassis crate no model reaches fails it", func(t *testing.T) {
		r := runFloors(t, nil, "FAKE_CHASSIS=am")
		if r.err == nil || !strings.Contains(r.out, "no generated model has the Rust chassis crate") {
			t.Fatalf("floors.sh passed with chassis crates unchecked: err %v\n%s", r.err, r.out)
		}
	})
	t.Run("a generation that fails fails it, with its log", func(t *testing.T) {
		r := runFloors(t, nil, "FAKE_FAIL=swift")
		if r.err == nil || !strings.Contains(r.out, "fake generation failed for swift") {
			t.Fatalf("floors.sh passed or hid the log though swift generation failed: err %v\n%s", r.err, r.out)
		}
		if _, err := os.Stat(filepath.Join(r.work, "trees")); err == nil {
			t.Errorf("a failed run left the generated models in %s", r.work)
		}
	})
}

// `just floors` is a gate: `just gates` runs it, and it is a call of the
// script (its contract lives there).
func TestGatesRunFloors(t *testing.T) {
	recipes := justRecipes(t, filepath.Join("..", "justfile"))
	body, ok := recipes["floors"]
	if !ok {
		t.Fatal("justfile has no floors recipe")
	}
	if !strings.Contains(body, "scripts/floors.sh") {
		t.Errorf("the floors recipe does not call scripts/floors.sh:\n%s", body)
	}
	if !strings.Contains(recipes["gates"], " floors ") {
		t.Errorf("just gates does not run floors:\n%s", recipes["gates"])
	}
}
