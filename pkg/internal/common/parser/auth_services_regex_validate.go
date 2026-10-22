// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ValidateAuthServicePatterns enforces at generation time that every
// auth_services entry is a valid RE2 regex.
//
// auth_services values are carried verbatim into every backend's authorizer as
// the caller-identity patterns for a method, and each backend compiles them
// with an unchecked constructor when the service starts: Go MustCompile, Rust
// Regex::new(..).unwrap(), Java Pattern.compile, TypeScript new RegExp, Python
// re.compile. A pattern that is not valid regex syntax therefore generates
// cleanly and then kills the service at startup, in code that has no way to
// report which .proto line produced it. Compiling every pattern here — in the
// same anchored "^(?:p)$" form the authorizers use — moves that failure to
// `tacplus build`, where the offending file, service, RPC and pattern can be
// named. Once this gate passes, the runtime MustCompile is correct by
// construction.
//
// "*" is not checked: every backend special-cases it as the match-any shorthand
// and never compiles it as a pattern.
func ValidateAuthServicePatterns(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	all := dp.GetAllFiles()
	paths := make([]string, 0, len(all))
	for path := range all {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var errs []string
	for _, path := range paths {
		fd := all[path]
		for si := range fd.Services {
			svc := &fd.Services[si]
			for _, p := range svc.AuthServices {
				if err := compileAuthServicePattern(p); err != nil {
					errs = append(errs, fmt.Sprintf("%s: service %s: auth_services pattern %q is not a valid regex: %v",
						path, svc.Name, p, err))
				}
			}
			for mi := range svc.Methods {
				m := &svc.Methods[mi]
				for _, p := range m.AuthServices {
					if err := compileAuthServicePattern(p); err != nil {
						errs = append(errs, fmt.Sprintf("%s: rpc %s.%s: auth_services pattern %q is not a valid regex: %v",
							path, svc.Name, m.Name, p, err))
					}
				}
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("auth_services pattern errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

// compileAuthServicePattern compiles p exactly as the generated authorizers do.
func compileAuthServicePattern(p string) error {
	if p == "*" {
		return nil
	}
	_, err := regexp.Compile("^(?:" + p + ")$")
	return err
}
