// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/collection"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

// THE TWO-TENANT NEGATIVE TEST, one arm per rendered seam.
//
// The property under test is the one the tenant boundary is FOR: a caller
// holding /tenants/acme reading a tenant-owned service's s2s record plane sees
// its own rows and NEVER a /tenants/beta row, even though both rows sit in the
// same store and auth_services let the caller through at the RPC layer. Its
// paired control is the one the doctrine equally requires: a /system peer
// (argus, stratum) reading the SAME surface still sees both, because that
// fleet-wide read is what the plane was built for.
//
// Each test below renders a seam through the FULL wrapper renderer, asserts the
// text it compiles is verbatim what the wrapper writes to disk, then compiles
// and RUNS it. What is executed is generator output; the harness supplies only
// the types the wrapper's own imports would otherwise provide (a row, a
// response builder, a watch event, the Auth/ProtoException seams) and the two
// rows themselves. A render-only assertion could not tell a filter that keeps
// the right rows from one that keeps none.

// javaTenantHarness is the support the generated seams compile against: the
// smallest stand-ins for the protobuf and chassis types they name. Nothing here
// makes a filtering decision — every comparison lives in generator output.
const javaTenantHarness = `
class ProtoException extends RuntimeException {
    static final int INVALID_ARGUMENT = 3;
    static final int PERMISSION_DENIED = 7;
    ProtoException(int code, String message) { super(code + ": " + message); }
}

class Token {
    private final String namespace;
    Token(String namespace) { this.namespace = namespace; }
    String getNamespace() { return namespace; }
}

class Auth {
    static String caller = "";
    static String callerNamespaceFromContext() { return caller; }
    static Token verifiedTokenFromContext() { return caller.isEmpty() ? null : new Token(caller); }
}

class Row {
    private final String eventId;
    private final String namespace;
    Row(String eventId, String namespace) { this.eventId = eventId; this.namespace = namespace; }
    String getEventId() { return eventId; }
    String getNamespace() { return namespace; }
    public String toString() { return eventId + "@" + namespace; }
}

class Resp {
    final java.util.List<Row> items;
    Resp(java.util.List<Row> items) { this.items = new java.util.ArrayList<>(items); }
    Builder toBuilder() { return new Builder(items); }
    public String toString() { return items.toString(); }
    static class Builder {
        private final java.util.List<Row> items;
        Builder(java.util.List<Row> src) { this.items = new java.util.ArrayList<>(src); }
        int getItemsCount() { return items.size(); }
        Row getItems(int i) { return items.get(i); }
        void removeItems(int i) { items.remove(i); }
        Resp build() { return new Resp(items); }
    }
}

class Change {
    private final Row item;
    Change(Row item) { this.item = item; }
    boolean hasItem() { return item != null; }
    Row getItem() { return item; }
}

class Event {
    private final Row snapshotItem;
    private final Change delta;
    Event(Row snapshotItem, Change delta) { this.snapshotItem = snapshotItem; this.delta = delta; }
    boolean hasSnapshotItem() { return snapshotItem != null; }
    Row getSnapshotItem() { return snapshotItem; }
    boolean hasDelta() { return delta != null; }
    Change getDelta() { return delta; }
}

class Query {
    private final String namespace;
    Query(String namespace) { this.namespace = namespace; }
    boolean hasNamespace() { return namespace != null; }
    String getNamespace() { return namespace == null ? "" : namespace; }
    Builder toBuilder() { return new Builder(namespace); }
    static class Builder {
        private String namespace;
        Builder(String namespace) { this.namespace = namespace; }
        Builder setNamespace(String v) { this.namespace = v; return this; }
        Query build() { return new Query(namespace); }
    }
}

class Req {
    private final Query query;
    Req(Query query) { this.query = query; }
    Query getQuery() { return query; }
    Builder toBuilder() { return new Builder(query); }
    static class Builder {
        private Query query;
        Builder(Query query) { this.query = query; }
        Builder setQuery(Query.Builder v) { this.query = v.build(); return this; }
        Req build() { return new Req(query); }
    }
}
`

