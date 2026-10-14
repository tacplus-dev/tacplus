// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/tacplusyaml"
)

// The build cache skips generation entirely when .hash.json matches a freshly
// computed hash (see Build in runner.go). A hashing failure that is swallowed
// instead of propagated therefore yields a digest over a SUBSET of the real
// inputs, which can spuriously match the cached digest and skip a build that
// should have run. These tests pin the fail-safe contract: any hashing failure
// must surface as a real error so the caller falls through to a full rebuild.

// danglingSrcFile creates a symlink named name in dir pointing at a path that
// does not exist. filepath.Walk lstats, so the entry is listed as a regular
// file, but opening it to hash fails. This is deterministic on every platform
// and, unlike a permission bit, is not defeated by running the tests as root.
func danglingSrcFile(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.Symlink(filepath.Join(dir, "does-not-exist"), filepath.Join(dir, name)); err != nil {
		t.Fatalf("symlink: %v", err)
	}
}

// unreadableDir creates a subdirectory with no permission bits so that walking
// into it fails. Root ignores the mode, so the caller must skip in that case.
func unreadableDir(t *testing.T, dir, name string) {
	t.Helper()
	sub := filepath.Join(dir, name)
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sub, "inner.go"), []byte("package inner\n"), 0o644); err != nil {
		t.Fatalf("write inner: %v", err)
	}
	if err := os.Chmod(sub, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(sub, 0o755) })
	if _, err := os.ReadDir(sub); err == nil {
		t.Skip("cannot make a directory unreadable (running as root?)")
	}
}

// Test_computeSrcHash_hashFileErrorPropagates: a source file that is listed but
// cannot be read must fail the hash. Before the fix the loop did
// `if err != nil { continue }`, so computeSrcHash returned a nil error and a
// digest identical to the one for a tree without that file at all.
func Test_computeSrcHash_hashFileErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "good.go"), []byte("package p\n"), 0o644); err != nil {
		t.Fatalf("write good.go: %v", err)
	}
	danglingSrcFile(t, dir, "broken.go")

	got, err := computeSrcHash(dir, filepath.Join(dir, "tacplus"), "go")
	if err == nil {
		t.Fatalf("computeSrcHash must fail when a listed source file cannot be hashed, got hash %q and nil error", got)
	}
}

// Test_computeSrcHash_walkErrorPropagates: a directory that cannot be entered
// must fail the hash. Before the fix the Walk callback did
// `if err != nil { return nil }`, silently hashing only the readable subset.
func Test_computeSrcHash_walkErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "good.go"), []byte("package p\n"), 0o644); err != nil {
		t.Fatalf("write good.go: %v", err)
	}
	unreadableDir(t, dir, "locked")

	got, err := computeSrcHash(dir, filepath.Join(dir, "tacplus"), "go")
	if err == nil {
		t.Fatalf("computeSrcHash must fail when a directory cannot be walked, got hash %q and nil error", got)
	}
}

