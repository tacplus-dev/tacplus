// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package proto

import "github.com/tacplus-dev/tacplus/pkg/registry"

// Type aliases so internal callers don't need to change their imports.
type ServiceRegistry = registry.Registry
type ServiceRegistryEntry = registry.ServiceEntry
type VServiceRegistryEntry = registry.VServiceEntry

// LoadServiceRegistry loads registry.yaml from inputDir.
func LoadServiceRegistry(inputDir string) (*ServiceRegistry, error) {
	return registry.Load(inputDir)
}
