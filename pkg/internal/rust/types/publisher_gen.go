// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/boundaries"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/eventbus"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// publisherEntry holds per-topic data for the service_manager template's
// model-driven EventBus publisher generation.
type publisherEntry struct {
	FnName        string // e.g. "create_gitlabconnector_git_lab_connector_event_bus_event_stream_publisher"
	LoopFn        string // e.g. "gitlabconnector_git_lab_connector_event_bus_event_stream_publish_loop"
	StreamFn      string // e.g. "gitlabconnector_git_lab_connector_event_bus_event_stream_publish_stream"
	VarPrefix     string // e.g. "gitlabconnector_git_lab_connector_event_bus_event_stream" — local var prefix
	Label         string // e.g. "gitlabconnector.GitLabConnectorEventBus.event_stream publisher"
	TargetService string // service_id of the declared broker
	EventTypeFQN  string // e.g. "trace.TraceEvent"
	EventTypeName string // e.g. "GitLabConnectorEvent"
	EventTypePath string // Rust path, e.g. "crate::types::vservices::gitlabconnectorsvc::GitLabConnectorEvent"
	PublishPath   string // gRPC path, e.g. "/eventbus.EventBusTypedService/PublishGitlabconnectorGitLabConnectorEventBusEventStream"
}

// collectEventPublishers discovers explicit event_publish declarations for this
// actor. Publisher helpers are generated only from the model; there is no
// service-name fallback table.
func collectEventPublishers(config *tactype.Config, protoDir string) ([]publisherEntry, error) {
	if protoDir == "" {
		protoDir = config.FlatProtoDir
	}
	if protoDir == "" {
		return nil, nil
	}
	p, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return nil, fmt.Errorf("create descriptor parser for publisher detection: %w", err)
	}

	seen := make(map[string]bool)
	var protoFiles []string
	for _, state := range config.States {
		if !seen[state.ProtoFile] {
			seen[state.ProtoFile] = true
			protoFiles = append(protoFiles, state.ProtoFile)
		}
	}
	for _, grpc := range config.GrpcServers {
		if !seen[grpc.ProtoFile] {
			seen[grpc.ProtoFile] = true
			protoFiles = append(protoFiles, grpc.ProtoFile)
		}
	}

	bindings, err := boundaries.CollectActorEventPublishes(p, protoDir, protoFiles, config.SelfPackage)
	if err != nil {
		return nil, err
	}

	var result []publisherEntry
	seenFns := make(map[string]string)
	for _, binding := range bindings {
		if binding.MethodS2S == nil {
			continue
		}
		entry := explicitPublisherEntry(p, binding.Topic)
		if entry.EventTypeName == "" {
			continue
		}
		if err := appendPublisherEntry(&result, seenFns, entry); err != nil {
			return nil, err
		}
	}

	return result, nil
}

// collectAccountingPublisher returns the publisher entry for the accounting
// record topic, if the proto set declares one.
//
// This is the one publisher that is NOT declared per-actor. Accounting is a
// chassis concern: every service emits it, so requiring an event_publish on
// every service's Config would be the same declaration copied N times, and a
// service that forgot it would silently produce no audit trail. The producer
// surface is already declared once, on the record itself
// (s2s: {auth_services: ["*"]}), so the generator resolves the topic from the
// record type instead.
func collectAccountingPublisher(config *tactype.Config, protoDir string) (*publisherEntry, error) {
	if protoDir == "" {
		protoDir = config.FlatProtoDir
	}
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
	entry := explicitPublisherEntry(p, topic)
	if entry.EventTypeName == "" {
		return nil, nil
	}
	return &entry, nil
}

func explicitPublisherEntry(p *parser.DescriptorParser, topic eventbus.Topic) publisherEntry {
	topicSnake := strutil.ToSnakeCase(topic.RPCStem)
	return publisherEntryForTopic(topic, topicSnake, topic.RPCStem, rustEventTypePath(p, topic))
}

func publisherEntryForTopic(topic eventbus.Topic, prefix, rpcStem, eventTypePath string) publisherEntry {
	eventType := simpleTypeName(topic.EventTypeFQN)
	return publisherEntry{
		FnName:        "create_" + prefix + "_publisher",
		LoopFn:        prefix + "_publish_loop",
		StreamFn:      prefix + "_publish_stream",
		VarPrefix:     prefix,
		Label:         topic.Ref.Raw + " publisher",
		TargetService: topic.EventBusLabel,
		EventTypeFQN:  topic.EventTypeFQN,
		EventTypeName: eventType,
		EventTypePath: eventTypePath,
		// Per-topic. This is the SECOND place a publish path is built —
		// boundaries.go is the other — and switching only that one left the rust
		// publisher still emitting the flat name into s2sm/mod.rs while every
		// gate stayed green. See control/EVENTBUS-PUBLISH-API-SPLIT.md.
		PublishPath: eventbus.PerTopicPublishPath(topic.Ref),
	}
}

func appendPublisherEntry(result *[]publisherEntry, seenFns map[string]string, entry publisherEntry) error {
	if prev, ok := seenFns[entry.FnName]; ok {
		return fmt.Errorf("duplicate EventBus publisher helper %q for %s and %s", entry.FnName, prev, entry.Label)
	}
	seenFns[entry.FnName] = entry.Label
	*result = append(*result, entry)
	return nil
}

// firstPathSegment returns the first directory segment of a relative proto path
// (e.g. "vservices/gitlab-connector.proto" → "vservices").
func firstPathSegment(relPath string) string {
	rel := filepath.ToSlash(relPath)
	if idx := strings.Index(rel, "/"); idx >= 0 {
		return rel[:idx]
	}
	return ""
}

// rustParentMod converts a proto parent directory to its Rust module segment,
// matching the convention used for crate::types::<parent>::<pkg> paths.
func rustParentMod(parentDir string) string {
	return strings.ReplaceAll(strings.ReplaceAll(parentDir, "-", "_"), "/", "::")
}

func rustEventTypePath(p *parser.DescriptorParser, topic eventbus.Topic) string {
	typePkg := topic.Ref.Package
	typeName := topic.EventTypeFQN
	if idx := strings.LastIndex(typeName, "."); idx >= 0 {
		typePkg = typeName[:idx]
		typeName = typeName[idx+1:]
	}
	parentDir := firstPathSegment(topic.ProtoFile)
	for relPath, fd := range p.GetAllFiles() {
		if fd.Package == typePkg {
			parentDir = firstPathSegment(relPath)
			break
		}
	}
	parts := []string{"crate::types"}
	if mod := rustParentMod(parentDir); mod != "" {
		parts = append(parts, mod)
	}
	parts = append(parts, typePkg, typeName)
	return strings.Join(parts, "::")
}
