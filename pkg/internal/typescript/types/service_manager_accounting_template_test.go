// Copyright (c) 2026 Seabeam LLC
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

// tsSyntaxCheck fails the test if src is not syntactically valid TypeScript.
// There is no existing tsc/esbuild-based precedent in this test suite (TS has
// no py_compile equivalent already wired in), so this uses vendored esbuild
// (found under platform/clients/webapp/node_modules/.bin, or on PATH) as a
// fast syntax-only check: unlike `tsc --noEmit`, esbuild does not attempt to
// resolve the rendered file's imports or types, so it won't false-fail on
// this throwaway temp-dir render's unresolvable relative imports. Skips
// (rather than fails) when no esbuild binary can be found, matching the
// existing "skip rather than fail if tool absent" pattern in
// runtime_templates_regression_test.go.
func tsSyntaxCheck(t *testing.T, src, name string) {
	t.Helper()
	candidates := []string{"esbuild"}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "..", "..", "..", "..", "..", "platform", "clients", "webapp", "node_modules", ".bin", "esbuild"))
	}
	var esbuild string
	for _, cand := range candidates {
		if p, err := exec.LookPath(cand); err == nil {
			esbuild = p
			break
		}
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			esbuild = cand
			break
		}
	}
	if esbuild == "" {
		t.Skip("no esbuild binary found; skipping syntax check")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, name)
	if err := os.WriteFile(file, []byte(src), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	out, err := exec.Command(esbuild, file, "--outfile="+filepath.Join(dir, "out.js")).CombinedOutput()
	if err != nil {
		t.Fatalf("rendered %s is not valid TypeScript syntax: %v\n%s", name, err, out)
	}
}

