// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package build

import (
	"github.com/spf13/cobra"

	"github.com/tacplus-dev/tacplus/pkg/runner"
	"github.com/tacplus-dev/tacplus/pkg/tacplusyaml"
)

var (
	protoTag    string
	verbose     bool
	protoSrcs   []string
	configFiles []string
)

var rootCmd = &cobra.Command{
	Use:   "build",
	Short: "Generate proto types and services for a single language",
	Long: `Build generates proto types and services for a single language.

The proto source (specified via --src) can be:
  - A local directory path containing proto files
  - A git repository URL (HTTPS or SSH) — cloned automatically
  - A short repo reference like gitlab.com/org/repo.git — HTTPS is prepended

The resolved directory must contain:
  - *.proto            Proto definition files
  - dependencies.yaml  External dependency versions and configuration

Multiple proto sources:
  --src is repeatable; tacplus.yaml also accepts a "sources:" list alongside
  "src:". All sources are merged into one proto tree for a single generation,
  so a shared base and a reusable service's protos can live in separate
  sources. A file at the same path in two sources must be byte-identical (it is
  deduplicated); a genuine conflict is a hard error — factor the shared file
  into a single source. dependencies.yaml is merged across sources (conflicting
  pins are an error).
  Name a source's layer in tacplus.yaml ("layer:" beside src, or a
  {src, layer} entry in "sources:") and each registry.json service and grant
  from it carries that name as "source".

If tacplus.yaml exists in the current directory, configuration is read from it
automatically. Flags are optional overrides.

The proto version tag is auto-detected from the git tag of the current repo.
Use --tag to override. When the proto source is a git URL, the tag's
MAJOR.MINOR is used to find the latest matching proto tag (e.g. v1.2.* ).
  - "latest"   Uses HEAD (local dev mode, version 0.0.0-SNAPSHOT)
  - "1.2"      Finds the latest v1.2.* tag
  - "1.2.3"    Uses the exact v1.2.3 tag

Language selection:
  - Language is declared in tacplus.yaml

Package derivation:
  - Package base is read from tacplus.yaml when pkg is set
  - If pkg is omitted, package base is derived from the host project

Output directory:
  - Java Maven: target/generated-sources/tacplus
  - Java Gradle: build/generated-sources/tacplus
  - All others: tacplus/

Config files:
  - By default, reads tacplus.yaml from the current directory
  - Use --config / -c to specify one or more config files
  - Multiple files are merged left-to-right (later values override)
  - Config files can use "import:" to compose from a base config
  - Imported files are resolved relative to the importing file's directory

Examples:
  tacplus build                                        # reads from tacplus.yaml
  tacplus build --tag latest                           # override tag
  tacplus build --tag 1.0.0 --src ../proto
  tacplus build --src ../base-proto --src ../kernel-proto  # merge two sources
  tacplus build -c tacplus-base.yaml -c tacplus-ci.yaml  # layered composition`,
	RunE:         runBuild,
	SilenceUsage: true,
}

func init() {
	rootCmd.Flags().StringVar(&protoTag, "tag", "", `Proto version tag override (default: auto-detected from git tag)`)
	rootCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "Verbose output")
	rootCmd.Flags().StringArrayVar(&protoSrcs, "src", nil, "Proto source: local directory path or git repo URL. Repeatable — pass --src multiple times to merge several proto source trees into one generation (e.g. a shared base plus a reusable service's protos).")
	rootCmd.Flags().StringSliceVarP(&configFiles, "config", "c", nil, "Config file(s) to use (can specify multiple, merged left-to-right)")
}

// GetRootCmd returns the root command for use as a subcommand.
func GetRootCmd() *cobra.Command {
	return rootCmd
}

func runBuild(cmd *cobra.Command, args []string) error {
	p := runner.Params{
		ProtoTag:    protoTag,
		ProtoSrcs:   protoSrcs,
		SrcExplicit: cmd.Flags().Changed("src"),
		Verbose:     verbose,
	}

	// Load and merge config files if --config was specified
	if len(configFiles) > 0 {
		cfg, err := tacplusyaml.ReadFiles(configFiles)
		if err != nil {
			return err
		}
		p.Config = cfg
	}

	return runner.Build(p)
}
