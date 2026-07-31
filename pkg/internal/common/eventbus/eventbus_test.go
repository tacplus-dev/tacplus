// Copyright (c) 2026 Second Star Cavalier LLC

package eventbus

import (
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

func TestParseTopicRefSupportsDottedPackages(t *testing.T) {
	ref, err := ParseTopicRef("company.sensor.AgenticCodeEventBus.event_stream")
	if err != nil {
		t.Fatalf("ParseTopicRef returned error: %v", err)
	}
	if ref.Package != "company.sensor" || ref.Message != "AgenticCodeEventBus" || ref.Field != "event_stream" {
		t.Fatalf("unexpected ref: %#v", ref)
	}
}

func TestBuildTopicRegistryAllowsLegacyStemCollisionWithCanonicalNames(t *testing.T) {
	reg, err := BuildTopicRegistry([]*parser.FileDescriptor{
		brokerFile(EventBusTag),
		topicFile("alpha", "SharedEventBus", "event_stream", "alpha.Event"),
		topicFile("beta", "SharedEventBus", "event_stream", "beta.Event"),
	})
	if err != nil {
		t.Fatalf("BuildTopicRegistry returned error: %v", err)
	}
	alpha, err := reg.Resolve("alpha.SharedEventBus.event_stream")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if alpha.RPCStem != "AlphaSharedEventBusEventStream" {
		t.Fatalf("unexpected canonical stem %q", alpha.RPCStem)
	}
	beta, err := reg.Resolve("beta.SharedEventBus.event_stream")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if beta.RPCStem != "BetaSharedEventBusEventStream" {
		t.Fatalf("unexpected canonical stem %q", beta.RPCStem)
	}
}

func TestValidateBindingRequiresExactlyOneAuthShape(t *testing.T) {
	reg, err := BuildTopicRegistry([]*parser.FileDescriptor{
		brokerFile(EventBusTag),
		topicFile("alpha", "SharedEventBus", "event_stream", "alpha.Event"),
	})
	if err != nil {
		t.Fatalf("BuildTopicRegistry returned error: %v", err)
	}

	entry := model.EventBusBindingEntry{TopicRef: "alpha.SharedEventBus.event_stream"}
	if _, err := reg.ValidateBinding(BindingWatch, "alpha", "Actor", entry); err == nil {
		t.Fatal("expected missing auth shape error")
	}

	entry.MethodS2S = &model.EventBusMethodS2S{}
	entry.MethodC2S = &model.EventBusMethodC2S{}
	if _, err := reg.ValidateBinding(BindingWatch, "alpha", "Actor", entry); err == nil {
		t.Fatal("expected multiple auth shapes error")
	}

	entry.MethodC2S = nil
	if _, err := reg.ValidateBinding(BindingWatch, "alpha", "Actor", entry); err != nil {
		t.Fatalf("expected one auth shape to validate, got %v", err)
	}
}

func TestBuildTopicRegistryUsesDeclaredBrokerTag(t *testing.T) {
	reg, err := BuildTopicRegistry([]*parser.FileDescriptor{
		brokerFile("events"),
		topicFileWithTag("alpha", "SharedEventBus", "event_stream", "alpha.Event", "events"),
	})
	if err != nil {
		t.Fatalf("BuildTopicRegistry returned error: %v", err)
	}
	topic, err := reg.Resolve("alpha.SharedEventBus.event_stream")
	if err != nil {
		t.Fatalf("Resolve returned error: %v", err)
	}
	if topic.EventBusTag != "events" {
		t.Fatalf("topic EventBusTag = %q, want events", topic.EventBusTag)
	}
	if got := PublishPathForBroker(topic.EventBusTag, topic.RPCStem); got != "/events.EventBusTypedService/PublishAlphaSharedEventBusEventStream" {
		t.Fatalf("PublishPathForBroker = %q", got)
	}
	if got := SubscribePathForBroker(topic.EventBusTag, topic.RPCStem); got != "/events.EventBusTypedService/SubscribeAlphaSharedEventBusEventStream" {
		t.Fatalf("SubscribePathForBroker = %q", got)
	}
}

func TestBuildTopicRegistryRejectsMissingBrokerTag(t *testing.T) {
	_, err := BuildTopicRegistry([]*parser.FileDescriptor{
		brokerFile("other"),
		topicFileWithTag("alpha", "SharedEventBus", "event_stream", "alpha.Event", "events"),
	})
	if err == nil {
		t.Fatal("expected missing broker tag error")
	}
}

func topicFile(pkg, msg, field, eventType string) *parser.FileDescriptor {
	return topicFileWithTag(pkg, msg, field, eventType, EventBusTag)
}

func topicFileWithTag(pkg, msg, field, eventType, tag string) *parser.FileDescriptor {
	return &parser.FileDescriptor{
		Path:    pkg + ".proto",
		Package: pkg,
		Messages: []parser.MessageDescriptor{
			{
				Name: msg,
				Annotations: map[string]model.AnnotationValue{
					"service_event_tag": {StringValue: tag},
				},
				Fields: []parser.FieldDescriptor{
					{
						Name:     field,
						Type:     eventType,
						TypeKind: "message",
						Annotations: map[string]model.AnnotationValue{
							"topic": {BoolValue: true},
						},
					},
				},
			},
		},
	}
}

func brokerFile(tag string) *parser.FileDescriptor {
	return &parser.FileDescriptor{
		Path:    tag + ".proto",
		Package: tag,
		Services: []parser.ServiceDescriptor{
			{
				Name:     "EventBusTypedService",
				GrpcName: tag,
				IsS2S:    true,
			},
		},
	}
}
