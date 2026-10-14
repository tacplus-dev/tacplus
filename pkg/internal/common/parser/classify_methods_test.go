// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import "testing"

func TestClassifyMethodsSkipsSynthesizedServiceFailures(t *testing.T) {
	var reports int
	ClassifyMethods([]*ServiceInfo{{
		Name:        "DemoStateService",
		IsS2S:       true,
		Synthesized: true,
		Methods: []*MethodInfo{{
			Name:            "WatchConfig",
			ServerStreaming: true,
		}},
	}}, func(_, _ string, _ error) {
		reports++
	})

	if reports != 0 {
		t.Fatalf("ClassifyMethods reported %d synthesized service failure(s), want 0", reports)
	}
}

func TestClassifyMethodsReportsAuthoredServiceFailures(t *testing.T) {
	var reports int
	ClassifyMethods([]*ServiceInfo{{
		Name:  "Authored",
		IsS2S: true,
		Methods: []*MethodInfo{{
			Name: "Callback",
		}},
	}}, func(_, _ string, _ error) {
		reports++
	})

	if reports != 1 {
		t.Fatalf("ClassifyMethods reported %d authored service failure(s), want 1", reports)
	}
}
