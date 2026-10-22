// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/verbs"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
)

// (spec.owner_default) is a DEFAULT held by a row's OWNER, so the refusal it
// generates must be scoped to a caller acting as that owner. These tests pin the
// scoping, and are the Java twin of go/types/ownerdefault_render_test.go: the
// same rights, the same shapes, the same refusals.

// javaOwnerDefaultSanitizer indexes a Task row that its owner may read but not
// write, plus the two request shapes that reach it.
func javaOwnerDefaultSanitizer() *javaEgressSanitizer {
	ann := map[string]model.AnnotationValue{
		"owner_default":       {Type: model.AnnotationTypeBool, BoolValue: true},
		"owner_default_read":  {Type: model.AnnotationTypeBool, BoolValue: true},
		"owner_default_write": {Type: model.AnnotationTypeBool, BoolValue: false},
	}
	principal := map[string]model.AnnotationValue{"principal": {Type: model.AnnotationTypeBool, BoolValue: true}}
	storePrincipal := map[string]model.AnnotationValue{"store_principal": {Type: model.AnnotationTypeBool, BoolValue: true}}
	return newJavaEgressSanitizer([]model.ProtoFile{{Package: "tasks", Messages: []model.Message{
		{Name: "Task", Annotations: ann, Fields: []model.Field{
			{Name: "task_id", Type: "string", Number: 1},
			{Name: "created_by", Type: "string", Number: 2, Annotations: storePrincipal},
		}},
		{Name: "UpdateTaskRequest", Fields: []model.Field{
			{Name: "task_id", Type: "string", Number: 1},
			{Name: "created_by", Type: "string", Number: 2, Annotations: principal},
		}},
		{Name: "UpdateTaskResponse", Fields: []model.Field{
			{Name: "task", Type: "Task", Number: 1, IsMessage: true},
		}},
		{Name: "BareUpdateRequest", Fields: []model.Field{
			{Name: "task_id", Type: "string", Number: 1},
		}},
	}}})
}

func javaOwnerDefaultMethod(name string, pattern verbs.Pattern, access verbs.Access, input string) *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:           name,
		InputType:      input,
		OutputType:     "UpdateTaskResponse",
		FullInputType:  "tasks." + input,
		FullOutputType: "tasks.UpdateTaskResponse",
		Class:          &verbs.Classification{Verb: name, Pattern: pattern, Access: access},
	}
}

func renderJavaOwnerDefault(m *parser.MethodInfo) string {
	return generateJavaGRPCWrapper(m, "Tasks", "tasks", "Tasks."+m.InputType, "Tasks."+m.OutputType,
		"Helpers", "Helpers", false, false, map[string]service.AccountingInfo{}, "tasks", "vservices",
		map[string]string{}, proto.SkippedTraceTypes{}, javaOwnerDefaultSanitizer(), nil, nil)
}

// An UPDATE of a {write:false} type refuses the OWNER — the caller whose subject
// equals the request's bound principal — and only while that caller's grant
// carries no wider (spec.principal_scope).
func TestJavaOwnerDefault_UpdateRefusesTheOwnerNotEveryCaller(t *testing.T) {
	got := renderJavaOwnerDefault(javaOwnerDefaultMethod("UpdateTask", verbs.PatternUpdate, verbs.AccessWrite, "UpdateTaskRequest"))
	for _, want := range []string{
		`var __odEnt = __odTok == null ? null : __odTok.getServiceEntitlements();`,
		`final String __principalScope = __odEnt == null ? "" : __odEnt.getPrincipalScope();`,
		`final boolean __scopeIsSelf = __principalScope.isEmpty()`,
		`|| "PRINCIPAL_SCOPE_SELF_ONLY".equals(__principalScope)`,
		`if (__scopeIsSelf && request.getCreatedBy().equals(__odCaller)) {`,
		`throw new ProtoException(ProtoException.PERMISSION_DENIED, "UpdateTask: (spec.owner_default) on Task (write) denies the owner this operation");`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("UpdateTask wrapper missing %q\ngot:\n%s", want, got)
		}
	}
	// The refusal must not be reachable before the comparison it is scoped by,
	// the comparison must follow the (spec.principal) stamp it reads, and all of
	// it must run before the handler.
	stamp := strings.Index(got, "request = request.toBuilder().setCreatedBy(")
	cond := strings.Index(got, "if (__scopeIsSelf && request.getCreatedBy().equals(__odCaller)) {")
	deny := strings.Index(got, "denies the owner this operation")
	handler := strings.Index(got, "handler.updateTask(caller, request)")
	if stamp < 0 || cond < stamp || deny < cond || handler < deny {
		t.Fatalf("want stamp < owner comparison < refusal < handler, got %d %d %d %d\n%s", stamp, cond, deny, handler, got)
	}
}

