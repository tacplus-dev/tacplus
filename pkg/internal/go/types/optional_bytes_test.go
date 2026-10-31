// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// protoc-gen-go emits a proto3 `optional bytes` as a plain []byte, not the
// pointer other optional scalars get; the helpers treating it as a pointer did
// not compile (found by the kitchensink, tacplus#196).
func TestOptionalBytesIsASliceInGo(t *testing.T) {
	pf := model.ProtoFile{Messages: []model.Message{{Name: "M", Fields: []model.Field{
		{Name: "blob", Type: "bytes", IsOptional: true},
		{Name: "name", Type: "string", IsOptional: true},
		{Name: "blobs", Type: "bytes", IsRepeated: true},
	}}}}
	optionalBytesAsSlice(&pf)
	f := pf.Messages[0].Fields
	if f[0].IsOptional {
		t.Errorf("optional bytes still optional: it would be emitted as *[]byte")
	}
	if !f[1].IsOptional {
		t.Errorf("optional string lost its optionality: it is a *string in Go")
	}
	if jsonType := computeJSONFieldType(f[0]); jsonType != "[]byte" {
		t.Errorf("JSON view type of optional bytes = %q, want []byte", jsonType)
	}
}
