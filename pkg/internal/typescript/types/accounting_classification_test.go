// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/accountingpolicy"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/tmplutil"
)

// tsAccountingEnumsStub stands in for the generated types/sys/accounting
// module: protobuf-es enums, numbered as in pkg/proto/sys/accounting.proto,
// with their reverse mapping.
const tsAccountingEnumsStub = `function tsEnum(values) {
  const e = {};
  values.forEach((name, i) => { e[name] = i; e[i] = name; });
  return e;
}
export const EventLevel = tsEnum(['UNDEFINED', 'DEBUG', 'INFO', 'WARN', 'ERROR']);
export const RpcOutcome = tsEnum(['UNDEFINED', 'PERMITTED', 'DENIED_AUTHN', 'DENIED_AUTHZ', 'INVALID', 'FAILED']);
export const RpcReason = tsEnum(['UNDEFINED', 'TOKEN_REJECTED', 'NOT_ENTITLED', 'POLICY_DENIED', 'REQUEST_INVALID', 'HANDLER_ERROR', 'RESPONSE_INVALID', 'INTERNAL']);
`

// tsClassificationDriver prints the rendered am's classification of every
// stage and code, then drives one accounted call through runWithMetrics, the
// Emitter and the EventBusSink and prints the stored record.
const tsClassificationDriver = `import {
  Emitter, EventBusSink, addBytes, isAccountingInternal, levelFor, outcomeForCode, reasonFor, runWithMetrics,
  setRecordsReturned, setVerifiedCaller,
} from './chassis/am/AccountingManager.ts';

const stages = JSON.parse(process.argv[2]);
const rows = [];
for (const stage of stages) {
  for (let code = 0; code <= 17; code++) {
    const outcome = outcomeForCode(code);
    rows.push({ stage, code, outcome, reason: reasonFor(stage, outcome), level: levelFor(outcome) });
  }
}

const queue = [];
const sink = new EventBusSink(queue, 'test-1', () => {});
const emitter = new Emitter('test', sink);
setRecordsReturned(99); // outside an accounted call: a no-op
await runWithMetrics(async (metrics) => {
  await Promise.resolve();
  setRecordsReturned(7);
  addBytes(10);
  addBytes(5);
  const denied = Object.assign(new Error('secret request text'), { code: 7 });
  emitter.rpc(undefined, 'REQUEST', denied, 3n, metrics, {}, 'r1');
});
emitter.rpc(undefined, 'HANDLER', null, 1n, undefined, {}, 'r2');

// The accounting_internal marker exempts a call only when its verified
// caller is a service principal.
const internal = {};
for (const subType of ['service', 'workload', 'user', '']) {
  internal[subType] = await runWithMetrics(async () => {
    setVerifiedCaller(subType);
    await Promise.resolve();
    return [isAccountingInternal(true), isAccountingInternal(false)];
  });
}
setVerifiedCaller('service'); // outside an accounted call: a no-op
internal.outside = [isAccountingInternal(true), isAccountingInternal(false)];
const records = queue.map((r) => ({
  requestId: r.requestId, outcome: r.outcome, reason: r.reason, code: r.code, level: r.level,
  recordsReturned: Number(r.recordsReturned), bytes: Number(r.bytes),
  text: JSON.stringify(r, (_k, v) => (typeof v === 'bigint' ? Number(v) : v)),
}));
console.log(JSON.stringify({ rows, records, stats: sink.stats(), internal }));
`

