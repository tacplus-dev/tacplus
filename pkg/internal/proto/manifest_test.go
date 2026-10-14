// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package proto

import "testing"

func TestEmittedProtoManifestFeatures(t *testing.T) {
	if !IsRuntimeConfigStateProto("sys/grpcconfig.proto") {
		t.Fatal("sys/grpcconfig.proto should be the manifest runtime config state proto")
	}
	if IsRuntimeConfigStateProto("services/grpcconfig.proto") {
		t.Fatal("runtime config state proto must be path-qualified, not a substring match")
	}

	if !EmitsFilesystemHelpers("sys/filesystem.proto", "filesystem") {
		t.Fatal("sys/filesystem.proto should emit filesystem helpers")
	}
	if EmitsFilesystemHelpers("services/filesystem.proto", "filesystem") {
		t.Fatal("filesystem helpers must come from the emitted manifest path")
	}

	if !EmitsEntitlementHelpers("generated/entitlements.proto", "entitlements") {
		t.Fatal("generated/entitlements.proto should emit entitlement helpers")
	}
	if EmitsEntitlementHelpers("generated/entitlements.proto", "other") {
		t.Fatal("entitlement helpers must also match the manifest package")
	}
}
