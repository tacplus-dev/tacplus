// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package verbs

import "strings"

// Scope is the closed entitlement-scope taxonomy (the archived RPC_PATTERNS_AND_VERBS_PLAN plan
// §5). A grant carries a scope; the generated authorizer looks up the call's
// Classification and admits the call when the granted scope is one that admits
// the method. This adds "grant any WATCH / any PAGE / any AGENT op to service S"
// on top of the existing service-wide "any RPC".
//
// Architecture is unchanged: grants live in OPA / entitlement data.
// tacplus only *classifies* the call and exposes its pattern/access so the
// authorizer can pass it into the entitlement check.
type Scope string

const (
	ScopeAny   Scope = "ANY"   // every RPC (⊇ all scopes)
	ScopeRead  Scope = "READ"  // GET + LIST + PAGE + WATCH + read-ACTION
	ScopeWrite Scope = "WRITE" // CREATE + UPDATE + DELETE + write-ACTION
	ScopeWatch Scope = "WATCH" // change streams
	ScopePage  Scope = "PAGE"  // paginated reads
	ScopeAgent Scope = "AGENT" // the whole AI/Agent verb class (⊇ per-verb AI scopes)
)

// aiVerbScope returns the per-verb AI scope for an agent-class verb (e.g.
// "Generate" → "GENERATE"). An LLM-security product needs to grant/audit these
// one at a time; AGENT ⊇ all of them.
func aiVerbScope(verb string) Scope { return Scope(strings.ToUpper(verb)) }

// ScopesAdmitting returns the set of grant scopes that authorize a call with
// this classification. The authorizer admits the call iff the caller's granted
// scope is in this set. ANY is always present. The returned scopes are ordered
// broad→narrow (ANY first) for stable output.
func (c Classification) ScopesAdmitting() []Scope {
	scopes := []Scope{ScopeAny}
	// Each method is admitted by ANY plus exactly one primary capability,
	// using the same precedence as CapabilityScope. WATCH and PAGE are their
	// own grant classes — they are NOT also admitted by READ_ANY (conservative
	// default per the archived RPC_PATTERNS_AND_VERBS_PLAN plan §5: WATCH/PAGE require their own
	// grant because they are operationally different from bounded reads).
	switch {
	case c.Class == ClassAgent:
		scopes = append(scopes, ScopeAgent, aiVerbScope(c.Verb))
	case c.Pattern == PatternWatch:
		scopes = append(scopes, ScopeWatch)
	case c.Pattern == PatternList && c.Mode == "PAGE":
		scopes = append(scopes, ScopePage)
	case c.Access == AccessRead:
		scopes = append(scopes, ScopeRead)
	case c.Access == AccessWrite:
		scopes = append(scopes, ScopeWrite)
	}
	return scopes
}

// CapabilityScope returns the narrowest broad capability bucket for this call.
// Per-verb AI scopes are exposed through ScopesAdmitting; this value is the
// package-level capability group a PACKAGE_<CAPABILITY>_ANY helper would expand.
func (c Classification) CapabilityScope() Scope {
	if c.Class == ClassAgent {
		return ScopeAgent
	}
	if c.Pattern == PatternWatch {
		return ScopeWatch
	}
	if c.Pattern == PatternList && c.Mode == "PAGE" {
		return ScopePage
	}
	if c.Access == AccessRead {
		return ScopeRead
	}
	return ScopeWrite
}

// RetryPolicy reports the generated-call retry/idempotency posture for this
// resolved method shape. It intentionally treats non-WATCH streams as no-auto
// retry because tacplus cannot replay arbitrary client or bidirectional streams.
func (c Classification) RetryPolicy(clientStreaming, serverStreaming bool) RetryPolicy {
	if c.Pattern == PatternWatch {
		return RetryWatchReconnect
	}
	if clientStreaming || serverStreaming || c.Pattern == PatternClientStream || c.Pattern == PatternBidi {
		return RetryNoGeneratedAutoRetry
	}
	if c.Access == AccessRead {
		return RetryReadOnly
	}
	return RetryIdempotencyRequired
}

// Admits reports whether a caller holding the given grant scope is authorized to
// make a call with this classification. ANY admits everything.
func (c Classification) Admits(granted Scope) bool {
	for _, s := range c.ScopesAdmitting() {
		if s == granted {
			return true
		}
	}
	return false
}

// IsValidScope reports whether s is a member of the closed scope taxonomy: one
// of the six base scopes or a per-verb scope of a registered AI/agent verb.
func IsValidScope(s Scope) bool {
	switch s {
	case ScopeAny, ScopeRead, ScopeWrite, ScopeWatch, ScopePage, ScopeAgent:
		return true
	}
	for _, d := range registry {
		if d.Class == ClassAgent && aiVerbScope(d.Verb) == s {
			return true
		}
	}
	return false
}
