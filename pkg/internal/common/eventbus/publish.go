// Copyright (c) 2026 Second Star Cavalier LLC

package eventbus

const typedServiceName = "EventBusTypedService"

// TypedServiceFullName returns the broker-qualified typed EventBus service name.
func TypedServiceFullName(brokerTag string) string {
	if brokerTag == "" {
		brokerTag = EventBusTag
	}
	return brokerTag + "." + typedServiceName
}

// PublishFullMethodName returns the canonical gRPC full method name for an
// EventBus publish RPC stem.
func PublishFullMethodName(rpcStem string) string {
	return PublishFullMethodNameForBroker(EventBusTag, rpcStem)
}

// PublishFullMethodNameForBroker returns the canonical gRPC full method name
// for an EventBus publish RPC stem on the declared broker tag.
func PublishFullMethodNameForBroker(brokerTag, rpcStem string) string {
	return TypedServiceFullName(brokerTag) + "/Publish" + rpcStem
}

// PublishPath returns the canonical grpc.ClientConnInterface path for an
// EventBus publish RPC stem.
func PublishPath(rpcStem string) string {
	return PublishPathForBroker(EventBusTag, rpcStem)
}

// PublishPathForBroker returns the canonical grpc.ClientConnInterface path for
// an EventBus publish RPC stem on the declared broker tag.
func PublishPathForBroker(brokerTag, rpcStem string) string {
	return "/" + PublishFullMethodNameForBroker(brokerTag, rpcStem)
}

// SubscribeFullMethodName returns the canonical gRPC full method name for an
// EventBus subscribe RPC stem.
func SubscribeFullMethodName(rpcStem string) string {
	return SubscribeFullMethodNameForBroker(EventBusTag, rpcStem)
}

// SubscribeFullMethodNameForBroker returns the canonical gRPC full method name
// for an EventBus subscribe RPC stem on the declared broker tag.
func SubscribeFullMethodNameForBroker(brokerTag, rpcStem string) string {
	return TypedServiceFullName(brokerTag) + "/Subscribe" + rpcStem
}

// SubscribePath returns the canonical grpc.ClientConnInterface path for an
// EventBus subscribe RPC stem.
func SubscribePath(rpcStem string) string {
	return SubscribePathForBroker(EventBusTag, rpcStem)
}

// SubscribePathForBroker returns the canonical grpc.ClientConnInterface path
// for an EventBus subscribe RPC stem on the declared broker tag.
func SubscribePathForBroker(brokerTag, rpcStem string) string {
	return "/" + SubscribeFullMethodNameForBroker(brokerTag, rpcStem)
}
