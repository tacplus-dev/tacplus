// Copyright (c) 2026 Second Star Cavalier LLC

package verbs

import "testing"

func TestRegistrySize(t *testing.T) {
	// 6 core + 16 action + 7 agent = 29 (the governed closed set).
	if len(registry) != 29 {
		t.Fatalf("registry must hold exactly 29 verbs, got %d", len(registry))
	}
	var core, action, agent int
	for _, d := range registry {
		if d.Verb == "" || d.Definition == "" || d.Boundary == "" || d.Audit == "" {
			t.Fatalf("verb metadata must be complete, got %#v", d)
		}
		if d.Retry == "" {
			t.Fatalf("verb %q has no retry policy", d.Verb)
		}
		if !IsValidScope(d.Capability) {
			t.Fatalf("verb %q has invalid capability scope %q", d.Verb, d.Capability)
		}
		if len(d.AllowedPatterns()) == 0 {
			t.Fatalf("verb %q has no allowed patterns", d.Verb)
		}
		switch d.Class {
		case ClassCore:
			core++
		case ClassAction:
			action++
		case ClassAgent:
			agent++
		default:
			t.Fatalf("verb %q has no class", d.Verb)
		}
	}
	if core != 6 || action != 16 || agent != 7 {
		t.Fatalf("class split must be 6/16/7, got %d/%d/%d", core, action, agent)
	}
}

func TestRegistryConfigValidationRejectsInvalidData(t *testing.T) {
	valid := validTestVerb("Ping")

	cases := []struct {
		name string
		doc  registryDocument
		want string
	}{
		{
			name: "invalid pattern",
			doc: registryDocument{Verbs: []VerbDef{{
				Verb: "Bad", Pattern: Pattern("BOGUS"), Access: AccessRead, Class: ClassAction,
				Capability: ScopeRead, Retry: RetryReadOnly, Definition: "d", Boundary: "b", Audit: "a",
			}}},
			want: "invalid pattern",
		},
		{
			name: "duplicate canonical verb",
			doc:  registryDocument{Verbs: []VerbDef{valid, valid}},
			want: "duplicate canonical verb",
		},
		{
			name: "synonym target unknown",
			doc: registryDocument{
				Verbs:    []VerbDef{valid},
				Synonyms: []synonymDef{{Verb: "Fetch", Canonical: "Missing"}},
			},
			want: "targets unknown canonical verb",
		},
		{
			name: "canonical verb cannot also be synonym",
			doc: registryDocument{
				Verbs:    []VerbDef{valid},
				Synonyms: []synonymDef{{Verb: "Ping", Canonical: "Ping"}},
			},
			want: "already a canonical verb",
		},
	}

	for _, c := range cases {
		_, _, err := buildRegistry(c.doc)
		if err == nil {
			t.Fatalf("%s: expected error containing %q, got nil", c.name, c.want)
		}
		if !contains(err.Error(), c.want) {
			t.Fatalf("%s: error = %q, want substring %q", c.name, err.Error(), c.want)
		}
	}
}

func validTestVerb(verb string) VerbDef {
	return VerbDef{
		Verb:       verb,
		Pattern:    PatternAction,
		Access:     AccessRead,
		Class:      ClassAction,
		Capability: ScopeRead,
		Retry:      RetryReadOnly,
		Definition: "definition",
		Boundary:   "boundary",
		Audit:      "audit",
	}
}

func TestRejectedSynonyms(t *testing.T) {
	got := RejectedSynonyms("List")
	want := []string{"Filter", "Find", "Query", "Search"}
	if len(got) != len(want) {
		t.Fatalf("RejectedSynonyms(List) = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("RejectedSynonyms(List) = %v, want %v", got, want)
		}
	}
	if got := RejectedSynonyms("Ping"); len(got) != 0 {
		t.Fatalf("RejectedSynonyms(Ping) = %v, want empty", got)
	}
}

