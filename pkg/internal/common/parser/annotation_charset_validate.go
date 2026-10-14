// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"sort"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// forbiddenAnnotationRune reports why r may not appear in an annotation string,
// or "" if it is allowed. Every generator backend interpolates annotation
// strings into emitted source — into a double-quoted string literal, or into a
// // or /* */ comment. These runes are the ones that can end that literal or
// comment early and let the rest of the annotation be read as code.
func forbiddenAnnotationRune(r rune) string {
	switch {
	case r == '\n' || r == '\r':
		return "line break"
	case r < 0x20 || r == 0x7f:
		return "control character"
	case r == '"':
		return "double quote"
	case r == '\\':
		return "backslash"
	case r == '`':
		return "backtick"
	}
	return ""
}

// checkAnnotationString describes the first breakout character in s, or returns
// "" if s is safe to interpolate into generated source.
func checkAnnotationString(s string) string {
	for _, r := range s {
		if why := forbiddenAnnotationRune(r); why != "" {
			return fmt.Sprintf("%s (%q)", why, r)
		}
	}
	if strings.Contains(s, "*/") {
		return `block comment terminator ("*/")`
	}
	return ""
}

// ValidateAnnotationCharset rejects annotation string values that could break
// out of the generated construct they are interpolated into.
//
// Annotation strings reach the emitted source of five language backends through
// well over a hundred interpolation sites. Most of those sites paste the value
// straight into a double-quoted string literal or a comment, so a value
// carrying a quote, a backslash, a backtick or a line break does not merely
// produce broken output — it can produce code that still compiles and does
// something the .proto author chose. Escaping correctly at each individual site
// is a losing game across five target languages; rejecting the characters once,
// at parse time, closes the whole class before any template runs.
//
// EnumValue is not checked: protoc constrains enum value names to identifiers,
// so they cannot carry these runes. Only the free-form string forms
// (StringValue, StringSliceValue) are shaped by the .proto author.
func ValidateAnnotationCharset(dp *DescriptorParser) error {
	all := dp.GetAllFiles()
	paths := make([]string, 0, len(all))
	for path := range all {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	fds := make([]*FileDescriptor, 0, len(all))
	for _, path := range paths {
		fds = append(fds, all[path])
	}
	return validateAnnotationCharset(fds)
}

// validateAnnotationCharset is the file-list form of ValidateAnnotationCharset;
// fds must already be in a deterministic order so the error text is stable.
func validateAnnotationCharset(fds []*FileDescriptor) error {
	var errs []string
	for _, fd := range fds {
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			errs = append(errs, annotationCharsetErrors(
				fd.Package+"."+msg.Name, msg.Annotations)...)
			for fi := range msg.Fields {
				field := &msg.Fields[fi]
				errs = append(errs, annotationCharsetErrors(
					fd.Package+"."+msg.Name+"."+field.Name, field.Annotations)...)
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("annotation charset errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}

// annotationCharsetErrors checks every string-valued annotation on one message
// or field, walking keys in sorted order so the error text is stable.
func annotationCharsetErrors(where string, anns map[string]model.AnnotationValue) []string {
	if len(anns) == 0 {
		return nil
	}
	keys := make([]string, 0, len(anns))
	for key := range anns {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var errs []string
	for _, key := range keys {
		val := anns[key]
		if why := checkAnnotationString(val.StringValue); why != "" {
			errs = append(errs, fmt.Sprintf(
				"%s annotation (%s) value %q contains a %s, which can break out of the generated string literal or comment it is emitted into",
				where, key, val.StringValue, why))
		}
		for _, sv := range val.StringSliceValue {
			if why := checkAnnotationString(sv); why != "" {
				errs = append(errs, fmt.Sprintf(
					"%s annotation (%s) value %q contains a %s, which can break out of the generated string literal or comment it is emitted into",
					where, key, sv, why))
			}
		}
	}
	return errs
}
