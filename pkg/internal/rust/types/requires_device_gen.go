// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
)

// The Rust half of (spec.rpc).requires_device — the device-only approval check.
//
// Go and Java compute the approval's request hash by REFLECTION: protoreflect
// and protobuf-java both carry a descriptor at runtime, so one hand-written
// chassis function canonicalizes any message. Rust has neither. prost generates
// plain structs with no descriptor and no reflection, so there is nothing for a
// hand-written chassis function to walk.
//
// So the canonicalization is GENERATED here instead, one function per message
// reachable from a guarded request. That is not a lesser port: the generator
// holds the same descriptor Go and Java read at runtime, so it emits the same
// decisions (field numbers, wire types, recursion) as straight-line code. What
// it must NOT do is lean on prost's own encoder — prost packs repeated scalars
// and owes no field ordering, which is exactly the "deterministic marshal" trap
// the Go chassis warns about at the top of approval.go.tmpl.
//
// The bytes these functions produce are pinned against Go's and Java's by
// approval_canonical_test.go's cross-language vector. Kernel compares raw hash
// bytes and never re-derives them, so a Rust-only drift is a silent refusal of
// every cross-language approval, not a test failure — hence the pinned vector.

// rustApprovalCanonicalFnPrefix is the emitted per-message canonicalization
// function, before the message's snake name.
const rustApprovalCanonicalFnPrefix = "approval_canonical_"

// rustApprovalHashFnPrefix is the emitted per-request hash function, before the
// message's snake name. Only guarded REQUEST types get one; the nested messages
// they reach get a canonicalization function only.
const rustApprovalHashFnPrefix = "approval_request_hash_"

// rustApprovalMsgKey keys a message by package and simple name, the same
// spelling rustEgressSanitizer.msgs uses.
func rustApprovalMsgKey(pkg, name string) string { return pkg + "\x00" + name }

// rustApprovalPlan is the emit-time answer to "which messages need a
// canonicalization function, and which of them are guarded request types".
type rustApprovalPlan struct {
	// canonical holds every message reachable from a guarded request,
	// including the requests themselves. Keyed by rustApprovalMsgKey.
	canonical map[string]bool
	// requests holds only the guarded request types, keyed the same way.
	requests map[string]bool
}

// needsCanonical reports whether msg in pkg gets a canonicalization function.
func (p *rustApprovalPlan) needsCanonical(pkg, name string) bool {
	return p != nil && p.canonical[rustApprovalMsgKey(pkg, name)]
}

// needsHash reports whether msg in pkg is a guarded request and so also gets a
// hash function.
func (p *rustApprovalPlan) needsHash(pkg, name string) bool {
	return p != nil && p.requests[rustApprovalMsgKey(pkg, name)]
}

// empty reports whether nothing at all is to be emitted. A proto set with no
// requires_device rpc generates byte-identical output to before this existed.
func (p *rustApprovalPlan) empty() bool { return p == nil || len(p.canonical) == 0 }

// buildRustApprovalPlan walks every (spec.rpc).requires_device request in the
// proto set and closes over the messages it reaches.
//
// A request tree that reaches a MAP field is dropped from the plan rather than
// refused here. The refusal belongs at the service wrapper
// (rustRequiresDeviceMapField), where Go and Java both make it and where the
// error can name the rpc; emitting a second refusal from the types builder —
// which runs first — would just hide the better message behind a worse one.
func buildRustApprovalPlan(dp *parser.DescriptorParser, files []model.ProtoFile) *rustApprovalPlan {
	if dp == nil {
		return &rustApprovalPlan{canonical: map[string]bool{}, requests: map[string]bool{}}
	}
	msgs := make(map[string]*model.Message)
	for fi := range files {
		pf := &files[fi]
		for mi := range pf.Messages {
			msgs[rustApprovalMsgKey(pf.Package, pf.Messages[mi].Name)] = &pf.Messages[mi]
		}
	}

	plan := &rustApprovalPlan{canonical: map[string]bool{}, requests: map[string]bool{}}

	// Sorted path order for the same reason DeriveApprovalBrokerClient uses it:
	// a raw map walk over a package spanning several files could otherwise
	// close over a different message set on each build.
	var paths []string
	for rp := range dp.GetAllFiles() {
		paths = append(paths, rp)
	}
	sort.Strings(paths)

	for _, rp := range paths {
		fd := dp.GetAllFiles()[rp]
		if fd == nil {
			continue
		}
		for _, svc := range fd.Services {
			for _, m := range svc.Methods {
				if !m.HasDeviceGate() {
					continue
				}
				// MethodDescriptor.InputType is already package-qualified
				// ("sentinel.DeleteNodeStateRequest"); InputTypePackage is the
				// fallback for the unqualified spelling.
				pkg, name := splitRustEgressFQN(m.InputType, fd.Package)
				if !strings.Contains(m.InputType, ".") && m.InputTypePackage != "" {
					pkg = m.InputTypePackage
				}
				// google.protobuf.Empty canonicalizes to the empty byte
				// string, which needs no generated function.
				if pkg == "google.protobuf" {
					continue
				}
				reached := map[string]bool{}
				if !rustApprovalReach(pkg, name, msgs, reached) {
					// Reaches a map, or a message this generator cannot
					// resolve. Either way the wrapper refuses this build with
					// an error naming the rpc, which is the better message.
					continue
				}
				for k := range reached {
					plan.canonical[k] = true
				}
				plan.requests[rustApprovalMsgKey(pkg, name)] = true
			}
		}
	}
	return plan
}

