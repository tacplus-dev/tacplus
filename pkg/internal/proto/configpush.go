// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"fmt"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
)

const configSliceDeliveryPush = "CONFIG_SLICE_DELIVERY_PUSH"

// PushedConfigSlice is one tenant-scope (spec.config_slice) with delivery
// CONFIG_SLICE_DELIVERY_PUSH (tacplus#927): each tenant's record store pushes
// it to Owner over sys/configpush.proto, which carries it as SliceField.
type PushedConfigSlice struct {
	Owner           string // the owning service, (spec.config_slice).service_name
	SliceField      int32  // the owner's registered tenant_slice_field
	Package         string // the slice message's proto package
	MessageName     string
	MessageFullName string // Package.MessageName
	ProtoFile       string // the declaring file, relative to the proto dir
}

// isPushedSlice reports whether msg's (spec.config_slice) asks for push delivery.
func isPushedSlice(msg parser.MessageDescriptor) bool {
	return msg.Annotations["config_slice_delivery"].EnumValue == configSliceDeliveryPush
}

// PushedConfigSlices returns the pushed tenant slices dp declares, in file
// order, and refuses one the contract cannot carry: an owner with no
// registered tenant_slice_field (the number is the slice's identity between
// the record store's build and the owner's, so only the registry gives it,
// never a build), an owner that is tenant-owned (the push goes from a tenant
// to a /system service), and a second pushed slice for one owner (the owner's
// number names one). It never assigns a registry field.
func PushedConfigSlices(dp *parser.DescriptorParser, reg *ServiceRegistry) ([]PushedConfigSlice, error) {
	tenantOwned := parser.TenantOwnedServices(dp)
	files := dp.GetAllFiles()
	var out []PushedConfigSlice
	byOwner := map[string]PushedConfigSlice{}
	for _, relPath := range sortedFilePaths(files) {
		fileDesc := files[relPath]
		if IsInfraProtoFile(relPath, fileDesc) {
			continue
		}
		for _, msg := range fileDesc.Messages {
			if !isPushedSlice(msg) {
				continue
			}
			owner, ok, err := configSliceOwner(relPath, fileDesc, msg, configSliceScopeTenant)
			if err != nil {
				return nil, err
			}
			if !ok {
				// Another scope: configSliceOwner's caller for that scope refuses it.
				continue
			}
			full := fileDesc.Package + "." + msg.Name
			if tenantOwned[owner] {
				return nil, fmt.Errorf("%s: %s is a pushed tenant slice (CONFIG_SLICE_DELIVERY_PUSH) of %q, which is tenant-owned: a slice is pushed from a tenant to a /system service", relPath, full, owner)
			}
			if prev, dup := byOwner[owner]; dup {
				return nil, fmt.Errorf("%s: %q has two pushed slices, %s (%s) and %s: an owner's registered tenant_slice_field names one pushed slice", relPath, owner, prev.MessageFullName, prev.ProtoFile, full)
			}
			field, ok := reg.TenantSliceFieldIfPresent(owner)
			if !ok {
				return nil, fmt.Errorf("%s: %s is a pushed tenant slice (CONFIG_SLICE_DELIVERY_PUSH) of %q, which has no registered tenant_slice_field: it is the slice's identity on the wire between the record store and %s, so the registry gives it, never a build. In a checkout of the authority run\n  tacplus registry register %s --tenant-slice\nand merge it, then sync this registry", relPath, full, owner, owner, owner)
			}
			s := PushedConfigSlice{
				Owner:           owner,
				SliceField:      field,
				Package:         fileDesc.Package,
				MessageName:     msg.Name,
				MessageFullName: full,
				ProtoFile:       relPath,
			}
			byOwner[owner] = s
			out = append(out, s)
		}
	}
	return out, nil
}

// OwnedPushedConfigSlices returns the pushed tenant slices owner declares in
// flatProtoDir, the registry read from inputDir: what a build of owner serves.
func OwnedPushedConfigSlices(inputDir, flatProtoDir, owner string) ([]PushedConfigSlice, error) {
	reg, err := LoadServiceRegistry(inputDir)
	if err != nil {
		return nil, fmt.Errorf("pushed config slices: loading registry: %w", err)
	}
	dp, err := parser.NewDescriptorParser(flatProtoDir)
	if err != nil {
		return nil, fmt.Errorf("pushed config slices: %w", err)
	}
	pushed, err := PushedConfigSlices(dp, reg)
	if err != nil {
		return nil, err
	}
	var out []PushedConfigSlice
	for _, s := range pushed {
		if s.Owner == owner {
			out = append(out, s)
		}
	}
	return out, nil
}
