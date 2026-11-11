// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/publicsuffix"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
)

// PublicSuffixGo returns utils/publicsuffix.go: the validators of
// (spec.assert_registrable_domain) and (spec.assert_not_address) (tacplus#224)
// and the Public Suffix List table, publicsuffix.Rules(), they match against.
// ERRORS_IMPORT_PATH is replaced like the other utils files.
func PublicSuffixGo() string {
	var b strings.Builder
	b.WriteString(header.GeneratedHeader(consts.LangGo, ""))
	b.WriteString(`package utils

import (
	"sort"
	"strings"

	protoerrors "ERRORS_IMPORT_PATH"
)

// ValidateAssertRegistrableDomain validates that a string is a domain name
// someone can register: ASCII labels of letters, digits and hyphens, at least
// two, and not a public suffix by the Public Suffix List snapshot below.
// Corresponds to the (spec.assert_registrable_domain) annotation.
func ValidateAssertRegistrableDomain(name string) error {
	if len(name) == 0 || len(name) > 253 {
		return protoerrors.NewErrMissingInvalidData(nil, "not a domain name")
	}
	for _, l := range strings.Split(name, ".") {
		if len(l) == 0 || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return protoerrors.NewErrMissingInvalidData(nil, "not a domain name")
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
				return protoerrors.NewErrMissingInvalidData(nil, "not a domain name")
			}
		}
	}
	if isPublicSuffix(strings.ToLower(name)) {
		return protoerrors.NewErrMissingInvalidData(nil, "a public suffix")
	}
	return nil
}

func isPublicSuffix(name string) bool {
	if !strings.Contains(name, ".") {
		return true
	}
	if hasPublicSuffixRule("!" + name) {
		return false
	}
	if hasPublicSuffixRule(name) {
		return true
	}
	_, parent, _ := strings.Cut(name, ".")
	return hasPublicSuffixRule("*." + parent)
}

func hasPublicSuffixRule(rule string) bool {
	i := sort.SearchStrings(publicSuffixRules, rule)
	return i < len(publicSuffixRules) && publicSuffixRules[i] == rule
}

// ValidateAssertNotAddress validates that a string is not address-shaped: it
// contains no '@'. Corresponds to the (spec.assert_not_address) annotation.
func ValidateAssertNotAddress(s string) error {
	if strings.Contains(s, "@") {
		return protoerrors.NewErrMissingInvalidData(nil, "address-shaped")
	}
	return nil
}

`)
	fmt.Fprintf(&b, "// publicSuffixRules is the Public Suffix List (publicsuffix.org, MPL-2.0),\n// version %s, in ASCII, sorted.\nvar publicSuffixRules = []string{\n", publicsuffix.Version())
	for _, r := range publicsuffix.Rules() {
		fmt.Fprintf(&b, "\t%q,\n", r)
	}
	b.WriteString("}\n")
	return b.String()
}
