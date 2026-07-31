// Copyright (c) 2026 Second Star Cavalier LLC

// Package verification runs formal verification tools (Kani, JBMC)
// on generated source code and produces structured results for registry.json
// and a human-readable verification-report.md.
package verification

import (
	"fmt"
	"time"

	"go.uber.org/zap"

	spec "gitlab.com/secondstarcavalier/schematic/tacplus/target/proto/spec/sys"
)

// Run executes formal verification for the given language and output directory.
// Returns structured results for embedding in registry.json.
func Run(language, outputDir string) ([]*spec.VerificationReport, error) {
	var runner func(string) (*spec.VerificationReport, error)

	switch language {
	case "rust":
		runner = runKani
	case "java":
		runner = runJbmc
	default:
		zap.L().Debug("no verification tool for language", zap.String("language", language))
		return nil, nil
	}

	start := time.Now()
	report, err := runner(outputDir)
	if err != nil {
		return nil, fmt.Errorf("%s verification: %w", language, err)
	}

	if report == nil || report.Total == 0 {
		zap.L().Debug("no verification harnesses found", zap.String("language", language))
		return nil, nil
	}

	report.DurationMs = time.Since(start).Milliseconds()

	zap.L().Info("verification complete",
		zap.String("tool", report.Tool),
		zap.Int32("passed", report.Passed),
		zap.Int32("failed", report.Failed),
		zap.Int32("total", report.Total),
		zap.Int64("duration_ms", report.DurationMs),
	)

	return []*spec.VerificationReport{report}, nil
}
