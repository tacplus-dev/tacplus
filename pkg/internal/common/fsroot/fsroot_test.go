// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package fsroot

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func symlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
}

// readAll walks root and reads every non-directory entry through the root,
// the way the compiler's walks read what they find.
func readAll(root string) (map[string]string, error) {
	got := map[string]string{}
	err := Walk(root, func(r *os.Root, rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		b, err := r.ReadFile(rel)
		if err != nil {
			return err
		}
		got[rel] = string(b)
		return nil
	})
	return got, err
}

// TestWalkRefusesSymlinkOutOfRoot: a symlink in the walked tree that points
// outside it (a crafted dependency checkout naming a host file like a proto,
// tacplus#753) is never followed: reading it fails, and the walk reports it.
func TestWalkRefusesSymlinkOutOfRoot(t *testing.T) {
	outside := t.TempDir()
	write(t, filepath.Join(outside, "secret.txt"), "host secret")
	root := t.TempDir()
	write(t, filepath.Join(root, "a.proto"), "syntax = \"proto3\";")
	symlink(t, filepath.Join(outside, "secret.txt"), filepath.Join(root, "leak.proto"))

	got, err := readAll(root)
	if err == nil {
		t.Fatalf("walk read a symlink out of its root: %v", got)
	}
	for rel, content := range got {
		if content == "host secret" {
			t.Fatalf("walk read %s from outside its root", rel)
		}
	}
}

// TestWalkRefusesRelativeSymlinkOutOfRoot: the same through a relative target
// that climbs out of the root.
func TestWalkRefusesRelativeSymlinkOutOfRoot(t *testing.T) {
	parent := t.TempDir()
	write(t, filepath.Join(parent, "secret.txt"), "host secret")
	root := filepath.Join(parent, "tree")
	write(t, filepath.Join(root, "sub", "a.proto"), "a")
	symlink(t, filepath.Join("..", "..", "secret.txt"), filepath.Join(root, "sub", "leak.proto"))

	if got, err := readAll(root); err == nil {
		t.Fatalf("walk read a symlink out of its root: %v", got)
	}
}

// TestWalkFollowsInTreeSymlink: a symlink to a regular file inside the tree is
// read through, and every entry is handed over relative to the root.
func TestWalkFollowsInTreeSymlink(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "a", "real.proto"), "real")
	symlink(t, filepath.Join("a", "real.proto"), filepath.Join(root, "link.proto"))

	got, err := readAll(root)
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	want := map[string]string{
		filepath.Join("a", "real.proto"): "real",
		"link.proto":                     "real",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestWalkOrderAndRoot: entries come in lexical order, the root first as ".",
// and SkipDir skips a directory as filepath.Walk's callers expect.
func TestWalkOrderAndRoot(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "b.txt"), "")
	write(t, filepath.Join(root, "a", "x.txt"), "")
	write(t, filepath.Join(root, "skip", "y.txt"), "")

	var seen []string
	err := Walk(root, func(r *os.Root, rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		seen = append(seen, rel)
		if d.IsDir() && rel == "skip" {
			return fs.SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	want := []string{".", "a", filepath.Join("a", "x.txt"), "b.txt", "skip"}
	if !reflect.DeepEqual(seen, want) {
		t.Fatalf("got %v, want %v", seen, want)
	}
}

// TestWalkMissingRoot: a root that cannot be opened is handed to the callback
// as the error for ".", as filepath.Walk hands it the root's Lstat error, so a
// caller that tolerates a missing tree still can.
func TestWalkMissingRoot(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent")
	var calls int
	err := Walk(missing, func(r *os.Root, rel string, d fs.DirEntry, err error) error {
		calls++
		if rel != "." || r != nil || d != nil || !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("callback got (%v, %q, %v, %v)", r, rel, d, err)
		}
		return nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
	if err := Walk(missing, func(*os.Root, string, fs.DirEntry, error) error { return fs.SkipDir }); err != nil {
		t.Fatalf("SkipDir on the root: %v", err)
	}
	if err := Walk(missing, func(_ *os.Root, _ string, _ fs.DirEntry, err error) error { return err }); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("propagated error: %v", err)
	}
}

// TestWalkWritesInsideRoot: a callback rewrites what it walks through the root.
func TestWalkWritesInsideRoot(t *testing.T) {
	root := t.TempDir()
	write(t, filepath.Join(root, "d", "f.txt"), "old")
	err := Walk(root, func(r *os.Root, rel string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		return r.WriteFile(rel, []byte("new"), 0o644)
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if b, _ := os.ReadFile(filepath.Join(root, "d", "f.txt")); string(b) != "new" {
		t.Fatalf("got %q", b)
	}
}
