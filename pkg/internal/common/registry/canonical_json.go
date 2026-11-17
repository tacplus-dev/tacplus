package registry

import (
	"bytes"
	"encoding/json"
	"errors"
)

// canonicalJSON lays out a JSON document in registry.json's canonical format,
// whatever the input's whitespace: each object member and array element on its
// own line, indented two spaces per level; two spaces after each ':'; empty
// objects and arrays as {} and []; no trailing newline. String literals, numbers
// and literals are copied byte for byte, so the escaping is the marshaler's.
//
// Why: protojson's multiline output deliberately adds a second space after ':'
// when a hash of the running binary is odd (protobuf's internal/detrand), so
// the same input gave one layout in one release, or one architecture, and the
// other in the next (#307). This formatter, not the protobuf module or the Go
// toolchain, owns the layout. The format is 2.5.8's, the one protojson wrote
// when the extra space was on. Changing what it writes changes every
// consumer's registry.json: a generated-surface change, never a patch.
func canonicalJSON(src []byte) ([]byte, error) {
	if !json.Valid(src) {
		return nil, errors.New("canonicalJSON: invalid JSON")
	}
	var out bytes.Buffer
	out.Grow(len(src) + len(src)/4)
	depth := 0
	newline := func() {
		out.WriteByte('\n')
		for i := 0; i < depth; i++ {
			out.WriteString("  ")
		}
	}
	// nextSignificant returns the index of the next non-whitespace byte at or after i.
	nextSignificant := func(i int) int {
		for i < len(src) && isJSONSpace(src[i]) {
			i++
		}
		return i
	}
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch {
		case isJSONSpace(c):
		case c == '"':
			start := i
			for i++; src[i] != '"'; i++ {
				if src[i] == '\\' {
					i++
				}
			}
			out.Write(src[start : i+1])
		case c == '{' || c == '[':
			out.WriteByte(c)
			if j := nextSignificant(i + 1); src[j] == '}' || src[j] == ']' {
				out.WriteByte(src[j])
				i = j
				continue
			}
			depth++
			newline()
		case c == '}' || c == ']':
			depth--
			newline()
			out.WriteByte(c)
		case c == ',':
			out.WriteByte(',')
			newline()
		case c == ':':
			out.WriteString(":  ")
		default:
			out.WriteByte(c)
		}
	}
	return out.Bytes(), nil
}

func isJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
