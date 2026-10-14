// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

// Package boundaries provides shared boundary collection logic used by all
// language generators. It extracts boundary metadata from proto file
// descriptors using annotation-driven detection (no naming conventions).
package boundaries

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/eventbus"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
)

// EventPublisherData is language-neutral metadata for a validated event_publish
// binding declared by the current actor.
type EventPublisherData struct {
	TopicRef          string // e.g. "telemetry.TraceEventBus.trace_events"
	BrokerLabel       string // service_id of the broker backing this topic
	EventTypeFQN      string // e.g. "trace.TraceEvent"
	EventTypeName     string // e.g. "TraceEvent"
	RPCStem           string // e.g. "TelemetryTraceEventBusTraceEvents"
	PublishStreamName string // e.g. "PublishTelemetryTraceEventBusTraceEvents"
	PublishFullMethod string // broker-qualified EventBus typed service method
	PublishPath       string // broker-qualified gRPC path for the publish stream
}

const TraceEventTypeFQN = "trace.TraceEvent"

func IsTraceEventPublisherData(publisher EventPublisherData) bool {
	return publisher.EventTypeFQN == TraceEventTypeFQN
}

// AccountingRecordTypeFQN is the durable accounting record — the flat, stored
// projection of accounting.Event that the chassis accounting sink publishes.
// Declared in platform/proto/vservices/accounting-record.proto; the topic
// wrapper (AccountingRecordBus) is synthesized from its spec.record.
const AccountingRecordTypeFQN = "accountingrecord.AccountingRecord"

// LookupAccountingRecordTopic returns the EventBus topic that carries
// AccountingRecord, if the loaded proto set declares one. Unlike every other
// publisher, this binding is not declared per-actor: accounting is a chassis
// concern, so the generator resolves the topic from the record type itself.
// Returns ok=false when the proto set has no accounting record (client repos,
// or any build that does not include platform/proto/vservices).
func LookupAccountingRecordTopic(p *parser.DescriptorParser) (eventbus.Topic, bool, error) {
	registry, err := eventbus.BuildTopicRegistry(allParserFiles(p))
	if err != nil {
		return eventbus.Topic{}, false, err
	}
	matches := registry.TopicRefsForEventType(AccountingRecordTypeFQN)
	switch len(matches) {
	case 0:
		return eventbus.Topic{}, false, nil
	case 1:
		return matches[0], true, nil
	default:
		refs := make([]string, 0, len(matches))
		for _, t := range matches {
			refs = append(refs, t.Ref.Raw)
		}
		sort.Strings(refs)
		return eventbus.Topic{}, false, fmt.Errorf("%s is carried by more than one EventBus topic (%s); the accounting sink needs exactly one", AccountingRecordTypeFQN, strings.Join(refs, ", "))
	}
}

// BoundaryData is the language-neutral metadata for a single boundary field.
type BoundaryData struct {
	StateRoot       string   // e.g. "BaseballState"
	Labels          []string // routing labels from the service_state_label annotation
	AutoTlsKeyType  string   // key algorithm when auto_tls is enabled ("ed25519" or "ecdsa"), empty = disabled
	Boundary        string   // PascalCase field name, e.g. "Roster"
	FieldName       string   // original field name, e.g. "roster"
	Persistence     bool
	Collection      bool   // true when the field is repeated
	TypeFQN         string // proto type FQN, e.g. "baseball.Roster"
	IsConfig        bool   // true when state has a grpc_config annotated type
	GraphType       string // state-graph boundary kind: "singleton", "collection", "topic", "queue", "priority_queue", "command", "view", "state_import", "private" (unrelated to the removed graph store)
	CollectedHealth bool   // true when boundary is marked for Sentinel health collection
	EvictionMaxSize int64  // max entries before eviction (from spec.eviction_max_size), 0 = unlimited
	Rpcs            []string
	BoundaryCallers []string
}

