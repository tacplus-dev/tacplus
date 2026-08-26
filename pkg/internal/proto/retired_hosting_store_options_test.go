// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// The hosting-store options are gone from sys/spec.proto: graph_projection
// (60066, graph store support removed) and timeseries_projection (60067, split
// into spec.transformation + (spec.record).retention). PLAN item 12 asks for a
// control proving they cannot come back.
//
// There is no generator-level "no-hosting-store validator" and (per
// BONEYARD_PROGRESS.md / Q1) deliberately none is being written: switching one
// on would red the fleet build on timeseries_projection sites that still
// legitimately exist pre-transformation-split. The control is protoc itself —
// see the two tests below for the two halves of it.
var retiredHostingStoreOptions = []struct {
	option string // spec option name
	number int    // its retired extension number in sys/spec.proto
	body   string // a plausible option literal for it
}{
	{"graph_projection", 60066, `{name: "Thing"}`},
	{"timeseries_projection", 60067, `{name: "ThingRow"}`},
}

// Half one, the consumer side: a proto that declares a retired hosting-store
// option is rejected by protoc, at parse time, before any tacplus code runs.
// The extension no longer exists, so the option cannot resolve.
//
// NOTE for anyone reading item 12's older wording: this is NOT protoc's
// reserved-FIELD-NUMBER error. Extension numbers live on the extendee
// (google.protobuf.MessageOptions), which tacplus does not own and cannot add
// `reserved` to, and `reserved` is not a legal statement inside an `extend`
// block at all. The rejection that actually fires is "Option
// "(spec.<name>)" unknown." — same outcome (protoc refuses to compile), fired
// at the same point (parse, every consumer, no generator awareness needed).
func TestRetiredHostingStoreOptionsRejectedByProtoc(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skipf("protoc not available: %v", err)
	}
	for _, tc := range retiredHostingStoreOptions {
		t.Run(tc.option, func(t *testing.T) {
			dir := t.TempDir()
			if err := CopySysProtos(dir); err != nil {
				t.Fatalf("CopySysProtos: %v", err)
			}
			src := filepath.Join(dir, "retired.proto")
			if err := os.WriteFile(src, []byte(`syntax = "proto3";

package retired;

import "sys/spec.proto";

message Thing {
  option (spec.`+tc.option+`) = `+tc.body+`;
  string id = 1;
}
`), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			cmd := exec.Command("protoc", "-I", dir, "--descriptor_set_out="+filepath.Join(dir, "out.pb"), src)
			out, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("protoc ACCEPTED a retired (spec.%s) option — the retirement is not enforced:\n%s", tc.option, out)
			}
			if want := `Option "(spec.` + tc.option + `)" unknown`; !strings.Contains(string(out), want) {
				t.Fatalf("protoc failed, but not on the retired option (want %q):\n%s", want, out)
			}
			t.Logf("protoc rejected (spec.%s): %s", tc.option, strings.TrimSpace(string(out)))
		})
	}
}

// Half two, the declaration side: a protoc-free pin that sys/spec.proto still
// carries no declaration of either retired name or number. protoc cannot catch
// a re-declaration here — nothing reserves the number on the extendee — so this
// is where a revert of the retirement gets caught, and it runs even where
// protoc is absent.
func TestRetiredHostingStoreOptionsStayUndeclared(t *testing.T) {
	specPath := filepath.Join("..", "..", "proto", "sys", "spec.proto")
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("ReadFile %s: %v", specPath, err)
	}
	// Both retired options extended google.protobuf.MessageOptions, and an
	// extension number is only unique per extendee — 60066 is legitimately live
	// on FieldOptions (timeseries_column). Scope the pin to the block they came
	// from, then strip // comments: the retirement is RECORDED in comments
	// naming both the name and the number, so an unstripped search would match
	// its own tombstone.
	block := messageOptionsExtendBlock(t, string(raw))
	var live strings.Builder
	for _, line := range strings.Split(block, "\n") {
		if i := strings.Index(line, "//"); i >= 0 {
			line = line[:i]
		}
		live.WriteString(line)
		live.WriteString("\n")
	}
	body := live.String()

	for _, tc := range retiredHostingStoreOptions {
		if strings.Contains(body, tc.option) {
			t.Errorf("sys/spec.proto declares retired option %q again", tc.option)
		}
		// `<Type> <name> = <number>;` — anything taking the number back.
		re := regexp.MustCompile(`=\s*` + strconv.Itoa(tc.number) + `\s*[;\[]`)
		if re.MatchString(body) {
			t.Errorf("sys/spec.proto re-uses retired MessageOptions extension number %d", tc.number)
		}
	}
}

// messageOptionsExtendBlock returns the body of the single
// `extend google.protobuf.MessageOptions { ... }` block in spec.proto.
func messageOptionsExtendBlock(t *testing.T, src string) string {
	t.Helper()
	const open = "extend google.protobuf.MessageOptions {"
	i := strings.Index(src, open)
	if i < 0 {
		t.Fatalf("sys/spec.proto has no %q block", open)
	}
	rest := src[i+len(open):]
	end := strings.Index(rest, "\n}")
	if end < 0 {
		t.Fatal("unterminated extend google.protobuf.MessageOptions block")
	}
	return rest[:end]
}
