// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// This file is the language-neutral half of redaction for any type (tacplus#274):
// (spec.sensitive) on a field, (spec.sensitive_message) on a message and
// (spec.not_sensitive) as the explicit opt-out. It validates the annotations,
// runs the sensitive-name gate, and plans the deterministic Redact<T> every
// full-service language emits from the same plan, so the three cannot drift.

// RedactMarker replaces a set sensitive string; its bytes replace a set
// sensitive bytes value (Jody, 2026-11-03: "replace a redacted string with the
// word REDACTED"). An empty or unset value stays empty.
const RedactMarker = "REDACTED"

// DefaultMinEntropyBits is the entropy floor of a required sensitive field that
// declares no min_entropy_bits of its own.
const DefaultMinEntropyBits = 128

// SensitivePlaceholders are the values the generated validators always refuse
// for a checked sensitive field (ASCII case-insensitive), besides a value of one
// character (byte) repeated.
var SensitivePlaceholders = []string{"redacted", "changeme"}

// sensitiveNameWords are the words that make a field LOOK sensitive. A field's
// name is split on '_'; a word that is one of these, or ends with one (apikey,
// authtoken), or is one of these plus a trailing "s", trips the gate.
var sensitiveNameWords = []string{"secret", "password", "passwd", "token", "key", "private", "credential"}

// privateKeyEncTypes are the (spec.assert_enc_type) values that admit private
// key material: a bytes or string field carrying one looks sensitive whatever
// its name.
var privateKeyEncTypes = map[string]bool{
	"ENC_TYPE_KEY_PRIVATE_RSA":     true,
	"ENC_TYPE_KEY_ANY_RSA":         true,
	"ENC_TYPE_KEY_PRIVATE_EC":      true,
	"ENC_TYPE_KEY_ANY_EC":          true,
	"ENC_TYPE_KEY_PRIVATE_ED25519": true,
	"ENC_TYPE_KEY_ANY_ED25519":     true,
	"ENC_TYPE_KEY_PRIVATE_ANY":     true,
	"ENC_TYPE_KEY_ANY":             true,
}

// LooksSensitiveName reports whether a field name trips the sensitive-name gate.
func LooksSensitiveName(name string) bool {
	for _, w := range strings.Split(strings.ToLower(name), "_") {
		w = strings.TrimSuffix(w, "s")
		for _, s := range sensitiveNameWords {
			if w == s || strings.HasSuffix(w, s) {
				return true
			}
		}
	}
	return false
}

// SensitiveLeafKind classifies what Redact<T> can do with a field's value.
type SensitiveLeafKind int

const (
	// LeafNone: not a string, bytes or message value (int, bool, enum, ...).
	LeafNone SensitiveLeafKind = iota
	// LeafString: a string, a repeated string, or a map with string values.
	LeafString
	// LeafBytes: a bytes, a repeated bytes, or a map with bytes values.
	LeafBytes
	// LeafMessage: a message, a repeated message, or a map with message values.
	LeafMessage
)

// FieldLeafKind classifies a model field's value type. For a map, it is the
// value's type: map keys are identifiers and are never redacted.
func FieldLeafKind(f model.Field) SensitiveLeafKind {
	if isInjectedTraceID(f.Name, f.Number) {
		return LeafNone
	}
	return leafKind(f.IsMap, f.Type, f.IsMessage, f.MapValueType, f.MapValueIsMessage)
}

func leafKind(isMap bool, typ string, isMessage bool, mapValueType string, mapValueIsMessage bool) SensitiveLeafKind {
	if isMap {
		typ, isMessage = mapValueType, mapValueIsMessage
	}
	switch {
	case isMessage:
		return LeafMessage
	case typ == "string":
		return LeafString
	case typ == "bytes":
		return LeafBytes
	}
	return LeafNone
}

// TraceIDFieldName / TraceIDFieldNumber are the trace_id tacplus injects into
// every message (pkg/internal/proto/trace.go reads them from here). It is never
// sensitive: Redact<T> leaves it, so a redacted copy still carries its trace.
const (
	TraceIDFieldName   = "trace_id"
	TraceIDFieldNumber = 200
)