// CollectMessageBoundaries collects all boundary metadata from a single state
// root message. The file descriptor is used for config detection (looking up
// referenced message types for the grpc_config annotation).
func CollectMessageBoundaries(msg *parser.MessageDescriptor, fd *parser.FileDescriptor) []BoundaryData {
	stateAnn, isState := msg.Annotations["service_state"]
	if !isState {
		return nil
	}
	stateRoot := stateAnn.StringValue

	// Routing labels from service_state_label.
	var labels []string
	if labelAnn, ok := msg.Annotations["service_state_label"]; ok {
		labels = labelAnn.StringSliceValue
	}

	// Auto-TLS: automatic cert lifecycle management.
	var autoTlsKeyType string
	if ann, ok := msg.Annotations["service_state_auto_tls"]; ok && ann.StringValue != "" {
		autoTlsKeyType = ann.StringValue
	}

	// Detect config boundary via grpc_config annotation on a referenced type.
	isConfig := detectConfig(msg, fd)

	var boundaries []BoundaryData
	for _, field := range msg.Fields {
		if field.TypeKind != "message" || field.IsMap {
			continue
		}
		bd := BoundaryData{
			StateRoot:      stateRoot,
			Labels:         labels,
			AutoTlsKeyType: autoTlsKeyType,
			Boundary:       strutil.ToPascalCase(field.Name),
			FieldName:      field.Name,
			TypeFQN:        field.Type,
			Collection:     field.IsRepeated,
			IsConfig:       isConfig,
			GraphType:      GraphTypeForField(field),
		}
		if ann, ok := field.Annotations["persistence"]; ok {
			bd.Persistence = ann.BoolValue
		}
		if ann, ok := field.Annotations["collected_health"]; ok {
			bd.CollectedHealth = ann.BoolValue
		}
		if ann, ok := field.Annotations["eviction_max_size"]; ok && ann.IntValue > 0 {
			bd.EvictionMaxSize = ann.IntValue
		}
		if ann, ok := field.Annotations["boundary_callers"]; ok {
			bd.BoundaryCallers = ann.StringSliceValue
		}
		bd.Rpcs = DeriveBoundaryRpcs(field)
		boundaries = append(boundaries, bd)
	}
	return boundaries
}

// DefaultCollectionSafetyLimit is the entry count a generated state-client
// collection watch accepts without complaint when the boundary field declares
// no (spec.eviction_max_size). It mirrors the Go state wrapper's long-standing
// maxEntries default so every language reports the same overflow.
const DefaultCollectionSafetyLimit = 100000

// CollectionSafetyLimit returns the client-side entry bound a generated
// collection watch enforces for a state boundary field: the field's
// (spec.eviction_max_size) when it declares one, otherwise
// DefaultCollectionSafetyLimit. Every language's state-wrapper generator reads
// the annotation through here so the emitters cannot drift from each other.
func CollectionSafetyLimit(field parser.FieldDescriptor) int64 {
	if ann, ok := field.Annotations["eviction_max_size"]; ok && ann.IntValue > 0 {
		return ann.IntValue
	}
	return DefaultCollectionSafetyLimit
}

// CollectAllBoundaries collects boundary metadata from all file descriptors.
func CollectAllBoundaries(fds []*parser.FileDescriptor) []BoundaryData {
	var all []BoundaryData
	for _, fd := range fds {
		for i := range fd.Messages {
			all = append(all, CollectMessageBoundaries(&fd.Messages[i], fd)...)
		}
	}
	return all
}

// GraphTypeForField returns the graph type string for a boundary field.
func GraphTypeForField(field parser.FieldDescriptor) string {
	if field.Annotations["private"].BoolValue {
		return "private"
	}
	if field.Annotations["command"].BoolValue {
		return "command"
	}
	if field.Annotations["topic"].BoolValue {
		return "topic"
	}
	if field.Annotations["queue"].BoolValue {
		return "queue"
	}
	if field.Annotations["priority_queue"].StringValue != "" {
		return "priority_queue"
	}
	if _, ok := field.Annotations["view_sources"]; ok {
		return "view"
	}
	if _, ok := field.Annotations["state_import"]; ok {
		return "state_import"
	}
	if field.IsRepeated {
		return "collection"
	}
	return "singleton"
}

