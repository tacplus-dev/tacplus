// Copyright (c) 2026 Second Star Cavalier LLC

package common

import (
	"os"
	"strings"
	"testing"
)

// The subscribe plane's s2s_subscribe.routing_only_services NARROWING across
// the five subscriber-generating languages.
//
// A topic OWNER names, on its own record/event type, the subset of the already
// authorized auth_services whose generated subscriber Handler is typed for the
// synthesized <T>RoutingEnvelope — {delivery_id, recipient, namespace} —
// instead of the full <T>. Every other subscriber is untouched: the narrowing
// is strictly opt-in and a type that declares nothing generates exactly what it
// generated before.
//
// WHY THIS TEST. The decision of envelope-vs-full-type is made ONCE, in
// boundaries.ApplyRoutingEnvelope, but it is EXPRESSED five times — once per
// subscriber backend and its templates. A language that renders the full type
// where the others render the envelope has silently widened one consumer's view
// of every tenant's body while the owner's declaration still says otherwise,
// and nothing else in the repo would notice: python and typescript subscriber
// generation is not reached by any live generation today (both backends refuse
// EventBus subscriptions up front), so for two of the five this file is the
// only thing holding the line.
//
// WHY SOURCE-LEVEL, AND WHY BOTH HALVES — the same reasoning as
// min_group_size_parity_test.go, which this file follows:
//
//	the SHARED half — literal text that must be byte-identical in all five,
//	    because it is the same contract stated once: who declared the
//	    narrowing, and the caveat that it is a TYPE boundary and not a memory
//	    one (the full message still arrives on the wire and is still decoded).
//	the MECHANICAL half — the per-language expression of the SAME decision, one
//	    row per language: the routing-only signature, the unflagged signature
//	    that must stay unchanged, and the three projected slots.

// routingEnvelopeSources is where each language makes and renders the
// envelope-vs-full-type decision: the generator that calls the shared resolver
// and names the envelope type, plus every template that restates the signature.
var routingEnvelopeSources = map[string][]string{
	"go": {
		"../go/types/generator.go",
		"../go/types/subscriber_gen.go",
		"../../templates/go/types/subscriber.go.tmpl",
	},
	"java": {
		"../java/types/subscriber_gen.go",
		"../../templates/java/types/Subscriber.java.tmpl",
		"../../templates/java/types/Handler.java.tmpl",
	},
	"rust": {
		"../rust/types/subscriber_gen.go",
		"../../templates/rust/types/subscriber.rs.tmpl",
		"../../templates/rust/types/handler.rs.tmpl",
	},
	"python": {
		"../python/types/subscriber_gen.go",
		"../../templates/python/types/subscriber.py.tmpl",
		"../../templates/python/types/handler.py.tmpl",
	},
	"typescript": {
		"../typescript/types/subscriber_gen.go",
		"../../templates/typescript/types/subscriber.ts.tmpl",
		"../../templates/typescript/types/handler.ts.tmpl",
	},
}

// readRoutingEnvelopeSources returns each language's generator and template
// sources joined, so a rule split across three files in one language and two in
// another still reads the same way.
func readRoutingEnvelopeSources(t *testing.T) map[string]string {
	t.Helper()
	out := make(map[string]string, len(routingEnvelopeSources))
	for lang, paths := range routingEnvelopeSources {
		var b strings.Builder
		for _, p := range paths {
			src, err := os.ReadFile(p)
			if err != nil {
				t.Fatalf("%s: read %s: %v", lang, p, err)
			}
			b.Write(src)
			b.WriteString("\n")
		}
		out[lang] = b.String()
	}
	return out
}

// wantRoutingEnvelopeShared asserts one shared contract string appears in all five.
func wantRoutingEnvelopeShared(t *testing.T, srcs map[string]string, what, want string) {
	t.Helper()
	var missing []string
	for lang, src := range srcs {
		if !strings.Contains(src, want) {
			missing = append(missing, lang)
		}
	}
	if len(missing) > 0 {
		t.Errorf("%s: %v no longer emit %q, but the other languages still do. "+
			"routing_only_services must not read differently depending on which language hosts the "+
			"subscriber — either every language changes together or none does",
			what, missing, want)
	}
}

// wantRoutingEnvelopePerLanguage asserts each language's own expression of a
// shared rule, and that no language is missing a row.
func wantRoutingEnvelopePerLanguage(t *testing.T, srcs map[string]string, what string, want map[string][]string) {
	t.Helper()
	for lang, wants := range want {
		for _, w := range wants {
			if !strings.Contains(srcs[lang], w) {
				t.Errorf("%s: the %s subscriber no longer renders %q — it has dropped the decision the "+
					"other four still make, and a %s host of this subscription now sees a different "+
					"slice of the topic than every other language does",
					what, lang, w, lang)
			}
		}
	}
	if len(want) != len(routingEnvelopeSources) {
		t.Errorf("%s: the per-language table covers %d of %d languages; a language with no row is a language "+
			"nothing checks", what, len(want), len(routingEnvelopeSources))
	}
}

// ---------------------------------------------------------------------------

// ONE RESOLVER, FIVE CALLERS. The narrowing is a property of the OWNER's
// declaration and of who is being generated. Five backends deciding that
// separately is five ways to disagree about one boundary, so all five call the
// same boundaries helper and all five spell the envelope's name from the same
// shared constant.
func TestRoutingEnvelopeDecisionIsMadeByTheSharedResolverInEveryLanguage(t *testing.T) {
	srcs := readRoutingEnvelopeSources(t)

	for _, shared := range []string{
		"boundaries.ApplyRoutingEnvelope(p, config.SelfPackage, &entry)",
		"boundaries.RoutingEnvelopeSuffix",
	} {
		wantRoutingEnvelopeShared(t, srcs, "the routing-envelope resolution", shared)
	}
}

