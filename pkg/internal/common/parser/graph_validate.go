// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// ValidateGraphAnnotations checks all messages in the parsed files for
// (spec.graph) annotation consistency. Returns an error describing ALL
// violations (not just the first) to minimize fix-and-rebuild cycles.
//
// Rules enforced:
//   - PREDICATE on a scalar field → index required, reverse invalid
//   - PREDICATE on a message field → index ignored (warn), reverse optional
//   - PREDICATE on a repeated or map field → error
//   - INCLUDE → upsert, reverse, index all ignored
//   - upsert only valid with PREDICATE
//   - Every message reachable as a uid edge target must have exactly one upsert field
func ValidateGraphAnnotations(allFiles []model.ProtoFile) error {
	var errs []string

	// First pass: validate individual field annotations.
	// Track which messages are uid-edge targets and which have upsert fields.
	type msgKey struct{ pkg, name string }
	uidEdgeTargets := make(map[msgKey]bool)   // messages referenced via uid edge
	upsertFields := make(map[msgKey][]string) // messages → fields with upsert: true
	graphMessages := make(map[msgKey]bool)    // messages with any graph annotation

	for _, pf := range allFiles {
		for _, msg := range pf.Messages {
			mk := msgKey{pf.Package, msg.Name}

			for _, field := range msg.Fields {
				modeAnn, hasMode := field.Annotations["graph_mode"]
				if !hasMode || modeAnn.StringValue == "" {
					continue
				}
				graphMessages[mk] = true
				mode := modeAnn.StringValue
				loc := fmt.Sprintf("%s.%s.%s", pf.Package, msg.Name, field.Name)

				_, hasIndex := field.Annotations["graph_field_index"]
				upsertAnn, hasUpsert := field.Annotations["graph_upsert"]
				reverseAnn, hasReverse := field.Annotations["graph_reverse"]
				isUpsert := hasUpsert && upsertAnn.BoolValue
				isReverse := hasReverse && reverseAnn.BoolValue

				if mode == "PREDICATE" {
					// Repeated/map fields cannot be predicates.
					if field.IsRepeated || field.IsMap {
						errs = append(errs, fmt.Sprintf(
							"%s: PREDICATE is not valid on %s fields — use INCLUDE to store as JSON",
							loc, fieldKind(field)))
						continue
					}

					if field.IsMessage {
						// uid edge — index is irrelevant, reverse is optional.
						if hasIndex {
							// Not an error, just a no-op. Could warn.
						}
						// Track the target message for upsert validation.
						targetPkg := field.TypePackage
						if targetPkg == "" {
							targetPkg = pf.Package
						}
						uidEdgeTargets[msgKey{targetPkg, field.Type}] = true
					} else {
						// Scalar predicate — index required.
						if !hasIndex || field.Annotations["graph_field_index"].StringValue == "" {
							errs = append(errs, fmt.Sprintf(
								"%s: PREDICATE on scalar field requires index (e.g., \"exact\", \"int\", \"term\")",
								loc))
						}
						// Reverse only valid on uid edges.
						if isReverse {
							errs = append(errs, fmt.Sprintf(
								"%s: reverse is only valid on message (uid edge) predicates, not scalar fields",
								loc))
						}
					}

					if isUpsert {
						upsertFields[mk] = append(upsertFields[mk], field.Name)
					}
				} else if mode == "INCLUDE" {
					// upsert and reverse are meaningless on INCLUDE fields.
					if isUpsert {
						errs = append(errs, fmt.Sprintf(
							"%s: upsert is only valid with mode PREDICATE, not INCLUDE",
							loc))
					}
					if isReverse {
						errs = append(errs, fmt.Sprintf(
							"%s: reverse is only valid with mode PREDICATE, not INCLUDE",
							loc))
					}
				}
			}
		}
	}

	// Second pass: verify uid-edge targets have exactly one upsert field.
	for target := range uidEdgeTargets {
		fields := upsertFields[target]
		if len(fields) == 0 {
			errs = append(errs, fmt.Sprintf(
				"%s.%s: referenced as uid edge target but has no upsert field — "+
					"add (spec.graph) = {mode: PREDICATE, index: \"...\", upsert: true} to the identity field",
				target.pkg, target.name))
		} else if len(fields) > 1 {
			errs = append(errs, fmt.Sprintf(
				"%s.%s: has multiple upsert fields [%s] — exactly one is required",
				target.pkg, target.name, strings.Join(fields, ", ")))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("graph annotation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

func fieldKind(f model.Field) string {
	if f.IsRepeated {
		return "repeated"
	}
	if f.IsMap {
		return "map"
	}
	return "scalar"
}