// rustApprovalReach closes over the messages reachable from pkg.name, adding
// each to reached. It returns false as soon as a map field is found anywhere in
// the tree — the canonicalization recurses, so a map three messages down is the
// same cross-language iteration-order nondeterminism as one at the top level.
func rustApprovalReach(pkg, name string, msgs map[string]*model.Message, reached map[string]bool) bool {
	key := rustApprovalMsgKey(pkg, name)
	if reached[key] {
		return true
	}
	// A well-known message is canonicalized INLINE by the emitter, so it needs
	// no entry of its own and is not a refusal.
	if rustApprovalWellKnown[pkg+"."+name] {
		return true
	}
	msg, ok := msgs[key]
	if !ok {
		// A message this generator cannot resolve would be SILENTLY ABSENT
		// from the Rust hash while Go and Java reflect into it. Drop the whole
		// request from the plan; the wrapper then refuses the build naming the
		// type (rustRequiresDeviceUnhashableType), rather than this emitting a
		// hash no other language agrees with.
		return false
	}
	reached[key] = true
	for i := range msg.Fields {
		f := &msg.Fields[i]
		if f.IsMap {
			return false
		}
		if !f.IsMessage {
			continue
		}
		sub := f.TypePackage
		if sub == "" {
			sub = pkg
		}
		if !rustApprovalReach(sub, f.Type, msgs, reached) {
			return false
		}
	}
	return true
}

// rustApprovalWellKnown is the set of google.protobuf messages this generator
// canonicalizes INLINE rather than through a generated function, because the
// types crate does not own them and so cannot carry a function for them.
// Timestamp and Duration share one shape: seconds = 1 (int64), nanos = 2
// (int32). Go and Java reach the same two fields by reflection, so the inline
// bytes are the same bytes.
//
// Any OTHER unresolvable message type in a guarded request tree is refused at
// the service wrapper rather than skipped: skipping a field Go would have
// hashed is precisely the silent cross-language drift this feature cannot
// tolerate.
var rustApprovalWellKnown = map[string]bool{
	"google.protobuf.Timestamp": true,
	"google.protobuf.Duration":  true,
}

// rustApprovalPrimitives is the wire-writing floor every generated
// canonicalization function stands on. It is emitted privately into each
// helpers.rs that carries such a function — the same posture
// generateHelperValidators takes for the shared validators, and the reason the
// functions are `fn` and not `pub fn`: a package's copy is its own.
const rustApprovalPrimitives = `
// ---- (spec.rpc).requires_device canonicalization ----
//
// The canonical encoding of a request, which the approval's request_hash is
// SHA-256 over. It is deliberately NOT prost's own encoder: prost packs
// repeated scalars and owes no field ordering, and a "deterministic marshal"
// that agrees with itself while disagreeing with Go and Java is the exact
// failure this encoding exists to prevent. Populated fields only, ascending
// field number, protobuf wire types, recursing into nested messages under the
// same rule. Byte-identical to the Go and Java chassis by construction and by
// the pinned cross-language vector.
//
// Each arm is allow(dead_code): a request tree carrying no float, no fixed and
// no sint field legitimately uses only some of them, and an unused arm is not a
// defect to warn about.

#[allow(dead_code)]
fn approval_put_varint(out: &mut Vec<u8>, mut v: u64) {
    loop {
        if v < 0x80 {
            out.push(v as u8);
            return;
        }
        out.push((v as u8) | 0x80);
        v >>= 7;
    }
}

#[allow(dead_code)]
fn approval_put_tag(out: &mut Vec<u8>, num: u32, wire: u32) {
    approval_put_varint(out, ((num as u64) << 3) | (wire as u64));
}

#[allow(dead_code)]
fn approval_put_bytes(out: &mut Vec<u8>, num: u32, b: &[u8]) {
    approval_put_tag(out, num, 2);
    approval_put_varint(out, b.len() as u64);
    out.extend_from_slice(b);
}

#[allow(dead_code)]
fn approval_put_fixed32(out: &mut Vec<u8>, num: u32, v: u32) {
    approval_put_tag(out, num, 5);
    out.extend_from_slice(&v.to_le_bytes());
}

#[allow(dead_code)]
fn approval_put_fixed64(out: &mut Vec<u8>, num: u32, v: u64) {
    approval_put_tag(out, num, 1);
    out.extend_from_slice(&v.to_le_bytes());
}

/// Zigzag for sint32/sint64, over u64 arithmetic so a shift of a large
/// magnitude cannot overflow-panic in a debug build.
#[allow(dead_code)]
fn approval_zigzag(v: i64) -> u64 {
    ((v as u64) << 1) ^ ((v >> 63) as u64)
}
`