// DeriveBoundaryRpcs returns the RPC method names generated for a boundary
// field based on its annotations. This mirrors the rules in state.go.
func DeriveBoundaryRpcs(field parser.FieldDescriptor) []string {
	fieldPascal := strutil.ToPascalCase(field.Name)

	if field.Annotations["private"].BoolValue {
		return nil
	}
	if field.Annotations["command"].BoolValue {
		return []string{fieldPascal}
	}
	if field.Annotations["queue"].BoolValue || field.Annotations["priority_queue"].StringValue != "" {
		rpcs := []string{
			"OfferTo" + fieldPascal,
			"ConsumeFrom" + fieldPascal,
			"Peek" + fieldPascal,
			"Size" + fieldPascal,
		}
		if field.Annotations["leader"].BoolValue {
			rpcs = append(rpcs, leaderRpcs(fieldPascal)...)
		}
		return rpcs
	}
	if field.Annotations["topic"].BoolValue {
		rpcs := []string{"PublishTo" + fieldPascal, "SubscribeTo" + fieldPascal}
		if field.Annotations["leader"].BoolValue {
			rpcs = append(rpcs, leaderRpcs(fieldPascal)...)
		}
		return rpcs
	}
	if _, hasView := field.Annotations["view_sources"]; hasView {
		return []string{
			"Get" + fieldPascal,
			"GetAll" + fieldPascal,
			"Watch" + fieldPascal,
			"Query" + fieldPascal,
			"WatchQuery" + fieldPascal,
			"KeySet" + fieldPascal,
			"Size" + fieldPascal,
			"ContainsKey" + fieldPascal,
		}
	}
	if field.IsRepeated {
		rpcs := []string{
			"Get" + fieldPascal,
			"Put" + fieldPascal,
			"Set" + fieldPascal,
			"Remove" + fieldPascal,
			"Delete" + fieldPascal,
			"ContainsKey" + fieldPascal,
			"GetAll" + fieldPascal,
			"KeySet" + fieldPascal,
			"Size" + fieldPascal,
			"Clear" + fieldPascal,
			"PutIfAbsent" + fieldPascal,
			"Watch" + fieldPascal,
		}
		rpcs = append(rpcs, "Query"+fieldPascal, "WatchQuery"+fieldPascal)
		if field.Annotations["refresh_on_miss"].StringValue != "" {
			rpcs = append(rpcs, "Lookup"+fieldPascal)
		}
		return rpcs
	}

	// Singleton.
	rpcs := []string{
		"Get" + fieldPascal,
		"Set" + fieldPascal,
		"Watch" + fieldPascal,
	}
	return rpcs
}

func leaderRpcs(fieldPascal string) []string {
	return []string{
		"Elect" + fieldPascal + "Leader",
		"Resign" + fieldPascal + "Leader",
		"GetLeader" + fieldPascal,
		"WatchLeader" + fieldPascal,
		"RenewLease" + fieldPascal,
	}
}

func detectConfig(msg *parser.MessageDescriptor, fd *parser.FileDescriptor) bool {
	for _, field := range msg.Fields {
		if field.TypeKind != "message" {
			continue
		}
		typeName := field.Type
		if idx := strings.LastIndex(typeName, "."); idx >= 0 {
			typeName = typeName[idx+1:]
		}
		for _, otherMsg := range fd.Messages {
			if otherMsg.Name == typeName {
				if _, hasFE := otherMsg.Annotations["grpc_config"]; hasFE {
					return true
				}
				break
			}
		}
	}
	return false
}

// SubscriptionData is the language-neutral metadata for a single event-bus subscription.
type SubscriptionData struct {
	StateRoot       string // owning state root, e.g. "DgraphState"
	Source          string // raw source ref, e.g. "telemetry.AgenticCodeEventBus.event_stream"
	BrokerLabel     string // service_id of the broker backing this topic
	SourcePackage   string // parsed package, e.g. "telemetry"
	SourceStateRoot string // parsed topic message, e.g. "AgenticCodeEventBus"
	SourceField     string // parsed field name, e.g. "event_stream"
	Origin          string // "subscribe", "event_watch", or "store_derived"
}

