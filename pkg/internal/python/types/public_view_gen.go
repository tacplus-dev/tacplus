// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
)

// writePublicView emits <t>_to_public for every (spec.public_view) message in
// pf: T's bytes parsed as the generated <T>Public (its SHOW fields at their own
// numbers), then DiscardUnknownFields — without it the BACKGROUND fields would
// sit in the unknown-field set and be re-emitted by SerializeToString.
func writePublicView(b *strings.Builder, pf model.ProtoFile, protoFilename string) {
	for _, msg := range pf.Messages {
		if !msg.Annotations["public_view"].BoolValue {
			continue
		}
		t := msg.Name
		mod := protoFilename + "_pb2"
		fmt.Fprintf(b, "\n\ndef %s_to_public(msg: %s.%s) -> %s.%sPublic:\n", strutil.ToSnakeCase(t), mod, t, mod, t)
		fmt.Fprintf(b, "    \"\"\"Returns msg's public view: a %sPublic holding only its VISIBILITY_SHOW fields.\n\n", t)
		b.WriteString("    It is the only value a renderer may be handed.\n    \"\"\"\n")
		fmt.Fprintf(b, "    if msg is None:\n        raise invalid_argument(\"%s is None\")\n", t)
		fmt.Fprintf(b, "    out = %s.%sPublic()\n", mod, t)
		b.WriteString("    out.ParseFromString(msg.SerializeToString())\n    out.DiscardUnknownFields()\n    return out\n")
	}
}
