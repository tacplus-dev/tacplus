// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"path/filepath"

	stateproto "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// goConfigPushSlice is one pushed tenant slice this service owns (tacplus#927):
// the ServiceManager's receiver holds it, and its typed accessor reads it.
type goConfigPushSlice struct {
	Field       int32  // the owner's registered tenant_slice_field, the slice's wire identity
	MessageName string // e.g. "KernelTenantSettings"
	PkgAlias    string // import alias of the slice's types package
	PkgImport   string // import path of the slice's types package
}

// goConfigPushSlices returns the pushed tenant slices this service owns: the
// (spec.config_slice) tenant slices with delivery CONFIG_SLICE_DELIVERY_PUSH
// whose service_name is this service's label. None for a project that declares
// no package of its own, or with no flat proto dir.
func goConfigPushSlices(config *tactype.Config) ([]goConfigPushSlice, error) {
	if config.FlatProtoDir == "" || config.SelfPackage == "" {
		return nil, nil
	}
	owned, err := stateproto.OwnedPushedConfigSlices(config.InputDir, config.FlatProtoDir, config.ServiceLabel())
	if err != nil {
		return nil, err
	}
	var out []goConfigPushSlice
	for _, s := range owned {
		out = append(out, goConfigPushSlice{
			Field:       s.SliceField,
			MessageName: s.MessageName,
			PkgAlias:    s.Package + "_push_pb",
			PkgImport:   config.TypesImportPath(filepath.Dir(s.ProtoFile), s.Package),
		})
	}
	return out, nil
}
