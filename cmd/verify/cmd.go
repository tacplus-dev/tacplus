// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package verify

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/tacplusyaml"
	"github.com/tacplus-dev/tacplus/pkg/verification"
)

var rootCmd = &cobra.Command{
	Use:   "verify",
	Short: "Run formal verification on Rust or Java code",
	Long: `Run formal verification tools on source code in the current directory.

Requires tacplus.yaml to be present — the language is read from it.

Supported languages and tools:
  rust    Kani (bounded model checking) — looks for #[kani::proof] harnesses
  java    JBMC (bounded model checking) — looks for Proof.java with CProver harnesses

Examples:
  cd gateway && tacplus verify    # Verify Rust code (language from tacplus.yaml)
  cd identity && tacplus verify   # Verify Java code (language from tacplus.yaml)`,
	RunE: runVerify,
}

// GetRootCmd returns the root command for use as a subcommand.
func GetRootCmd() *cobra.Command {
	return rootCmd
}

func runVerify(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("cannot determine working directory: %w", err)
	}

	// Require tacplus.yaml
	cfg, err := tacplusyaml.Read(cwd)
	if err != nil {
		return fmt.Errorf("tacplus.yaml is required for verify: %w", err)
	}

	language := cfg.Language
	switch language {
	case "rust", "java":
		// OK — these have verification tools
	default:
		return fmt.Errorf("no verification tool for language %q (supported: rust, java)", language)
	}

	verifyLang := language

	absDir, err := filepath.Abs(cwd)
	if err != nil {
		return fmt.Errorf("failed to resolve directory: %w", err)
	}

	zap.L().Info("running formal verification",
		zap.String("language", verifyLang),
		zap.String("dir", absDir),
	)

	reports, err := verification.Run(verifyLang, absDir)
	if err != nil {
		return fmt.Errorf("verification: %w", err)
	}

	if len(reports) == 0 {
		zap.L().Info("no verification harnesses found", zap.String("dir", absDir))
		return nil
	}

	if err := verification.WriteReport(absDir, reports); err != nil {
		zap.L().Warn("failed to write verification report", zap.Error(err))
	}

	for _, r := range reports {
		if r.Failed > 0 {
			return fmt.Errorf("verification failed: %s reported %d failed harnesses out of %d total",
				r.Tool, r.Failed, r.Total)
		}
	}

	zap.L().Info("verification passed",
		zap.String("language", verifyLang),
		zap.Int32("total", reports[0].Total),
	)
	return nil
}
