// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// flock state (tacplus#206): fleet-wide convergent state declared in proto. A
// flock table is a (spec.service_state) root whose label includes FlockLabel.
// Each of its boundaries is a repeated entry field, and each entry message is
// merged field by field by the (spec.merge) rule of every field. This file is
// the one place that decides what a flock entry is and how each field merges:
// refuseMaps refuses a map in f or anywhere in its message type: a value
// stored whole (a LAST_WRITE message, a field of an entry replaced whole) is
// part of the digest copies compare, and protobuf runtimes encode a map in
// their own order, so it has no canonical encoding across languages.
func (ix *FlockIndex) refuseMaps(r *MessageResolver, pkg, where string, f model.Field) {
	seen := map[string]bool{}
	var walk func(pkg string, f model.Field) bool
	walk = func(pkg string, f model.Field) bool {
		if f.IsMap {
			return true
		}
		msg, ok := r.Resolve(pkg, f)
		if !ok {
			return false
		}
		npkg := f.TypePackage
		if npkg == "" {
			npkg = pkg
		}
		if seen[npkg+"\x00"+msg.Name] {
			return false
		}
		seen[npkg+"\x00"+msg.Name] = true
		for _, nf := range msg.Fields {
			if walk(npkg, nf) {
				return true
			}
		}
		return false
	}
	if walk(pkg, f) {
		ix.errf("%s: a value stored whole cannot hold a map: a map has no canonical encoding, and copies compare digests", where)
	}
}

// ValidateFlockAnnotations refuses what cannot merge, and FlockIndex gives each
// language's emitter (Go, Rust, Java) the same plan, so the three cannot drift.

// FlockLabel is the routing label of a flock state root. IsFlockRoot is the
// only predicate that reads it.
const FlockLabel = "flock"

// FlockPackage is the proto package of the flock sys types (sys/flock.proto).
const FlockPackage = "flock"

// FlockMetaFieldName / FlockMetaFieldNumber: the flock.Meta field tacplus adds
// to every flock entry message (proto.InjectFlockMetaFields). The number is
// above the redacted-indicator band (RedactIndicatorFieldBase + a field number
// below it), so the two injections can never collide.
const (
	FlockMetaFieldName   = "flock_meta"
	FlockMetaFieldNumber = 100000
)

// Flock timing constants every language emits, so their verdicts agree.
const (
	// FlockMaxSkewMs: a remote clock time more than this ahead of local wall
	// time never moves the local clock; the write is held until it is not.
	FlockMaxSkewMs = 60_000
	// FlockRebuildAfterMs: a copy absent longer than this (30 days) is wiped and
	// rebuilt from its peers, never merged.
	FlockRebuildAfterMs = 30 * 24 * 60 * 60 * 1000
	// FlockMaxMessageAgeMs: a copy drops replicated state older than this, so a
	// mark acknowledged by every copy, and purged once this long has passed
	// since the last acknowledgement, can never be brought back by a message
	// still in flight.
	FlockMaxMessageAgeMs = 10 * 60 * 1000
)

// Merge rule names, as descriptor.go records (spec.merge).
const (
	FlockRuleImmutable     = "immutable"
	FlockRuleMin           = "min"
	FlockRuleMax           = "max"
	FlockRuleLastWrite     = "last_write"
	FlockRuleSetAddWins    = "set_add_wins"
	FlockRuleSetRemoveWins = "set_remove_wins"
	// FlockRuleNested is no annotation: a singular message field with no rule,
	// merged field by field.
	FlockRuleNested = "nested"
)

// IsFlockRoot reports whether msg is a flock state root.
func IsFlockRoot(msg *model.Message) bool {
	if _, ok := msg.Annotations["service_state"]; !ok {
		return false
	}
	for _, l := range msg.Annotations["service_state_label"].StringSliceValue {
		if l == FlockLabel {
			return true
		}
	}
	return false
}

