// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// The (spec.rpc).requires_device request hash is compared RAW by kernel and
// never re-derived there, so a one-byte disagreement between two languages is a
// silent, total failure of the feature. These are the SAME two constants Go's
// and Java's tests pin (pkg/internal/go/types/approval_canonical_test.go and
// pkg/internal/java/types/approval_canonical_test.go), reused rather than
// recomputed: a Rust bug must show up as "does not match the pinned vector",
// never as "all three languages agree with each other and all three are wrong".
const (
	rustApprovalVectorCanonicalHex = "0a05616c69636510e807180122040001feff2a036f6e652a0374776f320e0a06736861726473106418c0c4073d0000c03f410000000000c05e4048ff01"
	rustApprovalVectorSha256Hex    = "be641d5fcad1ae6e58a9d0adeaba0f2755cc9ebc5bc0ada6ad32a5e5d1b868ba"
)

// rustApprovalVectorProtoFile is the same message shape Go and Java build the
// vector from, spelled in tacplus's own model: varint (string/int32/bool/
// sint32), length-delimited (string/bytes/message), repeated (unpacked,
// order-significant), fixed32 (float), fixed64 (double), and one level of
// message recursion.
func rustApprovalVectorProtoFile() model.ProtoFile {
	f := func(name, typ string, num int, repeated, message bool) model.Field {
		return model.Field{Name: name, Type: typ, Number: num, IsRepeated: repeated, IsMessage: message}
	}
	return model.ProtoFile{
		Package: "approvalvector",
		RelPath: "services/approvalvector/approvalvector.proto",
		Messages: []model.Message{
			{Name: "Nested", Fields: []model.Field{
				f("label", "string", 1, false, false),
				f("count", "int32", 2, false, false),
				f("weight", "int64", 3, false, false),
			}},
			{Name: "Vector", Fields: []model.Field{
				f("subject", "string", 1, false, false),
				f("amount", "int32", 2, false, false),
				f("dry_run", "bool", 3, false, false),
				f("blob", "bytes", 4, false, false),
				f("tags", "string", 5, true, false),
				f("nested", "Nested", 6, false, true),
				f("ratio", "float", 7, false, false),
				f("score", "double", 8, false, false),
				f("delta", "sint32", 9, false, false),
			}},
		},
	}
}

// rustApprovalVectorEmitted renders the canonicalization the GENERATOR emits for
// the vector shape — the functions themselves, without the shared primitives.
func rustApprovalVectorEmitted(t *testing.T) string {
	t.Helper()
	pf := rustApprovalVectorProtoFile()
	plan := &rustApprovalPlan{
		canonical: map[string]bool{
			rustApprovalMsgKey("approvalvector", "Nested"): true,
			rustApprovalMsgKey("approvalvector", "Vector"): true,
		},
		requests: map[string]bool{rustApprovalMsgKey("approvalvector", "Vector"): true},
	}
	var b strings.Builder
	writeApprovalCanonical(&b, pf, []model.ProtoFile{pf}, plan)
	out := b.String()
	if !strings.Contains(out, rustApprovalPrimitives) {
		t.Fatalf("emitted canonicalization does not carry the shared wire primitives:\n%s", out)
	}
	return strings.TrimPrefix(out, rustApprovalPrimitives+"\n")
}

// TestRustApprovalChassisRunsTheGeneratedVector is the link that makes the
// cross-language vector a proof rather than a coincidence.
//
// The Rust chassis carries a self-test that asserts the two pinned constants
// (cargo test -p approval, run by the current-generation gate). That test can
// only prove the GENERATOR's bytes if the code it runs is the generator's own
// output — so this asserts the template contains, verbatim, exactly what
// writeApprovalCanonical emits for the vector shape. Hand-editing the Rust test
// into agreement with the constants would fail here instead of silently
// pinning bytes no generated service ever produces.
func TestRustApprovalChassisRunsTheGeneratedVector(t *testing.T) {
	raw, err := templates.FS.ReadFile("rust/chassis/approval/src/lib.rs.tmpl")
	if err != nil {
		t.Fatalf("read approval chassis template: %v", err)
	}
	src := string(raw)

	if !strings.Contains(src, rustApprovalPrimitives) {
		t.Fatalf("the approval chassis self-test no longer runs the generator's own wire primitives;\n" +
			"the pinned vector would then prove nothing about generated services")
	}
	emitted := rustApprovalVectorEmitted(t)
	for _, fn := range strings.SplitAfter(emitted, "}\n\n") {
		fn = strings.TrimSpace(fn)
		if fn == "" {
			continue
		}
		if !strings.Contains(src, fn) {
			t.Fatalf("the approval chassis self-test does not run the generator's emitted canonicalization.\n"+
				"missing from rust/chassis/approval/src/lib.rs.tmpl:\n%s", fn)
		}
	}
	for _, want := range []string{rustApprovalVectorCanonicalHex, rustApprovalVectorSha256Hex} {
		if !strings.Contains(src, want) {
			t.Fatalf("Rust chassis approval self-test no longer carries the cross-language vector %q;\n"+
				"the three canonicalizations are only provably byte-identical while all three assert it", want)
		}
	}
}

// TestRustApprovalCanonicalShape pins the decisions the bytes depend on, so a
// regression names WHICH rule broke rather than only that a digest moved.
func TestRustApprovalCanonicalShape(t *testing.T) {
	out := rustApprovalVectorEmitted(t)

	// Ascending field number, whatever order the proto declared.
	order := []string{"out, 1", "out, 2", "out, 3", "out, 4", "out, 5", "out, 6", "out, 7", "out, 8", "out, 9"}
	vector := out[strings.Index(out, "pub fn approval_canonical_vector"):]
	at := -1
	for _, tag := range order {
		i := strings.Index(vector, tag)
		if i < 0 {
			t.Fatalf("field tag %q never emitted:\n%s", tag, vector)
		}
		if i < at {
			t.Fatalf("fields are not emitted in ascending field-number order (%q came early):\n%s", tag, vector)
		}
		at = i
	}

	for _, want := range []struct{ why, frag string }{
		{"repeated fields are unpacked, one tag per element, in the given order", "for v in m.tags.iter() {"},
		{"sint32 is zigzagged", "approval_zigzag(m.delta as i64)"},
		{"float is fixed32 over its raw bits", "approval_put_fixed32(out, 7, (m.ratio).to_bits())"},
		{"double is fixed64 over its raw bits", "approval_put_fixed64(out, 8, (m.score).to_bits())"},
		{"int32 sign-extends before the varint", "(m.amount as i64) as u64"},
		{"a nested message is canonicalized under the same rule and written length-delimited", "approval_canonical_nested(v, &mut sub);"},
		{"populated-only: an empty string is not written", "if !m.subject.is_empty() {"},
		{"the request type also gets a hash function", "pub fn approval_request_hash_vector("},
	} {
		if !strings.Contains(out, want.frag) {
			t.Fatalf("%s — expected %q in:\n%s", want.why, want.frag, out)
		}
	}

	// prost's own encoder must never be the source of the bytes.
	for _, forbidden := range []string{"encode_to_vec", "prost::Message::encode"} {
		if strings.Contains(out, forbidden) {
			t.Fatalf("canonicalization leans on prost's encoder (%q); it packs repeated scalars and owes no field ordering", forbidden)
		}
	}
}
