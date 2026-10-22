// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
)

// PublicViewSuffix names the generated public view of a (spec.public_view)
// message: T → T + PublicViewSuffix.
const PublicViewSuffix = "Public"

// PublicViewMessages returns, per proto file path, the generated "<T>Public"
// message declarations for every top-level (spec.public_view) message in it:
// the VISIBILITY_SHOW fields only, with their names, numbers and labels kept, a
// SHOW field typed as another public_view message retyped to that message's
// <Sub>Public. Keeping the numbers is what makes the projection a parse: T's
// bytes parsed as <T>Public (unknown fields discarded) are exactly its SHOW
// fields, so every language's <T>ToPublic is one serialize + one parse and no
// per-field code that could name a BACKGROUND field.
//
// It assumes ValidatePublicViewAnnotations passed. Files with no public_view
// message are absent from the map; messages appear in declaration order.
func (p *DescriptorParser) PublicViewMessages() map[string]string {
	out := map[string]string{}
	p.files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		var b strings.Builder
		msgs := fd.Messages()
		for i := 0; i < msgs.Len(); i++ {
			md := msgs.Get(i)
			if !isPublicView(md) {
				continue
			}
			fmt.Fprintf(&b, "\n// %s%s — auto-generated public view of %s: its VISIBILITY_SHOW fields only.\n", md.Name(), PublicViewSuffix, md.FullName())
			fmt.Fprintf(&b, "message %s%s {\n", md.Name(), PublicViewSuffix)
			fields := md.Fields()
			for j := 0; j < fields.Len(); j++ {
				f := fields.Get(j)
				if fieldVisibility(f) != spec.Visibility_VISIBILITY_SHOW {
					continue
				}
				label := ""
				switch {
				case f.IsMap():
				case f.Cardinality() == protoreflect.Repeated:
					label = "repeated "
				case f.HasOptionalKeyword():
					label = "optional "
				}
				typ := publicViewFieldType(f)
				if f.IsMap() {
					typ = fmt.Sprintf("map<%s, %s>", f.MapKey().Kind(), publicViewFieldType(f.MapValue()))
				}
				fmt.Fprintf(&b, "  %s%s %s = %d;\n", label, typ, f.Name(), f.Number())
			}
			b.WriteString("}\n")
		}
		if b.Len() > 0 {
			out[fd.Path()] = b.String()
		}
		return true
	})
	return out
}

// publicViewFieldType is the proto type of a SHOW field (or map value) in
// <T>Public: a message type is the target's <Sub>Public, an enum keeps its own
// type, both fully qualified so the reference resolves from any file.
func publicViewFieldType(f protoreflect.FieldDescriptor) string {
	switch f.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return "." + string(f.Message().FullName()) + PublicViewSuffix
	case protoreflect.EnumKind:
		return "." + string(f.Enum().FullName())
	default:
		return f.Kind().String()
	}
}

func isPublicView(md protoreflect.MessageDescriptor) bool {
	opts := md.Options()
	return opts != nil && proto.HasExtension(opts, spec.E_PublicView) && proto.GetExtension(opts, spec.E_PublicView).(bool)
}

// fieldVisibility returns the field's (spec.visibility), or UNSPECIFIED when
// absent; hasVisibility tells the two apart.
func fieldVisibility(f protoreflect.FieldDescriptor) spec.Visibility {
	if !hasVisibility(f) {
		return spec.Visibility_VISIBILITY_UNSPECIFIED
	}
	return proto.GetExtension(f.Options(), spec.E_Visibility).(spec.Visibility)
}

func hasVisibility(f protoreflect.FieldDescriptor) bool {
	opts := f.Options()
	return opts != nil && proto.HasExtension(opts, spec.E_Visibility)
}

// ValidatePublicViewAnnotations enforces the (spec.public_view) /
// (spec.visibility) contract at generation time, for every target language:
//
//   - every field of a public_view message declares (spec.visibility) as SHOW or
//     BACKGROUND. There is no default: an unannotated field is an error, so adding
//     a field to a prospect record forces the author to decide which side it is on;
//   - (spec.visibility) on a field of a message that is not public_view is an
//     error (an annotation nothing reads), as is either annotation on a NESTED
//     message declaration (the generators only walk top-level messages);
//   - a SHOW field typed as a message (singular, repeated, or a map value) must
//     name a top-level public_view message: showing an unannotated message would
//     show all of it, BACKGROUND fields included. Its <Sub>Public stands in;
//   - a SHOW field may not be a oneof member (<T>Public does not replicate oneofs);
//   - <T>Public must not collide with a declared message;
//   - no RPC callable without a caller token (RPC_FLAG_PUBLIC_NO_AUTH /
//     RPC_FLAG_SELF_AUTHENTICATES, a C2S auth_disabled service) and no C2D (local,
//     unauthenticated) RPC may carry a public_view message anywhere in its request
//     or response: that is a read path open to whoever renders a page, and only
//     <T>Public may be handed to a renderer.
//
// Returns every violation at once.
func ValidatePublicViewAnnotations(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	var errs []string
	dp.files.RangeFiles(func(fd protoreflect.FileDescriptor) bool {
		msgs := fd.Messages()
		for i := 0; i < msgs.Len(); i++ {
			validatePublicViewMessage(dp, msgs.Get(i), false, &errs)
		}
		return true
	})
	errs = append(errs, publicViewUnauthReads(dp)...)
	if len(errs) == 0 {
		return nil
	}
	sort.Strings(errs)
	return fmt.Errorf("public_view annotation validation failed:\n  - %s", strings.Join(errs, "\n  - "))
}