// isInjectedTraceID reports whether f is the injected trace_id.
func isInjectedTraceID(name string, number int) bool {
	return name == TraceIDFieldName && number == TraceIDFieldNumber
}

// SensitiveIndicator reports whether tacplus injects the companion
// "<field>_redacted" bool for a field: it carries (spec.sensitive), or it is an
// eligible (string, bytes or message) field of a (spec.sensitive_message) that
// is not (spec.not_sensitive). The proto injector and every Redact<T> emitter
// use this one predicate, so an indicator exists exactly where Redact<T> sets it.
func SensitiveIndicator(msgAnns, fieldAnns map[string]model.AnnotationValue, kind SensitiveLeafKind, name string, number int) bool {
	if isInjectedTraceID(name, number) {
		return false
	}
	if _, ok := fieldAnns["sensitive"]; ok {
		return true
	}
	if _, ok := fieldAnns["not_sensitive"]; ok {
		return false
	}
	_, all := msgAnns["sensitive_message"]
	return all && kind != LeafNone
}

// DescriptorLeafKind classifies a FieldDescriptor the same way FieldLeafKind
// classifies a model field (the proto injector works on descriptors).
func DescriptorLeafKind(f FieldDescriptor) SensitiveLeafKind {
	if isInjectedTraceID(f.Name, int(f.Number)) {
		return LeafNone
	}
	return leafKind(f.IsMap, f.Type, f.TypeKind == "message", f.MapValueType, f.MapValueTypeKind == "message")
}

// RedactAction is what Redact<T> does with one field.
type RedactAction int

const (
	// RedactLeafValue replaces each set string/bytes value with the marker.
	RedactLeafValue RedactAction = iota
	// RedactAllLeaves applies RedactAll<Sub> (every string/bytes leaf) to the
	// message value(s).
	RedactAllLeaves
	// RedactInto applies Redact<Sub> (only Sub's annotated leaves) to the message
	// value(s).
	RedactInto
)

// RedactField is one field Redact<T> touches.
type RedactField struct {
	Field     model.Field
	Kind      SensitiveLeafKind
	Action    RedactAction
	Indicator bool // set <field>_redacted when the field held a value
}

// RedactPlan is the per-message plan of Redact<T> (All=false) or of
// RedactAll<T> (All=true, every string/bytes leaf).
type RedactPlan struct {
	Message *model.Message
	Package string
	All     bool
	Fields  []RedactField
}

// SensitiveIndex answers which messages need Redact<T> and RedactAll<T>, over
// every parsed file, cycle-safe.
type SensitiveIndex struct {
	r        *MessageResolver
	needs    map[string]bool // pkg\x00name -> Redact<T> has something to do
	allNeeds map[string]bool // pkg\x00name -> RedactAll<T> is referenced
}

// NewSensitiveIndex builds the index over allFiles. Call
// ValidateSensitiveAnnotations first: the index assumes a valid annotation set.
func NewSensitiveIndex(allFiles []model.ProtoFile, r *MessageResolver) *SensitiveIndex {
	ix := &SensitiveIndex{r: r, needs: map[string]bool{}, allNeeds: map[string]bool{}}
	// Fixed point: a message needs Redact<T> when one of its fields is
	// sensitive or recurses into a message that needs it.
	for changed := true; changed; {
		changed = false
		for fi := range allFiles {
			pf := &allFiles[fi]
			for mi := range pf.Messages {
				m := &pf.Messages[mi]
				k := pf.Package + "\x00" + m.Name
				if ix.needs[k] {
					continue
				}
				if ix.computeNeeds(m, pf.Package) {
					ix.needs[k] = true
					changed = true
				}
			}
		}
	}
	// RedactAll<T> targets: every message reached through an all-leaves action,
	// transitively.
	var mark func(m *model.Message, pkg string)
	mark = func(m *model.Message, pkg string) {
		k := pkg + "\x00" + m.Name
		if ix.allNeeds[k] {
			return
		}
		ix.allNeeds[k] = true
		for _, f := range m.Fields {
			if _, ok := f.Annotations["not_sensitive"]; ok {
				continue
			}
			if FieldLeafKind(f) == LeafMessage {
				if t, tpkg, ok := ix.resolveValue(pkg, f); ok {
					mark(t, tpkg)
				}
			}
		}
	}
	for fi := range allFiles {
		pf := &allFiles[fi]
		for mi := range pf.Messages {
			m := &pf.Messages[mi]
			_, all := m.Annotations["sensitive_message"]
			for _, f := range m.Fields {
				if FieldLeafKind(f) != LeafMessage {
					continue
				}
				_, sens := f.Annotations["sensitive"]
				_, not := f.Annotations["not_sensitive"]
				if sens || (all && !not) {
					if t, tpkg, ok := ix.resolveValue(pf.Package, f); ok {
						mark(t, tpkg)
					}
				}
			}
		}
	}
	return ix
}

