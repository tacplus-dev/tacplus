// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"regexp"
	"strings"
)

// renderComment turns a proto comment into Markdown. Comments are written as
// plain text, not Markdown, so the text is escaped: what renders is what the
// comment says. Structure is kept where comments use it consistently:
//
//   - blank lines separate paragraphs, and line breaks are kept;
//   - a line starting "- ", "* " or "1. " starts a list item, and indented
//     lines after it continue the item;
//   - other lines indented by two or more spaces are a code block (the
//     examples in spec.proto);
//   - "Example: <text>" on one line renders <text> as code.
//
// Backtick spans in a comment are kept as code spans.
func renderComment(c string) string {
	var b strings.Builder
	var para, code []string
	var items [][]string
	flushPara := func() {
		if len(para) > 0 {
			// Comments are plain text: a line break in one is kept.
			b.WriteString(escapeProse(strings.Join(para, "\n"), "  \n") + "\n\n")
			para = nil
		}
	}
	flushList := func() {
		if len(items) > 0 {
			for _, it := range items {
				b.WriteString("- " + escapeProse(strings.Join(it, "\n"), "  \n    ") + "\n")
			}
			b.WriteString("\n")
			items = nil
		}
	}
	flushCode := func() {
		for len(code) > 0 && code[len(code)-1] == "" {
			code = code[:len(code)-1]
		}
		if len(code) > 0 {
			b.WriteString("```protobuf\n" + strings.Join(dedent(code), "\n") + "\n```\n\n")
			code = nil
		}
	}
	for _, line := range commentLines(c) {
		text := strings.TrimLeft(line, " \t")
		ind := len(line) - len(text)
		switch {
		case text == "":
			flushPara()
			flushList()
			if len(code) > 0 {
				code = append(code, "")
			}
		case len(items) > 0 && ind > 0:
			items[len(items)-1] = append(items[len(items)-1], text)
		case ind >= 2:
			flushPara()
			flushList()
			code = append(code, line)
		case listMarker.MatchString(text):
			flushPara()
			flushCode()
			items = append(items, []string{listMarker.ReplaceAllString(text, "")})
		case len(items) > 0:
			// A lazy continuation of the current list item.
			items[len(items)-1] = append(items[len(items)-1], text)
		case strings.HasPrefix(text, "Example:") && strings.TrimSpace(text[len("Example:"):]) != "" && !strings.Contains(text, "`"):
			flushPara()
			flushCode()
			b.WriteString("Example: `" + strings.TrimSpace(text[len("Example:"):]) + "`\n\n")
		default:
			flushCode()
			para = append(para, text)
		}
	}
	flushPara()
	flushList()
	flushCode()
	return b.String()
}

var listMarker = regexp.MustCompile(`^(?:[-*]|\d+[.)])\s+`)

// commentLines splits a comment as protoc reports it (each line keeps the
// space after "//") and drops that one leading space.
func commentLines(c string) []string {
	lines := strings.Split(strings.TrimRight(c, "\n"), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(strings.TrimPrefix(l, " "), " \t")
	}
	return lines
}

func dedent(lines []string) []string {
	minInd := -1
	for _, l := range lines {
		if t := strings.TrimLeft(l, " "); t != "" {
			if ind := len(l) - len(t); minInd < 0 || ind < minInd {
				minInd = ind
			}
		}
	}
	out := make([]string, len(lines))
	for i, l := range lines {
		if len(l) >= minInd {
			out[i] = l[minInd:]
		}
	}
	return out
}

var proseEscaper = strings.NewReplacer(
	`\`, `\\`, `&`, `&amp;`, `<`, `&lt;`, `>`, `&gt;`,
	`*`, `\*`, `_`, `\_`, `[`, `\[`, `]`, `\]`, `{`, `\{`, `}`, `\}`, `#`, `\#`,
)

// escapeProse escapes Markdown and HTML in comment text, leaving backtick code
// spans as they are, and joins its lines with sep. A line that starts with a
// character Markdown would read as block structure is escaped too.
func escapeProse(s, sep string) string {
	s = mapOutsideCodeSpans(s, proseEscaper.Replace)
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" && strings.ContainsRune("-+=0123456789", rune(l[0])) {
			lines[i] = `\` + l
		}
	}
	return strings.Join(lines, sep)
}

// summary is the first sentence of a comment's first paragraph, escaped, on
// one line.
func summary(c string) string {
	var first []string
	for _, line := range commentLines(c) {
		text := strings.TrimSpace(line)
		if text == "" || strings.HasPrefix(line, "  ") || listMarker.MatchString(text) || strings.HasPrefix(text, "Example:") {
			if len(first) > 0 {
				break
			}
			continue
		}
		first = append(first, text)
	}
	p := strings.Join(first, " ")
	for i := 0; i+2 < len(p); i++ {
		if p[i] == '.' && p[i+1] == ' ' && p[i+2] >= 'A' && p[i+2] <= 'Z' {
			p = p[:i+1]
			break
		}
	}
	return escapeProse(p, " ")
}