func TestAllowedPatterns(t *testing.T) {
	generate, ok := Lookup("Generate")
	if !ok {
		t.Fatalf("Generate missing from registry")
	}
	got := generate.AllowedPatterns()
	want := []Pattern{PatternAction, PatternClientStream, PatternBidi}
	if len(got) != len(want) {
		t.Fatalf("Generate allowed patterns = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Generate allowed patterns = %v, want %v", got, want)
		}
	}

	get, ok := Lookup("Get")
	if !ok {
		t.Fatalf("Get missing from registry")
	}
	got = get.AllowedPatterns()
	if len(got) != 1 || got[0] != PatternGet {
		t.Fatalf("Get allowed patterns = %v, want [GET]", got)
	}
}

func TestSplitPascal(t *testing.T) {
	cases := map[string][]string{
		"ListEvents":      {"List", "Events"},
		"MintToken":       {"Mint", "Token"},
		"GetJWKS":         {"Get", "JWKS"},
		"GetOpenIdConfig": {"Get", "Open", "Id", "Config"},
		"Ping":            {"Ping"},
		"AssistWorkflow":  {"Assist", "Workflow"},
	}
	for in, want := range cases {
		got := splitPascal(in)
		if len(got) != len(want) {
			t.Fatalf("splitPascal(%q) = %v, want %v", in, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Fatalf("splitPascal(%q) = %v, want %v", in, got, want)
			}
		}
	}
}

func TestClassify_HappyPaths(t *testing.T) {
	cases := []struct {
		shape   MethodShape
		pattern Pattern
		access  Access
		verb    string
		noun    string
	}{
		{MethodShape{Name: "GetUser"}, PatternGet, AccessRead, "Get", "User"},
		{MethodShape{Name: "ListEvents", CollectionMode: "SNAPSHOT"}, PatternList, AccessRead, "List", "Events"},
		{MethodShape{Name: "ListEvents", CollectionMode: "PAGE"}, PatternList, AccessRead, "List", "Events"},
		{MethodShape{Name: "ListEvents", ServerStreaming: true, CollectionMode: "SNAPSHOT"}, PatternList, AccessRead, "List", "Events"},
		{MethodShape{Name: "WatchSessions", ServerStreaming: true, CollectionMode: "WATCH"}, PatternWatch, AccessRead, "Watch", "Sessions"},
		{MethodShape{Name: "SubscribeTrace", ServerStreaming: true}, PatternAction, AccessRead, "Subscribe", "Trace"},
		{MethodShape{Name: "CreateTenant"}, PatternCreate, AccessWrite, "Create", "Tenant"},
		{MethodShape{Name: "UpdateTenant"}, PatternUpdate, AccessWrite, "Update", "Tenant"},
		{MethodShape{Name: "DeleteTenant"}, PatternDelete, AccessWrite, "Delete", "Tenant"},
		{MethodShape{Name: "MintToken"}, PatternAction, AccessWrite, "Mint", "Token"},
		{MethodShape{Name: "BrokerSession", ClientStreaming: true, ServerStreaming: true}, PatternBidi, AccessWrite, "Broker", "Session"},
		{MethodShape{Name: "Authorize"}, PatternAction, AccessRead, "Authorize", ""},
		{MethodShape{Name: "Ping"}, PatternAction, AccessRead, "Ping", ""},
		{MethodShape{Name: "SendBlobs", ClientStreaming: true}, PatternClientStream, AccessWrite, "Send", "Blobs"},
		{MethodShape{Name: "AssistChat", ClientStreaming: true, ServerStreaming: true}, PatternBidi, AccessWrite, "Assist", "Chat"},
		// AI/agent
		{MethodShape{Name: "GenerateCompletion"}, PatternAction, AccessWrite, "Generate", "Completion"},
		{MethodShape{Name: "EmbedDocuments"}, PatternAction, AccessRead, "Embed", "Documents"},
		{MethodShape{Name: "GuardPrompt"}, PatternAction, AccessRead, "Guard", "Prompt"},
		{MethodShape{Name: "EvaluatePolicy"}, PatternAction, AccessRead, "Evaluate", "Policy"},
	}
	for _, c := range cases {
		got, err := Classify(c.shape)
		if err != nil {
			t.Fatalf("Classify(%q) unexpected error: %v", c.shape.Name, err)
		}
		if got.Pattern != c.pattern || got.Access != c.access {
			t.Fatalf("Classify(%q) = {%s,%s}, want {%s,%s}", c.shape.Name, got.Pattern, got.Access, c.pattern, c.access)
		}
	}
}

