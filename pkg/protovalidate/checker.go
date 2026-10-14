// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package protovalidate

import (
	"fmt"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
)

// blockedFieldNames contains proto field names that conflict with generated
// method names or language keywords across Go, Rust, Python, Java, and
// TypeScript. Using these as field names causes compilation errors in at
// least one target language.
var blockedFieldNames = map[string]string{
	// Go protobuf generated methods — these shadow required interface methods.
	"reset":         "Go: conflicts with proto Reset() method",
	"string":        "Go: conflicts with proto String() method",
	"proto_message": "Go: conflicts with proto ProtoMessage() method",
	"proto_reflect": "Go: conflicts with proto ProtoReflect() method",

	// Common nouns that are language keywords — likely to appear as field names.
	"type":      "keyword in Go, Rust, Python, TypeScript",
	"class":     "keyword in Python, Java, TypeScript",
	"self":      "keyword in Rust, Python",
	"super":     "keyword in Rust, Java, TypeScript",
	"new":       "keyword in Java, TypeScript",
	"default":   "keyword in Go, Java, TypeScript, Rust",
	"enum":      "keyword in Rust, Java, TypeScript",
	"interface": "keyword in Go, Java, TypeScript",
	"struct":    "keyword in Go, Rust",
	"match":     "keyword in Rust",
	"mod":       "keyword in Rust",
	"pub":       "keyword in Rust",
	"use":       "keyword in Rust",
	"ref":       "keyword in Rust",
	"fn":        "keyword in Rust",
	"impl":      "keyword in Rust",
	"trait":     "keyword in Rust",
	"async":     "keyword in Rust, Python, TypeScript",
	"await":     "keyword in Rust, Python, TypeScript",
	"yield":     "keyword in Rust, Python, TypeScript",
	"import":    "keyword in Go, Python, Java, TypeScript",
	"from":      "keyword in Python, TypeScript",
	"with":      "keyword in Python, TypeScript",
	"del":       "keyword in Python",
	"is":        "keyword in Python",
	"not":       "keyword in Python",
	"and":       "keyword in Python",
	"or":        "keyword in Python",
	"var":       "keyword in Go, TypeScript",
	"let":       "keyword in Rust, TypeScript",
	"const":     "keyword in Go, Rust, Java, TypeScript",
	"static":    "keyword in Rust, Java, TypeScript",
	"void":      "keyword in Java, TypeScript",
	"final":     "keyword in Java, Rust",
	"abstract":  "keyword in Java, TypeScript",
	"map":       "keyword in Go",
	"chan":      "keyword in Go",
	"go":        "keyword in Go",
	"select":    "keyword in Go",
	"range":     "keyword in Go",
	"defer":     "keyword in Go",
	"move":      "keyword in Rust",
	"mut":       "keyword in Rust",
	"crate":     "keyword in Rust",
	"dyn":       "keyword in Rust",
	"where":     "keyword in Rust",
	"unsafe":    "keyword in Rust",
	"extern":    "keyword in Rust",
	"loop":      "keyword in Rust",
	"as":        "keyword in Rust, Python, TypeScript",
	"in":        "keyword in Rust, Python, TypeScript",
}

// ValidateProtoDirectory parses all proto files in the directory and checks
// for basic validity (syntax, required package declarations, blocked field
// names, infrastructure field numbers, etc.).
// Returns the number of proto files found and any validation error.
func ValidateProtoDirectory(protoDir string) (int, error) {
	protos, err := parser.ParseProtoDirectoryDetailed(protoDir)
	if err != nil {
		return 0, err
	}

	if len(protos) == 0 {
		return 0, fmt.Errorf("no proto files found in %s", protoDir)
	}

	var errs []string
	for path, proto := range protos {
		if proto.Package == "" {
			errs = append(errs, fmt.Sprintf("%s: missing package declaration", path))
		}
		for _, msg := range proto.Messages {
			for _, field := range msg.Fields {
				if reason, ok := blockedFieldNames[field.Name]; ok {
					errs = append(errs, fmt.Sprintf(
						"%s: field %q in message %s is a blocked name (%s) — rename the field and use json_name to preserve wire format",
						path, field.Name, msg.Name, reason))
				}
				if field.Number >= 200 {
					errs = append(errs, fmt.Sprintf(
						"%s: field %q in message %s uses field number %d; field numbers >= 200 are used by tacplus infrastructure",
						path, field.Name, msg.Name, field.Number))
				}
			}
		}
	}

	if len(errs) > 0 {
		return len(protos), fmt.Errorf("validation errors:\n %s", strings.Join(errs, "\n "))
	}

	return len(protos), nil
}
