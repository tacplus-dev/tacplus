// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package fleet

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tacplus-dev/tacplus/pkg/fleet"
)

var (
	manifestDir string
	outDir      string
	check       bool
)

var rootCmd = &cobra.Command{
	Use:   "fleet",
	Short: "Build a fleet from a deployment repo's fleet.yaml",
}

var buildCmd = &cobra.Command{
	Use:   "build",
	Short: "Resolve, check and merge every service the fleet runs; write fleet.lock",
	Long: `Read fleet.yaml in the deployment repo: the registries the fleet trusts,
the tacplus floor, and every service it runs with the repo implementing it and
the release line it runs. Resolve each service's line (version: MAJOR.MINOR) to
its highest released vMAJOR.MINOR.PATCH tag, pre-releases excluded unless named
(version: MAJOR.MINOR.PATCH-pre), and to the image that release published.
Check each against the registries (same id, contract name declared) and against
the others (every declared dependency runs in the fleet at a satisfying
version), merge the protos into <out>/proto with the deployment repo's own
dependencies.yaml as the pins, run the whole-fleet checks, and write fleet.lock:
each service's exact version, commit and image digest.

Re-running it re-locks: a new patch of a stated line is taken; a new minor is a
fleet.yaml change. --check changes nothing: it lists, per service, a newer patch
(which re-locking takes) and a newer minor, and fails when re-locking would
change fleet.lock.

The merged tree is what the fleet-side services (the state host, the gateway,
the operator) are generated from: tacplus build --src <out>/proto.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if check {
			return runCheck()
		}
		res, err := fleet.Build(manifestDir, outDir)
		if err != nil {
			return err
		}
		fmt.Printf("fleet: %d services resolved; merged protos in %s; lock written\n", len(res.Lock.Services), res.ProtoDir)
		for _, s := range res.Lock.Services {
			fmt.Printf("  %-20s id %-5d %-12s %s @ %s\n", s.Name, s.ID, s.Version, s.Repo, short(s.Commit))
		}
		return nil
	},
}

// runCheck prints what is newer than fleet.lock and fails when re-locking
// would change it.
func runCheck() error {
	report, err := fleet.Check(manifestDir)
	if err != nil {
		return err
	}
	stale := 0
	for _, a := range report {
		locked := a.Locked
		if locked == "" {
			locked = "(not locked)"
		}
		var notes []string
		if a.Patch != "" {
			notes = append(notes, "newer patch "+a.Patch)
		}
		if a.Minor != "" {
			notes = append(notes, "newer minor "+a.Minor)
		}
		if len(notes) == 0 {
			notes = append(notes, "up to date")
		}
		if a.Stale() {
			stale++
		}
		fmt.Printf("  %-20s %-12s locked %-12s %s\n", a.Name, a.Version, locked, strings.Join(notes, ", "))
	}
	if stale > 0 {
		return fmt.Errorf("fleet: %d of %d services would move on re-lock; run tacplus fleet build and commit %s", stale, len(report), fleet.LockFile)
	}
	return nil
}

var (
	clientsOnly bool
	prebuilt    string
)

var gatewayCmd = &cobra.Command{
	Use:   "gateway",
	Short: "Build the fleet's gateway image, with the merged webapp and clients, from fleet.lock",
	Long: `Read fleet.lock (never re-resolving) and build the fleet's gateway image:
at the locked gateway release, the gateway generated against the merged protos
tacplus fleet build wrote under <out>/proto, its own webapp at /, every module
a service contributes at /-/apps/<service>/ with their index /-/apps.json, every
contributed client at /-/clients/<service>/<client>/<os>/<arch>, and the
launcher. Each part is built by its fixed recipe at its locked commit, the
image by tacplus-image-build at the gateway commit's time with rewritten
timestamps, into <out>/gateway/image.tar (an OCI layout); nothing is pushed.
It runs in the build image, named by digest in TACPLUS_BUILD_IMAGE, and
writes fleet.lock's gateway: section: every input and output by digest.

--check rebuilds and fails when any digest differs from the lock's, changing
nothing: building twice gives the same image.

The darwin/arm64 builds of rust: and swift: clients are the macos runner's:
there, --clients-only builds them into <out>/gateway/clients; pass that
directory to the Linux build as --prebuilt.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		g, err := fleet.Gateway(manifestDir, outDir, fleet.GatewayOptions{Check: check, ClientsOnly: clientsOnly, Prebuilt: prebuilt})
		if err != nil {
			return err
		}
		switch {
		case clientsOnly:
			fmt.Printf("fleet: this host's clients built in %s/gateway/clients\n", outDir)
		case check:
			fmt.Printf("fleet: the gateway rebuilt to the locked digests; image %s\n", g.Image)
		default:
			fmt.Printf("fleet: gateway %s image %s in %s/gateway/image.tar; lock written\n", g.Version, g.Image, outDir)
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
	buildCmd.Flags().BoolVar(&check, "check", false, "report newer patches and minors of every service, change nothing; fail when re-locking would change fleet.lock")
	rootCmd.AddCommand(buildCmd)

	gatewayCmd.Flags().StringVar(&manifestDir, "manifest-dir", ".", "deployment repo directory holding fleet.lock")
	gatewayCmd.Flags().StringVar(&outDir, "out", "build", "tacplus fleet build's output directory (the merged protos in <out>/proto); the gateway builds in <out>/gateway")
	gatewayCmd.Flags().BoolVar(&check, "check", false, "rebuild and fail when any digest differs from fleet.lock's gateway: section, changing nothing")
	gatewayCmd.Flags().BoolVar(&clientsOnly, "clients-only", false, "build only the clients this host builds (the macos runner's darwin/arm64), into <out>/gateway/clients; no image, no lock")
	gatewayCmd.Flags().StringVar(&prebuilt, "prebuilt", "", "a clients directory the macos runner built (its <out>/gateway/clients), for the targets this host does not build")
	rootCmd.AddCommand(gatewayCmd)
}

// GetCmd returns the fleet command for use as a subcommand.
func GetCmd() *cobra.Command { return rootCmd }
