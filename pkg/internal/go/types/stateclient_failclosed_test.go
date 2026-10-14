// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// NEW-13 residue: generateStateWrapper used to Warn-and-continue when a declared
// collection boundary could not be resolved. Generation reported success, the
// accessor was simply absent from the emitted state client, and the only trace
// was a log line. A service author saw a boundary they had declared not exist,
// with nothing failing anywhere.
//
// These tests exist to prove the guards FIRE. Every fixture and all five real go
// services generate without touching these paths, so a green gate says nothing
// about whether the checks work.

func stateRootWithCollection(fieldType string) *parser.MessageDescriptor {
	return &parser.MessageDescriptor{
		Name: "DemoState",
		Fields: []parser.FieldDescriptor{{
			Name:       "items",
			Type:       fieldType,
			TypeKind:   "message",
			IsRepeated: true,
		}},
	}
}

// demoSvc points generation at a per-test temp directory. generateStateWrapper
// WRITES its output, so without this the accepted-boundary control case emits a
// state.go into this very package and breaks `go build ./...`.
func demoSvc(t *testing.T) (ServiceInfo, *tactype.Config) {
	t.Helper()
	dir := t.TempDir()
	return ServiceInfo{
			ServiceName: "demo",
			PackageName: "demo",
			DirName:     "demo",
			OutputDir:   dir,
			ParentDir:   dir,
		}, &tactype.Config{
			OutputDir: dir,
		}
}

func TestCollectionBoundaryUnresolvedTypeFailsClosed(t *testing.T) {
	fileDesc := &parser.FileDescriptor{
		Path:    "services/demo.proto",
		Package: "demo",
		// Deliberately empty: the referenced message is not here, and the type
		// name has no package qualifier so no cross-package lookup is attempted.
		Messages: nil,
	}

	svc, cfg := demoSvc(t)
	_, err := generateStateWrapper(
		cfg, svc,
		stateRootWithCollection("MissingItem"),
		"DemoState", fileDesc, nil,
	)
	if err == nil {
		t.Fatal("unresolvable collection boundary type generated successfully; " +
			"it must fail closed, not silently omit the accessor")
	}
	if !strings.Contains(err.Error(), "could not be resolved") {
		t.Errorf("error should name the resolution failure, got: %v", err)
	}
	if !strings.Contains(err.Error(), "items") {
		t.Errorf("error should name the offending field, got: %v", err)
	}
}

func TestCollectionBoundaryWithoutIndexedKeyFailsClosed(t *testing.T) {
	// The type resolves, but carries no field marked (spec.indexed) — a
	// collection boundary has no key to address its members by.
	fileDesc := &parser.FileDescriptor{
		Path:    "services/demo.proto",
		Package: "demo",
		Messages: []parser.MessageDescriptor{{
			Name: "Item",
			Fields: []parser.FieldDescriptor{
				{Name: "value", Type: "string", TypeKind: "scalar"},
			},
		}},
	}

	svc, cfg := demoSvc(t)
	_, err := generateStateWrapper(
		cfg, svc,
		stateRootWithCollection("Item"),
		"DemoState", fileDesc, nil,
	)
	if err == nil {
		t.Fatal("collection boundary with no indexed key generated successfully; " +
			"it must fail closed, not silently omit the accessor")
	}
	if !strings.Contains(err.Error(), "indexed key") {
		t.Errorf("error should name the missing indexed key, got: %v", err)
	}
}

// Control: the same shape WITH an indexed key must not trip either guard. Without
// this, both tests above would still pass if generateStateWrapper simply always
// returned an error.
func TestCollectionBoundaryWithIndexedKeyIsAccepted(t *testing.T) {
	fileDesc := &parser.FileDescriptor{
		Path:    "services/demo.proto",
		Package: "demo",
		Messages: []parser.MessageDescriptor{{
			Name: "Item",
			Fields: []parser.FieldDescriptor{{
				Name:     "id",
				Type:     "string",
				TypeKind: "scalar",
				Annotations: map[string]model.AnnotationValue{
					"indexed": {BoolValue: true},
				},
			}},
		}},
	}

	svc, cfg := demoSvc(t)
	_, err := generateStateWrapper(
		cfg, svc,
		stateRootWithCollection("Item"),
		"DemoState", fileDesc, nil,
	)
	if err != nil && strings.Contains(err.Error(), "indexed key") {
		t.Fatalf("valid collection boundary rejected by the indexed-key guard: %v", err)
	}
	if err != nil && strings.Contains(err.Error(), "could not be resolved") {
		t.Fatalf("valid collection boundary rejected by the resolution guard: %v", err)
	}
}
