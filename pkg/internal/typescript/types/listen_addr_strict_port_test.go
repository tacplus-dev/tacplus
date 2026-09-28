// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
)

// (spec.assert_listen_addr) validates a "host:port" string. Every other target
// parses the port component whole and rejects a remainder: go
// `strconv.Atoi` (*NumError), java `Integer.parseInt` (NumberFormatException),
// python `int(port_str)` (ValueError), rust `port_str.parse::<i32>()` (Err).
//
// TypeScript used `parseInt(portStr, 10)`, which consumes the leading digit run
// and silently discards the rest, so "8080abc" validated as port 8080 and
// parseListenAddr returned 8080 for it. Both the validator and the parser are
// affected, and the parser's return value feeds real dial targets — a caller
// pasting "127.0.0.1:8080 " past the colon got a silently different address than
// the same config gives every other language.
func TestTypeScriptListenAddrPortParseIsStrict(t *testing.T) {
	dir := t.TempDir()
	if err := generateTypeScriptUtils(&tactype.Config{OutputDir: dir}); err != nil {
		t.Fatalf("generateTypeScriptUtils: %v", err)
	}
	src, err := os.ReadFile(filepath.Join(dir, "utils", "validation.ts"))
	if err != nil {
		t.Fatalf("read generated validation.ts: %v", err)
	}
	rendered := string(src)

	// Both call sites — validateListenAddr and parseListenAddr.
	strict := "const port = /^[+-]?[0-9]+$/.test(portStr) ? Number(portStr) : NaN;"
	if got := strings.Count(rendered, strict); got != 2 {
		t.Errorf("strict port parse appears %d times, want 2 (validateListenAddr and parseListenAddr)", got)
	}

	// The exact pre-fix fragment: a revert reintroduces it.
	if strings.Contains(rendered, "parseInt(portStr, 10)") {
		t.Errorf("generated validation.ts parses the port with parseInt, which ignores trailing input")
	}

	// The range check the strict parse feeds is unchanged.
	for _, want := range []string{
		"if (isNaN(port) || port < 0 || port > 65535) {",
		"export function parseListenAddr(addr: string): ListenAddr {",
	} {
		if !strings.Contains(rendered, want) {
			t.Errorf("generated validation.ts missing %q", want)
		}
	}
}
