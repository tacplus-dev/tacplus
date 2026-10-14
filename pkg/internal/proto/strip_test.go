// Copyright (c) 2026 Seabeam LLC

package proto

import (
	"strings"
	"testing"
)

func TestStripSpecOptions(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name: "removes spec.proto import",
			input: `syntax = "proto3";
package foo;

import "sys/spec.proto";
import "sys/errors.proto";

message Foo {
  string name = 1;
}
`,
			expected: `syntax = "proto3";
package foo;

import "sys/errors.proto";

message Foo {
  string name = 1;
}
`,
		},
		{
			name: "preserves spec.proto import when spec types remain",
			input: `syntax = "proto3";
package foo;

import "sys/spec.proto";

message Foo {
  spec.EncType key_type = 1;
}
`,
			expected: `syntax = "proto3";
package foo;

import "sys/spec.proto";

message Foo {
  spec.EncType key_type = 1;
}
`,
		},
		{
			name: "removes file-level option (spec.*)",
			input: `syntax = "proto3";
package foo;

option (spec.service_state) = "FooState";

message Foo {
  string name = 1;
}
`,
			expected: `syntax = "proto3";
package foo;

message Foo {
  string name = 1;
}
`,
		},
		{
			name: "removes message-level option (spec.*) indented",
			input: `syntax = "proto3";
package foo;

message StateRoot {
  option (spec.service_state) = "FooState";

  string name = 1;
}
`,
			expected: `syntax = "proto3";
package foo;

message StateRoot {

  string name = 1;
}
`,
		},
		{
			name: "strips single field annotation",
			input: `syntax = "proto3";
package foo;

message Foo {
  string user_id = 1 [(spec.assert_required) = true];
}
`,
			expected: `syntax = "proto3";
package foo;

message Foo {
  string user_id = 1;
}
`,
		},
		{
			name: "strips multiple field annotations",
			input: `syntax = "proto3";
package foo;

message Foo {
  string email = 3 [(spec.assert_email) = true, (spec.assert_required) = true];
}
`,
			expected: `syntax = "proto3";
package foo;

message Foo {
  string email = 3;
}
`,
		},
		{
			name: "strips private and combined field annotations",
			input: `syntax = "proto3";
package foo;

message StateRoot {
  TeamRoster roster = 1 [(spec.private) = true];
  Salary salaries = 3 [(spec.assert_required) = true, (spec.private) = true];
}
`,
			expected: `syntax = "proto3";
package foo;

message StateRoot {
  TeamRoster roster = 1;
  Salary salaries = 3;
}
`,
		},
		{
			name: "preserves non-spec options",
			input: `syntax = "proto3";
package foo;

option go_package = "example.com/foo";
option java_package = "com.example.foo";
option (spec.service_state) = "FooState";

message Foo {
  string name = 1;
}
`,
			expected: `syntax = "proto3";
package foo;

option go_package = "example.com/foo";
option java_package = "com.example.foo";

message Foo {
  string name = 1;
}
`,
		},
		{
			name: "preserves non-spec field annotations",
			input: `syntax = "proto3";
package foo;

message Foo {
  string name = 1 [(validate.rules).string.min_len = 1];
}
`,
			expected: `syntax = "proto3";
package foo;

message Foo {
  string name = 1 [(validate.rules).string.min_len = 1];
}
`,
		},
		{
			name: "no double blank lines after removal",
			input: `syntax = "proto3";
package foo;

import "sys/spec.proto";

import "sys/errors.proto";

option (spec.service_state) = "FooState";

message Foo {
  string name = 1;
}
`,
			expected: `syntax = "proto3";
package foo;

import "sys/errors.proto";

message Foo {
  string name = 1;
}
`,
		},
		{
			name: "handles service-level option in service block",
			input: `syntax = "proto3";
package foo;

service FooService {
  option (spec.expose_protocols) = EXPOSED_PROTOCOL_ALL;

  rpc GetFoo (GetFooRequest) returns (GetFooResponse);
}
`,
			expected: `syntax = "proto3";
package foo;

service FooService {

  rpc GetFoo (GetFooRequest) returns (GetFooResponse);
}
`,
		},
		{
			name: "comprehensive real-world example",
			input: `// Copyright (c) 2026 Seabeam LLC

syntax = "proto3";
package baseball;

import "sys/spec.proto";
import "sys/errors.proto";
import "common/ballsports.proto";

option (spec.service_state) = "BaseballState";

message StateRoot {
  option (spec.service_state) = "BaseballState";

  TeamRoster roster = 1 [(spec.persistence) = true];
  SeasonStats stats = 2 [(spec.persistence) = true];
}

message GetTeamRequest {
  string team_id = 1 [(spec.assert_required) = true];
}

message RecordGameRequest {
  string team_id = 1 [(spec.assert_required) = true];
  GameResult game = 2 [(spec.assert_required) = true];
}

service BaseballService {
  option (spec.expose_protocols) = EXPOSED_PROTOCOL_ALL;

  rpc GetTeam (GetTeamRequest) returns (TeamResponse);
}
`,
			expected: `// Copyright (c) 2026 Seabeam LLC

syntax = "proto3";
package baseball;

import "sys/errors.proto";
import "common/ballsports.proto";

message StateRoot {

  TeamRoster roster = 1;
  SeasonStats stats = 2;
}

message GetTeamRequest {
  string team_id = 1;
}

message RecordGameRequest {
  string team_id = 1;
  GameResult game = 2;
}

service BaseballService {

  rpc GetTeam (GetTeamRequest) returns (TeamResponse);
}
`,
		},
		{
			name: "strips state_import annotation",
			input: `message StateRoot {
  football.TeamRoster football_team_roster = 4 [(spec.state_import) = "football.FootballState.roster"];
}
`,
			expected: `message StateRoot {
  football.TeamRoster football_team_roster = 4;
}
`,
		},
		{
			name: "strips indexed annotation",
			input: `message TeamStats {
  string team_id = 1 [(spec.indexed) = true];
  int32 season_year = 2 [(spec.indexed) = true];
  string name = 3;
}
`,
			expected: `message TeamStats {
  string team_id = 1;
  int32 season_year = 2;
  string name = 3;
}
`,
		},
		{
			name: "strips json_return_value from enum values",
			input: `enum JwkUseType {
  JWK_USE_TYPE_UNSPECIFIED = 0;
  JWK_USE_TYPE_SIG= 1 [(spec.json_return_value) = "sig"];
  JWK_USE_TYPE_ENC= 2 [(spec.json_return_value) = "enc"];
}
`,
			expected: `enum JwkUseType {
  JWK_USE_TYPE_UNSPECIFIED = 0;
  JWK_USE_TYPE_SIG= 1;
  JWK_USE_TYPE_ENC= 2;
}
`,
		},
		{
			name: "strips multiple different spec annotations on same field",
			input: `message Config {
  string issuer = 1 [(spec.assert_url) = URL_SCHEME_HTTPS, (spec.assert_required) = true];
  int64 created = 2 [(spec.assert_ge) = 1735689600, (spec.assert_le) = 8045740800];
}
`,
			expected: `message Config {
  string issuer = 1;
  int64 created = 2;
}
`,
		},
		{
			name: "strips string_const option",
			input: `syntax = "proto3";
package jwk;

option (spec.string_const) = { name: "PATH_WELL_KNOWN_OPENID", value: ".well-known/openid-configuration" };

message Foo {
  string name = 1;
}
`,
			expected: `syntax = "proto3";
package jwk;

message Foo {
  string name = 1;
}
`,
		},
		{
			name: "multi-line option block",
			input: `message TokenRequestParams {
  option (spec.generate_from_map) = {
    key_enum: "TokenParamKey"
    strict: false
    validate_required: false
  };

  optional string client_id = 1;
}
`,
			expected: `message TokenRequestParams {

  optional string client_id = 1;
}
`,
		},
		{
			name: "multi-line field annotation (all spec)",
			input: `message TokenRequestParams {
  optional string client_id = 1 [
    (spec.assert_min_size) = 1,
    (spec.param_key) = "TOKEN_PARAM_KEY_CLIENT_ID"
  ];
}
`,
			expected: `message TokenRequestParams {
  optional string client_id = 1;
}
`,
		},
		{
			name: "multi-line field annotation with nested braces",
			input: `message TokenRequestParams {
  optional double exp = 4 [
    (spec.assert_float) = {gt: 0.0},
    (spec.param_key) = "TOKEN_PARAM_KEY_EXP"
  ];
}
`,
			expected: `message TokenRequestParams {
  optional double exp = 4;
}
`,
		},
		{
			name: "comment bracket before annotated field is not multiline annotation",
			input: `// EventTimeRange is a closed-open time window [start_ms, end_ms).
message EventTimeRange {
  int64 start_ms = 1 [(spec.assert_required) = true];
  int64 end_ms = 2 [(spec.assert_required) = true];
}
`,
			expected: `// EventTimeRange is a closed-open time window [start_ms, end_ms).
message EventTimeRange {
  int64 start_ms = 1;
  int64 end_ms = 2;
}
`,
		},
		{
			name: "multi-line field annotations with inline comments",
			input: `message UserProfile {
  int64 created_at = 13 [
    (spec.assert_required) = true,
    (spec.assert_ge) = 1704067200,  // 2024-01-01
    (spec.assert_le) = 8045740800   // 2225-01-01
  ];
  int64 session_timeout = 15 [
    (spec.assert_duration) = DURATION_TYPE_POSITIVE,
    (spec.assert_duration_min) = 300,      // 5 minutes
    (spec.assert_duration_max) = 604800    // 7 days
  ];
}
`,
			expected: `message UserProfile {
  int64 created_at = 13;
  int64 session_timeout = 15;
}
`,
		},
		{
			name:     "empty input",
			input:    "",
			expected: "",
		},
		{
			name: "no spec options",
			input: `syntax = "proto3";
package foo;

message Foo {
  string name = 1;
}
`,
			expected: `syntax = "proto3";
package foo;

message Foo {
  string name = 1;
}
`,
		},
		{
			// A single-line option that is fully closed on its own line but carries a
			// trailing // comment must not be mistaken for an unterminated multi-line
			// block — doing so silently skipped every following line to EOF.
			name: "single-line option with trailing comment keeps following content",
			input: `syntax = "proto3";
package foo;

option (spec.service) = {name: "foo"};  // trailing comment

message Foo {
  string name = 1;
}

service Bar {
  rpc Do(Foo) returns (Foo);
}
`,
			expected: `syntax = "proto3";
package foo;

message Foo {
  string name = 1;
}

service Bar {
  rpc Do(Foo) returns (Foo);
}
`,
		},
		{
			// Same ambiguity, but a later genuine block ends in }; — the skip used to
			// stop there, dropping the content in between instead of running to EOF.
			name: "single-line option with trailing comment does not swallow up to a later block",
			input: `syntax = "proto3";
package foo;

option (spec.service) = {name: "foo"};  // trailing comment

message Keep {
  string a = 1;
}

message Other {
  option (spec.thing) = {
    a: "b"
  };
  string b = 1;
}
`,
			expected: `syntax = "proto3";
package foo;

message Keep {
  string a = 1;
}

message Other {
  string b = 1;
}
`,
		},
		{
			name: "multi-line option block with trailing comment on closing line",
			input: `message Foo {
  option (spec.state) = {
    name: "x"
  };  // closing comment
  string a = 1;
}
`,
			expected: `message Foo {
  string a = 1;
}
`,
		},
		{
			name: "multi-line option block with }; inside opening line comment",
			input: `message Foo {
  option (spec.state) = {  // shape: {...};
    name: "x"
  };
  string a = 1;
}
`,
			expected: `message Foo {
  string a = 1;
}
`,
		},
		{
			name: "single-line option with }; inside a string literal and trailing comment",
			input: `option (spec.string_const) = {name: "X", value: "};"};  // literal brace
message Foo {
  string a = 1;
}
`,
			expected: `message Foo {
  string a = 1;
}
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := StripSpecOptions(tt.input)
			if got != tt.expected {
				t.Errorf("StripSpecOptions() mismatch\n--- got ---\n%s\n--- expected ---\n%s\n--- diff ---\ngot lines:      %d\nexpected lines: %d",
					visualizeWhitespace(got), visualizeWhitespace(tt.expected),
					len(strings.Split(got, "\n")), len(strings.Split(tt.expected, "\n")))

				// Show line-by-line diff
				gotLines := strings.Split(got, "\n")
				expLines := strings.Split(tt.expected, "\n")
				maxLen := len(gotLines)
				if len(expLines) > maxLen {
					maxLen = len(expLines)
				}
				for i := 0; i < maxLen; i++ {
					var g, e string
					if i < len(gotLines) {
						g = gotLines[i]
					} else {
						g = "<missing>"
					}
					if i < len(expLines) {
						e = expLines[i]
					} else {
						e = "<missing>"
					}
					if g != e {
						t.Errorf("  line %d:\n    got:      %q\n    expected: %q", i+1, g, e)
					}
				}
			}
		})
	}
}

// visualizeWhitespace makes whitespace visible for debugging test failures.
func visualizeWhitespace(s string) string {
	s = strings.ReplaceAll(s, "\t", "→")
	return s
}

func TestStripFieldAnnotations(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "single spec annotation",
			input:    `  string user_id = 1 [(spec.assert_required) = true];`,
			expected: `  string user_id = 1;`,
		},
		{
			name:     "multiple spec annotations",
			input:    `  string email = 3 [(spec.assert_email) = true, (spec.assert_required) = true];`,
			expected: `  string email = 3;`,
		},
		{
			name:     "no annotations",
			input:    `  string name = 1;`,
			expected: `  string name = 1;`,
		},
		{
			// Enum-valued field annotation (live: spec.proto eviction_policy=50022).
			name:     "eviction_policy annotation with enum value",
			input:    `  SessionData sessions = 2 [(spec.eviction_policy) = EVICTION_POLICY_LRU];`,
			expected: `  SessionData sessions = 2;`,
		},
		{
			name:     "state_import with string value",
			input:    `  football.TeamRoster roster = 4 [(spec.state_import) = "football.FootballState.roster"];`,
			expected: `  football.TeamRoster roster = 4;`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripFieldAnnotations(tt.input)
			if got != tt.expected {
				t.Errorf("stripFieldAnnotations() = %q, want %q", got, tt.expected)
			}
		})
	}
}
