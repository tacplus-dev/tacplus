// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"sort"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/verbs"
)

// The owner_default seam (§4b). (spec.owner_default) on a row type says what the
// type's owner may do with its own rows; the generated service WRAPPER refuses
// the RPCs that contradict it, at the same seam that already owns the principal
// stamp and the read filter. A handler never sees the refused call.
//
// It sits on the AUTH line rather than in the response path, so the refusal
// applies to every method shape — unary, PAGE, streaming alike — instead of the
// unary-only egress seam (spec.store_principal) has to use.

// ownerDefaultRights is a type's declared owner rights.
type ownerDefaultRights struct {
	Type  string // simple message name, for the refusal message
	Read  bool
	Write bool
}

// ownerDefaultTypes returns the owner_default-bearing message types a method
// touches: the request or response type itself, and any message-typed field of
// either at depth 1. Depth 1 is the whole of it — a record type is named
// directly or wrapped in a request/response envelope, and a deeper scan would
// start refusing methods that merely mention the type in passing.
func (es *rustEgressSanitizer) ownerDefaultTypes(method *parser.MethodInfo, servicePkg string) []ownerDefaultRights {
	if es == nil || method == nil {
		return nil
	}
	found := map[string]ownerDefaultRights{}
	for _, root := range []string{method.FullInputType, method.FullOutputType} {
		if root == "" || root == "google.protobuf.Empty" {
			continue
		}
		pkg, name := splitRustEgressFQN(root, servicePkg)
		msg, ok := es.msgs[pkg+"\x00"+name]
		if !ok {
			continue
		}
		if r, ok := ownerDefaultOf(msg.Annotations, name); ok {
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
			fmsg, ok := es.msgs[fpkg+"\x00"+f.Type]
			if !ok {
				continue
			}
			if r, ok := ownerDefaultOf(fmsg.Annotations, f.Type); ok {
				found[f.Type] = r
			}
		}
	}
	if len(found) == 0 {
		return nil
	}
	names := make([]string, 0, len(found))
	for n := range found {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]ownerDefaultRights, 0, len(names))
	for _, n := range names {
		out = append(out, found[n])
	}
	return out
}

// ownerDefaultOf reads the annotations parseMessageAnnotations recorded for a
// (spec.owner_default) declaration. Presence is its own marker, so an explicit
// {read: true, write: true} is distinguishable from no declaration at all.
func ownerDefaultOf(annotations map[string]model.AnnotationValue, name string) (ownerDefaultRights, bool) {
	if _, ok := annotations["owner_default"]; !ok {
		return ownerDefaultRights{}, false
	}
	return ownerDefaultRights{
		Type:  name,
		Read:  annotations["owner_default_read"].BoolValue,
		Write: annotations["owner_default_write"].BoolValue,
	}, true
}

// rustOwnerDefaultGuard renders the refusal the wrapper emits when the method's
// static access contradicts the owner rights of a type it touches, or "" when
// nothing is declared or nothing is contradicted. The error names the type and
// the right, so a denial here is never mistaken for a missing grant.
//
// A method that touches such a type but carries no resolved verb classification
// cannot be judged either way; that is an error rather than a silent pass,
// matching how the principal read filter refuses to under-enforce.
func rustOwnerDefaultGuard(method *parser.MethodInfo, san *rustEgressSanitizer, servicePkg string) (string, error) {
	rights := san.ownerDefaultTypes(method, servicePkg)
	if len(rights) == 0 {
		return "", nil
	}
	if method.Class == nil {
		return "", fmt.Errorf("%s touches a type declaring (spec.owner_default) but its verb did not classify, so its read/write access is unknown and the owner default cannot be enforced", method.Name)
	}
	var refused []string
	for _, r := range rights {
		switch {
		case method.Class.Access == verbs.AccessWrite && !r.Write:
			refused = append(refused, fmt.Sprintf("%s (write)", r.Type))
		case method.Class.Access == verbs.AccessRead && !r.Read:
			refused = append(refused, fmt.Sprintf("%s (read)", r.Type))
		}
	}
	if len(refused) == 0 {
		return "", nil
	}
	return fmt.Sprintf("\n        // (spec.owner_default): %s forbids this access for the row owner.\n"+
		"        return Err(tonic::Status::permission_denied(\n"+
		"            \"%s: (spec.owner_default) on %s denies the owner this operation\"));",
		strings.Join(refused, ", "), method.Name, strings.Join(refused, ", ")), nil
}