// THE CAVEAT, STATED IDENTICALLY. routing_only_services buys a TYPE boundary,
// not a memory one: the full message still arrives on the wire and is still
// decoded in the subscriber's process before the three fields are projected out
// of it. A language that drops this sentence from its generated doc comment
// hands its readers a memory guarantee the generator does not provide.
func TestRoutingEnvelopeCaveatIsIdenticalAcrossLanguages(t *testing.T) {
	srcs := readRoutingEnvelopeSources(t)

	for _, shared := range []string{
		"s2s_subscribe.routing_only_services",
		"A TYPED boundary, not a memory one: the full",
		"still arrives",
		"See spec.proto's routing_only_services comment.",
	} {
		wantRoutingEnvelopeShared(t, srcs, "the routing-envelope caveat", shared)
	}
}

// THE FLAGGED SIGNATURE. When the consuming service is named in the source
// type's routing_only_services, the generated Handler takes <T>RoutingEnvelope.
// This is the half the ask is actually about: the handler cannot be written
// against the body because the body is not in its argument type.
func TestRoutingEnvelopeHandlerTakesEnvelopeInEveryLanguage(t *testing.T) {
	srcs := readRoutingEnvelopeSources(t)

	wantRoutingEnvelopePerLanguage(t, srcs, "the routing-only Handler signature", map[string][]string{
		"go":         {"event *source.{{.EnvelopeTypeName}}, meta *eb.RecordMeta"},
		"java":       {"{{.TypeOuterClass}}.{{.EnvelopeTypeName}} event, RecordMeta meta"},
		"rust":       {"dyn Fn({{.EnvelopeTypeName}}, RecordMeta)"},
		"python":     {"import {{if .RoutingOnly}}{{.EnvelopeTypeName}}{{else}}{{.TypeName}}{{end}} as {{.TypeAlias}}"},
		"typescript": {"export type Handler = (event: {{.RoutingEnvelopeName}}, meta: RecordMeta) => Promise<void>;"},
	})
}

// THE UNFLAGGED SIGNATURE, UNCHANGED. The overwhelmingly common case is a type
// that declares no routing_only_services at all, and for it the generated
// Handler must still take the full type exactly as it did before this feature
// existed. A language whose else-branch drifts has made an opt-in narrowing
// into something every subscriber pays for.
func TestRoutingEnvelopeUnflaggedHandlerTakesFullTypeInEveryLanguage(t *testing.T) {
	srcs := readRoutingEnvelopeSources(t)

	wantRoutingEnvelopePerLanguage(t, srcs, "the unflagged Handler signature", map[string][]string{
		"go":         {"event *source.{{.TypeName}}, meta *eb.RecordMeta"},
		"java":       {"{{if .RoutingOnly}}{{.EnvelopeTypeName}}{{else}}{{.TypeName}}{{end}} event, SubscriptionMeta meta"},
		"rust":       {"dyn Fn({{.TypeName}}, RecordMeta)"},
		"python":     {"{{else}}{{.TypeName}}{{end}} as {{.TypeAlias}}"},
		"typescript": {"export type Handler = (event: {{.TypeName}}, meta: RecordMeta) => Promise<void>;"},
	})
}

// THREE SLOTS, NO MORE. The envelope is {delivery_id, recipient, namespace} and
// each slot is filled from the source field the OWNER's annotations name —
// (spec.record).key, (spec.store_principal), (spec.store_namespace) — resolved
// once by the shared resolver and spelled per language only in the projection
// itself. A language that adds a fourth assignment has widened the envelope;
// one that drops an assignment ships a routing table with an empty column.
func TestRoutingEnvelopeProjectsTheSameThreeSlotsInEveryLanguage(t *testing.T) {
	srcs := readRoutingEnvelopeSources(t)

	wantRoutingEnvelopePerLanguage(t, srcs, "the routing-envelope projection", map[string][]string{
		"go": {
			"DeliveryId: rec.Payload.Get{{.KeyFieldPascal}}(),",
			"Recipient:  rec.Payload.Get{{.PrincipalFieldPascal}}(),",
			"Namespace:  rec.Payload.Get{{.NamespaceFieldPascal}}(),",
		},
		"java": {
			".setDeliveryId(event.get{{.KeyFieldPascal}}())",
			".setRecipient(event.get{{.PrincipalFieldPascal}}())",
			".setNamespace(event.get{{.NamespaceFieldPascal}}())",
		},
		"rust": {
			"delivery_id: payload.{{.KeyFieldSnake}}.clone(),",
			"recipient: payload.{{.PrincipalFieldSnake}}.clone(),",
			"namespace: payload.{{.NamespaceFieldSnake}}.clone(),",
		},
		"python": {
			"delivery_id=record.payload.{{.KeyField}},",
			"recipient=record.payload.{{.PrincipalField}},",
			"namespace=record.payload.{{.NamespaceField}},",
		},
		"typescript": {
			"deliveryId: record.payload.{{.KeyFieldCamel}},",
			"recipient: record.payload.{{.PrincipalFieldCamel}},",
			"namespace: record.payload.{{.NamespaceFieldCamel}},",
		},
	})
}