// IsFlockType reports whether f is the flock sys message named name.
func IsFlockType(f model.Field, name string) bool {
	return f.IsMessage && !f.IsMap && f.TypePackage == FlockPackage && f.Type == name
}

// IsFlockMetaField reports whether f is the injected flock.Meta field.
func IsFlockMetaField(f model.Field) bool {
	return f.Number == FlockMetaFieldNumber && f.Name == FlockMetaFieldName
}

// FlockScalarKind is how a scalar flock value is compared and encoded.
type FlockScalarKind int

const (
	FlockKindNone FlockScalarKind = iota
	FlockKindString
	FlockKindBytes
	FlockKindBool
	FlockKindEnum
	FlockKindSigned   // int32, int64, sint32, sint64, sfixed32, sfixed64
	FlockKindUnsigned // uint32, uint64, fixed32, fixed64
	FlockKindFloat    // float, double: LAST_WRITE only
)

// FlockKind classifies a non-map field's element type.
func FlockKind(f model.Field) FlockScalarKind {
	if f.IsMessage {
		return FlockKindNone
	}
	if f.IsEnum {
		return FlockKindEnum
	}
	switch f.Type {
	case "string":
		return FlockKindString
	case "bytes":
		return FlockKindBytes
	case "bool":
		return FlockKindBool
	case "int32", "int64", "sint32", "sint64", "sfixed32", "sfixed64":
		return FlockKindSigned
	case "uint32", "uint64", "fixed32", "fixed64":
		return FlockKindUnsigned
	case "float", "double":
		return FlockKindFloat
	}
	return FlockKindNone
}

// FlockRoot is one flock state root and its boundaries.
type FlockRoot struct {
	Package    string
	Message    *model.Message
	Boundaries []FlockBoundary
}

// FlockBoundary is one repeated entry field of a root.
type FlockBoundary struct {
	Field model.Field
	Entry *FlockEntry
}

// FlockEntry is the merge plan of one entry message.
type FlockEntry struct {
	Package string
	Message *model.Message
	// The system fields, by name.
	Key, Epoch, Mark, Created string
	// WholeEntry: a table-level LAST_WRITE; the newer store epoch replaces the
	// whole entry and Fields is empty.
	WholeEntry bool
	// Fields: every other field (the injected flock_meta excluded), in field
	// number order.
	Fields []FlockField
}

// FlockField is one merged field.
type FlockField struct {
	Field model.Field
	Rule  string
	// Path names the field from its entry ("display", "profile.nickname"): the
	// key of its stamp or op set in flock.Meta, and of its conflicts.
	Path string
	// Kind of a scalar field, or of a repeated scalar set's element.
	Kind FlockScalarKind
	// Nested: the plan of a FlockRuleNested field's message.
	Nested *FlockNested
	// Element: the plan of a SET_* field's elements.
	Element *FlockElement
	Unique  bool
	// HlcValue: an IMMUTABLE or LAST_WRITE flock.Hlc field, compared by clock order.
	HlcValue bool
}

// FlockNested is a singular message merged field by field.
type FlockNested struct {
	Package string
	Message *model.Message
	Fields  []FlockField
}

// FlockElement is the element of a SET_* field.
type FlockElement struct {
	// Scalar: a repeated scalar set; the value is its own identity.
	Scalar bool
	Kind   FlockScalarKind
	// Map: a map of messages; the map key is the identity, KeyKind its kind.
	Map     bool
	KeyKind FlockScalarKind
	// Message: the element message (Map or repeated message sets).
	Package string
	Message *model.Message
	// Identity: the (spec.identity) fields of a repeated message set's element,
	// in field number order.
	Identity []model.Field
	// Fields: the element's other fields (MIN, MAX, LAST_WRITE), in field
	// number order. Their LAST_WRITE clock is the add's dot.
	Fields []FlockField
}

// SetRemoveWins reports whether f is a SET_REMOVE_WINS field.
func (f FlockField) SetRemoveWins() bool { return f.Rule == FlockRuleSetRemoveWins }

