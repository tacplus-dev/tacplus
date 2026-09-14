// Copyright (c) 2026 Second Star Cavalier LLC

package types

import "fmt"

// The principal seam (§4b). (spec.principal) on a request field and
// (spec.store_principal) on a record field are enforced in the generated
// service WRAPPER — the same seam that already owns request validation and
// egress sanitization — so a handler never hand-writes the stamp, the forgery
// refusal or the read filter. The message index is the one the egress
// sanitizer already builds; these are lookups over it, not a second index.

// rustPrincipalListField is a repeated record field of a response whose item
// type declares (spec.store_principal).
type rustPrincipalListField struct {
	Field     string // snake_case response field holding the rows
	ItemField string // snake_case principal field on the row type
}

// requestPrincipalField returns the snake_case name of fullType's
// (spec.principal) field, or "" when it declares none. At most one such field
// per message is enforced by parser.ValidatePrincipalAnnotations.
func (es *rustEgressSanitizer) requestPrincipalField(fullType, servicePkg string) string {
	return es.annotatedStringField(fullType, servicePkg, "principal")
}

// storePrincipalField returns the snake_case name of fullType's
// (spec.store_principal) field, or "" when it declares none.
func (es *rustEgressSanitizer) storePrincipalField(fullType, servicePkg string) string {
	return es.annotatedStringField(fullType, servicePkg, "store_principal")
}

func (es *rustEgressSanitizer) annotatedStringField(fullType, servicePkg, annotation string) string {
	if es == nil || fullType == "" {
		return ""
	}
	pkg, name := splitRustEgressFQN(fullType, servicePkg)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return ""
	}
	for _, f := range msg.Fields {
		if _, ok := f.Annotations[annotation]; ok {
			return prostSnakeName(f.Name)
		}
	}
	return ""
}

// principalListFields returns the repeated record fields of fullType whose item
// type declares (spec.store_principal) — the rows the wrapper must filter to the
// caller before the response leaves the process.
func (es *rustEgressSanitizer) principalListFields(fullType, servicePkg string) []rustPrincipalListField {
	if es == nil || fullType == "" {
		return nil
	}
	pkg, name := splitRustEgressFQN(fullType, servicePkg)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return nil
	}
	var out []rustPrincipalListField
	for _, f := range msg.Fields {
		if !f.IsRepeated || !f.IsMessage || f.IsMap || f.IsOneOf {
			continue
		}
		fpkg := f.TypePackage
		if fpkg == "" {
			fpkg = pkg
		}
		itemField := es.storePrincipalField(fpkg+"."+f.Type, servicePkg)
		if itemField == "" {
			continue
		}
		out = append(out, rustPrincipalListField{Field: prostSnakeName(f.Name), ItemField: itemField})
	}
	return out
}

// rustPrincipalResolve renders the local that binds the authenticated caller's
// subject. `caller` is the Option<Caller> the unary wrapper already extracted.
func rustPrincipalResolve(local, method, indent string) string {
	return fmt.Sprintf(`%slet %s = caller.as_ref()
%s    .and_then(|c| c.token().subject())
%s    .filter(|s| !s.is_empty())
%s    .map(|s| s.to_string())
%s    .ok_or_else(|| tonic::Status::unauthenticated("%s requires an authenticated caller"))?;
`, indent, local, indent, indent, indent, indent, method)
}

// rustActorResolve renders the local that binds the `act` (actor) claim off
// the caller's token, when present. Unlike rustPrincipalResolve, an absent
// actor is the ORDINARY case (an un-delegated call) — this never errors, it
// binds an Option<String> and lets the caller decide what "None" means.
func rustActorResolve(local, method, indent string) string {
	_ = method // kept for signature symmetry with rustPrincipalResolve
	return fmt.Sprintf(`%slet %s = caller.as_ref()
%s    .and_then(|c| c.token().actor())
%s    .filter(|s| !s.is_empty())
%s    .map(|s| s.to_string());
`, indent, local, indent, indent, indent)
}

// rustRequestPrincipalBlock renders the (spec.principal) stamp-or-refuse check.
// Empty is stamped from the caller, equal passes, different is refused: a forged
// principal is never silently overwritten.
//
// reqVar names the owned request binding in scope and indent its column — the
// two things that differ between RPC shapes. The check itself does not: a PAGE
// request, a WATCH request and a unary one are all one message bound to one
// caller, so there is one renderer, not one per shape.
func rustRequestPrincipalBlock(field, method, reqVar, indent string) string {
	return fmt.Sprintf(`
%[1]s// (spec.principal) %[2]s: bind the field to the authenticated caller.
%[4]s%[1]slet mut %[3]s = %[3]s;
%[1]sif %[3]s.%[2]s.is_empty() {
%[1]s    %[3]s.%[2]s = __principal;
%[1]s} else if %[3]s.%[2]s != __principal {
%[1]s    return Err(tonic::Status::invalid_argument(
%[1]s        "%[2]s does not match the authenticated caller"));
%[1]s}
%[1]slet %[3]s = %[3]s;
`, indent, field, reqVar, rustPrincipalResolve("__principal", method, indent))
}

// rustStreamPrincipalStmts renders the per-message half of the same check, for
// the request streams where there is no single request to stamp. The resolve
// (rustPrincipalResolve) runs once before the stream is wrapped; these
// statements run on EVERY inbound message, so message N cannot name a principal
// message N-1 did not.
func rustStreamPrincipalStmts(field, msgVar, indent string) string {
	return fmt.Sprintf(`%[1]s// (spec.principal) %[2]s: bind the field to the authenticated caller.
%[1]sif %[3]s.%[2]s.is_empty() {
%[1]s    %[3]s.%[2]s = __principal.clone();
%[1]s} else if %[3]s.%[2]s != __principal {
%[1]s    return Err(tonic::Status::invalid_argument(
%[1]s        "%[2]s does not match the authenticated caller"));
%[1]s}
`, indent, field, msgVar)
}

// rustResponsePrincipalFilterBlock renders the (spec.store_principal) egress
// filter: rows the caller does not own never leave the process.
func rustResponsePrincipalFilterBlock(fields []rustPrincipalListField, method string) string {
	out := "\n            // (spec.store_principal): filter rows to the caller's principal.\n"
	out += rustPrincipalResolve("__read_principal", method, "            ")
	out += "            let mut resp = resp;\n"
	for _, f := range fields {
		out += fmt.Sprintf("            resp.%s.retain(|__row| __row.%s == __read_principal);\n", f.Field, f.ItemField)
	}
	return out
}
