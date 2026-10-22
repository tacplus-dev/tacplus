// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// writePublicView emits to<T>Public for every (spec.public_view) message in pf:
// T's bytes parsed as the generated <T>Public (its SHOW fields at their own
// numbers) through DiscardUnknownFieldsParser, which drops every other field
// recursively — a plain parseFrom would keep BACKGROUND fields in the unknown
// field set and re-emit them on toByteString.
func writePublicView(b *strings.Builder, pf model.ProtoFile, outerClassName string) {
	header := false
	for _, msg := range pf.Messages {
		if !msg.Annotations["public_view"].BoolValue {
			continue
		}
		if !header {
			b.WriteString("// ============ Public View (spec.public_view) ============\n")
			header = true
		}
		t := msg.Name
		outer := msgOuterClass(msg, outerClassName)
		fmt.Fprintf(b, "// to%[1]sPublic returns m's public view: a %[1]sPublic holding only its\n", t)
		b.WriteString("// VISIBILITY_SHOW fields. It is the only value a renderer may be handed.\n")
		fmt.Fprintf(b, "public static %[1]s.%[2]sPublic to%[2]sPublic(%[1]s.%[2]s m) throws com.google.protobuf.InvalidProtocolBufferException {\n", outer, t)
		b.WriteString("if (m == null) { return null; }\n")
		fmt.Fprintf(b, "return com.google.protobuf.DiscardUnknownFieldsParser.wrap(%s.%sPublic.parser()).parseFrom(m.toByteString());\n}\n", outer, t)
	}
}
