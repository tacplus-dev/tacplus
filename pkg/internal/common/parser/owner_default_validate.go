// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"strings"
)

// ValidateOwnerDefaultAnnotations enforces the one invariant (spec.owner_default)
// needs to mean anything: the option states what the type's OWNER may do, and the
// owner is the principal named by the type's (spec.store_principal) field. A
// message that declares owner_default without such a field has no owner, so the
// declaration would be enforced against nobody in particular. Generation FAILS
// on that rather than emitting a guard whose subject is undefined.
func ValidateOwnerDefaultAnnotations(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	var errs []string
	for _, fd := range dp.GetAllFiles() {
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			if _, ok := msg.Annotations["owner_default"]; !ok {
				continue
			}
			owner := ""
			for fi := range msg.Fields {
				if _, ok := msg.Fields[fi].Annotations["store_principal"]; ok {
					owner = msg.Fields[fi].Name
					break
				}
			}
			if owner == "" {
				errs = append(errs, fmt.Sprintf("%s.%s: (spec.owner_default) requires the message to carry a (spec.store_principal) field naming its owner",
					fd.Package, msg.Name))
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("owner_default annotation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