// IsSet reports whether f is a SET_* field.
func (f FlockField) IsSet() bool {
	return f.Rule == FlockRuleSetAddWins || f.Rule == FlockRuleSetRemoveWins
}

// FlockIndex holds every flock root and entry plan of a generation run.
type FlockIndex struct {
	Roots   []FlockRoot
	entries map[string]*FlockEntry // pkg + "\x00" + name
	errs    []string
}

// NewFlockIndex plans every flock root in allFiles. It records, rather than
// returns, what it cannot plan; ValidateFlockAnnotations reports those.
func NewFlockIndex(allFiles []model.ProtoFile) *FlockIndex {
	r := NewMessageResolver(allFiles)
	ix := &FlockIndex{entries: map[string]*FlockEntry{}}
	files := append([]model.ProtoFile(nil), allFiles...)
	sort.SliceStable(files, func(i, j int) bool { return files[i].Package < files[j].Package })
	for fi := range files {
		pf := &files[fi]
		for mi := range pf.Messages {
			msg := &pf.Messages[mi]
			if !IsFlockRoot(msg) {
				continue
			}
			ix.planRoot(r, pf.Package, msg)
		}
	}
	return ix
}

// Entry returns the plan of the entry message pkg.name, if it is one.
func (ix *FlockIndex) Entry(pkg, name string) (*FlockEntry, bool) {
	e, ok := ix.entries[pkg+"\x00"+name]
	return e, ok
}

// EntriesIn returns the entry plans of package pkg, sorted by name.
func (ix *FlockIndex) EntriesIn(pkg string) []*FlockEntry {
	var out []*FlockEntry
	for _, e := range ix.entries {
		if e.Package == pkg {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Message.Name < out[j].Message.Name })
	return out
}

func (ix *FlockIndex) errf(format string, args ...any) {
	ix.errs = append(ix.errs, fmt.Sprintf(format, args...))
}

func (ix *FlockIndex) planRoot(r *MessageResolver, pkg string, root *model.Message) {
	fr := FlockRoot{Package: pkg, Message: root}
	for _, f := range root.Fields {
		where := fmt.Sprintf("%s.%s.%s", pkg, root.Name, f.Name)
		if !f.IsRepeated || f.IsMap || !f.IsMessage {
			ix.errf("%s: a flock root holds only boundaries, each a repeated entry message field", where)
			continue
		}
		// A presence test, like proto/state.go's: tacplus does not act on the
		// flag, the flock service does.
		if _, ok := f.Annotations["persistence"]; !ok {
			ix.errf("%s: a flock boundary needs (spec.persistence) = true", where)
		}
		if f.Annotations["assert_max_size"].IntValue <= 0 {
			ix.errf("%s: a flock boundary needs (spec.assert_max_size)", where)
		}
		whole := false
		switch rule, has := f.Annotations["merge"]; {
		case !has:
		case rule.StringValue == FlockRuleLastWrite:
			whole = true
		default:
			ix.errf("%s: a boundary takes no (spec.merge) rule but MERGE_RULE_LAST_WRITE (replace the whole entry); its entry's fields carry the rules", where)
		}
		msg, ok := r.Resolve(pkg, f)
		if !ok {
			ix.errf("%s: entry type %s not found", where, f.Type)
			continue
		}
		epkg := f.TypePackage
		if epkg == "" {
			epkg = pkg
		}
		e, seen := ix.entries[epkg+"\x00"+msg.Name]
		if !seen {
			e = ix.planEntry(r, epkg, msg, whole)
			ix.entries[epkg+"\x00"+msg.Name] = e
		} else if e.WholeEntry != whole {
			ix.errf("%s: entry %s is a boundary of two flock roots with different table rules", where, msg.Name)
		}
		fr.Boundaries = append(fr.Boundaries, FlockBoundary{Field: f, Entry: e})
	}
	ix.Roots = append(ix.Roots, fr)
}

