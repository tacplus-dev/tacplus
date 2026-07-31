// Copyright (c) 2026 Second Star Cavalier LLC

package verbs

import (
	"fmt"
	"strings"
)

// MethodShape is the minimal wire/annotation shape the classifier needs. The
// parser adapts MethodInfo into this; keeping it primitive makes the classifier
// unit-testable without tacplus types.
type MethodShape struct {
	Name            string
	ClientStreaming bool
	ServerStreaming bool
	// CollectionMode is "", "SNAPSHOT", "WATCH", or "PAGE".
	CollectionMode string
}

// Classification is the resolved pattern for a method — a first-class IR value
// that entitlements key on.
type Classification struct {
	Verb    string
	Noun    string
	Pattern Pattern
	Access  Access
	Class   Class
	// Mode is the collection mode ("", "SNAPSHOT", "WATCH", "PAGE") — retained so
	// the entitlement scope taxonomy can distinguish a PAGE list (grantable as
	// the PAGE scope) from a SNAPSHOT list.
	Mode string
}

// Classify is the total classifier: every RPC resolves to exactly one
// Classification, or it returns an error (generation hard-fails on error in
// enforcing mode; a reporting mode surfaces the error non-fatally). It enforces,
// as one gate: PascalCase, verb-first, verb ∈ registry (no synonyms / noun-first
// / unregistered), and verb↔pattern↔cardinality agreement.
func Classify(m MethodShape) (Classification, error) {
	verb, noun, err := splitVerbNoun(m.Name)
	if err != nil {
		return Classification{}, err
	}

	def, ok := Lookup(verb)
	if !ok {
		if canon, isSyn := FoldOf(verb); isSyn {
			return Classification{}, fmt.Errorf("method %q uses verb %q which is a folded synonym; use the canonical verb %q", m.Name, verb, canon)
		}
		// Heuristic: noun-first names (e.g. UserGet) put a registered verb last.
		if hint := nounFirstHint(m.Name); hint != "" {
			return Classification{}, fmt.Errorf("method %q leads with %q, not a registered verb — %s", m.Name, verb, hint)
		}
		return Classification{}, fmt.Errorf("method %q uses verb %q which is not in the closed verb registry; add it (fold test → registry) or rename", m.Name, verb)
	}

	pattern, access, err := resolvePattern(m, def)
	if err != nil {
		return Classification{}, err
	}
	return Classification{Verb: verb, Noun: noun, Pattern: pattern, Access: access, Class: def.Class, Mode: m.CollectionMode}, nil
}

// resolvePattern derives the pattern from cardinality + collection annotation and
// confirms it agrees with the verb.
func resolvePattern(m MethodShape, def VerbDef) (Pattern, Access, error) {
	switch {
	case m.ClientStreaming && m.ServerStreaming:
		// BIDI — must not be a CRUD/read verb (those are unary/collection shapes).
		if def.Pattern != PatternAction {
			return "", "", fmt.Errorf("method %q is bidirectional-streaming but verb %q implies %s; use an action/agent verb for a BIDI stream", m.Name, def.Verb, def.Pattern)
		}
		return PatternBidi, def.Access, nil

	case m.ClientStreaming:
		if def.Pattern != PatternAction {
			return "", "", fmt.Errorf("method %q is client-streaming but verb %q implies %s; use an action/agent verb for a CLIENT_STREAM", m.Name, def.Verb, def.Pattern)
		}
		return PatternClientStream, def.Access, nil

	case m.ServerStreaming:
		switch m.CollectionMode {
		case "WATCH":
			if def.Verb != "Watch" {
				return "", "", fmt.Errorf("method %q is a WATCH collection but verb is %q; a change stream must use the verb Watch", m.Name, def.Verb)
			}
			return PatternWatch, AccessRead, nil
		case "SNAPSHOT":
			if def.Verb != "List" {
				return "", "", fmt.Errorf("method %q is a SNAPSHOT collection (Tier-2/3) but verb is %q; a collection read must use the verb List", m.Name, def.Verb)
			}
			return PatternList, AccessRead, nil
		case "PAGE":
			return "", "", fmt.Errorf("method %q declares COLLECTION_MODE_PAGE but is server-streaming; PAGE is a unary RPC", m.Name)
		default:
			// A server-streaming action/agent verb (e.g. token streaming) is a
			// legitimate streaming domain op; a server-streaming read must instead
			// declare method_collection (WATCH or a Tier-2/3 SNAPSHOT).
			if def.Pattern == PatternAction {
				return PatternAction, def.Access, nil
			}
			return "", "", fmt.Errorf("method %q is server-streaming with verb %q but no method_collection; a streaming read must declare COLLECTION_MODE_SNAPSHOT or COLLECTION_MODE_WATCH", m.Name, def.Verb)
		}

	default: // unary
		switch m.CollectionMode {
		case "SNAPSHOT", "PAGE":
			if def.Verb != "List" {
				return "", "", fmt.Errorf("method %q is a %s collection but verb is %q; a collection read must use the verb List", m.Name, m.CollectionMode, def.Verb)
			}
			return PatternList, AccessRead, nil
		case "WATCH":
			return "", "", fmt.Errorf("method %q declares COLLECTION_MODE_WATCH but is unary; a WATCH must be server-streaming", m.Name)
		default:
			// No collection annotation. List/Watch require one.
			switch def.Pattern {
			case PatternList:
				return "", "", fmt.Errorf("method %q uses verb List but declares no method_collection; List requires COLLECTION_MODE_SNAPSHOT or COLLECTION_MODE_PAGE", m.Name)
			case PatternWatch:
				return "", "", fmt.Errorf("method %q uses verb Watch but is not a WATCH collection; Watch requires server-streaming with COLLECTION_MODE_WATCH", m.Name)
			default:
				// GET / CREATE / UPDATE / DELETE / ACTION — pattern is the verb's.
				return def.Pattern, def.Access, nil
			}
		}
	}
}

