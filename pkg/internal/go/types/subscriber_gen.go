// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
	"golang.org/x/tools/imports"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/boundaries"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/eventbus"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/header"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/tmplutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/consts"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
	"gitlab.com/secondstarcavalier/schematic/tacplus/templates"
)

// goSubscriberTemplateData holds the data passed to subscriber.go.tmpl.
type goSubscriberTemplateData struct {
	SourcePackage   string // e.g. "herald"
	SourceStateRoot string // e.g. "AgenticCodeEventBus"
	SourceField     string // e.g. "event_stream"
	FieldPascal     string // e.g. "EventStream"
	TypeName        string // e.g. "SensorEvent"
	TypeImport      string // e.g. "module/tacplus/types/common/sensor"
	EventBusImport  string // e.g. "module/tacplus/types/sys/eventbus"
	GrpcService     string // e.g. "alpha.SharedEventBusStateService" (per-topic; see EVENTBUS-PUBLISH-API-SPLIT.md)
	GrpcMethod      string // e.g. "SubscribeToEventStream"

	// ServiceIMImport is the chassis identity manager, used by the reconnect
	// loop to drop a rejected credential so the retry re-mints instead of
	// replaying the same dead token. Empty in sys-only mode, where the chassis
	// identity manager is not generated at all.
	ServiceIMImport string // e.g. "module/tacplus/chassis/service_im"

	// RoutingOnly is the owner-declared narrowing: this service is named in the
	// source type's s2s_subscribe.routing_only_services, so Handler takes
	// EnvelopeTypeName instead of TypeName and the dispatch projects the three
	// fields below into it. The full TypeName is still decoded off the wire
	// first — a typed boundary, not a memory one (see spec.proto).
	RoutingOnly          bool
	EnvelopeTypeName     string // e.g. "SensorEventRoutingEnvelope"
	KeyFieldPascal       string // e.g. "DeliveryId"  -> envelope delivery_id
	PrincipalFieldPascal string // e.g. "Recipient"   -> envelope recipient
	NamespaceFieldPascal string // e.g. "Namespace"   -> envelope namespace
}

// generateGoSubscribers generates per-topic subscriber packages for event-bus
// subscriptions declared via (spec.subscribe) annotations on state roots.
func generateGoSubscribers(config *tactype.Config) error {
	if len(config.Subscriptions) == 0 {
		return nil
	}

	typeFS, err := fs.Sub(templates.FS, "go/types")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}
	funcMap := tmplutil.BaseFuncMap()

	// generateIdentityManager is skipped in sys-only mode, so the subscriber
	// must not reference a package that will not exist.
	serviceIMImport := ""
	if !config.SysOnly {
		serviceIMImport = config.ModuleBasePath + "/chassis/service_im"
	}

	// Build a package→protoDir map for resolving type locations.
	pkgToDirMap := make(map[string]string)
	if config.FlatProtoDir != "" {
		if dp, err := parser.NewDescriptorParser(config.FlatProtoDir); err == nil {
			for relPath, fd := range dp.GetAllFiles() {
				if fd.Package != "" {
					pkgToDirMap[fd.Package] = filepath.Dir(relPath)
				}
			}
		}
	}

	for _, sub := range config.Subscriptions {
		typeName := sub.SourceTypeFqn
		if idx := strings.LastIndex(typeName, "."); idx >= 0 {
			typeName = typeName[idx+1:]
		}
		if typeName == "" {
			zap.L().Warn("Skipping subscription with empty type FQN",
				zap.String("source", sub.SourcePackage+"."+sub.SourceField))
			continue
		}

		// Extract type package from FQN (e.g. "sensor.SensorEvent" → "sensor").
		typePkg := sub.SourcePackage
		if idx := strings.LastIndex(sub.SourceTypeFqn, "."); idx >= 0 {
			typePkg = sub.SourceTypeFqn[:idx]
		}

		fieldPascal := strutil.ToPascalCase(sub.SourceField)

		// Resolve the type's proto directory from the parser (may differ from
		// the state root's directory).
		typeCategory := sub.SourceProtoDir
		if typeCategory == "" {
			typeCategory = "services"
		}
		if dir, ok := pkgToDirMap[typePkg]; ok && dir != "." {
			typeCategory = dir
		}

		// Per-topic subscribe name, built once in the eventbus package so the
		// five language generators cannot spell one wire contract five ways.
		subService, subMethod := eventbus.PerTopicSubscribeParts(
			sub.SourcePackage, sub.SourceStateRoot, sub.SourceField)

		data := goSubscriberTemplateData{
			SourcePackage:   sub.SourcePackage,
			SourceStateRoot: sub.SourceStateRoot,
			SourceField:     sub.SourceField,
			FieldPascal:     fieldPascal,
			TypeName:        typeName,
			TypeImport:      config.ModuleBasePath + "/types/" + typeCategory + "/" + typePkg,
			EventBusImport:  config.ModuleBasePath + "/types/generated/eventbus_support",
			GrpcService:     subService,
			GrpcMethod:      subMethod,
			ServiceIMImport: serviceIMImport,

			RoutingOnly:          sub.RoutingOnly,
			EnvelopeTypeName:     typeName + boundaries.RoutingEnvelopeSuffix,
			KeyFieldPascal:       strutil.ToPascalCase(sub.SourceKeyField),
			PrincipalFieldPascal: strutil.ToPascalCase(sub.SourceStorePrincipalField),
			NamespaceFieldPascal: strutil.ToPascalCase(sub.SourceStoreNamespaceField),
		}

		outDir := filepath.Join(config.OutputDir, "subscribe", sub.SourcePackage, sub.SourceStateRoot, sub.SourceField)
		if err := os.MkdirAll(outDir, 0755); err != nil {
			return fmt.Errorf("failed to create subscriber dir: %w", err)
		}

		rendered, err := tmplutil.ExecuteTemplate(typeFS, "subscriber.go.tmpl", funcMap, data)
		if err != nil {
			return fmt.Errorf("failed to execute subscriber template for %s.%s: %w",
				sub.SourcePackage, sub.SourceField, err)
		}

		var b strings.Builder
		b.WriteString(header.GeneratedHeader(consts.LangGo, "subscribe/"+sub.SourcePackage+"/"+sub.SourceStateRoot+"/"+sub.SourceField))
		fmt.Fprintf(&b, "package %s\n\n", sub.SourceField)
		b.Write(rendered)

		outputPath := filepath.Join(outDir, "subscriber.go")
		formatted, err := imports.Process(outputPath, []byte(b.String()), &imports.Options{
			Comments:  true,
			TabIndent: true,
			TabWidth:  8,
		})
		if err != nil {
			zap.L().Warn("goimports failed, writing unformatted",
				zap.String("path", outputPath), zap.Error(err))
			formatted = []byte(b.String())
		}

		if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
			return fmt.Errorf("failed to write subscriber: %w", err)
		}

		zap.L().Info("Generated Go event-bus subscriber",
			zap.String("topic", sub.SourcePackage+"."+sub.SourceField),
			zap.String("type", typeName))
	}

	return nil
}
