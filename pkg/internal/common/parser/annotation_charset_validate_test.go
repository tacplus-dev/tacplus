// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// widgetWithCondValue is a message whose required_if_eq annotation carries the
// given condition string — the annotation the Go and Rust backends interpolate
// into a generated string literal and a generated comment.
func widgetWithCondValue(cond string) *FileDescriptor {
	return &FileDescriptor{
		Path:    "services/widget.proto",
		Package: "widget",
		Messages: []MessageDescriptor{{
			Name: "Widget",
			Fields: []FieldDescriptor{
				{Name: "kind", Type: "string"},
				{Name: "name", Type: "string", Annotations: map[string]model.AnnotationValue{
					"required_if_eq": {Type: model.AnnotationTypeString, StringValue: cond},
				}},
			},
		}},
	}
}

// An ordinary condition — the shape every real .proto uses — must generate.
func TestValidateAnnotationCharsetAcceptsOrdinaryValue(t *testing.T) {
	fds := []*FileDescriptor{widgetWithCondValue("kind=PRIMARY")}
	if err := validateAnnotationCharset(fds); err != nil {
		t.Fatalf("an ordinary required_if_eq condition must generate: %v", err)
	}
}

// A double quote lets the value close the generated string literal early and
// have the remainder read as code: the emitted Go
//
//	if m.Kind == "a" || Pwned() || "b" && m.Name == "" {
//
// still compiles, and calls a function the .proto author named.
func TestValidateAnnotationCharsetRejectsQuoteBreakout(t *testing.T) {
	fds := []*FileDescriptor{widgetWithCondValue(`kind=a" || Pwned() || "b`)}
	err := validateAnnotationCharset(fds)
	if err == nil {
		t.Fatal("a quote-breakout condition must be rejected at parse time, got nil")
	}
	for _, want := range []string{"widget.Widget.name", "required_if_eq", "double quote"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must name %q, got: %v", want, err)
		}
	}
}

// A line break ends the generated // comment early, so the rest of the
// annotation lands in the emitted source as a statement.
func TestValidateAnnotationCharsetRejectsNewlineBreakout(t *testing.T) {
	fds := []*FileDescriptor{widgetWithCondValue("kind=a\n\tPwnedStatement()\n\t// x")}
	err := validateAnnotationCharset(fds)
	if err == nil {
		t.Fatal("a newline-breakout condition must be rejected at parse time, got nil")
	}
	if !strings.Contains(err.Error(), "line break") {
		t.Fatalf("error must name the line break, got: %v", err)
	}
}

// The remaining breakout shapes, on the value forms and the carriers the
// generators actually read: a backslash smuggles an escape sequence, a backtick
// closes a Go raw literal, "*/" closes a block comment, and a control character
// has no legitimate use in an annotation.
func TestValidateAnnotationCharsetRejectsRemainingBreakoutShapes(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
		want  string
	}{
		{"backslash", `kind=a\"`, "backslash"},
		{"backtick", "kind=a`", "backtick"},
		{"block comment", "kind=a*/x", "block comment terminator"},
		{"control character", "kind=a\x00", "control character"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAnnotationCharset([]*FileDescriptor{widgetWithCondValue(tc.value)})
			if err == nil {
				t.Fatalf("%s must be rejected at parse time, got nil", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error must name %q, got: %v", tc.want, err)
			}
		})
	}
}

// Message-level annotations and the repeated string form reach the same
// templates as field-level scalar values, so they are checked too.
func TestValidateAnnotationCharsetChecksMessageAndSliceValues(t *testing.T) {
	fds := []*FileDescriptor{{
		Path:    "services/widget.proto",
		Package: "widget",
		Messages: []MessageDescriptor{{
			Name: "Widget",
			Annotations: map[string]model.AnnotationValue{
				"service_state_label": {
					Type:             model.AnnotationTypeString,
					StringSliceValue: []string{"good", "bad\nlabel"},
				},
			},
		}},
	}}
	err := validateAnnotationCharset(fds)
	if err == nil {
		t.Fatal("a line break in a repeated message annotation must be rejected, got nil")
	}
	for _, want := range []string{"widget.Widget", "service_state_label", "line break"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must name %q, got: %v", want, err)
		}
	}
}
