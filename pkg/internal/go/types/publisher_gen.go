// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"path/filepath"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/boundaries"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/eventbus"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
)

// PublisherTemplateEntry holds per-topic EventBus publisher data for the Go
// service_manager template.
type PublisherTemplateEntry struct {
	MethodName        string
	LoopName          string
	StreamName        string
	Label             string
	TargetService     string
	EventTypeName     string
	TypePkgAlias      string
	TypePkgImport     string
	PublishStreamName string
	PublishPath       string
}

func collectGoEventPublishers(config *tactype.Config, protoDir string, pkgToDirMap map[string]string) ([]PublisherTemplateEntry, error) {
	if protoDir == "" {
		return nil, nil
	}
	p, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return nil, fmt.Errorf("create descriptor parser for publisher detection: %w", err)
	}
	publishers, err := boundaries.CollectActorEventPublisherData(p, protoDir, eventPublishProtoFiles(config), config.SelfPackage)
	if err != nil {
		return nil, err
	}

	seenMethods := make(map[string]string)
	result := make([]PublisherTemplateEntry, 0, len(publishers))
	for _, publisher := range publishers {
		entry := goPublisherEntryFromData(config, publisher, pkgToDirMap)
		if entry.EventTypeName == "" {
			continue
		}
		if prev, ok := seenMethods[entry.MethodName]; ok {
			return nil, fmt.Errorf("duplicate EventBus publisher helper %q for %s and %s", entry.MethodName, prev, entry.Label)
		}
		seenMethods[entry.MethodName] = entry.Label
		result = append(result, entry)
	}
	return result, nil
}

func goPublisherEntryFromData(config *tactype.Config, publisher boundaries.EventPublisherData, pkgToDirMap map[string]string) PublisherTemplateEntry {
	typePkg := ""
	typeName := publisher.EventTypeName
	if idx := strings.LastIndex(publisher.EventTypeFQN, "."); idx >= 0 {
		typePkg = publisher.EventTypeFQN[:idx]
		if publisher.EventTypeFQN[idx+1:] != "" {
			typeName = publisher.EventTypeFQN[idx+1:]
		}
	}
	if typePkg == "" {
		typePkg = config.SelfPackage
	}
	typeProtoDir := "services"
	if dir, ok := pkgToDirMap[typePkg]; ok && dir != "." {
		typeProtoDir = filepath.ToSlash(dir)
	}
	prefix := strutil.ToLowerCamelCase(publisher.RPCStem)
	return PublisherTemplateEntry{
		MethodName:        "Create" + publisher.RPCStem + "Publisher",
		LoopName:          prefix + "PublishLoop",
		StreamName:        prefix + "PublishStream",
		Label:             publisher.TopicRef + " publisher",
		TargetService:     publisher.BrokerLabel,
		EventTypeName:     typeName,
		TypePkgAlias:      sanitizeGoImportAlias(typePkg + "_pb"),
		TypePkgImport:     config.ModuleBasePath + "/types/" + typeProtoDir + "/" + typePkg,
		PublishStreamName: publisher.PublishStreamName,
		PublishPath:       publisher.PublishPath,
	}
}

// collectGoAccountingPublisher returns the publisher entry for the durable
// accounting record topic, if the proto set declares one.
//
// This is the one publisher that is NOT declared per-actor. Accounting is a
// chassis concern: every service emits it, so requiring an event_publish on
// every service's Config would be the same declaration copied N times, and a
// service that forgot it would silently produce no audit trail. The producer
// surface is already declared once, on the record itself
// (s2s: {auth_services: ["*"]}), so the generator resolves the topic from the
// record type instead — mirrors
// pkg/internal/rust/types/publisher_gen.go's collectAccountingPublisher.
func collectGoAccountingPublisher(config *tactype.Config, protoDir string, pkgToDirMap map[string]string) (*PublisherTemplateEntry, error) {
	if protoDir == "" {
		protoDir = config.AnnotatedProtoDir
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
	publisher := boundaries.EventPublisherData{
		TopicRef:          topic.Ref.Raw,
		BrokerLabel:       topic.EventBusLabel,
		EventTypeFQN:      topic.EventTypeFQN,
		EventTypeName:     eventTypeNameFromFQN(topic.EventTypeFQN),
		RPCStem:           topic.RPCStem,
		PublishStreamName: "Publish" + topic.RPCStem,
		PublishPath:       eventbus.PerTopicPublishPath(topic.Ref),
	}
	entry := goPublisherEntryFromData(config, publisher, pkgToDirMap)
	if entry.EventTypeName == "" {
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

func goPublisherTypeImports(publishers []PublisherTemplateEntry, subEntries []SubscriptionTemplateEntry, typeImports []WrapperImport, typeImportsIncluded bool) []WrapperImport {
	usedAliases := make(map[string]string)
	for _, sub := range subEntries {
		usedAliases[sub.TypePkgAlias] = sub.TypePkgImport
	}
	if typeImportsIncluded {
		for _, imp := range typeImports {
			usedAliases[imp.Alias] = imp.ImportPath
		}
	}

	var result []WrapperImport
	for i := range publishers {
		alias := publishers[i].TypePkgAlias
		importPath := publishers[i].TypePkgImport
		if prevPath, ok := usedAliases[alias]; ok && prevPath != importPath {
			alias = goUniquePublisherAlias(importPath, usedAliases)
			publishers[i].TypePkgAlias = alias
		}
		if prevPath, ok := usedAliases[alias]; ok {
			if prevPath == importPath {
				continue
			}
			alias = goUniquePublisherAlias(importPath, usedAliases)
			publishers[i].TypePkgAlias = alias
		}
		usedAliases[alias] = importPath
		result = append(result, WrapperImport{
			Alias:      alias,
			ImportPath: importPath,
		})
	}
	return result
}

func goUniquePublisherAlias(importPath string, usedAliases map[string]string) string {
	base := goImportAliasFromPath(importPath, "pb")
	alias := base
	for n := 2; ; n++ {
		if _, ok := usedAliases[alias]; !ok {
			return alias
		}
		if usedAliases[alias] == importPath {
			return alias
		}
		alias = fmt.Sprintf("%s_%d", base, n)
	}
}
