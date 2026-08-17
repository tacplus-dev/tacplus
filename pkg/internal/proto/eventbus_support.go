// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

const EventBusSupportProtoPath = "generated/eventbus_support.proto"

// EventBusSupportProtoOptions configures language-specific options on the
// generated EventBus support proto.
type EventBusSupportProtoOptions struct {
	GoPackage string
}

// NeedsEventBusSupportProto reports whether the source tree declares EventBus
// topics that require the canonical EventBus wire helper types.
func NeedsEventBusSupportProto(protoDir string) bool {
	if protoDir == "" {
		return false
	}
	if p, err := parser.NewDescriptorParser(protoDir); err == nil {
		for _, fd := range p.GetAllFiles() {
			for _, msg := range fd.Messages {
				for _, field := range msg.Fields {
					if field.Annotations["topic"].BoolValue {
						return true
					}
				}
			}
		}
	}
	foundTopic := false
	_ = filepath.Walk(protoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}
		relPath, relErr := filepath.Rel(protoDir, path)
		if relErr == nil && IsInfraProto(relPath) {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr == nil && strings.Contains(string(data), "spec.topic") {
			foundTopic = true
			return filepath.SkipAll
		}
		return nil
	})
	return foundTopic
}

// EnsureEventBusSupportProto writes generated/eventbus_support.proto, which
// defines the canonical EventBus wire helper messages used by generated
// per-topic publisher/subscriber clients.
//
// The file is written unconditionally on every generation. It is pure codegen
// output — nothing hand-edits it — so an existing copy on disk carries no
// information worth preserving, and skipping the write when one is present is
// how a stale wire shape survives regeneration (that is exactly how the
// PublishResponse drift fixed in babc6a0b persisted in every checked-in
// service tree until a clean rebuild).
func EnsureEventBusSupportProto(protoDir string, opts EventBusSupportProtoOptions) (string, error) {
	if protoDir == "" {
		return "", fmt.Errorf("eventbus support proto: empty protoDir")
	}
	relPath := filepath.FromSlash(EventBusSupportProtoPath)
	path := filepath.Join(protoDir, relPath)
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("create eventbus support proto dir: %w", err)
	}

	var b strings.Builder
	b.WriteString("syntax = \"proto3\";\n\n")
	b.WriteString("package eventbus_support;\n")
	if opts.GoPackage != "" {
		fmt.Fprintf(&b, "option go_package = %q;\n", opts.GoPackage)
	}
	b.WriteString("\n")
	b.WriteString("import \"google/protobuf/timestamp.proto\";\n\n")
	b.WriteString("message RecordMeta { bytes key = 1; string topic = 2; int32 partition = 3; int64 offset = 4; google.protobuf.Timestamp timestamp = 5; }\n\n")
	b.WriteString("message SubscribeRequest {\n")
	b.WriteString("  oneof start_from {\n")
	b.WriteString("    int64 offset = 1;\n")
	b.WriteString("    bool latest = 3;\n")
	b.WriteString("    bool earliest = 4;\n")
	b.WriteString("  }\n")
	b.WriteString("  string consumer_group = 5;\n")
	b.WriteString("}\n\n")
	// Must match eventbus.PublishResponse in platform/proto/services/eventbus.proto
	// byte-for-byte on the wire — the broker returns that message, generated
	// publishers decode it with this one.
	b.WriteString("message PublishResponse { int32 partition = 1; int64 offset = 2; }\n")

	if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
		return "", fmt.Errorf("write %s: %w", EventBusSupportProtoPath, err)
	}
	return EventBusSupportProtoPath, nil
}
