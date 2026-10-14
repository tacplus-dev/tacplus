// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/packages"
)

func typeScriptDependencyVersion(pkgName string) (string, error) {
	registry, err := packages.GetPackageRegistry()
	if err != nil {
		return "", fmt.Errorf("failed to load package registry: %w", err)
	}
	if ver, ok := registry.GetTypeScriptVersion(pkgName); ok {
		return ver, nil
	}
	return "", fmt.Errorf("%s version not found in dependencies.yaml", pkgName)
}
