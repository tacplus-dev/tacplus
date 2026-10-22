// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
)

// RedactIndicatorFieldBase is the base proto field number for the companion
// "<field>_redacted" indicator bool that tacplus injects for every (spec.redact)
// field (injected number = RedactIndicatorFieldBase + the source field's number).
// The whole band at or above this number is RESERVED for tacplus: an author field
// in it would collide with an injected indicator, so ValidateReservedFieldNumbers
// rejects any hand-authored field number >= RedactIndicatorFieldBase. Kept here in
// the parser package (which pkg/internal/proto imports) so the injector and the
// validator share one source of truth.
const RedactIndicatorFieldBase = 50000

// This file houses the recursive data-entitlement walk shared by the validator,
// the registry emitter, and every language's sanitize generator. In this repo
// all messages are top-level (no nested type declarations are used), so "a nested
// type" means a message-TYPED field pointing at another top-level message. Data
// entitlement is therefore recursive over message-typed fields: a message-typed
// field can be redacted as a WHOLE (the entire sub-message cleared) OR, when it
// is left pass-through, recursively field-by-field. BuildEntitlementTree resolves
// those references and produces the tree the downstream phases consume.

// MessageResolver indexes every message in the parsed set so a message-typed
// field can be resolved to the message it references. Keyed by (package, name);
// a name-only fallback covers the common single-definition case.
type MessageResolver struct {
	byPkgName map[string]*model.Message // key: pkg + "\x00" + name
	byName    map[string][]*model.Message
}

// NewMessageResolver builds a resolver over all parsed files. The returned
// resolver holds pointers into copies of each message, so it is safe to keep
// across the generation pass.
func NewMessageResolver(allFiles []model.ProtoFile) *MessageResolver {
	r := &MessageResolver{
		byPkgName: make(map[string]*model.Message),
		byName:    make(map[string][]*model.Message),
	}
	for fi := range allFiles {
		pf := &allFiles[fi]
		for mi := range pf.Messages {
			m := &pf.Messages[mi]
			r.byPkgName[pf.Package+"\x00"+m.Name] = m
			r.byName[m.Name] = append(r.byName[m.Name], m)
		}
	}
	return r
}

// HasEntitlement reports whether a generated "<name>Entitlement" message exists in
// the parsed set. Sanitize<T> must be emitted ONLY for a T that received an
// entitlement type — i.e. an author record or a sub-message reachable from one,
// which AppendEntitlementMessagesToProtos generates BEFORE the infra injectors run.
// Auto-generated wrapper messages (eventbus/subscribe/RecordMap) that merely embed
// a record are injected afterward and get no entitlement type, so they are excluded.
func (r *MessageResolver) HasEntitlement(name string) bool {
	return len(r.byName[name+"Entitlement"]) > 0
}

// Resolve returns the message a message-typed field references. currentPkg is the
// package of the message that owns the field; field.TypePackage (when set) names
// a cross-package reference. Returns (nil, false) when the type cannot be found —
// e.g. a cross-package well-known type we do not model, or a nested-declaration
// type that was never hoisted to top level (ValidateNoNestedRedact guards that).
func (r *MessageResolver) Resolve(currentPkg string, f model.Field) (*model.Message, bool) {
	if !f.IsMessage || f.IsMap {
		return nil, false
	}
	pkg := f.TypePackage
	if pkg == "" {
		pkg = currentPkg
	}
	if m, ok := r.byPkgName[pkg+"\x00"+f.Type]; ok {
		return m, true
	}
	// Fall back to a name-only lookup when the package key does not match (the
	// cross-package TypePackage is only the first path segment). Unambiguous only.
	if ms := r.byName[f.Type]; len(ms) == 1 {
		return ms[0], true
	}
	return nil, false
}

// EntitlementNode is a message in the recursive entitlement tree.
type EntitlementNode struct {
	Message     *model.Message
	Package     string
	DefaultMode string // message-level (spec.data_entitlement) default; "" if none
	Fields      []EntitlementField
}

// EntitlementField is one field of an EntitlementNode that participates in data
// entitlement: it either carries its own mode, or is a message-typed field whose
// sub-tree carries entitlement annotations (or both).
type EntitlementField struct {
	Field  model.Field
	Mode   string           // field-level mode ("drop"/"pseudonymize"/"may_redact"/"may_anonymize"), "" if none
	Nested *EntitlementNode // non-nil when Field is a message-typed field we recursed into
}

