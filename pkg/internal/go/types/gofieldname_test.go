// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import "testing"

// TestToGoFieldName_MatchesProtocGenGo guards that helper field names match
// protoc-gen-go's GoCamelCase, especially at digit boundaries (the bug that broke
// clients/myclient: requests_per_second_5m -> RequestsPerSecond_5M, not ...5m).
func TestToGoFieldName_MatchesProtocGenGo(t *testing.T) {
	cases := map[string]string{
		"requests_per_second_5m":      "RequestsPerSecond_5M",
		"request_bytes_per_second_5m": "RequestBytesPerSecond_5M",
		"tenant_namespace":            "TenantNamespace",
		"gitlab_url":                  "GitlabUrl",
		"events_last_hour":            "EventsLastHour",
		"webhook_active":              "WebhookActive",
		"id":                          "Id",
		"oauth2_token":                "Oauth2Token",
	}
	for in, want := range cases {
		if got := toGoFieldName(in); got != want {
			t.Errorf("toGoFieldName(%q) = %q, want %q", in, got, want)
		}
	}
}
