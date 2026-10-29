// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	buildcmd "github.com/tacplus-dev/tacplus/cmd/build"
	fleetcmd "github.com/tacplus-dev/tacplus/cmd/fleet"
	registrycmd "github.com/tacplus-dev/tacplus/cmd/registry"
	upgradecmd "github.com/tacplus-dev/tacplus/cmd/upgrade"
	validatecmd "github.com/tacplus-dev/tacplus/cmd/validate"
	verifycmd "github.com/tacplus-dev/tacplus/cmd/verify"
	versioncmd "github.com/tacplus-dev/tacplus/cmd/version"
	"github.com/tacplus-dev/tacplus/pkg/buildinfo"
	"github.com/tacplus-dev/tacplus/pkg/runner"
	"github.com/tacplus-dev/tacplus/pkg/tacplusyaml"
)

var rootCmd = &cobra.Command{
	Use:   "tacplus",
	Short: "TAC+ Protocol Buffer compiler",
	Long: `tacplus is a comprehensive code generator for Protocol Buffers.
It generates types, clients, and services from Protocol Buffer definitions
for multiple languages including Go, TypeScript, Python, Rust, and Java.

When run without a subcommand and tacplus.yaml exists, runs build.

Commands:
  build       Generate proto types and services for a single language
  validate    Validate proto files
  verify      Run formal verification on Rust or Java code
  version     Print the version and build information
  upgrade     Replace this binary with the newest patch of its line
  pin         Pin this repository's tacplus release and build image (tacplus.lock)
  completion  Generate shell completion scripts
  help        Help about any command`,
	Version:           buildinfo.Version,
	PersistentPreRunE: initLogger,
	RunE:              runRoot,
	SilenceUsage:      true,
}

var showBuildID bool

func init() {
	// Add --build-id flag
	rootCmd.Flags().BoolVar(&showBuildID, "build-id", false, "Print the build ID and exit")

	// Add subcommands
	buildCmd := buildcmd.GetRootCmd()
	buildCmd.Use = "build"
	rootCmd.AddCommand(buildCmd)

	validateCmd := validatecmd.GetRootCmd()
	validateCmd.Use = "validate"
	rootCmd.AddCommand(validateCmd)

	verifyCmd := verifycmd.GetRootCmd()
	verifyCmd.Use = "verify"
	rootCmd.AddCommand(verifyCmd)

	rootCmd.AddCommand(registrycmd.GetCmd())
	rootCmd.AddCommand(fleetcmd.GetCmd())
	rootCmd.AddCommand(versioncmd.GetCmd())
	rootCmd.AddCommand(upgradecmd.GetCmd())
	rootCmd.AddCommand(upgradecmd.GetPinCmd())
}

// runRoot handles bare `tacplus` invocation:
// - If tacplus.yaml exists → run build
// - Otherwise → error
func runRoot(cmd *cobra.Command, args []string) error {
	if showBuildID {
		fmt.Println(buildinfo.BuildID)
		return nil
	}

	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	if tacplusyaml.Exists(cwd) {
		return runner.Build(runner.Params{})
	}

	return fmt.Errorf("no tacplus.yaml found in %s", cwd)
}

// initLogger initializes the global zap logger.
func initLogger(cmd *cobra.Command, args []string) error {
	cfg := zap.Config{
		Level:            zap.NewAtomicLevelAt(zapcore.DebugLevel),
		Encoding:         "console",
		EncoderConfig:    zap.NewDevelopmentEncoderConfig(),
		OutputPaths:      []string{"stderr"},
		ErrorOutputPaths: []string{"stderr"},
	}
	cfg.EncoderConfig.CallerKey = ""
	cfg.EncoderConfig.StacktraceKey = ""
	cfg.DisableStacktrace = true

	logger, err := cfg.Build()
	if err != nil {
		return err
	}
	zap.ReplaceGlobals(logger)
	return nil
}

// Execute runs the root command
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
