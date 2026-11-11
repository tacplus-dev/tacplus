// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/publicsuffix"
)

// pyFileUsesPublicSuffix reports whether a proto file's validators call the
// (spec.assert_registrable_domain) or (spec.assert_not_address) helpers, so
// only the helpers.py of such a file carries them and the table.
func pyFileUsesPublicSuffix(pf model.ProtoFile) bool {
	for _, msg := range pf.Messages {
		for _, field := range msg.Fields {
			for _, name := range []string{"assert_registrable_domain", "assert_not_address"} {
				if v, ok := field.Annotations[name]; ok && v.Type == model.AnnotationTypeBool && v.BoolValue {
					return true
				}
			}
		}
	}
	return false
}

// pyPublicSuffixHelpers returns the Python validators of
// (spec.assert_registrable_domain) and (spec.assert_not_address) (tacplus#224)
// and the Public Suffix List table, publicsuffix.Rules(), they match against;
// the same semantics and refusal texts as the Go utils/publicsuffix.go. It is
// appended to a helpers.py, whose invalid_argument the validators raise.
func pyPublicSuffixHelpers() string {
	var b strings.Builder
	b.WriteString(`

# ---- assert_registrable_domain / assert_not_address (tacplus#224) ----
def _registrable_domain_refusal(name: str):
    """The refusal text for a name that is not a registrable domain name,
    "not a domain name" or "a public suffix"; None when it is registrable."""
    if len(name) == 0 or len(name.encode("utf-8")) > 253:
        return "not a domain name"
    for label in name.split("."):
        if len(label) == 0 or len(label) > 63 or label[0] == "-" or label[-1] == "-":
            return "not a domain name"
        for c in label:
            if not ("a" <= c <= "z" or "A" <= c <= "Z" or "0" <= c <= "9" or c == "-"):
                return "not a domain name"
    if _is_public_suffix(name.lower()):
        return "a public suffix"
    return None


def _is_public_suffix(name: str) -> bool:
    if "." not in name:
        return True
    if "!" + name in _PUBLIC_SUFFIX_RULES:
        return False
    if name in _PUBLIC_SUFFIX_RULES:
        return True
    return "*." + name.split(".", 1)[1] in _PUBLIC_SUFFIX_RULES


def _validate_registrable_domain(name: str, field_name: str) -> None:
    """Validates a domain name someone can register (assert_registrable_domain)."""
    reason = _registrable_domain_refusal(name)
    if reason is not None:
        raise invalid_argument(f"field {field_name} must be a registrable domain name: {reason}")


def _validate_not_address(s: str, field_name: str) -> None:
    """Validates a value that is not address-shaped: no '@' (assert_not_address)."""
    if "@" in s:
        raise invalid_argument(f"field {field_name} must not be address-shaped")

`)
	fmt.Fprintf(&b, "# The Public Suffix List (publicsuffix.org, MPL-2.0),\n# version %s, in ASCII.\n_PUBLIC_SUFFIX_RULES = frozenset((\n", publicsuffix.Version())
	for _, r := range publicsuffix.Rules() {
		// The rules are lowercase ASCII, so Go's %q is a Python literal too.
		fmt.Fprintf(&b, "    %q,\n", r)
	}
	b.WriteString("))\n")
	return b.String()
}

// pyEnumValueName is the name of the value numbered n of a field's enum when
// the file declares the enum, else the number, for an assert_enum_not_in
// refusal text.
func pyEnumValueName(field model.Field, enums []model.Enum, n int32) string {
	typeName := field.Type
	if i := strings.LastIndex(typeName, "."); i >= 0 {
		typeName = typeName[i+1:]
	}
	for _, e := range enums {
		if e.Name != field.Type && e.Name != typeName {
			continue
		}
		for _, v := range e.Values {
			if v.Number == int(n) {
				return v.Name
			}
		}
	}
	return fmt.Sprint(n)
}

// buildPySignInAssert writes the field checks of
// (spec.assert_registrable_domain), (spec.assert_not_address) and
// (spec.assert_enum_not_in) (tacplus#224), after the shared generation-time
// shape check.
func buildPySignInAssert(b *strings.Builder, msg model.Message, field model.Field, enums []model.Enum, pyFieldName, annotationName string, annotationValue model.AnnotationValue) error {
	if err := parser.CheckSignInAssert(msg.Name, field, annotationName, annotationValue); err != nil {
		return err
	}
	get := pyFieldGet("msg", pyFieldName)
	switch annotationName {
	case "assert_registrable_domain", "assert_not_address":
		fn := "_validate_registrable_domain"
		if annotationName == "assert_not_address" {
			fn = "_validate_not_address"
		}
		pyLinef(b, 1, "# Validate %s (%s)", pyFieldName, annotationName)
		if field.IsRepeated {
			pyLinef(b, 1, "for i, val in enumerate(%s):", get)
			pyLine(b, 2, "if val:")
			pyLinef(b, 3, "%s(val, f\"%s[{i}]\")", fn, pyFieldName)
		} else {
			pyLinef(b, 1, "if %s:", get)
			pyLinef(b, 2, "%s(%s, '%s')", fn, get, pyFieldName)
		}
	case "assert_enum_not_in":
		for _, refused := range annotationValue.IntSliceValue {
			name := pyEnumValueName(field, enums, refused)
			pyLinef(b, 1, "# Validate %s (assert_enum_not_in %d)", pyFieldName, refused)
			if field.IsRepeated {
				pyLinef(b, 1, "for i, val in enumerate(%s):", get)
				pyLinef(b, 2, "if int(val) == %d:", refused)
				pyLinef(b, 3, "raise invalid_argument(f\"field %s[{i}] must not be %s\")", pyFieldName, name)
			} else if field.IsOptional {
				pyLinef(b, 1, "if msg.HasField('%s') and int(%s) == %d:", field.Name, get, refused)
				pyLinef(b, 2, "raise invalid_argument(\"field %s must not be %s\")", pyFieldName, name)
			} else {
				pyLinef(b, 1, "if int(%s) == %d:", get, refused)
				pyLinef(b, 2, "raise invalid_argument(\"field %s must not be %s\")", pyFieldName, name)
			}
		}
	}
	return nil
}
