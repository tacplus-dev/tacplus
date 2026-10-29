// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
)

// The Java end of the namespace backstop: which TENANT's rows a c2s caller may
// see, as opposed to which principal's (recordprincipal_render_test.go) or which
// fields egress raw.

func recordNamespaceSanitizer() *javaEgressSanitizer {
	rec := func(name string, storeNamespace string) model.Message {
		m := model.Message{
			Name: name,
			Annotations: map[string]model.AnnotationValue{
				"record":       {Type: model.AnnotationTypeBool, BoolValue: true},
				"record_owner": {Type: model.AnnotationTypeString, StringValue: "spiffe_id"},
			},
			Fields: []model.Field{
				{Name: "event_id", Type: "string", Number: 1},
				{Name: "spiffe_id", Type: "string", Number: 2},
			},
		}
		if storeNamespace != "" {
			m.Fields = append(m.Fields, model.Field{
				Name: storeNamespace, Type: "string", Number: 3,
				Annotations: map[string]model.AnnotationValue{
					"store_namespace": {Type: model.AnnotationTypeBool, BoolValue: true},
				},
			})
		}
		return m
	}
	return newJavaEgressSanitizer([]model.ProtoFile{
		{Package: "agentsensor", Messages: []model.Message{
			rec("AgentEvent", "namespace"),
			// Not a record: carries no rows, so no tenant filter is owed.
			{Name: "Blob", Fields: []model.Field{{Name: "id", Type: "string", Number: 1}}},
		}},
		{Package: "query", Messages: []model.Message{
			{Name: "SampleAgentEventResponse", Fields: []model.Field{
				{Name: "items", Type: "AgentEvent", TypePackage: "agentsensor", IsRepeated: true, IsMessage: true, Number: 1},
			}},
			{Name: "SampleBlobResponse", Fields: []model.Field{
				{Name: "items", Type: "Blob", TypePackage: "agentsensor", IsRepeated: true, IsMessage: true, Number: 1},
			}},
			// The watch envelope the record query plane emits: a bare row, and a
			// change wrapper around one.
			{Name: "AgentEventWatchChange", Fields: []model.Field{
				{Name: "item", Type: "AgentEvent", TypePackage: "agentsensor", IsMessage: true, Number: 1},
			}},
			{Name: "AgentEventWatchEvent", Fields: []model.Field{
				{Name: "snapshot_item", Type: "AgentEvent", TypePackage: "agentsensor", IsMessage: true, Number: 1},
				{Name: "delta", Type: "AgentEventWatchChange", TypePackage: "query", IsMessage: true, Number: 2},
			}},
			// The PAGE leg's response, and the query predicate the lifecycle's
			// entry point carries — the other two of the four seams.
			{Name: "PullAgentEventResponse", Fields: []model.Field{
				{Name: "items", Type: "AgentEvent", TypePackage: "agentsensor", IsRepeated: true, IsMessage: true, Number: 1},
			}},
			{Name: "AgentEventQuery", Fields: []model.Field{
				{Name: "namespace", Type: "string", Number: 1, Annotations: map[string]model.AnnotationValue{
					"store_namespace": {Type: model.AnnotationTypeBool, BoolValue: true},
				}},
			}},
			{Name: "CreateAgentEventQueryRequest", Fields: []model.Field{
				{Name: "query", Type: "AgentEventQuery", TypePackage: "query", IsMessage: true, Number: 1},
			}},
		}},
	})
}

