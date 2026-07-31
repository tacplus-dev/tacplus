// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// ValidateDataEntitlementAnnotations enforces the data-entitlement / (spec.redact)
// contract in ONE place, at parse time, so it fails loudly and identically for
// every target language (not late inside one language's emitter). Data entitlement
// is recursive over message-TYPED fields: a message-typed field may be redacted as
// a whole (the sub-message cleared) or, when pass-through, field-by-field via
// BuildEntitlementTree. It is NOT valid on a nested type DECLARATION — that stays
// rejected by ValidateNoNestedRedact; hoist the type to top level. Rules:
//
//   - the mode must be a real mode (REDACT_MODE_UNSPECIFIED / an empty {} is rejected);
//   - the two REDACT modes (DROP, MAY_REDACT) clear to zero and are valid on any
//     scalar, on a message-typed field (whole sub-message cleared), or on a
//     repeated field (list emptied);
//   - the two ANONYMIZE modes (PSEUDONYMIZE, MAY_ANONYMIZE) are keyed HMAC over a
//     single value and require a plain (non-repeated, non-message) string or bytes;
//   - map and oneof fields are not supported;
//   - a message-level (spec.data_entitlement) default must carry a real mode.
//
// It is valid on any message, not only a (spec.record) — data entitlement applies
// to anything a service returns. Returns all violations at once.
func ValidateDataEntitlementAnnotations(allFiles []model.ProtoFile) error {
	var errs []string
	for _, pf := range allFiles {
		for _, msg := range pf.Messages {
			// A message-level default must carry a real mode when present.
			if de, ok := msg.Annotations["data_entitlement"]; ok && !isValidEntitlementMode(de.StringValue) {
				errs = append(errs, fmt.Sprintf(
					"%s: message-level (spec.data_entitlement) requires a mode — set REDACT_MODE_DROP, REDACT_MODE_PSEUDONYMIZE, REDACT_MODE_MAY_REDACT, or REDACT_MODE_MAY_ANONYMIZE",
					msg.Name))
			}
			for _, field := range msg.Fields {
				ann, ok := field.Annotations["redact"]
				if !ok {
					continue
				}
				mode := ann.StringValue
				if !isValidEntitlementMode(mode) {
					errs = append(errs, fmt.Sprintf(
						"%s.%s: (spec.redact) requires a mode — set REDACT_MODE_DROP, REDACT_MODE_PSEUDONYMIZE, REDACT_MODE_MAY_REDACT, or REDACT_MODE_MAY_ANONYMIZE",
						msg.Name, field.Name))
					continue
				}
				// A oneof member cannot be zeroed by the generated transform — it lives
				// behind a wrapper.
				if field.IsOneOf {
					errs = append(errs, fmt.Sprintf(
						"%s.%s: (spec.redact) is not supported on a oneof member", msg.Name, field.Name))
					continue
				}
				// Map redaction is not yet supported.
				if field.IsMap {
					errs = append(errs, fmt.Sprintf(
						"%s.%s: (spec.redact) is not supported on a map field", msg.Name, field.Name))
					continue
				}
				if isAnonymizeMode(mode) {
					// Anonymize is a keyed HMAC over a single value — defined only for a
					// plain string or bytes leaf; a whole message or a list has no value
					// to hash.
					if field.IsMessage || field.IsRepeated || (field.Type != "string" && field.Type != "bytes") {
						errs = append(errs, fmt.Sprintf(
							"%s.%s: (spec.redact) anonymize (PSEUDONYMIZE/MAY_ANONYMIZE) requires a string or bytes field (non-repeated, non-message)",
							msg.Name, field.Name))
					}
					continue
				}
				// Redact modes (drop/may_redact) clear the field to its zero value: valid
				// on any scalar, on a message-typed field (the whole sub-message is
				// cleared), or on a repeated field (the list is emptied). When a
				// message-typed field is left pass-through, BuildEntitlementTree recurses
				// into its own fields field-by-field downstream.
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("data entitlement annotation validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// ValidateRedactAnnotations is a back-compat alias for
// ValidateDataEntitlementAnnotations (the redact seam is subsumed by data
// entitlement). Kept so existing call sites do not churn.
func ValidateRedactAnnotations(allFiles []model.ProtoFile) error {
	return ValidateDataEntitlementAnnotations(allFiles)
}

// ValidateReservedFieldNumbers rejects any hand-authored field whose proto number
// falls in the reserved data-entitlement band (>= RedactIndicatorFieldBase).
// tacplus injects a companion "<field>_redacted" indicator at
// RedactIndicatorFieldBase + the source field number, so that band must stay clear
// — otherwise an injected indicator would collide with an author field and the
// generated proto would not compile. Fails loud with every offender named.
func ValidateReservedFieldNumbers(allFiles []model.ProtoFile) error {
	var errs []string
	for _, pf := range allFiles {
		for _, msg := range pf.Messages {
			for _, field := range msg.Fields {
				if field.Number >= RedactIndicatorFieldBase {
					errs = append(errs, fmt.Sprintf(
						"%s.%s: field number %d is in the reserved data-entitlement band; numbers %d and above are reserved for tacplus-injected redacted indicators",
						msg.Name, field.Name, field.Number, RedactIndicatorFieldBase))
				}
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("reserved field number validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// isValidEntitlementMode reports whether s is one of the four recognized
// data-entitlement mode strings stamped by redactModeString.
func isValidEntitlementMode(s string) bool {
	switch s {
	case "drop", "pseudonymize", "may_redact", "may_anonymize":
		return true
	}
	return false
}

// redactSupportedLanguages lists the FULL-SERVICE targets whose Sanitize<T> egress
// transform tacplus generates. These languages host record stores and apply
// redaction at the store egress (Sample/Pull/Watch). A record that declares
// (spec.redact) must NOT build for a full-service language absent here: silently
// emitting an unredacted egress surface would leak the very data the annotation
// exists to protect. Add a full-service language only once its Release codegen and
// tests land.
var redactSupportedLanguages = map[string]bool{
	"go":   true,
	"rust": true,
	"java": true,
}

// redactClientOnlyLanguages lists the client-only targets. They reject record-store
// / service generation, so they never emit the egress surface redaction protects —
// the store (a full-service language) applies Release, and these targets only ever
// consume already-redacted records. A (spec.redact) field is therefore a no-op for
// them, not a leak, so they build without Release codegen. (Per the client-only
// principle: no server-side responsibilities in client-only targets.)
var redactClientOnlyLanguages = map[string]bool{
	"python":     true,
	"typescript": true,
	"swift":      true,
	"csharp":     true,
}

// ValidateRedactLanguageSupport fails the build LOUDLY when a record uses
// (spec.redact) but the target is a full-service language with no Sanitize<T>
// generator. Fail-loud, never fail-open for store-hosting languages: an
// unimplemented one refuses rather than shipping raw data through an egress
// surface the annotation was meant to redact. Client-only targets pass — they
// cannot host a store, so there is no egress surface to redact.
func ValidateRedactLanguageSupport(allFiles []model.ProtoFile, language string) error {
	if redactSupportedLanguages[language] || redactClientOnlyLanguages[language] {
		return nil
	}
	var offenders []string
	for _, pf := range allFiles {
		for _, msg := range pf.Messages {
			for _, field := range msg.Fields {
				if _, ok := field.Annotations["redact"]; ok {
					offenders = append(offenders, fmt.Sprintf("%s.%s", msg.Name, field.Name))
				}
			}
		}
	}
	if len(offenders) == 0 {
		return nil
	}
	return fmt.Errorf(
		"(spec.redact) is declared on %s but egress data-entitlement codegen (Sanitize<T>) is not implemented for target language %q; "+
			"tacplus refuses to generate an unredacted egress surface — implement redaction for %q or remove the annotation",
		strings.Join(offenders, ", "), language, language)
}
