// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
)

// generateFilesystemHelpers writes DirectoryFromFileSystem and DirectoryToFileSystem
// helpers into the Go helpers.go output for the filesystem package.
func generateFilesystemHelpers(b *strings.Builder) {
	b.WriteString("// ============================================================\n")
	b.WriteString("// Filesystem Helpers\n")
	b.WriteString("// ============================================================\n\n")

	// shouldSkip helper
	b.WriteString(`// shouldSkipEntry returns true for entries that should not be captured:
// symlinks, sockets, pipes, device files, and known system dotfiles.
func shouldSkipEntry(entry os.DirEntry) bool {
	name := entry.Name()
	// Skip known system dotfiles/directories
	switch name {
	case ".git", ".svn", ".hg", ".DS_Store":
		return true
	}
	// Skip non-regular, non-directory entries (symlinks, sockets, pipes, devices)
	if entry.Type()&os.ModeType != 0 && !entry.IsDir() {
		return true
	}
	return false
}

`)

	// modeToPermission
	b.WriteString(`// modeToPermission converts an os.FileMode to a Permission message.
func modeToPermission(mode os.FileMode) *Permission {
	return &Permission{
		Owner: uint32((mode >> 6) & 7),
		Group: uint32((mode >> 3) & 7),
		Other: uint32(mode & 7),
	}
}

`)

	// permissionToMode
	b.WriteString(`// permissionToMode converts a Permission message to an os.FileMode.
// Returns 0755 if p is nil.
func permissionToMode(p *Permission) os.FileMode {
	if p == nil {
		return 0755
	}
	return os.FileMode((p.Owner << 6) | (p.Group << 3) | p.Other)
}

`)

	// DirectoryFromFileSystem
	b.WriteString(`// DirectoryFromFileSystem reads a real directory tree from disk and returns
// a populated Directory proto message. It skips symlinks, sockets, pipes,
// device files, and known system dotfiles (.git, .DS_Store, etc.).
func DirectoryFromFileSystem(path string) (*Directory, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", path)
	}

	dir := &Directory{
		Name:       info.Name(),
		Permission: modeToPermission(info.Mode()),
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("readdir %s: %w", path, err)
	}

	for _, entry := range entries {
		if shouldSkipEntry(entry) {
			continue
		}
		fullPath := filepath.Join(path, entry.Name())

		if entry.IsDir() {
			subDir, err := DirectoryFromFileSystem(fullPath)
			if err != nil {
				return nil, err
			}
			dir.Directories = append(dir.Directories, subDir)
		} else {
			fi, err := entry.Info()
			if err != nil {
				return nil, fmt.Errorf("info %s: %w", fullPath, err)
			}
			content, err := os.ReadFile(fullPath)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", fullPath, err)
			}
			dir.Files = append(dir.Files, &File{
				Name:       entry.Name(),
				Permission: modeToPermission(fi.Mode()),
				Content:    content,
			})
		}
	}

	return dir, nil
}

`)

	// DirectoryToFileSystem
	b.WriteString(`// DirectoryToFileSystem writes a Directory proto tree to disk, creating
// directories and files with the correct Unix permissions.
func DirectoryToFileSystem(dir *Directory, path string) error {
	mode := permissionToMode(dir.Permission)
	if err := os.MkdirAll(path, mode); err != nil {
		return fmt.Errorf("mkdir %s: %w", path, err)
	}

	for _, f := range dir.Files {
		fPath := filepath.Join(path, f.Name)
		fMode := permissionToMode(f.Permission)
		if err := os.WriteFile(fPath, f.Content, fMode); err != nil {
			return fmt.Errorf("write %s: %w", fPath, err)
		}
	}

	for _, sub := range dir.Directories {
		if err := DirectoryToFileSystem(sub, filepath.Join(path, sub.Name)); err != nil {
			return err
		}
	}

	return nil
}
`)
}
