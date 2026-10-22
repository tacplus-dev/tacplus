// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/tacplus-dev/tacplus/templates"
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

	return os.WriteFile(exceptionsPath, append([]byte(header.Copyright(consts.LangPython)), content...), 0644)
}
