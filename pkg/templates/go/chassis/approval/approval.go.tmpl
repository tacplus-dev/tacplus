// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0
//
// CODE GENERATED - DO NOT EDIT
// Generated runtime support for (spec.rpc).requires_device — the device-only
// approval check. See looper/decisions/requires-device-hash-transport-2026-09-21.md.
//
// Two things live here, and BOTH ends of a requires_device call use these same
// functions: the generated server check and the generated caller-side helper.
//
//  1. RequestHash — the canonical hash of a request message. It is NOT
//     proto.Marshal: protobuf wire serialization is not canonical across
//     languages or even across runs of one language (field ordering, unknown
//     fields, map iteration order), so relying on a "deterministic marshal"
//     is a break that passes every same-language test. The algorithm here is
//     ours: populated fields only, in ASCENDING FIELD-NUMBER order, each
//     encoded under protobuf's own wire types, recursing into nested messages
//     under the same rule. Repeated fields keep the request's given order
//     (repeated order is meaningful data). Maps are refused — map iteration
//     order is exactly the cross-language nondeterminism this exists to avoid,
//     and the generator refuses to build a requires_device RPC whose request
//     contains one, so reaching that error here is a generator bug.
//
//  2. The two metadata keys. approval_id and device_assertion travel as CALL
//     METADATA, never as request fields: kernel requires that whatever carries
//     them is outside the hashed bytes, and metadata is outside the request
//     message by construction.
package approval

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"sort"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	// MetadataApprovalID carries the pending approval's opaque id.
	MetadataApprovalID = "x-tacplus-approval-id"
	// MetadataDeviceAssertion carries the serialized WebauthnAssertion. The
	// -bin suffix is the gRPC convention for a non-UTF8 metadata value; the
	// gRPC runtime base64s it on the wire and hands it back as raw bytes.
	MetadataDeviceAssertion = "x-tacplus-device-assertion-bin"
)

// RequestHash returns SHA-256 over the canonical bytes of m: 32 bytes, which is
// kernel's only stated constraint on request_hash. Both ends of a
// requires_device call compute it through this one function.
func RequestHash(m protoreflect.ProtoMessage) ([]byte, error) {
	b, err := CanonicalBytes(m)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	return sum[:], nil
}

// CanonicalBytes renders m under the canonicalization described at the top of
// this file. It is exported so a test can assert on the bytes themselves rather
// than only on the digest.
func CanonicalBytes(m protoreflect.ProtoMessage) ([]byte, error) {
	if m == nil {
		return nil, fmt.Errorf("approval: cannot hash a nil request")
	}
	// A typed-nil pointer is a non-nil interface, so the nil case is checked on
	// the reflected message rather than only on the interface value.
	rm := m.ProtoReflect()
	if !rm.IsValid() {
		return nil, fmt.Errorf("approval: cannot hash a nil request")
	}
	return appendMessage(nil, rm)
}

// appendMessage appends msg's canonical bytes. Range visits only POPULATED
// fields and in an unspecified order, so the fields are collected and sorted by
// field number before anything is written — the ordering is ours, not the
// runtime's.
func appendMessage(dst []byte, msg protoreflect.Message) ([]byte, error) {
	type populated struct {
		fd protoreflect.FieldDescriptor
		v  protoreflect.Value
	}
	var fields []populated
	msg.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		fields = append(fields, populated{fd: fd, v: v})
		return true
	})
	sort.Slice(fields, func(i, j int) bool {
		return fields[i].fd.Number() < fields[j].fd.Number()
	})
	var err error
	for _, f := range fields {
		dst, err = appendField(dst, f.fd, f.v)
		if err != nil {
			return nil, err
		}
	}
	return dst, nil
}

// appendField appends one populated field. A repeated field emits one
// tag+value per element in the request's given order — never the packed
// encoding, which would make the bytes depend on a runtime's packing choice.
func appendField(dst []byte, fd protoreflect.FieldDescriptor, v protoreflect.Value) ([]byte, error) {
	switch {
	case fd.IsMap():
		return nil, fmt.Errorf("approval: field %q is a map; a requires_device request may not contain one (generator bug: this build should have been refused)", fd.FullName())
	case fd.IsList():
		list := v.List()
		var err error
		for i := 0; i < list.Len(); i++ {
			dst, err = appendSingular(dst, fd, list.Get(i))
			if err != nil {
				return nil, err
			}
		}
		return dst, nil
	default:
		return appendSingular(dst, fd, v)
	}
}

