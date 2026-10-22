// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

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
// EventBusSink/method_label seam is present, non-dropping, and syntactically
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
		"time.sleep(0.01)",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered accounting_manager missing marker %q:\n%s", want, rendered)
		}
	}

	// Non-dropping: the sink must retry, never give up and discard the record.
	if strings.Contains(rendered, "dropping accounting") {
		t.Fatalf("EventBusSink must never drop accounting records (item 9's ruling):\n%s", rendered)
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
