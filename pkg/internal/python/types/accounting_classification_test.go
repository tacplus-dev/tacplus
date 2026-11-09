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
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// pyGrpcStub stands in for grpcio: the StatusCode values the am reads.
const pyGrpcStub = `import enum


class StatusCode(enum.Enum):
    OK = (0, "ok")
    PERMISSION_DENIED = (7, "permission denied")
    INTERNAL = (13, "internal")
    UNAUTHENTICATED = (16, "unauthenticated")
`

// pyClassificationDriver prints the rendered am's classification of every
// stage and code, then drives one denied and one successful call through the
// metrics scope, the Emitter and the EventBusSink and prints the stored
// records.
const pyClassificationDriver = `import asyncio
import json
import sys

sys.path.insert(0, sys.argv[1])
import accounting_manager as am

stages = json.loads(sys.argv[2])
rows = []
for stage in stages:
    for code in range(18):
        outcome = am.outcome_for_code(code)
        rows.append({"stage": stage, "code": code, "outcome": outcome,
                     "reason": am.reason_for(stage, outcome), "level": am.level_for(outcome)})

queue = asyncio.Queue(maxsize=16)
sink = am.EventBusSink(queue, "test-1")
emitter = am.Emitter("test", sink)
am.set_records_returned(99)  # outside an accounted call: a no-op
metrics, token = am.begin_metrics()
am.set_records_returned(7)
am.add_bytes(10)
am.add_bytes(5)
emitter.rpc(None, am.STAGE_AUTH, PermissionError("secret request text"), 3, metrics, {}, "r1")
am.end_metrics(token)
emitter.rpc(None, am.STAGE_HANDLER, None, 1, None, {}, "r2")
stats = sink.stats()

# The accounting_internal marker exempts a call only when its verified caller
# is a service principal.
internal = {}
for sub_type in ("service", "workload", "user", ""):
    _, scope_token = am.begin_metrics()
    am.set_verified_caller(sub_type)
    internal[sub_type] = [am.is_accounting_internal(True), am.is_accounting_internal(False)]
    am.end_metrics(scope_token)
am.set_verified_caller("service")  # outside an accounted call: a no-op
internal["outside"] = [am.is_accounting_internal(True), am.is_accounting_internal(False)]
records = []
while not queue.empty():
    r = queue.get_nowait()
    records.append({"request_id": r.request_id, "outcome": r.outcome, "reason": r.reason,
                    "code": r.code, "level": r.level, "records_returned": r.records_returned,
                    "bytes": r.bytes, "text": repr(r.__dict__)})
print(json.dumps({"rows": rows, "records": records, "internal": internal,
                  "stats": {"ingested": stats.ingested, "dropped": stats.dropped, "depth": stats.depth}}))
`