// CollectMessageSubscriptions collects legacy subscription metadata from a
// single state root message. New generation paths should use
// CollectActorSubscriptions so topic refs are validated against the EventBus
// topic registry.
func CollectMessageSubscriptions(msg *parser.MessageDescriptor) ([]SubscriptionData, error) {
	subAnn, hasSub := msg.Annotations["subscribe"]
	if !hasSub || len(subAnn.SubscriptionEntries) == 0 {
		return nil, nil
	}

	stateAnn, isState := msg.Annotations["service_state"]
	if !isState {
		return nil, fmt.Errorf("(spec.subscribe) on message %s requires (spec.service_state)", msg.Name)
	}
	stateRoot := stateAnn.StringValue

	var subs []SubscriptionData
	for _, entry := range subAnn.SubscriptionEntries {
		ref, err := eventbus.ParseTopicRef(entry.Source)
		if err != nil {
			continue
		}
		sd := SubscriptionData{
			StateRoot:       stateRoot,
			Source:          ref.Raw,
			BrokerLabel:     eventbus.EventBusLabel,
			SourcePackage:   ref.Package,
			SourceStateRoot: ref.Message,
			SourceField:     ref.Field,
			Origin:          "subscribe",
		}
		subs = append(subs, sd)
	}
	return subs, nil
}

// CollectAllSubscriptions collects subscription metadata from all file descriptors.
func CollectAllSubscriptions(fds []*parser.FileDescriptor) ([]SubscriptionData, error) {
	var all []SubscriptionData
	for _, fd := range fds {
		for i := range fd.Messages {
			subs, err := CollectMessageSubscriptions(&fd.Messages[i])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", fd.Package, err)
			}
			all = append(all, subs...)
		}
	}
	return all, nil
}

// CollectActorSubscriptions normalizes legacy subscribe, explicit event_watch,
// and store-derived watch declarations into validated subscription metadata for
// the current actor.
func CollectActorSubscriptions(p *parser.DescriptorParser, protoDir string, protoFiles []string, selfPackage string) ([]SubscriptionData, error) {
	fds := allParserFiles(p)
	registry, err := eventbus.BuildTopicRegistry(fds)
	if err != nil {
		return nil, err
	}

	var result []SubscriptionData
	seen := make(map[string]bool)
	add := func(sd SubscriptionData) {
		key := sd.SourcePackage + "." + sd.SourceStateRoot + "." + sd.SourceField
		if seen[key] {
			return
		}
		seen[key] = true
		result = append(result, sd)
	}

	for _, fd := range actorFiles(p, protoDir, protoFiles, selfPackage) {
		for i := range fd.Messages {
			msg := &fd.Messages[i]
			stateRoot := msg.Name
			if ann, ok := msg.Annotations["service_state"]; ok && ann.StringValue != "" {
				stateRoot = ann.StringValue
			}

			if subAnn, ok := msg.Annotations["subscribe"]; ok {
				if _, isState := msg.Annotations["service_state"]; !isState {
					return nil, fmt.Errorf("(spec.subscribe) on %s.%s requires (spec.service_state)", fd.Package, msg.Name)
				}
				for _, entry := range subAnn.SubscriptionEntries {
					topic, err := registry.Resolve(entry.Source)
					if err != nil {
						return nil, fmt.Errorf("invalid subscribe on %s.%s: %w", fd.Package, msg.Name, err)
					}
					add(subscriptionDataFromTopic(stateRoot, topic, "subscribe"))
				}
			}

			watchAnn, ok := msg.Annotations["event_watch"]
			if !ok {
				continue
			}
			for _, entry := range watchAnn.EventWatchEntries {
				binding, err := registry.ValidateBinding(eventbus.BindingWatch, fd.Package, msg.Name, entry)
				if err != nil {
					return nil, fmt.Errorf("invalid event_watch on %s.%s: %w", fd.Package, msg.Name, err)
				}
				add(subscriptionDataFromTopic(stateRoot, binding.Topic, "event_watch"))
			}
		}
	}

	derived, err := DeriveStoreSubscriptionsFromRegistry(p, selfPackage, registry)
	if err != nil {
		return nil, err
	}
	for _, sd := range derived {
		add(sd)
	}
	return result, nil
}

