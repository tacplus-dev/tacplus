// Copyright (c) 2026 Second Star Cavalier LLC

package header

import (
	"strings"
	"testing"
)

func TestLanguageCommentPrefix(t *testing.T) {
	tests := []struct {
		lang string
		want string
	}{
		{"go", "//"},
		{"rust", "//"},
		{"java", "//"},
		{"typescript", "//"},
		{"python", "#"},
		{"unknown", "//"},
	}
	for _, tt := range tests {
		got := LanguageCommentPrefix(tt.lang)
		if got != tt.want {
			t.Errorf("LanguageCommentPrefix(%q) = %q, want %q", tt.lang, got, tt.want)
		}
	}
}

func TestGeneratedHeader(t *testing.T) {
	h := GeneratedHeader("go", "test.proto")
	if !strings.HasPrefix(h, "//") {
		t.Error("Go header should start with //")
	}
	if !strings.Contains(h, "test.proto") {
		t.Error("header should contain source file name")
	}
}

func TestGeneratedHeader_Python(t *testing.T) {
	h := GeneratedHeader("python", "test.proto")
	if !strings.HasPrefix(h, "#") {
		t.Error("Python header should start with #")
	}
}

func TestSimpleHeader(t *testing.T) {
	h := SimpleHeader("go")
	if !strings.HasPrefix(h, "//") {
		t.Error("Go header should start with //")
	}
	if len(h) == 0 {
		t.Error("header should not be empty")
	}
}

// --- Expanded edge case tests ---

func TestLanguageCommentPrefix_AllLanguages(t *testing.T) {
	tests := []struct {
		lang string
		want string
	}{
		{"go", "//"},
		{"rust", "//"},
		{"java", "//"},
		{"typescript", "//"},
		{"python", "#"},
		{"unknown", "//"},
		{"", "//"},
		{"ruby", "//"},
		{"csharp", "//"},
	}
	for _, tt := range tests {
		t.Run(tt.lang, func(t *testing.T) {
			got := LanguageCommentPrefix(tt.lang)
			if got != tt.want {
				t.Errorf("LanguageCommentPrefix(%q) = %q, want %q", tt.lang, got, tt.want)
			}
		})
	}
}

func TestGeneratedHeader_AllLanguages(t *testing.T) {
	languages := []struct {
		lang   string
		prefix string
	}{
		{"go", "//"},
		{"rust", "//"},
		{"java", "//"},
		{"typescript", "//"},
		{"python", "#"},
	}
	for _, tt := range languages {
		t.Run(tt.lang, func(t *testing.T) {
			h := GeneratedHeader(tt.lang, "test.proto")
			if !strings.HasPrefix(h, tt.prefix) {
				t.Errorf("header for %s should start with %q, got prefix %q", tt.lang, tt.prefix, h[:min(len(h), 5)])
			}
			if !strings.Contains(h, "test.proto") {
				t.Errorf("header for %s should contain source file name", tt.lang)
			}
			if !strings.Contains(h, "Copyright") {
				t.Errorf("header for %s should contain Copyright", tt.lang)
			}
			if !strings.Contains(h, "DO NOT EDIT") {
				t.Errorf("header for %s should contain DO NOT EDIT", tt.lang)
			}
			if !strings.Contains(h, "tacplus") {
				t.Errorf("header for %s should contain tacplus", tt.lang)
			}
			// Should end with a double newline (trailing \n from formatHeaderLines + \n from GeneratedHeader)
			if !strings.HasSuffix(h, "\n\n") {
				t.Errorf("header for %s should end with double newline", tt.lang)
			}
		})
	}
}

func TestGeneratedHeader_EmptySource(t *testing.T) {
	h := GeneratedHeader("go", "")
	if strings.Contains(h, "Source:") {
		t.Error("header with empty source should not contain Source line")
	}
	if !strings.Contains(h, "Copyright") {
		t.Error("header should still contain Copyright")
	}
	if !strings.Contains(h, "DO NOT EDIT") {
		t.Error("header should still contain DO NOT EDIT")
	}
}

func TestGeneratedHeader_LongSourcePath(t *testing.T) {
	longSource := "very/deeply/nested/directory/structure/containing/my/proto/definition/file.proto"
	h := GeneratedHeader("go", longSource)
	if !strings.Contains(h, longSource) {
		t.Error("header should contain the full long source path")
	}
}