// TestTypeScriptAccountingManagerClassifiesLikeAccountingPolicy renders the
// real AccountingManager.ts.tmpl from accountingpolicy.TemplateContext and
// checks, by running it, that its outcome/reason/level of every stage and
// code is accountingpolicy.Classify's, and that one accounted call stores one
// record with its typed outcome, reason and handler metrics and no error text.
func TestTypeScriptAccountingManagerClassifiesLikeAccountingPolicy(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("no node on PATH; skipping AccountingManager classification check")
	}
	tmplStr, err := tmplutil.LoadTemplate("typescript/chassis/am", "AccountingManager.ts.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate AccountingManager.ts.tmpl: %v", err)
	}
	// The tables come from the template data, never from the template text.
	for _, want := range []string{"range .Acct.CodeOutcomes", "range .Acct.StageReasons", "range .Acct.OutcomeLevels", "range $i, $s := .Acct.Stages", ".Acct.DefaultOutcome", ".Acct.InternalHeader"} {
		if !strings.Contains(tmplStr, want) {
			t.Fatalf("AccountingManager.ts.tmpl must render its classification from accountingpolicy (%q missing)", want)
		}
	}
	for _, banned := range []string{"PERMISSION_DENIED", "'NOT_ENTITLED'", "'TOKEN_REJECTED'", "x-tacplus-accounting-internal", "'rpcError'", "'tokenRejected'", "'diagnostic'", "ErrorContext"} {
		if strings.Contains(tmplStr, banned) {
			t.Fatalf("AccountingManager.ts.tmpl must not hand-write %q", banned)
		}
	}
	tmpl, err := template.New("accounting_manager").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse AccountingManager template: %v", err)
	}
	data := struct {
		HasAccountingRecord        bool
		AccountingRecordImportPath string
		ServiceRPCs                []struct{ FieldName string }
		Acct                       accountingpolicy.Context
	}{
		HasAccountingRecord:        true,
		AccountingRecordImportPath: "../../types/vservices/accountingrecord/index.js",
		Acct:                       accountingpolicy.TemplateContext(),
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, data); err != nil {
		t.Fatalf("execute AccountingManager template: %v", err)
	}
	if !strings.Contains(rendered.String(), "export const ACCOUNTING_INTERNAL_HEADER = '"+accountingpolicy.InternalHeader+"';") {
		t.Fatalf("rendered AccountingManager missing the accounting_internal marker constant:\n%s", rendered.String())
	}

	dir := t.TempDir()
	files := map[string]string{
		"package.json": `{"type": "module"}`,
		"node_modules/@bufbuild/protobuf/package.json": `{"name": "@bufbuild/protobuf", "type": "module", "main": "index.js"}`,
		"node_modules/@bufbuild/protobuf/index.js":     "export function create(_schema, init) { return init; }\n",
		"types/vservices/accountingrecord/index.js":    "export const AccountingRecordSchema = {};\n",
		"types/sys/accounting/index.js":                tsAccountingEnumsStub,
		"chassis/am/AccountingManager.ts":              rendered.String(),
		"driver.ts":                                    tsClassificationDriver,
	}
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	stages, _ := json.Marshal(accountingpolicy.Stages)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, filepath.Join(dir, "driver.ts"), string(stages))
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("driver failed: %v\n%s", err, stderr.String())
	}

	var got struct {
		Rows []struct {
			Stage, Outcome, Reason, Level string
			Code                          int
		}
		Records []struct {
			RequestID       string `json:"requestId"`
			Outcome         int
			Reason          int
			Code            int
			Level           int
			RecordsReturned int `json:"recordsReturned"`
			Bytes           int
			Text            string
		}
		Stats    struct{ Ingested, Dropped, Depth int }
		Internal map[string][2]bool
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("parse driver output %q: %v", out, err)
	}
	if len(got.Rows) != len(accountingpolicy.Stages)*18 {
		t.Fatalf("driver classified %d cases, want %d", len(got.Rows), len(accountingpolicy.Stages)*18)
	}
	for _, r := range got.Rows {
		outcome, reason := accountingpolicy.Classify(r.Stage, r.Code)
		if r.Outcome != outcome || r.Reason != reason || r.Level != accountingpolicy.OutcomeLevel[outcome] {
			t.Errorf("stage %s code %d: TypeScript am classifies %s/%s/%s, accountingpolicy says %s/%s/%s",
				r.Stage, r.Code, r.Outcome, r.Reason, r.Level, outcome, reason, accountingpolicy.OutcomeLevel[outcome])
		}
	}

	if len(got.Records) != 2 {
		t.Fatalf("stored %d records, want 2 (one per call): %+v", len(got.Records), got.Records)
	}
	denied := got.Records[0]
	// RPC_OUTCOME_DENIED_AUTHZ=3, RPC_REASON_POLICY_DENIED=3, EVENT_LEVEL_WARN=3.
	if denied.RequestID != "r1" || denied.Outcome != 3 || denied.Reason != 3 || denied.Code != 7 || denied.Level != 3 {
		t.Errorf("denied call stored %+v, want outcome DENIED_AUTHZ, reason POLICY_DENIED, code 7, level WARN", denied)
	}
	if denied.RecordsReturned != 7 || denied.Bytes != 15 {
		t.Errorf("denied call stored metrics records_returned=%d bytes=%d, want 7 and 15 (set inside runWithMetrics)", denied.RecordsReturned, denied.Bytes)
	}
	if strings.Contains(denied.Text, "secret") {
		t.Errorf("stored record carries the error text: %s", denied.Text)
	}
	ok := got.Records[1]
	// RPC_OUTCOME_PERMITTED=1, RPC_REASON_UNDEFINED=0, EVENT_LEVEL_INFO=2.
	if ok.Outcome != 1 || ok.Reason != 0 || ok.Code != 0 || ok.Level != 2 || ok.RecordsReturned != 0 {
		t.Errorf("successful call stored %+v, want outcome PERMITTED, reason UNDEFINED, code 0, level INFO, no metrics", ok)
	}
	if got.Stats.Ingested != 2 || got.Stats.Dropped != 0 || got.Stats.Depth != 2 {
		t.Errorf("EventBusSink stats %+v, want ingested 2, dropped 0, depth 2", got.Stats)
	}
	for caller, want := range map[string][2]bool{
		"service":  {true, false},
		"workload": {false, false},
		"user":     {false, false},
		"":         {false, false},
		"outside":  {false, false},
	} {
		if got.Internal[caller] != want {
			t.Errorf("isAccountingInternal(marker, no marker) for caller %q = %v, want %v: only a service caller with the marker is exempt", caller, got.Internal[caller], want)
		}
	}
}