// TestPythonAccountingManagerClassifiesLikeAccountingPolicy renders the real
// accounting_manager.py.tmpl from accountingpolicy.TemplateContext and checks,
// by running it, that its outcome/reason/level of every stage and code is
// accountingpolicy.Classify's, and that one accounted call stores one record
// with its typed outcome, reason and handler metrics and no error text.
func TestPythonAccountingManagerClassifiesLikeAccountingPolicy(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3 on PATH; skipping AccountingManager classification check")
	}
	tmplStr, err := tmplutil.LoadTemplate("python/chassis/am", "accounting_manager.py.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate accounting_manager.py.tmpl: %v", err)
	}
	// The tables come from the template data, never from the template text.
	for _, want := range []string{"range .Acct.CodeOutcomes", "range .Acct.StageReasons", "range .Acct.OutcomeLevels", "range .Acct.Stages", ".Acct.DefaultOutcome", ".Acct.InternalHeader"} {
		if !strings.Contains(tmplStr, want) {
			t.Fatalf("accounting_manager.py.tmpl must render its classification from accountingpolicy (%q missing)", want)
		}
	}
	for _, banned := range []string{"PERMISSION_DENIED", `"NOT_ENTITLED"`, `"TOKEN_REJECTED"`, "x-tacplus-accounting-internal", "rpc_error", "token_rejected", "diagnostic", "error_context"} {
		if strings.Contains(tmplStr, banned) {
			t.Fatalf("accounting_manager.py.tmpl must not hand-write %q", banned)
		}
	}
	tmpl, err := template.New("accounting_manager").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse accounting_manager template: %v", err)
	}
	data := struct {
		HasAccountingRecord        bool
		AccountingRecordImportFrom string
		AccountingRecordPbModule   string
		ServicesImportFrom         string
		ServicesPbModule           string
		ServiceRPCs                []struct{ FieldName, EnumName string }
		Acct                       accountingpolicy.Context
	}{
		HasAccountingRecord:        true,
		AccountingRecordImportFrom: "stub",
		AccountingRecordPbModule:   "accountingrecord_pb2",
		ServicesImportFrom:         "stub",
		ServicesPbModule:           "services_pb2",
		Acct:                       accountingpolicy.TemplateContext(),
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, data); err != nil {
		t.Fatalf("execute accounting_manager template: %v", err)
	}
	if !strings.Contains(rendered.String(), `ACCOUNTING_INTERNAL_HEADER = "`+accountingpolicy.InternalHeader+`"`) {
		t.Fatalf("rendered accounting_manager missing the accounting_internal marker constant:\n%s", rendered.String())
	}

	dir := t.TempDir()
	files := map[string]string{
		"grpc.py":              pyGrpcStub,
		"stub/__init__.py":     "",
		"stub/services_pb2.py": "",
		"stub/accountingrecord_pb2.py": "class AccountingRecord:\n" +
			"    def __init__(self, **kwargs):\n" +
			"        self.__dict__.update(kwargs)\n",
		"accounting_manager.py": rendered.String(),
		"driver.py":             pyClassificationDriver,
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
	cmd := exec.CommandContext(ctx, py, "-I", filepath.Join(dir, "driver.py"), dir, string(stages))
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
			RequestID       string `json:"request_id"`
			Outcome         string
			Reason          string
			Code            int
			Level           int
			RecordsReturned int `json:"records_returned"`
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
			t.Errorf("stage %s code %d: Python am classifies %s/%s/%s, accountingpolicy says %s/%s/%s",
				r.Stage, r.Code, r.Outcome, r.Reason, r.Level, outcome, reason, accountingpolicy.OutcomeLevel[outcome])
		}
	}

	if len(got.Records) != 2 {
		t.Fatalf("stored %d records, want 2 (one per call): %+v", len(got.Records), got.Records)
	}
	denied := got.Records[0]
	// A PermissionError is sent as UNAUTHENTICATED (16); EVENT_LEVEL_WARN=3.
	if denied.RequestID != "r1" || denied.Outcome != "RPC_OUTCOME_DENIED_AUTHN" || denied.Reason != "RPC_REASON_TOKEN_REJECTED" || denied.Code != 16 || denied.Level != 3 {
		t.Errorf("denied call stored %+v, want outcome DENIED_AUTHN, reason TOKEN_REJECTED, code 16, level WARN", denied)
	}
	if denied.RecordsReturned != 7 || denied.Bytes != 15 {
		t.Errorf("denied call stored metrics records_returned=%d bytes=%d, want 7 and 15 (set inside the metrics scope)", denied.RecordsReturned, denied.Bytes)
	}
	if strings.Contains(denied.Text, "secret") {
		t.Errorf("stored record carries the error text: %s", denied.Text)
	}
	ok := got.Records[1]
	if ok.Outcome != "RPC_OUTCOME_PERMITTED" || ok.Reason != "RPC_REASON_UNDEFINED" || ok.Code != 0 || ok.Level != 2 || ok.RecordsReturned != 0 {
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
			t.Errorf("is_accounting_internal(marker, no marker) for caller %q = %v, want %v: only a service caller with the marker is exempt", caller, got.Internal[caller], want)
		}
	}
}