func validatePublicViewMessage(p *DescriptorParser, md protoreflect.MessageDescriptor, nested bool, errs *[]string) {
	if md.IsMapEntry() {
		return
	}
	pv := isPublicView(md)
	if nested && pv {
		*errs = append(*errs, fmt.Sprintf("%s: (spec.public_view) is declared on a NESTED message declaration; hoist it to top level", md.FullName()))
	}
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		name := fmt.Sprintf("%s.%s", md.FullName(), f.Name())
		if !pv || nested {
			if hasVisibility(f) {
				*errs = append(*errs, fmt.Sprintf("%s: (spec.visibility) is only valid on a field of a top-level (spec.public_view) message", name))
			}
			continue
		}
		switch fieldVisibility(f) {
		case spec.Visibility_VISIBILITY_BACKGROUND:
			continue
		case spec.Visibility_VISIBILITY_SHOW:
		default:
			*errs = append(*errs, fmt.Sprintf("%s: every field of a (spec.public_view) message must declare (spec.visibility) = VISIBILITY_SHOW or VISIBILITY_BACKGROUND", name))
			continue
		}
		if f.ContainingOneof() != nil && !f.HasOptionalKeyword() {
			*errs = append(*errs, fmt.Sprintf("%s: a VISIBILITY_SHOW field cannot be a oneof member", name))
		}
		target := f
		if f.IsMap() {
			target = f.MapValue()
		}
		if target.Kind() == protoreflect.MessageKind || target.Kind() == protoreflect.GroupKind {
			sub := target.Message()
			if _, topLevel := sub.Parent().(protoreflect.FileDescriptor); !isPublicView(sub) || !topLevel {
				*errs = append(*errs, fmt.Sprintf("%s: VISIBILITY_SHOW field of type %s would show every field of it; mark %s (spec.public_view) and annotate its fields, or make the field VISIBILITY_BACKGROUND", name, sub.FullName(), sub.FullName()))
			}
		}
	}
	if pv && !nested {
		gen := md.FullName().Parent().Append(protoreflect.Name(string(md.Name()) + PublicViewSuffix))
		if _, err := p.files.FindDescriptorByName(gen); err == nil {
			*errs = append(*errs, fmt.Sprintf("%s: %s is generated from (spec.public_view) and must not be declared", md.FullName(), gen))
		}
	}
	nestedMsgs := md.Messages()
	for i := 0; i < nestedMsgs.Len(); i++ {
		validatePublicViewMessage(p, nestedMsgs.Get(i), true, errs)
	}
}

// publicViewUnauthReads reports every RPC reachable without a caller token
// whose request or response reaches a public_view message.
func publicViewUnauthReads(p *DescriptorParser) []string {
	var errs []string
	for _, fdesc := range p.GetAllFiles() {
		for _, svc := range fdesc.Services {
			sd, err := p.files.FindDescriptorByName(protoreflect.FullName(fdesc.Package + "." + svc.Name))
			if err != nil {
				continue
			}
			for _, m := range svc.Methods {
				if !(svc.IsC2D || svc.AuthDisabled || m.AuthDisabled) {
					continue
				}
				md := sd.(protoreflect.ServiceDescriptor).Methods().ByName(protoreflect.Name(m.Name))
				if md == nil {
					continue
				}
				for _, io := range []protoreflect.MessageDescriptor{md.Input(), md.Output()} {
					if pv := reachablePublicView(io, map[protoreflect.FullName]bool{}); pv != "" {
						errs = append(errs, fmt.Sprintf("%s.%s.%s: callable without a caller token but carries (spec.public_view) message %s, BACKGROUND fields included; "+
							"only %s%s (via the generated %sToPublic) may leave the company",
							fdesc.Package, svc.Name, m.Name, pv, pv, PublicViewSuffix, pv.Name()))
					}
				}
			}
		}
	}
	return errs
}

// reachablePublicView returns the first public_view message reachable from md
// through its fields (md itself included), or "".
func reachablePublicView(md protoreflect.MessageDescriptor, seen map[protoreflect.FullName]bool) protoreflect.FullName {
	if seen[md.FullName()] {
		return ""
	}
	seen[md.FullName()] = true
	if isPublicView(md) {
		return md.FullName()
	}
	fields := md.Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		if f.IsMap() {
			f = f.MapValue()
		}
		if f.Kind() != protoreflect.MessageKind && f.Kind() != protoreflect.GroupKind {
			continue
		}
		if pv := reachablePublicView(f.Message(), seen); pv != "" {
			return pv
		}
	}
	return ""
}
