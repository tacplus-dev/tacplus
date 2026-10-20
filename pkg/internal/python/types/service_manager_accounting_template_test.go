// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/tmplutil"
)

// pyCompileCheck fails the test if src is not syntactically valid Python.
// Skips (rather than fails) when no python interpreter is on PATH, matching
// the existing pattern in runtime_templates_regression_test.go.
func pyCompileCheck(t *testing.T, src, name string) {
	t.Helper()
	var py string
	for _, cand := range []string{"python3", "python"} {
		if p, err := exec.LookPath(cand); err == nil {
			py = p
			break
		}
	}
	if py == "" {
		t.Skip("no python interpreter found; skipping syntax check")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, name)
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	out, err := exec.Command(py, "-m", "py_compile", file).CombinedOutput()
	if err != nil {
		t.Fatalf("rendered %s is not valid Python syntax: %v\n%s", name, err, out)
	}
}

// TestPythonAccountingPublisherWiresEventBusSinkOnEveryWrapper renders
// service_manager.py.tmpl with an accounting publisher resolved and asserts:
//   - the emitter is built from the blocking EventBusSink, not the drop-on-full
//     trace publisher pattern
//   - set_emitter is called on every gRPC wrapper, in the same block as
//     set_trace_publisher
//   - the accounting queue/publish-loop methods are generated
//   - the rendered output is syntactically valid Python
func TestPythonAccountingPublisherWiresEventBusSinkOnEveryWrapper(t *testing.T) {
	tmplStr, err := loadTemplate("service_manager.py.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.py.tmpl: %v", err)
	}
	tmpl, err := template.New("service_manager").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse service_manager template: %v", err)
	}

	data := pySmTemplateData{
		PythonPackageBase:     "svc",
		ServiceLabel:          "demo",
		HasGrpc:               true,
		HasServiceHealthState: true,
		HealthStateProtoDir:   "services/demo",
		HealthStateModule:     "svc.types.services.demo",
		HealthStatePbModule:   "demo",
		GrpcServices: []pyGrpcServiceEntry{
			{
				GetterName:        "demo_grpc_impl",
				ServiceName:       "DemoService",
				PackageName:       "demo",
				WrapperImportPath: "svc.types.demo.service",
				WrapperClassName:  "GrpcServer",
				WrapperVarName:    "wrapper_demo",
				AuthDisabled:      true,
			},
		},
		HasAccountingPublisher:      true,
		AccountingPublisherTopicRef: "accountingrecord.AccountingRecordBus",
		AccountingPublisherTarget:   "eventbus",
		AccountingPublishGrpcPath:   "/accountingrecord.AccountingRecordBusStateService/PublishToStream",
		AccountingEventTypeName:     "AccountingRecord",
		AccountingEventImportFrom:   "svc.types.vservices.accountingrecord",
		AccountingEventPbModule:     "accountingrecord_pb2",
	}

	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute service_manager template: %v", err)
	}
	rendered := out.String()

	for _, want := range []string{
		"from svc.chassis.am import EventBusSink, Emitter",
		"from svc.types.vservices.accountingrecord import accountingrecord_pb2 as accountingrecord_pb",
		"def _create_accounting_emitter(self):",
		"sink = EventBusSink(queue, self.instance_id)",
		"return Emitter(self.instance_id, sink)",
		"accounting_emitter = self._create_accounting_emitter()",
		"wrapper_demo.set_emitter(accounting_emitter)",
		"async def _accounting_publish_resolve_loop(self, queue: asyncio.Queue) -> None:",
		"async def _accounting_publish_loop(self, eb_conn, queue: asyncio.Queue) -> None:",
		"async def _accounting_publish_stream(self, eb_conn, queue: asyncio.Queue) -> None:",
		`await self._resolve_service("eventbus")`,
		`await self._identity.get_token_for("eventbus")`,
		"/accountingrecord.AccountingRecordBusStateService/PublishToStream",
		"request_serializer=accountingrecord_pb.AccountingRecord.SerializeToString",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered python service manager missing accounting wiring marker %q:\n%s", want, rendered)
		}
	}

	// set_emitter must be wired in the same per-wrapper block as
	// set_trace_publisher would be (i.e. right after set_instance_id), so
	// finding it is unconditional across every wrapper regardless of
	// HasTracePublisher.
	ctorIdx := strings.Index(rendered, "wrapper_demo = wrapper_demo_cls(handler.demo_grpc_impl())")
	instanceIdx := strings.Index(rendered, "wrapper_demo.set_instance_id(self.instance_id)")
	emitIdx := strings.Index(rendered, "wrapper_demo.set_emitter(accounting_emitter)")
	registerIdx := strings.Index(rendered, "wrapper_demo.register_services(server)")
	if ctorIdx < 0 || instanceIdx < 0 || emitIdx < 0 || registerIdx < 0 {
		t.Fatalf("could not locate wiring order markers in rendered output:\n%s", rendered)
	}
	if !(ctorIdx < instanceIdx && instanceIdx < emitIdx && emitIdx < registerIdx) {
		t.Fatalf("set_emitter must be wired after set_instance_id and before register_services:\n%s", rendered)
	}

	pyCompileCheck(t, rendered, "service_manager.py")
}

