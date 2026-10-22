// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// generatePublicViewHelpers emits <T>ToPublic for every (spec.public_view)
// message in pf. The <T>Public TYPE is generated as proto
// (AppendPublicViewMessagesToProtos) with T's SHOW fields at their own numbers,
// so the conversion is T's bytes parsed as <T>Public with unknown fields
// discarded: nothing here names a field, and a BACKGROUND field has nowhere to
// land — not even the unknown-field set, which would re-emit it on marshal.
func generatePublicViewHelpers(b *strings.Builder, pf model.ProtoFile) {
	for _, msg := range pf.Messages {
		if !msg.Annotations["public_view"].BoolValue {
			continue
		}
		t := msg.Name
		fmt.Fprintf(b, "// %[1]sToPublic returns m's public view: a %[1]sPublic holding only its\n", t)
		b.WriteString("// VISIBILITY_SHOW fields. It is the only value a renderer may be handed.\n")
		fmt.Fprintf(b, "func %[1]sToPublic(m *%[1]s) (*%[1]sPublic, error) {\n", t)
		b.WriteString("\tif m == nil {\n\t\treturn nil, nil\n\t}\n")
		b.WriteString("\tb, err := proto.Marshal(m)\n\tif err != nil {\n\t\treturn nil, err\n\t}\n")
		fmt.Fprintf(b, "\tout := &%sPublic{}\n", t)
		b.WriteString("\tif err := (proto.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(b, out); err != nil {\n\t\treturn nil, err\n\t}\n")
		b.WriteString("\treturn out, nil\n}\n\n")
	}
}