func (ix *FlockIndex) planEntry(r *MessageResolver, pkg string, msg *model.Message, whole bool) *FlockEntry {
	e := &FlockEntry{Package: pkg, Message: msg, WholeEntry: whole}
	where := pkg + "." + msg.Name
	var keys, epochs, marks, created []string
	for _, f := range msg.Fields {
		if IsFlockMetaField(f) {
			continue
		}
		fw := where + "." + f.Name
		if f.IsOneOf || f.IsOptional {
			ix.errf("%s: a flock entry field cannot be in a oneof or optional", fw)
			continue
		}
		switch {
		case f.Annotations["store_key"].BoolValue:
			if f.Type != "string" || f.IsRepeated || f.IsMap {
				ix.errf("%s: the (spec.store_key) of a flock entry is a string", fw)
			}
			// The state service keys a collection by its (spec.indexed) field.
			if !f.Annotations["indexed"].BoolValue {
				ix.errf("%s: the (spec.store_key) of a flock entry is its collection key too: add (spec.indexed) = true", fw)
			}
			if _, has := f.Annotations["merge"]; has {
				ix.errf("%s: the store key takes no (spec.merge) rule: it never changes", fw)
			}
			keys = append(keys, f.Name)
			continue
		case f.Annotations["store_epoch"].BoolValue:
			if !IsFlockType(f, "Hlc") || f.IsRepeated {
				ix.errf("%s: the (spec.store_epoch) of a flock entry is a flock.Hlc, the hybrid clock of its last write", fw)
			}
			if _, has := f.Annotations["merge"]; has {
				ix.errf("%s: the store epoch takes no (spec.merge) rule: it merges by MAX", fw)
			}
			epochs = append(epochs, f.Name)
			continue
		case f.Annotations["deleted_flag"].BoolValue:
			if !IsFlockType(f, "DeletionMark") || f.IsRepeated {
				ix.errf("%s: the (spec.deleted_flag) of a flock entry is a flock.DeletionMark", fw)
			}
			if _, has := f.Annotations["merge"]; has {
				ix.errf("%s: the deletion mark takes no (spec.merge) rule: a mark wins every merge", fw)
			}
			marks = append(marks, f.Name)
			continue
		}
		if IsFlockType(f, "Hlc") && !f.IsRepeated && f.Annotations["merge"].StringValue == FlockRuleImmutable {
			created = append(created, f.Name)
			continue
		}
		if whole {
			if _, has := f.Annotations["merge"]; has {
				ix.errf("%s: the entry is replaced whole (its boundary is MERGE_RULE_LAST_WRITE); its fields take no (spec.merge) rule", fw)
			}
			ix.refuseMaps(r, pkg, fw, f)
			continue
		}
		if ff, ok := ix.planField(r, pkg, fw, f.Name, f, true); ok {
			e.Fields = append(e.Fields, ff)
		}
	}
	one := func(names []string, what string) string {
		if len(names) != 1 {
			ix.errf("%s: a flock entry declares exactly one %s, found %d (%s)", where, what, len(names), strings.Join(names, ", "))
			return ""
		}
		return names[0]
	}
	e.Key = one(keys, "(spec.store_key) string")
	e.Epoch = one(epochs, "(spec.store_epoch) flock.Hlc")
	e.Mark = one(marks, "(spec.deleted_flag) flock.DeletionMark")
	e.Created = one(created, "creation stamp (a flock.Hlc with (spec.merge) = MERGE_RULE_IMMUTABLE)")
	return e
}