// writeApprovalCanonical emits the canonicalization functions this proto file
// owes the plan, and the primitives they stand on. It writes nothing at all
// when the file reaches no guarded request — a proto set with no
// (spec.rpc).requires_device rpc generates exactly what it generated before.
func writeApprovalCanonical(b *strings.Builder, pf model.ProtoFile, allProtos []model.ProtoFile, plan *rustApprovalPlan) {
	if plan.empty() {
		return
	}
	var owed []*model.Message
	for i := range pf.Messages {
		if plan.needsCanonical(pf.Package, pf.Messages[i].Name) {
			owed = append(owed, &pf.Messages[i])
		}
	}
	if len(owed) == 0 {
		return
	}

	protoByPkg := make(map[string]model.ProtoFile, len(allProtos))
	for _, p := range allProtos {
		protoByPkg[p.Package] = p
	}

	b.WriteString(rustApprovalPrimitives)
	b.WriteString("\n")
	for _, msg := range owed {
		writeApprovalCanonicalMessage(b, *msg, pf.Package, protoByPkg, plan)
	}
}

// approvalField pairs a field with the number it is emitted at, so a oneof
// MEMBER can take its own position in the ascending-field-number order rather
// than the position of the oneof as a whole. Go sorts the POPULATED fields, and
// which member of a oneof is populated is only known at runtime, so each member
// is emitted as its own guarded block at its own number — the one shape whose
// output order matches the sort for every possible input.
type approvalField struct {
	f   *model.Field
	num int
}

func writeApprovalCanonicalMessage(b *strings.Builder, msg model.Message, pkg string, protoByPkg map[string]model.ProtoFile, plan *rustApprovalPlan) {
	typeName := toProstTypeName(msg.Name)
	snake := strutil.ToSnakeCase(typeName)

	var fields []approvalField
	for i := range msg.Fields {
		fields = append(fields, approvalField{f: &msg.Fields[i], num: msg.Fields[i].Number})
	}
	sort.SliceStable(fields, func(i, j int) bool { return fields[i].num < fields[j].num })

	fmt.Fprintf(b, "/// %s%s appends the canonical bytes of m, as the (spec.rpc).requires_device\n", rustApprovalCanonicalFnPrefix, snake)
	b.WriteString("/// approval hash defines them: populated fields only, ascending field number.\n")
	fmt.Fprintf(b, "pub fn %s%s(m: &%s, out: &mut Vec<u8>) {\n", rustApprovalCanonicalFnPrefix, snake, typeName)
	if len(fields) == 0 {
		b.WriteString("    let _ = (m, out);\n")
	}
	for _, af := range fields {
		writeApprovalCanonicalField(b, af.f, msg, pkg, protoByPkg, plan)
	}
	b.WriteString("}\n\n")

	if !plan.needsHash(pkg, msg.Name) {
		return
	}
	fmt.Fprintf(b, "/// %s%s is the 32-byte request_hash a (spec.rpc).requires_device approval\n", rustApprovalHashFnPrefix, snake)
	b.WriteString("/// for this request is created with, and the same value the generated server\n")
	b.WriteString("/// check recomputes from the live request. Both ends call this one function.\n")
	fmt.Fprintf(b, "pub fn %s%s(m: &%s) -> Vec<u8> {\n", rustApprovalHashFnPrefix, snake, typeName)
	b.WriteString("    use sha2::{Digest, Sha256};\n")
	b.WriteString("    let mut canonical = Vec::new();\n")
	fmt.Fprintf(b, "    %s%s(m, &mut canonical);\n", rustApprovalCanonicalFnPrefix, snake)
	b.WriteString("    Sha256::digest(&canonical).to_vec()\n")
	b.WriteString("}\n\n")
}

// rustApprovalScalarKind maps a field to the canonicalization kind that decides
// its wire type and its encoding. An enum is i32 in prost and a varint on the
// wire, exactly as protoreflect reports it.
func rustApprovalScalarKind(f *model.Field) string {
	if f.IsEnum {
		return "enum"
	}
	return f.Type
}

// rustApprovalIsCopyKind reports whether prost renders this field's value as a
// Copy scalar. It decides how a value is bound out of a Vec or a oneof: a Copy
// scalar is taken by value, a String/bytes/message by reference.
func rustApprovalIsCopyKind(f *model.Field) bool {
	if f.IsMessage {
		return false
	}
	switch rustApprovalScalarKind(f) {
	case "string", "bytes":
		return false
	}
	return true
}

