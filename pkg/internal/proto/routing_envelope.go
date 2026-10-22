// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/boundaries"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"go.uber.org/zap"
)

// AppendRoutingEnvelopesToProtos synthesizes the narrow routing envelope for
// every record/event type whose s2s_subscribe declares routing_only_services.
//
// For event message Foo in package p, the appended envelope is:
//
//	// tacplus:generated routing envelope for Foo.
//	message FooRoutingEnvelope {
//	  string delivery_id = 1;
//	  string recipient = 2;
//	  string namespace = 3;
//	}
//
// The three slots are projected, at dispatch, from Foo's own (spec.record).key,
// (spec.store_principal) and (spec.store_namespace) fields — see
// boundaries.ResolveRoutingEnvelope, which resolves them and owns the refusal
// when Foo cannot fill all three.
//
// This is the same synthesis move AppendEventBusWrappersToProtos makes for the
// topic wrapper, and it runs beside it and for the same reasons: as proto text
// into tempProtoDir, before protoc, so every language backend gets the envelope
// type in the SAME package as the source type and the subscriber generators can
// spell it as <T>RoutingEnvelope with no import of their own.
//
// It is emitted unconditionally for a flagged type, not per consuming service:
// a topic's declaration is a property of the topic, and the envelope must exist
// in the source package whichever service is being generated.
//
// The generation refusal here is deliberately NOT conditional on who is being
// generated either — a type that cannot fill its envelope is broken for every
// consumer, so it fails on the first generation of anything that parses it
// rather than only when the flagged subscriber happens to be built.
func AppendRoutingEnvelopesToProtos(tempProtoDir, inputDir string) error {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	for relPath, fileDesc := range dp.GetAllFiles() {
		if IsInfraProtoFile(relPath, fileDesc) {
			continue
		}

		existing := make(map[string]bool, len(fileDesc.Messages))
		for i := range fileDesc.Messages {
			existing[fileDesc.Messages[i].Name] = true
		}

		var buf bytes.Buffer
		var generated []string
		for i := range fileDesc.Messages {
			msg := &fileDesc.Messages[i]
			env, ok, err := boundaries.ResolveRoutingEnvelope(fileDesc.Package, msg)
			if err != nil {
				return fmt.Errorf("%s: %w", relPath, err)
			}
			if !ok {
				continue
			}

			envelopeName := msg.Name + boundaries.RoutingEnvelopeSuffix
			if existing[envelopeName] {
				// An unrelated message on the envelope's name would silently
				// suppress the envelope (and then fail to compile against the
				// projection). Fail loud, exactly as the topic wrapper does on the
				// same collision.
				return fmt.Errorf("record %s.%s would synthesize the routing envelope %q, "+
					"but a message named %q already exists in %s; rename one to resolve the collision",
					fileDesc.Package, msg.Name, envelopeName, envelopeName, relPath)
			}
			existing[envelopeName] = true

			buf.WriteString("\n// tacplus:generated routing envelope for " + msg.Name + ".\n")
			buf.WriteString("// Delivered INSTEAD of " + msg.Name + " to the subscribers named in\n")
			buf.WriteString("// s2s_subscribe.routing_only_services. A typed boundary, not a memory\n")
			buf.WriteString("// one: the full " + msg.Name + " still transits the subscriber's process\n")
			buf.WriteString("// and is projected here, then dropped. See spec.proto.\n")
			buf.WriteString("message " + envelopeName + " {\n")
			buf.WriteString("  // from " + msg.Name + "." + env.KeyField + " ((spec.record).key)\n")
			buf.WriteString("  string delivery_id = 1;\n")
			buf.WriteString("  // from " + msg.Name + "." + env.PrincipalField + " ((spec.store_principal))\n")
			buf.WriteString("  string recipient = 2;\n")
			buf.WriteString("  // from " + msg.Name + "." + env.NamespaceField + " ((spec.store_namespace))\n")
			buf.WriteString("  string namespace = 3;\n")
			buf.WriteString("}\n")
			generated = append(generated, envelopeName)
		}
		if buf.Len() == 0 {
			continue
		}

		protoPath := filepath.Join(tempProtoDir, relPath)
		content, err := os.ReadFile(protoPath)
		if err != nil {
			return fmt.Errorf("failed to read proto file %s: %w", relPath, err)
		}
		if err := os.WriteFile(protoPath, append(content, buf.Bytes()...), 0644); err != nil {
			return fmt.Errorf("failed to write proto file %s: %w", relPath, err)
		}

		zap.L().Info("Appended routing envelopes to proto file",
			zap.String("file", relPath),
			zap.Strings("envelopes", generated))
	}

	return nil
}