// CollectActorEventPublishes returns the validated event_publish bindings
// declared by the current actor. Topic definitions may come from imported protos,
// but publish authority must be declared on an actor-owned file.
func CollectActorEventPublishes(p *parser.DescriptorParser, protoDir string, protoFiles []string, selfPackage string) ([]eventbus.Binding, error) {
	fds := allParserFiles(p)
	registry, err := eventbus.BuildTopicRegistry(fds)
	if err != nil {
		return nil, err
	}

	var result []eventbus.Binding
	seen := make(map[string]string)
	for _, fd := range actorFiles(p, protoDir, protoFiles, selfPackage) {
		for i := range fd.Messages {
			msg := &fd.Messages[i]
			publishAnn, ok := msg.Annotations["event_publish"]
			if !ok {
				continue
			}
			for _, entry := range publishAnn.EventPublishEntries {
				binding, err := registry.ValidateBinding(eventbus.BindingPublish, fd.Package, msg.Name, entry)
				if err != nil {
					return nil, fmt.Errorf("invalid event_publish on %s.%s: %w", fd.Package, msg.Name, err)
				}
				if prev, ok := seen[binding.Topic.Ref.Raw]; ok {
					return nil, fmt.Errorf("duplicate event_publish for %q on %s.%s and %s", binding.Topic.Ref.Raw, fd.Package, msg.Name, prev)
				}
				seen[binding.Topic.Ref.Raw] = fd.Package + "." + msg.Name
				result = append(result, binding)
			}
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Topic.Ref.Raw < result[j].Topic.Ref.Raw
	})
	return result, nil
}

// CollectActorEventPublisherData returns language-neutral publish metadata for
// the current actor. It is intentionally derived from CollectActorEventPublishes
// so collision checks, topic validation, and auth-shape validation stay in one
// path.
func CollectActorEventPublisherData(p *parser.DescriptorParser, protoDir string, protoFiles []string, selfPackage string) ([]EventPublisherData, error) {
	bindings, err := CollectActorEventPublishes(p, protoDir, protoFiles, selfPackage)
	if err != nil {
		return nil, err
	}
	result := make([]EventPublisherData, 0, len(bindings))
	for _, binding := range bindings {
		if binding.MethodS2S == nil {
			continue
		}
		eventType := binding.Topic.EventTypeFQN
		eventTypeName := eventType
		if idx := strings.LastIndex(eventTypeName, "."); idx >= 0 {
			eventTypeName = eventTypeName[idx+1:]
		}
		result = append(result, EventPublisherData{
			TopicRef:          binding.Topic.Ref.Raw,
			BrokerLabel:       binding.Topic.EventBusLabel,
			EventTypeFQN:      eventType,
			EventTypeName:     eventTypeName,
			RPCStem:           binding.Topic.RPCStem,
			PublishStreamName: "Publish" + binding.Topic.RPCStem,
			// Per-topic, not the flat eventbus.EventBusTypedService/Publish<Stem>.
			// The flat name is declared in no proto in the tree, so a client
			// emitting it is reachable only by hardcoding. The broker binds BOTH
			// (eventbus d3b6f93, corrected in 25d2863), so publishers moving here
			// keep working against a deployed broker.
			PublishFullMethod: eventbus.PerTopicPublishFullMethodName(binding.Topic.Ref),
			PublishPath:       eventbus.PerTopicPublishPath(binding.Topic.Ref),
		})
	}
	return result, nil
}

// FindTraceEventPublisherData returns the actor's trace publisher metadata when
// it declares an explicit event_publish for trace.TraceEvent.
func FindTraceEventPublisherData(p *parser.DescriptorParser, protoDir string, protoFiles []string, selfPackage string) (*EventPublisherData, error) {
	publishers, err := CollectActorEventPublisherData(p, protoDir, protoFiles, selfPackage)
	if err != nil {
		return nil, err
	}
	for i := range publishers {
		if IsTraceEventPublisherData(publishers[i]) {
			return &publishers[i], nil
		}
	}
	return nil, nil
}

