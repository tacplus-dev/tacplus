// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"strings"
	"testing"
)

// The flock.Meta field is added once, before the entry's closing brace, and
// only to the entries named (tacplus#206).
func TestInjectFlockMetaText(t *testing.T) {
	src := "message Person {\n  string key = 1 [\n    (spec.store_key) = true\n  ];\n}\n\nmessage Other {\n  string x = 1;\n}\n"
	out, err := injectFlockMetaText(src, map[string]bool{"Person": true})
	if err != nil {
		t.Fatal(err)
	}
	decl := "  flock.Meta flock_meta = 100000;"
	if strings.Count(out, decl) != 1 || !strings.Contains(out, decl+"\n}\n\nmessage Other") {
		t.Fatalf("flock_meta not injected into Person alone:\n%s", out)
	}
	again, err := injectFlockMetaText(out, map[string]bool{"Person": true})
	if err != nil {
		t.Fatal(err)
	}
	if again != out {
		t.Fatalf("injection is not idempotent:\n%s", again)
	}
	if _, err := injectFlockMetaText(src, map[string]bool{"Missing": true}); err == nil {
		t.Fatal("a missing entry message was not reported")
	}
}