func (ix *SensitiveIndex) computeNeeds(m *model.Message, pkg string) bool {
	if _, ok := m.Annotations["sensitive_message"]; ok {
		return true
	}
	for _, f := range m.Fields {
		if _, ok := f.Annotations["sensitive"]; ok {
			return true
		}
		if FieldLeafKind(f) == LeafMessage {
			if t, tpkg, ok := ix.resolveValue(pkg, f); ok && ix.needs[tpkg+"\x00"+t.Name] {
				return true
			}
		}
	}
	return false
}

// resolveValue resolves a message-typed field, or a map's message value type.
func (ix *SensitiveIndex) resolveValue(pkg string, f model.Field) (*model.Message, string, bool) {
	if f.IsMap {
		if !f.MapValueIsMessage {
			return nil, "", false
		}
		f = model.Field{Name: f.Name, Type: f.MapValueType, TypePackage: f.MapValueTypePackage, IsMessage: true}
	}
	t, ok := ix.r.Resolve(pkg, f)
	if !ok {
		return nil, "", false
	}
	return t, childPkg(pkg, f), true
}

// NeedsRedact reports whether Redact<T> is generated for a message.
func (ix *SensitiveIndex) NeedsRedact(pkg, name string) bool { return ix.needs[pkg+"\x00"+name] }

// NeedsRedactAll reports whether RedactAll<T> is generated for a message.
func (ix *SensitiveIndex) NeedsRedactAll(pkg, name string) bool { return ix.allNeeds[pkg+"\x00"+name] }

// Plan returns the Redact<T> (all=false) or RedactAll<T> (all=true) plan of m.
// A (spec.sensitive_message) message's Redact<T> is its all-leaves plan.
func (ix *SensitiveIndex) Plan(m *model.Message, pkg string, all bool) *RedactPlan {
	if _, ok := m.Annotations["sensitive_message"]; ok {
		all = true
	}
	p := &RedactPlan{Message: m, Package: pkg, All: all}
	for _, f := range m.Fields {
		kind := FieldLeafKind(f)
		_, sens := f.Annotations["sensitive"]
		_, not := f.Annotations["not_sensitive"]
		ind := SensitiveIndicator(m.Annotations, f.Annotations, kind, f.Name, f.Number)
		covered := sens || (all && !not)
		switch {
		case kind == LeafNone:
			continue
		case covered && kind != LeafMessage:
			p.Fields = append(p.Fields, RedactField{Field: f, Kind: kind, Action: RedactLeafValue, Indicator: ind})
		case covered:
			p.Fields = append(p.Fields, RedactField{Field: f, Kind: kind, Action: RedactAllLeaves, Indicator: ind})
		case kind == LeafMessage:
			if t, tpkg, ok := ix.resolveValue(pkg, f); ok && ix.needs[tpkg+"\x00"+t.Name] {
				p.Fields = append(p.Fields, RedactField{Field: f, Kind: kind, Action: RedactInto})
			}
		}
	}
	return p
}

// SensitiveCheck is the generated validation of one singular string/bytes
// sensitive field: required, and the value checks (placeholders, entropy).
type SensitiveCheck struct {
	Required       bool
	CheckValue     bool  // placeholders + entropy floor
	MinEntropyBits int64 // meaningful when CheckValue
}

