// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package codefile

import (
	"fmt"
	"strings"
)

// Language selects language-specific formatting rules.
type Language int

const (
	Go         Language = iota // tab indent
	Rust                       // 4-space indent
	Java                       // 4-space indent
	TypeScript                 // 2-space indent
)

// langConfig holds language-specific formatting settings.
type langConfig struct {
	indent  string
	comment string
}

var configs = map[Language]langConfig{
	Go:         {indent: "\t", comment: "//"},
	Rust:       {indent: "    ", comment: "//"},
	Java:       {indent: "    ", comment: "//"},
	TypeScript: {indent: "  ", comment: "//"},
}

// File is an indentation-aware code writer that tracks brace nesting
// and automatically indents output lines.
type File struct {
	buf    strings.Builder
	depth  int
	lang   Language
	config langConfig
}

// New creates a new code file builder for the given language.
func New(lang Language) *File {
	cfg, ok := configs[lang]
	if !ok {
		cfg = configs[Go]
	}
	return &File{
		lang:   lang,
		config: cfg,
	}
}

// P writes a formatted line at the current indentation level.
// The line is terminated with a newline.
func (f *File) P(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	f.writeLine(line)
}

// Pn writes a formatted string at the current indentation level
// WITHOUT a trailing newline. Use for building partial lines.
func (f *File) Pn(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	f.writeIndent()
	f.buf.WriteString(line)
}

// Blank writes an empty line.
func (f *File) Blank() {
	f.buf.WriteByte('\n')
}

// Comment writes a single-line comment at the current indentation level.
func (f *File) Comment(format string, args ...any) {
	text := fmt.Sprintf(format, args...)
	f.writeIndent()
	f.buf.WriteString(f.config.comment)
	f.buf.WriteByte(' ')
	f.buf.WriteString(text)
	f.buf.WriteByte('\n')
}

// BlockComment writes a /* ... */ block comment at the current indentation level.
func (f *File) BlockComment(lines ...string) {
	f.writeIndent()
	f.buf.WriteString("/*\n")
	for _, line := range lines {
		f.writeIndent()
		f.buf.WriteString(" * ")
		f.buf.WriteString(line)
		f.buf.WriteByte('\n')
	}
	f.writeIndent()
	f.buf.WriteString(" */\n")
}

// Open writes a line ending with "{" and increases the indentation level.
// The opening brace is appended automatically if format doesn't end with one.
func (f *File) Open(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	line = strings.TrimRight(line, " ")
	if !strings.HasSuffix(line, "{") {
		if line == "" {
			line = "{"
		} else {
			line += " {"
		}
	}
	f.writeIndent()
	f.buf.WriteString(line)
	f.buf.WriteByte('\n')
	f.depth++
}

// Close writes "}" at the current indentation level (decreasing indent first).
// An optional suffix is appended after the brace: Close(",") writes "},".
func (f *File) Close(suffix ...string) {
	if f.depth > 0 {
		f.depth--
	}
	f.writeIndent()
	f.buf.WriteByte('}')
	for _, s := range suffix {
		f.buf.WriteString(s)
	}
	f.buf.WriteByte('\n')
}

// CloseOpen closes the current block and immediately opens a new one.
// Useful for "} else {", "} catch (Exception e) {", etc.
func (f *File) CloseOpen(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	line = strings.TrimRight(line, " ")
	if !strings.HasSuffix(line, "{") {
		line += " {"
	}
	if f.depth > 0 {
		f.depth--
	}
	f.writeIndent()
	f.buf.WriteString("} ")
	f.buf.WriteString(line)
	f.buf.WriteByte('\n')
	f.depth++
}

// Indent manually increases the indentation level.
func (f *File) Indent() {
	f.depth++
}

// Dedent manually decreases the indentation level.
func (f *File) Dedent() {
	if f.depth > 0 {
		f.depth--
	}
}

// Section writes a visual separator comment.
func (f *File) Section(title string) {
	sep := strings.Repeat("=", 60)
	f.Comment("%s", sep)
	f.Comment("%s", title)
	f.Comment("%s", sep)
}

// Raw writes pre-formatted text verbatim without indentation.
// Use for embedding template output or pre-formatted blocks.
func (f *File) Raw(s string) {
	f.buf.WriteString(s)
}

// Build returns the formatted file content as bytes.
func (f *File) Build() []byte {
	return []byte(f.buf.String())
}

// String returns the formatted file content as a string.
func (f *File) String() string {
	return f.buf.String()
}

// Depth returns the current indentation depth.
func (f *File) Depth() int {
	return f.depth
}

// IndentCode takes raw unindented code and returns it with proper indentation
// based on brace-counting. This is useful for post-processing template output
// that was emitted without indentation (relying on external formatters).
//
// For each line, it counts all "{" and "}" characters to determine the net
// brace balance. Closing braces reduce depth before writing; opening braces
// increase depth after writing. This handles multi-brace patterns like "}}"
// or "]};" correctly. Balanced lines starting with "}" (e.g., "} else {")
// are handled as a dedent-then-indent.
func IndentCode(lang Language, raw string) string {
	cfg, ok := configs[lang]
	if !ok {
		cfg = configs[Go]
	}

	lines := strings.Split(raw, "\n")
	var buf strings.Builder
	buf.Grow(len(raw) + len(raw)/4) // rough estimate: +25% for indentation
	depth := 0

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)

		// Preserve empty lines
		if trimmed == "" {
			if i < len(lines)-1 { // don't add trailing newline for last empty split
				buf.WriteByte('\n')
			}
			continue
		}

		// Count all braces on the line
		openCount := strings.Count(trimmed, "{")
		closeCount := strings.Count(trimmed, "}")
		net := openCount - closeCount

		if net < 0 {
			// Net closing: decrease depth before writing
			depth += net // net is negative
			if depth < 0 {
				depth = 0
			}
		} else if net == 0 && closeCount > 0 && trimmed[0] == '}' {
			// Balanced but starts with "}" (e.g., "} else {"):
			// dedent for this line, indent back after
			depth--
			if depth < 0 {
				depth = 0
			}
		}

		// Write indent + line
		for j := 0; j < depth; j++ {
			buf.WriteString(cfg.indent)
		}
		buf.WriteString(trimmed)
		buf.WriteByte('\n')

		if net > 0 {
			// Net opening: increase depth after writing
			depth += net
		} else if net == 0 && closeCount > 0 && trimmed[0] == '}' {
			// Re-indent after "} else {" pattern
			depth++
		}
	}

	return buf.String()
}

// writeLine handles auto-indent/dedent for brace lines, then writes the line.
func (f *File) writeLine(line string) {
	trimmed := strings.TrimSpace(line)

	// Dedent for lines that start with a closing brace
	if f.depth > 0 && len(trimmed) > 0 && trimmed[0] == '}' {
		f.depth--
	}

	f.writeIndent()
	f.buf.WriteString(line)
	f.buf.WriteByte('\n')

	// Indent after lines that end with an opening brace
	if len(trimmed) > 0 && trimmed[len(trimmed)-1] == '{' {
		f.depth++
	}
}

// writeIndent writes the current indentation prefix.
func (f *File) writeIndent() {
	for i := 0; i < f.depth; i++ {
		f.buf.WriteString(f.config.indent)
	}
}