// planField plans one field of an entry (top) or of a nested message.
func (ix *FlockIndex) planField(r *MessageResolver, pkg, where, path string, f model.Field, top bool) (FlockField, bool) {
	ff := FlockField{Field: f, Path: path, Kind: FlockKind(f), Unique: f.Annotations["unique"].BoolValue}
	ann, has := f.Annotations["merge"]
	if f.Annotations["identity"].BoolValue {
		ix.errf("%s: (spec.identity) belongs on a field of a set element's message", where)
	}
	if ff.Unique && !top {
		ix.errf("%s: (spec.unique) belongs on a set field of a flock entry, not of a nested message", where)
	}
	if !has {
		if f.IsMessage && !f.IsRepeated && !f.IsMap && !IsFlockType(f, "Hlc") {
			ff.Rule = FlockRuleNested
			msg, ok := r.Resolve(pkg, f)
			if !ok {
				ix.errf("%s: message type %s not found", where, f.Type)
				return ff, false
			}
			npkg := f.TypePackage
			if npkg == "" {
				npkg = pkg
			}
			ff.Nested = &FlockNested{Package: npkg, Message: msg}
			for _, nf := range msg.Fields {
				if IsFlockMetaField(nf) {
					continue
				}
				nw := where + "." + nf.Name
				if nf.IsOneOf || nf.IsOptional {
					ix.errf("%s: a flock field cannot be in a oneof or optional", nw)
					continue
				}
				if nff, ok := ix.planField(r, npkg, nw, path+"."+nf.Name, nf, false); ok {
					ff.Nested.Fields = append(ff.Nested.Fields, nff)
				}
			}
			if ff.Unique {
				ix.errf("%s: (spec.unique) belongs on a set field", where)
			}
			return ff, true
		}
		ix.errf("%s: a field of a flock entry needs a (spec.merge) rule", where)
		return ff, false
	}
	ff.Rule = ann.StringValue
	switch ff.Rule {
	case FlockRuleImmutable:
		ff.HlcValue = IsFlockType(f, "Hlc")
		if f.IsRepeated || f.IsMap || (f.IsMessage && !ff.HlcValue) {
			ix.errf("%s: MERGE_RULE_IMMUTABLE takes a singular scalar, enum, bytes or flock.Hlc field", where)
			return ff, false
		}
	case FlockRuleMin, FlockRuleMax:
		if f.IsRepeated || f.IsMap || (ff.Kind != FlockKindSigned && ff.Kind != FlockKindUnsigned && ff.Kind != FlockKindString) {
			ix.errf("%s: MERGE_RULE_MIN and MERGE_RULE_MAX take a singular integer or string field", where)
			return ff, false
		}
	case FlockRuleLastWrite:
		ff.HlcValue = IsFlockType(f, "Hlc") && !f.IsRepeated
		if f.IsMap {
			ix.errf("%s: MERGE_RULE_LAST_WRITE does not take a map; a map of messages is a SET_* field", where)
			return ff, false
		}
		ix.refuseMaps(r, pkg, where, f)
	case FlockRuleSetAddWins, FlockRuleSetRemoveWins:
		el, ok := ix.planElement(r, pkg, where, f)
		if !ok {
			return ff, false
		}
		ff.Element = el
	default:
		ix.errf("%s: (spec.merge) names no rule (%q)", where, ff.Rule)
		return ff, false
	}
	if ff.Unique && !ff.IsSet() {
		ix.errf("%s: (spec.unique) belongs on a SET_* field", where)
	}
	return ff, true
}

