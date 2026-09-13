// Copyright (c) 2026 Second Star Cavalier LLC

package registry

import (
	"fmt"

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

func runRegistry(cmd *cobra.Command, args []string) error {
	reg, err := registry.Load(protoDir)
	if err != nil {
		return fmt.Errorf("loading registry: %w", err)
	}

	// Discover declarations anywhere in the tree, the way generation does — a
	// service outside services/ must still be assigned an ID.
	serviceNames, vserviceNames, err := registry.ScanRegistryNames(protoDir)
	if err != nil {
		return fmt.Errorf("scanning proto declarations: %w", err)
	}
	for _, name := range serviceNames {
		zap.L().Debug("found service declaration", zap.String("name", name))
		if _, err := reg.ServiceID(name); err != nil {
			return fmt.Errorf("assigning service %q: %w", name, err)
		}
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
