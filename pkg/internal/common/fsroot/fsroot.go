// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Package fsroot confines the compiler's file operations to the tree each one
// is meant for (the generated output, the staged proto copy, a fleet
// repository, a fetched checkout) with os.Root: a symlink under the tree that
// leads out of it makes the operation fail instead of following it (#754).
// Walk walks a directory tree confined to it (#753).
//
// Every tree the compiler walks (the staged proto copy the passes rewrite, the
// generated output the generators post-process, a fetched registry or
// `depends` checkout, the verifiers' harness output) is walked here, never with
// filepath.Walk or filepath.WalkDir (cmd/soundness refuses those under pkg/).
// The callback touches what it finds only through the *os.Root it is handed, by
// the path relative to the root: a symlink that resolves outside the tree, or
// an entry swapped for one between the walk's read of its directory and the
// operation, fails with an error instead of reading or writing outside it. A
// symlink that stays inside the tree is followed.
package fsroot

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// WalkFunc is called for each entry of the tree, the root first as ".". rel is
// the entry's path relative to the root, in the OS's separators (what
// filepath.Rel(root, path) gave the callbacks of filepath.Walk); d is its
// directory entry, a symlink reported as a symlink, never followed by the walk
// itself. err is as in fs.WalkDirFunc. r is nil only when the root itself
// could not be opened: the callback is then called once, with rel ".", d nil
// and that error. Returning fs.SkipDir or fs.SkipAll works as in fs.WalkDir.
type WalkFunc func(r *os.Root, rel string, d fs.DirEntry, err error) error

// Walk walks the tree rooted at root in lexical order, calling fn for each
// entry, through an os.Root opened on root for the length of the walk.
func Walk(root string, fn WalkFunc) error {
	r, err := os.OpenRoot(root)
	if err != nil {
		if err := fn(nil, ".", nil, err); err != nil && !errors.Is(err, fs.SkipDir) && !errors.Is(err, fs.SkipAll) {
			return err
		}
		return nil
	}
	walkErr := fs.WalkDir(r.FS(), ".", func(p string, d fs.DirEntry, err error) error {
		return fn(r, filepath.FromSlash(p), d, err)
	})
	if closeErr := r.Close(); walkErr == nil {
		walkErr = closeErr
	}
	return walkErr
}
