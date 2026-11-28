// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"fmt"
	"slices"

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

// ConfigPushOwner is one owner a tenant's record store may push a slice to:
// its registered tenant_slice_field and its routing label.
type ConfigPushOwner struct {
	SliceField int32  `json:"sliceField"`
	Owner      string `json:"owner"`
}

// ConfigPushOwners returns every owner the registry read from inputDir gives
// a tenant_slice_field, self aside, in field order: the publisher's owners in
// a build of the record store self. The store's run never holds an owner's
// proto, so it cannot tell a pushed slice from a TenantConfig one: what the
// tenant writes for a field is what it pushes, and an owner that serves no
// ConfigPush refuses the stream.
func ConfigPushOwners(inputDir, self string) ([]ConfigPushOwner, error) {
	reg, err := LoadServiceRegistry(inputDir)
	if err != nil {
		return nil, fmt.Errorf("config push owners: loading registry: %w", err)
	}
	var out []ConfigPushOwner
	for _, e := range reg.TenantSlices() {
		if e.Name != self {
			out = append(out, ConfigPushOwner{SliceField: e.TenantSliceField, Owner: e.Name})
		}
	}
	return out, nil
}

// RecordStorePushOwners returns the publisher's owners for a build of the
// service self (its label; selfPackage its package), or none when self is no
// tenant's record store: a tenant-owned (spec.record_store), whose instance is
// one tenant's store, the only publisher a receiver admits.
func RecordStorePushOwners(inputDir, flatProtoDir, selfPackage, self string) ([]ConfigPushOwner, error) {
	if inputDir == "" || flatProtoDir == "" || selfPackage == "" || self == "" {
		return nil, nil
	}
	store, err := IsRecordStore(flatProtoDir, selfPackage)
	if err != nil || !store {
		return nil, err
	}
	dp, err := parser.NewDescriptorParser(flatProtoDir)
	if err != nil {
		return nil, fmt.Errorf("config push owners: %w", err)
	}
	if !parser.TenantOwnedServices(dp)[self] {
		return nil, nil
	}
	return ConfigPushOwners(inputDir, self)
}

// TenantSliceRecordPackage, TenantSliceRecordName and TenantSliceRecordProto
// name the sys record a (spec.record_store).config_push store holds its
// tenant's pushed slices in (tacplus#927), keyed by TenantSliceRecordKey.
const (
	TenantSliceRecordPackage = "configpush"
	TenantSliceRecordName    = "TenantSliceRecord"
	TenantSliceRecordProto   = "sys/configpush.proto"
	TenantSliceRecordKey     = "slice_field"
)

// tenantSliceRecordType is the sys record configpush.TenantSliceRecord as the
// record plane of a config_push store carries it: on the S2S half alone, its
// audience the trait's, acked at the LOCAL floor. It is no (spec.record), so
// no other store, no event bus and no C2S reader carries it.
var tenantSliceRecordType = recordType{
	typeName:   TenantSliceRecordName,
	pkg:        TenantSliceRecordPackage,
	relPath:    TenantSliceRecordProto,
	key:        TenantSliceRecordKey,
	durability: "DURABILITY_LOCAL",
	keyType:    "int32",
	s2sOnly:    true,
}

// validateRecordStoreConfigPush refuses a (spec.record_store).config_push
// the generated feed cannot serve: on a store that is no tenant's (only a
// tenant-owned store's instance is one tenant's, the publisher a receiver
// admits), or on a store no (spec.trait) hosts a record plane on (the record
// would have no write surface and no change feed to watch).
func validateRecordStoreConfigPush(dp *parser.DescriptorParser, planes []traitPlane) error {
	tenantOwned := parser.TenantOwnedServices(dp)
	files := dp.GetAllFiles()
	for _, relPath := range sortedFilePaths(files) {
		fd := files[relPath]
		if !fd.RecordStore || !fd.RecordStorePush || fd.ServiceName == "" {
			continue
		}
		if !tenantOwned[fd.ServiceName] {
			return fmt.Errorf("%s: %q declares (spec.record_store).config_push but is not tenant-owned: only a tenant's record store ((spec.service).tenant_owned) publishes its tenant's pushed config slices", relPath, fd.ServiceName)
		}
		hosted := slices.ContainsFunc(planes, func(p traitPlane) bool { return p.host == fd.ServiceName })
		if !hosted {
			return fmt.Errorf("%s: %q declares (spec.record_store).config_push but no (spec.trait) hosts its record plane: configpush.TenantSliceRecord would have no write surface and no change feed", relPath, fd.ServiceName)
		}
	}
	return nil
}

// ConfigPushFeedPlane is the record plane a config_push store's generated
// feed watches configpush.TenantSliceRecord on: the S2S service the trait
// emits (S2SService) in the trait's proto package (Package), declared in
// ProtoFile.
type ConfigPushFeedPlane struct {
	Package    string
	S2SService string
	ProtoFile  string
}

// RecordStoreConfigPushFeed returns the plane a build of the service self
// (its label; selfPackage its package) feeds its publisher from, or nil when
// self declares no (spec.record_store).config_push. A store that hosts more
// than one plane carries the record on each; the feed watches the first, in
// file order. flatProtoDir is read before the plane is injected, so it holds
// the trait declarations alone.
func RecordStoreConfigPushFeed(flatProtoDir, selfPackage, self string) (*ConfigPushFeedPlane, error) {
	if flatProtoDir == "" || selfPackage == "" || self == "" {
		return nil, nil
	}
	dp, err := parser.NewDescriptorParser(flatProtoDir)
	if err != nil {
		return nil, fmt.Errorf("config push feed: %w", err)
	}
	push := false
	for _, fd := range dp.GetAllFiles() {
		if fd.Package == selfPackage && fd.RecordStore && fd.RecordStorePush {
			push = true
		}
	}
	if !push {
		return nil, nil
	}
	planes, err := collectRecordQueryPlanes(dp)
	if err != nil {
		return nil, fmt.Errorf("config push feed: %w", err)
	}
	for _, p := range planes {
		if p.host != self {
			continue
		}
		fd, err := dp.GetFileDescriptor(p.relPath)
		if err != nil {
			return nil, fmt.Errorf("config push feed: %w", err)
		}
		return &ConfigPushFeedPlane{Package: fd.Package, S2SService: p.s2sService, ProtoFile: p.relPath}, nil
	}
	return nil, fmt.Errorf("%q declares (spec.record_store).config_push but no (spec.trait) hosts its record plane", self)
}