// splitVerbNoun parses a PascalCase RPC name into its leading verb token and the
// remaining noun. It rejects empty / non-PascalCase names.
func splitVerbNoun(name string) (verb, noun string, err error) {
	if name == "" {
		return "", "", fmt.Errorf("empty method name")
	}
	if name[0] < 'A' || name[0] > 'Z' {
		return "", "", fmt.Errorf("method %q is not PascalCase (must start with an uppercase letter, verb first)", name)
	}
	words := splitPascal(name)
	if len(words) == 0 {
		return "", "", fmt.Errorf("method %q has no parseable words", name)
	}
	verb = words[0]
	noun = strings.Join(words[1:], "")
	return verb, noun, nil
}

// LeadingVerb returns the first PascalCase token in an RPC name. It is useful
// for diagnostics that need to report synonym folds even when classification
// itself rejects the method.
func LeadingVerb(name string) (string, bool) {
	verb, _, err := splitVerbNoun(name)
	if err != nil {
		return "", false
	}
	return verb, true
}

// splitPascal splits a PascalCase identifier into words, treating a run of
// uppercase letters as an acronym boundary the same way protobuf/Go casing does:
// "ListEvents" → [List, Events]; "MintToken" → [Mint, Token]; "GetJWKS" → [Get, JWKS].
func splitPascal(s string) []string {
	var words []string
	runes := []rune(s)
	start := 0
	isUpper := func(r rune) bool { return r >= 'A' && r <= 'Z' }
	isLower := func(r rune) bool { return r >= 'a' && r <= 'z' }
	for i := 1; i < len(runes); i++ {
		// boundary: lower→Upper (foo|Bar), or Upper-run→Upper+lower (JWKS|Reader → at the S|R? )
		if isUpper(runes[i]) {
			prev := runes[i-1]
			if isLower(prev) {
				words = append(words, string(runes[start:i]))
				start = i
			} else if isUpper(prev) && i+1 < len(runes) && isLower(runes[i+1]) {
				// end of an acronym run before a new capitalized word
				words = append(words, string(runes[start:i]))
				start = i
			}
		}
	}
	words = append(words, string(runes[start:]))
	return words
}

// nounFirstHint returns a suggestion when a name appears noun-first: a registered
// verb (or synonym) appears as a non-leading word. Returns "" when no hint applies.
func nounFirstHint(name string) string {
	words := splitPascal(name)
	for i := 1; i < len(words); i++ {
		if _, ok := Lookup(words[i]); ok {
			return fmt.Sprintf("looks noun-first; reorder to %s%s (Verb<Noun>)", words[i], strings.Join(append(append([]string{}, words[:i]...), words[i+1:]...), ""))
		}
		if canon, ok := FoldOf(words[i]); ok {
			return fmt.Sprintf("looks noun-first and uses a synonym; reorder to %s<Noun> with the canonical verb", canon)
		}
	}
	return ""
}
