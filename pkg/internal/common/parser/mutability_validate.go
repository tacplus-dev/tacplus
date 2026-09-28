// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"sort"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/verbs"
)

// The generation end of (spec.record).mutability = MUTABILITY_APPEND_ONLY.
//
// "Enforced at BOTH ends or it is decoration" is the shape (spec.record).min_group_size
// already has: one declared value drives a generation-time refusal AND a rendered
// runtime guard, and generation fails outright when the guard cannot be rendered
// (javaMinGroupSizeUnenforceable). Append-only follows it one-for-one, with one
// difference that has to be said plainly rather than papered over:
//
// THERE IS NO ROW-MUTATION PLANE TODAY. Rows reach the record store by EventBus
// publish, because (spec.record) implies (spec.event); the generated record query
// plane's Update<T>Query / Delete<T>Query operate on a QUERY HANDLE, not on a row
// (Update<T>Query replaces the predicate, Delete<T>Query closes the query). So
// "append-only emits no mutating RPCs" cannot mean "remove an RPC that exists".
// It means: the generator must never emit one, and an author must not be able to
// declare one either.
//
// A ROW-MUTATING RPC for record type T is defined here, once, as an RPC whose
// resolved verb pattern is UPDATE or DELETE and whose request or response message
// IS T. That definition deliberately does not catch the query plane — its
// Update/Delete carry Refine<T>Request / QueryHandle / QueryStats, never T — so the
// plane emitted today is untouched, which is what "emits no mutating RPCs for such
// a record" has to mean in a tree that emits none.
//
// The runtime end lives in the per-language service wrapper generators, which
// refuse to HOST such a method (see javaAppendOnlyUnenforceable and its Go/Rust
// siblings). Both ends read the same annotation.

// appendOnlyRecordTypes returns the fully-qualified names of every record type in
// the loaded proto set declaring MUTABILITY_APPEND_ONLY, mapped to the package it
// was declared in, in no particular order (callers sort what they emit).
func appendOnlyRecordTypes(dp *DescriptorParser) map[string]bool {
	out := make(map[string]bool)
	if dp == nil {
		return out
	}
	for _, fd := range dp.GetAllFiles() {
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			if ann, ok := msg.Annotations["record"]; !ok || !ann.BoolValue {
				continue
			}
			if msg.Annotations["record_mutability"].StringValue != "MUTABILITY_APPEND_ONLY" {
				continue
			}
			out[fd.Package+"."+msg.Name] = true
		}
	}
	return out
}

// IsRowMutatingPattern reports whether a resolved verb pattern mutates a row (as
// opposed to a query handle, a session, or any other non-row resource). Exported
// so the wrapper generators classify a method the same way this validator does —
// two definitions of "row mutation" is how one end silently stops matching the
// other.
func IsRowMutatingPattern(c *verbs.Classification) bool {
	if c == nil {
		return false
	}
	return c.Pattern == verbs.PatternUpdate || c.Pattern == verbs.PatternDelete
}

// AppendOnlyRecordOnMethod returns the fully-qualified append-only record type a
// row-mutating method carries on its request or response, or "" when the method
// is not a row mutation over an append-only record. appendOnly is the set from
// appendOnlyRecordTypes.
//
// The record type must appear as the message ITSELF, not merely be reachable
// from it: a response that happens to embed the record (a query result, a watch
// event) is a read, and refusing those would make the annotation unusable.
func AppendOnlyRecordOnMethod(m *MethodInfo, appendOnly map[string]bool) string {
	if m == nil || len(appendOnly) == 0 || !IsRowMutatingPattern(m.Class) {
		return ""
	}
	for _, t := range []string{m.FullInputType, m.FullOutputType} {
		if t != "" && appendOnly[t] {
			return t
		}
	}
	return ""
}

// validateAppendOnlyMethods is the generation-end refusal: no service may declare
// a row-mutating RPC over an append-only record type. Errors are accumulated and
// reported together, sorted, so the diagnostic is stable and an author sees every
// offending RPC in one pass rather than one per regeneration.
func validateAppendOnlyMethods(services []*ServiceInfo, appendOnly map[string]bool) error {
	if len(appendOnly) == 0 {
		return nil
	}
	var errs []string
	for _, svc := range services {
		if svc == nil {
			continue
		}
		for _, m := range svc.Methods {
			recordType := AppendOnlyRecordOnMethod(m, appendOnly)
			if recordType == "" {
				continue
			}
			errs = append(errs, fmt.Sprintf(
				"%s.%s is a %s over %s, which declares (spec.record).mutability = MUTABILITY_APPEND_ONLY; "+
					"an append-only record's rows are written once, so no row-mutating RPC is generated for "+
					"the type and none may be declared against it. Publish a new row to the record's EventBus "+
					"topic instead, or drop MUTABILITY_APPEND_ONLY from %s",
				svc.Name, m.Name, m.Class.Pattern, recordType, recordType))
		}
	}
	if len(errs) == 0 {
		return nil
	}
	sort.Strings(errs)
	return fmt.Errorf("append-only record mutation errors:\n  %s", strings.Join(errs, "\n  "))
}
