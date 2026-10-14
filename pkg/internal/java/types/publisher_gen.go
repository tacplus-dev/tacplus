// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/boundaries"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/eventbus"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
)

// javaPublisherEntry holds per-topic EventBus publisher data for
// ServiceManager.java.
type javaPublisherEntry struct {
	MethodName        string
	ResolveLoopName   string
	LoopName          string
	StreamName        string
	ThreadName        string
	Label             string
	TargetService     string
	TypeName          string
	TypeImport        string
	TypeOuterClass    string
	PublishFullMethod string
}

// collectJavaAccountingPublisher returns the publisher entry for the durable
// accounting record topic, if the proto set declares one.
//
// This is the one publisher that is NOT declared per-actor. Accounting is a
// chassis concern: every service emits it, so requiring an event_publish on
// every service's Config would be the same declaration copied N times, and a
// service that forgot it would silently produce no audit trail. The producer
// surface is already declared once, on the record itself
// (s2s: {auth_services: ["*"]}), so the generator resolves the topic from the
// record type instead — mirrors pkg/internal/go/types/publisher_gen.go's
// collectGoAccountingPublisher (mechanical port, per the design doc's
// B-rest note).
func collectJavaAccountingPublisher(config *tactype.Config, protoDir string) (*javaPublisherEntry, error) {
	if protoDir == "" {
		return nil, nil
	}
	p, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return nil, fmt.Errorf("create descriptor parser for accounting publisher detection: %w", err)
	}
	topic, ok, err := boundaries.LookupAccountingRecordTopic(p)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, nil
	}
	publisher := boundaries.EventPublisherData{
		TopicRef:          topic.Ref.Raw,
		BrokerLabel:       topic.EventBusLabel,
		EventTypeFQN:      topic.EventTypeFQN,
		EventTypeName:     eventTypeNameFromFQN(topic.EventTypeFQN),
		RPCStem:           topic.RPCStem,
		PublishStreamName: "Publish" + topic.RPCStem,
		PublishFullMethod: eventbus.PerTopicPublishFullMethodName(topic.Ref),
	}
	entry := javaPublisherEntryFromData(config, protoDir, publisher)
	if entry.TypeName == "" {
		return nil, nil
	}
	return &entry, nil
}

// eventTypeNameFromFQN returns the bare message name from a fully-qualified
// proto type name, e.g. "accountingrecord.AccountingRecord" -> "AccountingRecord".
func eventTypeNameFromFQN(fqn string) string {
	if idx := strings.LastIndex(fqn, "."); idx >= 0 {
		return fqn[idx+1:]
	}
	return fqn
}

func collectJavaEventPublishers(config *tactype.Config, protoDir string) ([]javaPublisherEntry, []string, error) {
	if protoDir == "" {
		return nil, nil, nil
	}
	p, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return nil, nil, fmt.Errorf("create descriptor parser for publisher detection: %w", err)
	}
	publishers, err := boundaries.CollectActorEventPublisherData(p, protoDir, eventPublishProtoFiles(config), config.SelfPackage)
	if err != nil {
		return nil, nil, err
	}

	seenMethods := make(map[string]string)
	seenImports := make(map[string]bool)
	entries := make([]javaPublisherEntry, 0, len(publishers))
	var imports []string
	for _, publisher := range publishers {
		entry := javaPublisherEntryFromData(config, protoDir, publisher)
		if entry.TypeName == "" {
			continue
		}
		if prev, ok := seenMethods[entry.MethodName]; ok {
			return nil, nil, fmt.Errorf("duplicate EventBus publisher helper %q for %s and %s", entry.MethodName, prev, entry.Label)
		}
		seenMethods[entry.MethodName] = entry.Label
		entries = append(entries, entry)
		if entry.TypeImport != "" && !seenImports[entry.TypeImport] {
			seenImports[entry.TypeImport] = true
			imports = append(imports, entry.TypeImport)
		}
	}
	return entries, imports, nil
}

func javaPublisherEntryFromData(config *tactype.Config, protoDir string, publisher boundaries.EventPublisherData) javaPublisherEntry {
	typePkg := config.SelfPackage
	if idx := strings.LastIndex(publisher.EventTypeFQN, "."); idx >= 0 {
		typePkg = publisher.EventTypeFQN[:idx]
	}
	event := resolveJavaSubscriptionEvent(config, protoDir, tactype.SubscriptionEntry{
		SourcePackage:  typePkg,
		SourceTypeFqn:  publisher.EventTypeFQN,
		SourceProtoDir: "services",
	})
	prefix := strutil.ToLowerCamelCase(publisher.RPCStem)
	return javaPublisherEntry{
		MethodName:        "create" + publisher.RPCStem + "Publisher",
		ResolveLoopName:   prefix + "PublishResolveLoop",
		LoopName:          prefix + "PublishLoop",
		StreamName:        prefix + "PublishStream",
		ThreadName:        "publisher-" + strutil.ToKebabCase(publisher.RPCStem),
		Label:             publisher.TopicRef + " publisher",
		TargetService:     publisher.BrokerLabel,
		TypeName:          event.TypeName,
		TypeImport:        event.TypeImport,
		TypeOuterClass:    event.TypeOuterClass,
		PublishFullMethod: publisher.PublishFullMethod,
	}
}
