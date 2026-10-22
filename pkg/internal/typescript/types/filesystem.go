// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
)

// generateFilesystemHelpers writes a filesystem_helpers.ts file with
// directoryToFileSystem and directoryFromFileSystem functions.
func generateFilesystemHelpers(baseDir, subdir string) error {
	outputDir := filepath.Join(baseDir, "typescript", subdir, "filesystem")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}
	outputPath := filepath.Join(outputDir, "filesystem_helpers.ts")

	var b strings.Builder

	b.WriteString(header.SimpleHeader(consts.LangTypeScript))
	b.WriteString("// Source: sys/filesystem.proto\n\n")
	b.WriteString(`import * as fs from "fs";
import * as path from "path";
import { create } from "@bufbuild/protobuf";
import { Permission, PermissionSchema, File, FileSchema, Directory, DirectorySchema } from "./filesystem_pb.js";

const SKIP_NAMES = new Set([".git", ".svn", ".hg", ".DS_Store"]);

function shouldSkipEntry(entry: fs.Dirent): boolean {
  if (SKIP_NAMES.has(entry.name)) {
    return true;
  }
  // Skip macOS AppleDouble resource-fork sidecar files (e.g. "._main.rego").
  if (entry.name.startsWith("._")) {
    return true;
  }
  // Skip non-regular, non-directory entries (symlinks, sockets, pipes, devices)
  if (!entry.isFile() && !entry.isDirectory()) {
    return true;
  }
  return false;
}

function modeToPermission(mode: number): Permission {
  const m = mode & 0o777;
  return create(PermissionSchema, {
    owner: (m >> 6) & 7,
    group: (m >> 3) & 7,
    other: m & 7,
  });
}

function permissionToMode(p: Permission | undefined): number {
  if (!p) {
    return 0o755;
  }
  return (p.owner << 6) | (p.group << 3) | p.other;
}

/**
 * directoryFromFileSystem reads a real directory tree from disk and returns
 * a populated Directory proto message. Skips symlinks, sockets, pipes,
 * device files, and known system dotfiles (.git, .DS_Store, etc.).
 */
export function directoryFromFileSystem(dirPath: string): Directory {
  const stat = fs.statSync(dirPath);
  if (!stat.isDirectory()) {
    throw new Error(` + "`${dirPath} is not a directory`" + `);
  }

  const dir = create(DirectorySchema, {
    name: path.basename(dirPath),
    permission: modeToPermission(stat.mode),
    directories: [],
    files: [],
  });

  const entries = fs.readdirSync(dirPath, { withFileTypes: true });
  for (const entry of entries) {
    if (shouldSkipEntry(entry)) {
      continue;
    }
    const fullPath = path.join(dirPath, entry.name);

    if (entry.isDirectory()) {
      dir.directories.push(directoryFromFileSystem(fullPath));
    } else {
      const fileStat = fs.statSync(fullPath);
      const content = fs.readFileSync(fullPath);
      dir.files.push(
        create(FileSchema, {
          name: entry.name,
          permission: modeToPermission(fileStat.mode),
          content: new Uint8Array(content),
        })
      );
    }
  }

  return dir;
}

/**
 * directoryToFileSystem writes a Directory proto tree to disk, creating
 * directories and files with the correct Unix permissions.
 */
export function directoryToFileSystem(dir: Directory, dirPath: string): void {
  const mode = permissionToMode(dir.permission);
  fs.mkdirSync(dirPath, { recursive: true, mode });

  for (const f of dir.files) {
    const filePath = path.join(dirPath, f.name);
    const fileMode = permissionToMode(f.permission);
    fs.writeFileSync(filePath, f.content, { mode: fileMode });
  }

  for (const sub of dir.directories) {
    directoryToFileSystem(sub, path.join(dirPath, sub.name));
  }
}
`)

	return os.WriteFile(outputPath, []byte(b.String()), 0644)
}