// TestTypeScriptServiceWrapperAccountsEveryExitPath pins the accounted wrapper
// shape (tacplus#273): the call runs inside runWithMetrics so the handler's
// metrics reach its record; the stage starts at AUTH and advances to REQUEST,
// HANDLER and RESPONSE around the auth check, the request validation and the
// handler; the auth check runs inside the try whose finally emits, so a denied
// call is recorded; and the wrapper records the verified caller's sub_type,
// so emitAccounting skips a marked call only from a service principal.
func TestTypeScriptServiceWrapperAccountsEveryExitPath(t *testing.T) {
	method := &parser.MethodInfo{
		Name:           "Read",
		InputType:      "ReadRequest",
		OutputType:     "ReadResponse",
		FullInputType:  "contract.ReadRequest",
		FullOutputType: "contract.ReadResponse",
	}
	accounting := map[string]service.AccountingInfo{
		"contract": {EnumName: "Contract", RPCs: map[string]bool{"Read": true}, RPCOrdinals: map[string]int{"Read": 1}},
	}
	render := func(svc *parser.ServiceInfo, m *parser.MethodInfo) string {
		return generateTsGRPCWrapperMethod(svc, m, "contract", "services", "contract", accounting)
	}

	rendered := render(&parser.ServiceInfo{Name: "ContractApi", IsS2S: true}, method)
	order := []string{
		"accounting.runWithMetrics(async (__tacplusAccountingMetrics) => {",
		"let __tacplusAccountingStage: accounting.Stage = 'AUTH';",
		"try {",
		"await this.authorize('/contract.ContractApi/Read', call.metadata, call.request);",
		"__tacplusAccountingStage = 'REQUEST';",
		"types.validateReadRequest(call.request);",
		"__tacplusAccountingStage = 'HANDLER';",
		"const resp = await this.handler.read(call.request);",
		"__tacplusAccountingStage = 'RESPONSE';",
		"this.validateResponse(types.validateReadResponse, resp);",
		"} finally {",
		"this.emitAccounting(create(accountingTypes.AccountingMethodSchema, { contract: 1 }), __tacplusAccountingStage, __tacplusAccountingStart, __tacplusAccountingError, call.metadata, __tacplusAccountingMetrics);",
		"});",
	}
	at := 0
	for _, want := range order {
		i := strings.Index(rendered[at:], want)
		if i < 0 {
			t.Fatalf("accounted S2S wrapper missing %q after offset %d:\n%s", want, at, rendered)
		}
		at += i + len(want)
	}
	if strings.Contains(rendered, "(async () => {") {
		t.Fatalf("accounted wrapper must run its body inside runWithMetrics:\n%s", rendered)
	}

	// The marker is honoured by every accounted wrapper, C2S included: the
	// exemption is decided by the verified caller, not the service kind.
	if got := render(&parser.ServiceInfo{Name: "ContractApi", IsC2S: true}, method); !strings.Contains(got, "__tacplusAccountingMetrics);") {
		t.Fatalf("C2S wrapper must emit through the same emitAccounting:\n%s", got)
	}

	unaccounted := generateTsGRPCWrapperMethod(&parser.ServiceInfo{Name: "ContractApi", IsS2S: true}, method, "contract", "services", "contract", map[string]service.AccountingInfo{})
	for _, notWant := range []string{"__tacplusAccounting", "runWithMetrics"} {
		if strings.Contains(unaccounted, notWant) {
			t.Fatalf("unaccounted wrapper must not render %q:\n%s", notWant, unaccounted)
		}
	}

	tmplStr, err := tmplutil.LoadTemplate("typescript/types", "service_wrapper.ts.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_wrapper.ts.tmpl: %v", err)
	}
	for _, want := range []string{
		"if (accounting.isAccountingInternal(metadata.get(accounting.ACCOUNTING_INTERNAL_HEADER).length > 0)) return;",
		"this.emitter.rpc(method, stage, err, durationMs, metrics, {}, requestId);",
		"const subType = await this.authorizeFunc(fullMethod, this.metadataToRecord(metadata), req);",
		"const subType = await this.authenticateFunc(this.metadataToRecord(metadata), targetNamespace, fullMethod);",
		"accounting.setVerifiedCaller(typeof subType === 'string' ? subType : '');",
	} {
		if !strings.Contains(tmplStr, want) {
			t.Fatalf("service_wrapper.ts.tmpl emitAccounting missing %q", want)
		}
	}
	// The Authorizer resolves every successful check to the verified sub_type.
	authz, err := tmplutil.LoadTemplate("typescript/chassis/grpcserver", "Authorizer.ts.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate Authorizer.ts.tmpl: %v", err)
	}
	if strings.Count(authz, "return subType;") != 2 || !strings.Contains(authz, "return tw.getSubType();") || strings.Count(authz, "): Promise<string> {") < 2 {
		t.Fatalf("Authorizer.authorize/authenticate must resolve to the verified token's sub_type")
	}
}
