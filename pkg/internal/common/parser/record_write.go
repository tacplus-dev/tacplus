// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"sort"
	"strings"

	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
)

// ValidateRecordWriteAnnotations enforces (spec.record).s2s_write, the grant
// half of the record write surface the record-map injector emits on the record
// query plane's S2S half. It is refused, rather than silently doing nothing,
// when:
//
//   - it declares no auth_services: a write surface callable by nobody is a
//     declaration that did not mean what it says, and folding it back into the
//     trait-wide list would turn an attempt to narrow access into the widest
//     grant there is (the s2s_read rule, for the same reason);
//   - no (spec.trait) hosts a record query plane: nothing would emit or
//     enforce the surface;
//   - the record is (spec.record).private: the peer-facing surface has no
//     caller principal to stamp as the row's owner, so the plane emits no write
//     surface for a private type and the grant would authorize nothing.
func ValidateRecordWriteAnnotations(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	all := dp.GetAllFiles()
	paths := make([]string, 0, len(all))
	for path := range all {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	fds := make([]*FileDescriptor, 0, len(all))
	for _, path := range paths {
		fds = append(fds, all[path])
	}
	return validateRecordWriteAnnotations(fds)
}

// validateRecordWriteAnnotations is the file-list form of
// ValidateRecordWriteAnnotations; fds must be in a deterministic order.
func validateRecordWriteAnnotations(fds []*FileDescriptor) error {
	planeExists := hasRecordQueryPlane(fds)
	var errs []string
	for _, fd := range fds {
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			if ann, ok := msg.Annotations["record"]; !ok || !ann.BoolValue {
				continue
			}
			sw, ok := msg.Annotations["record_s2s_write"]
			if !ok || !sw.BoolValue {
				continue
			}
			fqn := fd.Package + "." + msg.Name
			switch {
			case len(sw.StringSliceValue) == 0:
				errs = append(errs, fmt.Sprintf(
					"%s: (spec.record).s2s_write declares no auth_services; the record's write surface would be callable by nobody "+
						"(name the writing services, or use [\"*\"] for any authenticated service, or remove the block to keep the trait-wide default)",
					fqn))
			case msg.Annotations["record_private"].BoolValue:
				errs = append(errs, fmt.Sprintf(
					"%s: (spec.record).private is true and it declares s2s_write; the record write surface is peer-facing, "+
						"so a write carries no caller principal to stamp as the row's owner and the plane emits no write surface "+
						"for a private type — the grant would authorize nothing. Drop s2s_write or drop private",
					fqn))
			case !planeExists:
				errs = append(errs, fmt.Sprintf(
					"%s: (spec.record).s2s_write declares a write audience but no (spec.trait) hosts a record query plane, so nothing would emit or enforce it%s",
					fqn, noRecordQueryPlaneHint))
			}
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("record write annotation errors:\n  %s", strings.Join(errs, "\n  "))
}

// recordApplyOps reads the (spec.record_op) stamps off an RPC's request: the
// cases of the RecordOp oneof carried by the request's repeated ops field. It
// returns nil for every request that carries none, which is every RPC but the
// record write surface's Apply.
func recordApplyOps(input protoreflect.MessageDescriptor) []RecordEntry {
	if input == nil {
		return nil
	}
	var ops []RecordEntry
	fields := input.Fields()
	for i := 0; i < fields.Len(); i++ {
		f := fields.Get(i)
		if f.Kind() != protoreflect.MessageKind || !f.IsList() {
			continue
		}
		opMsg := f.Message()
		cases := opMsg.Fields()
		for j := 0; j < cases.Len(); j++ {
			c := cases.Get(j)
			if c.ContainingOneof() == nil || c.Kind() != protoreflect.MessageKind {
				continue
			}
			opts, _ := c.Options().(*descriptorpb.FieldOptions)
			if opts == nil || !proto.HasExtension(opts, spec.E_RecordOp) {
				continue
			}
			b, _ := proto.GetExtension(opts, spec.E_RecordOp).(*spec.RecordEntryBinding)
			if b == nil {
				continue
			}
			e := recordEntryOf(b, c.Message())
			e.OpsField = string(f.Name())
			e.OpType = string(opMsg.FullName())
			e.OneofName = string(c.ContainingOneof().Name())
			e.CaseField = string(c.Name())
			e.CaseType = string(c.Message().FullName())
			ops = append(ops, e)
		}
	}
	return ops
}

// recordEntryOf resolves a binding against the message it is stamped for.
func recordEntryOf(b *spec.RecordEntryBinding, msg protoreflect.MessageDescriptor) RecordEntry {
	e := RecordEntry{Record: b.GetRecord(), RPC: b.GetRpc(), Durability: b.GetDurability()}
	if p := b.GetKeyPath(); p != "" {
		e.KeyPath = strings.Split(p, ".")
		e.KeyKind = keyKindAt(msg, e.KeyPath)
	}
	if p := b.GetNamespacePath(); p != "" {
		e.NamespacePath = strings.Split(p, ".")
	}
	return e
}

// keyKindAt walks path from msg and returns the proto kind of the field it ends
// on ("string", "int64", ...), or "" when the path does not resolve to a scalar.
func keyKindAt(msg protoreflect.MessageDescriptor, path []string) string {
	for i, name := range path {
		if msg == nil {
			return ""
		}
		f := msg.Fields().ByName(protoreflect.Name(name))
		if f == nil {
			return ""
		}
		if i == len(path)-1 {
			if f.Kind() == protoreflect.MessageKind || f.Kind() == protoreflect.GroupKind || f.IsList() || f.IsMap() {
				return ""
			}
			return f.Kind().String()
		}
		msg = f.Message()
	}
	return ""
}
