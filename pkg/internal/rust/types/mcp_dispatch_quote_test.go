// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The MCP tool table drops the rpc's doc comment — proto-authored text — between
// the quotes of a Rust string literal. Go's strconv.Quote writes a non-printable
// rune as a bare \uXXXX, which is Go escape syntax, not Rust's \u{XXXX}, so a
// comment carrying one used to emit Rust that rustc refuses with "incorrect
// unicode escape sequence". These pin the Rust form.
//
// The rune here is U+200B, not the audit finding's U+00A0: parser.McpToolDescription
// and described() both flatten the comment with strings.Fields, which drops
// every unicode.IsSpace rune — U+00A0 among them. A zero-width space is not
// IsSpace, so it survives the flattening and reaches the literal.

// zwspToolComment is the rpc doc comment under test: an ordinary sentence with
// one U+200B in it.
const zwspToolComment = "GetTicketBrief summarizes one\u200bticket for an assistant."

// TestRustQuoteUsesRustUnicodeEscapes pins rustQuote against the strconv.Quote
// it replaced, on the rune class that separates them.
func TestRustQuoteUsesRustUnicodeEscapes(t *testing.T) {
	cases := []struct{ in, want string }{
		// The common case: ASCII is quoted exactly as before.
		{`plain text`, `"plain text"`},
		{`a"b`, `"a\"b"`},
		{`a\b`, `"a\\b"`},
		{"a\nb\tc\rd", `"a\nb\tc\rd"`},
		// Printable non-ASCII embeds as itself; Rust source is UTF-8.
		{"héllo ✓", `"héllo ✓"`},
		// Non-printable runes take Rust's braced form.
		{"a\u00a0b", `"a\u{a0}b"`},
		{"a\u200bb", `"a\u{200b}b"`},
		{"a\u202eb", `"a\u{202e}b"`},
		{"a\U0001d173b", `"a\u{1d173}b"`},
	}
	for _, tc := range cases {
		if got := rustQuote(tc.in); got != tc.want {
			t.Errorf("rustQuote(%q) = %s, want %s", tc.in, got, tc.want)
		}
	}

	// The control, and the bug this fixes: Go's quoting of the same input is a
	// bare \u00a0, which is not a Rust escape at all.
	if got := strconv.Quote("a\u00a0b"); !strings.Contains(got, `\u00a0`) || strings.Contains(got, `\u{`) {
		t.Fatalf("strconv.Quote no longer produces the Go-only escape this test is the control for: %s", got)
	}
}

// TestRustMcpToolTableQuotesNonPrintableComments runs the real generator over a
// tool whose doc comment carries U+200B and checks both the table's description
// and the schema it is copied into.
func TestRustMcpToolTableQuotesNonPrintableComments(t *testing.T) {
	svc, tool, _, _ := rustMcpTestService()
	tool.Comment = zwspToolComment

	got := mustRustMcpDispatch(t, svc)

	if strings.Contains(got, `\u200b`) {
		t.Errorf("generated Rust carries a Go \\uXXXX escape, which rustc rejects:\n%s", got)
	}
	// The description literal, and the same text inside the JSON schema literal.
	if want := `description: "GetTicketBrief summarizes one\u{200b}ticket for an assistant."`; !strings.Contains(got, want) {
		t.Errorf("generated MCP tool table is missing %q\ngot:\n%s", want, got)
	}
	if want := `summarizes one\u{200b}ticket`; strings.Count(got, want) < 2 {
		t.Errorf("the schema literal did not get the same escaping as the description (%q seen %d times)\ngot:\n%s",
			want, strings.Count(got, want), got)
	}
	// And the raw rune is never embedded: escaped is the whole point.
	if strings.Contains(got, "\u200b") {
		t.Errorf("a raw U+200B survived into the generated Rust:\n%s", got)
	}
}

// TestRustMcpToolTableIsUnchangedForAsciiComments is the non-regression case:
// the ordinary all-ASCII tool renders byte-for-byte what strconv.Quote did.
func TestRustMcpToolTableIsUnchangedForAsciiComments(t *testing.T) {
	svc, tool, _, _ := rustMcpTestService()

	got := mustRustMcpDispatch(t, svc)

	for _, want := range []string{
		`name: "get_ticket_brief"`,
		`description: "` + tool.Comment + `"`,
		`"get_ticket_brief" => self.call_get_ticket_brief(arguments).await,`,
		`if arguments.contains_key("requester")`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ASCII rendering changed; missing %q\ngot:\n%s", want, got)
		}
	}
}

// TestRustMcpToolTableCompiles hands the generated tool table to rustc. The
// paired control is the same table quoted the old way, which must NOT compile —
// so this test cannot pass by compiling something the bug never touched.
func TestRustMcpToolTableCompiles(t *testing.T) {
	if _, err := exec.LookPath("rustc"); err != nil {
		t.Skip("rustc not installed; skipping the compile check")
	}
	svc, tool, _, _ := rustMcpTestService()
	tool.Comment = zwspToolComment

	table := toolTableOf(t, mustRustMcpDispatch(t, svc))
	if err := rustcAccepts(t, table); err != nil {
		t.Fatalf("rustc rejected the generated tool table: %v\n--- table ---\n%s", err, table)
	}

	// The control: the same table with the description quoted by strconv.Quote.
	old := strings.Replace(table, rustQuote(zwspToolComment), strconv.Quote(zwspToolComment), 1)
	if old == table {
		t.Fatalf("could not build the pre-fix control from:\n%s", table)
	}
	if err := rustcAccepts(t, old); err == nil {
		t.Fatalf("rustc accepted the Go-quoted control, so this test proves nothing:\n%s", old)
	}
}

// toolTableOf cuts the `pub struct ...McpTool` declaration and the static table
// that follows it out of the generated module: a self-contained pair of items
// rustc can check without rmcp, tonic or the rest of the crate.
func toolTableOf(t *testing.T, src string) string {
	t.Helper()
	start := strings.Index(src, "pub struct TicketApiMcpTool {")
	if start < 0 {
		t.Fatalf("no tool struct in the generated module:\n%s", src)
	}
	rest := src[start:]
	end := strings.Index(rest, "\n];\n")
	if end < 0 {
		t.Fatalf("no tool table terminator in the generated module:\n%s", rest)
	}
	return rest[:end+len("\n];\n")]
}

// rustcAccepts type-checks one snippet of generated Rust as a library crate.
func rustcAccepts(t *testing.T, src string) error {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tools.rs")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write tools.rs: %v", err)
	}
	out, err := exec.Command("rustc", "--crate-type=lib", "--emit=metadata",
		"-o", filepath.Join(dir, "tools.rmeta"), path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v\n%s", err, out)
	}
	return nil
}
