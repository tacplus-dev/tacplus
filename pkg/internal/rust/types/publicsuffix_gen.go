// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/publicsuffix"
)

// writeRustSignInHelpers writes the private helpers behind
// (spec.assert_registrable_domain) and (spec.assert_not_address)
// (tacplus#224) into a generated module, each only when a field of the module
// uses it so the module stays free of dead_code warnings, like the helpers of
// validation_helpers.rs.tmpl. The registrable-domain check carries the Public
// Suffix List table, publicsuffix.Rules(), as a sorted static slice it binary
// searches. Semantics and refusal texts are the Go reference's
// (pkg/internal/go/types/publicsuffix_gen.go).
func writeRustSignInHelpers(b *strings.Builder, needsRegistrableDomain, needsNotAddress bool) {
	if needsRegistrableDomain {
		b.WriteString(`/// Validates that a string is a domain name someone can register: ASCII labels
/// of letters, digits and hyphens, at least two, and not a public suffix by the
/// Public Suffix List snapshot in TACPLUS_PUBLIC_SUFFIX_RULES.
/// Corresponds to the (spec.assert_registrable_domain) annotation.
fn validate_registrable_domain(name: &str) -> Result<(), String> {
    if name.is_empty() || name.len() > 253 {
        return Err("not a domain name".to_string());
    }
    for label in name.split('.') {
        let l = label.as_bytes();
        if l.is_empty() || l.len() > 63 || l[0] == b'-' || l[l.len() - 1] == b'-' {
            return Err("not a domain name".to_string());
        }
        if !l.iter().all(|c| c.is_ascii_alphanumeric() || *c == b'-') {
            return Err("not a domain name".to_string());
        }
    }
    if tacplus_is_public_suffix(&name.to_ascii_lowercase()) {
        return Err("a public suffix".to_string());
    }
    Ok(())
}

/// Reports whether a lowercase ASCII domain name is a public suffix.
fn tacplus_is_public_suffix(name: &str) -> bool {
    let has = |rule: &str| TACPLUS_PUBLIC_SUFFIX_RULES.binary_search(&rule).is_ok();
    match name.split_once('.') {
        None => true,
        Some((_, parent)) => {
            if has(&format!("!{}", name)) {
                return false;
            }
            has(name) || has(&format!("*.{}", parent))
        }
    }
}

`)
		fmt.Fprintf(b, "/// The Public Suffix List (publicsuffix.org, MPL-2.0),\n/// version %s, in ASCII, sorted.\nstatic TACPLUS_PUBLIC_SUFFIX_RULES: &[&str] = &[\n", publicsuffix.Version())
		for _, r := range publicsuffix.Rules() {
			// Rules are lowercase ASCII without quotes or backslashes, so
			// Go's %q is a valid Rust string literal for each.
			fmt.Fprintf(b, "    %q,\n", r)
		}
		b.WriteString("];\n")
	}
	if needsNotAddress {
		b.WriteString(`/// Validates that a string is not address-shaped: it contains no '@'.
/// Corresponds to the (spec.assert_not_address) annotation.
fn validate_not_address(s: &str) -> Result<(), String> {
    if s.contains('@') {
        return Err("address-shaped".to_string());
    }
    Ok(())
}
`)
	}
}
