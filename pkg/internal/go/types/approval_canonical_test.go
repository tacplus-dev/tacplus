// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/structpb"

	approvalchassis "gitlab.com/seabeam/schematic/tacplus/pkg/internal/go/types/testdata/approvalchassis"
	spec "gitlab.com/seabeam/schematic/tacplus/target/proto/spec/sys"
	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// The approval canonicalization is emitted into every generated project as a
// chassis package, so its BEHAVIOR cannot be exercised from here directly.
// testdata/approvalchassis is that exact source, compiled into this module so the
// tests below run the real algorithm; this test is the drift guard that keeps the
// two byte-identical. If it fails, copy the template over the testdata file —
// never edit the testdata copy on its own.
func TestApprovalChassisTestdataMatchesTemplate(t *testing.T) {
	want, err := templates.FS.ReadFile("go/chassis/approval/approval.go.tmpl")
	if err != nil {
		t.Fatalf("read template: %v", err)
	}
	got, err := os.ReadFile("testdata/approvalchassis/approval.go")
	if err != nil {
		t.Fatalf("read testdata copy: %v", err)
	}
	if !bytes.Equal(want, got) {
		t.Fatal("testdata/approvalchassis/approval.go has drifted from " +
			"pkg/templates/go/chassis/approval/approval.go.tmpl; the behaviour tests would be " +
			"proving something the generated chassis does not do. Re-copy the template over it.")
	}
}

// Determinism: the same logical request hashes to the same bytes every time,
// however it was built. Both ends of a requires_device call compute this
// independently, so a hash that depends on construction order or on a runtime's
// field-iteration order is a production break that passes every same-language
// test of a single object.
func TestApprovalRequestHashIsDeterministic(t *testing.T) {
	// Built field-by-field in DECLARED order.
	a := &spec.RpcAnnotation{}
	a.Principal = spec.RpcPrincipal_RPC_PRINCIPAL_USER
	a.AuthServices = []string{"alpha", "beta"}
	a.TargetNamespaceField = "tenant_namespace"
	a.AuthActors = []string{"delegate"}
	a.RequiresDevice = true

	// A fresh object, same values, populated in REVERSE order — and then again
	// through the reflective setter in descending field-number order, which is
	// the order the canonicalization must not inherit.
	b := &spec.RpcAnnotation{}
	b.RequiresDevice = true
	b.AuthActors = []string{"delegate"}
	b.TargetNamespaceField = "tenant_namespace"
	b.AuthServices = []string{"alpha", "beta"}
	b.Principal = spec.RpcPrincipal_RPC_PRINCIPAL_USER

	ha, err := approvalchassis.RequestHash(a)
	if err != nil {
		t.Fatalf("hash a: %v", err)
	}
	hb, err := approvalchassis.RequestHash(b)
	if err != nil {
		t.Fatalf("hash b: %v", err)
	}
	if !bytes.Equal(ha, hb) {
		t.Fatalf("two identical requests hashed differently: %x vs %x", ha, hb)
	}
	if len(ha) != 32 {
		t.Fatalf("request_hash must be 32 bytes (kernel's only stated constraint), got %d", len(ha))
	}
	// Re-hashing the same object must not drift either (no map iteration, no
	// cached serialization state leaking in).
	again, err := approvalchassis.RequestHash(a)
	if err != nil {
		t.Fatalf("re-hash a: %v", err)
	}
	if !bytes.Equal(ha, again) {
		t.Fatalf("hashing the same object twice differed: %x vs %x", ha, again)
	}
	// A different request must not collide.
	c := &spec.RpcAnnotation{Principal: spec.RpcPrincipal_RPC_PRINCIPAL_USER, AuthServices: []string{"beta", "alpha"}}
	hc, err := approvalchassis.RequestHash(c)
	if err != nil {
		t.Fatalf("hash c: %v", err)
	}
	if bytes.Equal(ha, hc) {
		t.Fatal("a different request produced the same hash")
	}
}

