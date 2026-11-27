// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// javaConfigPushSlice is one pushed tenant slice this service owns
// (tacplus#927): the ServiceManager's receiver holds it, and its typed
// accessor reads it.
type javaConfigPushSlice struct {
	Field       int32  // the owner's registered tenant_slice_field, the slice's wire identity
	MessageName string // e.g. "KernelTenantSettings"
	Accessor    string // the ServiceManager accessor, e.g. "kernelTenantSettings"
	JavaType    string // the message class, fully qualified
	Helpers     string // the slice package's Helpers class, fully qualified (its validator)
}

// javaConfigPushSlices returns the pushed tenant slices this service owns. A
// pushed slice's file declares no service state, so its types package is the
// plain one.
func javaConfigPushSlices(config *tactype.Config, protoDir string) ([]javaConfigPushSlice, error) {
	if config.FlatProtoDir == "" || config.SelfPackage == "" {
		return nil, nil
	}
	owned, err := proto.OwnedPushedConfigSlices(config.InputDir, config.FlatProtoDir, config.ServiceLabel())
	if err != nil {
		return nil, err
	}
	var out []javaConfigPushSlice
	for _, s := range owned {
		outer := javaOuterClassForProto(protoDir, "", s.ProtoFile, s.Package)
		pkg := javaTypesPackageForProto(config.JavaPackageBase, s.ProtoFile, s.Package, false)
		out = append(out, javaConfigPushSlice{
			Field:       s.SliceField,
			MessageName: s.MessageName,
			Accessor:    strings.ToLower(s.MessageName[:1]) + s.MessageName[1:],
			JavaType:    pkg + "." + outer + "." + s.MessageName,
			Helpers:     pkg + ".Helpers",
		})
	}
	return out, nil
}