func TestLeadingVerb(t *testing.T) {
	verb, ok := LeadingVerb("SearchEvents")
	if !ok || verb != "Search" {
		t.Fatalf("LeadingVerb(SearchEvents) = (%q, %v), want (Search, true)", verb, ok)
	}
	if verb, ok := LeadingVerb("searchEvents"); ok || verb != "" {
		t.Fatalf("LeadingVerb(searchEvents) = (%q, %v), want empty false", verb, ok)
	}
}

func TestClassify_Rejections(t *testing.T) {
	cases := []struct {
		shape   MethodShape
		wantSub string
	}{
		{MethodShape{Name: "SearchEvents", CollectionMode: "PAGE"}, "folded synonym"}, // Search→List
		{MethodShape{Name: "QueryEvents", CollectionMode: "SNAPSHOT"}, "use the canonical verb \"List\""},
		{MethodShape{Name: "LogoutSession"}, "canonical verb \"Revoke\""},                                               // Logout→Revoke
		{MethodShape{Name: "SummarizeText"}, "canonical verb \"Generate\""},                                             // fold rule
		{MethodShape{Name: "FrobnicateWidget"}, "not in the closed verb registry"},                                      // unknown verb
		{MethodShape{Name: "UserGet"}, "noun-first"},                                                                    // noun-first
		{MethodShape{Name: "listEvents", CollectionMode: "SNAPSHOT"}, "PascalCase"},                                     // mis-cased
		{MethodShape{Name: "WatchEvents", ServerStreaming: true, CollectionMode: "SNAPSHOT"}, "must use the verb List"}, // verb↔mode mismatch
		{MethodShape{Name: "ListEvents", ServerStreaming: true, CollectionMode: "WATCH"}, "must use the verb Watch"},    // List on WATCH
		{MethodShape{Name: "ListEvents"}, "List requires COLLECTION_MODE"},                                              // List without annotation
		{MethodShape{Name: "WatchEvents"}, "Watch requires server-streaming"},                                           // Watch unary
		{MethodShape{Name: "GetEvents", ServerStreaming: true}, "must declare COLLECTION_MODE"},                         // server-stream read no annotation
		{MethodShape{Name: "CreateThing", ClientStreaming: true}, "use an action/agent verb"},                           // CRUD verb on client-stream
		{MethodShape{Name: "ListEvents", CollectionMode: "PAGE", ServerStreaming: true}, "PAGE is a unary RPC"},
	}
	for _, c := range cases {
		_, err := Classify(c.shape)
		if err == nil {
			t.Fatalf("Classify(%q) expected error containing %q, got nil", c.shape.Name, c.wantSub)
		}
		if !contains(err.Error(), c.wantSub) {
			t.Fatalf("Classify(%q) error = %q, want substring %q", c.shape.Name, err.Error(), c.wantSub)
		}
	}
}

// A server-streaming agent verb (e.g. streaming token Generate) is a legitimate
// streaming ACTION, not a misclassified read.
func TestClassify_ServerStreamingAgentVerbIsAction(t *testing.T) {
	got, err := Classify(MethodShape{Name: "GenerateCompletion", ServerStreaming: true})
	if err != nil {
		t.Fatalf("streaming Generate must classify, got error: %v", err)
	}
	if got.Pattern != PatternAction || got.Access != AccessWrite {
		t.Fatalf("streaming Generate = {%s,%s}, want {ACTION,WRITE}", got.Pattern, got.Access)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