// TestPythonServiceManagerOmitsAccountingWiringWithoutPublisher renders the
// template with HasAccountingPublisher false (client repos, or any build
// without platform/proto/vservices/accounting-record.proto) and asserts no
// accounting seam appears.
func TestPythonServiceManagerOmitsAccountingWiringWithoutPublisher(t *testing.T) {
	tmplStr, err := loadTemplate("service_manager.py.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.py.tmpl: %v", err)
	}
	tmpl, err := template.New("service_manager").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse service_manager template: %v", err)
	}

	data := pySmTemplateData{
		PythonPackageBase:     "svc",
		ServiceLabel:          "demo",
		HasGrpc:               true,
		HasServiceHealthState: true,
		HealthStateProtoDir:   "services/demo",
		HealthStateModule:     "svc.types.services.demo",
		HealthStatePbModule:   "demo",
		GrpcServices: []pyGrpcServiceEntry{
			{
				GetterName:        "demo_grpc_impl",
				ServiceName:       "DemoService",
				PackageName:       "demo",
				WrapperImportPath: "svc.types.demo.service",
				WrapperClassName:  "GrpcServer",
				WrapperVarName:    "wrapper_demo",
				AuthDisabled:      true,
			},
		},
		HasAccountingPublisher: false,
	}

	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute service_manager template: %v", err)
	}
	rendered := out.String()

	for _, notWant := range []string{
		"EventBusSink",
		"_create_accounting_emitter",
		"set_emitter",
		"_accounting_publish",
		"accounting_emitter",
	} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("rendered python service manager must omit accounting wiring when HasAccountingPublisher is false; found %q:\n%s", notWant, rendered)
		}
	}

	pyCompileCheck(t, rendered, "service_manager_no_accounting.py")
}

// TestPythonAccountingManagerEventBusSinkRendersValidPython renders
// accounting_manager.py.tmpl with HasAccountingRecord true and asserts the
// EventBusSink/method_label seam is present, never blocks, and syntactically
// valid Python.
func TestPythonAccountingManagerEventBusSinkRendersValidPython(t *testing.T) {
	tmplStr, err := tmplutil.LoadTemplate("python/chassis/am", "accounting_manager.py.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate accounting_manager.py.tmpl: %v", err)
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
	}{
		HasAccountingRecord:        true,
		AccountingRecordImportFrom: "svc.types.vservices.accountingrecord",
		AccountingRecordPbModule:   "accountingrecord_pb2",
		ServicesImportFrom:         "svc.types.services",
		ServicesPbModule:           "services_pb2",
		ServiceRPCs: []struct{ FieldName, EnumName string }{
			{FieldName: "demo_service", EnumName: "DemoService"},
		},
	}

	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute accounting_manager template: %v", err)
	}
	rendered := out.String()

	for _, want := range []string{
		"from svc.types.vservices.accountingrecord import accountingrecord_pb2 as accountingrecord_pb",
		"from svc.types.services import services_pb2 as services_pb",
		"class EventBusSink:",
		"def method_label(method: Any) -> str:",
		"if method.demo_service:",
		"return _rpc_short_name(services_pb.DemoServiceRPCs.Name(method.demo_service))",
		"except asyncio.QueueFull:",
		"accounting spool full; record dropped: %s dropped_total=%d",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered accounting_manager missing marker %q:\n%s", want, rendered)
		}
	}

	// Never blocks: emit must not wait on a full queue.
	if strings.Contains(rendered, "time.sleep(") {
		t.Fatalf("EventBusSink.emit must never wait on the publisher (issue #34):\n%s", rendered)
	}

	pyCompileCheck(t, rendered, "accounting_manager.py")
}