// rustApprovalWriteValue renders the statement that writes ONE value of kind at
// field number num. expr must already have the right Rust shape for the kind:
// by value for a Copy scalar, a reference for a string or bytes.
func rustApprovalWriteValue(num int, kind, expr string) string {
	switch kind {
	case "string":
		return fmt.Sprintf("approval_put_bytes(out, %d, %s.as_bytes());", num, expr)
	case "bytes":
		// `&x[..]` rather than `&x`, so the one arm covers a Vec<u8> field, a
		// &Vec<u8> bound out of a repeated field, and a prost Bytes alike.
		return fmt.Sprintf("approval_put_bytes(out, %d, &%s[..]);", num, expr)
	case "bool":
		return fmt.Sprintf("approval_put_tag(out, %d, 0);\napproval_put_varint(out, if %s { 1 } else { 0 });", num, expr)
	case "enum", "int32":
		// Go writes uint64(int64(v)) for a 32-bit signed varint, which
		// sign-extends. A plain `as u64` would not.
		return fmt.Sprintf("approval_put_tag(out, %d, 0);\napproval_put_varint(out, (%s as i64) as u64);", num, expr)
	case "int64":
		return fmt.Sprintf("approval_put_tag(out, %d, 0);\napproval_put_varint(out, %s as u64);", num, expr)
	case "uint32", "uint64":
		return fmt.Sprintf("approval_put_tag(out, %d, 0);\napproval_put_varint(out, %s as u64);", num, expr)
	case "sint32", "sint64":
		return fmt.Sprintf("approval_put_tag(out, %d, 0);\napproval_put_varint(out, approval_zigzag(%s as i64));", num, expr)
	case "fixed32":
		return fmt.Sprintf("approval_put_fixed32(out, %d, %s);", num, expr)
	case "sfixed32":
		return fmt.Sprintf("approval_put_fixed32(out, %d, %s as u32);", num, expr)
	case "float":
		return fmt.Sprintf("approval_put_fixed32(out, %d, (%s).to_bits());", num, expr)
	case "fixed64":
		return fmt.Sprintf("approval_put_fixed64(out, %d, %s);", num, expr)
	case "sfixed64":
		return fmt.Sprintf("approval_put_fixed64(out, %d, %s as u64);", num, expr)
	case "double":
		return fmt.Sprintf("approval_put_fixed64(out, %d, (%s).to_bits());", num, expr)
	}
	// Unreachable for a planned tree: an unresolvable kind keeps the field out
	// of the plan, and the service wrapper refuses the build.
	return ""
}

// rustApprovalPresence renders the "is this field populated" test for a
// singular field with IMPLICIT presence — proto3's "not the zero value", which
// is what protoreflect's Range visits and therefore what Go and Java hash.
func rustApprovalPresence(kind, access string) string {
	switch kind {
	case "string", "bytes":
		return "!" + access + ".is_empty()"
	case "bool":
		return access
	case "float", "double":
		return access + " != 0.0"
	}
	return access + " != 0"
}

// rustApprovalIndent re-indents a rendered statement block to depth.
func rustApprovalIndent(s, pad string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = pad + l
		}
	}
	return strings.Join(lines, "\n")
}

// rustApprovalSubMessageBody renders the "canonicalize into sub, then write it
// length-delimited" block for a message-typed value bound to `v`.
func rustApprovalSubMessageBody(num int, fn, inline, pad string) string {
	var b strings.Builder
	b.WriteString(pad + "let mut sub = Vec::new();\n")
	if inline != "" {
		b.WriteString(rustApprovalIndent(inline, pad) + "\n")
	} else {
		b.WriteString(pad + fn + "(v, &mut sub);\n")
	}
	fmt.Fprintf(&b, "%sapproval_put_bytes(out, %d, &sub);\n", pad, num)
	return b.String()
}

// rustApprovalWellKnownInline renders the canonicalization of a google.protobuf
// message this generator writes inline (see rustApprovalWellKnown), over a
// value bound to `v` and a buffer named `sub`.
func rustApprovalWellKnownInline(fullType string) string {
	switch fullType {
	case "google.protobuf.Timestamp", "google.protobuf.Duration":
		return `if v.seconds != 0 {
    approval_put_tag(&mut sub, 1, 0);
    approval_put_varint(&mut sub, v.seconds as u64);
}
if v.nanos != 0 {
    approval_put_tag(&mut sub, 2, 0);
    approval_put_varint(&mut sub, (v.nanos as i64) as u64);
}`
	}
	return ""
}

// rustApprovalMessageFn resolves the canonicalization function a message-typed
// field recurses through. Cross-package calls carry the crate path the types
// crate already uses for cross-package helpers (see writeSerialization).
// Returns "" when the type is not in the plan — the wrapper refuses such a
// build, so an emitted call would never be reached.
func rustApprovalMessageFn(f *model.Field, currentPkg string, protoByPkg map[string]model.ProtoFile, plan *rustApprovalPlan) string {
	sub := f.TypePackage
	if sub == "" {
		sub = currentPkg
	}
	if !plan.needsCanonical(sub, f.Type) {
		return ""
	}
	fn := rustApprovalCanonicalFnPrefix + strutil.ToSnakeCase(toProstTypeName(f.Type))
	if f.TypePackage == "" || f.TypePackage == currentPkg {
		return fn
	}
	pf, ok := protoByPkg[f.TypePackage]
	if !ok {
		return ""
	}
	parentDir := filepath.Dir(pf.RelPath)
	return fmt.Sprintf("crate::%s::%s::%s", strings.ReplaceAll(parentDir, "/", "::"), f.TypePackage, fn)
}