// SubscriptionResolution holds the resolved type and directory for a subscription source.
type SubscriptionResolution struct {
	TypeFqn  string // e.g. "sensor.SensorEvent"
	ProtoDir string // e.g. "common", "services"
	// IsRecord is true when TypeFqn is a (spec.record) — a stored record type,
	// not merely an event. Every record type's rows land in a NODE-LOCAL store
	// (one in-memory map per type with a disk leg beneath it, checkpointed per
	// pod), so a node's copy is only complete if that node receives every
	// record. That is what selects a per-INSTANCE consumer group for the topic;
	// see the java generator's javaSubscriptionEntry.PerInstanceConsumerGroup.
	IsRecord bool
	// StoreEpochField is the name of TypeFqn's (spec.store_epoch) field, "" when
	// it declares none. A record has no write RPC — it reaches a store on the
	// store's EventBus subscription — so the subscription dispatch is the seam
	// the store's clock is stamped at. See spec.proto's store_epoch comment.
	StoreEpochField string
}

// ResolveSubscriptionSource resolves the fully-qualified type name and proto
// directory for a subscription's source field. It searches all parsed proto
// files for the source package's proto (by base name), then derives the
// directory from the actual descriptor key.
func ResolveSubscriptionSource(p *parser.DescriptorParser, flatDir string, sd SubscriptionData) SubscriptionResolution {
	for relPath, fd := range p.GetAllFiles() {
		if fd.Package != sd.SourcePackage {
			continue
		}
		for _, msg := range fd.Messages {
			if msg.Name == sd.SourceStateRoot {
				for _, field := range msg.Fields {
					if field.Name == sd.SourceField {
						// Derive the proto directory from the relative path
						// (e.g., "common/sensor.proto" → "common").
						protoDir := filepath.Dir(relPath)
						if protoDir == "." {
							protoDir = ""
						}
						return SubscriptionResolution{
							TypeFqn:         field.Type,
							ProtoDir:        protoDir,
							IsRecord:        recordIsRecord(p, field.Type),
							StoreEpochField: recordStoreEpochField(p, field.Type),
						}
					}
				}
			}
		}
	}
	return SubscriptionResolution{}
}

// recordIsRecord reports whether the message named by typeFqn (e.g.
// "accountingrecord.AccountingRecord") carries (spec.record), i.e. whether the
// topic delivers a STORED record rather than a plain event.
//
// This is a lookup, not a second opinion, and it is the single definition of
// "this topic feeds a node-local store" rather than one per consumer. It reads
// the same msg.Annotations["record"] key every other record consumer reads.
func recordIsRecord(p *parser.DescriptorParser, typeFqn string) bool {
	if typeFqn == "" {
		return false
	}
	for _, fd := range p.GetAllFiles() {
		if fd.Package == "" || !strings.HasPrefix(typeFqn, fd.Package+".") {
			continue
		}
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			if fd.Package+"."+msg.Name != typeFqn {
				continue
			}
			return msg.Annotations["record"].BoolValue
		}
	}
	return false
}

// recordStoreEpochField returns the name of the (spec.store_epoch) field declared
// by the message named by typeFqn, or "" when the type is not found or declares
// none.
//
// Same shape as recordIsRecord, and for the same reason: a lookup, not a second
// opinion. The field's well-formedness (int64, non-repeated, RANGE-queryable) is
// enforced once at parse time by parser.ValidateResidencyAnnotations.
func recordStoreEpochField(p *parser.DescriptorParser, typeFqn string) string {
	if typeFqn == "" {
		return ""
	}
	for _, fd := range p.GetAllFiles() {
		if fd.Package == "" || !strings.HasPrefix(typeFqn, fd.Package+".") {
			continue
		}
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			if fd.Package+"."+msg.Name != typeFqn {
				continue
			}
			for i := range msg.Fields {
				if msg.Fields[i].Annotations["store_epoch"].BoolValue {
					return msg.Fields[i].Name
				}
			}
			return ""
		}
	}
	return ""
}

