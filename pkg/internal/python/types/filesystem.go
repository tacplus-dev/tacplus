// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
)

// generateFilesystemHelpers writes a filesystem_helpers.py file with
// directory_to_filesystem and directory_from_filesystem functions.
func generateFilesystemHelpers(outputDir, subdir string) error {
	helpersDir := filepath.Join(outputDir, subdir, "filesystem")
	if err := os.MkdirAll(helpersDir, 0755); err != nil {
		return err
	}
	outputPath := filepath.Join(helpersDir, "filesystem_helpers.py")

	var b strings.Builder

	b.WriteString(header.SimpleHeader(consts.LangPython))
	b.WriteString("# Source: sys/filesystem.proto\n\n")
	b.WriteString(`"""Filesystem helpers for reading/writing Directory proto trees to/from disk."""

import os
import stat
from pathlib import Path
from typing import Union

from .filesystem_pb2 import Permission, File, Directory

_SKIP_NAMES = frozenset({".git", ".svn", ".hg", ".DS_Store"})


def _should_skip_entry(entry: os.DirEntry) -> bool:
    """Return True for entries that should not be captured."""
    if entry.name in _SKIP_NAMES:
        return True
    # Skip macOS AppleDouble resource-fork sidecar files (e.g. "._main.rego").
    if entry.name.startswith("._"):
        return True
    # Skip non-regular, non-directory entries (symlinks, sockets, pipes, devices)
    if not entry.is_file(follow_symlinks=False) and not entry.is_dir(follow_symlinks=False):
        return True
    if entry.is_symlink():
        return True
    return False


def _mode_to_permission(mode: int) -> Permission:
    """Convert a Unix mode to a Permission message."""
    m = mode & 0o777
    return Permission(
        owner=(m >> 6) & 7,
        group=(m >> 3) & 7,
        other=m & 7,
    )


def _permission_to_mode(p: Permission | None) -> int:
    """Convert a Permission message to a Unix mode. Returns 0o755 if None."""
    if p is None:
        return 0o755
    return (p.owner << 6) | (p.group << 3) | p.other


def directory_from_filesystem(path: Union[str, Path]) -> Directory:
    """Read a real directory tree from disk and return a populated Directory
    proto message. Skips symlinks, sockets, pipes, device files, and known
    system dotfiles (.git, .DS_Store, etc.).
    """
    path = Path(path)
    if not path.is_dir():
        raise ValueError(f"{path} is not a directory")

    st = path.stat()
    dir_msg = Directory(
        name=path.name,
        permission=_mode_to_permission(st.st_mode),
    )

    with os.scandir(path) as entries:
        for entry in sorted(entries, key=lambda e: e.name):
            if _should_skip_entry(entry):
                continue
            full_path = path / entry.name

            if entry.is_dir(follow_symlinks=False):
                dir_msg.directories.append(directory_from_filesystem(full_path))
            else:
                file_stat = entry.stat(follow_symlinks=False)
                content = full_path.read_bytes()
                dir_msg.files.append(
                    File(
                        name=entry.name,
                        permission=_mode_to_permission(file_stat.st_mode),
                        content=content,
                    )
                )

    return dir_msg


def directory_to_filesystem(dir_msg: Directory, path: Union[str, Path]) -> None:
    """Write a Directory proto tree to disk, creating directories and files
    with the correct Unix permissions.
    """
    path = Path(path)
    mode = _permission_to_mode(dir_msg.permission)
    path.mkdir(parents=True, exist_ok=True)
    os.chmod(path, mode)

    for f in dir_msg.files:
        file_path = path / f.name
        file_path.write_bytes(f.content)
        os.chmod(file_path, _permission_to_mode(f.permission))

    for sub in dir_msg.directories:
        directory_to_filesystem(sub, path / sub.name)
`)

	return os.WriteFile(outputPath, []byte(b.String()), 0644)
}
