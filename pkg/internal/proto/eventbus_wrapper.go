// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"go.uber.org/zap"
)

// AppendEventBusWrappersToProtos synthesizes the EventBus topic wrapper message
// for every message annotated spec.event (spec.record implies spec.event — see
// parseMessageAnnotations). This replaces the hand-written *EventBus wrappers:
// authors annotate the event message itself (event{c2s?,s2s?} / record{...,c2s?,
// s2s?}) and tacplus derives the topic.
//
// For event message Foo in package p, the appended wrapper is:
//
//	// tacplus:generated EventBus topic wrapper for Foo.
//	message FooBus {
//	  option (spec.service_event) = {tag: "eventbus", export: true};
//	  Foo stream = 1 [(spec.topic) = true];
//	}
//
// yielding the deterministic topic ref p.FooBus.stream. The wrapper is appended
// as proto text to the source file in tempProtoDir, mirroring the other proto
// synthesis passes (state/store/projection): it runs before protoc so the
// wrapper type is generated, before trace injection so the wrapper receives the
// same trace fields a hand-written wrapper would, and before the EventBus topic
// registry is built (which parses tempProtoDir) so the topic is discovered.
//
// Idempotent + migration-safe: when a message named FooBus already exists in the
// file (a hand-written wrapper not yet removed), synthesis is skipped for Foo so
// no duplicate topic_ref is produced. The event message and its wrapper are
// emitted into the same file, so the topic field references Foo by its local
// (unqualified) name.
func AppendEventBusWrappersToProtos(tempProtoDir, inputDir string) error {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	for relPath, fileDesc := range dp.GetAllFiles() {
		if IsInfraProtoFile(relPath, fileDesc) {
			continue
		}

		existing := make(map[string]bool, len(fileDesc.Messages))
		msgByName := make(map[string]*parser.MessageDescriptor, len(fileDesc.Messages))
		for i := range fileDesc.Messages {
			existing[fileDesc.Messages[i].Name] = true
			msgByName[fileDesc.Messages[i].Name] = &fileDesc.Messages[i]
		}

		var buf bytes.Buffer
		var generated []string
		for i := range fileDesc.Messages {
			msg := &fileDesc.Messages[i]
			if ev, ok := msg.Annotations["event"]; !ok || !ev.BoolValue {
				continue
			}
			// A message that is itself a topic wrapper is never an event source.
			if strings.HasSuffix(msg.Name, "Bus") {
				continue
			}
			wrapperName := msg.Name + "Bus"
			if existing[wrapperName] {
				// A message with the wrapper's name already exists. If it is a genuine
				// EventBus topic wrapper (hand-written, mid-migration — it carries a
				// (spec.topic) field), leave it: synthesis is a no-op. But an UNRELATED
				// message that merely collides on the FooBus name would otherwise
				// SILENTLY suppress Foo's topic (MS-1). Fail loud on that collision so
				// the ambiguity is resolved by a rename, not by a lost topic.
				if m := msgByName[wrapperName]; m != nil && !isEventBusTopicWrapper(m) {
					return fmt.Errorf("event %s.%s would synthesize the EventBus topic wrapper %q, "+
						"but an unrelated message named %q already exists in %s; rename one to resolve the collision",
						fileDesc.Package, msg.Name, wrapperName, wrapperName, relPath)
				}
				continue
			}
			existing[wrapperName] = true

			buf.WriteString("\n// tacplus:generated EventBus topic wrapper for " + msg.Name + ".\n")
			buf.WriteString("message " + wrapperName + " {\n")
			buf.WriteString("  option (spec.service_event) = {tag: \"eventbus\", export: true};\n")
			buf.WriteString("  " + msg.Name + " stream = 1 [(spec.topic) = true];\n")
			buf.WriteString("}\n")
			generated = append(generated, wrapperName)
		}
		if buf.Len() == 0 {
			continue
		}

		protoPath := filepath.Join(tempProtoDir, relPath)
		content, err := os.ReadFile(protoPath)
		if err != nil {
			return fmt.Errorf("failed to read proto file %s: %w", relPath, err)
		}
		text := string(content) + buf.String()
		if err := os.WriteFile(protoPath, []byte(text), 0644); err != nil {
			return fmt.Errorf("failed to write proto file %s: %w", relPath, err)
		}

		zap.L().Info("Appended EventBus topic wrappers to proto file",
			zap.String("file", relPath),
			zap.Strings("wrappers", generated))
	}

	return nil
}

// isEventBusTopicWrapper reports whether msg is a genuine EventBus topic wrapper —
// identified by a field carrying (spec.topic) = true, the defining feature of both
// the synthesized wrapper and a hand-written one. An unrelated message that merely
// shares the "<Event>Bus" name has no such field, so this distinguishes a
// migration-safe wrapper from a name collision (MS-1). Pure (no descriptor
// parsing) so it has a protoc-free unit pin.
func isEventBusTopicWrapper(msg *parser.MessageDescriptor) bool {
	for i := range msg.Fields {
		if msg.Fields[i].Annotations["topic"].BoolValue {
			return true
		}
	}
	return false
}
