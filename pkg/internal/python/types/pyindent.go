// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"strings"
)

// pyLine writes an indented line to the builder (with trailing newline)
func pyLine(b *strings.Builder, level int, s string) {
	b.WriteString(strings.Repeat("    ", level))
	b.WriteString(s)
	b.WriteString("\n")
}

// pyLinef writes a formatted, indented line to the builder (with trailing newline)
func pyLinef(b *strings.Builder, level int, format string, args ...any) {
	b.WriteString(strings.Repeat("    ", level))
	fmt.Fprintf(b, format, args...)
	b.WriteString("\n")
}