func TestSimpleHeader_AllLanguages(t *testing.T) {
	languages := []struct {
		lang   string
		prefix string
	}{
		{"go", "//"},
		{"rust", "//"},
		{"java", "//"},
		{"typescript", "//"},
		{"python", "#"},
	}
	for _, tt := range languages {
		t.Run(tt.lang, func(t *testing.T) {
			h := SimpleHeader(tt.lang)
			if !strings.HasPrefix(h, tt.prefix) {
				t.Errorf("simple header for %s should start with %q", tt.lang, tt.prefix)
			}
			if !strings.Contains(h, "Copyright") {
				t.Errorf("simple header for %s should contain Copyright", tt.lang)
			}
			if !strings.Contains(h, "tacplus") {
				t.Errorf("simple header for %s should contain tacplus", tt.lang)
			}
			// Simple header should NOT contain "DO NOT EDIT" (that's only in full header)
			if strings.Contains(h, "DO NOT EDIT") {
				t.Errorf("simple header for %s should NOT contain DO NOT EDIT", tt.lang)
			}
			if !strings.HasSuffix(h, "\n\n") {
				t.Errorf("simple header for %s should end with double newline", tt.lang)
			}
		})
	}
}

func TestSimpleHeader_IsShorterThanFull(t *testing.T) {
	simple := SimpleHeader("go")
	full := GeneratedHeader("go", "test.proto")
	if len(simple) >= len(full) {
		t.Errorf("simple header (%d bytes) should be shorter than full header (%d bytes)", len(simple), len(full))
	}
}

func TestExampleHeader(t *testing.T) {
	h := ExampleHeader("go", "auth.proto")
	if !strings.HasPrefix(h, "//") {
		t.Error("Go example header should start with //")
	}
	if !strings.Contains(h, "Copyright") {
		t.Error("example header should contain Copyright")
	}
	if !strings.Contains(h, "example") {
		t.Error("example header should contain 'example'")
	}
	if !strings.Contains(h, "auth.proto") {
		t.Error("example header should contain source file name")
	}
	if !strings.Contains(h, "Feel free to modify") {
		t.Error("example header should encourage modification")
	}
}

func TestExampleHeader_EmptySource(t *testing.T) {
	h := ExampleHeader("go", "")
	if strings.Contains(h, "Source:") {
		t.Error("example header with empty source should not contain Source line")
	}
	if !strings.Contains(h, "example") {
		t.Error("example header should contain 'example'")
	}
}

func TestExampleHeader_Python(t *testing.T) {
	h := ExampleHeader("python", "test.proto")
	if !strings.HasPrefix(h, "#") {
		t.Error("Python example header should start with #")
	}
	if !strings.Contains(h, "test.proto") {
		t.Error("example header should contain source file name")
	}
}

func TestExampleHeader_AllLanguages(t *testing.T) {
	languages := []string{"go", "rust", "java", "typescript", "python"}
	for _, lang := range languages {
		t.Run(lang, func(t *testing.T) {
			h := ExampleHeader(lang, "test.proto")
			prefix := LanguageCommentPrefix(lang)
			if !strings.HasPrefix(h, prefix) {
				t.Errorf("example header for %s should start with %q", lang, prefix)
			}
			if !strings.Contains(h, "test.proto") {
				t.Errorf("example header for %s should contain source file", lang)
			}
			if !strings.HasSuffix(h, "\n\n") {
				t.Errorf("example header for %s should end with double newline", lang)
			}
		})
	}
}

func TestExampleHeader_DoesNotContainDoNotEdit(t *testing.T) {
	// Example headers should NOT contain "DO NOT EDIT" - they are meant to be modified
	h := ExampleHeader("go", "test.proto")
	if strings.Contains(h, "DO NOT EDIT") {
		t.Error("example header should NOT contain 'DO NOT EDIT'")
	}
}

func TestGeneratedHeader_ContainsAgentWarning(t *testing.T) {
	h := GeneratedHeader("go", "test.proto")
	if !strings.Contains(h, "AGENTS") {
		t.Error("full header should contain agent warning")
	}
}

func TestGeneratedHeader_ContainsGitlabRef(t *testing.T) {
	h := GeneratedHeader("go", "test.proto")
	if !strings.Contains(h, "gitlab.com") {
		t.Error("full header should contain gitlab reference for tacplus source")
	}
}

func TestSimpleHeader_DoesNotContainAgentWarning(t *testing.T) {
	h := SimpleHeader("go")
	if strings.Contains(h, "AGENTS") {
		t.Error("simple header should NOT contain agent warning")
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
