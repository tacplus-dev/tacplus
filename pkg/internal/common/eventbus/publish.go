// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package eventbus

const typedServiceName = "EventBusTypedService"

// TypedServiceFullName returns the broker-qualified typed EventBus service name.
func TypedServiceFullName(brokerTag string) string {
	if brokerTag == "" {
		brokerTag = EventBusLabel
	}
	return brokerTag + "." + typedServiceName
}

// PerTopicServiceFullName returns the per-topic gRPC service name for a topic:
// "<package>.<Message>StateService".
//
// This is the convention the generated PROTO declares, and therefore the only
// one a descriptor-driven caller -- grpcurl, verify-live, anything using
// reflection -- can reach. The flat TypedServiceFullName above is declared in no
// proto in the tree. See control/EVENTBUS-PUBLISH-API-SPLIT.md.
//
// The package comes from the topic_ref, which names the proto DECLARING the
// state message. The broker derives the same name from its own model; the two
// disagreed once (it used the event type's package instead) and produced a
// service nothing declared -- eventbus 25d2863.
func PerTopicServiceFullName(ref TopicRef) string {
	return ref.Package + "." + ref.Message + "StateService"
}

// PerTopicPublishFullMethodName returns "<pkg>.<Message>StateService/PublishTo<Field>".
func PerTopicPublishFullMethodName(ref TopicRef) string {
	return PerTopicServiceFullName(ref) + "/PublishTo" + pascalField(ref.Field)
}

// PerTopicPublishPath returns the grpc.ClientConnInterface path for the
// per-topic publish method.
func PerTopicPublishPath(ref TopicRef) string {
	return "/" + PerTopicPublishFullMethodName(ref)
}

// PerTopicSubscribeFullMethodName returns "<pkg>.<Message>StateService/SubscribeTo<Field>".
func PerTopicSubscribeFullMethodName(ref TopicRef) string {
	return PerTopicServiceFullName(ref) + "/SubscribeTo" + pascalField(ref.Field)
}

// PerTopicSubscribePath returns the grpc.ClientConnInterface path for the
// per-topic subscribe method.
func PerTopicSubscribePath(ref TopicRef) string {
	return "/" + PerTopicSubscribeFullMethodName(ref)
}

// pascalField converts a proto field name ("trace_events") to the PascalCase
// form used in the RPC name ("TraceEvents").
func pascalField(field string) string {
	var b []rune
	upper := true
	for _, r := range field {
		if r == '_' {
			upper = true
			continue
		}
		if upper && r >= 'a' && r <= 'z' {
			r = r - 'a' + 'A'
		}
		upper = false
		b = append(b, r)
	}
	return string(b)
}

// PublishFullMethodName returns the canonical gRPC full method name for an
// EventBus publish RPC stem.
func PublishFullMethodName(rpcStem string) string {
	return PublishFullMethodNameForBroker(EventBusLabel, rpcStem)
}

// PublishFullMethodNameForBroker returns the canonical gRPC full method name
// for an EventBus publish RPC stem on the declared broker tag.
func PublishFullMethodNameForBroker(brokerTag, rpcStem string) string {
	return TypedServiceFullName(brokerTag) + "/Publish" + rpcStem
}

// PublishPath returns the canonical grpc.ClientConnInterface path for an
// EventBus publish RPC stem.
func PublishPath(rpcStem string) string {
	return PublishPathForBroker(EventBusLabel, rpcStem)
}

// PublishPathForBroker returns the canonical grpc.ClientConnInterface path for
// an EventBus publish RPC stem on the declared broker tag.
func PublishPathForBroker(brokerTag, rpcStem string) string {
	return "/" + PublishFullMethodNameForBroker(brokerTag, rpcStem)
}

// SubscribeFullMethodName returns the canonical gRPC full method name for an
// EventBus subscribe RPC stem.
func SubscribeFullMethodName(rpcStem string) string {
	return SubscribeFullMethodNameForBroker(EventBusLabel, rpcStem)
}

// SubscribeFullMethodNameForBroker returns the canonical gRPC full method name
// for an EventBus subscribe RPC stem on the declared broker tag.
func SubscribeFullMethodNameForBroker(brokerTag, rpcStem string) string {
	return TypedServiceFullName(brokerTag) + "/Subscribe" + rpcStem
}

// SubscribePath returns the canonical grpc.ClientConnInterface path for an
// EventBus subscribe RPC stem.
func SubscribePath(rpcStem string) string {
	return SubscribePathForBroker(EventBusLabel, rpcStem)
}

// SubscribePathForBroker returns the canonical grpc.ClientConnInterface path
// for an EventBus subscribe RPC stem on the declared broker tag.
func SubscribePathForBroker(brokerTag, rpcStem string) string {
	return "/" + SubscribeFullMethodNameForBroker(brokerTag, rpcStem)
}

// PerTopicSubscribeParts returns the (service, method) pair a subscriber must
// call for a topic, given the source package / state root / field a
// SubscriptionData already carries.
//
// Kept here beside the publish helpers so the five language generators cannot
// each assemble the name their own way — five independent spellings of one wire
// contract is how the flat/per-topic split arose in the first place.
func PerTopicSubscribeParts(sourcePackage, sourceStateRoot, sourceField string) (service, method string) {
	ref := TopicRef{Package: sourcePackage, Message: sourceStateRoot, Field: sourceField}
	return PerTopicServiceFullName(ref), "SubscribeTo" + pascalField(sourceField)
}

// PerTopicServiceName returns the bare per-topic service name ("<Message>StateService"),
// unqualified by package. Grants carry package separately.
func PerTopicServiceName(ref TopicRef) string {
	return ref.Message + "StateService"
}

// PerTopicPublishRPC returns the per-topic publish RPC name ("PublishTo<Field>").
func PerTopicPublishRPC(ref TopicRef) string {
	return "PublishTo" + pascalField(ref.Field)
}

// PerTopicSubscribeRPC returns the per-topic subscribe RPC name ("SubscribeTo<Field>").
func PerTopicSubscribeRPC(ref TopicRef) string {
	return "SubscribeTo" + pascalField(ref.Field)
}
