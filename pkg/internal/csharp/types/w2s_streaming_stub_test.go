// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/templates"
)

func loadCSharpTemplate(name string) (string, error) {
	typeFS, err := fs.Sub(templates.FS, "csharp/types")
	if err != nil {
		return "", fmt.Errorf("failed to create sub filesystem: %w", err)
	}
	content, err := fs.ReadFile(typeFS, name)
	if err != nil {
		return "", fmt.Errorf("failed to read template %s: %w", name, err)
	}
	return string(content), nil
}

// NEW-44 regression: streaming methods in the C# W2S client must emit a
// throwing stub (NotSupportedException + Obsolete error) instead of being
// silently omitted. Removing the stub markers from the template fails this test.
func TestCSharpW2SStreamingStub(t *testing.T) {
	tmpl, err := loadCSharpTemplate("W2SClient.cs.tmpl")
	if err != nil {
		t.Fatalf("loadCSharpTemplate: %v", err)
	}
	for _, want := range []string{
		"Obsolete",
		"NotSupportedException",
		".IsStreaming",
	} {
		if !strings.Contains(tmpl, want) {
			t.Errorf("C# W2S template missing streaming stub marker %q", want)
		}
	}
}

// PAR-4 regression: C# C2D client must validate the pipe name. Removing
// the ArgumentException from the template fails this test.
func TestCSharpC2DValidation(t *testing.T) {
	tmpl, err := loadCSharpTemplate("C2DClient.cs.tmpl")
	if err != nil {
		t.Fatalf("loadCSharpTemplate: %v", err)
	}
	if !strings.Contains(tmpl, "ArgumentException") {
		t.Error("C# C2D template missing pipe name validation (ArgumentException)")
	}
}
