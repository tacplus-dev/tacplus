// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"strings"
	"testing"
	"text/template"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/accountingpolicy"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/templates"
)

// renderRustAmForTest renders chassis/am/src/am.rs.tmpl with the template data
// writeLibTemplates gives it.
func renderRustAmForTest(t *testing.T, acct accountingpolicy.Context) string {
	t.Helper()
	src, err := templates.FS.ReadFile("rust/chassis/am/src/am.rs.tmpl")
	if err != nil {
		t.Fatalf("read am.rs.tmpl: %v", err)
	}
	tmpl, err := template.New("am").Funcs(libTemplateFuncs).Parse(string(src))
	if err != nil {
		t.Fatalf("parse am.rs.tmpl: %v", err)
	}
	var out strings.Builder
	data := struct {
		CrateBasePathUnderscore string
		HasAccountingRecord     bool
		ServiceRPCs             []serviceRPCInfo
		Acct                    accountingpolicy.Context
	}{"demo_model", true, []serviceRPCInfo{{FieldName: "tasks", ProstEnumType: "TasksRpCs"}}, acct}
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute am.rs.tmpl: %v", err)
	}
	return out.String()
}

// tacplus#273: the Rust classification is rendered from accountingpolicy, the
// definition Go and Java render too, so a change to the tables reaches every
// language at once.
func TestRustAmClassificationRenderedFromPolicy(t *testing.T) {
	acct := accountingpolicy.TemplateContext()
	rendered := renderRustAmForTest(t, acct)
	pascal := strutil.UpperSnakeCaseToPascalCase

	var wants []string
	for _, st := range acct.Stages {
		wants = append(wants, "    "+pascal(st)+",\n")
	}
	for _, c := range acct.CodeOutcomes {
		wants = append(wants, fmt.Sprintf("%d => RpcOutcome::%s, // %s", c.Code, pascal(c.Outcome), c.Name))
	}
	wants = append(wants, "_ => RpcOutcome::"+pascal(acct.DefaultOutcome)+",")
	for _, r := range acct.StageReasons {
		wants = append(wants, fmt.Sprintf("(Stage::%s, RpcOutcome::%s) => RpcReason::%s,", pascal(r.Stage), pascal(r.Outcome), pascal(r.Reason)))
	}
	for _, l := range acct.OutcomeLevels {
		wants = append(wants, fmt.Sprintf("RpcOutcome::%s => EventLevel::%s,", pascal(l.Outcome), pascal(l.Level)))
	}
	wants = append(wants,
		`pub const ACCOUNTING_INTERNAL_HEADER: &str = "x-tacplus-accounting-internal";`,
		"(_, RpcOutcome::Permitted) => RpcReason::Undefined,",
	)
	for _, want := range wants {
		if !strings.Contains(rendered, want) {
			t.Fatalf("am.rs missing %q:\n%s", want, rendered)
		}
	}

	// The tables come from the data, not the template: a changed table renders
	// a changed arm.
	acct.CodeOutcomes = append([]accountingpolicy.CodeOutcome{}, acct.CodeOutcomes...)
	acct.CodeOutcomes[0].Outcome = accountingpolicy.OutcomeFailed
	if changed := renderRustAmForTest(t, acct); !strings.Contains(changed, "0 => RpcOutcome::Failed, // OK") {
		t.Fatalf("am.rs did not follow the policy tables:\n%s", changed)
	}
}