// runJavaTenantSeam compiles Main.java (harness + the verbatim seam) and runs it
// with args, returning stdout+stderr and the exit code.
func runJavaTenantSeam(t *testing.T, body string, args ...string) (string, int) {
	t.Helper()
	dir := t.TempDir()
	src := filepath.Join(dir, "Main.java")
	if err := os.WriteFile(src, []byte(javaTenantHarness+"\n"+body), 0o644); err != nil {
		t.Fatalf("write Main.java: %v", err)
	}
	if out, err := exec.Command("javac", "-d", dir, src).CombinedOutput(); err != nil {
		t.Fatalf("javac on the generated seam failed: %v\n%s\n--- program ---\n%s", err, out, javaTenantHarness+"\n"+body)
	}
	out, err := exec.Command("java", append([]string{"-cp", dir, "Main"}, args...)...).CombinedOutput()
	code := 0
	if err != nil {
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("running the generated seam: %v\n%s", err, out)
		}
		code = ee.ExitCode()
	}
	return string(out), code
}

func requireJava(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("javac"); err != nil {
		t.Skip("javac not installed; skipping the executable two-tenant test")
	}
}

// mustBeInWrapper fails unless the seam about to be compiled is VERBATIM what
// the full wrapper renderer writes. Without this the executable arms below
// would prove a property of a function nobody generates from.
func mustBeInWrapper(t *testing.T, wrapper, seam, what string) {
	t.Helper()
	if !strings.Contains(wrapper, seam) {
		t.Fatalf("the %s seam compiled below is not what the wrapper emits\n--- seam ---\n%s\n--- wrapper ---\n%s", what, seam, wrapper)
	}
}

func namespacePageMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name: "ListAgentEvent", InputType: "QueryHandle", OutputType: "PullAgentEventResponse",
		FullInputType: "query.QueryHandle", FullOutputType: "query.PullAgentEventResponse",
		AuthDisabled: true,
		Collection:   &parser.CollectionDescriptor{Mode: "PAGE", OrderBy: []string{"event_id"}},
		Resolved: &collection.Resolved{
			Mode: "PAGE", PageItemType: "agentsensor.AgentEvent", PageItemsField: "items",
			DefaultPageSize: 100, MaxPageSize: 10000, OrderBy: []string{"event_id"},
		},
	}
}

func namespaceWatchMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name: "WatchAgentEvent", InputType: "QueryHandle", OutputType: "AgentEventWatchEvent",
		FullInputType: "query.QueryHandle", FullOutputType: "query.AgentEventWatchEvent",
		ServerStreaming: true, AuthDisabled: true,
		Collection: &parser.CollectionDescriptor{Mode: "WATCH", OrderBy: []string{"event_id"}},
		Resolved: &collection.Resolved{
			Mode: "WATCH", ReplayDepth: 1024, OrderBy: []string{"event_id"}, WatchOneOf: "event",
			WatchItemType: "agentsensor.AgentEvent", WatchChangeType: "query.AgentEventWatchChange",
		},
	}
}

func namespacePredicateMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name: "CreateAgentEventQuery", InputType: "CreateAgentEventQueryRequest", OutputType: "SampleAgentEventResponse",
		FullInputType: "query.CreateAgentEventQueryRequest", FullOutputType: "query.SampleAgentEventResponse",
		AuthDisabled: true,
	}
}

func namespaceUnaryMethod() *parser.MethodInfo {
	return &parser.MethodInfo{
		Name: "SampleAgentEvent", InputType: "SampleAgentEventRequest", OutputType: "SampleAgentEventResponse",
		FullInputType: "query.SampleAgentEventRequest", FullOutputType: "query.SampleAgentEventResponse",
		AuthDisabled: true,
	}
}