func TestJavaNamespaceUnaryFilterHaltsOnMismatch(t *testing.T) {
	san := recordNamespaceSanitizer()
	fields := san.namespaceListFields("query.SampleAgentEventResponse", "query")
	if len(fields) != 1 || fields[0].ItemField != "namespace" {
		t.Fatalf("the response's record rows must resolve their namespace field, got %+v", fields)
	}
	got := javaNamespaceUnaryFilter(fields, "SampleAgentEventResponse", "SampleAgentEvent", "            ", false)
	for _, want := range []string{
		"Auth.callerNamespaceFromContext()",
		"if (!__rowNamespace.equals(__nfb.getItems(__ni).getNamespace())) {",
		"__nfb.removeItems(__ni);",
		"Runtime.getRuntime().halt(1);",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered namespace filter is missing %q:\n%s", want, got)
		}
	}
	// halt, not exit: shutdown hooks draining in-flight work would let the very
	// rows this fired over finish leaving the process.
	if strings.Contains(got, "System.exit") {
		t.Fatalf("the namespace filter must halt, not exit:\n%s", got)
	}
}

// A non-record response carries no rows, so no filter is owed over it and none
// is rendered — the guard must not fire on every list-bearing message.
func TestJavaNamespaceFilterSkipsNonRecordRows(t *testing.T) {
	san := recordNamespaceSanitizer()
	if fs := san.namespaceListFields("query.SampleBlobResponse", "query"); len(fs) != 0 {
		t.Fatalf("a non-record response must owe no namespace filter, got %+v", fs)
	}
}

// Both levels of the watch envelope are guarded: the plane emits a bare
// snapshot row AND a change wrapper around one, and guarding only the first
// would stream every delta unchecked.
func TestJavaNamespaceWatchGuardCoversBothLevels(t *testing.T) {
	san := recordNamespaceSanitizer()
	got := san.javaNamespaceWatchGuard("query.AgentEventWatchEvent", "query", "WatchAgentEvent", "                ", false)
	for _, want := range []string{
		"__event.hasSnapshotItem() && !__rowNamespace.equals(__event.getSnapshotItem().getNamespace())",
		"__event.hasDelta() && __event.getDelta().hasItem() && !__rowNamespace.equals(__event.getDelta().getItem().getNamespace())",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("rendered watch guard is missing %q:\n%s", want, got)
		}
	}
	if strings.Count(got, "Runtime.getRuntime().halt(1);") != 2 {
		t.Fatalf("both watch levels must halt on a foreign row:\n%s", got)
	}
}

// Executable control. The GENERATED crash snippet is compiled and run: it must
// kill the JVM outright. A thrown exception would degrade to one failed RPC and
// be swallowed by the stream's error path, which is exactly what the decision
// rejected.
func TestGeneratedJavaNamespaceCrashHaltsTheJvm(t *testing.T) {
	if _, err := exec.LookPath("javac"); err != nil {
		t.Skip("javac not installed; skipping the executable namespace-crash control")
	}
	dir := t.TempDir()
	// The body is VERBATIM generator output; only the local it reads and the
	// line after it are the harness.
	program := `public class Crash {
    public static void main(String[] args) {
        final String __rowNamespace = "/acme/team-a";
        if (args.length > 0) {
` + javaNamespaceCrash("SampleAgentEvent", "rows in items", "            ") + `        }
        System.out.println("SURVIVED");
    }
}
`
	src := filepath.Join(dir, "Crash.java")
	if err := os.WriteFile(src, []byte(program), 0o644); err != nil {
		t.Fatalf("write Crash.java: %v", err)
	}
	if out, err := exec.Command("javac", "-d", dir, src).CombinedOutput(); err != nil {
		t.Fatalf("javac on the generated crash failed: %v\n%s\n--- program ---\n%s", err, out, program)
	}

	// Mismatch arm: the crash runs.
	var sb strings.Builder
	crash := exec.Command("java", "-cp", dir, "Crash", "mismatch")
	crash.Stdout = &sb
	err := crash.Run()
	if err == nil {
		t.Fatalf("the generated crash must halt the JVM; it exited 0 with stdout %q", sb.String())
	}
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("running the generated crash: %v", err)
	}
	if ee.ExitCode() != 1 {
		t.Fatalf("the generated crash must halt(1), got exit code %d", ee.ExitCode())
	}
	if strings.Contains(sb.String(), "SURVIVED") {
		t.Fatalf("the JVM ran past the generated crash: %q", sb.String())
	}

	// Paired arm: the SAME program without the mismatch runs to completion, so
	// the halt above is the crash firing and not the harness being broken.
	out, err := exec.Command("java", "-cp", dir, "Crash").CombinedOutput()
	if err != nil {
		t.Fatalf("the no-mismatch arm must exit normally: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "SURVIVED") {
		t.Fatalf("the no-mismatch arm must reach the end, got %q", out)
	}
}

