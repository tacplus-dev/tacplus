// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"strings"
)

// ValidatePrincipalAnnotations enforces the shape the principal annotations
// need to be generatable: the wrapper compares the field against the caller's
// subject string, so the field must be a singular string. Generation FAILS on
// violation rather than emitting code that cannot compile.
func ValidatePrincipalAnnotations(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	var errs []string
	for _, fd := range dp.GetAllFiles() {
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			// At most one principal field per message: the wrapper binds THE
			// principal of the type, so two candidates have no defined meaning.
			seen := map[string]string{}
			for fi := range msg.Fields {
				field := &msg.Fields[fi]
				for _, name := range []string{"principal", "store_principal"} {
					if _, ok := field.Annotations[name]; !ok {
						continue
					}
					if field.Type != "string" || field.IsMap || field.IsRepeated {
						errs = append(errs, fmt.Sprintf("%s.%s.%s: (spec.%s) requires a singular string field; got %s",
							fd.Package, msg.Name, field.Name, name, field.Type))
					}
					if prev, dup := seen[name]; dup {
						errs = append(errs, fmt.Sprintf("%s.%s: (spec.%s) is declared on both %s and %s; exactly one field per message may carry it",
							fd.Package, msg.Name, name, prev, field.Name))
						continue
					}
					seen[name] = field.Name
				}
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("principal annotation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
