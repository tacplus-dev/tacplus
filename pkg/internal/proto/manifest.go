// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"path/filepath"
)

type EmittedProtoFeature string

const (
	FeatureRuntimeConfigState EmittedProtoFeature = "runtime_config_state"
	FeatureFilesystemHelpers  EmittedProtoFeature = "filesystem_helpers"
	FeatureEntitlementHelpers EmittedProtoFeature = "entitlement_helpers"
)

type EmittedProto struct {
	RelPath  string
	Package  string
	Features []EmittedProtoFeature
}

var emittedProtoManifest = map[string]EmittedProto{
	"sys/grpcconfig.proto": {
		RelPath:  "sys/grpcconfig.proto",
		Package:  "grpcconfig",
		Features: []EmittedProtoFeature{FeatureRuntimeConfigState},
	},
	"sys/filesystem.proto": {
		RelPath:  "sys/filesystem.proto",
		Package:  "filesystem",
		Features: []EmittedProtoFeature{FeatureFilesystemHelpers},
	},
	"generated/entitlements.proto": {
		RelPath:  "generated/entitlements.proto",
		Package:  "entitlements",
		Features: []EmittedProtoFeature{FeatureEntitlementHelpers},
	},
}

func IsRuntimeConfigStateProto(relPath string) bool {
	return emittedProtoHasFeature(relPath, "", FeatureRuntimeConfigState)
}

func EmitsFilesystemHelpers(relPath, packageName string) bool {
	return emittedProtoHasFeature(relPath, packageName, FeatureFilesystemHelpers)
}

func EmitsEntitlementHelpers(relPath, packageName string) bool {
	return emittedProtoHasFeature(relPath, packageName, FeatureEntitlementHelpers)
}

func emittedProtoHasFeature(relPath, packageName string, feature EmittedProtoFeature) bool {
	entry, ok := emittedProtoManifest[filepath.ToSlash(relPath)]
	if !ok {
		return false
	}
	if packageName != "" && entry.Package != packageName {
		return false
	}
	for _, candidate := range entry.Features {
		if candidate == feature {
			return true
		}
	}
	return false
}
