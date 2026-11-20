// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// accountingRoots are the accounting event types (tacplus#273): the emit-time
// envelope of sys/accounting.proto and the flat record a consumer declares for
// the accounting topic (boundaries.AccountingRecordTypeFQN). Everything
// reachable from them is accounting, and accounting carries no data.
var accountingRoots = [][2]string{
	{"accounting", "Event"},
	{"accountingrecord", "AccountingRecord"},
}

// ValidateAccountingSafe is the no-data gate of tacplus#273. Every string or
// bytes field (singular, repeated, a map's key or value) reachable from an
// accounting event type must be an identifier or an enum-like code, and say so
// with (spec.accounting_safe); a field that does not is free text that could
// carry request data, an error message or a stack trace, and fails generation.
// (spec.accounting_safe) on any other type is refused. The injected trace_id is
// an identifier and is not gated. A proto set that declares no accounting type
// is not gated.
func ValidateAccountingSafe(allFiles []model.ProtoFile) error {
	r := NewMessageResolver(allFiles)
	var unmarked, misplaced []string
	seen := map[string]bool{}
	var walk func(m *model.Message, pkg string)
	walk = func(m *model.Message, pkg string) {
		k := pkg + "\x00" + m.Name
		if seen[k] {
			return
		}
		seen[k] = true
		for _, f := range m.Fields {
			_, safe := f.Annotations["accounting_safe"]
			name := fmt.Sprintf("%s.%s.%s", pkg, m.Name, f.Name)
			text := FieldLeafKind(f) == LeafString || FieldLeafKind(f) == LeafBytes ||
				(f.IsMap && (f.MapKeyType == "string" || f.MapKeyType == "bytes"))
			switch {
			case isInjectedTraceID(f.Name, f.Number):
			case text && !safe:
				unmarked = append(unmarked, name)
			case !text && safe:
				misplaced = append(misplaced, name)
			}
			if f.IsMap && f.MapValueIsMessage {
				f = model.Field{Name: f.Name, Type: f.MapValueType, TypePackage: f.MapValueTypePackage, IsMessage: true}
			}
			if t, ok := r.Resolve(pkg, f); ok {
				walk(t, childPkg(pkg, f))
			}
		}
	}
	for _, root := range accountingRoots {
		if m, ok := r.byPkgName[root[0]+"\x00"+root[1]]; ok {
			walk(m, root[0])
		}
	}
	var errs []string
	if len(unmarked) > 0 {
		sort.Strings(unmarked)
		errs = append(errs, fmt.Sprintf("accounting carries no data (tacplus#273): these string or bytes fields of an accounting event "+
			"are not marked (spec.accounting_safe): %s — mark each identifier or enum-like code, and make anything else a number or an enum",
			strings.Join(unmarked, ", ")))
	}
	if len(misplaced) > 0 {
		sort.Strings(misplaced)
		errs = append(errs, fmt.Sprintf("(spec.accounting_safe) is valid only on a string or bytes field: %s", strings.Join(misplaced, ", ")))
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(errs, "; "))
}