// IsRelevant reports whether this node carries any data-entitlement decision —
// a message-level default, a field mode, or a relevant nested child. Callers use
// it to skip messages that need no sanitize surface.
func (n *EntitlementNode) IsRelevant() bool {
	if n == nil {
		return false
	}
	if n.DefaultMode != "" {
		return true
	}
	for i := range n.Fields {
		if n.Fields[i].Mode != "" || n.Fields[i].Nested.IsRelevant() {
			return true
		}
	}
	return false
}

// BuildEntitlementTree walks msg and its message-typed fields recursively,
// resolving each reference through r. A field is included when it carries a
// redact annotation OR it is a message-typed field whose resolved sub-tree is
// itself relevant. Recursion is cycle-guarded: a message reached again on the
// current path is included as a whole-message-redactable field with a nil Nested
// (field-by-field recursion into an infinite structure is not possible; the whole
// sub-message can still be cleared).
func BuildEntitlementTree(msg *model.Message, pkg string, r *MessageResolver) *EntitlementNode {
	return buildEntitlementTree(msg, pkg, r, map[string]bool{})
}

func buildEntitlementTree(msg *model.Message, pkg string, r *MessageResolver, onPath map[string]bool) *EntitlementNode {
	node := &EntitlementNode{Message: msg, Package: pkg}
	if de, ok := msg.Annotations["data_entitlement"]; ok {
		node.DefaultMode = de.StringValue
	}

	key := pkg + "\x00" + msg.Name
	onPath[key] = true
	defer delete(onPath, key)

	for fi := range msg.Fields {
		f := msg.Fields[fi]
		mode := ""
		if ann, ok := f.Annotations["redact"]; ok {
			mode = ann.StringValue
		}

		// A message-typed field recurses: whole-message/whole-list redaction (via a
		// mode) OR field-by-field into the referenced message. A repeated message
		// field recurses ELEMENT-WISE (one sub-entitlement applied to every element);
		// the generators iterate. Maps are still excluded. Message-typed ONEOF
		// members recurse too (a hand-authored oneof envelope embedding a record
		// must sanitize through it) — they can never carry a redact MODE themselves
		// (ValidateDataEntitlementAnnotations rejects that), so the emitters only
		// need the oneof-aware recursion shape (Go wrapper-struct set, Rust prost
		// enum variant, Java per-member accessors).
		var nested *EntitlementNode
		if f.IsMessage && !f.IsMap {
			if target, ok := r.Resolve(pkg, f); ok {
				childKey := childPkg(pkg, f) + "\x00" + f.Type
				if !onPath[childKey] {
					child := buildEntitlementTree(target, childPkg(pkg, f), r, onPath)
					if child.IsRelevant() {
						nested = child
					}
				}
			}
		}

		if mode != "" || nested != nil {
			node.Fields = append(node.Fields, EntitlementField{Field: f, Mode: mode, Nested: nested})
		}
	}
	return node
}

// childPkg is the package a message-typed field's target lives in.
func childPkg(currentPkg string, f model.Field) string {
	if f.TypePackage != "" {
		return f.TypePackage
	}
	return currentPkg
}

// DataGrantToken is the canonical token naming one data-entitlement grant in
// TokenEntitlements.data_grants: "this caller may see <pkg>.<message>.<field>
// RAW". Same normalization as the RPC grant tokens (PACKAGE_SERVICE_RPC):
// PKG_MESSAGE_FIELD, upper snake. The token names the message that OWNS the
// field, not the path it was reached through, so one grant covers a nested type
// wherever it appears. Every language's generated Build<T>Entitlement uses this
// exact string, computed at generation time; OPA computes the same tokens from
// the registry.
func DataGrantToken(pkg, message, field string) string {
	return dataGrantTokenPart(pkg) + "_" + dataGrantTokenPart(message) + "_" + dataGrantTokenPart(field)
}

func dataGrantTokenPart(s string) string {
	normalized := strings.NewReplacer("-", "_", ".", "_").Replace(s)
	return strings.ToUpper(strutil.ToSnakeCase(normalized))
}

// isAnonymizeMode reports whether a mode is one of the two keyed-HMAC anonymize
// modes (valid only on string/bytes scalar fields).
func isAnonymizeMode(mode string) bool {
	return mode == "pseudonymize" || mode == "may_anonymize"
}

// isRedactMode reports whether a mode is one of the two clear-to-zero redact
// modes (valid on any scalar, message, or repeated field).
func isRedactMode(mode string) bool {
	return mode == "drop" || mode == "may_redact"
}