// Golden: the canonical bytes are field-number ascending, wire-type encoded, and
// recurse into nested messages under the same rule — regardless of where a field
// sits in the Go struct or in the proto source. The expected value is written out
// by hand so a change to the algorithm cannot quietly redefine "canonical".
func TestApprovalCanonicalBytesGolden(t *testing.T) {
	msg := &spec.Error{
		Code:    7,
		Message: "nope",
		Details: []*anypb.Any{
			{TypeUrl: "t"},
			{Value: []byte{0x01, 0x02}},
		},
	}
	// field 1 (varint)       : 08 07
	// field 2 (len-delim)    : 12 04 "nope"
	// field 3 (len-delim) x2, in the request's given order:
	//   Any{type_url:"t"}    : 1a 03 | 0a 01 74
	//   Any{value:01 02}     : 1a 04 | 12 02 01 02
	want := "0807" + "12046e6f7065" + "1a030a0174" + "1a0412020102"

	got, err := approvalchassis.CanonicalBytes(msg)
	if err != nil {
		t.Fatalf("CanonicalBytes: %v", err)
	}
	if hex.EncodeToString(got) != want {
		t.Fatalf("canonical bytes are not the declared canonicalization:\n got %s\nwant %s", hex.EncodeToString(got), want)
	}

	// The same message assembled in a different order yields the same bytes: the
	// ordering is the field numbers', not the assembly's.
	reordered := &spec.Error{}
	reordered.Details = []*anypb.Any{{TypeUrl: "t"}, {Value: []byte{0x01, 0x02}}}
	reordered.Message = "nope"
	reordered.Code = 7
	regot, err := approvalchassis.CanonicalBytes(reordered)
	if err != nil {
		t.Fatalf("CanonicalBytes (reordered): %v", err)
	}
	if !bytes.Equal(got, regot) {
		t.Fatalf("assembly order changed the canonical bytes:\n%s\n%s", hex.EncodeToString(got), hex.EncodeToString(regot))
	}

	// Repeated-field ORDER is meaningful data, so reversing it must change the
	// bytes (otherwise two different requests would share one approval).
	swapped := &spec.Error{Code: 7, Message: "nope", Details: []*anypb.Any{{Value: []byte{0x01, 0x02}}, {TypeUrl: "t"}}}
	swgot, err := approvalchassis.CanonicalBytes(swapped)
	if err != nil {
		t.Fatalf("CanonicalBytes (swapped): %v", err)
	}
	if bytes.Equal(got, swgot) {
		t.Fatal("reversing a repeated field left the canonical bytes unchanged")
	}
}

// Unset fields contribute nothing: an empty request is empty bytes, and a field
// left at its default is indistinguishable from one never set — the one presence
// rule every language agrees on.
func TestApprovalCanonicalBytesSkipsUnsetFields(t *testing.T) {
	empty, err := approvalchassis.CanonicalBytes(&spec.RpcAnnotation{})
	if err != nil {
		t.Fatalf("CanonicalBytes: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("an empty request must canonicalize to no bytes, got %x", empty)
	}
	explicitDefault, err := approvalchassis.CanonicalBytes(&spec.RpcAnnotation{RequiresDevice: false})
	if err != nil {
		t.Fatalf("CanonicalBytes: %v", err)
	}
	if !bytes.Equal(empty, explicitDefault) {
		t.Fatal("a field set to its default changed the canonical bytes")
	}
}

// A map cannot be canonicalized, which is why the generator refuses to build a
// requires_device RPC whose request reaches one. If one ever does reach the
// runtime it is a generator bug, and the error must SAY so rather than silently
// hashing a map in whatever order this run happened to iterate.
func TestApprovalCanonicalBytesRefusesMaps(t *testing.T) {
	// structpb.Struct is map<string, Value> fields = 1 — a real map-bearing
	// message, so this exercises the refusal rather than asserting it exists.
	withMap, err := structpb.NewStruct(map[string]any{"a": "1", "b": "2"})
	if err != nil {
		t.Fatalf("build struct: %v", err)
	}
	_, err = approvalchassis.CanonicalBytes(withMap)
	if err == nil {
		t.Fatal("a map field was canonicalized; its iteration order is exactly the cross-language nondeterminism this refuses")
	}
	for _, want := range []string{"map", "requires_device", "google.protobuf.Struct.fields"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the map refusal does not mention %q: %v", want, err)
		}
	}
	// A message with the map left EMPTY is not populated, so Range never visits
	// it and the hash is well-defined.
	if _, err := approvalchassis.CanonicalBytes(&structpb.Struct{}); err != nil {
		t.Fatalf("an unpopulated map must not trip the refusal: %v", err)
	}
}
