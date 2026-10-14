// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"path/filepath"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/strutil"
)

// pyModuleName normalizes a proto filename/package segment into a Python module
// identifier. protoc's Python generator maps '-' in filenames to '_'; generated
// helpers, wrappers, and aggregators must use the same importable spelling.
func pyModuleName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := b.String()
	if out == "" {
		out = "_"
	}
	if out[0] >= '0' && out[0] <= '9' {
		out = "_" + out
	}
	return escapePyKeyword(out)
}

func splitPyPath(path string) []string {
	if path == "" || path == "." {
		return nil
	}
	return strings.FieldsFunc(path, func(r rune) bool {
		return r == '/' || r == '\\'
	})
}

func pyPathParts(path string) []string {
	parts := splitPyPath(path)
	for i, part := range parts {
		parts[i] = pyModuleName(part)
	}
	return parts
}

func pyPath(path string) string {
	parts := pyPathParts(path)
	if len(parts) == 0 {
		return ""
	}
	return filepath.Join(parts...)
}

func pyDottedPath(path string) string {
	return strings.Join(pyPathParts(path), ".")
}

func pyPackageBase(name string) string {
	parts := strings.Split(name, ".")
	for i, part := range parts {
		parts[i] = pyModuleName(part)
	}
	return strings.Join(parts, ".")
}

func pyPackagePath(name string) string {
	parts := strings.Split(pyPackageBase(name), ".")
	if len(parts) == 0 {
		return ""
	}
	return filepath.Join(parts...)
}

func pyJoinDotted(parts ...string) string {
	var clean []string
	for _, part := range parts {
		if part != "" && part != "." {
			clean = append(clean, part)
		}
	}
	return strings.Join(clean, ".")
}

func pyTonicLikeTypeName(protoName string) string {
	runes := []rune(protoName)
	n := len(runes)
	if n == 0 {
		return ""
	}

	const (
		modeBoundary = iota
		modeLowercase
		modeUppercase
	)

	var words []string
	mode := modeBoundary
	init := 0
	for i := 0; i < n; i++ {
		c := runes[i]
		if i+1 >= n {
			continue
		}
		next := runes[i+1]
		nextMode := mode
		if c >= 'a' && c <= 'z' {
			nextMode = modeLowercase
		} else if c >= 'A' && c <= 'Z' {
			nextMode = modeUppercase
		}
		if nextMode == modeLowercase && next >= 'A' && next <= 'Z' {
			words = append(words, string(runes[init:i+1]))
			init = i + 1
			mode = modeBoundary
			continue
		}
		if mode == modeUppercase && c >= 'A' && c <= 'Z' && next >= 'a' && next <= 'z' {
			if init < i {
				words = append(words, string(runes[init:i]))
			}
			init = i
			mode = modeBoundary
			continue
		}
		mode = nextMode
	}
	if init < n {
		words = append(words, string(runes[init:]))
	}

	var result strings.Builder
	for _, word := range words {
		first := true
		for _, r := range word {
			if first && r >= 'a' && r <= 'z' {
				result.WriteRune(r - 'a' + 'A')
				first = false
			} else if !first && r >= 'A' && r <= 'Z' {
				result.WriteRune(r - 'A' + 'a')
			} else {
				if r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' {
					first = false
				}
				result.WriteRune(r)
			}
		}
	}
	return result.String()
}

func pyGeneratedSnakeName(name string) string {
	return strutil.ToSnakeCase(pyTonicLikeTypeName(name))
}
