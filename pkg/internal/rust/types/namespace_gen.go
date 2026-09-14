// Copyright (c) 2026 Second Star Cavalier LLC

package types

import "fmt"

// The namespace isolation seam. (spec.namespace) on a request field and
// (spec.store_namespace) on a record field are the principal seam's two
// mechanisms (principal_gen.go) bound to the caller's NAMESPACE claim instead of
// its subject, and they answer a different question: not "are these my rows" but
// "are these my TENANT's rows". Both are enforced in the generated service
// wrapper over the message index the egress sanitizer already builds.
//
// EXACT MATCH. The comparison is plain string equality against the caller's
// namespace claim. It is NOT target_namespace_field's level-or-below
// containment rule — that rule is for ADMINISTRATIVE reach down the tenant
// tree, and reusing it here would hand a caller at /acme every row of every
// tenant beneath it. Data lands in a leaf; root is a leaf like any other.
//
// A MISMATCH CRASHES. Everywhere else in this file's neighbourhood a wrong
// value is a refusal of the call in flight. Here it is std::process::abort,
// because reaching the comparison with a foreign row means the store already
// handed the wrapper another tenant's data — the filter is the backstop behind
// the service's own query logic, and the only correct reading of it firing is
// that the query logic is broken. See the (spec.store_namespace) doctrine in
// spec.proto for why a crash is the cheaper failure.

// rustNamespaceListField is a repeated record field of a response whose item
// type declares (spec.store_namespace).
type rustNamespaceListField struct {
	Field     string // snake_case response field holding the rows
	ItemField string // snake_case namespace field on the row type
}

// requestNamespaceField returns the snake_case name of fullType's
// (spec.namespace) field, or "" when it declares none. At most one such field
// per message is enforced by parser.ValidatePrincipalAnnotations.
func (es *rustEgressSanitizer) requestNamespaceField(fullType, servicePkg string) string {
	return es.annotatedStringField(fullType, servicePkg, "namespace")
}

// storeNamespaceField returns the snake_case name of fullType's
// (spec.store_namespace) field, or "" when it declares none. Every
// (spec.record) declares one — parser.ValidateRecordNamespaceAnnotations fails
// generation otherwise — so "" here means the type is not a record.
func (es *rustEgressSanitizer) storeNamespaceField(fullType, servicePkg string) string {
	return es.annotatedStringField(fullType, servicePkg, "store_namespace")
}

// namespaceListFields returns the repeated record fields of fullType whose item
// type declares (spec.store_namespace) — the rows the wrapper must hold to the
// caller's namespace before the response leaves the process.
func (es *rustEgressSanitizer) namespaceListFields(fullType, servicePkg string) []rustNamespaceListField {
	if es == nil || fullType == "" {
		return nil
	}
	pkg, name := splitRustEgressFQN(fullType, servicePkg)
	msg, ok := es.msgs[pkg+"\x00"+name]
	if !ok {
		return nil
	}
	var out []rustNamespaceListField
	for _, f := range msg.Fields {
		if !f.IsRepeated || !f.IsMessage || f.IsMap || f.IsOneOf {
			continue
		}
		fpkg := f.TypePackage
		if fpkg == "" {
			fpkg = pkg
		}
		itemField := es.storeNamespaceField(fpkg+"."+f.Type, servicePkg)
		if itemField == "" {
			continue
		}
		out = append(out, rustNamespaceListField{Field: prostSnakeName(f.Name), ItemField: itemField})
	}
	return out
}

// rustNamespaceResolve renders the local that binds the authenticated caller's
// namespace claim. `caller` is the Option<Caller> the unary wrapper already
// extracted. Fail-closed: no caller, or a caller with no namespace claim, is a
// refusal, not an empty filter.
func rustNamespaceResolve(local, method, indent string) string {
	return fmt.Sprintf(`%slet %s = caller.as_ref()
%s    .and_then(|c| c.namespace())
%s    .filter(|s| !s.is_empty())
%s    .map(|s| s.to_string())
%s    .ok_or_else(|| tonic::Status::unauthenticated("%s requires an authenticated caller carrying a namespace claim"))?;
`, indent, local, indent, indent, indent, indent, method)
}

// rustRequestNamespaceBlock renders the (spec.namespace) stamp-or-refuse check.
// Empty is stamped from the caller, equal passes, different is refused: a forged
// namespace is never silently overwritten.
//
// reqVar and indent carry the same meaning as on rustRequestPrincipalBlock: the
// binding to stamp and the column to stamp it at.
func rustRequestNamespaceBlock(field, method, reqVar, indent string) string {
	return fmt.Sprintf(`
%[1]s// (spec.namespace) %[2]s: bind the field to the caller's namespace claim.
%[4]s%[1]slet mut %[3]s = %[3]s;
%[1]sif %[3]s.%[2]s.is_empty() {
%[1]s    %[3]s.%[2]s = __namespace;
%[1]s} else if %[3]s.%[2]s != __namespace {
%[1]s    return Err(tonic::Status::invalid_argument(
%[1]s        "%[2]s does not match the authenticated caller's namespace"));
%[1]s}
%[1]slet %[3]s = %[3]s;
`, indent, field, reqVar, rustNamespaceResolve("__namespace", method, indent))
}

// rustStreamNamespaceStmts is the per-message half, run on every inbound
// message of a request stream. See rustStreamPrincipalStmts.
func rustStreamNamespaceStmts(field, msgVar, indent string) string {
	return fmt.Sprintf(`%[1]s// (spec.namespace) %[2]s: bind the field to the caller's namespace claim.
%[1]sif %[3]s.%[2]s.is_empty() {
%[1]s    %[3]s.%[2]s = __namespace.clone();
%[1]s} else if %[3]s.%[2]s != __namespace {
%[1]s    return Err(tonic::Status::invalid_argument(
%[1]s        "%[2]s does not match the authenticated caller's namespace"));
%[1]s}
`, indent, field, msgVar)
}

// rustResponseNamespaceFilterBlock renders the (spec.store_namespace) egress
// filter: rows stamped with another tenant's namespace never leave the process,
// and having had to drop one aborts.
func rustResponseNamespaceFilterBlock(fields []rustNamespaceListField, method string) string {
	out := "\n            // (spec.store_namespace): hold the rows to the caller's tenant.\n"
	out += rustNamespaceResolve("__read_namespace", method, "            ")
	out += "            let mut resp = resp;\n"
	for _, f := range fields {
		out += fmt.Sprintf(`            let __ns_before = resp.%[1]s.len();
            resp.%[1]s.retain(|__row| __row.%[2]s == __read_namespace);
            if resp.%[1]s.len() != __ns_before {
%[3]s            }
`, f.Field, f.ItemField, rustNamespaceCrash(method, f.Field, "                "))
	}
	return out
}

// rustNamespaceCrash renders the abort taken when the filter above had to drop a
// row. A panic would be caught per-task by tonic and degrade to a failed RPC, so
// the process is aborted outright — the point is that the fleet notices.
func rustNamespaceCrash(method, field, indent string) string {
	return fmt.Sprintf(`%[1]seprintln!("FATAL: (spec.store_namespace) backstop fired: %[2]s returned rows in %[3]s stamped with a namespace other than the caller's ({}); the store's own query is leaking across tenants", __read_namespace);
%[1]sstd::process::abort();
`, indent, method, field)
}
