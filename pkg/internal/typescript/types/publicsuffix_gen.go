// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/publicsuffix"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
)

// publicSuffixTsFile is the generated runtime helper's path, relative to the
// TypeScript output root (the helpers files import it from there).
const publicSuffixTsFile = "utils/publicsuffix.js"

// PublicSuffixTs returns utils/publicsuffix.ts: the validators of
// (spec.assert_registrable_domain) and (spec.assert_not_address) (tacplus#224)
// and the Public Suffix List table, publicsuffix.Rules(), they match against.
// Mirrors Go's utils/publicsuffix.go: same rules, same refusal texts, thrown
// as the ValidationError newErrMissingInvalidData builds.
func PublicSuffixTs() string {
	var b strings.Builder
	b.WriteString(header.SimpleHeader(consts.LangTypeScript))
	b.WriteString(`import { newErrMissingInvalidData } from '../types/sys/errors/errors.js';

const DOMAIN_LABEL_REGEX = /^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$/;

/**
 * Validates that a string is a domain name someone can register: ASCII labels
 * of letters, digits and hyphens, at least two, and not a public suffix by the
 * Public Suffix List snapshot below.
 * Corresponds to the (spec.assert_registrable_domain) annotation.
 * @throws ValidationError "not a domain name" or "a public suffix".
 */
export function validateAssertRegistrableDomain(name: string): void {
  if (name.length === 0 || name.length > 253) {
    throw newErrMissingInvalidData(undefined, 'not a domain name');
  }
  for (const label of name.split('.')) {
    if (!DOMAIN_LABEL_REGEX.test(label)) {
      throw newErrMissingInvalidData(undefined, 'not a domain name');
    }
  }
  if (isPublicSuffix(name.toLowerCase())) {
    throw newErrMissingInvalidData(undefined, 'a public suffix');
  }
}

function isPublicSuffix(name: string): boolean {
  const dot = name.indexOf('.');
  if (dot < 0) {
    return true;
  }
  if (PUBLIC_SUFFIX_RULES.has('!' + name)) {
    return false;
  }
  if (PUBLIC_SUFFIX_RULES.has(name)) {
    return true;
  }
  return PUBLIC_SUFFIX_RULES.has('*.' + name.slice(dot + 1));
}

/**
 * Validates that a string is not address-shaped: it contains no '@'.
 * Corresponds to the (spec.assert_not_address) annotation.
 * @throws ValidationError "address-shaped".
 */
export function validateAssertNotAddress(s: string): void {
  if (s.includes('@')) {
    throw newErrMissingInvalidData(undefined, 'address-shaped');
  }
}

`)
	fmt.Fprintf(&b, "// PUBLIC_SUFFIX_RULES is the Public Suffix List (publicsuffix.org, MPL-2.0),\n// version %s, in ASCII.\nconst PUBLIC_SUFFIX_RULES: ReadonlySet<string> = new Set<string>([\n", publicsuffix.Version())
	for _, r := range publicsuffix.Rules() {
		fmt.Fprintf(&b, "  %s,\n", strconv.Quote(r))
	}
	b.WriteString("]);\n")
	return b.String()
}

// signInStringAssertImports returns the utils/publicsuffix.ts validators the
// helpers file of pf calls, in import order: those of the
// assert_registrable_domain / assert_not_address annotations on its
// (non-oneof) fields.
func signInStringAssertImports(pf model.ProtoFile) []string {
	var domain, address bool
	for _, msg := range pf.Messages {
		for _, field := range msg.Fields {
			if field.IsOneOf {
				continue
			}
			if v, ok := field.Annotations["assert_registrable_domain"]; ok && v.BoolValue {
				domain = true
			}
			if v, ok := field.Annotations["assert_not_address"]; ok && v.BoolValue {
				address = true
			}
		}
	}
	var names []string
	if address {
		names = append(names, "validateAssertNotAddress")
	}
	if domain {
		names = append(names, "validateAssertRegistrableDomain")
	}
	return names
}

// writeTsSignInStringAssert writes the field check of
// (spec.assert_registrable_domain) or (spec.assert_not_address): an empty
// value is skipped, any other goes through the utils/publicsuffix.ts
// validator, whose refusal becomes the cause of the field's error.
func writeTsSignInStringAssert(b *strings.Builder, field model.Field, fieldName, annotationName string) {
	fn, what := "validateAssertRegistrableDomain", "must be a registrable domain name"
	if annotationName == "assert_not_address" {
		fn, what = "validateAssertNotAddress", "must not be address-shaped"
	}
	if field.IsRepeated {
		fmt.Fprintf(b, `	// Validate %s (%s)
	if (m.%s) {
		for (let i = 0; i < m.%s.length; i++) {
			const val = m.%s[i];
			if (val) {
				try {
					%s(val);
				} catch (e) {
					throw newErrMissingInvalidData(e as Error, `+"`field %s[${i}] %s`"+`);
				}
			}
		}
	}

`, fieldName, annotationName, fieldName, fieldName, fieldName, fn, fieldName, what)
		return
	}
	fmt.Fprintf(b, `	// Validate %s (%s)
	if (m.%s) {
		try {
			%s(m.%s);
		} catch (e) {
			throw newErrMissingInvalidData(e as Error, `+"`field %s %s`"+`);
		}
	}

`, fieldName, annotationName, fieldName, fn, fieldName, fieldName, what)
}

// writeTsEnumNotInValidation writes the field check of
// (spec.assert_enum_not_in): one refusal per listed enum number, compared as
// numbers (a protobuf-es enum member does not compare to a bare literal).
func writeTsEnumNotInValidation(b *strings.Builder, field model.Field, fieldName string, refused []int32) {
	for _, n := range refused {
		switch {
		case field.IsRepeated:
			fmt.Fprintf(b, `	// Validate %s (assert_enum_not_in %d)
	if (m.%s) {
		for (let i = 0; i < m.%s.length; i++) {
			if (Number(m.%s[i]) === %d) {
				throw newErrMissingInvalidData(undefined, `+"`field %s[${i}] must not be %d`"+`);
			}
		}
	}

`, fieldName, n, fieldName, fieldName, fieldName, n, fieldName, n)
		case field.IsOptional:
			fmt.Fprintf(b, `	// Validate %s (assert_enum_not_in %d)
	if (m.%s !== undefined && Number(m.%s) === %d) {
		throw newErrMissingInvalidData(undefined, `+"`field %s must not be %d`"+`);
	}

`, fieldName, n, fieldName, fieldName, n, fieldName, n)
		default:
			fmt.Fprintf(b, `	// Validate %s (assert_enum_not_in %d)
	if (Number(m.%s) === %d) {
		throw newErrMissingInvalidData(undefined, `+"`field %s must not be %d`"+`);
	}

`, fieldName, n, fieldName, n, fieldName, n)
		}
	}
}
