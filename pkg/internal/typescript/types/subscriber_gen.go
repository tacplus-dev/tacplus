// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"fmt"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/boundaries"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/eventbus"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// tsSubscriberTemplateData holds the data passed to subscriber.ts.tmpl.
type tsSubscriberTemplateData struct {
	SourcePackage   string // e.g. "aegis"
	SourceStateRoot string // e.g. "AegisState"
	SourceField     string // e.g. "event_stream"
	FieldPascal     string // e.g. "EventStream"
	TypeName        string // e.g. "TopologyEvent"
	TypeSchemaName  string // e.g. "TopologyEventSchema"
	// EnvelopeName / EnvelopeSchemaName are the generated Subscribe-record wrapper
	// (ToPascalCase(SourceStateRoot)+FieldPascal+"SubscribeRecord"), co-generated
	// with the payload event in the source module. field 1 = EventBusRecordMeta
	// meta, field 2 = payload. Decoding it (not the bare payload) recovers
	// offset/timestamp/key for the handler (PC-C-5).
	EnvelopeName       string // e.g. "AegisStateEventStreamSubscribeRecord"
	EnvelopeSchemaName string // e.g. "AegisStateEventStreamSubscribeRecordSchema"
	TypeImportPath     string // relative import path to the generated type index
	GrpcService        string // e.g. "alpha.SharedEventBusStateService" (per-topic; see EVENTBUS-PUBLISH-API-SPLIT.md)
	GrpcMethod         string // e.g. "SubscribeToEventStream"

	// RoutingOnly is the owner-declared narrowing: this service is named in the
	// source type's s2s_subscribe.routing_only_services, so Handler takes
	// RoutingEnvelopeName and the dispatch projects the three fields below into
	// it. The full TypeName is still decoded off the wire first — a typed
	// boundary, not a memory one (see spec.proto).
	RoutingOnly           bool
	RoutingEnvelopeName   string // e.g. "SensorEventRoutingEnvelope"
	RoutingEnvelopeSchema string // e.g. "SensorEventRoutingEnvelopeSchema"
	KeyFieldCamel         string // source field -> envelope deliveryId
	PrincipalFieldCamel   string // source field -> envelope recipient
	NamespaceFieldCamel   string // source field -> envelope namespace
}

type tsSubscriptionEventInfo struct {
	TypeName  string
	ModuleDir string
}

func resolveTsSubscriptionEvent(config *tactype.Config, sub tactype.SubscriptionEntry) tsSubscriptionEventInfo {
	typeName := sub.SourceTypeFqn
	typePkg := sub.SourcePackage
	if idx := strings.LastIndex(typeName, "."); idx >= 0 {
		typePkg = typeName[:idx]
		typeName = typeName[idx+1:]
	}
	if typeName == "" {
		typeName = "Event"
	}

	moduleDir := typescriptModuleDirForPackageOrDir(config.OutputDir, typePkg, sub.SourceProtoDir)
	return tsSubscriptionEventInfo{TypeName: typeName, ModuleDir: moduleDir}
}

func tsRelativeModuleImport(fromDir, targetDir string) string {
	rel, err := filepath.Rel(fromDir, targetDir)
	if err != nil {
		rel = targetDir
	}
	rel = filepath.ToSlash(rel)
	if !strings.HasPrefix(rel, ".") {
		rel = "./" + rel
	}
	return rel + "/index.js"
}

// collectTsSubscriptions scans actor-owned protos for legacy subscribe and
// explicit event_watch annotations, plus store-derived watches, and returns
// them as tactype.SubscriptionEntry.
func collectTsSubscriptions(config *tactype.Config, protoDir string) ([]tactype.SubscriptionEntry, error) {
	if protoDir == "" {
		protoDir = config.FlatProtoDir
	}
	if protoDir == "" {
		return nil, nil
	}
	p, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return nil, fmt.Errorf("create descriptor parser for subscription detection: %w", err)
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

	subs, err := boundaries.CollectActorSubscriptions(p, protoDir, protoFiles, config.SelfPackage)
	if err != nil {
		return nil, err
	}
	result := make([]tactype.SubscriptionEntry, 0, len(subs))
	for _, sd := range subs {
		res := boundaries.ResolveSubscriptionSource(p, protoDir, sd)
		entry := tactype.SubscriptionEntry{
			SourcePackage:   sd.SourcePackage,
			SourceStateRoot: sd.SourceStateRoot,
			SourceField:     sd.SourceField,
			SourceTypeFqn:   res.TypeFqn,
			SourceProtoDir:  res.ProtoDir,
		}
		if err := boundaries.ApplyRoutingEnvelope(p, config.SelfPackage, &entry); err != nil {
			return nil, err
		}
		result = append(result, entry)
		if sd.Origin == "store_derived" {
			zap.L().Info("Auto-derived store subscription",
				zap.String("source", sd.Source),
				zap.String("type", res.TypeFqn))
		}
	}
	return result, nil
}

