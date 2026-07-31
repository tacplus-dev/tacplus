// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"strings"
	"testing"
)

func TestInjectRedactIndicatorText_InsertsBeforeClose(t *testing.T) {
	src := `syntax = "proto3";
package demo;

message Event {
  string id = 1;
  string email = 2 [(spec.redact) = {mode: REDACT_MODE_MAY_ANONYMIZE}];
  string ssn = 3 [(spec.redact) = {mode: REDACT_MODE_DROP}];
}

message Other {
  int64 n = 1;
}
`
	byMsg := map[string][]redactIndicator{
		"Event": {{name: "email_redacted", number: 50002}, {name: "ssn_redacted", number: 50003}},
	}
	got, err := injectRedactIndicatorText(src, byMsg)
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	for _, want := range []string{"bool email_redacted = 50002;", "bool ssn_redacted = 50003;"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n---\n%s", want, got)
		}
	}
	// The indicators must land inside Event, before its closing brace — i.e. before
	// the `message Other` line.
	if strings.Index(got, "email_redacted") > strings.Index(got, "message Other") {
		t.Errorf("indicators leaked past Event's closing brace:\n%s", got)
	}
	// Other has no injections and must be untouched.
	if strings.Count(got, "bool ") != 2 {
		t.Errorf("expected exactly 2 injected bools, got:\n%s", got)
	}
}

// A multi-line field option block closes with an INDENTED brace, which must not be
// mistaken for the message's own closing brace.
func TestInjectRedactIndicatorText_IgnoresIndentedOptionBrace(t *testing.T) {
	src := `message Event {
  string email = 1 [(spec.redact) = {
    mode: REDACT_MODE_DROP
  }];
  string tail = 2;
}
`
	byMsg := map[string][]redactIndicator{
		"Event": {{name: "email_redacted", number: 50001}},
	}
	got, err := injectRedactIndicatorText(src, byMsg)
	if err != nil {
		t.Fatalf("inject: %v", err)
	}
	// The injected bool must appear AFTER `string tail = 2;`, i.e. at the real close,
	// not after the indented option-block brace (which precedes tail).
	if strings.Index(got, "email_redacted") < strings.Index(got, "string tail = 2;") {
		t.Errorf("injected before the real message close (confused by option brace):\n%s", got)
	}
}

func TestInjectRedactIndicatorText_MissingMessageFailsLoud(t *testing.T) {
	src := "message Present {\n  string a = 1;\n}\n"
	byMsg := map[string][]redactIndicator{
		"Absent": {{name: "a_redacted", number: 50001}},
	}
	if _, err := injectRedactIndicatorText(src, byMsg); err == nil {
		t.Fatal("expected a loud error when a target message is not found")
	}
}
