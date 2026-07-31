// Copyright (c) 2026 Second Star Cavalier LLC

package java

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/boundaries"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/eventbus"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// javaSubscriberTemplateData holds the data passed to Subscriber.java.tmpl.
type javaSubscriberTemplateData struct {
	PackageBase     string // e.g. "com.example.s2sm.subscribe"
	SourcePackage   string // e.g. "aegis"
	SourceStateRoot string // e.g. "AegisState"
	SourceField     string // e.g. "event_stream"
	FieldPascal     string // e.g. "EventStream"
	TypeName        string // e.g. "TopologyEvent"
	TypeImport      string // e.g. "com.example.types.vservices.aegis.Aegis"
	TypeOuterClass  string // e.g. "Aegis"
	ClassName       string // e.g. "AegisEventStreamSubscriber"
	GrpcService     string // e.g. "eventbus.EventBusTypedService"
	GrpcMethod      string // e.g. "SubscribeAegisAegisStateEventStream"
}

type javaSubscriptionEventInfo struct {
	TypeName       string
	TypeImport     string
	TypeOuterClass string
}

func resolveJavaSubscriptionEvent(config *tactype.Config, protoDir string, sub tactype.SubscriptionEntry) javaSubscriptionEventInfo {
	typeName := sub.SourceTypeFqn
	typePkg := sub.SourcePackage
	if idx := strings.LastIndex(typeName, "."); idx >= 0 {
		typePkg = typeName[:idx]
		typeName = typeName[idx+1:]
	}
	if typeName == "" {
		typeName = "Event"
	}

	typeRelPath := ""
	if protoDir != "" {
		if p, err := parser.NewDescriptorParser(protoDir); err == nil {
			for relPath, fd := range p.GetAllFiles() {
				if fd.Package == typePkg {
					typeRelPath = relPath
					break
				}
			}
		}
	}

	typeParentDir := sub.SourceProtoDir
	protoPath := ""
	if typeRelPath != "" {
		typeParentDir = filepath.Dir(typeRelPath)
		if typeParentDir == "." {
			typeParentDir = ""
		}
		protoPath = filepath.Join(protoDir, typeRelPath)
	}

	javaPackage := ""
	outerClass := ""
	if protoPath != "" {
		javaPackage = extractJavaPackageName(protoPath)
		outerClass = extractJavaOuterClassName(protoPath)
	}
	if outerClass == "" {
		outerClass = strutil.ToCamelCase(typePkg)
	}
	if javaPackage == "" {
		parts := []string{config.JavaPackageBase, "types"}
		if typeParentDir != "" {
			parts = append(parts, strings.Split(filepath.ToSlash(typeParentDir), "/")...)
		}
		parts = append(parts, typePkg)
		javaPackage = strings.Join(parts, ".")
	}

	return javaSubscriptionEventInfo{
		TypeName:       typeName,
		TypeImport:     javaPackage + "." + outerClass,
		TypeOuterClass: outerClass,
	}
}

// collectJavaSubscriptions scans actor-owned protos for legacy subscribe and
// explicit event_watch annotations, plus store-derived watches, and returns
// them as tactype.SubscriptionEntry.
func collectJavaSubscriptions(config *tactype.Config, protoDir string) ([]tactype.SubscriptionEntry, error) {
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
		result = append(result, tactype.SubscriptionEntry{
			SourcePackage:   sd.SourcePackage,
			SourceStateRoot: sd.SourceStateRoot,
			SourceField:     sd.SourceField,
			TargetService:   sd.BrokerTag,
			SourceTypeFqn:   res.TypeFqn,
			SourceProtoDir:  res.ProtoDir,
		})
		if sd.Origin == "store_derived" {
			zap.L().Info("Auto-derived store subscription",
				zap.String("source", sd.Source),
				zap.String("type", res.TypeFqn))
		}
	}

	return result, nil
}

// generateJavaSubscribers generates per-topic subscriber classes for event-bus subscriptions.
func generateJavaSubscribers(config *tactype.Config, protoDir string) error {
	tmplContent, err := loadTemplate("Subscriber.java.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load subscriber template: %w", err)
	}

	tmpl, err := template.New("subscriber").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse subscriber template: %w", err)
	}

	packagePath := strings.ReplaceAll(config.JavaPackageBase, ".", string(filepath.Separator))
	subscribeDir := filepath.Join(config.LangDir(), "src", "main", "java", packagePath, config.GetSmPrefix(), "subscribe")
	if err := os.MkdirAll(subscribeDir, 0755); err != nil {
		return fmt.Errorf("failed to create subscribe dir: %w", err)
	}

	subscribePkg := config.JavaPackageBase + "." + config.GetSmPrefix() + ".subscribe"

	for _, sub := range config.Subscriptions {
		event := resolveJavaSubscriptionEvent(config, protoDir, sub)

		fieldPascal := strutil.ToPascalCase(sub.SourceField)
		rpcStem := eventbus.RPCStemForTopicParts(sub.SourcePackage, sub.SourceStateRoot, sub.SourceField)
		className := rpcStem + "Subscriber"

		data := javaSubscriberTemplateData{
			PackageBase:     subscribePkg,
			SourcePackage:   sub.SourcePackage,
			SourceStateRoot: sub.SourceStateRoot,
			SourceField:     sub.SourceField,
			FieldPascal:     fieldPascal,
			TypeName:        event.TypeName,
			TypeImport:      event.TypeImport,
			TypeOuterClass:  event.TypeOuterClass,
			ClassName:       className,
			GrpcService:     eventbus.TypedServiceFullName(sub.TargetService),
			GrpcMethod:      "Subscribe" + rpcStem,
		}

		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			return fmt.Errorf("failed to execute subscriber template for %s: %w", sub.SourceField, err)
		}

		outputPath := filepath.Join(subscribeDir, className+".java")
		if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
			return fmt.Errorf("failed to write subscriber class %s: %w", className, err)
		}

		zap.L().Info("Generated Java event-bus subscriber", zap.String("class", className))
	}

	return nil
}
