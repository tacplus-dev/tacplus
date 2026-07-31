// Copyright (c) 2026 Second Star Cavalier LLC

package java

import (
	"fmt"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/boundaries"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
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
		TargetService:     publisher.BrokerTag,
		TypeName:          event.TypeName,
		TypeImport:        event.TypeImport,
		TypeOuterClass:    event.TypeOuterClass,
		PublishFullMethod: publisher.PublishFullMethod,
	}
}
