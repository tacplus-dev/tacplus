// Copyright (c) 2026 Second Star Cavalier LLC

package registry

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/registry"
)

var protoDir string

var rootCmd = &cobra.Command{
	Use:   "registry",
	Short: "Update the service/vservice registry from proto definitions",
	Long: `Scan proto files for (spec.service) and (spec.vservice) declarations
and update registry.yaml with stable ID assignments.

Run this before tagging the proto repo to ensure all services and vservices
have stable, non-overlapping field IDs in the unified registry.`,
	RunE: runRegistry,
}

func init() {
	rootCmd.Flags().StringVar(&protoDir, "proto-dir", ".", "Directory containing proto files")
}

// GetCmd returns the registry command for use as a subcommand.
func GetCmd() *cobra.Command {
	return rootCmd
}

var (
	serviceRe = regexp.MustCompile(`(?m)^option\s+\(spec\.service\)\s*=\s*\{[^}]*name:\s*"([^"]+)"`)
)

func runRegistry(cmd *cobra.Command, args []string) error {
	reg, err := registry.Load(protoDir)
	if err != nil {
		return fmt.Errorf("loading registry: %w", err)
	}

	// Scan services/ for (spec.service) declarations.
	servicesDir := filepath.Join(protoDir, "services")
	if err := scanDir(servicesDir, serviceRe, func(name string) error {
		_, err := reg.ServiceID(name)
		return err
	}); err != nil {
		return fmt.Errorf("scanning services: %w", err)
	}

	vserviceNames, err := registry.ScanVServiceNames(protoDir)
	if err != nil {
		return fmt.Errorf("scanning vservices: %w", err)
	}
	for _, name := range vserviceNames {
		zap.L().Debug("found vservice declaration", zap.String("name", name))
		if _, err := reg.VServiceFieldID(name); err != nil {
			return fmt.Errorf("assigning vservice %q: %w", name, err)
		}
	}

	if err := reg.Save(); err != nil {
		return fmt.Errorf("saving registry: %w", err)
	}

	// Print summary.
	fmt.Printf("Services (%d):\n", len(reg.Services))
	for _, s := range reg.Services {
		fmt.Printf("  %-20s = %d\n", s.Name, s.ID)
	}
	if len(reg.VServices) > 0 {
		fmt.Printf("VServices (%d):\n", len(reg.VServices))
		for _, vs := range reg.VServices {
			fmt.Printf("  %-20s = %d\n", vs.Name, vs.FieldID)
		}
	}

	return nil
}

func scanDir(dir string, re *regexp.Regexp, assign func(string) error) error {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	return filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		matches := re.FindAllStringSubmatch(string(data), -1)
		for _, m := range matches {
			if len(m) > 1 {
				zap.L().Debug("found declaration", zap.String("name", m[1]), zap.String("file", path))
				if err := assign(m[1]); err != nil {
					return err
				}
			}
		}
		return nil
	})
}
