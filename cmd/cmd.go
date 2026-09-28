// Copyright (c) 2026 Second Star Cavalier LLC

package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	buildcmd "gitlab.com/seabeam/schematic/tacplus/cmd/build"
	registrycmd "gitlab.com/seabeam/schematic/tacplus/cmd/registry"
	upgradecmd "gitlab.com/seabeam/schematic/tacplus/cmd/upgrade"
	validatecmd "gitlab.com/seabeam/schematic/tacplus/cmd/validate"
	verifycmd "gitlab.com/seabeam/schematic/tacplus/cmd/verify"
	versioncmd "gitlab.com/seabeam/schematic/tacplus/cmd/version"
	"gitlab.com/seabeam/schematic/tacplus/pkg/buildinfo"
	"gitlab.com/seabeam/schematic/tacplus/pkg/runner"
	"gitlab.com/seabeam/schematic/tacplus/pkg/tacplusyaml"
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
	rootCmd.AddCommand(versioncmd.GetCmd())
	rootCmd.AddCommand(upgradecmd.GetCmd())
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
