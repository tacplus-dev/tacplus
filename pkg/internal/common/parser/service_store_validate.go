// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
)

// ValidateServiceStores checks that every (spec.service_store)-annotated message
// has a unique name across the whole proto set. Store identity is derived from
// the snake_cased message name (Go diskstore function names and on-disk snapshot
// paths, the PersistedStore enum values shared by all languages), so two stores
// whose names collide after snake_casing would silently share generated
// identifiers and snapshot files. Returns an error describing ALL collisions.
func ValidateServiceStores(allFiles []model.ProtoFile) error {
	type storeLoc struct {
		pkg  string
		name string
	}
	byStore := map[string][]storeLoc{}
	for _, pf := range allFiles {
		for _, msg := range pf.Messages {
			if _, ok := msg.Annotations["service_store"]; !ok {
				continue
			}
			key := strutil.ToSnakeCase(msg.Name)
			byStore[key] = append(byStore[key], storeLoc{pkg: pf.Package, name: msg.Name})
		}
	}

	keys := make([]string, 0, len(byStore))
	for k := range byStore {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var errs []string
	for _, k := range keys {
		locs := byStore[k]
		if len(locs) < 2 {
			continue
		}
		where := make([]string, 0, len(locs))
		for _, l := range locs {
			where = append(where, fmt.Sprintf("%s.%s", l.pkg, l.name))
		}
		sort.Strings(where)
		errs = append(errs, fmt.Sprintf(
			"%s all map to store %q; service_store message names must be unique (per snake_case identity) across all packages",
			strings.Join(where, ", "), k))
	}

	if len(errs) > 0 {
		return fmt.Errorf("service_store annotation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
