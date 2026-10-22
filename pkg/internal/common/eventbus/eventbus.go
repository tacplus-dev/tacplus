// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package eventbus

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
)

const EventBusLabel = "eventbus"

var protoIdentRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// TopicRef is the canonical identity for an EventBus topic:
// <proto-package>.<topic-message>.<topic-field>.
type TopicRef struct {
	Raw     string
	Package string
	Message string
	Field   string
}

// Topic is language-neutral metadata for one declared EventBus topic.
type Topic struct {
	Ref           TopicRef
	ProtoFile     string
	EventTypeFQN  string
	EventBusLabel string
	Export        bool
	RPCStem       string
	LegacyRPCStem string
	// PublishC2S/PublishS2S are the producer surface declared on the event/record
	// message (via the event_surface annotation). event_publish bindings that omit
	// their own auth inherit this surface — the surface is declared once on the
	// event, not repeated on every producer. Exactly one is non-nil when declared.
	PublishC2S *model.EventBusMethodC2S
	PublishS2S *model.EventBusMethodS2S
}

// BindingKind identifies the derived EventBus RPC family.
type BindingKind string

const (
	BindingPublish BindingKind = "publish"
	BindingWatch   BindingKind = "watch"
)

// Binding is a validated event_publish or event_watch annotation.
type Binding struct {
	Kind         BindingKind
	ActorPackage string
	ActorMessage string
	Topic        Topic
	MethodS2S    *model.EventBusMethodS2S
	MethodC2S    *model.EventBusMethodC2S
}

// TopicRegistry indexes all eventbus-tagged topic fields loaded for a build.
type TopicRegistry struct {
	topics map[string]Topic
}

// ParseTopicRef parses and validates a canonical topic ref. It parses from the
// right so dotted proto package names are supported.
func ParseTopicRef(raw string) (TopicRef, error) {
	ref := strings.TrimSpace(raw)
	if ref == "" {
		return TopicRef{}, fmt.Errorf("empty topic_ref")
	}
	parts := strings.Split(ref, ".")
	if len(parts) < 3 {
		return TopicRef{}, fmt.Errorf("topic_ref %q must have format <proto-package>.<topic-message>.<topic-field>", raw)
	}
	msg := parts[len(parts)-2]
	field := parts[len(parts)-1]
	pkgParts := parts[:len(parts)-2]
	for _, part := range pkgParts {
		if !protoIdentRE.MatchString(part) {
			return TopicRef{}, fmt.Errorf("topic_ref %q has invalid proto package segment %q", raw, part)
		}
	}
	if !protoIdentRE.MatchString(msg) {
		return TopicRef{}, fmt.Errorf("topic_ref %q has invalid topic message %q", raw, msg)
	}
	if !protoIdentRE.MatchString(field) {
		return TopicRef{}, fmt.Errorf("topic_ref %q has invalid topic field %q", raw, field)
	}
	return TopicRef{
		Raw:     strings.Join(append(append([]string{}, pkgParts...), msg, field), "."),
		Package: strings.Join(pkgParts, "."),
		Message: msg,
		Field:   field,
	}, nil
}

// BuildTopicRegistry collects all (spec.service_event) messages and their
// (spec.topic) fields into a validated registry. Each topic's declared broker
// tag must match a loaded gRPC service identity so retagged brokers fail loud
// instead of producing hard-coded EventBus clients.
func BuildTopicRegistry(fds []*parser.FileDescriptor) (*TopicRegistry, error) {
	reg := &TopicRegistry{topics: make(map[string]Topic)}
	rpcStems := make(map[string]string)
	brokerLabels := parser.CollectServingLabels(fds)
	surfaceByFQN := collectEventSurfaces(fds)
	for _, fd := range sortedFiles(fds) {
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			labelAnn, isEvent := msg.Annotations["service_event_label"]
			if !isEvent || labelAnn.StringValue == "" {
				continue
			}
			if !brokerLabels[labelAnn.StringValue] {
				return nil, fmt.Errorf("EventBus topic %s.%s declares broker label %q but no matching broker service exists", fd.Package, msg.Name, labelAnn.StringValue)
			}
			export := false
			if ann, ok := msg.Annotations["service_state_export"]; ok {
				export = ann.BoolValue
			}
			for _, field := range msg.Fields {
				if !field.Annotations["topic"].BoolValue {
					continue
				}
				raw := fd.Package + "." + msg.Name + "." + field.Name
				ref, err := ParseTopicRef(raw)
				if err != nil {
					return nil, err
				}
				if prev, exists := reg.topics[ref.Raw]; exists {
					return nil, fmt.Errorf("duplicate EventBus topic_ref %q in %s and %s", ref.Raw, prev.ProtoFile, fd.Path)
				}
				topic := Topic{
					Ref:           ref,
					ProtoFile:     fd.Path,
					EventTypeFQN:  field.Type,
					EventBusLabel: labelAnn.StringValue,
					Export:        export,
					RPCStem:       topicRPCStem(ref, true),
					LegacyRPCStem: topicRPCStem(ref, false),
				}
				if surface, ok := surfaceByFQN[field.Type]; ok {
					topic.PublishC2S = surface.c2s
					topic.PublishS2S = surface.s2s
				}
				if prevRef, exists := rpcStems[topic.RPCStem]; exists {
					return nil, fmt.Errorf("EventBus RPC stem %q collides for topic refs %q and %q", topic.RPCStem, prevRef, ref.Raw)
				}
				rpcStems[topic.RPCStem] = ref.Raw
				reg.topics[ref.Raw] = topic
			}
		}
	}
	return reg, nil
}

