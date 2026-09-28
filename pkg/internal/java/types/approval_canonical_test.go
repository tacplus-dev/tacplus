// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"encoding/hex"
	"strings"
	"testing"

	approval "gitlab.com/seabeam/schematic/tacplus/pkg/internal/go/types/testdata/approvalchassis"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protodesc"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/descriptorpb"
	"google.golang.org/protobuf/types/dynamicpb"

	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// The (spec.rpc).requires_device request hash is compared RAW by kernel and
// never re-derived there, so a one-byte disagreement between two languages is a
// silent, total failure of the feature. These vectors are the cross-language
// contract: the same logical message, canonicalized by each language's chassis,
// must produce these exact bytes.
//
// The Java half asserts the SAME two constants from the same message shape —
// see pkg/templates/java/chassis/approval/ApprovalCanonicalTest.java.tmpl, which
// the generated-Java self-test gate runs. Changing either constant here without
// changing it there breaks the contract this pair exists to hold.
const (
	approvalVectorCanonicalHex = "0a05616c69636510e807180122040001feff2a036f6e652a0374776f320e0a06736861726473106418c0c4073d0000c03f410000000000c05e4048ff01"
	approvalVectorSha256Hex    = "be641d5fcad1ae6e58a9d0adeaba0f2755cc9ebc5bc0ada6ad32a5e5d1b868ba"
)

// approvalVectorDescriptor is the message shape both languages build the vector
// from, spelled once as a FileDescriptorProto so neither side can drift into a
// differently numbered or differently typed field. It covers every wire type
// the canonicalization has an arm for that a real request plausibly carries:
// varint (string/int32/bool/sint32), length-delimited (string/bytes/message),
// repeated (unpacked, order-significant), fixed32 (float), fixed64 (double),
// and one level of message recursion.
func approvalVectorDescriptor(t *testing.T) protoreflect.MessageDescriptor {
	t.Helper()
	lbl := func(l descriptorpb.FieldDescriptorProto_Label) *descriptorpb.FieldDescriptorProto_Label { return &l }
	typ := func(k descriptorpb.FieldDescriptorProto_Type) *descriptorpb.FieldDescriptorProto_Type { return &k }
	field := func(name string, num int32, k descriptorpb.FieldDescriptorProto_Type, repeated bool) *descriptorpb.FieldDescriptorProto {
		label := lbl(descriptorpb.FieldDescriptorProto_LABEL_OPTIONAL)
		if repeated {
			label = lbl(descriptorpb.FieldDescriptorProto_LABEL_REPEATED)
		}
		return &descriptorpb.FieldDescriptorProto{
			Name: proto.String(name), Number: proto.Int32(num), Type: typ(k), Label: label,
		}
	}
	nested := field("nested", 6, descriptorpb.FieldDescriptorProto_TYPE_MESSAGE, false)
	nested.TypeName = proto.String(".approvalvector.Nested")

	fdp := &descriptorpb.FileDescriptorProto{
		Name:    proto.String("approvalvector.proto"),
		Package: proto.String("approvalvector"),
		Syntax:  proto.String("proto3"),
		MessageType: []*descriptorpb.DescriptorProto{
			{
				Name: proto.String("Nested"),
				Field: []*descriptorpb.FieldDescriptorProto{
					field("label", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING, false),
					field("count", 2, descriptorpb.FieldDescriptorProto_TYPE_INT32, false),
					field("weight", 3, descriptorpb.FieldDescriptorProto_TYPE_INT64, false),
				},
			},
			{
				Name: proto.String("Vector"),
				Field: []*descriptorpb.FieldDescriptorProto{
					field("subject", 1, descriptorpb.FieldDescriptorProto_TYPE_STRING, false),
					field("amount", 2, descriptorpb.FieldDescriptorProto_TYPE_INT32, false),
					field("dry_run", 3, descriptorpb.FieldDescriptorProto_TYPE_BOOL, false),
					field("blob", 4, descriptorpb.FieldDescriptorProto_TYPE_BYTES, false),
					field("tags", 5, descriptorpb.FieldDescriptorProto_TYPE_STRING, true),
					nested,
					field("ratio", 7, descriptorpb.FieldDescriptorProto_TYPE_FLOAT, false),
					field("score", 8, descriptorpb.FieldDescriptorProto_TYPE_DOUBLE, false),
					field("delta", 9, descriptorpb.FieldDescriptorProto_TYPE_SINT32, false),
				},
			},
		},
	}
	fd, err := protodesc.NewFile(fdp, nil)
	if err != nil {
		t.Fatalf("build vector descriptor: %v", err)
	}
	return fd.Messages().ByName("Vector")
}

