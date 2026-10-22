// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"

	"google.golang.org/protobuf/reflect/protoreflect"

	spec "github.com/tacplus-dev/tacplus/target/proto/spec/sys"
)

// RequiresDeviceWhenDescriptor carries a method's (spec.rpc).requires_device_when:
// the ONE top-level request field the device-approval gate reads, and the ONE
// value it must hold for the gate to run. Nil on a MethodDescriptor/MethodInfo
// means the rpc declares no conditional gate.
//
// Both halves are resolved here, against the request message's descriptor, so
// every language emits the same comparison from the same answer — Enum/EnumValue
// carries the value's NUMBER rather than a generated Go/Java/Rust constant name,
// which is a spelling each language would otherwise have to re-derive.
type RequiresDeviceWhenDescriptor struct {
	Field  string // top-level request field name, as declared
	Equals string // declared value: the enum value's short name, or the literal string
	IsEnum bool   // the field is an enum field; false means a string field
	// EnumNumber is the declared enum value's number, resolved from the enum's
	// descriptor. Meaningless unless IsEnum.
	EnumNumber int32
}

// HasDeviceGate reports whether this rpc carries the device-approval gate at
// all — unconditionally (requires_device) or on one field's value
// (requires_device_when). Every site that asks "does this rpc get the gate"
// must use this: the two differ only in WHEN the check runs, so the broker
// wiring and every structural refusal apply to both alike.
func (m *MethodDescriptor) HasDeviceGate() bool {
	return m.RequiresDevice || m.RequiresDeviceWhen != nil
}

// HasDeviceGate reports whether this rpc carries the device-approval gate at
// all. See MethodDescriptor.HasDeviceGate.
func (m *MethodInfo) HasDeviceGate() bool {
	return m != nil && (m.RequiresDevice || m.RequiresDeviceWhen != nil)
}

// DeviceGateAnnotation names the annotation that asked for the gate, for the
// refusal messages the generators raise when a gated rpc's shape cannot carry
// the check. An rpc refused for declaring requires_device_when must not be told
// it declared requires_device.
func (m *MethodInfo) DeviceGateAnnotation() string {
	if m != nil && m.RequiresDeviceWhen != nil {
		return "requires_device_when"
	}
	return "requires_device"
}

// requiresDeviceWhenDescriptor resolves and validates one declared
// requires_device_when against the rpc's request message. Every failure is a
// generation-time refusal: a gate whose field or value cannot be resolved would
// otherwise become a comparison that is silently never true, which is an
// ungated rpc the author believes is gated.
func requiresDeviceWhenDescriptor(method protoreflect.MethodDescriptor, w *spec.RequiresDeviceWhen) (*RequiresDeviceWhenDescriptor, error) {
	name := w.GetField()
	if name == "" {
		return nil, fmt.Errorf("%s declares (spec.rpc).requires_device_when with no field; the gate has no request field to read", method.FullName())
	}
	input := method.Input()
	fd := input.Fields().ByName(protoreflect.Name(name))
	if fd == nil {
		return nil, fmt.Errorf("%s declares (spec.rpc).requires_device_when on field %q, but its request %s has no top-level field of that name; the gate reads the field off the request before the handler runs",
			method.FullName(), name, input.FullName())
	}
	if fd.IsList() || fd.IsMap() {
		return nil, fmt.Errorf("%s declares (spec.rpc).requires_device_when on %s.%s, which is repeated or a map; the gate compares ONE value against one declared value",
			method.FullName(), input.FullName(), name)
	}
	// Explicit presence (proto3 `optional`) makes the field a pointer in Go and
	// an Option in Rust, so "holds this value" is two questions, not one.
	if fd.HasPresence() {
		return nil, fmt.Errorf("%s declares (spec.rpc).requires_device_when on %s.%s, which has explicit presence (proto3 optional); the gate compares a plain field's value, not a set-or-unset one",
			method.FullName(), input.FullName(), name)
	}
	switch fd.Kind() {
	case protoreflect.StringKind:
		return &RequiresDeviceWhenDescriptor{Field: name, Equals: w.GetEquals()}, nil
	case protoreflect.EnumKind:
		ev := fd.Enum().Values().ByName(protoreflect.Name(w.GetEquals()))
		if ev == nil {
			return nil, fmt.Errorf("%s declares (spec.rpc).requires_device_when equals %q, but enum %s (the type of %s.%s) has no value of that name; the gate would never fire",
				method.FullName(), w.GetEquals(), fd.Enum().FullName(), input.FullName(), name)
		}
		return &RequiresDeviceWhenDescriptor{Field: name, Equals: w.GetEquals(), IsEnum: true, EnumNumber: int32(ev.Number())}, nil
	default:
		return nil, fmt.Errorf("%s declares (spec.rpc).requires_device_when on %s.%s, which is a %s field; the gate compares a plain string or enum field",
			method.FullName(), input.FullName(), name, fd.Kind())
	}
}
