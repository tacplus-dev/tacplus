// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// StripSpecOptions removes all spec.* annotations from proto file content.
// This includes:
//   - import "sys/spec.proto" lines (removed when no remaining spec types need them)
//   - File/message/service-level option (spec.*) lines, including multi-line blocks (removed entirely)
//   - Field-level [(spec.*)] annotations, including multi-line (stripped from field declarations)
//
// Non-spec options and annotations are preserved.
// Uses strings.Contains throughout because this operates on raw proto text
// before parsing — no descriptors are available at this stage.
func StripSpecOptions(content string) string {
	lines := strings.Split(content, "\n")
	var result []string
	prevBlank := false
	i := 0

	for i < len(lines) {
		line := lines[i]
		trimmed := strings.TrimSpace(line)

		// Remove file/message/service-level option (spec.*) lines.
		// These can be single-line or multi-line (with { ... } blocks).
		if strings.HasPrefix(trimmed, "option (spec.") {
			// Check if this is a multi-line option block (opens { without closing };).
			// A trailing // comment must not count toward the terminator, otherwise a
			// fully closed single-line option would be mistaken for an unterminated
			// block and every following line silently skipped.
			if strings.Contains(trimmed, "{") && !optionBlockCloses(trimmed) {
				// Skip until we find the closing };
				i++
				for i < len(lines) {
					if optionBlockCloses(strings.TrimSpace(lines[i])) {
						i++
						break
					}
					i++
				}
			} else {
				i++
			}
			continue
		}

		// Handle field-level annotations that may contain (spec.*).
		// These can be single-line [...] or multi-line with [ on one line and ] on another.
		// Only start this path from real field declarations; comments can contain
		// bracketed prose such as "[start_ms, end_ms)" before an annotated field.
		if !isProtoCommentLine(trimmed) && (strings.Contains(line, "(spec.") || (looksLikeFieldAnnotationStart(line) && i+1 < len(lines) && looksLikeAnnotationContinuation(lines, i))) {
			line, i = stripFieldAnnotationsMultiLine(lines, i)
		} else {
			i++
		}

		// Collapse double-blank-lines
		isBlank := strings.TrimSpace(line) == ""
		if isBlank && prevBlank {
			continue
		}
		prevBlank = isBlank

		result = append(result, line)
	}

	stripped := strings.Join(result, "\n")
	if !contentNeedsSpecProto(stripped) {
		stripped = removeSpecImport(stripped)
	}
	return stripped
}

// optionBlockCloses reports whether a trimmed option line terminates its
// { ... } block, ignoring any trailing // line comment. stripProtoLineComment
// is quote-aware, so a "//" or "};" inside a string literal is not mistaken
// for a comment or a terminator.
func optionBlockCloses(trimmed string) bool {
	return strings.HasSuffix(stripProtoLineComment(trimmed), "};")
}

func contentNeedsSpecProto(content string) bool {
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == `import "sys/spec.proto";` {
			continue
		}
		if strings.Contains(line, "spec.") {
			return true
		}
	}
	return false
}

