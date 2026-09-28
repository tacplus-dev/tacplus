// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
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

// OwnerDefaultRights is a type's declared (spec.owner_default): what the row's
// OWNER may do with its own rows. It is read identically by every backend that
// renders the guard, so the reading lives here rather than once per language.
type OwnerDefaultRights struct {
	Type  string // simple message name, for the refusal message
	Read  bool
	Write bool
}

// OwnerDefaultOf reads the annotations parseMessageAnnotations recorded for a
// (spec.owner_default) declaration. Presence is its own marker, so an explicit
// {read: true, write: true} is distinguishable from no declaration at all.
func OwnerDefaultOf(annotations map[string]model.AnnotationValue, name string) (OwnerDefaultRights, bool) {
	if _, ok := annotations["owner_default"]; !ok {
		return OwnerDefaultRights{}, false
	}
	return OwnerDefaultRights{
		Type:  name,
		Read:  annotations["owner_default_read"].BoolValue,
		Write: annotations["owner_default_write"].BoolValue,
	}, true
}