// End-to-end over the FULL wrapper renderer, not just the seam function: a c2s
// method returning record rows must carry the namespace filter in the code that
// is actually written to disk. The seam tests above would still pass if the
// splice into generateJavaGRPCWrapper were dropped.
func renderNamespaceWrapper(t *testing.T, isC2S bool) string {
	t.Helper()
	m := &parser.MethodInfo{
		Name:           "SampleAgentEvent",
		InputType:      "SampleAgentEventRequest",
		OutputType:     "SampleAgentEventResponse",
		FullInputType:  "query.SampleAgentEventRequest",
		FullOutputType: "query.SampleAgentEventResponse",
		AuthDisabled:   true,
	}
	return renderNamespaceMethod(t, m, isC2S, false)
}

// renderNamespaceMethod is the same full-wrapper render for an arbitrary method
// and either surface. recordPlaneS2S is the s2s record-plane runtime gate.
func renderNamespaceMethod(t *testing.T, m *parser.MethodInfo, isC2S, recordPlaneS2S bool) string {
	t.Helper()
	return generateJavaGRPCWrapperSurface(
		m, "RecordQuery", "query", "Query."+m.InputType, "Query."+m.OutputType,
		"Helpers", "Helpers", false, true, map[string]service.AccountingInfo{},
		"query", "vservices", map[string]string{}, proto.SkippedTraceTypes{},
		recordNamespaceSanitizer(), nil, nil, isC2S, recordPlaneS2S,
	)
}

func TestJavaNamespaceFilterLandsInGeneratedWrapper(t *testing.T) {
	got := renderNamespaceWrapper(t, true)
	for _, want := range []string{
		"Auth.callerNamespaceFromContext()",
		"__nfb.removeItems(__ni);",
		"Runtime.getRuntime().halt(1);",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("generated c2s wrapper is missing %q\ngot:\n%s", want, got)
		}
	}
	// The filter must run before the response is written to the observer.
	filter := strings.Index(got, "Auth.callerNamespaceFromContext()")
	send := strings.Index(got, "responseObserver.onNext(response);")
	if filter < 0 || send < 0 || filter > send {
		t.Fatalf("the namespace filter must run before the response is sent\ngot:\n%s", got)
	}
}

// Paired control, NON-RECORD half: the IDENTICAL method on an s2s surface that
// is NOT a synthesized record query plane generates NO namespace filter. The
// (spec.store_namespace) seams belong to the record plane alone; a hand-authored
// s2s service is not one, and its wrapper must be byte-for-byte what it always
// was. This is the regression guard that the gate widening below did not leak
// into every other s2s surface in the fleet.
func TestJavaNamespaceFilterIsC2SOnly(t *testing.T) {
	got := renderNamespaceWrapper(t, false)
	if strings.Contains(got, "callerNamespaceFromContext") || strings.Contains(got, "halt(1)") {
		t.Fatalf("a non-record s2s wrapper must carry no namespace filter\ngot:\n%s", got)
	}
	if strings.Contains(got, "__nsPinned") {
		t.Fatalf("a non-record s2s wrapper must carry no runtime tenant gate\ngot:\n%s", got)
	}
}

