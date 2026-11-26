// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package tacplusyaml

import (
	"fmt"
	"path"
)

// A service with a deterministic core and an LLM advisor (the Skipper, the
// Admiral) declares where the advisor's code lives (tacplus#909; the platform
// rule of tacplus-dev/roadmap decisions/skipper-and-admiral-2026-11-01.md,
// 2026-11-19: an LLM may propose; it never holds a token or a write path):
//
//	advisor:
//	  path: advisor   # the advisor's own unit: a crate (Rust)
//
// tacplus then generates the advisor's model port, the core-side adapter that
// implements it over the model service's S2S client, and the gate that fails
// the build when the advisor's unit depends on anything else of the service:
// a state client, an S2S client, a token, the core. Where that code lives is
// a project-local fact, so it is here and not in proto.
type Advisor struct {
	Path string `yaml:"path"`
}

// CheckAdvisor refuses an advisor whose path is missing or leaves the repo, and
// one in a project that is not a service.
func CheckAdvisor(kind string, a *Advisor) error {
	if a == nil {
		return nil
	}
	if kind == KindClient || kind == KindLibrary {
		return fmt.Errorf("%s: advisor: only a service has an advisor; this one is kind: %s", Filename, kind)
	}
	if a.Path == "" {
		return fmt.Errorf("%s: advisor: path is required: the directory of the advisor's own unit", Filename)
	}
	if !isRepoRelative(a.Path) {
		return fmt.Errorf("%s: advisor: path %q must be a directory of this repo, relative, without ..", Filename, a.Path)
	}
	if path.Clean(a.Path) == "." {
		return fmt.Errorf("%s: advisor: path %q must be its own directory, not the service's root: the advisor is a unit apart from the core", Filename, a.Path)
	}
	return nil
}
