// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
)

// StoreEpochField resolves WHICH field of a message carries the row's store
// epoch, or "" when it declares none. The location is the annotation's whole
// job — the stamp itself is the wrapper's, exactly as with (spec.principal).
//
// Well-formedness (int64, non-repeated, RANGE-queryable) and uniqueness are
// enforced once, at parse time, by ValidateResidencyAnnotations. This is a
// lookup, not a second opinion, so it returns the first annotated field: by the
// time a generator asks, "more than one" has already failed the build.
//
// It lives here, shared, for the same reason MinGroupSizeField does: Go, Java
// and Rust each render a stamp over this field, and three private copies of
// "which field is the epoch" is three stamps that can come to disagree.
func StoreEpochField(msg *model.Message) string {
	if msg == nil {
		return ""
	}
	for i := range msg.Fields {
		if msg.Fields[i].Annotations["store_epoch"].BoolValue {
			return msg.Fields[i].Name
		}
	}
	return ""
}
