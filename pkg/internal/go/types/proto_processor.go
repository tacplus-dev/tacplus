// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// processProtoFiles walks through all proto files, parses them, and generates helper files
// This replicates the functionality of the builder tool
// Returns the parsed ProtoFile data for use in other generators
func processProtoFiles(protocDir, outputDir string, config *tactype.Config) ([]model.ProtoFile, error) {
	// Create output directory
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create output directory: %w", err)
	}

	// Use descriptor-based parser (robust, uses protoc internally)
	parser, err := parser.NewDescriptorParser(protocDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	// Collect all proto files
	var protoFiles []model.ProtoFile

	// Walk through all proto files
	err = filepath.Walk(protocDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}

		// Parse proto file using descriptor parser
		protoFile, err := parser.ConvertToProtoFile(path)
		if err != nil {
			return fmt.Errorf("failed to parse %s: %w", path, err)
		}

		if len(protoFile.Enums) > 0 || len(protoFile.Constants) > 0 || len(protoFile.Messages) > 0 {
			protoFiles = append(protoFiles, protoFile)
		}

		return nil
	})

	if err != nil {
		return nil, err
	}

	// Group proto files by output subdir so that multiple files sharing the
	// same package (e.g., spec.proto and tacplus.proto compile to Go package spec)
	// get their enums, messages, and constants merged into a single helpers.go.
	type subdirEntry struct {
		subdir string
		merged model.ProtoFile
	}
	subdirOrder := make([]string, 0)
	subdirMap := make(map[string]*subdirEntry)

	for _, pf := range protoFiles {
		// Skip well-known google protos — they don't need generated helpers
		if strings.HasPrefix(pf.RelPath, "google/") {
			continue
		}
		parentDir := filepath.Dir(pf.RelPath)
		var subdir string
		if filepath.Base(parentDir) == pf.Package {
			subdir = parentDir
		} else {
			subdir = filepath.Join(parentDir, pf.Package)
		}

		if existing, ok := subdirMap[subdir]; ok {
			// Merge into existing entry
			existing.merged.Enums = append(existing.merged.Enums, pf.Enums...)
			existing.merged.Messages = append(existing.merged.Messages, pf.Messages...)
			existing.merged.Constants = append(existing.merged.Constants, pf.Constants...)
		} else {
			subdirOrder = append(subdirOrder, subdir)
			subdirMap[subdir] = &subdirEntry{subdir: subdir, merged: pf}
		}
	}

	// Generate helper files for each merged proto
	for _, subdir := range subdirOrder {
		entry := subdirMap[subdir]
		if err := generateAllHelpers(entry.merged, outputDir, entry.subdir, config, protoFiles); err != nil {
			return nil, fmt.Errorf("failed to generate helpers for %s: %w", entry.merged.RelPath, err)
		}
	}

	return protoFiles, nil
}

// generateAllHelpers generates all helper files for a proto file.
// This generates a single helpers.go file with enums, serialization, validation, constants, and defaults.
// allFiles is the complete set of parsed proto files, used for cross-package type resolution
// in recursive LLM type descriptors.
func generateAllHelpers(pf model.ProtoFile, baseDir, subdir string, config *tactype.Config, allFiles []model.ProtoFile) error {
	if len(pf.Enums) > 0 || len(pf.Messages) > 0 || len(pf.Constants) > 0 || hasFieldDefaults(pf, messagesWithDefaults(allFiles)) {
		if err := generateGoHelpers(pf, baseDir, subdir, config, allFiles); err != nil {
			return fmt.Errorf("failed to generate Go helpers: %w", err)
		}
		zap.L().Debug("Generated helpers.go", zap.String("package", pf.Package))
	}

	return nil
}
