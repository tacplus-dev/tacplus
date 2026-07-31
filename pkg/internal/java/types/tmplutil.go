// Copyright (c) 2026 Second Star Cavalier LLC

package java

import (
	"fmt"
	"io/fs"

	"gitlab.com/secondstarcavalier/schematic/tacplus/templates"
)

// loadTemplate loads a template from the embedded templates filesystem for Java.
func loadTemplate(name string) (string, error) {
	typeFS, err := fs.Sub(templates.FS, "java/types")
	if err != nil {
		return "", fmt.Errorf("failed to create sub filesystem: %w", err)
	}
	content, err := fs.ReadFile(typeFS, name)
	if err != nil {
		return "", fmt.Errorf("failed to read template %s: %w", name, err)
	}
	return string(content), nil
}
