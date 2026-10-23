// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"fmt"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/protosrc"
	"github.com/tacplus-dev/tacplus/pkg/registry"
)

var (
	protoDir           string
	registerKind       string
	registerRepo       string
	registerVisibility string
)

var rootCmd = &cobra.Command{
	Use:   "registry",
	Short: "Update the service/vservice registry from proto definitions",
	Long: `Scan proto files for (spec.service) and (spec.vservice) declarations
and update registry.yaml with stable ID assignments.

Run this before tagging the proto repo to ensure all services and vservices
have stable, non-overlapping field IDs in the unified registry.

When registry.yaml names its authority with "global: <git URL or path>", it is
a lock: ids are synced from the global registry and never allocated here. A
declared service missing from the global registry is an error naming the
registration step (see "tacplus registry register").`,
	RunE: runRegistry,
}

var registerCmd = &cobra.Command{
	Use:   "register NAME",
	Short: "Register a service or vservice in the GLOBAL registry",
	Long: `Add NAME to the global registry in --proto-dir (a checkout of the
global registry repo) and allocate it the next global id. Commit the change
and merge it; consumers then sync their locks with "tacplus registry".

Ids are allocated upward from 101. The legacy range 1-99 is frozen.`,
	Args: cobra.ExactArgs(1),
	RunE: runRegister,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&protoDir, "proto-dir", ".", "Directory containing proto files (for register: the global registry checkout)")
	registerCmd.Flags().StringVar(&registerKind, "kind", "service", "service | vservice")
	registerCmd.Flags().StringVar(&registerRepo, "repo", "", "git URL of the proto repo that declares NAME (required)")
	registerCmd.Flags().StringVar(&registerVisibility, "visibility", registry.VisibilityPrivate, "public | private: whether that repo is public")
	_ = registerCmd.MarkFlagRequired("repo")
	rootCmd.AddCommand(registerCmd)
}

func runRegister(cmd *cobra.Command, args []string) error {
	reg, err := registry.Load(protoDir)
	if err != nil {
		return fmt.Errorf("loading global registry: %w", err)
	}
	if reg.Global != "" {
		return fmt.Errorf("%s/registry.yaml is a lock (global: %s), not the global registry; run register in a checkout of the global registry", protoDir, reg.Global)
	}
	id, err := reg.Register(registerKind, args[0], registerRepo, registerVisibility)
	if err != nil {
		return err
	}
	if err := reg.Save(); err != nil {
		return fmt.Errorf("saving global registry: %w", err)
	}
	fmt.Printf("%s %s = %d\n", registerKind, args[0], id)
	return nil
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
	if reg.Global != "" {
		return syncLock(reg, serviceNames, vserviceNames)
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

// syncLock brings a lock up to date with its global registry. The lock is
// saved even when some names are unregistered, so after registering them one
// re-run finishes the job.
func syncLock(lock *registry.Registry, serviceNames, vserviceNames []string) error {
	res, err := protosrc.Resolve(lock.Global, "latest")
	if err != nil {
		return fmt.Errorf("resolving global registry %s: %w", lock.Global, err)
	}
	defer res.Cleanup()
	global, err := registry.Load(res.Dir)
	if err != nil {
		return fmt.Errorf("loading global registry %s: %w", lock.Global, err)
	}
	syncErr := lock.SyncFromGlobal(global, serviceNames, vserviceNames)
	if err := lock.Save(); err != nil {
		return fmt.Errorf("saving registry lock: %w", err)
	}
	if syncErr != nil {
		return syncErr
	}
	fmt.Printf("Lock in sync with %s\n", lock.Global)
	for _, s := range lock.Services {
		fmt.Printf("  %-20s = %d\n", s.Name, s.ID)
	}
	for _, vs := range lock.VServices {
		fmt.Printf("  %-20s = %d (vservice)\n", vs.Name, vs.FieldID)
	}
	return nil
}