func writeApprovalCanonicalField(b *strings.Builder, f *model.Field, msg model.Message, pkg string, protoByPkg map[string]model.ProtoFile, plan *rustApprovalPlan) {
	// A map keeps the whole request out of the plan, so reaching one here is a
	// generator bug rather than a build the wrapper let through.
	if f.IsMap {
		return
	}
	name := escapeRustKeyword(strutil.ToSnakeCase(f.Name))
	num := f.Number
	kind := rustApprovalScalarKind(f)

	if f.IsMessage {
		fullType := f.TypePackage
		if fullType == "" {
			fullType = pkg
		}
		fullType += "." + f.Type
		inline := ""
		fn := ""
		if rustApprovalWellKnown[fullType] {
			inline = rustApprovalWellKnownInline(fullType)
		} else {
			fn = rustApprovalMessageFn(f, pkg, protoByPkg, plan)
			if fn == "" {
				return
			}
		}
		switch {
		case f.IsOneOf:
			enumPath, variant := rustApprovalOneofPath(msg, f)
			fmt.Fprintf(b, "    if let Some(%s::%s(v)) = m.%s.as_ref() {\n", enumPath, variant, escapeRustKeyword(strutil.ToSnakeCase(f.OneOfName)))
			b.WriteString(rustApprovalSubMessageBody(num, fn, inline, "        "))
			b.WriteString("    }\n")
		case f.IsRepeated:
			fmt.Fprintf(b, "    for v in m.%s.iter() {\n", name)
			b.WriteString(rustApprovalSubMessageBody(num, fn, inline, "        "))
			b.WriteString("    }\n")
		default:
			fmt.Fprintf(b, "    if let Some(v) = m.%s.as_ref() {\n", name)
			b.WriteString(rustApprovalSubMessageBody(num, fn, inline, "        "))
			b.WriteString("    }\n")
		}
		return
	}

	switch {
	case f.IsOneOf:
		// A oneof member has EXPLICIT presence: the active variant is written
		// even when its value is the zero value, which is what protoreflect
		// reports and therefore what Go and Java hash.
		enumPath, variant := rustApprovalOneofPath(msg, f)
		expr := "v"
		if rustApprovalIsCopyKind(f) {
			expr = "(*v)"
		}
		fmt.Fprintf(b, "    if let Some(%s::%s(v)) = m.%s.as_ref() {\n", enumPath, variant, escapeRustKeyword(strutil.ToSnakeCase(f.OneOfName)))
		b.WriteString(rustApprovalIndent(rustApprovalWriteValue(num, kind, expr), "        ") + "\n")
		b.WriteString("    }\n")
	case f.IsRepeated:
		// Never the packed encoding: one tag+value per element, in the
		// request's given order, because repeated order is meaningful data and
		// packing is a runtime's choice.
		iter := fmt.Sprintf("m.%s.iter()", name)
		if rustApprovalIsCopyKind(f) {
			iter += ".copied()"
		}
		fmt.Fprintf(b, "    for v in %s {\n", iter)
		b.WriteString(rustApprovalIndent(rustApprovalWriteValue(num, kind, "v"), "        ") + "\n")
		b.WriteString("    }\n")
	case f.IsOptional:
		// EXPLICIT presence: a set-to-zero optional is populated.
		access := fmt.Sprintf("m.%s", name)
		if !rustApprovalIsCopyKind(f) {
			access += ".as_ref()"
		}
		fmt.Fprintf(b, "    if let Some(v) = %s {\n", access)
		b.WriteString(rustApprovalIndent(rustApprovalWriteValue(num, kind, "v"), "        ") + "\n")
		b.WriteString("    }\n")
	default:
		access := fmt.Sprintf("m.%s", name)
		fmt.Fprintf(b, "    if %s {\n", rustApprovalPresence(kind, access))
		b.WriteString(rustApprovalIndent(rustApprovalWriteValue(num, kind, access), "        ") + "\n")
		b.WriteString("    }\n")
	}
}

// rustApprovalOneofPath returns the prost oneof enum path and the member's
// variant name, spelled exactly as the sanitize emitter spells them
// (generateRustSanitizeField) so the two cannot disagree about prost's output.
func rustApprovalOneofPath(msg model.Message, f *model.Field) (string, string) {
	enumPath := strutil.ToSnakeCase(msg.Name) + "::" + strutil.ToPascalCase(f.OneOfName)
	return enumPath, strutil.ToPascalCase(f.Name)
}

// ---- the generated server gate ----