func (ix *FlockIndex) planElement(r *MessageResolver, pkg, where string, f model.Field) (*FlockElement, bool) {
	el := &FlockElement{}
	switch {
	case f.IsMap:
		if !f.MapValueIsMessage {
			ix.errf("%s: a SET_* map holds messages; a map of scalars has no flock rule", where)
			return nil, false
		}
		el.Map = true
		el.KeyKind = FlockKind(model.Field{Type: f.MapKeyType})
		if el.KeyKind != FlockKindString && el.KeyKind != FlockKindSigned && el.KeyKind != FlockKindUnsigned {
			ix.errf("%s: a SET_* map is keyed by a string or an integer", where)
			return nil, false
		}
		vpkg := f.MapValueTypePackage
		if vpkg == "" {
			vpkg = pkg
		}
		msg, ok := r.Resolve(vpkg, model.Field{IsMessage: true, Type: f.MapValueType})
		if !ok {
			ix.errf("%s: map value type %s not found", where, f.MapValueType)
			return nil, false
		}
		el.Package, el.Message = vpkg, msg
	case !f.IsRepeated:
		ix.errf("%s: a SET_* rule takes a repeated field or a map of messages", where)
		return nil, false
	case !f.IsMessage:
		el.Scalar = true
		el.Kind = FlockKind(f)
		if el.Kind == FlockKindFloat || el.Kind == FlockKindNone {
			ix.errf("%s: a SET_* field of scalars holds strings, bytes, integers, bools or enums", where)
			return nil, false
		}
		return el, true
	default:
		msg, ok := r.Resolve(pkg, f)
		if !ok {
			ix.errf("%s: element type %s not found", where, f.Type)
			return nil, false
		}
		el.Package = f.TypePackage
		if el.Package == "" {
			el.Package = pkg
		}
		el.Message = msg
	}
	for _, ef := range el.Message.Fields {
		if IsFlockMetaField(ef) {
			continue
		}
		ew := fmt.Sprintf("%s(%s).%s", where, el.Message.Name, ef.Name)
		if ef.IsOneOf || ef.IsOptional || ef.IsMap || ef.IsRepeated || (ef.IsMessage && !IsFlockType(ef, "Hlc")) {
			ix.errf("%s: a set element's fields are singular scalars, enums, bytes or flock.Hlc", ew)
			continue
		}
		if ef.Annotations["identity"].BoolValue {
			if el.Map {
				ix.errf("%s: a map element's identity is its key; (spec.identity) does not apply", ew)
				continue
			}
			if _, has := ef.Annotations["merge"]; has {
				ix.errf("%s: an identity field takes no (spec.merge) rule: it never changes", ew)
			}
			k := FlockKind(ef)
			if k == FlockKindFloat || k == FlockKindNone {
				ix.errf("%s: an identity field is a string, bytes, integer, bool or enum", ew)
				continue
			}
			el.Identity = append(el.Identity, ef)
			continue
		}
		ann, has := ef.Annotations["merge"]
		ff := FlockField{Field: ef, Path: ef.Name, Kind: FlockKind(ef), Rule: ann.StringValue, HlcValue: IsFlockType(ef, "Hlc")}
		switch {
		case !has:
			ix.errf("%s: a set element's field needs (spec.identity) or a (spec.merge) rule", ew)
			continue
		case ff.Rule == FlockRuleLastWrite:
		case (ff.Rule == FlockRuleMin || ff.Rule == FlockRuleMax) && (ff.Kind == FlockKindSigned || ff.Kind == FlockKindUnsigned || ff.Kind == FlockKindString):
		default:
			ix.errf("%s: a set element's field merges by MERGE_RULE_MIN, MERGE_RULE_MAX (integers, strings) or MERGE_RULE_LAST_WRITE", ew)
			continue
		}
		el.Fields = append(el.Fields, ff)
	}
	if !el.Map && len(el.Identity) == 0 {
		ix.errf("%s: element %s needs at least one (spec.identity) field", where, el.Message.Name)
		return nil, false
	}
	return el, true
}

