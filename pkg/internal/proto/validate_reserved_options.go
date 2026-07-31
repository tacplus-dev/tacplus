// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// reservedSpecOptionFields are field names that once existed in spec.proto
// annotation messages and are now RESERVED (removed/renamed). protoc's option
// text-format parser silently DISCARDS reserved-name fields instead of
// erroring, so a proto still using the old shape generates successfully with
// the intent silently dropped (e.g. low_cardinality:true no longer marking a
// group-counts column). tacplus therefore fails loud on them itself. The
// replacement is named so the error is actionable. A test pins this table
// against the reserved declarations in sys/spec.proto so it cannot drift.
var reservedSpecOptionFields = map[string]string{
	"engine":          "store-neutral dedup: {strategy: ... version_field: ...}",
	"ttl":             "store-neutral retention: {timestamp_field: ... duration_seconds: ...}",
	"low_cardinality": "cardinality_hint: CARDINALITY_LOW",
	"codec":           "(removed — store decides encodings)",
	"default_expr":    "(removed — store decides defaults)",
}

var reservedFieldRes = func() map[string]*regexp.Regexp {
	res := make(map[string]*regexp.Regexp, len(reservedSpecOptionFields))
	for name := range reservedSpecOptionFields {
		res[name] = regexp.MustCompile(`(^|[\s{])` + name + `\s*:`)
	}
	return res
}()

// specOptionStartRe matches an active (non-commented) spec.* option start, with
// any same-line prefix that contains no comment slash (MS-10 comment-safe form).
var specOptionStartRe = regexp.MustCompile(`(?m)^[^\n/]*?option\s+\(\s*spec\.`)

// ValidateReservedSpecOptionFields hard-errors when any proto under protoDir
// uses a reserved (removed) field name inside a spec.* option literal. Without
// this, protoc silently drops the field and the declared intent vanishes.
func ValidateReservedSpecOptionFields(protoDir string) error {
	var violations []string
	err := filepath.Walk(protoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return err
		}
		rel, rerr := filepath.Rel(protoDir, path)
		if rerr != nil {
			rel = path
		}
		// sys/ is tacplus's own (current) spec surface; only user protos matter.
		if IsInfraProto(filepath.ToSlash(rel)) {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		text := string(data)
		for _, loc := range specOptionStartRe.FindAllStringIndex(text, -1) {
			block, endLine := specOptionBlock(text, loc[0])
			if block == "" {
				continue
			}
			stripped := stripBlockComments(block)
			startLine := 1 + strings.Count(text[:loc[0]], "\n")
			for name, re := range reservedFieldRes {
				if re.MatchString(stripped) {
					violations = append(violations, fmt.Sprintf(
						"%s:%d-%d: spec option uses reserved field %q — protoc silently drops it; use %s",
						rel, startLine, endLine, name, reservedSpecOptionFields[name]))
				}
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(violations) > 0 {
		sort.Strings(violations)
		return fmt.Errorf("reserved spec option fields (silently discarded by protoc — the declared intent would be LOST):\n  %s",
			strings.Join(violations, "\n  "))
	}
	return nil
}

// specOptionBlock returns the full option literal starting at start (through the
// balanced closing brace + semicolon), and the 1-based line the block ends on.
// Returns "" when braces never balance (malformed input — protoc will complain).
func specOptionBlock(text string, start int) (string, int) {
	depth := 0
	inString := false
	for i := start; i < len(text); i++ {
		c := text[i]
		if inString {
			if c == '\\' {
				i++
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end := i + 1
				return text[start:end], 1 + strings.Count(text[:end], "\n")
			}
		case ';':
			if depth == 0 { // scalar option (no braces), e.g. option (spec.x) = true;
				return text[start : i+1], 1 + strings.Count(text[:i+1], "\n")
			}
		}
	}
	return "", 0
}

// stripBlockComments removes // line comments and quoted strings from a block so
// a reserved name appearing in a comment or string value cannot false-match.
func stripBlockComments(block string) string {
	var b strings.Builder
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		if idx := indexLineComment(line); idx >= 0 {
			line = line[:idx]
		}
		b.WriteString(removeQuotedStrings(line))
		if i < len(lines)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func indexLineComment(line string) int {
	inString := false
	for i := 0; i < len(line); i++ {
		switch line[i] {
		case '\\':
			if inString {
				i++
			}
		case '"':
			inString = !inString
		case '/':
			if !inString && i+1 < len(line) && line[i+1] == '/' {
				return i
			}
		}
	}
	return -1
}

func removeQuotedStrings(line string) string {
	var b strings.Builder
	inString := false
	for i := 0; i < len(line); i++ {
		c := line[i]
		if inString {
			if c == '\\' {
				i++
			} else if c == '"' {
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}
