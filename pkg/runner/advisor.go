// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package runner

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/tacplusyaml"
)

// resolveAdvisorDir returns the absolute directory of the service's LLM advisor
// (tacplus.yaml advisor.path, tacplus#909), or "" when it declares none. It
// refuses an advisor tacplus cannot keep apart from the service: one inside
// the generated output (regenerated away on the next build) or containing it,
// and one in a language whose advisor gate is not generated.
func resolveAdvisorDir(cwd, outDir string, cfg *tacplusyaml.Config, lang string) (string, error) {
	if cfg == nil || cfg.Advisor == nil {
		return "", nil
	}
	switch lang {
	case "rust", "go":
	case "java":
		// Parity is staged under tacplus#909: Rust and Go first, and a language
		// without the gate refuses the advisor rather than build one ungated.
		return "", fmt.Errorf("%s: advisor: the advisor's model port and gate are not generated for %s yet (tacplus#909: Rust and Go first, Java next); an advisor without its gate is refused", tacplusyaml.Filename, lang)
	default:
		return "", fmt.Errorf("%s: advisor: %s is a client-only target; an advisor belongs to a service's deterministic core", tacplusyaml.Filename, lang)
	}
	dir := filepath.Join(cwd, filepath.FromSlash(cfg.Advisor.Path))
	if within(outDir, dir) {
		return "", fmt.Errorf("%s: advisor: path %q is inside the generated output %q, which tacplus deletes and regenerates", tacplusyaml.Filename, cfg.Advisor.Path, outDir)
	}
	if within(dir, outDir) {
		return "", fmt.Errorf("%s: advisor: path %q contains the generated output %q: the advisor must be a unit apart from the service and its generated code", tacplusyaml.Filename, cfg.Advisor.Path, outDir)
	}
	return dir, nil
}

// within reports whether path is dir or below it.
func within(dir, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(dir), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
