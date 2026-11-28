// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
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

// javaConfigPushFeed names the Watch<T>Changes of configpush.TenantSliceRecord
// on the record plane's S2S service a (spec.record_store).config_push store
// hosts (tacplus#927), which the ServiceManager feeds its publisher from.
type javaConfigPushFeed struct {
	Method      string // "watchTenantSliceRecordChanges"
	HandlerType string // the plane's handler interface, fully qualified
	GetterName  string // the Handler's getter of it
	RequestType string // Watch<T>ChangesRequest, fully qualified
	ChangeType  string // <T>WatchChange, fully qualified
	ChangeEnum  string // RecordChangeType, fully qualified
}

// javaConfigPushFeedFor finds the plane's S2S service among the services the
// build hosts; a config_push store whose plane it does not host has nothing
// to watch, which is a generator error.
func javaConfigPushFeedFor(config *tactype.Config, protoDir string, plane *proto.ConfigPushFeedPlane, entries []javaGrpcServiceEntry) (*javaConfigPushFeed, error) {
	if plane == nil {
		return nil, nil
	}
	for _, e := range entries {
		if e.ServiceName != plane.S2SService ||
			(e.PackageName != plane.Package && e.PackageName != strings.SplitN(plane.Package, ".", 2)[0]) {
			continue
		}
		outer := javaOuterClassForProto(protoDir, "", plane.ProtoFile, plane.Package)
		types := javaTypesPackageForProto(config.JavaPackageBase, plane.ProtoFile, plane.Package, false) + "." + outer
		return &javaConfigPushFeed{
			Method:      "watch" + proto.TenantSliceRecordName + "Changes",
			HandlerType: e.WrapperPackage + "." + e.ServiceName + "." + e.HandlerType,
			GetterName:  e.GetterName,
			RequestType: types + ".Watch" + proto.TenantSliceRecordName + "ChangesRequest",
			ChangeType:  types + "." + proto.TenantSliceRecordName + "WatchChange",
			ChangeEnum:  types + ".RecordChangeType",
		}, nil
	}
	return nil, fmt.Errorf("config push feed: the build hosts no %s.%s, the record plane configpush.TenantSliceRecord is watched on", plane.Package, plane.S2SService)
}
