// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
)

// writePublicView emits <t>_to_public for every (spec.public_view) message in
// pf: T's bytes decoded as the generated <T>Public (its SHOW fields at their own
// numbers). Prost keeps no unknown fields, so every BACKGROUND field is dropped
// on decode, recursively.
func writePublicView(b *strings.Builder, pf model.ProtoFile) {
	for _, msg := range pf.Messages {
		if !msg.Annotations["public_view"].BoolValue {
			continue
		}
		t := msg.Name
		fn := strutil.ToSnakeCase(t) + "_to_public"
		fmt.Fprintf(b, "/// %[1]s returns m's public view: a %[2]sPublic holding only its\n", fn, t)
		b.WriteString("/// VISIBILITY_SHOW fields. It is the only value a renderer may be handed.\n")
		fmt.Fprintf(b, "pub fn %[1]s(m: &%[2]s) -> Result<%[2]sPublic, prost::DecodeError> {\n", fn, t)
		fmt.Fprintf(b, "    <%sPublic as prost::Message>::decode(prost::Message::encode_to_vec(m).as_slice())\n}\n\n", t)
	}
}