// appendSingular appends one value under fd's wire type.
func appendSingular(dst []byte, fd protoreflect.FieldDescriptor, v protoreflect.Value) ([]byte, error) {
	num := fd.Number()
	switch fd.Kind() {
	case protoreflect.BoolKind:
		n := uint64(0)
		if v.Bool() {
			n = 1
		}
		return protowire.AppendVarint(protowire.AppendTag(dst, num, protowire.VarintType), n), nil
	case protoreflect.EnumKind:
		return protowire.AppendVarint(protowire.AppendTag(dst, num, protowire.VarintType), uint64(v.Enum())), nil
	case protoreflect.Int32Kind, protoreflect.Int64Kind:
		return protowire.AppendVarint(protowire.AppendTag(dst, num, protowire.VarintType), uint64(v.Int())), nil
	case protoreflect.Uint32Kind, protoreflect.Uint64Kind:
		return protowire.AppendVarint(protowire.AppendTag(dst, num, protowire.VarintType), v.Uint()), nil
	case protoreflect.Sint32Kind, protoreflect.Sint64Kind:
		return protowire.AppendVarint(protowire.AppendTag(dst, num, protowire.VarintType), protowire.EncodeZigZag(v.Int())), nil
	case protoreflect.Fixed32Kind:
		return protowire.AppendFixed32(protowire.AppendTag(dst, num, protowire.Fixed32Type), uint32(v.Uint())), nil
	case protoreflect.Sfixed32Kind:
		return protowire.AppendFixed32(protowire.AppendTag(dst, num, protowire.Fixed32Type), uint32(v.Int())), nil
	case protoreflect.FloatKind:
		return protowire.AppendFixed32(protowire.AppendTag(dst, num, protowire.Fixed32Type), math.Float32bits(float32(v.Float()))), nil
	case protoreflect.Fixed64Kind:
		return protowire.AppendFixed64(protowire.AppendTag(dst, num, protowire.Fixed64Type), v.Uint()), nil
	case protoreflect.Sfixed64Kind:
		return protowire.AppendFixed64(protowire.AppendTag(dst, num, protowire.Fixed64Type), uint64(v.Int())), nil
	case protoreflect.DoubleKind:
		return protowire.AppendFixed64(protowire.AppendTag(dst, num, protowire.Fixed64Type), math.Float64bits(v.Float())), nil
	case protoreflect.StringKind:
		return protowire.AppendBytes(protowire.AppendTag(dst, num, protowire.BytesType), []byte(v.String())), nil
	case protoreflect.BytesKind:
		return protowire.AppendBytes(protowire.AppendTag(dst, num, protowire.BytesType), v.Bytes()), nil
	case protoreflect.MessageKind:
		sub, err := appendMessage(nil, v.Message())
		if err != nil {
			return nil, err
		}
		return protowire.AppendBytes(protowire.AppendTag(dst, num, protowire.BytesType), sub), nil
	case protoreflect.GroupKind:
		return nil, fmt.Errorf("approval: field %q is a proto2 group; a requires_device request may not contain one", fd.FullName())
	}
	return nil, fmt.Errorf("approval: field %q has unsupported kind %v", fd.FullName(), fd.Kind())
}

// FromIncomingContext reads the approval id and the serialized WebauthnAssertion
// off the incoming call metadata. Both are mandatory: a requires_device RPC
// arriving without them is refused, never served.
func FromIncomingContext(ctx context.Context) (approvalID string, deviceAssertion []byte, err error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", nil, status.Error(codes.Unauthenticated, "this rpc requires a device approval, but the call carries no metadata")
	}
	ids := md.Get(MetadataApprovalID)
	if len(ids) == 0 || ids[0] == "" {
		return "", nil, status.Errorf(codes.Unauthenticated, "this rpc requires a device approval, but %s is absent", MetadataApprovalID)
	}
	assertions := md.Get(MetadataDeviceAssertion)
	if len(assertions) == 0 || len(assertions[0]) == 0 {
		return "", nil, status.Errorf(codes.Unauthenticated, "this rpc requires a device approval, but %s is absent", MetadataDeviceAssertion)
	}
	return ids[0], []byte(assertions[0]), nil
}

// AttachOutgoing puts the approval id and the serialized WebauthnAssertion on an
// outgoing call. Callers never construct this metadata by hand, the same rule
// that holds for Bearer tokens.
func AttachOutgoing(ctx context.Context, approvalID string, deviceAssertion []byte) context.Context {
	return metadata.AppendToOutgoingContext(ctx,
		MetadataApprovalID, approvalID,
		MetadataDeviceAssertion, string(deviceAssertion))
}