// rustRequiresDeviceMapField names the map field reachable from a
// requires_device rpc's request message, as "Message.field", or "" when the
// request contains none. The walk recurses because the canonicalization
// recurses: a map nested three messages down is the same cross-language
// iteration-order nondeterminism as one at the top level. Same walk Go and Java
// make, over Rust's own message index.
func rustRequiresDeviceMapField(fullType string, san *rustEgressSanitizer, servicePackage string) string {
	hit, _ := rustRequiresDeviceWalk(fullType, san, servicePackage)
	return hit
}

// rustRequiresDeviceUnhashableType names a message type reachable from a
// requires_device rpc's request that this generator cannot canonicalize,
// or "" when every reachable type is resolvable.
//
// This refusal has no Go or Java counterpart, and the asymmetry is real rather
// than an oversight: they canonicalize by runtime reflection, which reaches any
// message whether or not the generator can see it. Rust GENERATES the walk, so
// a type the generator cannot see is a field Rust would silently omit from the
// hash while Go and Java include it — a cross-language drift kernel could never
// report, because it compares finished hashes. Refusing to build is the only
// honest answer.
func rustRequiresDeviceUnhashableType(fullType string, san *rustEgressSanitizer, servicePackage string) string {
	_, unhashable := rustRequiresDeviceWalk(fullType, san, servicePackage)
	return unhashable
}

// rustRequiresDeviceWalk closes over the request tree once, reporting the first
// map field and the first unresolvable message type it reaches.
func rustRequiresDeviceWalk(fullType string, san *rustEgressSanitizer, servicePackage string) (mapField, unhashable string) {
	if san == nil || fullType == "" || fullType == "google.protobuf.Empty" {
		return "", ""
	}
	seen := make(map[string]bool)
	var walk func(t string)
	walk = func(t string) {
		if mapField != "" || unhashable != "" {
			return
		}
		if rustApprovalWellKnown[t] {
			return
		}
		pkg, name := splitRustEgressFQN(t, servicePackage)
		key := rustApprovalMsgKey(pkg, name)
		if seen[key] {
			return
		}
		seen[key] = true
		msg, ok := san.msgs[key]
		if !ok {
			unhashable = t
			return
		}
		for i := range msg.Fields {
			if msg.Fields[i].IsMap {
				mapField = name + "." + msg.Fields[i].Name
				return
			}
		}
		for i := range msg.Fields {
			f := &msg.Fields[i]
			if !f.IsMessage {
				continue
			}
			sub := f.TypePackage
			if sub == "" {
				sub = pkg
			}
			walk(sub + "." + f.Type)
			if mapField != "" || unhashable != "" {
				return
			}
		}
	}
	walk(fullType)
	return mapField, unhashable
}

// rustApprovalHashFnRef resolves the generated request-hash function for a
// guarded rpc's request type, fully qualified through the types crate. It is
// the SAME function the caller-side helper calls, so the proposed approval and
// the served call cannot disagree about what was approved.
// It is ALWAYS fully qualified, including for the service's own package. A bare
// name would lean on the emitting file glob-importing the types module — true
// of the server wrapper, false of the generated gRPC client, which is how the
// caller-side helper first failed to compile.
func rustApprovalHashFnRef(fullInputType, servicePkg, serviceParentDir string, crossPkgTypes map[string]string) string {
	fn := rustApprovalHashFnPrefix + strutil.ToSnakeCase(toProstTypeName(simpleTypeName(fullInputType)))
	parts := strings.Split(fullInputType, ".")
	if len(parts) <= 1 {
		return rustApprovalTypesModPath(servicePkg, serviceParentDir) + "::" + fn
	}
	pkg := strings.Join(parts[:len(parts)-1], ".")
	if pkg == servicePkg {
		return rustApprovalTypesModPath(pkg, serviceParentDir) + "::" + fn
	}
	if dir, ok := crossPkgTypes[pkg]; ok {
		return rustApprovalTypesModPath(pkg, dir) + "::" + fn
	}
	return fmt.Sprintf("crate::types::%s::%s", strings.ReplaceAll(pkg, ".", "::"), fn)
}

// rustApprovalTypesModPath is the types-crate module a package's helpers live
// in, spelled the way resolveOneRustTypeQualified spells the types themselves.
func rustApprovalTypesModPath(pkg, dir string) string {
	if dir == "" {
		return "crate::types::" + pkg
	}
	dirPath := strings.ReplaceAll(strings.ReplaceAll(dir, "-", "_"), string(filepath.Separator), "::")
	return "crate::types::" + dirPath + "::" + pkg
}

