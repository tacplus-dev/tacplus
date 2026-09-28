// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/eventbus"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/service"
)

// accountingSelfPublishRPC returns the proto package, service and RPC that
// publish AccountingRecord onto its own topic — the one RPC that must never be
// accounted for.
//
// Q2 of decisions/2026-08-25-accounting-durable-sink-design.md, decided in
// decisions/2026-08-25-accounting-sink-q2-q7-decided.md as option (b): a host
// of the accounting topic's publish RPC would emit an accounting event for
// handling that publish, which publishes another AccountingRecord, forever.
// The exclusion is deliberately narrow — only this RPC, not the whole broker
// service, so the broker's other RPCs stay accounted. Same shape as the
// SkippedTraceTypes precedent already threaded through this generator.
//
// The names are derived, not hard-coded: the topic comes from the record type
// via boundaries.LookupAccountingRecordTopic, and the service/RPC names come
// from the same eventbus helpers the publish path itself uses, so renaming the
// topic moves the exclusion with it.
func accountingSelfPublishRPC(topic eventbus.Topic) (protoPackage, serviceName, rpcName string) {
	svcFQN := eventbus.PerTopicServiceFullName(topic.Ref)
	full := eventbus.PerTopicPublishFullMethodName(topic.Ref)
	protoPackage = topic.Ref.Package
	serviceName = strings.TrimPrefix(svcFQN, protoPackage+".")
	rpcName = strings.TrimPrefix(full, svcFQN+"/")
	return protoPackage, serviceName, rpcName
}

// excludeAccountingSelfPublish returns accountingMap with the accounting
// topic's own publish RPC removed, when the wrapper being generated is for the
// service that hosts it. Every other wrapper gets the map back unchanged.
//
// topicOK is false when the proto set declares no accounting record at all
// (client repos), in which case there is nothing to exclude.
func excludeAccountingSelfPublish(
	accountingMap map[string]service.AccountingInfo,
	grpcName, protoPackage, serviceName string,
	topic eventbus.Topic,
	topicOK bool,
) map[string]service.AccountingInfo {
	if !topicOK {
		return accountingMap
	}
	wantPkg, wantSvc, wantRPC := accountingSelfPublishRPC(topic)
	if protoPackage != wantPkg || serviceName != wantSvc {
		return accountingMap
	}
	info, ok := accountingMap[grpcName]
	if !ok || !info.RPCs[wantRPC] {
		return accountingMap
	}
	rpcs := make(map[string]bool, len(info.RPCs))
	for name, on := range info.RPCs {
		if name == wantRPC {
			continue
		}
		rpcs[name] = on
	}
	out := make(map[string]service.AccountingInfo, len(accountingMap))
	for k, v := range accountingMap {
		out[k] = v
	}
	out[grpcName] = service.AccountingInfo{EnumName: info.EnumName, RPCs: rpcs, RPCOrdinals: info.RPCOrdinals}
	return out
}
