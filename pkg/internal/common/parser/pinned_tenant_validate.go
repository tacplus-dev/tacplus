// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"fmt"
	"sort"
	"strings"
)

// ValidatePinnedTenant enforces that (spec.service).pinned_tenant is declared
// only alongside tenant_owned = true. Generation FAILS otherwise.
//
// pinned_tenant changes the cardinality of a tenant-owned service — one
// instance in the named tenant instead of one per configured tenant — and has
// no meaning for a shared service, which is deployed once into /system and
// never carries a tenant at all. Accepting it there would leave a declaration
// in the proto that every reader ignores: the operator branches on it only
// inside the tenant_owned arm, so the pin would silently do nothing.
func ValidatePinnedTenant(dp *DescriptorParser) error {
	if dp == nil {
		return nil
	}
	var errs []string
	for _, fd := range dp.GetAllFiles() {
		if fd.ServicePinnedTenant == "" || fd.ServiceTenantOwned {
			continue
		}
		errs = append(errs, fmt.Sprintf("%s: (spec.service).pinned_tenant = %q without tenant_owned = true; pinned_tenant only narrows a tenant-owned service to one tenant — add tenant_owned or drop the pin",
			fd.Package, fd.ServicePinnedTenant))
	}
	if len(errs) > 0 {
		// GetAllFiles is a map: sort so the message is identical run-to-run.
		sort.Strings(errs)
		return fmt.Errorf("service annotation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