// eventSurface is the producer surface declared on an event/record message.
type eventSurface struct {
	c2s *model.EventBusMethodC2S
	s2s *model.EventBusMethodS2S
}

// collectEventSurfaces maps each event/record message's fully-qualified name to
// its declared producer surface (from the event_surface annotation). Topics
// resolve their EventTypeFQN against this so auth-less event_publish bindings can
// inherit the surface.
func collectEventSurfaces(fds []*parser.FileDescriptor) map[string]eventSurface {
	surfaces := make(map[string]eventSurface)
	for _, fd := range fds {
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			ann, ok := msg.Annotations["event_surface"]
			if !ok || len(ann.EventPublishEntries) == 0 {
				continue
			}
			entry := ann.EventPublishEntries[0]
			fqn := fd.Package + "." + msg.Name
			surfaces[fqn] = eventSurface{c2s: entry.MethodC2S, s2s: entry.MethodS2S}
		}
	}
	return surfaces
}

// Resolve validates and resolves a topic_ref against this registry.
func (r *TopicRegistry) Resolve(raw string) (Topic, error) {
	ref, err := ParseTopicRef(raw)
	if err != nil {
		return Topic{}, err
	}
	topic, ok := r.topics[ref.Raw]
	if !ok {
		return Topic{}, fmt.Errorf("topic_ref %q does not resolve to an eventbus topic", ref.Raw)
	}
	return topic, nil
}

// Topics returns all topics in stable order.
func (r *TopicRegistry) Topics() []Topic {
	if r == nil {
		return nil
	}
	topics := make([]Topic, 0, len(r.topics))
	for _, topic := range r.topics {
		topics = append(topics, topic)
	}
	sort.Slice(topics, func(i, j int) bool {
		return topics[i].Ref.Raw < topics[j].Ref.Raw
	})
	return topics
}

// ValidateBinding validates a parsed event_publish/event_watch annotation and
// resolves it to a declared topic. Exactly one standard method auth shape must
// be present — either declared on the binding itself, or, for a publish binding
// that omits its auth, inherited from the topic's producer surface (the c2s/s2s
// declared on the event/record message). This lets a producer write just
// `event_publish { topic_ref: ... }` and inherit the single surface declared on
// the event. event_watch must still carry its own (subscribe) auth.
func (r *TopicRegistry) ValidateBinding(kind BindingKind, actorPackage, actorMessage string, entry model.EventBusBindingEntry) (Binding, error) {
	topic, err := r.Resolve(entry.TopicRef)
	if err != nil {
		return Binding{}, err
	}
	methodS2S := entry.MethodS2S
	methodC2S := entry.MethodC2S
	if kind == BindingPublish && methodS2S == nil && methodC2S == nil {
		// Inherit the producer surface declared on the event/record message.
		methodS2S = topic.PublishS2S
		methodC2S = topic.PublishC2S
	}
	hasS2S := methodS2S != nil
	hasC2S := methodC2S != nil
	if hasS2S == hasC2S {
		return Binding{}, fmt.Errorf("event_%s %q on %s.%s must carry exactly one standard method auth shape (declare it on the binding, or for a publish binding on the event/record producer surface)", kind, topic.Ref.Raw, actorPackage, actorMessage)
	}
	return Binding{
		Kind:         kind,
		ActorPackage: actorPackage,
		ActorMessage: actorMessage,
		Topic:        topic,
		MethodS2S:    methodS2S,
		MethodC2S:    methodC2S,
	}, nil
}

// TopicRefsForEventType returns all topics that publish eventTypeFQN.
func (r *TopicRegistry) TopicRefsForEventType(eventTypeFQN string) []Topic {
	if r == nil || eventTypeFQN == "" {
		return nil
	}
	var matches []Topic
	for _, topic := range r.topics {
		if topic.EventTypeFQN == eventTypeFQN {
			matches = append(matches, topic)
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].Ref.Raw < matches[j].Ref.Raw
	})
	return matches
}

func topicRPCStem(ref TopicRef, includePackage bool) string {
	if includePackage {
		return RPCStemForTopicParts(ref.Package, ref.Message, ref.Field)
	}
	parts := []string{}
	parts = append(parts, ref.Message, ref.Field)
	var b strings.Builder
	for _, part := range parts {
		b.WriteString(strutil.ToPascalCase(part))
	}
	return b.String()
}

// RPCStemForTopicParts returns the canonical package-qualified RPC suffix for a
// topic, e.g. telemetry.AgenticCodeEventBus.event_stream becomes
// TelemetryAgenticCodeEventBusEventStream.
func RPCStemForTopicParts(protoPackage, topicMessage, topicField string) string {
	parts := []string{}
	if protoPackage != "" {
		parts = append(parts, strings.Split(protoPackage, ".")...)
	}
	parts = append(parts, topicMessage, topicField)
	var b strings.Builder
	for _, part := range parts {
		b.WriteString(strutil.ToPascalCase(part))
	}
	return b.String()
}

func sortedFiles(fds []*parser.FileDescriptor) []*parser.FileDescriptor {
	out := append([]*parser.FileDescriptor(nil), fds...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Path == out[j].Path {
			return out[i].Package < out[j].Package
		}
		return out[i].Path < out[j].Path
	})
	return out
}
