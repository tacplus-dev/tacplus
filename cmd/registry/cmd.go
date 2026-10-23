// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package registry

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/protosrc"
	"github.com/tacplus-dev/tacplus/pkg/registry"
)

var (
	protoDir           string
	registerKind       string
	registerContract   string
	registerVisibility string
)

var rootCmd = &cobra.Command{
	Use:   "registry",
	Short: "Update the service/vservice registry from proto definitions",
	Long: `Scan proto files for (spec.service) and (spec.vservice) declarations
and update registry.yaml with stable ID assignments.

Run this before tagging the proto repo to ensure all services and vservices
have stable, non-overlapping field IDs in the unified registry.

When registry.yaml names its authorities with "registries: [<git URL or path>, ...]",
it is a lock: the authorities are unioned (a name or id in two of them is an
error) and ids are synced from the union, never allocated here. A declared
service missing from every authority is an error naming the registration step
(see "tacplus registry register").`,
	RunE: runRegistry,
}

var registerCmd = &cobra.Command{
	Use:   "register NAME",
	Short: "Register a service or vservice in an authority registry",
	Long: `Add NAME to the authority in --proto-dir (a checkout of a registry repo
whose registry.yaml declares "scope: global" or "scope: private") and allocate
it the next id in that scope's range: global 101-1023, private 1024-2047. The
legacy range 1-99 is frozen. --contract is the home of NAME's proto; it may be
any repo, public or not. Commit the change and merge it; consumers then sync
their locks with "tacplus registry".`,
	Args: cobra.ExactArgs(1),
	RunE: runRegister,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&protoDir, "proto-dir", ".", "Directory containing proto files (for register: the global registry checkout)")
	registerCmd.Flags().StringVar(&registerKind, "kind", "service", "service | vservice")
	registerCmd.Flags().StringVar(&registerContract, "contract", "", "git URL of the repo that holds NAME's proto (required)")
	registerCmd.Flags().StringVar(&registerVisibility, "visibility", registry.VisibilityPrivate, "public | private: a note on who the contract is for")
	_ = registerCmd.MarkFlagRequired("contract")
	rootCmd.AddCommand(registerCmd)
}

func runRegister(cmd *cobra.Command, args []string) error {
	reg, err := registry.Load(protoDir)
	if err != nil {
		return fmt.Errorf("loading registry: %w", err)
	}
	id, err := reg.Register(registerKind, args[0], registerContract, registerVisibility)
	if err != nil {
		return err
	}
	if err := reg.Save(); err != nil {
		return fmt.Errorf("saving registry: %w", err)
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
	if reg.IsLock() {
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

// syncLock brings a lock up to date with the union of its authorities. The
// lock is saved even when some names are unregistered, so after registering
// them one re-run finishes the job.
func syncLock(lock *registry.Registry, serviceNames, vserviceNames []string) error {
	union, cleanup, err := registry.LoadAuthorities(lock.Registries, resolveRegistry)
	if err != nil {
		return err
	}
	defer cleanup()
	syncErr := lock.SyncFromGlobal(union, serviceNames, vserviceNames)
	if err := lock.Save(); err != nil {
		return fmt.Errorf("saving registry lock: %w", err)
	}
	if syncErr != nil {
		return syncErr
	}
	fmt.Printf("Lock in sync with %s\n", strings.Join(lock.Registries, ", "))
	for _, s := range lock.Services {
		fmt.Printf("  %-20s = %d\n", s.Name, s.ID)
	}
	for _, vs := range lock.VServices {
		fmt.Printf("  %-20s = %d (vservice)\n", vs.Name, vs.FieldID)
	}
	return nil
}

// resolveRegistry fetches a registry reference (git URL or local path) at its
// default branch: an authority is append-only, so HEAD is the right version.
func resolveRegistry(ref string) (string, func(), error) {
	res, err := protosrc.Resolve(ref, "latest")
	if err != nil {
		return "", nil, err
	}
	return res.Dir, res.Cleanup, nil
}