// rustRequiresDeviceGate renders the (spec.rpc).requires_device check for a
// unary method, or "" when the method declares none. It sits between caller
// extraction and the handler call — the same seam the auth token is extracted
// at — because it reads two more metadata keys the same way and needs the
// caller the auth check just verified.
//
// It runs before `request.into_inner()` and hashes through `request.get_ref()`,
// so one splice point covers every unary shape rather than one before the
// request is consumed and another after.
//
// The approval id and the device assertion are metadata, so they are physically
// outside the request message and therefore outside the hashed bytes, which is
// kernel's own constraint.
func rustRequiresDeviceGate(method *parser.MethodInfo, svc *parser.ServiceInfo, servicePackage, serviceParentDir string, crossPkgTypes map[string]string) string {
	if method == nil || !method.HasDeviceGate() {
		return ""
	}
	// An Empty request canonicalizes to the empty byte string. The approval is
	// still bound to the rpc and the caller; there is simply no request content
	// for it to cover.
	hash := "approval::hash_canonical(&[])"
	if method.FullInputType != "google.protobuf.Empty" {
		hash = rustApprovalHashFnRef(method.FullInputType, servicePackage, serviceParentDir, crossPkgTypes) + "(request.get_ref())"
	}
	body := fmt.Sprintf(`
        // (spec.rpc).requires_device: the device-only approval gate. Any
        // failure refuses the rpc — the handler below is never reached.
        let __approval = approval::from_metadata(request.metadata())?;
        let __request_hash = %s;
        self.broker_approval(%q, &__approval, __request_hash, caller.as_ref()).await?;`, hash, rustQualifiedMethodAuthKey(servicePackage, svc.Name, method.Name))
	when := method.RequiresDeviceWhen
	if when == nil {
		return body
	}
	// (spec.rpc).requires_device_when: the SAME body, reached only when the
	// named field holds the named value. Every other request is served exactly
	// as it is without the annotation.
	return fmt.Sprintf(`
        // (spec.rpc).requires_device_when: the approval gate below runs only
        // when %s == %s.
        if %s {%s
        }`, when.Field, when.Equals, rustRequiresDeviceWhenCondition(when),
		strings.ReplaceAll(body, "\n        ", "\n            "))
}

// rustRequiresDeviceWhenCondition is the Rust expression the gated field is
// tested with. prost lowers an enum field to a plain i32, so an enum is compared
// by NUMBER — the value's name was resolved through the enum descriptor at parse
// time, so this needs no generated variant's spelling.
func rustRequiresDeviceWhenCondition(when *parser.RequiresDeviceWhenDescriptor) string {
	field := escapeRustKeyword(strutil.ToSnakeCase(when.Field))
	if when.IsEnum {
		return fmt.Sprintf("request.get_ref().%s == %d", field, when.EnumNumber)
	}
	return fmt.Sprintf("request.get_ref().%s == %q", field, when.Equals)
}

// ---- the ServiceManager seam ----

// rustApprovalBrokerEntry names the approval-broker rpc the generated
// (spec.rpc).requires_device check calls, and the generated S2S client seam it
// goes out over. Everything here is derived from the authoritative
// (spec.token_minter) declaration — no service, method or package is hardcoded.
//
// The two accessors are computed from the SAME functions the client aggregator
// emits with (resolveMeshTarget, grpcClientNames), because a second spelling
// here would compile in the generator and fail in rustc.
type rustApprovalBrokerEntry struct {
	Package        string // "kernel"
	ServiceName    string // "Mint"
	MethodName     string // "BrokerPendingApproval"
	TargetAccessor string // sm.s2s().<this>()
	ClientAccessor string // .<this>()
	MethodFn       string // the generated client's snake-cased method
	TypesModPath   string // "crate::types::services::kernel"
}

// rustRequiresDeviceServices reports which hosted services carry the gate,
// keyed "<package>.<Service>". Resolved from the descriptors rather than from a
// field on the shared ServiceInfo, so this stays a Rust-local concern.
func rustRequiresDeviceServices(flatProtoDir string) (map[string]bool, error) {
	dp, err := parser.NewDescriptorParser(flatProtoDir)
	if err != nil {
		return nil, fmt.Errorf("requires_device service scan: parse %q: %w", flatProtoDir, err)
	}
	guarded := make(map[string]bool)
	for _, fd := range dp.GetAllFiles() {
		if fd == nil {
			continue
		}
		for _, svc := range fd.Services {
			for _, m := range svc.Methods {
				if m.HasDeviceGate() {
					guarded[fd.Package+"."+svc.Name] = true
				}
			}
		}
	}
	return guarded, nil
}

