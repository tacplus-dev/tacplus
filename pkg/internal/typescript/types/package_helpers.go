// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"path/filepath"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

// generatePackageHelpers generates package-specific helper files (wrappers, utilities, etc.)
func generatePackageHelpers(protocDir, outputDir string) error {
	// Use descriptor-based parser (robust, uses protoc internally)
	parser, err := parser.NewDescriptorParser(protocDir)
	if err != nil {
		return err
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
			return err
		}

		protoFiles = append(protoFiles, protoFile)
		return nil
	})

	if err != nil {
		return err
	}

	// Generate helpers for each relevant package
	for _, pf := range protoFiles {
		subdir := filepath.Dir(pf.RelPath)
		if subdir == "." {
			subdir = ""
		}

		// Generate error utils
		if pf.Package == "errors" {
			if err := generateErrorUtils(pf, outputDir, subdir); err != nil {
				return err
			}
		}

	}

	return nil
}
