// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"fmt"
	"sort"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// ValidateTimeseriesAnnotations checks all messages in the parsed files for
// (spec.timeseries_type) and (spec.timeseries_column) annotation consistency.
// Returns an error describing ALL violations.
//
// Rules enforced:
//   - At least one field must have order_key > 0
//   - order_key values must be unique and contiguous starting from 1
//   - CARDINALITY_LOW hint only on string or enum types
//   - Messages with source must have projection_source on every timeseries field
//
// LIMITATION: this only inspects order_key/dedup.key_fields declared directly on
// the message's own fields. A message's separately-declared (spec.transformation)
// block has its own inline field: entries and is not walked here, so an order_key/
// key_fields conflict expressed only in a transformation will not be caught by
// this validator.
func ValidateTimeseriesAnnotations(allFiles []model.ProtoFile) error {
	var errs []string

	for _, pf := range allFiles {
		for _, msg := range pf.Messages {
			// Only validate messages with timeseries_type annotation
			if _, hasTable := msg.Annotations["timeseries_type"]; !hasTable {
				continue
			}

			loc := fmt.Sprintf("%s.%s", pf.Package, msg.Name)
			hasSource := false
			if _, ok := msg.Annotations["timeseries_table_source"]; ok {
				hasSource = true
			}

			var dedupKeyFields []string
			if dedupAnn, hasDedup := msg.Annotations["timeseries_dedup"]; hasDedup && dedupAnn.TimeseriesDedup != nil {
				dedupKeyFields = dedupAnn.TimeseriesDedup.KeyFields
			}

			var orderKeys []int
			orderKeyFields := make(map[int]string)
			var tsFields int

			for _, field := range msg.Fields {
				if _, hasTS := field.Annotations["timeseries_column"]; !hasTS {
					continue
				}
				tsFields++
				fieldLoc := fmt.Sprintf("%s.%s", loc, field.Name)

				// Validate a CARDINALITY_LOW hint only on string/enum. The hint is
				// stored as its spec enum number (CARDINALITY_LOW = 1).
				if chAnn, hasCH := field.Annotations["timeseries_cardinality_hint"]; hasCH && chAnn.IntValue == int64(model.CardinalityLow) {
					if field.Type != "string" && !field.IsEnum {
						errs = append(errs, fmt.Sprintf(
							"%s: CARDINALITY_LOW is only valid on string or enum fields, not %s",
							fieldLoc, field.Type))
					}
				}

				// Validate source → projection_source requirement
				if hasSource {
					if _, hasGS := field.Annotations["projection_source"]; !hasGS {
						errs = append(errs, fmt.Sprintf(
							"%s: message has timeseries_type.source set, so every timeseries field must have (spec.projection_source)",
							fieldLoc))
					}
				}

				// Collect order keys
				if okAnn, hasOK := field.Annotations["timeseries_order_key"]; hasOK && okAnn.IntValue > 0 {
					key := int(okAnn.IntValue)
					if existing, dup := orderKeyFields[key]; dup {
						errs = append(errs, fmt.Sprintf(
							"%s: duplicate order_key %d (also on %s)",
							fieldLoc, key, existing))
					} else {
						orderKeyFields[key] = field.Name
						orderKeys = append(orderKeys, key)
					}
					if len(dedupKeyFields) > 0 {
						errs = append(errs, fmt.Sprintf(
							"%s: order_key %d conflicts with dedup.key_fields %v — key_fields already establishes row identity/order for dedup; remove order_key",
							fieldLoc, key, dedupKeyFields))
					}
				}
			}

			if tsFields == 0 {
				errs = append(errs, fmt.Sprintf(
					"%s: has (spec.timeseries_type) but no fields with (spec.timeseries_column)",
					loc))
				continue
			}

			// Must have at least one order key, unless dedup.key_fields already
			// establishes row identity/order.
			if len(orderKeys) == 0 && len(dedupKeyFields) == 0 {
				errs = append(errs, fmt.Sprintf(
					"%s: at least one field must have order_key > 0",
					loc))
				continue
			}

			// order_key values must be contiguous starting from 1
			sort.Ints(orderKeys)
			for i, k := range orderKeys {
				expected := i + 1
				if k != expected {
					errs = append(errs, fmt.Sprintf(
						"%s: order_key values must be contiguous starting from 1, got %v",
						loc, orderKeys))
					break
				}
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("timeseries annotation errors:\n  %s", strings.Join(errs, "\n  "))
	}
	return nil
}