// buildHashDirs returns a (protoDir, projectDir) pair holding one valid proto
// and one valid Go source file, so computeBuildHash succeeds unless a test
// deliberately breaks one of them.
func buildHashDirs(t *testing.T) (string, string) {
	t.Helper()
	protoDir := t.TempDir()
	projectDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(protoDir, "good.proto"), []byte("syntax = \"proto3\";\n"), 0o644); err != nil {
		t.Fatalf("write good.proto: %v", err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	return protoDir, projectDir
}

func testConfig() *tacplusyaml.Config {
	return &tacplusyaml.Config{
		Language:  "go",
		Src:       "protos",
		Pkg:       "com.example.test",
		OutputDir: "tacplus",
	}
}

// Test_computeBuildHash_baseline proves the failure tests below are non-vacuous:
// the same inputs without an induced failure hash successfully.
func Test_computeBuildHash_baseline(t *testing.T) {
	protoDir, projectDir := buildHashDirs(t)
	h, err := computeBuildHash(protoDir, projectDir, filepath.Join(projectDir, "tacplus"), "go", testConfig())
	if err != nil {
		t.Fatalf("computeBuildHash: %v", err)
	}
	if h.ConfigHash == "" || h.SrcHash == "" || len(h.Files) != 1 {
		t.Fatalf("unexpected build hash: %+v", h)
	}
}

// Test_computeBuildHash_protoHashFileErrorPropagates: before the fix the proto
// walk's hashFile error returned nil FROM THE CALLBACK, meaning "no error, keep
// walking" while quietly dropping the file from h.Files.
func Test_computeBuildHash_protoHashFileErrorPropagates(t *testing.T) {
	protoDir, projectDir := buildHashDirs(t)
	danglingSrcFile(t, protoDir, "broken.proto")

	h, err := computeBuildHash(protoDir, projectDir, filepath.Join(projectDir, "tacplus"), "go", testConfig())
	if err == nil {
		t.Fatalf("computeBuildHash must fail when a proto file cannot be hashed, got %+v and nil error", h)
	}
}

// Test_computeBuildHash_protoWalkErrorPropagates: before the fix a proto-dir
// walk error was swallowed by the callback's `return nil`.
func Test_computeBuildHash_protoWalkErrorPropagates(t *testing.T) {
	protoDir, projectDir := buildHashDirs(t)
	unreadableDir(t, protoDir, "locked")

	h, err := computeBuildHash(protoDir, projectDir, filepath.Join(projectDir, "tacplus"), "go", testConfig())
	if err == nil {
		t.Fatalf("computeBuildHash must fail when the proto dir cannot be walked, got %+v and nil error", h)
	}
}

// Test_computeBuildHash_srcHashErrorPropagates: computeSrcHash failing used to
// warn and leave SrcHash empty. Two consecutive failing runs would both record
// "" and compare equal — the same false-cache-hit class — so it is now a hard
// error out of computeBuildHash.
func Test_computeBuildHash_srcHashErrorPropagates(t *testing.T) {
	protoDir, projectDir := buildHashDirs(t)
	danglingSrcFile(t, projectDir, "broken.go")

	h, err := computeBuildHash(protoDir, projectDir, filepath.Join(projectDir, "tacplus"), "go", testConfig())
	if err == nil {
		t.Fatalf("computeBuildHash must fail when project source cannot be hashed, got %+v and nil error", h)
	}
	if h != nil {
		t.Errorf("computeBuildHash must return a nil hash on error so the caller rebuilds, got %+v", h)
	}
}

// Test_hashConfig_marshalErrorIsUnreachable documents why the hashConfig error
// path in computeBuildHash has no failure test of its own: tacplusyaml.Config
// is built exclusively from kinds json.Marshal cannot fail on, so the error is
// defensive only. This test fails if a field is added that CAN fail to marshal
// (chan, func, complex, an interface, or a non-string-keyed map) — at which
// point the propagation added in computeBuildHash becomes live and deserves a
// real failure test.
func Test_hashConfig_marshalErrorIsUnreachable(t *testing.T) {
	var marshalable func(reflect.Type, string)
	marshalable = func(typ reflect.Type, path string) {
		switch typ.Kind() {
		case reflect.String, reflect.Bool,
			reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
			reflect.Float32, reflect.Float64:
		case reflect.Ptr, reflect.Slice, reflect.Array:
			marshalable(typ.Elem(), path+"[]")
		case reflect.Map:
			if typ.Key().Kind() != reflect.String {
				t.Errorf("%s: map key %s can fail json.Marshal", path, typ.Key())
			}
			marshalable(typ.Elem(), path+"[]")
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				marshalable(f.Type, path+"."+f.Name)
			}
		default:
			t.Errorf("%s: kind %s can fail json.Marshal — the hashConfig error path in computeBuildHash is now reachable and needs a failure test", path, typ.Kind())
		}
	}
	marshalable(reflect.TypeOf(tacplusyaml.Config{}), "Config")
}