// DeriveStoreSubscriptionsFromRegistry discovers store EventBus subscriptions
// using the validated topic registry. Ambiguous event type matches are
// generation errors; stores must then declare an explicit event_watch topic_ref.
func DeriveStoreSubscriptionsFromRegistry(p *parser.DescriptorParser, selfPackage string, registry *eventbus.TopicRegistry) ([]SubscriptionData, error) {
	sourceFQNs := collectStoreSourceFQNs(p, selfPackage)
	if len(sourceFQNs) == 0 {
		return nil, nil
	}

	// Deterministic order so the error below names the same source every run.
	ordered := make([]string, 0, len(sourceFQNs))
	for sourceFQN := range sourceFQNs {
		ordered = append(ordered, sourceFQN)
	}
	sort.Strings(ordered)

	var subs []SubscriptionData
	for _, sourceFQN := range ordered {
		matches := registry.TopicRefsForEventType(sourceFQN)
		switch len(matches) {
		case 0:
			// PAIRED CONTROL. This store hosts rows derived from sourceFQN, but no
			// EventBus topic publishes that type — so the generated store would
			// subscribe to nothing and never ingest a single row. That used to be a
			// silent `continue`: generation succeeded and produced a store that was
			// dead on arrival.
			//
			// A transformation on its own is fine and generates with no eventbus in
			// sight; a store on its own is fine too. What is NOT fine is a store that
			// claims a source with no delivery behind it. Fail loudly instead.
			return nil, fmt.Errorf("store source type %q resolves to no EventBus topic: "+
				"the generated store would never ingest a row. Declare delivery for %q "+
				"(a spec.record/spec.event with a producer surface, so a topic is "+
				"synthesized), or drop the store's claim on it", sourceFQN, sourceFQN)
		case 1:
			subs = append(subs, subscriptionDataFromTopic(selfPackage, matches[0], "store_derived"))
		default:
			var refs []string
			for _, match := range matches {
				refs = append(refs, match.Ref.Raw)
			}
			return nil, fmt.Errorf("store source type %q matches multiple EventBus topics (%s); add an explicit event_watch topic_ref", sourceFQN, strings.Join(refs, ", "))
		}
	}
	return subs, nil
}

// DeriveStoreSubscriptions discovers EventBus subscriptions that a timeseries
// or hot store should have, based on projection source annotations
// or record-annotated types.
//
// It follows the chain:
//
//	timeseries_table_source → source type FQN
//	→ find EventBus topic (service_event + topic field) that publishes that type
//	→ subscription entry
//
//	record_store: record-annotated type FQN
//	→ find EventBus topic that publishes that type
//	→ subscription entry
//
// This allows store services to auto-subscribe to the event stream that
// carries their projection source types without explicit spec.subscribe.
func DeriveStoreSubscriptions(p *parser.DescriptorParser, selfPackage string) []SubscriptionData {
	registry, err := eventbus.BuildTopicRegistry(allParserFiles(p))
	if err != nil {
		return nil
	}
	subs, err := DeriveStoreSubscriptionsFromRegistry(p, selfPackage, registry)
	if err != nil {
		return nil
	}
	return subs
}