// TestPythonAccountingManagerOmitsEventBusSinkWithoutRecord renders
// accounting_manager.py.tmpl with HasAccountingRecord false (client repos)
// and asserts LoggerSink still renders correctly with no EventBusSink seam.
func TestPythonAccountingManagerOmitsEventBusSinkWithoutRecord(t *testing.T) {
	tmplStr, err := tmplutil.LoadTemplate("python/chassis/am", "accounting_manager.py.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate accounting_manager.py.tmpl: %v", err)
	}
	tmpl, err := template.New("accounting_manager").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse accounting_manager template: %v", err)
	}

	data := struct{ HasAccountingRecord bool }{HasAccountingRecord: false}

	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute accounting_manager template: %v", err)
	}
	rendered := out.String()

	for _, notWant := range []string{"class EventBusSink:", "def method_label(method: Any) -> str:", "accountingrecord_pb", "services_pb"} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("rendered accounting_manager must omit accounting seam when HasAccountingRecord is false; found %q:\n%s", notWant, rendered)
		}
	}
	if !strings.Contains(rendered, "class LoggerSink:") {
		t.Fatalf("LoggerSink must still render when HasAccountingRecord is false:\n%s", rendered)
	}

	pyCompileCheck(t, rendered, "accounting_manager_no_record.py")
}

// pyEventBusSinkDriver imports the rendered accounting_manager.py from argv[1],
// fills a four-slot publisher queue that nothing drains (an event-bus outage),
// then emits fifty records. It prints what the queue held, in order, and how
// many records the sink counted as dropped.
const pyEventBusSinkDriver = `import asyncio
import json
import sys

sys.path.insert(0, sys.argv[1])
import accounting_manager as am

DEPTH = 4
EMITS = 50

queue = asyncio.Queue(maxsize=DEPTH)
sink = am.EventBusSink(queue, "test-1")
for i in range(EMITS):
    sink.emit({
        "timestamp_ms": 1,
        "source_id": "test",
        "request_id": "r%d" % i,
        "identity": {},
        "level": 2,
        "rpc_completed": {"method": None, "duration_ms": 1},
    })
held = []
while not queue.empty():
    held.append(queue.get_nowait().request_id)
print(json.dumps({"held": held, "dropped": sink._dropped}))
`

// TestPythonEventBusSinkNeverHoldsEmitWhenPublisherBlocked renders the real
// accounting_manager.py.tmpl and drives its EventBusSink with the publisher
// blocked. Each emit must return; once the queue is full the surplus records
// are dropped and counted, and the records that were held replay in emit order.
// Before the spool, emit slept in a loop until the queue drained, so the driver
// never finished and the timeout fails the test.
func TestPythonEventBusSinkNeverHoldsEmitWhenPublisherBlocked(t *testing.T) {
	py, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("no python3 on PATH; skipping EventBusSink behaviour check")
	}
	tmplStr, err := tmplutil.LoadTemplate("python/chassis/am", "accounting_manager.py.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate accounting_manager.py.tmpl: %v", err)
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
	}{
		HasAccountingRecord:        true,
		AccountingRecordImportFrom: "stub",
		AccountingRecordPbModule:   "accountingrecord_pb2",
		ServicesImportFrom:         "stub",
		ServicesPbModule:           "services_pb2",
	}
	var rendered bytes.Buffer
	if err := tmpl.Execute(&rendered, data); err != nil {
		t.Fatalf("execute accounting_manager template: %v", err)
	}

	dir := t.TempDir()
	stubs := map[string]string{
		"grpc.py":              "",
		"stub/__init__.py":     "",
		"stub/services_pb2.py": "",
		"stub/accountingrecord_pb2.py": "class AccountingRecord:\n" +
			"    def __init__(self, **kwargs):\n" +
			"        self.__dict__.update(kwargs)\n",
		"accounting_manager.py": rendered.String(),
		"driver.py":             pyEventBusSinkDriver,
	}
	for name, content := range stubs {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", name, err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, py, "-I", filepath.Join(dir, "driver.py"), dir).Output()
	if ctx.Err() != nil {
		t.Fatalf("EventBusSink.emit did not return while the publisher was blocked: the driver ran past %v", 10*time.Second)
	}
	if err != nil {
		t.Fatalf("driver failed: %v", err)
	}

	var got struct {
		Held    []string `json:"held"`
		Dropped int      `json:"dropped"`
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("parse driver output %q: %v", out, err)
	}
	if want := []string{"r0", "r1", "r2", "r3"}; !reflect.DeepEqual(got.Held, want) {
		t.Fatalf("publisher queue held %v, want %v: the spool must replay the first records in emit order", got.Held, want)
	}
	if got.Dropped != 46 {
		t.Fatalf("dropped %d records, want 46: records past a full spool must be dropped and counted", got.Dropped)
	}
}
