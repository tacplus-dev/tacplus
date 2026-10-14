// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package verification

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	spec "gitlab.com/seabeam/schematic/tacplus/target/proto/spec/sys"
)

// WriteReport generates a human-readable verification-report.md in the output directory.
func WriteReport(outputDir string, reports []*spec.VerificationReport) error {
	if len(reports) == 0 {
		return nil
	}

	var b strings.Builder

	b.WriteString("# Formal Verification Report\n\n")
	b.WriteString(fmt.Sprintf("**Date:** %s\n", time.Now().UTC().Format(time.RFC3339)))

	// Tool chain summary
	var tools []string
	for _, r := range reports {
		tools = append(tools, fmt.Sprintf("%s %s (%s)", r.Tool, r.ToolVersion, r.Language))
	}
	b.WriteString(fmt.Sprintf("**Tool chain:** %s\n\n", strings.Join(tools, ", ")))

	// Summary table
	b.WriteString("## Summary\n\n")
	b.WriteString("| Tool | Language | Passed | Failed | Total | Duration |\n")
	b.WriteString("|------|----------|--------|--------|-------|----------|\n")
	for _, r := range reports {
		duration := fmt.Sprintf("%.1fs", float64(r.DurationMs)/1000.0)
		b.WriteString(fmt.Sprintf("| %s | %s | %d | %d | %d | %s |\n",
			r.Tool, r.Language, r.Passed, r.Failed, r.Total, duration))
	}
	b.WriteString("\n")

	// Per-tool detail sections
	for _, r := range reports {
		toolTitle := toolDisplayName(r.Tool)
		b.WriteString(fmt.Sprintf("## %s -- %s (%s)\n\n", titleCase(r.Language), r.Tool, toolTitle))

		for _, h := range r.Harnesses {
			fqnLabel := h.HarnessName
			if h.MessageFqn != "" {
				fqnLabel = h.MessageFqn + " -- " + h.HarnessName
			}
			b.WriteString(fmt.Sprintf("### %s\n", fqnLabel))
			b.WriteString(fmt.Sprintf("**Status:** %s\n", strings.ToUpper(h.Status)))

			if len(h.Assumptions) > 0 {
				b.WriteString("**Assumptions:**\n")
				for _, a := range h.Assumptions {
					b.WriteString(fmt.Sprintf("- `%s`\n", a))
				}
			}

			if len(h.Contracts) > 0 {
				b.WriteString("**Contracts:**\n")
				for _, c := range h.Contracts {
					b.WriteString(fmt.Sprintf("- `%s`\n", c))
				}
			}

			if h.Detail != "" {
				b.WriteString("**Detail:**\n")
				b.WriteString(fmt.Sprintf("```\n%s\n```\n", h.Detail))
			}

			b.WriteString("\n")
		}
	}

	reportPath := filepath.Join(outputDir, "verification-report.md")
	return os.WriteFile(reportPath, []byte(b.String()), 0644)
}

// titleCase capitalizes the first letter of a string.
func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// toolDisplayName returns a human-readable description for a verification tool.
func toolDisplayName(tool string) string {
	switch tool {
	case "kani":
		return "Bounded Model Checking"
	case "jbmc":
		return "Bounded Model Checking"
	case "openjml":
		return "Extended Static Checking"
	default:
		return tool
	}
}
