// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/tacplus-dev/tacplus/pkg/fleet"
)

var (
	manifestDir string
	outDir      string
)

var rootCmd = &cobra.Command{
	Use:   "fleet",
	Short: "Build a fleet from a deployment repo's fleet.yaml",
}

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Resolve, check and merge every service the fleet runs; write fleet.lock",
	Long: `Read fleet.yaml in the deployment repo: the registries the fleet trusts,
the tacplus floor, and every service it runs with the repo implementing it.
Resolve each implementation at its MAJOR.MINOR (latest patch), check it against
the registries (same id, contract name declared) and against the others (every
declared dependency runs in the fleet at a satisfying version), merge the protos
into <out>/proto with the deployment repo's own dependencies.yaml as the pins,
run the whole-fleet checks, and write fleet.lock with the exact commits.

The merged tree is what the fleet-side services (the state host, the gateway,
the operator) are generated from: tacplus build --src <out>/proto.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		res, err := fleet.Build(manifestDir, outDir)
		if err != nil {
			return err
		}
		fmt.Printf("fleet: %d services resolved; merged protos in %s; lock written\n", len(res.Lock.Services), res.ProtoDir)
		for _, s := range res.Lock.Services {
			fmt.Printf("  %-20s id %-5d %s @ %s\n", s.Name, s.ID, s.Repo, short(s.Commit))
		}
		return nil
	},
}

func short(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

func init() {
	buildCmd.Flags().StringVar(&manifestDir, "manifest-dir", ".", "deployment repo directory holding fleet.yaml and dependencies.yaml")
	buildCmd.Flags().StringVar(&outDir, "out", "build", "output directory (merged protos land in <out>/proto)")
	rootCmd.AddCommand(buildCmd)
}

// GetCmd returns the fleet command for use as a subcommand.
func GetCmd() *cobra.Command { return rootCmd }
