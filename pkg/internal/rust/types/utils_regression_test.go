// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

func TestRustUtilsDecodeBase64PadsLegacyUnpaddedInput(t *testing.T) {
	out := t.TempDir()
	if err := generateRustUtils(&tactype.Config{OutputDir: out}); err != nil {
		t.Fatalf("generateRustUtils: %v", err)
	}

	utils, err := os.ReadFile(filepath.Join(out, "utils", "utils.rs"))
	if err != nil {
		t.Fatalf("read generated utils.rs: %v", err)
	}

	rendered := string(utils)
	for _, want := range []string{
		"let mut padded = input.to_string();",
		"padded.extend(std::iter::repeat('=').take(4 - remainder));",
		"general_purpose::STANDARD.decode(&padded)",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("generated Rust utils missing %q:\n%s", want, rendered)
		}
	}
}
