// Copyright (c) 2026 Second Star Cavalier LLC

// Package verbs is the finite, governed RPC verb registry and the exhaustive
// method-pattern classifier (see archive/RPC_PATTERNS_AND_VERBS_PLAN.md §2–§3).
//
// The verb set is CLOSED: every RPC must be named Verb<Noun> (PascalCase, verb
// first) where Verb is one of the registered canonical verbs. Synonyms fold to
// a canonical (the classifier reports the fold so authors can fix the name),
// and every method resolves to exactly one Pattern with a single static Access
// kind. This kills "different verbs for the same pattern" and gives entitlements
// (§5) a first-class pattern to key on.
//
// This package is pure data + logic with no tacplus dependencies so it can be
// unit-tested exhaustively and reused by the parser, the classifier gate, and
// any reporting command.
package verbs

import (
	"encoding/json"
	"fmt"
	"sort"

	_ "embed"
)

// Pattern is the closed set of RPC shapes. Every method resolves to exactly one
// or classification hard-fails.
type Pattern string

const (
	PatternGet          Pattern = "GET"           // single read by identity
	PatternList         Pattern = "LIST"          // collection read (SNAPSHOT or PAGE)
	PatternWatch        Pattern = "WATCH"         // change stream (snapshot→delta)
	PatternCreate       Pattern = "CREATE"        // bring a new resource into existence
	PatternUpdate       Pattern = "UPDATE"        // modify an existing resource
	PatternDelete       Pattern = "DELETE"        // remove a resource
	PatternAction       Pattern = "ACTION"        // unary domain op (not CRUD-over-a-resource)
	PatternClientStream Pattern = "CLIENT_STREAM" // client-streaming ingest
	PatternBidi         Pattern = "BIDI"          // bidirectional stream
)

// Access is the static read/write classification a method carries. Entitlements
// and retry-safety key on it. Collections and GET are READ; CREATE/UPDATE/DELETE
// are WRITE; ACTION/CLIENT_STREAM/BIDI default WRITE but a verb (or, later, a
// method annotation) may pin READ for safe/idempotent ops.
type Access string

const (
	AccessRead  Access = "READ"
	AccessWrite Access = "WRITE"
)

// Class groups verbs for governance and the AGENT entitlement scope.
type Class string

const (
	ClassCore   Class = "core"   // reads & CRUD — added rarely, reviewed strictly
	ClassAction Class = "action" // domain ops that survive the fold test
	ClassAgent  Class = "agent"  // AI/LLM ops — lighter admission bar, still closed
)

// RetryPolicy describes the semantic retry/idempotency posture for a governed
// RPC. Generated clients may be stricter at first; this is the shared contract
// that registry consumers and future client parity work key on.
type RetryPolicy string

const (
	RetryReadOnly             RetryPolicy = "READ_ONLY"               // idempotent read; unary retry is semantically safe
	RetryWatchReconnect       RetryPolicy = "WATCH_RECONNECT"         // reconnect/resubscribe with replay semantics, not unary retry
	RetryIdempotencyRequired  RetryPolicy = "IDEMPOTENCY_REQUIRED"    // write/action retry requires an idempotency contract
	RetryNoGeneratedAutoRetry RetryPolicy = "NO_GENERATED_AUTO_RETRY" // stream/ambiguous shape; do not auto-retry generated calls
)

// VerbDef is one registered canonical verb.
type VerbDef struct {
	Verb string `json:"verb"`
	// Pattern is the pattern this verb implies for a UNARY method. Stream
	// cardinality overrides it to CLIENT_STREAM/BIDI; collection annotations
	// confirm LIST/WATCH. Cross-checked by the classifier.
	Pattern Pattern `json:"pattern"`
	// Access is the verb's default access kind. ACTION/stream verbs may be
	// overridden per-method (default WRITE) once method-level access lands.
	Access Access `json:"access"`
	Class  Class  `json:"class"`
	// Capability is the broad entitlement capability this verb contributes to.
	// Method-level classification may refine it further, for example PAGE list.
	Capability Scope       `json:"capability"`
	Retry      RetryPolicy `json:"retry"`
	// Definition and Boundary mirror the dictionary so a single source drives
	// docs and error messages.
	Definition string `json:"definition"`
	Boundary   string `json:"boundary"`
	// Audit describes what use of this verb means in security/operator logs.
	Audit string `json:"audit"`
}

type registryDocument struct {
	Verbs    []VerbDef    `json:"verbs"`
	Synonyms []synonymDef `json:"synonyms"`
}

type synonymDef struct {
	Verb      string `json:"verb"`
	Canonical string `json:"canonical"`
}

//go:embed registry.json
var registryJSON []byte

// registry and synonyms are loaded from registry.json, the compiler-read
// source of truth for the closed verb dictionary. Go owns validation and
// classification; the authored dictionary lives outside code.
var registry, synonyms = mustLoadRegistry(registryJSON)

func mustLoadRegistry(data []byte) (map[string]VerbDef, map[string]string) {
	var doc registryDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		panic(fmt.Sprintf("load embedded verb registry: %v", err))
	}
	verbs, folds, err := buildRegistry(doc)
	if err != nil {
		panic(fmt.Sprintf("invalid embedded verb registry: %v", err))
	}
	return verbs, folds
}

