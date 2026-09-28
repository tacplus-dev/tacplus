// Package collection holds the language-agnostic logic for collection-returning
// RPC reads (the method_collection annotation). It defines the platform
// constants, the max-message-byte computation, the delivery-tier routing, and
// the trust-scaled ceilings. See docs/API.md for the full model.
//
// This package is the single source of truth for these computations; the
// gen-time validator, the constant emitters, and the per-language service
// wrappers all derive their behavior from here so the five backends stay in
// parity.
package collection

import (
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// Platform constants (see docs/API.md "Default constants"). Initial cuts —
// tune under load. Bytes.
const (
	// InlineLimit is the Tier-1↔2 boundary: max bytes for a single unary
	// response. Well under gRPC's 4 MB default message size.
	InlineLimit int64 = 1 << 20 // 1 MB

	// MaterializeCeiling is the Tier-2↔3 boundary: max bytes materialized for
	// one response before a cursor (Tier 3) is required.
	MaterializeCeiling int64 = 64 << 20 // 64 MB

	// DefaultReplayDepth is the WATCH replay-ring depth used when replay_depth
	// is unset. Mirrors the M18 default collection element cap.
	DefaultReplayDepth uint32 = 1024

	// DefaultWatchRevisionField is the delta field every WATCH is keyed by when
	// (spec.method_collection).revision_field names none. It is a DEFAULT, not
	// an assumption: validation proves the field exists and is a singular
	// int64 before any emitter renders an accessor for it.
	DefaultWatchRevisionField = "revision"

	// DefaultPageSizeFallback is the PAGE per-page window used when the
	// annotation omits default_page_size (clamped to the trust ceiling).
	DefaultPageSizeFallback uint32 = 50
)

// Default field bounds (mirror the M18 validation defaults). Used when a field
// carries no explicit assert_max_size.
const (
	DefaultScalarBytes int64 = 32768 // 32 KB for unbounded string/bytes
	DefaultCollCount   int64 = 1024  // element count for unbounded repeated/map
)

// maxRecursionDepth caps message nesting traversal; a structure deeper than this
// (or cyclic) is treated as effectively unbounded for sizing purposes.
const maxRecursionDepth = 24

// unboundedBytes is the sentinel returned when a message's max size cannot be
// finitely bounded (cycle or excessive depth). It exceeds MaterializeCeiling so
// such a read is forced to Tier 3 / rejected rather than silently materialized.
const unboundedBytes int64 = 1 << 62

// Tier is a SNAPSHOT delivery tier.
type Tier int

const (
	TierInline Tier = 1 // single unary message
	TierStream Tier = 2 // server-stream, client accumulates into a collection
	TierCursor Tier = 3 // server-stream, client exposes an iterator
)

// TierFor returns the delivery tier for a SNAPSHOT of maxItems elements each at
// most itemBytes large.
func TierFor(maxItems uint32, itemBytes int64) Tier {
	total := totalBytes(maxItems, itemBytes)
	switch {
	case total <= InlineLimit:
		return TierInline
	case total <= MaterializeCeiling:
		return TierStream
	default:
		return TierCursor
	}
}

func totalBytes(maxItems uint32, itemBytes int64) int64 {
	if itemBytes >= unboundedBytes {
		return unboundedBytes
	}
	// Saturate rather than overflow.
	if maxItems == 0 {
		return 0
	}
	if itemBytes != 0 && int64(maxItems) > (1<<62)/itemBytes {
		return unboundedBytes
	}
	return int64(maxItems) * itemBytes
}

// TrustTier is the caller-trust level a method runs at, derived from its auth
// annotations and service traffic category. Higher = more tolerance.
type TrustTier int

const (
	// TrustAnon: unauthenticated, external (auth_disabled on a C2S service).
	TrustAnon TrustTier = iota
	// TrustC2SAuth: authenticated external (any-user / authorized C2S).
	TrustC2SAuth
	// TrustS2S: internal service↔service. I2I is collapsed here (no I2I marker
	// in the model yet — see docs/API.md).
	TrustS2S
	// TrustC2D: local CLI↔daemon IPC. Treated as internal/trusted.
	TrustC2D
)

// Ceilings is the per-trust resource policy for collection reads.
type Ceilings struct {
	MaxItems       uint32 // max declarable max_items
	AllowWatch     bool   // WATCH permitted
	AllowCursor    bool   // Tier-3 cursor permitted (else capped to Tier 1/2)
	InlineOnly     bool   // SNAPSHOT must be Tier-1 (anon)
	StreamIdleSecs int    // STREAM_IDLE_TIMEOUT for this trust level (0 = n/a)
}

// CeilingsFor returns the policy for a trust tier.
func CeilingsFor(t TrustTier) Ceilings {
	switch t {
	case TrustAnon:
		return Ceilings{MaxItems: 100, AllowWatch: false, AllowCursor: false, InlineOnly: true, StreamIdleSecs: 0}
	case TrustC2SAuth:
		return Ceilings{MaxItems: 10_000, AllowWatch: true, AllowCursor: true, InlineOnly: false, StreamIdleSecs: 30}
	case TrustS2S, TrustC2D:
		return Ceilings{MaxItems: 1_000_000, AllowWatch: true, AllowCursor: true, InlineOnly: false, StreamIdleSecs: 300}
	default:
		return Ceilings{MaxItems: 100, AllowWatch: false, AllowCursor: false, InlineOnly: true, StreamIdleSecs: 0}
	}
}

// TrustOf derives the trust tier from effective auth flags and service category.
// effectiveAuthDisabled / effectiveAuthAnyUser already fold service-level
// defaults with method-level overrides.
func TrustOf(isC2S, isS2S, isC2D, effectiveAuthDisabled bool) TrustTier {
	switch {
	case isC2D:
		return TrustC2D
	case isS2S:
		return TrustS2S
	case isC2S && effectiveAuthDisabled:
		return TrustAnon
	case isC2S:
		return TrustC2SAuth
	default:
		// Unknown/unmarked services are treated as internal S2S-equivalent.
		return TrustS2S
	}
}

// MaxMessageBytes returns an upper bound on the serialized size of one instance
// of msg, using explicit assert_max_size bounds where present and the M18
// defaults otherwise. index maps fully-qualified message names ("pkg.Name") to
// their definitions for nested-type resolution. Returns unboundedBytes for
// cyclic or excessively deep structures.
func MaxMessageBytes(msg *model.Message, pkg string, index map[string]*model.Message) int64 {
	return maxMessageBytes(msg, pkg, index, map[string]bool{}, 0)
}

func maxMessageBytes(msg *model.Message, pkg string, index map[string]*model.Message, visiting map[string]bool, depth int) int64 {
	if msg == nil {
		return 0
	}
	if depth > maxRecursionDepth {
		return unboundedBytes
	}
	fqn := pkg + "." + msg.Name
	if visiting[fqn] {
		return unboundedBytes // cycle
	}
	visiting[fqn] = true
	defer delete(visiting, fqn)

	var total int64
	for i := range msg.Fields {
		f := &msg.Fields[i]
		b := fieldMaxBytes(f, index, visiting, depth)
		if b >= unboundedBytes {
			return unboundedBytes
		}
		total += b
		if total >= unboundedBytes {
			return unboundedBytes
		}
	}
	return total
}

// fieldMaxBytes upper-bounds one field's contribution, including repeated/map
// multiplicity and protobuf framing overhead.
func fieldMaxBytes(f *model.Field, index map[string]*model.Message, visiting map[string]bool, depth int) int64 {
	const tagOverhead = 2 // field tag (conservative for field numbers < 2048)
	const lenOverhead = 5 // length varint for length-delimited entries

	elem := elementMaxBytes(f, index, visiting, depth)
	if elem >= unboundedBytes {
		return unboundedBytes
	}

	if f.IsRepeated || f.IsMap {
		count := DefaultCollCount
		if ann, ok := f.Annotations["assert_max_size"]; ok && ann.IntValue > 0 {
			count = ann.IntValue
		}
		per := elem + tagOverhead + lenOverhead
		if per != 0 && count > (unboundedBytes)/per {
			return unboundedBytes
		}
		return count * per
	}
	return elem + tagOverhead + lenOverhead
}

// elementMaxBytes is the max size of a single element value (ignoring repeated
// multiplicity): the scalar size, the bounded string/bytes size, or the nested
// message size.
func elementMaxBytes(f *model.Field, index map[string]*model.Message, visiting map[string]bool, depth int) int64 {
	if f.IsMap {
		// key (scalar) + value
		key := scalarBytes(f.MapKeyType)
		var val int64
		switch {
		case f.MapValueIsMessage:
			val = nestedMessageBytes(f.MapValueType, f.MapValueTypePackage, index, visiting, depth)
		case f.MapValueIsEnum:
			val = 10
		default:
			val = scalarBytes(f.MapValueType)
			if bound := f.ElementMaxSizeBytes(); bound > 0 && (f.MapValueType == "string" || f.MapValueType == "bytes") {
				val = bound
			}
		}
		if val >= unboundedBytes {
			return unboundedBytes
		}
		return key + val
	}
	if f.IsMessage {
		return nestedMessageBytes(f.Type, f.TypePackage, index, visiting, depth)
	}
	if f.IsEnum {
		return 10 // varint, conservative
	}
	switch strings.ToLower(f.Type) {
	case "string", "bytes":
		return scalarStringBytes(f)
	}
	return scalarBytes(f.Type)
}

func scalarStringBytes(f *model.Field) int64 {
	// On a repeated/map field, assert_max_size is an element COUNT, not a
	// per-element byte length (see spec.proto and the runtime validator, which
	// only checks len(field) for these). fieldMaxBytes already consumes it as
	// the count; assert_element_max_size is the annotation that bounds an
	// individual element's length, and without it an element falls back to the
	// unbounded-string default.
	if f.IsRepeated || f.IsMap {
		if bound := f.ElementMaxSizeBytes(); bound > 0 {
			return bound
		}
		return DefaultScalarBytes
	}
	var bound int64
	for _, name := range []string{"assert_max_size"} {
		ann, ok := f.Annotations[name]
		if !ok || ann.IntValue <= 0 {
			continue
		}
		if bound == 0 || ann.IntValue < bound {
			bound = ann.IntValue
		}
	}
	if bound > 0 {
		return bound
	}
	return DefaultScalarBytes
}

func nestedMessageBytes(typeName, typePkg string, index map[string]*model.Message, visiting map[string]bool, depth int) int64 {
	if index == nil {
		return unboundedBytes // can't resolve → treat as unbounded (forces Tier 3 / error)
	}
	candidates := []string{}
	if typePkg != "" {
		candidates = append(candidates, typePkg+"."+typeName)
	}
	candidates = append(candidates, typeName)
	for _, key := range candidates {
		if m, ok := index[key]; ok {
			return maxMessageBytes(m, msgPkg(key), index, visiting, depth+1)
		}
	}
	// Unresolved (e.g. well-known type like Timestamp) — treat as a small fixed
	// blob rather than unbounded, to avoid spuriously forcing Tier 3.
	return 64
}

func msgPkg(fqn string) string {
	if i := strings.LastIndex(fqn, "."); i >= 0 {
		return fqn[:i]
	}
	return ""
}

// scalarBytes returns the conservative max serialized size for a proto scalar
// type name. Unbounded string/bytes use the M18 default. Unknown names default
// to the scalar default (treated as a bounded blob).
func scalarBytes(t string) int64 {
	switch strings.ToLower(t) {
	case "bool":
		return 1
	case "int32", "uint32", "sint32":
		return 5
	case "int64", "uint64", "sint64":
		return 10
	case "fixed32", "sfixed32", "float":
		return 4
	case "fixed64", "sfixed64", "double":
		return 8
	case "string", "bytes":
		return DefaultScalarBytes
	default:
		return DefaultScalarBytes
	}
}

// ItemBytes returns the max serialized size of a single element of a repeated
// field f (ignoring multiplicity) — i.e. the "item" size for tier routing.
func ItemBytes(f *model.Field, index map[string]*model.Message) int64 {
	return elementMaxBytes(f, index, map[string]bool{}, 0)
}

// Resolved is the concrete, codegen-facing view of a method_collection
// annotation: the delivery tier (SNAPSHOT), the effective bounds, and the
// runtime knobs. The per-language emitters build this once per collection
// method and branch on it, so all five backends derive identical behavior.
type Resolved struct {
	Mode        string // "SNAPSHOT" | "WATCH" | "PAGE"
	Tier        Tier   // SNAPSHOT delivery tier (0 for WATCH/PAGE)
	MaxItems    uint32 // SNAPSHOT item cap
	ReplayDepth uint32 // WATCH ring depth, default applied
	IdleSecs    int    // STREAM_IDLE_TIMEOUT for the caller-trust level
	OrderBy     []string

	// PAGE-only (0/empty otherwise): the per-page window default (clamped to the
	// trust ceiling), the hard per-page ceiling (the trust-tier MaxItems), and the
	// FQN of the response's single repeated items element (the page item type).
	DefaultPageSize uint32
	MaxPageSize     uint32
	PageItemType    string
	PageItemsField  string // proto name of the response's single repeated items field
	PageHasTotal    bool   // response declares an optional total_count field

	// WATCH-only envelope shape, resolved from the {Method}WatchEvent for the
	// emitters (empty on SNAPSHOT). FQNs ("pkg.Name") are rendered per language.
	WatchOneOf      string // proto oneof name in the envelope (e.g. "event")
	WatchItemType   string // FQN of the snapshot_item message (the watched item)
	WatchChangeType string // FQN of the delta message ({Method}Change)
	// WatchRevisionField is the proto (snake_case) name of the int64 field on
	// the delta that carries each change's revision — what the replay ring is
	// keyed by and what from_revision resumes against. Always populated on
	// WATCH: DefaultWatchRevisionField unless (spec.method_collection)
	// .revision_field names another. Each emitter renders it in its own idiom
	// (prost snake_case, Java/Go getters); none of them may assume "revision".
	WatchRevisionField string
	// WatchHasFromRevision is true when the request message carries a
	// `from_revision` field (the standard {Method}Request), so the wrapper can
	// resume a watch from a caller-supplied revision. Handle-based watches (e.g.
	// RecordMap's QueryHandle) omit it — the wrapper then only snapshots + streams
	// live changes with no revision resume.
	WatchHasFromRevision bool
}

// Resolve turns an annotation plus its computed context into the concrete
// codegen values. mode is "SNAPSHOT"|"WATCH"; itemBytes is the per-item max
// size (from MaxMessageBytes for a streamed item, or ItemBytes for a unary
// repeated field); trust is the caller-trust level. replayDepth of 0 falls back
// to the platform default.
func Resolve(mode string, maxItems uint32, replayDepth uint32, defaultPageSize uint32, orderBy []string, itemBytes int64, trust TrustTier) Resolved {
	r := Resolved{Mode: mode, MaxItems: maxItems, OrderBy: orderBy}
	r.IdleSecs = CeilingsFor(trust).StreamIdleSecs
	switch mode {
	case "SNAPSHOT":
		if itemBytes >= 0 {
			r.Tier = TierFor(maxItems, itemBytes)
		}
	case "WATCH":
		r.ReplayDepth = replayDepth
		if r.ReplayDepth == 0 {
			r.ReplayDepth = DefaultReplayDepth
		}
	case "PAGE":
		// PAGE is a unary, client-driven window; page_size is bounded by the
		// trust ceiling. The default applies when the request omits page_size.
		r.MaxPageSize = CeilingsFor(trust).MaxItems
		r.DefaultPageSize = defaultPageSize
		if r.DefaultPageSize == 0 {
			r.DefaultPageSize = DefaultPageSizeFallback
		}
		if r.DefaultPageSize > r.MaxPageSize {
			r.DefaultPageSize = r.MaxPageSize
		}
	}
	return r
}

// BuildMessageIndex builds a fully-qualified-name index ("pkg.Name") of every
// message across the parsed files, for nested-type resolution.
func BuildMessageIndex(files []model.ProtoFile) map[string]*model.Message {
	index := map[string]*model.Message{}
	for fi := range files {
		pf := &files[fi]
		for mi := range pf.Messages {
			m := &pf.Messages[mi]
			index[pf.Package+"."+m.Name] = m
			// Also index by bare name as a fallback for same-package refs.
			if _, ok := index[m.Name]; !ok {
				index[m.Name] = m
			}
		}
	}
	return index
}