// SEAM 1 of 4 — the PAGE filter.
func TestTenantS2S_PageFilterKeepsOnlyTheCallersTenant(t *testing.T) {
	requireJava(t)
	seam := javaNamespacePageFilter("namespace", "Row", "__page", "ListAgentEvent", "            ", true)
	// The same call the wrapper makes, differing only in the row type and the
	// list it reads — everything the filter DECIDES is identical.
	mustBeInWrapper(t, renderNamespaceMethod(t, namespacePageMethod(), false, true),
		javaNamespacePageFilter("namespace", "Agentsensor.AgentEvent", "__page.items", "ListAgentEvent", "            ", true), "page")

	body := `public class Main {
    public static void main(String[] args) {
        Auth.caller = args[0];
        java.util.List<Row> __page = new java.util.ArrayList<>();
        __page.add(new Row("r-acme", "/tenants/acme"));
        if (args.length > 1) { __page.add(new Row("r-beta", "/tenants/beta")); }
` + seam + `        System.out.println("DELIVERED " + __tenantItems);
    }
}
`
	// The tenant caller with only its own row: the page is delivered whole, so
	// the arms below are the filter firing and not the harness being broken.
	out, code := runJavaTenantSeam(t, body, "/tenants/acme")
	if code != 0 || !strings.Contains(out, "DELIVERED [r-acme@/tenants/acme]") {
		t.Fatalf("a tenant caller must still receive its OWN page rows: exit %d\n%s", code, out)
	}
	// The two-tenant arm: beta's row never reaches an acme caller.
	out, code = runJavaTenantSeam(t, body, "/tenants/acme", "with-beta")
	if code == 0 {
		t.Fatalf("a /tenants/beta page row must not be delivered to a /tenants/acme caller\n%s", out)
	}
	if strings.Contains(out, "DELIVERED") || strings.Contains(out, "r-beta@/tenants/beta]") {
		t.Fatalf("a /tenants/beta row leaked to a /tenants/acme caller\n%s", out)
	}
	// The regression control: the /system peer still reads fleet-wide.
	out, code = runJavaTenantSeam(t, body, "/system/services/argus", "with-beta")
	if code != 0 || !strings.Contains(out, "DELIVERED [r-acme@/tenants/acme, r-beta@/tenants/beta]") {
		t.Fatalf("a /system peer must keep its fleet-wide page read: exit %d\n%s", code, out)
	}
}

