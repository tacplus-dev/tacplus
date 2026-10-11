// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/strutil"
)

// writePublicView emits <t>ToPublic for every (spec.public_view) message in pf:
// T's bytes read as the generated <T>Public (its SHOW fields at their own
// numbers) with readUnknownFields off — protobuf-es keeps unknown fields by
// default and would re-emit the BACKGROUND fields on toBinary.
func writePublicView(b *strings.Builder, pf model.ProtoFile) {
	for _, msg := range pf.Messages {
		if !msg.Annotations["public_view"].BoolValue {
			continue
		}
		t := msg.Name
		fmt.Fprintf(b, "/**\n * Returns m's public view: a %sPublic holding only its VISIBILITY_SHOW fields.\n", t)
		b.WriteString(" * It is the only value a renderer may be handed.\n */\n")
		fmt.Fprintf(b, "export function %[1]sToPublic(m: %[2]s): %[2]sPublic {\n", strutil.LcFirst(t), t)
		fmt.Fprintf(b, "\treturn fromBinary(%[1]sPublicSchema, toBinary(%[1]sSchema, m), { readUnknownFields: false });\n}\n\n", t)
	}
}
