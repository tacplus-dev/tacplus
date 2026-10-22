// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
)

// writeFilesystemHelpers writes directory_to_filesystem and directory_from_filesystem
// functions into the Rust helpers.rs output for the filesystem package.
func writeFilesystemHelpers(b *strings.Builder) {
	b.WriteString("// ============ Filesystem Helpers ============\n\n")

	b.WriteString(`use std::fs;
use std::path::Path;
use std::io;

#[cfg(unix)]
use std::os::unix::fs::PermissionsExt;

const SKIP_NAMES: &[&str] = &[".git", ".svn", ".hg", ".DS_Store"];

fn should_skip_entry(entry: &fs::DirEntry) -> bool {
    let name = entry.file_name();
    let name_str = name.to_string_lossy();
    if SKIP_NAMES.contains(&name_str.as_ref()) {
        return true;
    }
    // Skip macOS AppleDouble resource-fork sidecar files (e.g. "._main.rego").
    if name_str.starts_with("._") {
        return true;
    }
    // Skip non-regular, non-directory entries (symlinks, sockets, pipes, devices)
    match entry.file_type() {
        Ok(ft) => !ft.is_file() && !ft.is_dir(),
        Err(_) => true,
    }
}

fn mode_to_permission(mode: u32) -> super::Permission {
    let m = mode & 0o777;
    super::Permission {
        owner: (m >> 6) & 7,
        group: (m >> 3) & 7,
        other: m & 7,
    }
}

fn permission_to_mode(p: &Option<super::Permission>) -> u32 {
    match p {
        Some(p) => (p.owner << 6) | (p.group << 3) | p.other,
        None => 0o755,
    }
}

/// Reads a real directory tree from disk and returns a populated Directory
/// proto message. Skips symlinks, sockets, pipes, device files, and known
/// system dotfiles (.git, .DS_Store, etc.).
pub fn directory_from_filesystem(path: &Path) -> io::Result<super::Directory> {
    if !path.is_dir() {
        return Err(io::Error::new(
            io::ErrorKind::InvalidInput,
            format!("{} is not a directory", path.display()),
        ));
    }

    let metadata = fs::metadata(path)?;
    #[cfg(unix)]
    let mode = metadata.permissions().mode();
    #[cfg(not(unix))]
    let mode = 0o755u32;

    let name = path
        .file_name()
        .map(|n| n.to_string_lossy().into_owned())
        .unwrap_or_default();

    let mut dir = super::Directory {
        name,
        permission: Some(mode_to_permission(mode)),
        directories: Vec::new(),
        files: Vec::new(),
    };

    let mut entries: Vec<fs::DirEntry> = fs::read_dir(path)?
        .filter_map(|e| e.ok())
        .collect();
    entries.sort_by_key(|e| e.file_name());

    for entry in entries {
        if should_skip_entry(&entry) {
            continue;
        }
        let full_path = entry.path();

        if full_path.is_dir() {
            dir.directories.push(directory_from_filesystem(&full_path)?);
        } else {
            let file_meta = fs::metadata(&full_path)?;
            #[cfg(unix)]
            let file_mode = file_meta.permissions().mode();
            #[cfg(not(unix))]
            let file_mode = 0o644u32;

            let content = fs::read(&full_path)?;
            dir.files.push(super::File {
                name: entry.file_name().to_string_lossy().into_owned(),
                permission: Some(mode_to_permission(file_mode)),
                content,
            });
        }
    }

    Ok(dir)
}

/// Writes a Directory proto tree to disk, creating directories and files
/// with the correct Unix permissions.
pub fn directory_to_filesystem(dir: &super::Directory, path: &Path) -> io::Result<()> {
    let mode = permission_to_mode(&dir.permission);
    fs::create_dir_all(path)?;
    #[cfg(unix)]
    fs::set_permissions(path, fs::Permissions::from_mode(mode))?;

    for f in &dir.files {
        let file_path = path.join(&f.name);
        fs::write(&file_path, &f.content)?;
        #[cfg(unix)]
        {
            let file_mode = permission_to_mode(&f.permission);
            fs::set_permissions(&file_path, fs::Permissions::from_mode(file_mode))?;
        }
    }

    for sub in &dir.directories {
        directory_to_filesystem(sub, &path.join(&sub.name))?;
    }

    Ok(())
}
`)
}
