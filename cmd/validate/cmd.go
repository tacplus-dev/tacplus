// Copyright (c) 2026 Second Star Cavalier LLC

package validate

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/protovalidate"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/registry"
)

var (
	buildDir string
)

var rootCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate proto files",
	Long: `Validate proto files in the build directory.

Validation includes:
  A) Parse and validate all proto files in the build directory
  B) Verify that registry annotations resolve cleanly

Examples:
  tacplus validate                              # Validate in current directory
  tacplus validate --build-dir /path/to/project # Validate specific directory
`,
	RunE: runValidate,
}

func init() {
	defaultBuildDir := os.Getenv("TACPLUS_BUILD_DIR")
	if defaultBuildDir == "" {
		defaultBuildDir = "."
	}

	rootCmd.Flags().StringVar(&buildDir, "build-dir", defaultBuildDir, "Directory containing proto/ (default: current directory, or TACPLUS_BUILD_DIR env var)")
}

// GetRootCmd returns the root command for use as a subcommand
func GetRootCmd() *cobra.Command {
	return rootCmd
}

func runValidate(cmd *cobra.Command, args []string) error {
	absBuildDir, err := filepath.Abs(buildDir)
	if err != nil {
		return fmt.Errorf("failed to resolve build directory: %w", err)
	}

	protoDir := filepath.Join(absBuildDir, "proto")
	if _, err := os.Stat(protoDir); os.IsNotExist(err) {
		return fmt.Errorf("proto directory not found: %s", protoDir)
	}

	// A) Validate proto files
	zap.L().Info("validating proto files", zap.String("dir", protoDir))
	count, err := protovalidate.ValidateProtoDirectory(protoDir)
	if err != nil {
		return fmt.Errorf("proto validation failed: %w", err)
	}
	zap.L().Info("proto files valid", zap.Int("count", count))

	// B) Registry completeness check
	zap.L().Info("checking registry completeness")
	if err := registry.Validate(protoDir); err != nil {
		return fmt.Errorf("registry validation failed: %w", err)
	}
	zap.L().Info("registry valid")
	return nil
}