// SEAM 2 of 4 — the query PREDICATE pin. The only seam that answers before the
// store is asked, and so the only one whose two-tenant arm is a refusal rather
// than an abort.
func TestTenantS2S_PredicatePinsTheQueryToTheCallersTenant(t *testing.T) {
	requireJava(t)
	seam := javaPredicateNamespacePin("query", "namespace", "request", "CreateAgentEventQuery", "            ", true)
	if seam == "" {
		t.Fatal("the predicate pin rendered nothing; the fixture request carries no tenant criterion")
	}
	mustBeInWrapper(t, renderNamespaceMethod(t, namespacePredicateMethod(), false, true), seam, "predicate")

	body := `public class Main {
    public static void main(String[] args) {
        Auth.caller = args[0];
        Req request = new Req(new Query(args.length > 1 ? args[1] : null));
` + seam + `        System.out.println("PREDICATE " + request.getQuery().getNamespace());
    }
}
`
	// Unset -> stamped with the caller's own tenant. The caller never names it.
	out, code := runJavaTenantSeam(t, body, "/tenants/acme")
	if code != 0 || !strings.Contains(out, "PREDICATE /tenants/acme") {
		t.Fatalf("an unset predicate must be stamped with the caller's tenant: exit %d\n%s", code, out)
	}
	// Its own tenant, named explicitly -> accepted unchanged.
	out, code = runJavaTenantSeam(t, body, "/tenants/acme", "/tenants/acme")
	if code != 0 || !strings.Contains(out, "PREDICATE /tenants/acme") {
		t.Fatalf("a predicate naming the caller's own tenant must be accepted: exit %d\n%s", code, out)
	}
	// The two-tenant arm: naming beta is refused, so beta's rows are never
	// queried for and the aborting egress backstop stays unreachable.
	out, code = runJavaTenantSeam(t, body, "/tenants/acme", "/tenants/beta")
	if code == 0 || strings.Contains(out, "PREDICATE /tenants/beta") {
		t.Fatalf("a /tenants/acme caller must not be able to query /tenants/beta: exit %d\n%s", code, out)
	}
	if !strings.Contains(out, "names a tenant other than the caller's") {
		t.Fatalf("the refusal must name the boundary it enforced\n%s", out)
	}
	// The regression control: a /system peer's predicate is left exactly as it
	// wrote it, including one naming a tenant — that is the fleet-wide read.
	out, code = runJavaTenantSeam(t, body, "/system/services/argus", "/tenants/beta")
	if code != 0 || !strings.Contains(out, "PREDICATE /tenants/beta") {
		t.Fatalf("a /system peer's predicate must be untouched: exit %d\n%s", code, out)
	}
	out, code = runJavaTenantSeam(t, body, "/system/services/argus")
	if code != 0 || !strings.Contains(out, "PREDICATE \n") && !strings.HasSuffix(strings.TrimRight(out, "\n"), "PREDICATE") {
		t.Fatalf("a /system peer's unset predicate must stay unset: exit %d\n%s", code, out)
	}
}

// SEAM 3 of 4 — the UNARY row filter.
func TestTenantS2S_UnaryFilterKeepsOnlyTheCallersTenant(t *testing.T) {
	requireJava(t)
	san := recordNamespaceSanitizer()
	seam := javaNamespaceUnaryFilter(san.namespaceListFields("query.SampleAgentEventResponse", "query"),
		"Resp", "SampleAgentEvent", "            ", true)
	if seam == "" {
		t.Fatal("the unary filter rendered nothing")
	}
	mustBeInWrapper(t, renderNamespaceMethod(t, namespaceUnaryMethod(), false, true),
		strings.ReplaceAll(seam, "Resp.Builder", "Query.SampleAgentEventResponse.Builder"), "unary")

	body := `public class Main {
    public static void main(String[] args) {
        Auth.caller = args[0];
        java.util.List<Row> __rows = new java.util.ArrayList<>();
        __rows.add(new Row("r-acme", "/tenants/acme"));
        if (args.length > 1) { __rows.add(new Row("r-beta", "/tenants/beta")); }
        Resp response = new Resp(__rows);
` + seam + `        System.out.println("DELIVERED " + response);
    }
}
`
	out, code := runJavaTenantSeam(t, body, "/tenants/acme")
	if code != 0 || !strings.Contains(out, "DELIVERED [r-acme@/tenants/acme]") {
		t.Fatalf("a tenant caller must still receive its OWN unary rows: exit %d\n%s", code, out)
	}
	out, code = runJavaTenantSeam(t, body, "/tenants/acme", "with-beta")
	if code == 0 || strings.Contains(out, "DELIVERED") {
		t.Fatalf("a /tenants/beta row must not be delivered to a /tenants/acme caller: exit %d\n%s", code, out)
	}
	out, code = runJavaTenantSeam(t, body, "/system/services/argus", "with-beta")
	if code != 0 || !strings.Contains(out, "DELIVERED [r-acme@/tenants/acme, r-beta@/tenants/beta]") {
		t.Fatalf("a /system peer must keep its fleet-wide unary read: exit %d\n%s", code, out)
	}
}

