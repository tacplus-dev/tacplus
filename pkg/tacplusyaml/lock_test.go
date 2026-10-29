// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package tacplusyaml

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestLockValidate(t *testing.T) {
	image := "ghcr.io/tacplus-dev/tacplus/build:2.6.1@" + testDigest
	cases := []struct {
		name    string
		lock    Lock
		wantErr string // substring; "" = valid
	}{
		{"exact version and digest", Lock{Tacplus: "2.6.1", BuildImage: image}, ""},
		{"digest without a tag", Lock{Tacplus: "2.6.1", BuildImage: "ghcr.io/tacplus-dev/tacplus/build@" + testDigest}, ""},
		{"line, not a version", Lock{Tacplus: "2.6", BuildImage: image}, "exact MAJOR.MINOR.PATCH"},
		{"v prefix", Lock{Tacplus: "v2.6.1", BuildImage: image}, "exact MAJOR.MINOR.PATCH"},
		{"pre-release", Lock{Tacplus: "2.6.1-rc.1", BuildImage: image}, "exact MAJOR.MINOR.PATCH"},
		{"missing version", Lock{BuildImage: image}, "exact MAJOR.MINOR.PATCH"},
		{"tag, no digest", Lock{Tacplus: "2.6.1", BuildImage: "ghcr.io/tacplus-dev/tacplus/build:2.6"}, "by digest"},
		{"missing image", Lock{Tacplus: "2.6.1"}, "by digest"},
		{"short digest", Lock{Tacplus: "2.6.1", BuildImage: "ghcr.io/tacplus-dev/tacplus/build@sha256:0123"}, "by digest"},
		{"image of another line", Lock{Tacplus: "2.6.1", BuildImage: "ghcr.io/tacplus-dev/tacplus/build:2.5.0@" + testDigest}, "line 2.5"},
	}
	for _, c := range cases {
		err := c.lock.Validate()
		switch {
		case c.wantErr == "" && err != nil:
			t.Errorf("%s: Validate() = %v, want valid", c.name, err)
		case c.wantErr != "" && (err == nil || !strings.Contains(err.Error(), c.wantErr)):
			t.Errorf("%s: Validate() = %v, want error containing %q", c.name, err, c.wantErr)
		}
	}
}

func TestLockRoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := Lock{Tacplus: "2.6.1", BuildImage: "ghcr.io/tacplus-dev/tacplus/build:2.6.1@" + testDigest}
	if err := WriteLock(dir, want); err != nil {
		t.Fatal(err)
	}
	got, err := ReadLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	if *got != want {
		t.Fatalf("ReadLock = %+v, want %+v", *got, want)
	}
	// images/tacplus.sh reads the file with sed, not a YAML parser: each key
	// must be a plain `key: value` line of its own.
	data, err := os.ReadFile(filepath.Join(dir, LockFilename))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{"tacplus: 2.6.1", "build_image: " + want.BuildImage} {
		if !strings.Contains("\n"+string(data), "\n"+line+"\n") {
			t.Errorf("%s has no line %q:\n%s", LockFilename, line, data)
		}
	}
}

func TestReadLockMissing(t *testing.T) {
	_, err := ReadLock(t.TempDir())
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ReadLock of a directory with no lock = %v, want os.ErrNotExist", err)
	}
}

func TestReadLockRefusesInvalid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, LockFilename), []byte("tacplus: \"2.6\"\nbuild_image: ghcr.io/tacplus-dev/tacplus/build:2.6\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLock(dir); err == nil || !strings.Contains(err.Error(), "exact MAJOR.MINOR.PATCH") {
		t.Fatalf("ReadLock of a line pin = %v, want refusal", err)
	}
}

func TestWriteLockRefusesInvalid(t *testing.T) {
	dir := t.TempDir()
	if err := WriteLock(dir, Lock{Tacplus: "2.6", BuildImage: "x"}); err == nil {
		t.Fatal("WriteLock of an invalid lock succeeded")
	}
	if _, err := os.Stat(filepath.Join(dir, LockFilename)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an invalid lock was written: %v", err)
	}
}
