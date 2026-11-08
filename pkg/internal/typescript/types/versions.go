// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/packages"
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

// defaultNodeEngine is the chassis engines.node when dependencies.yaml states
// no tools.node: at or above what the TypeScript pins need
// (@bufbuild/protoc-gen-es 2.16 needs Node >=22), on a line that still gets
// security fixes (18 and 20 are end of life).
const defaultNodeEngine = ">=22.0.0"

// typeScriptNodeEngine is the engines.node of the generated chassis manifests:
// dependencies.yaml's tools.node, else defaultNodeEngine.
func typeScriptNodeEngine() (string, error) {
	registry, err := packages.GetPackageRegistry()
	if err != nil {
		return "", fmt.Errorf("failed to load package registry: %w", err)
	}
	if ver, ok := registry.GetToolVersion("node"); ok && ver != "" {
		return ver, nil
	}
	return defaultNodeEngine, nil
}
