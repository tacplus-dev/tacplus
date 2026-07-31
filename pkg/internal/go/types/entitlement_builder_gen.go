// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"strings"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

// This file emits the caller → <T>Entitlement builders for Go: for every
// entitlement-relevant message T, a mechanical Build<T>Entitlement that maps the
// caller's token data grants (TokenEntitlements.data_grants, OPA-stamped at
// mint) onto a <T>Entitlement. Fail-closed: a MAY_* field with a matching grant
// is set to RAW (3); absent a grant it is left UNSPECIFIED (0) and Sanitize<T>
// applies its most-protective default. Hard-floor (DROP/PSEUDONYMIZE) fields
// have no caller field and are always applied inside Sanitize<T>. No policy
// lives here — the grant tokens are fixed strings computed at generation time
// (parser.DataGrantToken), and the grant SET is an identity property of the
// caller's token, never a request parameter.
//
// Nested sub-entitlements are constructed INLINE following the cycle-guarded
// entitlement tree (never by calling Build<Sub> — mutually recursive record
// types would loop forever at runtime). A tree edge cut by the cycle guard
// yields a nil sub-entitlement, which Sanitize<T> treats as fully protective.
//
// RAW is assigned as the untyped constant 3, which Go converts to the generated
// EntitlementLevel enum type implicitly — so, like Sanitize<T>, the builder
// needs no import of the entitlement package.

// generateEntitlementBuilders emits Build<T>Entitlement for every
// entitlement-relevant message in pf, beside the Sanitize<T> transforms.
func generateEntitlementBuilders(b *strings.Builder, pf model.ProtoFile, resolver *parser.MessageResolver) error {
	for mi := range pf.Messages {
		msg := &pf.Messages[mi]
		if !resolver.HasEntitlement(msg.Name) {
			continue
		}
		tree := parser.BuildEntitlementTree(msg, pf.Package, resolver)
		if !tree.IsRelevant() {
			continue
		}
		writeGoEntitlementBuilder(b, tree)
	}
	return nil
}

// writeGoEntitlementBuilder emits one Build<T>Entitlement function.
func writeGoEntitlementBuilder(b *strings.Builder, node *parser.EntitlementNode) {
	t := node.Message.Name
	fmt.Fprintf(b, "// Build%[1]sEntitlement maps the caller's token data grants onto a %[1]sEntitlement.\n", t)
	fmt.Fprintf(b, "// Fail-closed: a MAY_* field with a matching grant is set to RAW; absent a grant it\n")
	fmt.Fprintf(b, "// is left UNSPECIFIED and Sanitize%[1]s applies its most-protective default.\n", t)
	fmt.Fprintf(b, "func Build%[1]sEntitlement(dataGrants map[string]bool) *%[1]sEntitlement {\n", t)
	fmt.Fprintf(b, "\tent := &%sEntitlement{}\n", t)
	writeGoEntitlementFields(b, node, "ent")
	b.WriteString("\treturn ent\n}\n\n")
}

// writeGoEntitlementFields emits the grant checks and inline sub-entitlement
// construction for one tree node, assigning into the accessor path v.
func writeGoEntitlementFields(b *strings.Builder, node *parser.EntitlementNode, v string) {
	for _, ef := range node.Fields {
		gf := toGoFieldName(ef.Field.Name)
		if ef.Mode == "may_redact" || ef.Mode == "may_anonymize" {
			token := parser.DataGrantToken(node.Package, node.Message.Name, ef.Field.Name)
			fmt.Fprintf(b, "\tif dataGrants[%q] {\n\t\t%s.%s = 3 // ENTITLEMENT_LEVEL_RAW\n\t}\n", token, v, gf)
		}
		// Field-by-field recursion mirrors the <T>Entitlement _sub shape: only a
		// same-package relevant subtree gets a sub-entitlement.
		if ef.Field.IsMessage && ef.Nested != nil && (ef.Field.TypePackage == "" || ef.Field.TypePackage == node.Package) {
			sub := v + "." + gf + "Sub"
			fmt.Fprintf(b, "\t%s = &%sEntitlement{}\n", sub, ef.Field.Type)
			writeGoEntitlementFields(b, ef.Nested, sub)
		}
	}
}
