// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/templates"
)

func loadSwiftTemplate(name string) (string, error) {
	typeFS, err := fs.Sub(templates.FS, "swift/types")
	if err != nil {
		return "", fmt.Errorf("failed to create sub filesystem: %w", err)
	}
	content, err := fs.ReadFile(typeFS, name)
	if err != nil {
		return "", fmt.Errorf("failed to read template %s: %w", name, err)
	}
	return string(content), nil
}

// NEW-44 regression: streaming methods in the Swift W2S client must emit a
// throwing stub (fatalError + @available unavailable) instead of being silently
// omitted. Removing the stub markers from the template fails this test.
func TestSwiftW2SStreamingStub(t *testing.T) {
	tmpl, err := loadSwiftTemplate("W2SClient.swift.tmpl")
	if err != nil {
		t.Fatalf("loadSwiftTemplate: %v", err)
	}
	for _, want := range []string{
		"@available(*, unavailable",
		"fatalError",
		".IsStreaming",
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("Swift W2S template missing streaming stub marker %q", want)
		}
	}
}

// PAR-4 regression: Swift C2D client must validate the socket path. Removing
// the precondition from the template fails this test.
func TestSwiftC2DValidation(t *testing.T) {
	tmpl, err := loadSwiftTemplate("C2DClient.swift.tmpl")
	if err != nil {
		t.Fatalf("loadSwiftTemplate: %v", err)
	}
	if !strings.Contains(tmpl, "precondition(!socketPath.isEmpty") {
		t.Error("Swift C2D template missing socket path validation (precondition)")
	}
}