// FieldSensitiveCheck returns the validation a field needs, or ok=false when it
// needs none. A field's own (spec.sensitive) wins; otherwise a direct
// string/bytes field of a (spec.sensitive_message) inherits the message's.
func FieldSensitiveCheck(msgAnns map[string]model.AnnotationValue, f model.Field) (SensitiveCheck, bool) {
	kind := FieldLeafKind(f)
	if f.IsRepeated || f.IsMap || (kind != LeafString && kind != LeafBytes) {
		return SensitiveCheck{}, false
	}
	ann, ok := f.Annotations["sensitive"]
	if !ok {
		if _, not := f.Annotations["not_sensitive"]; not {
			return SensitiveCheck{}, false
		}
		ann, ok = msgAnns["sensitive_message"]
	}
	if !ok {
		return SensitiveCheck{}, false
	}
	c := SensitiveCheck{Required: ann.BoolValue}
	switch {
	case ann.IntValue >= 0:
		c.CheckValue, c.MinEntropyBits = true, ann.IntValue
	case ann.BoolValue:
		c.CheckValue, c.MinEntropyBits = true, DefaultMinEntropyBits
	}
	if !c.Required && !c.CheckValue {
		return SensitiveCheck{}, false
	}
	return c, true
}

// EntropyMicroBits is floor(log2(pool) * 1e6) for every pool size a string's
// character classes can add up to (lower 26, upper 26, digits 10, other 33).
// The generators emit this table as integer constants so no language computes a
// logarithm at run time: the verdict is integer arithmetic, identical everywhere.
// A string's estimate in micro-bits is code points x EntropyMicroBits[pool]; a
// bytes value's is length x 8e6.
func EntropyMicroBits() map[int]int64 {
	classes := []int{26, 26, 10, 33}
	out := map[int]int64{}
	for mask := 1; mask < 16; mask++ {
		pool := 0
		for i, c := range classes {
			if mask&(1<<i) != 0 {
				pool += c
			}
		}
		out[pool] = int64(math.Floor(math.Log2(float64(pool)) * 1e6))
	}
	return out
}

// EntropyPools returns the pool sizes of EntropyMicroBits in ascending order,
// so the emitted tables are deterministic.
func EntropyPools() []int {
	t := EntropyMicroBits()
	pools := make([]int, 0, len(t))
	for p := range t {
		pools = append(pools, p)
	}
	sort.Ints(pools)
	return pools
}

// ValidateSensitiveAnnotations enforces the (spec.sensitive) contract at parse
// time, identically for every target language, and returns every violation:
//
//   - (spec.sensitive) only on string, bytes and message fields (singular,
//     repeated or a map's value); never on a oneof member;
//   - not both (spec.sensitive) and (spec.not_sensitive), nor (spec.sensitive)
//     and the legacy (spec.redact) (they would inject the same indicator);
//   - required / min_entropy_bits only on a singular string or bytes field;
//   - a message Redact<T> must traverse resolves in the same package: a
//     cross-package, oneof or unresolvable (well-known) message in a sensitive
//     tree is refused rather than silently left unredacted.
func ValidateSensitiveAnnotations(allFiles []model.ProtoFile) error {
	var errs []string
	r := NewMessageResolver(allFiles)
	for _, pf := range allFiles {
		for _, msg := range pf.Messages {
			if _, ok := msg.Annotations["sensitive_message"]; ok {
				for _, f := range msg.Fields {
					if _, red := f.Annotations["redact"]; red && SensitiveIndicator(msg.Annotations, f.Annotations, FieldLeafKind(f), f.Name, f.Number) {
						errs = append(errs, fmt.Sprintf("%s.%s: (spec.redact) inside a (spec.sensitive_message) — use (spec.sensitive) only (the legacy (spec.redact) is deprecated)", msg.Name, f.Name))
					}
				}
			}
			for _, f := range msg.Fields {
				ann, sens := f.Annotations["sensitive"]
				_, not := f.Annotations["not_sensitive"]
				kind := FieldLeafKind(f)
				if sens && not {
					errs = append(errs, fmt.Sprintf("%s.%s: cannot be both (spec.sensitive) and (spec.not_sensitive)", msg.Name, f.Name))
				}
				if !sens {
					continue
				}
				if kind == LeafNone {
					errs = append(errs, fmt.Sprintf("%s.%s: (spec.sensitive) is allowed only on string, bytes and message fields (and maps with such values); this field is %s", msg.Name, f.Name, fieldTypeLabel(f)))
					continue
				}
				if f.IsOneOf {
					errs = append(errs, fmt.Sprintf("%s.%s: (spec.sensitive) is not supported on a oneof member", msg.Name, f.Name))
				}
				if _, red := f.Annotations["redact"]; red {
					errs = append(errs, fmt.Sprintf("%s.%s: cannot be both (spec.sensitive) and the legacy (spec.redact); use (spec.sensitive) only", msg.Name, f.Name))
				}
				if (ann.BoolValue || ann.IntValue >= 0) && (f.IsRepeated || f.IsMap || kind == LeafMessage) {
					errs = append(errs, fmt.Sprintf("%s.%s: (spec.sensitive) required / min_entropy_bits apply only to a singular string or bytes field", msg.Name, f.Name))
				}
			}
		}
	}
	if len(errs) == 0 {
		// The tree checks need a valid annotation set to plan over.
		errs = append(errs, validateSensitiveTrees(allFiles, r)...)
	}
	if len(errs) > 0 {
		sort.Strings(errs)
		return fmt.Errorf("sensitive annotation validation failed:\n  - %s", strings.Join(errs, "\n  - "))
	}
	return nil
}

