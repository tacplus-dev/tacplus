// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package proto

import "testing"

// Regression: a service declaring `option (spec.service) = {}` (no explicit
// name:) must still be recognized, with its name defaulting to the proto
// package — same rule as DescriptorParser.parseServiceIdentity. Before this,
// such services were dropped from the generated services.ServiceName enum, so
// S2S authorization resolved them to SERVICE_NAME_UNSPECIFIED and denied every
// token exchange naming them (no bootstrapped cluster could authorize S2S).
func TestParseServiceAnnotation_NameDefaultsToPackage(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		wantName string
		wantOK   bool
	}{
		{
			name:     "empty annotation defaults to package",
			text:     "syntax = \"proto3\";\npackage opa;\noption (spec.service) = {};\n",
			wantName: "opa",
			wantOK:   true,
		},
		{
			name:     "annotation with other fields but no name defaults to package",
			text:     "syntax = \"proto3\";\npackage kernel;\noption (spec.service) = {store: true};\n",
			wantName: "kernel",
			wantOK:   true,
		},
		{
			name:     "explicit name wins over package",
			text:     "syntax = \"proto3\";\npackage kmakersvc;\noption (spec.service) = {name: \"kmaker\"};\n",
			wantName: "kmaker",
			wantOK:   true,
		},
		{
			name:   "no service annotation at all",
			text:   "syntax = \"proto3\";\npackage common;\n",
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok, err := parseServiceAnnotation(tt.text)
			if err != nil {
				t.Fatalf("parseServiceAnnotation: %v", err)
			}
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got != tt.wantName {
				t.Errorf("name = %q, want %q", got, tt.wantName)
			}
		})
	}
}
