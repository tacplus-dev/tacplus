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

// This line keeps Python 3.8 (#264), so its floors are the newest releases that
// install on 3.8. The advisories they leave are listed in docs/USER.md as
// accepted; 2.6+ requires Python 3.10 and clears them.
var pythonAdvisoryFloors = map[string]string{
	"cryptography": "47.0.0",
	"requests":     "2.32.4",
	"urllib3":      "2.2.3",
}

const pythonRequiresTest = ">=3.8"

const pythonFloorTestDeps = `lang:
    python:
        dependencies:
            PyYAML: '>=6.0.3'
            cryptography: '>=47.0.0'
            grpcio: '>=1.76.0'
            httpx: '>=0.28.1'
            protobuf: '>=6.33.6'
            requests: '>=2.32.4'
            tomli: '>=2.4.0'
            tomli-w: '>=1.2.0'
            urllib3: '>=2.2.3'
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
	if !strings.Contains(content, `requires-python = "`+pythonRequiresTest+`"`) {
		t.Errorf("pyproject.toml must declare requires-python %s:\n%s", pythonRequiresTest, content)
	}
	for name, floor := range pythonAdvisoryFloors {
		if !strings.Contains(content, `"`+name+`>=`+floor+`"`) {
			t.Errorf("pyproject.toml must declare %s>=%s:\n%s", name, floor, content)
		}
	}
}

func TestRootPyprojectDeclaresPythonAndAdvisoryFloors(t *testing.T) {
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
requires-python = ">=3.7"
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
	for _, stale := range []string{`">=3.7"`, "cryptography>=42.0.0", "requests>=2.31.0", "urllib3>=2.0.0"} {
		if strings.Contains(content, stale) {
			t.Errorf("updated pyproject.toml still holds %s:\n%s", stale, content)
		}
	}
	if !strings.Contains(content, `"rich>=13.0.0"`) {
		t.Errorf("update must keep the consumer's own dependencies:\n%s", content)
	}
}

func TestTypesPyprojectDeclaresPython(t *testing.T) {
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
	if !strings.Contains(string(data), `requires-python = "`+pythonRequiresTest+`"`) {
		t.Errorf("types pyproject.toml must declare requires-python %s:\n%s", pythonRequiresTest, data)
	}
}

// Every dependencies.yaml in the repository, example/proto's included (consumers
// copy it), declares this line's Python and floors at or above its own.
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
		if v, ok := deps.Tools["python"]; ok && v != pythonRequiresTest {
			t.Errorf("%s: tools.python is %q, want %q", path, v, pythonRequiresTest)
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