// validateSensitiveTrees walks every message Redact<T> or RedactAll<T> is
// generated for and refuses a field the emitters could not redact.
func validateSensitiveTrees(allFiles []model.ProtoFile, r *MessageResolver) []string {
	ix := NewSensitiveIndex(allFiles, r)
	var errs []string
	for _, pf := range allFiles {
		for _, msg := range pf.Messages {
			needs, needsAll := ix.NeedsRedact(pf.Package, msg.Name), ix.NeedsRedactAll(pf.Package, msg.Name)
			if !needs && !needsAll {
				continue
			}
			for _, f := range msg.Fields {
				kind := FieldLeafKind(f)
				if kind == LeafNone {
					continue
				}
				_, sens := f.Annotations["sensitive"]
				_, not := f.Annotations["not_sensitive"]
				_, msgAll := msg.Annotations["sensitive_message"]
				covered := sens || ((needsAll || msgAll) && !not)
				if kind != LeafMessage {
					if covered && f.IsOneOf {
						errs = append(errs, fmt.Sprintf("%s.%s: a sensitive leaf in a oneof is not supported by Redact<T>; move it out of the oneof", msg.Name, f.Name))
					}
					continue
				}
				t, tpkg, ok := ix.resolveValue(pf.Package, f)
				var targetNeeds bool
				if ok {
					targetNeeds = ix.NeedsRedact(tpkg, t.Name) || covered
				}
				if !covered && !targetNeeds {
					continue
				}
				switch {
				case !ok:
					errs = append(errs, fmt.Sprintf("%s.%s: Redact<T> cannot redact type %s (not a message tacplus models); mark the field (spec.not_sensitive) or change its type", msg.Name, f.Name, fieldTypeLabel(f)))
				case tpkg != pf.Package:
					errs = append(errs, fmt.Sprintf("%s.%s: Redact<T> does not cross packages (%s is in package %s); move the sensitive type into package %s or mark the field (spec.not_sensitive)", msg.Name, f.Name, t.Name, tpkg, pf.Package))
				case f.IsOneOf:
					errs = append(errs, fmt.Sprintf("%s.%s: Redact<T> does not recurse into a oneof member; move it out of the oneof", msg.Name, f.Name))
				}
			}
		}
	}
	return errs
}

func fieldTypeLabel(f model.Field) string {
	switch {
	case f.IsMap:
		return "map<" + f.MapKeyType + ", " + f.MapValueType + ">"
	case f.IsRepeated:
		return "repeated " + f.Type
	}
	return f.Type
}