// TestPythonServiceWrapperAccountsEveryExitPath pins the accounted wrapper
// shape (tacplus#273): the metrics scope opens before the auth check; a call
// the auth check or the request validation stops emits its record with that
// stage once the abort has set its code; the try advances HANDLER -> RESPONSE
// and its finally emits on every other exit; and the wrapper records the
// verified caller's sub_type, so _emit_accounting skips a marked call only
// from a service principal, on any accounted wrapper.
func TestPythonServiceWrapperAccountsEveryExitPath(t *testing.T) {
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
	render := func(svc *parser.ServiceInfo) string {
		return generatePyGRPCWrapperMethod(svc, method, "contract", "services", "contract", accounting)
	}
	const methodExpr = "accounting_method_pb2.AccountingMethod(contract=1)"
	const scope = "__tacplus_accounting_metrics, __tacplus_accounting_token"

	rendered := render(&parser.ServiceInfo{Name: "ContractApi", IsS2S: true})
	order := []string{
		scope + " = begin_metrics()",
		"self._authorize(context, '/contract.ContractApi/Read', request)",
		"self._abort_handler_error(context, e)",
		"finally:",
		"self._emit_accounting(" + methodExpr + ", STAGE_AUTH, __tacplus_accounting_start, e, context, " + scope + ")",
		"validate_read_request(request)",
		"self._abort_request_validation(context, e)",
		"finally:",
		"self._emit_accounting(" + methodExpr + ", STAGE_REQUEST, __tacplus_accounting_start, e, context, " + scope + ")",
		"__tacplus_accounting_stage = STAGE_HANDLER",
		"try:",
		"resp = self.handler.read(request)",
		"__tacplus_accounting_stage = STAGE_RESPONSE",
		"validate_read_response(resp)",
		"__tacplus_accounting_error = e",
		"finally:",
		"self._emit_accounting(" + methodExpr + ", __tacplus_accounting_stage, __tacplus_accounting_start, __tacplus_accounting_error, context, " + scope + ")",
	}
	at := 0
	for _, want := range order {
		i := strings.Index(rendered[at:], want)
		if i < 0 {
			t.Fatalf("accounted S2S wrapper missing %q after offset %d:\n%s", want, at, rendered)
		}
		at += i + len(want)
	}

	// The marker is honoured by every accounted wrapper, C2S included: the
	// exemption is decided by the verified caller, not the service kind.
	if got := render(&parser.ServiceInfo{Name: "ContractApi", IsC2S: true}); !strings.Contains(got, scope+")") {
		t.Fatalf("C2S wrapper must emit through the same _emit_accounting:\n%s", got)
	}

	unaccounted := generatePyGRPCWrapperMethod(&parser.ServiceInfo{Name: "ContractApi", IsS2S: true}, method, "contract", "services", "contract", map[string]service.AccountingInfo{})
	for _, notWant := range []string{"__tacplus_accounting", "begin_metrics", "_emit_accounting"} {
		if strings.Contains(unaccounted, notWant) {
			t.Fatalf("unaccounted wrapper must not render %q:\n%s", notWant, unaccounted)
		}
	}

	tmplStr, err := tmplutil.LoadTemplate("python/types", "servicewrapper.py.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate servicewrapper.py.tmpl: %v", err)
	}
	for _, want := range []string{
		"internal = is_accounting_internal(ACCOUNTING_INTERNAL_HEADER in self._metadata_from_context(context))\n        end_metrics(token)",
		"if self._emitter is None or internal:",
		"sub_type = self._authorize_func(self._metadata_from_context(context), full_method, req)",
		"set_verified_caller(sub_type)",
		"self._emitter.rpc(method, stage, exc, duration_ms, metrics, {}, request_id, code=code)",
	} {
		if !strings.Contains(tmplStr, want) {
			t.Fatalf("servicewrapper.py.tmpl _emit_accounting missing %q", want)
		}
	}
	// The authorizer returns the verified sub_type from every successful check.
	authz, err := tmplutil.LoadTemplate("python/chassis/grpcserver", "authorizer.py.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate authorizer.py.tmpl: %v", err)
	}
	if strings.Count(authz, "return sub_type\n") != 2 || !strings.Contains(authz, "return tw.get_sub_type()") {
		t.Fatalf("Authorizer.authorize/authenticate must return the verified token's sub_type")
	}
}

// TestPythonAccountedServiceWrapperCompiles generates a whole S2S wrapper
// whose every method kind is accounted and checks it is valid Python: the
// guards' nested try/finally and the stage lines indent correctly everywhere.
func TestPythonAccountedServiceWrapperCompiles(t *testing.T) {
	outputDir := t.TempDir()
	svc := ServiceInfo{
		ProtoFile:    "contract/contract.proto",
		PackageName:  "contract",
		ServiceName:  "ContractApi",
		GrpcName:     "contract",
		IsGrpc:       true,
		IsS2S:        true,
		ParentDir:    "contract",
		AuthServices: []string{"identity"},
		Config:       &tactype.Config{OutputDir: outputDir},
	}
	plain := func(name string, client, server, emptyOut bool) *parser.MethodInfo {
		out, fullOut := "ReadResponse", "contract.ReadResponse"
		if emptyOut {
			out, fullOut = "Empty", "google.protobuf.Empty"
		}
		return &parser.MethodInfo{
			Name: name, InputType: "ReadRequest", OutputType: out,
			FullInputType: "contract.ReadRequest", FullOutputType: fullOut,
			ClientStreaming: client, ServerStreaming: server,
		}
	}
	methods := []*parser.MethodInfo{
		plain("Read", false, false, false),
		plain("Touch", false, false, true),
		plain("Stream", false, true, false),
		plain("Upload", true, false, false),
		plain("UploadEmpty", true, false, true),
		plain("Chat", true, true, false),
		pyWatchMethod(),
		pyPageMethod(),
	}
	rpcs := map[string]bool{}
	ordinals := map[string]int{}
	for i, m := range methods {
		rpcs[m.Name] = true
		ordinals[m.Name] = i + 1
	}
	info := &parser.ProtoInfo{
		Package:  "contract",
		Services: []*parser.ServiceInfo{{Name: "ContractApi", IsS2S: true, Methods: methods}},
	}
	accounting := map[string]service.AccountingInfo{"contract": {EnumName: "Contract", RPCs: rpcs, RPCOrdinals: ordinals}}
	if err := generateServiceWrapper(svc, info, accounting); err != nil {
		t.Fatalf("generateServiceWrapper: %v", err)
	}
	var rendered string
	_ = filepath.Walk(outputDir, func(path string, fi os.FileInfo, err error) error {
		if err == nil && !fi.IsDir() && strings.HasSuffix(path, ".py") {
			b, _ := os.ReadFile(path)
			rendered += string(b)
		}
		return nil
	})
	if got := strings.Count(rendered, "= begin_metrics()"); got != len(methods) {
		t.Fatalf("%d methods open a metrics scope, want %d:\n%s", got, len(methods), rendered)
	}
	pyCompileCheck(t, rendered, "accounted_wrapper.py")
}
