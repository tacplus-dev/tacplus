// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/packages"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// The Python floors that clear every known advisory need Python 3.10 (#264):
// requests 2.33.0 and urllib3 2.8.0 do not install on 3.8 or 3.9, and
// cryptography 50.0.0 not on 3.8. A generated package must not declare an
// interpreter its own floors cannot install on, and must not accept a release
// with a known advisory.
var pythonAdvisoryFloors = map[string]string{
	"cryptography": "50.0.0",
	"requests":     "2.33.0",
	"urllib3":      "2.8.0",
}

const pythonFloorTestDeps = `lang:
    python:
        dependencies:
            PyYAML: '>=6.0.3'
            cryptography: '>=50.0.0'
            grpcio: '>=1.76.0'
            httpx: '>=0.28.1'
            protobuf: '>=6.33.6'
            requests: '>=2.33.0'
            tomli: '>=2.4.0'
            tomli-w: '>=1.2.0'
            urllib3: '>=2.8.0'
`

func usePythonFloorTestDeps(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dependencies.yaml"), []byte(pythonFloorTestDeps), 0644); err != nil {
		t.Fatalf("write dependencies.yaml: %v", err)
	}
	packages.SetProtoDir(dir)
	t.Cleanup(func() { packages.SetProtoDir("") })
}

func assertPythonFloors(t *testing.T, content string) {
	t.Helper()
	if !strings.Contains(content, `requires-python = ">=3.10"`) {
		t.Errorf("pyproject.toml must declare requires-python >=3.10:\n%s", content)
	}
	for name, floor := range pythonAdvisoryFloors {
		if !strings.Contains(content, `"`+name+`>=`+floor+`"`) {
			t.Errorf("pyproject.toml must declare %s>=%s:\n%s", name, floor, content)
		}
	}
}

func TestRootPyprojectDeclaresPython310AndAdvisoryFloors(t *testing.T) {
	usePythonFloorTestDeps(t)
	out := t.TempDir()
	cfg := &tactype.Config{PythonPackageBase: "acme-client", VersionTag: "v1.2.3"}
	if err := createRootPyprojectToml(cfg, out); err != nil {
		t.Fatalf("createRootPyprojectToml: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(out, "pyproject.toml"))
	if err != nil {
		t.Fatalf("read pyproject.toml: %v", err)
	}
	assertPythonFloors(t, string(data))
}

// A regeneration into an existing output updates the pyproject.toml in place:
// it must raise requires-python and the root floors too, not keep the ones an
// older tacplus wrote.
func TestUpdatePyprojectRaisesPythonAndRootFloors(t *testing.T) {
	usePythonFloorTestDeps(t)
	out := t.TempDir()
	existing := `[project]
name = "acme-client"
version = "1.0.0"
requires-python = ">=3.8"
dependencies = [
    "protobuf>=6.33.6",
    "grpcio>=1.76.0",
    "cryptography>=42.0.0",
    "httpx>=0.28.1",
    "requests>=2.31.0",
    "urllib3>=2.0.0",
    "rich>=13.0.0"
]
`
	path := filepath.Join(out, "pyproject.toml")
	if err := os.WriteFile(path, []byte(existing), 0644); err != nil {
		t.Fatalf("write pyproject.toml: %v", err)
	}
	cfg := &tactype.Config{PythonPackageBase: "acme-client", VersionTag: "v1.2.3"}
	if err := createRootPyprojectToml(cfg, out); err != nil {
		t.Fatalf("createRootPyprojectToml: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read pyproject.toml: %v", err)
	}
	content := string(data)
	assertPythonFloors(t, content)
	for _, stale := range []string{`">=3.8"`, "cryptography>=42.0.0", "requests>=2.31.0", "urllib3>=2.0.0"} {
		if strings.Contains(content, stale) {
			t.Errorf("updated pyproject.toml still holds %s:\n%s", stale, content)
		}
	}
	if !strings.Contains(content, `"rich>=13.0.0"`) {
		t.Errorf("update must keep the consumer's own dependencies:\n%s", content)
	}
}

func TestTypesPyprojectDeclaresPython310(t *testing.T) {
	usePythonFloorTestDeps(t)
	typesDir := t.TempDir()
	cfg := &tactype.Config{PythonPackageBase: "acme-utils", VersionTag: "v1.2.3"}
	if err := generateTypesPyProject(cfg, typesDir, "services", "acme"); err != nil {
		t.Fatalf("generateTypesPyProject: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(typesDir, "pyproject.toml"))
	if err != nil {
		t.Fatalf("read pyproject.toml: %v", err)
	}
	if !strings.Contains(string(data), `requires-python = ">=3.10"`) {
		t.Errorf("types pyproject.toml must declare requires-python >=3.10:\n%s", data)
	}
}

// Every dependencies.yaml in the repository, example/proto's included (consumers
// copy it), declares Python 3.10 and floors at or above the advisory-free ones.
func TestRepoDependenciesYamlPythonFloors(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	var files []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "build", "target", "out", "site":
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "dependencies.yaml" {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk repository: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("found no dependencies.yaml")
	}
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var deps struct {
			Tools map[string]string `yaml:"tools"`
			Lang  struct {
				Python struct {
					Dependencies map[string]string `yaml:"dependencies"`
				} `yaml:"python"`
			} `yaml:"lang"`
		}
		if err := yaml.Unmarshal(data, &deps); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		if v, ok := deps.Tools["python"]; ok && v != ">=3.10" {
			t.Errorf("%s: tools.python is %q, want '>=3.10'", path, v)
		}
		for name, floor := range pythonAdvisoryFloors {
			v, ok := deps.Lang.Python.Dependencies[name]
			if !ok {
				continue
			}
			if !strings.HasPrefix(v, ">=") || versionLess(strings.TrimPrefix(v, ">="), floor) {
				t.Errorf("%s: %s is %q, want '>=%s' or higher", path, name, v, floor)
			}
		}
	}
}

func versionLess(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(as) || i < len(bs); i++ {
		var x, y int
		if i < len(as) {
			x, _ = strconv.Atoi(as[i])
		}
		if i < len(bs) {
			y, _ = strconv.Atoi(bs[i])
		}
		if x != y {
			return x < y
		}
	}
	return false
}