// A READ the owner may not make is refused for a self-scoped caller with no row
// comparison: the (spec.store_principal) read filter has already narrowed the
// rows to the caller.
func TestJavaOwnerDefault_ForbiddenReadRefusesASelfScopedCaller(t *testing.T) {
	san := javaOwnerDefaultSanitizer()
	san.msgs["tasks\x00Task"].Annotations["owner_default_read"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: false}
	m := javaOwnerDefaultMethod("GetTask", verbs.PatternGet, verbs.AccessRead, "UpdateTaskRequest")
	got, err := javaOwnerDefaultGuard(m, san, "tasks", javaOwnerDefaultGrpcSeam("    "), "    ")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "    if (__scopeIsSelf) {\n") || !strings.Contains(got, `"GetTask: (spec.owner_default) on Task (read) denies the owner this operation"`) {
		t.Fatalf("read refusal not rendered as a self-scope check\ngot:\n%s", got)
	}
}

// CREATE brings a row into existence, so there is no prior owner for the type's
// owner rights to speak about and no refusal is generated.
func TestJavaOwnerDefault_CreateIsExempt(t *testing.T) {
	got := renderJavaOwnerDefault(javaOwnerDefaultMethod("CreateTask", verbs.PatternCreate, verbs.AccessWrite, "UpdateTaskRequest"))
	if strings.Contains(got, "spec.owner_default") {
		t.Fatalf("CreateTask emitted an owner_default refusal; creation has no prior owner\ngot:\n%s", got)
	}
}

// A READ the owner MAY make emits nothing: the option is only enforced where it
// contradicts the method's access.
func TestJavaOwnerDefault_PermittedReadEmitsNothing(t *testing.T) {
	got := renderJavaOwnerDefault(javaOwnerDefaultMethod("GetTask", verbs.PatternGet, verbs.AccessRead, "UpdateTaskRequest"))
	if strings.Contains(got, "spec.owner_default") {
		t.Fatalf("GetTask emitted a refusal for an access the owner holds\ngot:\n%s", got)
	}
}

// Without a request (spec.principal) field the wrapper cannot tell whose row is
// being written, so it refuses to generate rather than guess.
func TestJavaOwnerDefault_WriteWithoutRequestPrincipalIsAGenerationError(t *testing.T) {
	_, err := javaOwnerDefaultGuard(
		javaOwnerDefaultMethod("UpdateTask", verbs.PatternUpdate, verbs.AccessWrite, "BareUpdateRequest"),
		javaOwnerDefaultSanitizer(), "tasks", javaOwnerDefaultGrpcSeam(""), "")
	if err == nil || !strings.Contains(err.Error(), "(spec.principal)") {
		t.Fatalf("expected a generation error naming the missing (spec.principal) field, got %v", err)
	}
}

// A method whose verb did not classify cannot be judged either way.
func TestJavaOwnerDefault_UnclassifiedVerbIsAGenerationError(t *testing.T) {
	m := javaOwnerDefaultMethod("UpdateTask", verbs.PatternUpdate, verbs.AccessWrite, "UpdateTaskRequest")
	m.Class = nil
	if _, err := javaOwnerDefaultGuard(m, javaOwnerDefaultSanitizer(), "tasks", javaOwnerDefaultGrpcSeam(""), ""); err == nil {
		t.Fatal("expected a generation error for an unclassified verb touching an owner_default type")
	}
}