func collectStoreSourceFQNs(p *parser.DescriptorParser, selfPackage string) map[string]bool {
	isTimeseriesStore := false
	isRecordStore := false
	storeServiceName := ""
	for _, fd := range p.GetAllFiles() {
		if fd.Package == selfPackage {
			isTimeseriesStore = isTimeseriesStore || fd.TimeseriesStore
			isRecordStore = isRecordStore || fd.RecordStore
			if fd.RecordStore && fd.ServiceName != "" {
				storeServiceName = fd.ServiceName
			}
		}
	}
	if !isTimeseriesStore && !isRecordStore {
		return nil
	}

	sourceFQNs := make(map[string]bool)
	for _, fd := range p.GetAllFiles() {
		for _, msg := range fd.Messages {
			if isTimeseriesStore {
				if ann, ok := msg.Annotations["timeseries_table_source"]; ok && ann.StringValue != "" {
					sourceFQNs[ann.StringValue] = true
				}
			}
			// For record_store: every type with (spec.record) is a source,
			// except a PRIVATE record, which is a source only for the stores
			// its s2s_subscribe grant actually names (see
			// ValidateRecordPrivateAnnotations). Deriving an ingest
			// subscription this store is not granted would wire it to a topic
			// it is fail-closed-denied on at runtime.
			if isRecordStore {
				if _, ok := msg.Annotations["record"]; ok && privateRecordGrantsStore(msg, storeServiceName) {
					sourceFQNs[fd.Package+"."+msg.Name] = true
				}
			}
		}
	}
	return sourceFQNs
}

// privateRecordGrantsStore reports whether the record-store service named
// storeService may derive a store-ingest subscription for msg. A non-private
// record is unrestricted: every store in the build ingests it. A private record
// is a source only for the stores named in its s2s_subscribe.auth_services —
// the grant the private-record validator requires it to declare, and the only
// place a private record says which stores may hold its rows.
func privateRecordGrantsStore(msg parser.MessageDescriptor, storeService string) bool {
	if ann, ok := msg.Annotations["record_private"]; !ok || !ann.BoolValue {
		return true
	}
	surface, ok := msg.Annotations["event_surface"]
	if !ok || len(surface.EventPublishEntries) == 0 {
		return false
	}
	sub := surface.EventPublishEntries[0].MethodS2SSubscribe
	if sub == nil || storeService == "" {
		return false
	}
	for _, name := range sub.AuthServices {
		if name == storeService {
			return true
		}
	}
	return false
}

func subscriptionDataFromTopic(stateRoot string, topic eventbus.Topic, origin string) SubscriptionData {
	return SubscriptionData{
		StateRoot:       stateRoot,
		Source:          topic.Ref.Raw,
		BrokerLabel:     topic.EventBusLabel,
		SourcePackage:   topic.Ref.Package,
		SourceStateRoot: topic.Ref.Message,
		SourceField:     topic.Ref.Field,
		Origin:          origin,
	}
}

func allParserFiles(p *parser.DescriptorParser) []*parser.FileDescriptor {
	var fds []*parser.FileDescriptor
	for _, fd := range p.GetAllFiles() {
		fds = append(fds, fd)
	}
	sort.Slice(fds, func(i, j int) bool {
		if fds[i].Path == fds[j].Path {
			return fds[i].Package < fds[j].Package
		}
		return fds[i].Path < fds[j].Path
	})
	return fds
}

func actorFiles(p *parser.DescriptorParser, protoDir string, protoFiles []string, selfPackage string) []*parser.FileDescriptor {
	seen := make(map[string]bool)
	var fds []*parser.FileDescriptor
	add := func(fd *parser.FileDescriptor) {
		if fd == nil || fd.Path == "" || seen[fd.Path] || !actorOwnsFile(fd, selfPackage) {
			return
		}
		seen[fd.Path] = true
		fds = append(fds, fd)
	}
	for _, protoFile := range protoFiles {
		if protoFile == "" || seen[protoFile] {
			continue
		}
		fd, err := p.GetFileDescriptor(filepath.Join(protoDir, protoFile))
		if err != nil {
			continue
		}
		add(fd)
	}
	for _, fd := range allParserFiles(p) {
		add(fd)
	}
	sort.Slice(fds, func(i, j int) bool {
		if fds[i].Path == fds[j].Path {
			return fds[i].Package < fds[j].Package
		}
		return fds[i].Path < fds[j].Path
	})
	return fds
}

func actorOwnsFile(fd *parser.FileDescriptor, selfPackage string) bool {
	if selfPackage == "" || fd.Package == selfPackage {
		return true
	}
	if fd.ServiceName == selfPackage {
		return true
	}
	for _, svc := range fd.Services {
		if svc.HostLabel == selfPackage {
			return true
		}
	}
	return false
}