// TestTypeScriptAccountingPublisherWiresEventBusSinkOnEveryAccountedWrapper
// renders service_manager.ts.tmpl with an accounting publisher resolved and
// asserts:
//   - the emitter is built from the non-dropping EventBusSink, not the
//     drop-on-full trace publisher pattern
//   - setEmitter is called only for wrappers with HasAccounting true (TS's
//     setEmitter is gated per-service like Java, unlike Go/Rust/Python's
//     unconditional setEmitter)
//   - the accounting queue/publish-loop methods are generated
//   - the rendered output is syntactically valid TypeScript
func TestTypeScriptAccountingPublisherWiresEventBusSinkOnEveryAccountedWrapper(t *testing.T) {
	tmplStr, err := loadTemplate("service_manager.ts.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.ts.tmpl: %v", err)
	}
	tmpl, err := template.New("service_manager").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse service_manager template: %v", err)
	}

	data := tsSmTemplateData{
		TsPackageBase:         "demo-tacplus",
		ServiceLabel:          "demo",
		HasGrpc:               true,
		HasServiceHealthState: true,
		HealthStateProtoDir:   "services/demo",
		HealthStatePackage:    "demo",
		GrpcServices: []tsGrpcServiceEntry{
			{
				GetterName:        "demoGrpcImpl",
				ServiceName:       "DemoService",
				PackageName:       "demo",
				WrapperImportPath: "./internal/grpc/servers/services/demo/service.js",
				WrapperClassName:  "GrpcServer",
				WrapperVarName:    "wrapperDemo",
				AuthDisabled:      true,
				HasAccounting:     true,
			},
			{
				GetterName:        "otherGrpcImpl",
				ServiceName:       "OtherService",
				PackageName:       "other",
				WrapperImportPath: "./internal/grpc/servers/services/other/service.js",
				WrapperClassName:  "GrpcServer",
				WrapperVarName:    "wrapperOther",
				AuthDisabled:      true,
				HasAccounting:     false,
			},
		},
		HasAccountingPublisher:      true,
		AccountingPublisherTopicRef: "accountingrecord.AccountingRecordBus",
		AccountingPublisherTarget:   "eventbus",
		AccountingPublishGrpcPath:   "/accountingrecord.AccountingRecordBusStateService/PublishToStream",
		AccountingEventTypeName:     "AccountingRecord",
		AccountingEventImportPath:   "../types/vservices/accountingrecord/index.js",
	}

	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute service_manager template: %v", err)
	}
	rendered := out.String()

	for _, want := range []string{
		"import { EventBusSink, Emitter } from '../chassis/am/index.js';",
		"import { type AccountingRecord, AccountingRecordSchema } from '../types/vservices/accountingrecord/index.js';",
		"private createAccountingEmitter(): Emitter {",
		"const sink = new EventBusSink(queue, this.instanceId, wake);",
		"return new Emitter(this.instanceId, sink);",
		"const accountingEmitter = this.createAccountingEmitter();",
		"wrapperDemo.setEmitter(accountingEmitter);",
		"private async accountingPublishResolveLoop(take: () => Promise<AccountingRecord>, signal: AbortSignal): Promise<void> {",
		"private async accountingPublishLoop(",
		"private async accountingPublishStream(",
		"await this.resolveService('eventbus', signal);",
		"await this.identity.getTokenFor('eventbus');",
		"/accountingrecord.AccountingRecordBusStateService/PublishToStream",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered typescript service manager missing accounting wiring marker %q:\n%s", want, rendered)
		}
	}

	// setEmitter must NOT be wired for wrapperOther (HasAccounting: false).
	if strings.Contains(rendered, "wrapperOther.setEmitter") {
		t.Fatalf("rendered typescript service manager must not wire setEmitter for a wrapper with HasAccounting false:\n%s", rendered)
	}

	// setEmitter must be wired in the same per-wrapper block as
	// setTracePublisher would be (i.e. right after setInstanceId), so
	// finding it is deterministic across every accounted wrapper.
	ctorIdx := strings.Index(rendered, "const wrapperDemo = new wrapperDemoClass(handler.demoGrpcImpl());")
	instanceIdx := strings.Index(rendered, "wrapperDemo.setInstanceId(this.instanceId);")
	emitIdx := strings.Index(rendered, "wrapperDemo.setEmitter(accountingEmitter);")
	registerIdx := strings.Index(rendered, "wrapperDemo.registerServices(server);")
	if ctorIdx < 0 || instanceIdx < 0 || emitIdx < 0 || registerIdx < 0 {
		t.Fatalf("could not locate wiring order markers in rendered output:\n%s", rendered)
	}
	if !(ctorIdx < instanceIdx && instanceIdx < emitIdx && emitIdx < registerIdx) {
		t.Fatalf("setEmitter must be wired after setInstanceId and before registerServices:\n%s", rendered)
	}

	// Non-dropping: the accounting queue must never discard a record.
	if strings.Contains(rendered, "dropping accounting") {
		t.Fatalf("accounting publisher must never drop accounting records (item 9's ruling):\n%s", rendered)
	}

	tsSyntaxCheck(t, rendered, "service_manager.ts")
}

// TestTypeScriptServiceManagerOmitsAccountingWiringWithoutPublisher renders
// the template with HasAccountingPublisher false (client repos, or any build
// without an AccountingRecord topic declared) and asserts no accounting seam
// appears anywhere, even for a wrapper with HasAccounting true (shouldn't
// happen in practice — generateServiceWrappers only sets HasAccounting when
// the topic resolves — but the template itself must not wire it without the
// publisher).
func TestTypeScriptServiceManagerOmitsAccountingWiringWithoutPublisher(t *testing.T) {
	tmplStr, err := loadTemplate("service_manager.ts.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.ts.tmpl: %v", err)
	}
	tmpl, err := template.New("service_manager").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse service_manager template: %v", err)
	}

	data := tsSmTemplateData{
		TsPackageBase:         "demo-tacplus",
		ServiceLabel:          "demo",
		HasGrpc:               true,
		HasServiceHealthState: true,
		HealthStateProtoDir:   "services/demo",
		HealthStatePackage:    "demo",
		GrpcServices: []tsGrpcServiceEntry{
			{
				GetterName:        "demoGrpcImpl",
				ServiceName:       "DemoService",
				PackageName:       "demo",
				WrapperImportPath: "./internal/grpc/servers/services/demo/service.js",
				WrapperClassName:  "GrpcServer",
				WrapperVarName:    "wrapperDemo",
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
		"createAccountingEmitter",
		"setEmitter",
		"accountingPublish",
		"accountingEmitter",
	} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("rendered typescript service manager must omit accounting wiring when HasAccountingPublisher is false; found %q:\n%s", notWant, rendered)
		}
	}

	tsSyntaxCheck(t, rendered, "service_manager_no_accounting.ts")
}

