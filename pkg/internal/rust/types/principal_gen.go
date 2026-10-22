// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

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
	Private   bool   // the row type also declares (spec.record).private
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

// isRecordPrivate reports whether fullType itself declares
// (spec.record).private = true. Unlike store_principal/store_namespace this
// is a MESSAGE-level annotation, not a field one (see Go's goPrivateEgressRows,
// which this mirrors) — a private record's guarantee is stronger than a plain
// (spec.store_principal) field's soft filter: a mismatch aborts the process
// instead of silently dropping the row (rustPrincipalCrash), because reaching
// a foreign owner's row here means the store's own query already leaked it.
func (es *rustEgressSanitizer) isRecordPrivate(fullType, servicePkg string) bool {
	if es == nil || fullType == "" {
		return false
	}
	pkg, name := splitRustEgressFQN(fullType, servicePkg)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return false
	}
	return msg.Annotations["record_private"].BoolValue
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
		itemFullType := fpkg + "." + f.Type
		itemField := es.storePrincipalField(itemFullType, servicePkg)
		if itemField == "" {
			continue
		}
		out = append(out, rustPrincipalListField{
			Field:     prostSnakeName(f.Name),
			ItemField: itemField,
			Private:   es.isRecordPrivate(itemFullType, servicePkg),
		})
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

// rustPrivatePrincipalResolve renders the local a (spec.record).private pin
// binds against. Unlike rustPrincipalResolve's plain caller.subject(), a
// private record read behind a delegated S2S call must pin against the
// DELEGATE (the `act` claim), not the calling service's own subject — the
// service is not the row's owner, whoever it is acting for is. A service/
// workload token carrying no act claim has no delegate to pin to at all and
// is refused outright (permission_denied, not unauthenticated: the caller IS
// authenticated, it simply cannot read a private record on its own behalf).
// The chassis Authorizer's auth_actors gate (authorizer.rs.tmpl) has already
// decided whether this act may call the method at all by the time this
// resolve runs; this only decides which identity the pin compares rows
// against.
func rustPrivatePrincipalResolve(local, method, indent string) string {
	return fmt.Sprintf(`%[1]slet %[2]s = caller.as_ref()
%[1]s    .ok_or_else(|| tonic::Status::unauthenticated("%[3]s requires an authenticated caller"))?;
%[1]slet %[2]s = match %[2]s.token().sub_type().unwrap_or("") {
%[1]s    "service" | "workload" => %[2]s.token().actor()
%[1]s        .filter(|s| !s.is_empty())
%[1]s        .map(|s| s.to_string())
%[1]s        .ok_or_else(|| tonic::Status::permission_denied("%[3]s reads a private record and requires a delegated caller identity (act claim)"))?,
%[1]s    _ => %[2]s.token().subject()
%[1]s        .filter(|s| !s.is_empty())
%[1]s        .map(|s| s.to_string())
%[1]s        .ok_or_else(|| tonic::Status::unauthenticated("%[3]s requires an authenticated caller"))?,
%[1]s};
`, indent, local, method)
}

// anyPrincipalFieldPrivate reports whether any field in the list is a
// (spec.record).private row — the resolve function for the whole block (they
// share one __read_principal binding) must be the private-aware one if so.
func anyPrincipalFieldPrivate(fields []rustPrincipalListField) bool {
	for _, f := range fields {
		if f.Private {
			return true
		}
	}
	return false
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
// filter. A plain store_principal field is a soft filter: rows the caller
// does not own are dropped and the call proceeds. A field on a
// (spec.record).private row is stronger — ValidateRecordPrivateAnnotations
// already requires it to be store_principal-annotated, and reaching a foreign
// owner's row here means the store's own query is leaking, the same posture
// (spec.store_namespace) already takes, so the process aborts instead of
// filtering (rustPrincipalCrash).
func rustResponsePrincipalFilterBlock(fields []rustPrincipalListField, method string) string {
	out := "\n            // (spec.store_principal): filter rows to the caller's principal.\n"
	if anyPrincipalFieldPrivate(fields) {
		out += rustPrivatePrincipalResolve("__read_principal", method, "            ")
	} else {
		out += rustPrincipalResolve("__read_principal", method, "            ")
	}
	out += "            let mut resp = resp;\n"
	for _, f := range fields {
		if !f.Private {
			out += fmt.Sprintf("            resp.%s.retain(|__row| __row.%s == __read_principal);\n", f.Field, f.ItemField)
			continue
		}
		out += fmt.Sprintf(`            let __priv_before = resp.%[1]s.len();
            resp.%[1]s.retain(|__row| __row.%[2]s == __read_principal);
            if resp.%[1]s.len() != __priv_before {
%[3]s            }
`, f.Field, f.ItemField, rustPrincipalCrash(method, f.Field, "                "))
	}
	return out
}

// rustPrincipalCrash renders the abort taken when a (spec.record).private
// row's owner did not match the caller — see rustNamespaceCrash, the same
// doctrine applied to the principal seam.
func rustPrincipalCrash(method, field, indent string) string {
	return fmt.Sprintf(`%[1]seprintln!("FATAL: (spec.record).private backstop fired: %[2]s returned rows in %[3]s owned by a principal other than the caller's; the store's own query is leaking across owners");
%[1]sstd::process::abort();
`, indent, method, field)
}