// ValidateFlockAnnotations refuses a flock root that cannot merge: a field
// with no rule, a rule on a field it cannot merge, a missing or malformed
// system field. It also refuses (spec.merge), (spec.identity) and
// (spec.unique) anywhere a flock root does not reach, where nothing would
// read them.
func ValidateFlockAnnotations(allFiles []model.ProtoFile) error {
	ix := NewFlockIndex(allFiles)
	errs := append([]string(nil), ix.errs...)
	reached := ix.reachedMessages()
	for _, pf := range allFiles {
		for _, msg := range pf.Messages {
			if reached[pf.Package+"\x00"+msg.Name] || IsFlockRoot(&msg) {
				continue
			}
			for _, f := range msg.Fields {
				if (f.Annotations["store_epoch"].BoolValue && IsFlockType(f, "Hlc")) ||
					(f.Annotations["deleted_flag"].BoolValue && IsFlockType(f, "DeletionMark")) {
					errs = append(errs, fmt.Sprintf("%s.%s.%s: a flock.Hlc store epoch or flock.DeletionMark deletion mark belongs to the entries of a flock state root",
						pf.Package, msg.Name, f.Name))
				}
				for _, a := range []string{"merge", "identity", "unique"} {
					if _, has := f.Annotations[a]; has {
						errs = append(errs, fmt.Sprintf("%s.%s.%s: (spec.%s) applies only to the entries of a flock state root ((spec.service_state) labelled %q)",
							pf.Package, msg.Name, f.Name, a, FlockLabel))
					}
				}
			}
		}
	}
	if len(errs) == 0 {
		return nil
	}
	sort.Strings(errs)
	errs = dedupSorted(errs)
	return fmt.Errorf("flock annotation errors:\n  %s", strings.Join(errs, "\n  "))
}

// reachedMessages is every message a flock root reaches: its entries, their
// nested messages and set elements.
func (ix *FlockIndex) reachedMessages() map[string]bool {
	out := map[string]bool{}
	var walk func(pkg string, fields []FlockField)
	walk = func(pkg string, fields []FlockField) {
		for _, f := range fields {
			if f.Nested != nil {
				out[f.Nested.Package+"\x00"+f.Nested.Message.Name] = true
				walk(f.Nested.Package, f.Nested.Fields)
			}
			if f.Element != nil && f.Element.Message != nil {
				out[f.Element.Package+"\x00"+f.Element.Message.Name] = true
			}
		}
	}
	for _, e := range ix.entries {
		out[e.Package+"\x00"+e.Message.Name] = true
		walk(e.Package, e.Fields)
	}
	return out
}

// FlockSetFields returns every SET_* field of e, nested ones included, in
// path order: the op sets of flock.Meta.
func (e *FlockEntry) FlockSetFields() []FlockField {
	var out []FlockField
	var walk func([]FlockField)
	walk = func(fs []FlockField) {
		for _, f := range fs {
			if f.IsSet() {
				out = append(out, f)
			}
			if f.Nested != nil {
				walk(f.Nested.Fields)
			}
		}
	}
	walk(e.Fields)
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// UniqueFields returns e's (spec.unique) set fields, in path order.
func (e *FlockEntry) UniqueFields() []FlockField {
	var out []FlockField
	for _, f := range e.Fields {
		if f.Unique {
			out = append(out, f)
		}
	}
	return out
}

// FlockRules returns every merge decision of the flock entries in allFiles,
// by field path, for registry.yaml to pin (a rule never changes once
// released): "<pkg>.<Entry>" is the table rule ("fields" or "last_write"),
// "<pkg>.<Entry>.<path>" a field's rule, "<pkg>.<Entry>.<path>[].<field>" an
// element field's rule or "identity".
func FlockRules(allFiles []model.ProtoFile) map[string]string {
	ix := NewFlockIndex(allFiles)
	out := map[string]string{}
	var walk func(prefix string, fields []FlockField)
	walk = func(prefix string, fields []FlockField) {
		for _, f := range fields {
			path := prefix + "." + f.Field.Name
			out[path] = f.Rule
			if f.Nested != nil {
				walk(path, f.Nested.Fields)
			}
			if el := f.Element; el != nil {
				for _, id := range el.Identity {
					out[path+"[]."+id.Name] = "identity"
				}
				for _, ef := range el.Fields {
					out[path+"[]."+ef.Field.Name] = ef.Rule
				}
			}
		}
	}
	for _, e := range ix.entries {
		name := e.Package + "." + e.Message.Name
		out[name] = "fields"
		if e.WholeEntry {
			out[name] = FlockRuleLastWrite
		}
		walk(name, e.Fields)
	}
	return out
}
