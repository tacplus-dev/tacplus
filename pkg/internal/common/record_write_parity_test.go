// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package common

import (
	"os"
	"testing"
)

// The record write surface's guards (#163) across the three full languages:
// the same refusals, spelled the same way, in every generator. A caller of the
// record store reads these messages, and which language hosts the store must
// not change what it reads. Behaviour is driven per language by the
// proto-recordegress host leg of cross-test/current-generation; this keeps a
// reworded or dropped refusal from passing one language's leg alone.
var recordWriteGenerators = map[string][]string{
	"go":   {"../go/types/recordwrite_gen.go"},
	"java": {"../java/types/recordwrite_gen.go"},
	"rust": {"../rust/types/recordwrite_gen.go"},
}

func TestRecordWriteGuardsReadTheSameInEveryLanguage(t *testing.T) {
	srcs := make(map[string]string, len(recordWriteGenerators))
	for lang, paths := range recordWriteGenerators {
		for _, p := range paths {
			src, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("%s: read %s: %v", lang, p, err)
			}
			srcs[lang] += string(src) + "\n"
		}
	}

	for what, want := range map[string]string{
		"durability floor refusal":   "asks for a durability below the record type's declared floor ",
		"durability floor rule":      "a write may raise its type's durability, never lower it",
		"ack below the owed level":   "the record store acked the write below the durability the request required; a write reported durable must be",
		"Apply op with no case":      "names no operation",
		"Apply floor":                "an Apply is acked at the highest level its ops involve, never lower",
		"Apply lock order":           "is out of canonical lock order (record type full name, then key, each entry once)",
		"Apply short answer":         "Apply: the record store answered ",
		"Apply ack below owed level": "below the durability the request required; a write reported durable must be",
		"tenant refusal":             "names a tenant the caller neither owns nor is linked to; the tenant boundary is not the caller's to choose",
		"tenant claim missing":       "addresses entries held to a tenant and the caller presented no verified namespace claim",
		"/system peer exemption":     "/system",
	} {
		wantInEveryLanguage(t, srcs, what, want)
	}
}
