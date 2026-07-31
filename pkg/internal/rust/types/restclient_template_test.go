// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"
)

func TestRustRestClientSkipsBlankNdjsonLinesWithoutFakeMessages(t *testing.T) {
	tmplStr, err := loadTemplate("restclient.rs.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}

	if strings.Contains(tmplStr, `serde_json::from_str::<Resp>("{}").unwrap()`) {
		t.Fatal("Rust REST NDJSON stream must not deserialize {} to fake-skip blank lines")
	}
	if !strings.Contains(tmplStr, "if trimmed.is_empty() {\n                            continue;\n                        }") {
		t.Fatal("Rust REST NDJSON stream should skip blank lines without yielding an item")
	}
}