// SEAM 4 of 4 — the WATCH guard, on both levels of the envelope.
func TestTenantS2S_WatchGuardStopsTheForeignTenantsEvents(t *testing.T) {
	requireJava(t)
	san := recordNamespaceSanitizer()
	resolve := javaNamespaceResolve("WatchAgentEvent", "            ", true)
	guard := san.javaNamespaceWatchGuard("query.AgentEventWatchEvent", "query", "WatchAgentEvent", "                ", true)
	if guard == "" {
		t.Fatal("the watch guard rendered nothing")
	}
	wrapper := renderNamespaceMethod(t, namespaceWatchMethod(), false, true)
	mustBeInWrapper(t, wrapper, resolve, "watch resolve")
	mustBeInWrapper(t, wrapper, guard, "watch guard")

	body := `public class Main {
    public static void main(String[] args) {
        Auth.caller = args[0];
        Row __row = new Row(args[1].equals("acme") ? "r-acme" : "r-beta",
                args[1].equals("acme") ? "/tenants/acme" : "/tenants/beta");
        Event __event = args[2].equals("delta") ? new Event(null, new Change(__row)) : new Event(__row, null);
` + resolve + guard + `        System.out.println("DELIVERED " + __row);
    }
}
`
	for _, level := range []string{"snapshot", "delta"} {
		// Own-tenant control at this level: the event is delivered.
		out, code := runJavaTenantSeam(t, body, "/tenants/acme", "acme", level)
		if code != 0 || !strings.Contains(out, "DELIVERED r-acme@/tenants/acme") {
			t.Fatalf("%s: a tenant caller must still receive its OWN events: exit %d\n%s", level, code, out)
		}
		// The two-tenant arm: beta's event never reaches an acme caller.
		out, code = runJavaTenantSeam(t, body, "/tenants/acme", "beta", level)
		if code == 0 || strings.Contains(out, "DELIVERED") {
			t.Fatalf("%s: a /tenants/beta event must not reach a /tenants/acme caller: exit %d\n%s", level, code, out)
		}
		// The regression control: the /system peer still watches fleet-wide.
		out, code = runJavaTenantSeam(t, body, "/system/services/argus", "beta", level)
		if code != 0 || !strings.Contains(out, "DELIVERED r-beta@/tenants/beta") {
			t.Fatalf("%s: a /system peer must keep its fleet-wide watch: exit %d\n%s", level, code, out)
		}
	}
}

// The gate is a property of the SERVICE, not of the caller: a shared service's
// s2s wrapper gains nothing, on any of the four seams, so no /tenants/* token
// arriving at argus or stratum can make one appear.
func TestTenantS2S_SharedServiceSeamsAreUnchanged(t *testing.T) {
	for _, m := range []*parser.MethodInfo{
		namespaceUnaryMethod(), namespacePageMethod(), namespaceWatchMethod(), namespacePredicateMethod(),
	} {
		shared := renderNamespaceMethod(t, m, false, false)
		for _, forbidden := range []string{"__nsPinned", "__rowNamespace", "__queryNamespace"} {
			if strings.Contains(shared, forbidden) {
				t.Fatalf("%s: a shared service's s2s wrapper must not carry %s\n%s", m.Name, forbidden, shared)
			}
		}
		if owned := renderNamespaceMethod(t, m, false, true); owned == shared {
			t.Fatalf("%s: the tenant-owned s2s wrapper must differ from the shared one", m.Name)
		}
	}
}

// And the c2s half is untouched by all of this: it pins unconditionally, with no
// runtime gate to get wrong.
func TestTenantS2S_C2SHalfHasNoRuntimeGate(t *testing.T) {
	for _, m := range []*parser.MethodInfo{
		namespaceUnaryMethod(), namespacePageMethod(), namespaceWatchMethod(), namespacePredicateMethod(),
	} {
		got := renderNamespaceMethod(t, m, true, false)
		if strings.Contains(got, "__nsPinned") || strings.Contains(got, `startsWith("/system")`) {
			t.Fatalf("%s: the c2s wrapper must pin unconditionally\n%s", m.Name, got)
		}
	}
}