// generateTsSubscribers generates per-topic subscriber modules for event-bus subscriptions.
func generateTsSubscribers(config *tactype.Config) error {
	tmplContent, err := loadTemplate("subscriber.ts.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load subscriber template: %w", err)
	}

	tmpl, err := template.New("subscriber").Funcs(header.Funcs()).Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse subscriber template: %w", err)
	}

	subscribeDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "subscribe")
	if err := os.MkdirAll(subscribeDir, 0755); err != nil {
		return fmt.Errorf("failed to create subscribe dir: %w", err)
	}

	// Generate an index.ts that re-exports all subscriber modules.
	var indexBuf bytes.Buffer
	indexBuf.WriteString(header.Copyright(consts.LangTypeScript))
	indexBuf.WriteString("// Generated by tacplus\n//\n// CODE GENERATED - DO NOT EDIT\n\n")

	for _, sub := range config.Subscriptions {
		event := resolveTsSubscriptionEvent(config, sub)

		fieldPascal := strutil.ToPascalCase(sub.SourceField)
		rpcStem := eventbus.RPCStemForTopicParts(sub.SourcePackage, sub.SourceStateRoot, sub.SourceField)
		modName := strutil.ToSnakeCase(rpcStem)

		// Per-topic subscribe name, built once in the eventbus package so the
		// five language generators cannot spell one wire contract five ways.
		subService, subMethod := eventbus.PerTopicSubscribeParts(
			sub.SourcePackage, sub.SourceStateRoot, sub.SourceField)

		data := tsSubscriberTemplateData{
			SourcePackage:      sub.SourcePackage,
			SourceStateRoot:    sub.SourceStateRoot,
			SourceField:        sub.SourceField,
			FieldPascal:        fieldPascal,
			TypeName:           event.TypeName,
			TypeSchemaName:     event.TypeName + "Schema",
			EnvelopeName:       strutil.ToPascalCase(sub.SourceStateRoot) + fieldPascal + "SubscribeRecord",
			EnvelopeSchemaName: strutil.ToPascalCase(sub.SourceStateRoot) + fieldPascal + "SubscribeRecordSchema",
			TypeImportPath:     tsRelativeModuleImport(filepath.Join(config.GetSmPrefix(), "subscribe"), event.ModuleDir),
			GrpcService:        subService,
			GrpcMethod:         subMethod,

			RoutingOnly:           sub.RoutingOnly,
			RoutingEnvelopeName:   event.TypeName + boundaries.RoutingEnvelopeSuffix,
			RoutingEnvelopeSchema: event.TypeName + boundaries.RoutingEnvelopeSuffix + "Schema",
			KeyFieldCamel:         strutil.ToLowerCamelCase(sub.SourceKeyField),
			PrincipalFieldCamel:   strutil.ToLowerCamelCase(sub.SourceStorePrincipalField),
			NamespaceFieldCamel:   strutil.ToLowerCamelCase(sub.SourceStoreNamespaceField),
		}

		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			return fmt.Errorf("failed to execute subscriber template for %s: %w", sub.SourceField, err)
		}

		outputPath := filepath.Join(subscribeDir, modName+".ts")
		if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
			return fmt.Errorf("failed to write subscriber module %s: %w", modName, err)
		}

		indexBuf.WriteString(fmt.Sprintf("export { Subscriber as %sSubscriber } from './%s.js';\n", fieldPascal, modName))
		zap.L().Info("Generated TypeScript event-bus subscriber", zap.String("module", modName))
	}

	indexPath := filepath.Join(subscribeDir, "index.ts")
	if err := os.WriteFile(indexPath, indexBuf.Bytes(), 0644); err != nil {
		return fmt.Errorf("failed to write subscribe index.ts: %w", err)
	}

	return nil
}
