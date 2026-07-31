// Copyright (c) 2026 Second Star Cavalier LLC

package java

import "testing"

// toJavaFieldName must mirror protobuf-java's UnderscoresToCamelCase, whose key
// non-obvious rule is that a letter immediately following a digit is capitalized.
// A field like requests_per_second_5m must yield requestsPerSecond5M (getter
// getRequestsPerSecond5M); the older split-on-underscore logic produced ...5m,
// which protobuf never emits — a "cannot find symbol" compile error downstream.
func TestToJavaFieldName_DigitBoundaryMatchesProtobuf(t *testing.T) {
	cases := map[string]string{
		"requests_per_second_5m":      "requestsPerSecond5M",
		"request_bytes_per_second_1m": "requestBytesPerSecond1M",
		"utf8_mb4":                    "utf8Mb4",
		"v2beta":                      "v2Beta",
		// no digit boundary — unchanged from the historical behavior:
		"simple_field": "simpleField",
		"id":           "id",
		"oauth2_token": "oauth2Token",
		"http_port":    "httpPort",
	}
	for in, want := range cases {
		if got := toJavaFieldName(in); got != want {
			t.Errorf("toJavaFieldName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestJavaFieldNameUpper_CapitalizesStem(t *testing.T) {
	if got := javaFieldNameUpper("requests_per_second_5m"); got != "RequestsPerSecond5M" {
		t.Fatalf("javaFieldNameUpper = %q, want RequestsPerSecond5M", got)
	}
	if got := javaFieldNameUpper(""); got != "" {
		t.Fatalf("javaFieldNameUpper(\"\") = %q, want empty", got)
	}
}
