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
// scoping, and are the Go twin of rust/types/ownerdefault_render_test.go: the
// same rights, the same shapes, the same refusals.

// goOwnerDefaultSanitizer indexes a Task row that its owner may read but not
// write, plus the two request shapes that reach it.
func goOwnerDefaultSanitizer() *egressSanitizer {
	ann := map[string]model.AnnotationValue{
		"owner_default":       {Type: model.AnnotationTypeBool, BoolValue: true},
		"owner_default_read":  {Type: model.AnnotationTypeBool, BoolValue: true},
		"owner_default_write": {Type: model.AnnotationTypeBool, BoolValue: false},
	}
	principal := map[string]model.AnnotationValue{"principal": {Type: model.AnnotationTypeBool, BoolValue: true}}
	storePrincipal := map[string]model.AnnotationValue{"store_principal": {Type: model.AnnotationTypeBool, BoolValue: true}}
	return newEgressSanitizer([]model.ProtoFile{{Package: "tasks", Messages: []model.Message{
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

func goOwnerDefaultMethod(name string, pattern verbs.Pattern, access verbs.Access, input string) *parser.MethodInfo {
	return &parser.MethodInfo{
		Name:           name,
		InputType:      input,
		OutputType:     "UpdateTaskResponse",
		FullInputType:  "tasks." + input,
		FullOutputType: "tasks.UpdateTaskResponse",
		Class:          &verbs.Classification{Verb: name, Pattern: pattern, Access: access},
	}
}

func renderGoOwnerDefault(t *testing.T, m *parser.MethodInfo) string {
	t.Helper()
	return generateGRPCWrapper(&parser.ServiceInfo{Name: "Tasks"}, m, "tasks", "services", "",
		map[string]service.AccountingInfo{}, map[string]string{}, proto.SkippedTraceTypes{},
		goOwnerDefaultSanitizer())
}

// An UPDATE of a {write:false} type refuses the OWNER — the caller whose subject
// equals the request's bound principal — and only while that caller's grant
// carries no wider (spec.principal_scope).
func TestGoOwnerDefault_UpdateRefusesTheOwnerNotEveryCaller(t *testing.T) {
	got := renderGoOwnerDefault(t, goOwnerDefaultMethod("UpdateTask", verbs.PatternUpdate, verbs.AccessWrite, "UpdateTaskRequest"))
	for _, want := range []string{
		`caller.Token().GetServiceEntitlements().GetPrincipalScope()`,
		`__scopeIsSelf := __principalScope == "" ||`,
		`__principalScope == "PRINCIPAL_SCOPE_SELF_ONLY" ||`,
		`if __scopeIsSelf && req.CreatedBy == __odCaller {`,
		`"UpdateTask: (spec.owner_default) on Task (write) denies the owner this operation"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("UpdateTask wrapper missing %q\ngot:\n%s", want, got)
		}
	}
	// The refusal must not be reachable before the comparison it is scoped by.
	deny := strings.Index(got, "denies the owner this operation")
	cond := strings.Index(got, "if __scopeIsSelf && req.CreatedBy == __odCaller {")
	if cond < 0 || deny < cond {
		t.Fatalf("the refusal is not guarded by the owner comparison\ngot:\n%s", got)
	}
}

// CREATE brings a row into existence, so there is no prior owner for the type's
// owner rights to speak about and no refusal is generated.
func TestGoOwnerDefault_CreateIsExempt(t *testing.T) {
	got := renderGoOwnerDefault(t, goOwnerDefaultMethod("CreateTask", verbs.PatternCreate, verbs.AccessWrite, "UpdateTaskRequest"))
	if strings.Contains(got, "spec.owner_default") {
		t.Fatalf("CreateTask emitted an owner_default refusal; creation has no prior owner\ngot:\n%s", got)
	}
}

// A READ the owner MAY make emits nothing: the option is only enforced where it
// contradicts the method's access.
func TestGoOwnerDefault_PermittedReadEmitsNothing(t *testing.T) {
	got := renderGoOwnerDefault(t, goOwnerDefaultMethod("GetTask", verbs.PatternGet, verbs.AccessRead, "UpdateTaskRequest"))
	if strings.Contains(got, "spec.owner_default") {
		t.Fatalf("GetTask emitted a refusal for an access the owner holds\ngot:\n%s", got)
	}
}

// Without a request (spec.principal) field the wrapper cannot tell whose row is
// being written, so it refuses to generate rather than guess.
func TestGoOwnerDefault_WriteWithoutRequestPrincipalIsAGenerationError(t *testing.T) {
	_, err := goOwnerDefaultGuard(
		goOwnerDefaultMethod("UpdateTask", verbs.PatternUpdate, verbs.AccessWrite, "BareUpdateRequest"),
		goOwnerDefaultSanitizer(), "tasks", "caller", goOwnerDefaultGrpcDeny)
	if err == nil {
		t.Fatal("expected a generation error for a write refusal with no (spec.principal) field to compare against")
	}
	if !strings.Contains(err.Error(), "(spec.principal)") {
		t.Fatalf("error does not name the missing option: %v", err)
	}
}

// A method whose verb did not classify cannot be judged either way.
func TestGoOwnerDefault_UnclassifiedVerbIsAGenerationError(t *testing.T) {
	m := goOwnerDefaultMethod("UpdateTask", verbs.PatternUpdate, verbs.AccessWrite, "UpdateTaskRequest")
	m.Class = nil
	if _, err := goOwnerDefaultGuard(m, goOwnerDefaultSanitizer(), "tasks", "caller", goOwnerDefaultGrpcDeny); err == nil {
		t.Fatal("expected a generation error for an unclassified verb touching an owner_default type")
	}
}
