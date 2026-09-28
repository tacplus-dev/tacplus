// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// generateExceptions generates a Python exceptions module that uses error.Error proto
func generateExceptions(outputDir, subdir string) error {
	exceptionsPath := filepath.Join(outputDir, subdir, "exceptions.py")

	typeFS, err := fs.Sub(templates.FS, "python/types")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}

	content, err := fs.ReadFile(typeFS, "exceptions.py.tmpl")
	if err != nil {
		return fmt.Errorf("failed to read exceptions template: %w", err)
	}

	return os.WriteFile(exceptionsPath, content, 0644)
}