func buildRegistry(doc registryDocument) (map[string]VerbDef, map[string]string, error) {
	if len(doc.Verbs) == 0 {
		return nil, nil, fmt.Errorf("verbs list is empty")
	}

	verbs := make(map[string]VerbDef, len(doc.Verbs))
	for _, d := range doc.Verbs {
		if err := validateVerbDef(d); err != nil {
			return nil, nil, err
		}
		if _, exists := verbs[d.Verb]; exists {
			return nil, nil, fmt.Errorf("duplicate canonical verb %q", d.Verb)
		}
		verbs[d.Verb] = d
	}

	folds := make(map[string]string, len(doc.Synonyms))
	for _, syn := range doc.Synonyms {
		if syn.Verb == "" || syn.Canonical == "" {
			return nil, nil, fmt.Errorf("synonym entries require verb and canonical: %#v", syn)
		}
		if _, exists := verbs[syn.Verb]; exists {
			return nil, nil, fmt.Errorf("synonym %q is already a canonical verb", syn.Verb)
		}
		if _, exists := verbs[syn.Canonical]; !exists {
			return nil, nil, fmt.Errorf("synonym %q targets unknown canonical verb %q", syn.Verb, syn.Canonical)
		}
		if prev, exists := folds[syn.Verb]; exists {
			return nil, nil, fmt.Errorf("duplicate synonym %q targets both %q and %q", syn.Verb, prev, syn.Canonical)
		}
		folds[syn.Verb] = syn.Canonical
	}

	return verbs, folds, nil
}

func validateVerbDef(d VerbDef) error {
	if d.Verb == "" {
		return fmt.Errorf("canonical verb entries require verb")
	}
	if !validPattern(d.Pattern) {
		return fmt.Errorf("verb %q has invalid pattern %q", d.Verb, d.Pattern)
	}
	if !validAccess(d.Access) {
		return fmt.Errorf("verb %q has invalid access %q", d.Verb, d.Access)
	}
	if !validClass(d.Class) {
		return fmt.Errorf("verb %q has invalid class %q", d.Verb, d.Class)
	}
	if !validCapability(d.Capability) {
		return fmt.Errorf("verb %q has invalid capability %q", d.Verb, d.Capability)
	}
	if !validRetryPolicy(d.Retry) {
		return fmt.Errorf("verb %q has invalid retry policy %q", d.Verb, d.Retry)
	}
	if d.Definition == "" || d.Boundary == "" || d.Audit == "" {
		return fmt.Errorf("verb %q must declare definition, boundary, and audit meaning", d.Verb)
	}
	return nil
}

func validPattern(p Pattern) bool {
	switch p {
	case PatternGet, PatternList, PatternWatch, PatternCreate, PatternUpdate, PatternDelete, PatternAction, PatternClientStream, PatternBidi:
		return true
	default:
		return false
	}
}

func validAccess(a Access) bool {
	switch a {
	case AccessRead, AccessWrite:
		return true
	default:
		return false
	}
}

func validClass(c Class) bool {
	switch c {
	case ClassCore, ClassAction, ClassAgent:
		return true
	default:
		return false
	}
}

func validCapability(s Scope) bool {
	switch s {
	case ScopeRead, ScopeWrite, ScopeWatch, ScopePage, ScopeAgent:
		return true
	default:
		return false
	}
}

func validRetryPolicy(r RetryPolicy) bool {
	switch r {
	case RetryReadOnly, RetryWatchReconnect, RetryIdempotencyRequired, RetryNoGeneratedAutoRetry:
		return true
	default:
		return false
	}
}

// Lookup returns the canonical VerbDef for a verb token, or ok=false if it is
// neither a canonical verb nor a known synonym.
func Lookup(verb string) (VerbDef, bool) {
	d, ok := registry[verb]
	return d, ok
}

// FoldOf returns the canonical verb a synonym folds to, or ok=false if the verb
// is not a known synonym.
func FoldOf(verb string) (string, bool) {
	c, ok := synonyms[verb]
	return c, ok
}

// RejectedSynonyms returns known non-canonical verbs that fold to canonical.
// These words are rejected by the classifier with "use canonical" guidance.
func RejectedSynonyms(canonical string) []string {
	var out []string
	for syn, canon := range synonyms {
		if canon == canonical {
			out = append(out, syn)
		}
	}
	sort.Strings(out)
	return out
}

// AllowedPatterns returns the method patterns this canonical verb may resolve
// to. Action/agent verbs may be unary ACTION or streaming action shapes.
func (d VerbDef) AllowedPatterns() []Pattern {
	if d.Pattern == PatternAction {
		return []Pattern{PatternAction, PatternClientStream, PatternBidi}
	}
	return []Pattern{d.Pattern}
}

// All returns every registered canonical verb (for reporting / docs generation).
func All() map[string]VerbDef {
	out := make(map[string]VerbDef, len(registry))
	for k, v := range registry {
		out[k] = v
	}
	return out
}
