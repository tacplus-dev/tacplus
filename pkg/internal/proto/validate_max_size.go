// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

// ValidateMaxSize checks that all repeated and map fields in user protos
// have an (spec.assert_max_size) annotation. Generated validators still
// apply a runtime fallback when the annotation is absent, but user-facing
// protos must declare their intended capacity explicitly.
//
// Exceptions (fields that do NOT require assert_max_size):
//   - Fields in sys/ protos (internal infrastructure)
//   - Message-typed fields of a (spec.service_state) root (state boundaries have their own eviction)
//   - Fields with (spec.view_sources) (read-only unions)
//   - Fields with (spec.queue), (spec.topic), or (spec.priority_queue) (separate capacity model)
//   - Fields inside spec.proto itself (defines the annotations)
func ValidateMaxSize(inputDir string) error {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser for max_size validation: %w", err)
	}

	var errors []string
	for relPath, fileDesc := range dp.GetAllFiles() {
		normalized := strings.ReplaceAll(relPath, "\\", "/")
		// Skip infrastructure protos
		if IsInfraProto(normalized) {
			continue
		}
		// Skip spec.proto itself
		if strings.HasSuffix(normalized, "spec.proto") || strings.HasSuffix(normalized, "special.proto") {
			continue
		}
		// Skip Google well-known types (descriptor.proto, struct.proto, etc.)
		if strings.HasPrefix(normalized, "google/") {
			continue
		}
		// Skip errors.proto (contains google.protobuf.Any repeated fields)
		if strings.HasSuffix(normalized, "errors.proto") {
			continue
		}

		for _, msg := range fileDesc.Messages {
			for _, field := range msg.Fields {
				if !field.IsRepeated && !field.IsMap {
					continue
				}

				// Skip statedb boundary fields (they have their own eviction controls).
				// A boundary is a NON-MAP message-typed field of a (spec.service_state)
				// root — the same type-based detection boundaries.go uses (consistency, the
				// old per-field marker, was removed). Maps are excluded: a map-entry field
				// also has TypeKind "message" but is not a generated boundary (the state
				// client rejects map boundaries), so it must still carry an explicit bound
				// rather than escape the capacity check.
				if _, isStateRoot := msg.Annotations["service_state"]; isStateRoot && field.TypeKind == "message" && !field.IsMap {
					continue
				}
				// Skip view_sources fields (read-only unions)
				if vsAnn, hasVS := field.Annotations["view_sources"]; hasVS && len(vsAnn.StringSliceValue) > 0 {
					continue
				}
				// Skip queue/topic/priority_queue fields (separate capacity model)
				if _, hasQueue := field.Annotations["queue"]; hasQueue {
					continue
				}
				if _, hasTopic := field.Annotations["topic"]; hasTopic {
					continue
				}
				if _, hasPQ := field.Annotations["priority_queue"]; hasPQ {
					continue
				}

				// Check for assert_max_size
				if _, hasMaxSize := field.Annotations["assert_max_size"]; !hasMaxSize {
					fieldKind := "repeated"
					if field.IsMap {
						fieldKind = "map"
					}
					errors = append(errors, fmt.Sprintf(
						"%s: %s field %s.%s is missing (spec.assert_max_size) — "+
							"all %s fields must specify a maximum element count",
						relPath, fieldKind, msg.Name, field.Name, fieldKind))
				}
			}
		}
	}

	if len(errors) > 0 {
		return fmt.Errorf("assert_max_size validation failed:\n  %s", strings.Join(errors, "\n  "))
	}
	return nil
}
