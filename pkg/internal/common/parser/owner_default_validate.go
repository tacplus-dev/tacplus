// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/verbs"
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

// OwnerDefaultPlan is what a method's (spec.owner_default) guard refuses,
// decided once for every backend that renders it. The Go and Rust wrappers
// carry the same decision inline (go/types/ownerdefault_gen.go,
// rust/types/ownerdefault_gen.go); Java reads it from here.
type OwnerDefaultPlan struct {
	// Refused names each touched type whose owner right the method's access
	// contradicts, e.g. "Task (write)". Empty means nothing is refused and no
	// guard is rendered.
	Refused string
	// Read is true when the refused access is a read. A read has no row
	// principal to compare: the (spec.store_principal) read filter has already
	// narrowed the rows to the caller, so a self-scoped caller IS the owner. A
	// write is compared against the request's (spec.principal) field.
	Read bool
}

// PlanOwnerDefault decides the (spec.owner_default) refusal for one method.
//
// The types consulted are the request and response types themselves and any
// message-typed field of either at depth 1: a record type is named directly or
// wrapped in a request/response envelope, and a deeper scan would start
// refusing methods that merely mention the type in passing. lookup resolves a
// message by proto package and simple name.
//
// CREATE is exempt: it brings a row into existence, so there is no prior owner
// for the type's owner rights to speak about. A method that touches such a
// type but whose verb did not classify cannot be judged either way, so that is
// an error rather than a silent pass.
func PlanOwnerDefault(method *MethodInfo, servicePkg string, lookup func(pkg, name string) (*model.Message, bool)) (OwnerDefaultPlan, error) {
	if method == nil {
		return OwnerDefaultPlan{}, nil
	}
	found := map[string]OwnerDefaultRights{}
	for _, root := range []string{method.FullInputType, method.FullOutputType} {
		if root == "" || root == "google.protobuf.Empty" {
			continue
		}
		pkg, name := servicePkg, root
		if i := strings.LastIndex(root, "."); i >= 0 {
			pkg, name = root[:i], root[i+1:]
		}
		msg, ok := lookup(pkg, name)
		if !ok {
			continue
		}
		if r, ok := OwnerDefaultOf(msg.Annotations, name); ok {
			found[name] = r
		}
		for _, f := range msg.Fields {
			if !f.IsMessage || f.IsMap {
				continue
			}
			fpkg := f.TypePackage
			if fpkg == "" {
				fpkg = pkg
			}
			fmsg, ok := lookup(fpkg, f.Type)
			if !ok {
				continue
			}
			if r, ok := OwnerDefaultOf(fmsg.Annotations, f.Type); ok {
				found[f.Type] = r
			}
		}
	}
	if len(found) == 0 {
		return OwnerDefaultPlan{}, nil
	}
	if method.Class == nil {
		return OwnerDefaultPlan{}, fmt.Errorf("%s touches a type declaring (spec.owner_default) but its verb did not classify, so its read/write access is unknown and the owner default cannot be enforced", method.Name)
	}
	if method.Class.Pattern == verbs.PatternCreate {
		return OwnerDefaultPlan{}, nil
	}
	names := make([]string, 0, len(found))
	for n := range found {
		names = append(names, n)
	}
	sort.Strings(names)
	var refused []string
	for _, n := range names {
		r := found[n]
		switch {
		case method.Class.Access == verbs.AccessWrite && !r.Write:
			refused = append(refused, fmt.Sprintf("%s (write)", r.Type))
		case method.Class.Access == verbs.AccessRead && !r.Read:
			refused = append(refused, fmt.Sprintf("%s (read)", r.Type))
		}
	}
	if len(refused) == 0 {
		return OwnerDefaultPlan{}, nil
	}
	return OwnerDefaultPlan{
		Refused: strings.Join(refused, ", "),
		Read:    method.Class.Access == verbs.AccessRead,
	}, nil
}