// buildRustApprovalBrokerEntry resolves the broker seam for a build that hosts
// at least one guarded rpc. clientServices is the resolved S2S client set, so
// the Mint client auto-injected by DeriveApprovalBrokerClient is already in it.
func buildRustApprovalBrokerEntry(flatProtoDir string, clientServices []ServiceInfo) (rustApprovalBrokerEntry, error) {
	broker, err := parser.DiscoverApprovalBroker(flatProtoDir)
	if err != nil {
		return rustApprovalBrokerEntry{}, err
	}
	if broker == nil {
		return rustApprovalBrokerEntry{}, fmt.Errorf("service manager data: a hosted rpc declares (spec.rpc).requires_device, but no authoritative (spec.token_minter) service in this proto set hosts an approval broker; the generated device-approval check has nothing to call")
	}

	// The same global dedup the aggregator computes, so grpcClientNames answers
	// with the field name the aggregator actually emitted.
	nameCount := make(map[string]int, len(clientServices))
	for _, svc := range clientServices {
		nameCount[svc.ServiceName]++
	}
	for _, svc := range clientServices {
		if svc.PackageName != broker.Package || svc.ServiceName != broker.ServiceName {
			continue
		}
		clientField, _ := grpcClientNames(svc, nameCount)
		return rustApprovalBrokerEntry{
			Package:        broker.Package,
			ServiceName:    broker.ServiceName,
			MethodName:     broker.MethodName,
			TargetAccessor: strutil.ToSnakeCase(strings.ReplaceAll(resolveMeshTarget(svc), "-", "_")),
			ClientAccessor: clientField,
			MethodFn:       prostSnakeName(broker.MethodName),
			// broker.Dir is the proto's relative dir ("services/kernel"); the
			// types module sits under its parent group, as everywhere else.
			TypesModPath: fmt.Sprintf("crate::types::%s::%s",
				strings.ReplaceAll(filepath.ToSlash(filepath.Dir(broker.Dir)), "/", "::"), broker.Package),
		}, nil
	}

	// No client to the broker's package. The one way to reach here is the
	// broker's OWN package declaring a guarded rpc: DeriveApprovalBrokerClient
	// returns nil for the minter itself, because no package gets a generated
	// client to itself. Refuse with the reason rather than emit a call to a
	// client that will not exist — the same refusal Java makes.
	return rustApprovalBrokerEntry{}, fmt.Errorf("%s hosts the approval broker (%s.%s/%s) AND declares a (spec.rpc).requires_device rpc; the generated check calls the broker over a generated S2S client, and a package gets no generated client to itself, so this cannot be wired — move the guarded rpc to another service",
		broker.Package, broker.Package, broker.ServiceName, broker.MethodName)
}

// ---- the caller-side helpers ----

// rustRequiresDeviceClientHelpers renders the caller-side half of
// (spec.rpc).requires_device for one unary rpc, or "" when the rpc declares
// none. Two seams, because the caller owns two distinct steps and tacplus owns
// neither of them wholly:
//
//   - <method>_approval_hash gives the caller the request hash it must propose
//     the pending approval with. It is the SAME generated canonicalization the
//     server-side check recomputes from the live request, so the two ends
//     cannot drift; every product hand-rolling this is exactly what it
//     prevents.
//   - <method>_with_approval attaches the approval id and the device assertion
//     as CALL METADATA and makes the ordinary call. Metadata is outside the
//     request message, which is what keeps both values outside the hashed
//     bytes.
//
// Proposing the approval itself (kernel PersonalIdentity.CreatePendingApproval)
// and obtaining the device assertion are the caller's own business — not
// generated here, and deliberately so: an approval is proposed by a person, not
// by a service's client stub.
func rustRequiresDeviceClientHelpers(svc *parser.ServiceInfo, method *parser.MethodInfo, inputType, outputType, servicePkg, serviceParentDir string, crossPkgTypes map[string]string) string {
	if method == nil || !method.HasDeviceGate() {
		return ""
	}
	methodSnake := prostSnakeName(method.Name)
	// An Empty-request rpc's generated client method takes no message, so the
	// hash seam takes none either: the canonicalization of an Empty is the
	// empty byte string, and the caller's approval is still proposed with it.
	hashArgs, hashBody, callArg := "", "approval::hash_canonical(&[])", "()"
	if method.FullInputType != "google.protobuf.Empty" {
		hashArgs = "request: &" + inputType
		hashBody = rustApprovalHashFnRef(method.FullInputType, servicePkg, serviceParentDir, crossPkgTypes) + "(request)"
		callArg = "request"
	}
	withArgs := "        approval_id: &str,\n        device_assertion: &[u8],"
	if callArg == "request" {
		withArgs = "        request: " + inputType + ",\n" + withArgs
	}
	return fmt.Sprintf(`
    /// %[1]s_approval_hash is the request_hash a pending approval for this call
    /// must be created with. It comes from the same generated canonicalization
    /// the server-side (spec.rpc).requires_device check recomputes from the live
    /// request, so the proposed approval and the served call cannot disagree
    /// about what was approved.
    pub fn %[1]s_approval_hash(%[2]s) -> Vec<u8> {
        %[3]s
    }

    /// %[1]s_with_approval calls %[4]s.%[5]s carrying a device-only approval:
    /// approval_id names the pending approval the person proposed, and
    /// device_assertion is the serialized WebauthnAssertion of the approving
    /// tap. Both ride as call METADATA — never as request fields — which is what
    /// keeps them outside the hashed request.
    pub async fn %[1]s_with_approval(
        &self,
%[6]s
    ) -> Result<tonic::Response<%[7]s>, tonic::Status> {
        let mut request = tonic::Request::new(%[8]s);
        approval::attach_outgoing(&mut request, approval_id, device_assertion)?;
        self.%[1]s(request).await
    }
`, methodSnake, hashArgs, hashBody, svc.Name, method.Name, withArgs, outputType, callArg)
}
