// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import "gitlab.com/seabeam/schematic/tacplus/pkg/registry"

// Type aliases so internal callers don't need to change their imports.
type ServiceRegistry = registry.Registry
type ServiceRegistryEntry = registry.ServiceEntry
type VServiceRegistryEntry = registry.VServiceEntry

// LoadServiceRegistry loads registry.yaml from inputDir.
func LoadServiceRegistry(inputDir string) (*ServiceRegistry, error) {
	return registry.Load(inputDir)
}