// approvalVectorMessage populates the vector. The values are deliberately
// awkward — a negative sint32, a non-ASCII-free byte string, two repeated
// entries whose order is meaningful — so a language that packs repeated fields,
// zigzags the wrong way, or canonicalizes NaN differs immediately.
func approvalVectorMessage(t *testing.T) protoreflect.ProtoMessage {
	t.Helper()
	md := approvalVectorDescriptor(t)
	m := dynamicpb.NewMessage(md)
	f := md.Fields()
	m.Set(f.ByName("subject"), protoreflect.ValueOfString("alice"))
	m.Set(f.ByName("amount"), protoreflect.ValueOfInt32(1000))
	m.Set(f.ByName("dry_run"), protoreflect.ValueOfBool(true))
	m.Set(f.ByName("blob"), protoreflect.ValueOfBytes([]byte{0x00, 0x01, 0xfe, 0xff}))

	tags := m.Mutable(f.ByName("tags")).List()
	tags.Append(protoreflect.ValueOfString("one"))
	tags.Append(protoreflect.ValueOfString("two"))

	nf := f.ByName("nested")
	nested := m.NewField(nf).Message()
	nd := nf.Message().Fields()
	nested.Set(nd.ByName("label"), protoreflect.ValueOfString("shards"))
	nested.Set(nd.ByName("count"), protoreflect.ValueOfInt32(100))
	nested.Set(nd.ByName("weight"), protoreflect.ValueOfInt64(123456))
	m.Set(nf, protoreflect.ValueOfMessage(nested))

	m.Set(f.ByName("ratio"), protoreflect.ValueOfFloat32(1.5))
	m.Set(f.ByName("score"), protoreflect.ValueOfFloat64(123.0))
	m.Set(f.ByName("delta"), protoreflect.ValueOfInt32(-128))
	return m
}

// TestApprovalCrossLanguageVector pins the bytes the Java chassis port must
// reproduce. It is the Go half of the cross-language byte-identity contract.
func TestApprovalCrossLanguageVector(t *testing.T) {
	canonical, err := approval.CanonicalBytes(approvalVectorMessage(t))
	if err != nil {
		t.Fatalf("CanonicalBytes: %v", err)
	}
	if got := hex.EncodeToString(canonical); got != approvalVectorCanonicalHex {
		t.Fatalf("canonical bytes drifted from the cross-language vector\n got: %s\nwant: %s", got, approvalVectorCanonicalHex)
	}
	sum, err := approval.RequestHash(approvalVectorMessage(t))
	if err != nil {
		t.Fatalf("RequestHash: %v", err)
	}
	if got := hex.EncodeToString(sum); got != approvalVectorSha256Hex {
		t.Fatalf("request hash drifted from the cross-language vector\n got: %s\nwant: %s", got, approvalVectorSha256Hex)
	}
}

// TestApprovalJavaChassisMatchesTheVector is the drift guard on the Java side of
// the pair: the Java chassis test must carry the same two constants, so a
// change to one that is not made to the other fails here rather than in
// production, where the failure mode is a refused RPC no one can explain.
func TestApprovalJavaChassisMatchesTheVector(t *testing.T) {
	raw, err := templates.FS.ReadFile("java/chassis/approval/ApprovalCanonicalTest.java.tmpl")
	if err != nil {
		t.Fatalf("read ApprovalCanonicalTest.java template: %v", err)
	}
	src := string(raw)
	for _, want := range []string{approvalVectorCanonicalHex, approvalVectorSha256Hex} {
		if !strings.Contains(src, want) {
			t.Fatalf("Java chassis approval test no longer carries the cross-language vector %q;\n"+
				"the Go and Java canonicalizations are only provably byte-identical while both assert it", want)
		}
	}
}