// TestTypeScriptAccountingManagerEventBusSinkRendersValidTypeScript renders
// AccountingManager.ts.tmpl with HasAccountingRecord true and asserts the
// EventBusSink/methodLabel seam is present, non-dropping, and syntactically
// valid TypeScript.
func TestTypeScriptAccountingManagerEventBusSinkRendersValidTypeScript(t *testing.T) {
	tmplStr, err := tmplutil.LoadTemplate("typescript/chassis/am", "AccountingManager.ts.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate AccountingManager.ts.tmpl: %v", err)
	}
	tmpl, err := template.New("accounting_manager").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse AccountingManager template: %v", err)
	}

	type rpcEntry struct{ Ordinal, Label string }
	type serviceRPCEntry struct {
		FieldName string
		RPCs      []rpcEntry
	}

	data := struct {
		HasAccountingRecord        bool
		AccountingRecordImportPath string
		ServiceRPCs                []serviceRPCEntry
	}{
		HasAccountingRecord:        true,
		AccountingRecordImportPath: "../../types/vservices/accountingrecord/index.js",
		ServiceRPCs: []serviceRPCEntry{
			{
				FieldName: "demoService",
				RPCs: []rpcEntry{
					{Ordinal: "1", Label: "DEMO_SERVICE_RPCS_READ"},
				},
			},
		},
	}

	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute AccountingManager template: %v", err)
	}
	rendered := out.String()

	for _, want := range []string{
		"import { type AccountingRecord, AccountingRecordSchema } from '../../types/vservices/accountingrecord/index.js';",
		"export class EventBusSink implements Sink {",
		"export function methodLabel(method: unknown): string {",
		"if (m.demoService) {",
		"case 1: return 'DEMO_SERVICE_RPCS_READ';",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered AccountingManager missing marker %q:\n%s", want, rendered)
		}
	}

	// Non-dropping: the sink must never give up and discard the record.
	if strings.Contains(rendered, "dropping accounting") {
		t.Fatalf("EventBusSink must never drop accounting records (item 9's ruling):\n%s", rendered)
	}

	tsSyntaxCheck(t, rendered, "AccountingManager.ts")
}

// TestTypeScriptAccountingManagerOmitsEventBusSinkWithoutRecord renders
// AccountingManager.ts.tmpl with HasAccountingRecord false (client repos) and
// asserts LoggerSink still renders correctly with no EventBusSink seam.
func TestTypeScriptAccountingManagerOmitsEventBusSinkWithoutRecord(t *testing.T) {
	tmplStr, err := tmplutil.LoadTemplate("typescript/chassis/am", "AccountingManager.ts.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate AccountingManager.ts.tmpl: %v", err)
	}
	tmpl, err := template.New("accounting_manager").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse AccountingManager template: %v", err)
	}

	data := struct{ HasAccountingRecord bool }{HasAccountingRecord: false}

	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute AccountingManager template: %v", err)
	}
	rendered := out.String()

	for _, notWant := range []string{"class EventBusSink", "function methodLabel", "AccountingRecordSchema"} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("rendered AccountingManager must omit accounting seam when HasAccountingRecord is false; found %q:\n%s", notWant, rendered)
		}
	}
	if !strings.Contains(rendered, "export class LoggerSink implements Sink {") {
		t.Fatalf("LoggerSink must still render when HasAccountingRecord is false:\n%s", rendered)
	}

	tsSyntaxCheck(t, rendered, "AccountingManager_no_record.ts")
}