// The emitter API: one record method, metrics through a task_local the wrapper
// scopes around the handler, nothing that carries an error text or stack.
func TestRustAmRecordCarriesNoData(t *testing.T) {
	rendered := renderRustAmForTest(t, accountingpolicy.TemplateContext())
	for _, want := range []string{
		"pub async fn rpc(",
		"tokio::task_local! {\n    static METRICS: Arc<Metrics>;\n}",
		"pub fn set_records_returned(n: i64)",
		"pub fn add_bytes(n: i64)",
		"let result = METRICS.scope(self.metrics.clone(), handler).await;",
		"if self.internal && service_caller {",
		"pub struct AccountedStream<S>",
		"impl<S> Drop for AccountedStream<S>",
		// flatten carries the typed outcome and metrics.
		"record.outcome = p.outcome;",
		"record.reason = p.reason;",
		"record.records_returned = m.records_returned;",
		"record.bytes = m.bytes;",
		// pipeline health is counters, never records.
		"pub fn stats(&self) -> SinkStats",
		"pub ingested: u64,",
		"pub dropped: u64,",
		"pub depth: u64,",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("am.rs missing %q:\n%s", want, rendered)
		}
	}
	for _, gone := range []string{
		"RpcError", "Event::TokenRejected", "ErrorContext", "Diagnostic", "CommandExecuted",
		"ToolUsed", "SubagentStarted", "FileModified", "capture_error_context",
		"Backtrace", "pub async fn rpc_completed", "pub async fn rpc_error",
	} {
		if strings.Contains(rendered, gone) {
			t.Fatalf("am.rs still carries the removed %q:\n%s", gone, rendered)
		}
	}
	// The error text reaches the debug console only.
	rpc := rendered[strings.Index(rendered, "pub async fn rpc("):]
	rpc = rpc[:strings.Index(rpc, "\n    }\n")]
	if !strings.Contains(rpc, "tracing::debug!(") || strings.Count(rpc, "status.message()") != 1 {
		t.Fatalf("Emitter::rpc must send the error text to debug tracing only:\n%s", rpc)
	}
}

// The accounting publisher's publish call carries the accounting_internal
// marker so the event bus's wrapper does not account the store of accounting;
// no other publisher does.
func TestRustAccountingPublisherSendsInternalMarker(t *testing.T) {
	accounting := publisherEntry{
		FnName:             "create_accountingrecord_accounting_record_bus_stream_publisher",
		LoopFn:             "accountingrecord_accounting_record_bus_stream_publish_loop",
		StreamFn:           "accountingrecord_accounting_record_bus_stream_publish_stream",
		VarPrefix:          "accountingrecord_accounting_record_bus_stream",
		Label:              "accountingrecord.AccountingRecordBus.stream publisher",
		TargetService:      "eventbus",
		EventTypeFQN:       "accountingrecord.AccountingRecord",
		EventTypeName:      "AccountingRecord",
		PublishPath:        "/accountingrecord.AccountingRecordBusStateService/PublishToStream",
		AccountingInternal: true,
	}
	trace := publisherEntry{
		FnName:        "create_trace_publisher_fn",
		LoopFn:        "trace_publish_loop",
		StreamFn:      "trace_publish_stream",
		VarPrefix:     "trace",
		Label:         "trace publisher",
		TargetService: "eventbus",
		EventTypeFQN:  "trace.TraceEvent",
		EventTypeName: "TraceEvent",
		PublishPath:   "/trace.TraceBusStateService/PublishToEvents",
	}
	rendered := renderRustServiceManagerForTest(t, smTemplateData{
		ServiceLabel:          "tasks",
		HasGrpc:               true,
		HasTlsGrpc:            true,
		HasServiceHealthState: true,
		GrpcServices:          []grpcServiceEntry{{TraitName: "Tasks", WrapperVar: "wrapper_tasks"}},
		Publishers:            []publisherEntry{trace, accounting},
		HasPublishers:         true,
		AccountingPublisher:   &accounting,
	})
	const marker = "grpc_req.metadata_mut().insert(\n        am::ACCOUNTING_INTERNAL_HEADER,"
	if n := strings.Count(rendered, marker); n != 1 {
		t.Fatalf("want the accounting_internal marker on exactly one publisher, found %d:\n%s", n, rendered)
	}
	fn := rendered[strings.Index(rendered, "async fn "+accounting.StreamFn+"("):]
	fn = fn[:strings.Index(fn, "\n}\n")]
	if !strings.Contains(fn, marker) {
		t.Fatalf("the accounting publisher's publish call must carry the marker:\n%s", fn)
	}
}