// ValidateSensitiveNames is the gate for the LLM use case: in the tree of every
// message Redact<T> is generated for, a string or bytes field that LOOKS
// sensitive (LooksSensitiveName, or a private-key assert_enc_type) must be
// covered — (spec.sensitive), a (spec.sensitive_message) or a sensitive
// ancestor field — or answered with (spec.not_sensitive). A missed annotation is
// caught at build time, not found in an LLM transcript. The gate covers the
// types a tool can hand over through Redact<T>; a proto that declares no
// sensitive annotation at all is not gated (no consumer's existing build breaks
// in a minor, docs/VERSIONING.md).
func ValidateSensitiveNames(allFiles []model.ProtoFile) error {
	r := NewMessageResolver(allFiles)
	ix := NewSensitiveIndex(allFiles, r)
	var errs []string
	seen := map[string]bool{}
	var walk func(m *model.Message, pkg string, all bool)
	walk = func(m *model.Message, pkg string, all bool) {
		if _, ok := m.Annotations["sensitive_message"]; ok {
			all = true
		}
		k := fmt.Sprintf("%s\x00%s\x00%v", pkg, m.Name, all)
		if seen[k] {
			return
		}
		seen[k] = true
		for _, f := range m.Fields {
			kind := FieldLeafKind(f)
			_, sens := f.Annotations["sensitive"]
			_, not := f.Annotations["not_sensitive"]
			covered := sens || (all && !not)
			switch kind {
			case LeafString, LeafBytes:
				if covered || not {
					continue
				}
				enc, hasEnc := f.Annotations["assert_enc_type"]
				if LooksSensitiveName(f.Name) || (hasEnc && privateKeyEncTypes[enc.EnumValue]) {
					errs = append(errs, fmt.Sprintf("%s.%s.%s", pkg, m.Name, f.Name))
				}
			case LeafMessage:
				if t, tpkg, ok := ix.resolveValue(pkg, f); ok {
					walk(t, tpkg, covered)
				}
			}
		}
	}
	for fi := range allFiles {
		pf := &allFiles[fi]
		for mi := range pf.Messages {
			m := &pf.Messages[mi]
			if ix.NeedsRedact(pf.Package, m.Name) {
				walk(m, pf.Package, false)
			}
		}
	}
	if len(errs) == 0 {
		return nil
	}
	sort.Strings(errs)
	errs = dedupSorted(errs)
	return fmt.Errorf("sensitive-name gate: these fields look sensitive and are reachable from a type with a generated Redact<T>, "+
		"but carry no (spec.sensitive) and no (spec.not_sensitive): %s — annotate each one so the redacted view cannot leak it",
		strings.Join(errs, ", "))
}

func dedupSorted(s []string) []string {
	out := s[:0]
	for i, v := range s {
		if i == 0 || v != s[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// LegacyRedactWarning returns the generation warning for the deprecated
// (spec.redact) / (spec.data_entitlement) annotations, or "" when none is
// declared. Deprecated in 2.6, removed in 3.0 (tacplus#274; USER.md "Redact").
func LegacyRedactWarning(allFiles []model.ProtoFile) string {
	var sites []string
	for _, pf := range allFiles {
		for _, msg := range pf.Messages {
			if _, ok := msg.Annotations["data_entitlement"]; ok {
				sites = append(sites, pf.Package+"."+msg.Name+" (spec.data_entitlement)")
			}
			for _, f := range msg.Fields {
				if _, ok := f.Annotations["redact"]; ok {
					sites = append(sites, pf.Package+"."+msg.Name+"."+f.Name+" (spec.redact)")
				}
			}
		}
	}
	if len(sites) == 0 {
		return ""
	}
	sort.Strings(sites)
	return "warning: deprecated: " + strings.Join(dedupSorted(sites), ", ") +
		": the legacy egress redaction ((spec.redact), (spec.data_entitlement), Sanitize<T>, <T>Entitlement) is deprecated in 2.6 and removed in 3.0; " +
		"use (spec.sensitive) and the generated Redact<T> (docs/USER.md, Redact for any type)"
}
