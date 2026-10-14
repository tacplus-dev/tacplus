// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// SortedAnnotationNames returns the keys of an annotation map in a stable,
// lexicographically sorted order. Generators MUST use this instead of ranging
// directly over the map: Go map iteration order is randomized, and emitting
// per-annotation code (validation blocks, etc.) in map order leaks that
// randomization into generated output, breaking byte-for-byte determinism.
func SortedAnnotationNames(annotations map[string]AnnotationValue) []string {
	names := make([]string, 0, len(annotations))
	for name := range annotations {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// DefaultRecordKeyField is the record-key field name assumed when a (spec.record)
// message does not declare one via (spec.record).key. The RecordMap query plane,
// the projection generators, and the registry (llmdesc) emitter all resolve to
// this ONE default so a record and its projections/registry entry never disagree
// on which field is the record key.
const DefaultRecordKeyField = "event_id"

// IsEnumSentinel returns true if the enum value name is a sentinel/zero value
// that should be excluded from parse/toString/values helpers.
// Uses substring matching because these are well-known protobuf naming conventions
// for zero/sentinel values, not expressible via proto annotations.
func IsEnumSentinel(name string) bool {
	upper := strings.ToUpper(name)
	return strings.Contains(upper, "UNDEFINED") ||
		strings.Contains(upper, "NO_ERROR") ||
		strings.Contains(upper, "UNSPECIFIED")
}

// EnumValue represents a single value in an enum
type EnumValue struct {
	Name      string
	Number    int
	Comment   string // Leading comment from proto source (extracted via protoc --include_source_info)
	JsonValue string // Optional json_return_value annotation for custom JSON serialization
}

// Enum represents an enum type from a proto file
type Enum struct {
	Name       string
	Values     []EnumValue
	Comment    string // Leading comment from proto source (extracted via protoc --include_source_info)
	OuterClass string // Java outer class name (set when merging proto files from the same package)
}

// Constant represents a constant definition
type Constant struct {
	Name    string
	Type    string // Optional type (e.g., "time.Duration")
	Value   string // Go-syntax source literal, rendered into generated source by every backend
	Comment string
}

// RawValue returns the value a string constant's literal denotes, undoing the
// Go-literal quoting Value carries. Non-string constants (and any value that is
// not a quoted literal) are returned unchanged.
func (c Constant) RawValue() string {
	if unquoted, err := strconv.Unquote(c.Value); err == nil {
		return unquoted
	}
	return c.Value
}

// AnnotationType represents the type of an annotation value
type AnnotationType int

const (
	AnnotationTypeBool AnnotationType = iota
	AnnotationTypeEnum
	AnnotationTypeInt
	AnnotationTypeNumericConstraint
	AnnotationTypeFloatConstraint
	AnnotationTypeString
	AnnotationTypeFloat
	AnnotationTypeHost
	AnnotationTypeIdentityResource
)

// NumericConstraint represents constraints for numeric fields
type NumericConstraint struct {
	Gt         *int64  // greater than
	Ge         *int64  // greater than or equal
	Lt         *int64  // less than
	Le         *int64  // less than or equal
	Eq         *int64  // equal
	Ne         *int64  // not equal
	In         []int64 // must be one of these values
	NotIn      []int64 // must not be one of these values
	MultipleOf *int64  // must be divisible by this value
}

// FloatConstraint represents constraints for floating-point fields
type FloatConstraint struct {
	Gt      *float64  // greater than
	Ge      *float64  // greater than or equal
	Lt      *float64  // less than
	Le      *float64  // less than or equal
	Epsilon *float64  // tolerance for equality comparisons
	In      []float64 // must be one of these values (within epsilon)
	NotIn   []float64 // must not be one of these values (within epsilon)
}

// HostValue holds the default host and port for a host.Host field.
type HostValue struct {
	Host string
	Port int32
}

// IdentitySpiffeResourceValue is a parsed (spec.identity_spiffe_resource)
// annotation: the field holds a resource SPIFFE ID constructed from sibling
// fields as resource_spiffe_id(trust_domain, <NamespaceField>, Kind, <IdField>).
type IdentitySpiffeResourceValue struct {
	NamespaceField string
	Kind           string
	IdField        string
}

// SubscriptionEntry represents a parsed (spec.subscribe) annotation.
type SubscriptionEntry struct {
	Source string // raw source string, e.g. "aegis.AegisState.event_stream"
}

// EventBusMethodS2S represents standard S2S method auth carried by an
// EventBus-derived RPC annotation.
type EventBusMethodS2S struct {
	AuthServices []string
	AuthDisabled bool
	// RoutingOnlyServices is the subset of AuthServices whose generated
	// subscriber Handler takes the synthesized <T>RoutingEnvelope instead of the
	// full <T>. Only meaningful on the s2s_subscribe half; empty everywhere else.
	// It grants nothing — see spec.proto's routing_only_services comment, which
	// is also where the "typed boundary, not a memory guarantee" caveat lives.
	RoutingOnlyServices []string
}

// EventBusMethodC2S represents standard C2S method auth carried by an
// EventBus-derived RPC annotation.
type EventBusMethodC2S struct {
	AuthDisabled         bool
	AuthAnyUser          bool
	TargetNamespaceField string
}

// EventBusBindingEntry represents a parsed event_publish/event_watch binding.
type EventBusBindingEntry struct {
	TopicRef  string
	MethodS2S *EventBusMethodS2S
	MethodC2S *EventBusMethodC2S
	// MethodS2SSubscribe is the CONSUMER half of the surface, from
	// (spec.record).s2s_subscribe / (spec.event).s2s_subscribe. MethodS2S names
	// who may publish to the topic; this names who may subscribe to it. Nil when
	// the author declared none, which leaves the service-level default in place.
	MethodS2SSubscribe *EventBusMethodS2S
}

// TimeseriesCardinalityHint is the store-neutral distinct-value hint for a
// timeseries column. Values mirror spec.CardinalityHint (0/1/2).
type TimeseriesCardinalityHint int32

const (
	CardinalityUnspecified TimeseriesCardinalityHint = 0
	CardinalityLow         TimeseriesCardinalityHint = 1 // few distinct values
	CardinalityHigh        TimeseriesCardinalityHint = 2 // many distinct values
)

// TimeseriesDedupStrategy is the store-neutral duplicate-resolution strategy.
// Values mirror spec.DedupStrategy (0/1).
type TimeseriesDedupStrategy int32

const (
	DedupNone            TimeseriesDedupStrategy = 0 // append-only; keep all rows
	DedupLatestByVersion TimeseriesDedupStrategy = 1 // keep latest row per key by version_field
)

// ResidencyPolicyEntry is a record type's lifecycle, as two clocks. Both fields
// are 0 when the author left them unset, which means "apply the platform
// default" (parser.DefaultResidencyIdleSeconds,
// parser.DefaultResidencyDeletedMaxAgeSeconds) — never "never".
type ResidencyPolicyEntry struct {
	IdleSeconds          int64 // no read and no write for this long -> evicted from memory
	DeletedMaxAgeSeconds int64 // flagged deleted AND older than this -> purged at the next sweep
}

// DedupPolicyEntry is the store-neutral duplicate-resolution policy.
type DedupPolicyEntry struct {
	Strategy     TimeseriesDedupStrategy
	KeyFields    []string // logical row identity
	VersionField string   // winning-row selector under DedupLatestByVersion
}

// TransformationFieldEntry represents a single field in a transformation.
type TransformationFieldEntry struct {
	Source          string                    // source field path
	As              string                    // rename in derived type (defaults to last segment of source)
	OrderKey        int32                     // store-neutral sort-key position (0 = not a sort key)
	PartitionKey    bool                      // store-neutral partition role
	CardinalityHint TimeseriesCardinalityHint // store-neutral distinct-value hint
}

// TransformationEntry represents a pure typed conversion of a source message into
// a derived row type. Dedup is semantic intent; a downstream materializer renders
// the concrete backend schema. It carries no lifetime of its own: a pure typed
// conversion has none, and the record's lifecycle lives on
// (spec.record).residency.
type TransformationEntry struct {
	Name   string // derived row type name (e.g. "SessionRecord")
	When   string // oneof variant that triggers this row
	Fields []TransformationFieldEntry
	Dedup  *DedupPolicyEntry // store-neutral dedup policy (nil = none)
}

// AnnotationValue represents a field annotation value
type AnnotationValue struct {
	Type                   AnnotationType
	BoolValue              bool
	EnumValue              string // e.g., "IP_ADDR_TYPE_IP_V4"
	IntValue               int64  // e.g., 10 for min_length
	StringValue            string
	FloatValue             float64
	NumericConstraint      *NumericConstraint
	FloatConstraint        *FloatConstraint
	IdentitySpiffeResource *IdentitySpiffeResourceValue // For identity_spiffe_resource annotations
	StringSliceValue       []string                     // For repeated string annotations (e.g., view_sources)
	HostValue              *HostValue                   // For default_host annotations on host.Host fields
	SubscriptionEntries    []SubscriptionEntry          // For subscribe annotations on service_state messages
	EventPublishEntries    []EventBusBindingEntry       // For event_publish annotations
	EventWatchEntries      []EventBusBindingEntry       // For event_watch annotations
	TransformationEntries  []TransformationEntry        // For transformation annotations
	RecordResidency        *ResidencyPolicyEntry        // For (spec.record).residency (the two lifecycle clocks)
	TimeseriesDedup        *DedupPolicyEntry            // For timeseries_type dedup (store-neutral)
}

// Field represents a field in a message
type Field struct {
	Name                string
	Type                string // e.g., "string", "int64", "KeyType", "Jwk" (just the type name without package)
	TypePackage         string // Package name for cross-package types (e.g., "key" for key.KeyType), empty for same-package types
	IsRepeated          bool
	IsOptional          bool // True if field is marked optional in proto3 (generates pointer in Go)
	IsMessage           bool
	IsEnum              bool
	IsMap               bool   // True if field is a map type
	IsOneOf             bool   // True if field is inside a oneof group
	OneOfName           string // Name of the oneof group (e.g., "boundary"), empty if not oneof
	MapKeyType          string // For map fields: the key type (e.g., "string")
	MapValueType        string // For map fields: the value type (e.g., "string", "int64")
	MapValueTypePackage string // For map fields with message/enum values: the Go package alias (e.g., "key"), empty for same-package types
	MapValueIsMessage   bool   // True if map value type is a message (needs pointer in Go)
	MapValueIsEnum      bool   // True if map value type is an enum (serialize as string in JSON)
	ProtoType           string // Original proto type before package resolution
	Number              int
	Comment             string                     // Leading comment from proto source (extracted via protoc --include_source_info)
	Annotations         map[string]AnnotationValue // Field options/annotations like assert_required, assert_ipaddr_type
	ParamKey            string                     // Explicit enum value for FromMap generation, e.g., "TOKEN_PARAM_KEY_CLIENT_ID"
	JsonKey             string                     // Override JSON key for serialization (from spec.json_key)
	JsonAliases         []string                   // Additional keys accepted during deserialization (from spec.json_aliases)
}

// ElementMaxSizeBytes returns the per-element byte bound declared by
// (spec.assert_element_max_size) on f, or 0 when the annotation is absent. The
// annotation bounds each element of a repeated string/bytes field and each
// VALUE of a map with a string/bytes value type; map keys are not covered.
// Callers that emit validation MUST call ValidateElementMaxSize first so a
// misapplied annotation is a generation error rather than a silent no-op.
func (f *Field) ElementMaxSizeBytes() int64 {
	ann, ok := f.Annotations["assert_element_max_size"]
	if !ok || ann.Type != AnnotationTypeInt || ann.IntValue <= 0 {
		return 0
	}
	return ann.IntValue
}

// ValidateElementMaxSize reports a misapplied (spec.assert_element_max_size):
// the annotation is only meaningful on a repeated field of type string/bytes or
// on a map whose value type is string/bytes. msgName names the owning message
// for the error text.
func (f *Field) ValidateElementMaxSize(msgName string) error {
	ann, ok := f.Annotations["assert_element_max_size"]
	if !ok {
		return nil
	}
	if ann.Type != AnnotationTypeInt {
		return fmt.Errorf("assert_element_max_size annotation on field %s.%s must be an integer value", msgName, f.Name)
	}
	switch {
	case f.IsMap:
		if f.MapValueType == "string" || f.MapValueType == "bytes" {
			return nil
		}
	case f.IsRepeated:
		if f.Type == "string" || f.Type == "bytes" {
			return nil
		}
	}
	return fmt.Errorf("assert_element_max_size annotation on field %s.%s requires a repeated string/bytes field or a map with a string/bytes value type", msgName, f.Name)
}

// ParamMapConfig represents the generate_from_params message option
type ParamMapConfig struct {
	KeyEnum          string // The enum type to use for parameter keys (e.g., "TokenParamKey")
	Strict           bool   // Whether to return an error on unknown keys
	ValidateRequired bool   // Whether to validate required fields
}

// Message represents a message type from a proto file
type Message struct {
	Name           string
	Fields         []Field
	Comment        string                     // Leading comment from proto source (extracted via protoc --include_source_info)
	ParamMapConfig *ParamMapConfig            // Optional generate_from_params configuration
	Annotations    map[string]AnnotationValue // Message-level annotations (e.g., service_state)
	OuterClass     string                     // Java outer class name (set when merging proto files from the same package)
}

// Import represents an import statement in a proto file
type Import struct {
	Path    string // e.g., "sys/key.proto"
	Package string // e.g., "key"
}

// ProtoFile represents a parsed proto file
type ProtoFile struct {
	Package        string
	Enums          []Enum
	Constants      []Constant
	Messages       []Message
	Imports        []Import
	SourcePath     string
	RelPath        string
	JavaOuterClass string // The Java outer class name for this proto file
}