// The record-plane half of the same control: the SAME method on the SAME s2s
// surface, when that surface IS the synthesized record query plane, carries the
// seams — under the runtime gate, never unconditionally.
//
// The host's own (spec.service).tenant_owned is NOT an input here, which is the
// point of this test's name change: the boundary is the CALLER's namespace, read
// per request, so the pin is emitted on every s2s record plane and a /system
// peer is let through at runtime rather than at generation time.
func TestJavaNamespaceFilterOnS2SRecordPlane(t *testing.T) {
	got := renderNamespaceMethod(t, &parser.MethodInfo{
		Name: "SampleAgentEvent", InputType: "SampleAgentEventRequest", OutputType: "SampleAgentEventResponse",
		FullInputType: "query.SampleAgentEventRequest", FullOutputType: "query.SampleAgentEventResponse",
		AuthDisabled: true,
	}, false, true)
	for _, want := range []string{
		"Auth.callerNamespaceFromContext()",
		javaNsPinnedDecl,
		"if (__nsPinned && !__rowNamespace.equals(__nfb.getItems(__ni).getNamespace())) {",
		"Runtime.getRuntime().halt(1);",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("the s2s record-plane wrapper is missing %q\ngot:\n%s", want, got)
		}
	}
	// (spec.store_principal) is about a USER and stays c2s-only even here.
	if strings.Contains(got, "__rowPrincipal") {
		t.Fatalf("the s2s record-plane wrapper must not gain the per-user principal filter\ngot:\n%s", got)
	}
}

// javaNsPinnedDecl is the generated runtime pin, quoted once so the tests below
// agree with the renderer on the exact predicate they reason about.
const javaNsPinnedDecl = `final boolean __nsPinned = !__rowNamespace.startsWith("/system");`

// The security case the host-keyed gate got backwards, and the reason it was
// dropped: a TENANT-OWNED CALLER reading through a SHARED, non-tenant-owned HOST
// must still be pinned to its own tenant.
//
// The old gate keyed on the host, so this path generated NO pin at all and a
// /tenants/<t> peer got a fleet-wide read of every tenant's rows. The renderer
// has no host input left to pass, so the guard is that the pin is present on the
// s2s record plane unconditionally, and that the predicate it emits classifies
// callers the right way round.
//
// The predicate is Java, so it is not executed here; it is a single
// startsWith, and this evaluates that same rule in Go against the namespace
// grammar spiffe.go classifies, to state in a test what the generated branch
// means for each kind of caller.
func TestJavaNamespacePinIsCallerKeyedNotHostKeyed(t *testing.T) {
	got := renderNamespaceMethod(t, &parser.MethodInfo{
		Name: "SampleAgentEvent", InputType: "SampleAgentEventRequest", OutputType: "SampleAgentEventResponse",
		FullInputType: "query.SampleAgentEventRequest", FullOutputType: "query.SampleAgentEventResponse",
		AuthDisabled: true,
	}, false, true)
	if !strings.Contains(got, javaNsPinnedDecl) {
		t.Fatalf("the s2s record plane must pin on the caller whoever hosts it\ngot:\n%s", got)
	}

	for _, tc := range []struct {
		caller    string
		wantPinne bool
		why       string
	}{
		{"/system", false, "the fleet's own control plane keeps its fleet-wide read"},
		{"/system/services/argus", false, "a /system peer is fleet-wide however deep its path"},
		{"/tenants/acme/services/docmanager", true, "a tenant-owned caller is pinned even through a shared host"},
		{"/tenants/beta/services/docmanager", true, "and so is the second tenant, to its own rows"},
		{"/tenants/acme", true, "a bare tenant identity is pinned too"},
	} {
		// The generated Java, evaluated as Go: !ns.startsWith("/system").
		if pinned := !strings.HasPrefix(tc.caller, "/system"); pinned != tc.wantPinne {
			t.Fatalf("caller %q: __nsPinned = %v, want %v (%s)", tc.caller, pinned, tc.wantPinne, tc.why)
		}
	}
}
