// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"github.com/tacplus-dev/tacplus/templates"
)

// errorCheckerEntry holds an error checker for template rendering.
type errorCheckerEntry struct {
	Name string
	Code string
}

// errorHelpersData holds the template data for error_helpers.ts.tmpl.
type errorHelpersData struct {
	Header        string
	RelPath       string
	ErrorCheckers []errorCheckerEntry
}

// generateErrorUtils generates api_errors.ts for the errors package.
// This creates the ApiError class, error checking utility functions, and
// the newError HTTP response parser.
//
// Note: The newErr* validation error constructors (newErrMissingInvalidData, etc.)
// are provided by the ErrorsTs template in errors.ts, which returns throwable
// ValidationError objects. This file provides API-level error handling using
// the proto-generated Error type.
func generateErrorUtils(pf model.ProtoFile, baseDir, subdir string) error {
	// Only generate for errors package
	if pf.Package != "errors" {
		return nil
	}

	// Write to api_errors.ts (not helpers.ts) to avoid overwriting parser-generated helpers.
	// sys/ goes directly under output root; other categories under types/.
	protoFilename := strings.TrimSuffix(filepath.Base(pf.RelPath), ".proto")
	// All proto-derived types (including the sys/ declarative primitives) live
	// under types/; only runtime machinery lives outside it.
	outputPath := filepath.Join(baseDir, "typescript", "types", subdir, protoFilename, "api_errors.ts")
	outputDir := filepath.Dir(outputPath)

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}

	data := errorHelpersData{
		Header:  header.SimpleHeader(consts.LangTypeScript),
		RelPath: pf.RelPath,
		ErrorCheckers: []errorCheckerEntry{
			{"MissingInvalidData", "INVALID_ARGUMENT"},
			{"NotFound", "NOT_FOUND"},
			{"Expired", "DEADLINE_EXCEEDED"},
			{"General", "INTERNAL"},
			{"Unauthorized", "UNAUTHENTICATED"},
			{"Cancelled", "CANCELLED"},
		},
	}

	typeFS, err := fs.Sub(templates.FS, "typescript/types")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}

	tmplContent, err := fs.ReadFile(typeFS, "error_helpers.ts.tmpl")
	if err != nil {
		return fmt.Errorf("failed to read error_helpers template: %w", err)
	}

	tmpl, err := template.New("error_helpers").Parse(string(tmplContent))
	if err != nil {
		return fmt.Errorf("failed to parse error_helpers template: %w", err)
	}

	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, data); err != nil {
		return fmt.Errorf("failed to execute error_helpers template: %w", err)
	}

	return nil
}