// The shapes with no seam at which the comparison can be made are refused at
// generation — the same set Go and Rust refuse — and the unary shape is not.
func TestJavaOwnerDefault_UnhostableShapesAreRefused(t *testing.T) {
	san := javaOwnerDefaultSanitizer()
	unary := javaOwnerDefaultMethod("UpdateTask", verbs.PatternUpdate, verbs.AccessWrite, "UpdateTaskRequest")
	if why, err := javaOwnerDefaultUnhostable(unary, san, "tasks"); err != nil || why != "" {
		t.Fatalf("a unary write can host the guard, got %q / %v", why, err)
	}
	for name, mutate := range map[string]func(*parser.MethodInfo){
		"client-streaming": func(m *parser.MethodInfo) { m.ClientStreaming = true },
		"server-streaming": func(m *parser.MethodInfo) { m.ServerStreaming = true },
	} {
		m := javaOwnerDefaultMethod("UpdateTask", verbs.PatternUpdate, verbs.AccessWrite, "UpdateTaskRequest")
		mutate(m)
		if why, err := javaOwnerDefaultUnhostable(m, san, "tasks"); err != nil || why == "" {
			t.Fatalf("%s must be refused at generation, got %q / %v", name, why, err)
		}
	}
}

// The MCP seam renders the same decision against the session's Caller value and
// reports the refusal as a tool error, after the (spec.principal) stamp.
func TestJavaOwnerDefault_McpDispatchGuardsTheTool(t *testing.T) {
	san := javaOwnerDefaultSanitizer()
	m := javaOwnerDefaultMethod("UpdateTask", verbs.PatternUpdate, verbs.AccessWrite, "UpdateTaskRequest")
	m.IsMcp, m.McpToolName = true, "update_task"
	svc := &parser.ServiceInfo{Name: "Tasks", Methods: []*parser.MethodInfo{m}, HasMcpRpcs: true}
	tools, err := javaMcpTools(svc, "tasks", "Tasks", map[string]string{}, map[string]string{}, san)
	if err != nil {
		t.Fatal(err)
	}
	got := javaMcpToolDispatch("Tasks", tools[0])
	for _, want := range []string{
		"final String __principalScope = caller.getPrincipalScope();",
		"final String __odCaller = caller.getSubject();",
		"if (__scopeIsSelf && __req.getCreatedBy().equals(__odCaller)) {",
		`return toolError("UpdateTask: (spec.owner_default) on Task (write) denies the owner this operation");`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("MCP dispatch missing %q\ngot:\n%s", want, got)
		}
	}
	stamp := strings.Index(got, "__req = __req.toBuilder().setCreatedBy(")
	cond := strings.Index(got, "if (__scopeIsSelf && __req.getCreatedBy()")
	call := strings.Index(got, "handler.updateTask(")
	if stamp < 0 || cond < stamp || call < cond {
		t.Fatalf("want stamp < owner comparison < handler call, got %d %d %d\n%s", stamp, cond, call, got)
	}

	// The generated gate drives the refusal AND its wider-scope control instead
	// of the plain reaches-its-handler case, which the guard would fail.
	gate := generateJavaMcpDispatchTest(svc, tools, javaMcpFileParams{JavaPackage: "x", FrameRequestType: "Mcp.McpRequest", FrameResponseType: "Mcp.McpResponse"})
	for _, want := range []string{"updateTaskRefusesTheRowOwner();", "updateTaskAdmitsAWiderPrincipalScope();", `\"principal_scope\":\"PRINCIPAL_SCOPE_ANY\"`} {
		if !strings.Contains(gate, want) {
			t.Fatalf("generated MCP gate missing %q\ngot:\n%s", want, gate)
		}
	}
	if strings.Contains(gate, "updateTaskReachesItsTypedHandler") {
		t.Fatalf("the gate drives the owner's call to the handler, which the guard refuses\n%s", gate)
	}

	// An empty request has no seam to compare at: refused as a tool too.
	empty := javaOwnerDefaultMethod("UpdateTask", verbs.PatternUpdate, verbs.AccessRead, "UpdateTaskRequest")
	empty.FullInputType, empty.IsMcp, empty.McpToolName = "google.protobuf.Empty", true, "update_task"
	san.msgs["tasks\x00Task"].Annotations["owner_default_read"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: false}
	if err := javaMcpToolGeneratable(&parser.ServiceInfo{Name: "Tasks"}, empty, "tasks", san); err == nil {
		t.Fatal("an MCP tool with an empty request whose owner_default guard is non-empty must be refused")
	}
}
