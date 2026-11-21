// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package fsroot

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Rel is path relative to dir, refused if it leaves dir. path may be absolute
// or relative to the working directory, like dir.
func Rel(dir, path string) (string, error) {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%s is not under %s", path, dir)
	}
	return rel, nil
}

// Open opens dir as an os.Root, creating it first if it is missing.
func Open(dir string) (*os.Root, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return os.OpenRoot(dir)
}

// WriteFile writes data to path, which must be under dir, through an os.Root
// opened at dir.
func WriteFile(dir, path string, data []byte, perm fs.FileMode) error {
	rel, err := Rel(dir, path)
	if err != nil {
		return err
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer r.Close()
	return r.WriteFile(rel, data, perm)
}

// MkdirAll creates path, which must be under dir, and its missing parents,
// dir among them, through an os.Root opened at dir.
func MkdirAll(dir, path string, perm fs.FileMode) error {
	rel, err := Rel(dir, path)
	if err != nil {
		return err
	}
	r, err := Open(dir)
	if err != nil {
		return err
	}
	defer r.Close()
	return r.MkdirAll(rel, perm)
}