func removeSpecImport(content string) string {
	lines := strings.Split(content, "\n")
	var result []string
	prevBlank := false
	for _, line := range lines {
		if strings.TrimSpace(line) == `import "sys/spec.proto";` {
			continue
		}
		isBlank := strings.TrimSpace(line) == ""
		if isBlank && prevBlank {
			continue
		}
		prevBlank = isBlank
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

func isProtoCommentLine(trimmed string) bool {
	return strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*")
}

func looksLikeFieldAnnotationStart(line string) bool {
	bracketStart := strings.Index(line, "[")
	if bracketStart < 0 || strings.Contains(line[bracketStart:], "]") {
		return false
	}
	prefix := line[:bracketStart]
	return strings.Contains(prefix, "=")
}

// looksLikeAnnotationContinuation checks if lines after index i look like
// continuation of a multi-line proto annotation (contain spec.* and eventually ]).
func looksLikeAnnotationContinuation(lines []string, i int) bool {
	for j := i + 1; j < len(lines) && j < i+10; j++ {
		t := strings.TrimSpace(lines[j])
		if strings.Contains(t, "(spec.") {
			return true
		}
		if strings.Contains(t, "]") {
			break
		}
	}
	return false
}

// stripFieldAnnotationsMultiLine handles stripping spec annotations from field
// declarations that may span multiple lines. Returns the processed line and the
// next index to process.
func stripFieldAnnotationsMultiLine(lines []string, startIdx int) (string, int) {
	line := lines[startIdx]

	// Check if this is a single-line case (both [ and ] on the same line)
	if strings.Contains(line, "[") && strings.Contains(line, "]") {
		return stripFieldAnnotations(line), startIdx + 1
	}

	// Multi-line case: collect all lines from [ to ]
	bracketStart := strings.Index(line, "[")
	if bracketStart < 0 {
		// No bracket on this line, just strip inline spec annotations
		return stripFieldAnnotations(line), startIdx + 1
	}

	// Preserve the prefix before [ from the first line
	prefix := line[:bracketStart]

	// Collect annotation content across lines
	var innerParts []string
	innerParts = append(innerParts, stripProtoLineComment(line[bracketStart+1:]))
	endIdx := startIdx + 1
	var suffix string
	for endIdx < len(lines) {
		closeBracket := strings.Index(lines[endIdx], "]")
		if closeBracket >= 0 {
			innerParts = append(innerParts, stripProtoLineComment(lines[endIdx][:closeBracket]))
			suffix = lines[endIdx][closeBracket+1:]
			endIdx++
			break
		}
		innerParts = append(innerParts, stripProtoLineComment(lines[endIdx]))
		endIdx++
	}

	// Join the inner content and process as a single-line annotation
	innerJoined := strings.Join(innerParts, " ")
	// Build synthetic single line and strip
	synthetic := prefix + "[" + innerJoined + "]" + suffix
	stripped := stripFieldAnnotations(synthetic)

	return stripped, endIdx
}

// StripSpecOptionsFromDir strips spec.* annotations from all .proto files
// in the given directory tree and removes sys/spec.proto (no longer needed
// once annotations are stripped).
func StripSpecOptionsFromDir(protoDir string) error {
	needsSpecProto := false

	// Strip all remaining .proto files
	if err := filepath.Walk(protoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", path, err)
		}

		stripped := StripSpecOptions(string(content))
		if contentNeedsSpecProto(stripped) {
			needsSpecProto = true
		}
		if stripped != string(content) {
			if err := os.WriteFile(path, []byte(stripped), 0644); err != nil {
				return fmt.Errorf("failed to write %s: %w", path, err)
			}
		}
		return nil
	}); err != nil {
		return err
	}

	if !needsSpecProto {
		libPath := filepath.Join(protoDir, "sys", "spec.proto")
		if _, err := os.Stat(libPath); err == nil {
			if err := os.Remove(libPath); err != nil {
				return fmt.Errorf("failed to remove spec.proto: %w", err)
			}
		}
	}

	return nil
}

// stripFieldAnnotations removes (spec.*) annotations from a field declaration line.
// If the entire bracket contains only spec annotations, the whole bracket is removed.
// If there are mixed annotations, only the spec ones are removed.
func stripFieldAnnotations(line string) string {
	bracketStart := strings.Index(line, "[")
	if bracketStart < 0 {
		return line
	}
	bracketEnd := strings.LastIndex(line, "]")
	if bracketEnd < 0 || bracketEnd < bracketStart {
		return line
	}

	// Parse annotations inside the brackets
	inner := stripProtoLineComment(line[bracketStart+1 : bracketEnd])
	parts := splitAnnotations(inner)
	var nonSpecialParts []string
	for _, part := range parts {
		trimmedPart := strings.TrimSpace(part)
		if trimmedPart == "" {
			continue
		}
		if !strings.HasPrefix(trimmedPart, "(spec.") {
			nonSpecialParts = append(nonSpecialParts, trimmedPart)
		}
	}

	if len(nonSpecialParts) > 0 {
		// Keep the bracket with only non-spec annotations
		newBracket := "[" + strings.Join(nonSpecialParts, ", ") + "]"
		prefix := strings.TrimRight(line[:bracketStart], " \t")
		suffix := line[bracketEnd+1:]
		return prefix + " " + newBracket + suffix
	}

	// All annotations are spec — remove the entire bracket and leading whitespace
	prefix := strings.TrimRight(line[:bracketStart], " \t")
	suffix := line[bracketEnd+1:]
	return prefix + suffix
}

func stripProtoLineComment(line string) string {
	inQuote := false
	escaped := false
	for i := 0; i < len(line)-1; i++ {
		ch := line[i]
		if escaped {
			escaped = false
			continue
		}
		if ch == '\\' && inQuote {
			escaped = true
			continue
		}
		if ch == '"' {
			inQuote = !inQuote
			continue
		}
		if !inQuote && ch == '/' && line[i+1] == '/' {
			return strings.TrimRight(line[:i], " \t")
		}
	}
	return line
}

// splitAnnotations splits the inner content of a proto annotation bracket by commas,
// respecting parentheses, braces, and quoted strings so that (name) = "value,with,commas"
// and (name) = {nested} are kept together.
func splitAnnotations(inner string) []string {
	var parts []string
	depth := 0
	inQuote := false
	start := 0
	for i, ch := range inner {
		if ch == '"' {
			inQuote = !inQuote
			continue
		}
		if inQuote {
			continue
		}
		switch ch {
		case '(', '{':
			depth++
		case ')', '}':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, inner[start:i])
				start = i + 1
			}
		}
	}
	if start < len(inner) {
		parts = append(parts, inner[start:])
	}
	return parts
}
