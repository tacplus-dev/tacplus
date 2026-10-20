// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"go/format"
	"strings"
	"testing"
	"text/template"
)

// TestGoAccountingPublisherWiresEventBusSinkOnEveryWrapper renders
// service_manager.go.tmpl with an accounting publisher resolved and asserts:
//   - the emitter is built from an EventBusSink that spools, never a drop-on-full one
//   - SetEmitter is called on every gRPC wrapper, in the same block as SetTracePublisher
//   - the accounting queue/publish-loop functions are generated
//   - the rendered output is syntactically valid Go
func TestGoAccountingPublisherWiresEventBusSinkOnEveryWrapper(t *testing.T) {
	tmplStr, err := loadTemplate("service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.go.tmpl: %v", err)
	}
	tmpl, err := template.New("service_manager").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse service_manager template: %v", err)
	}
	data := serviceManagerTemplateData{
		ServiceLabel:             "demo",
		ModuleBasePath:           "example.com/demo/model",
		HasServiceHealthState:    true,
		HealthStateProtoDir:      "services/demo",
		HealthStateGroupAccessor: "State",
		HasGrpc:                  true,
		HasGrpcServers:           true,
		HasTlsGrpc:               true,
		GrpcServices: []GrpcServiceEntry{
			{
				GetterName:      "GetDemoGrpcImpl",
				ServerType:      "DemoServiceServer",
				HandlerType:     "DemoServiceHandler",
				TypeAlias:       "demo_pb",
				WrapperAlias:    "demo_fe",
				ConstructorName: "NewDemoService",
				WrapperVarName:  "wrapperDemo",
				AuthDisabled:    true,
			},
		},
		HasAccountingPublisher: true,
		AccountingPublisher: PublisherTemplateEntry{
			MethodName:        "CreateAccountingRecordBusStreamPublisher",
			LoopName:          "accountingRecordBusStreamPublishLoop",
			StreamName:        "accountingRecordBusStreamPublishStream",
			Label:             "accountingrecord.AccountingRecordBus.stream publisher",
			TargetService:     "eventbus",
			EventTypeName:     "AccountingRecord",
			TypePkgAlias:      "accountingrecord_pb",
			TypePkgImport:     "example.com/demo/model/types/vservices/accountingrecord",
			PublishStreamName: "PublishToStream",
			PublishPath:       "/accountingrecord.AccountingRecordBusStateService/PublishToStream",
		},
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute service_manager template: %v", err)
	}
	rendered := out.String()

	for _, want := range []string{
		"am.NewEventBusSink(accountingCh, accountingSourceID, sm.ctx.Done())",
		"am.NewEmitter(accountingSourceID,",
		"wrapperDemo.SetEmitter(accountingEmitter)",
		"accountingRecordBusStreamPublishLoop(taskCtx,",
		"func accountingRecordBusStreamPublishLoop(ctx context.Context",
		"func accountingRecordBusStreamPublishStream(ctx context.Context",
		"accountingCh := make(chan *accountingrecord_pb.AccountingRecord, accountingQueueDepth)",
		"accountingrecord_pb \"example.com/demo/model/types/vservices/accountingrecord\"",
		"\"example.com/demo/model/chassis/am\"",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered go service manager missing accounting wiring marker %q:\n%s", want, rendered)
		}
	}

	// SetEmitter must be wired in the same block as SetTracePublisher, i.e.
	// after the wrapper constructor loop and before the multiWrapper assembly.
	traceIdx := strings.Index(rendered, "wrapperDemo.SetTracePublisher(tracePublish)")
	emitIdx := strings.Index(rendered, "wrapperDemo.SetEmitter(accountingEmitter)")
	mwIdx := strings.Index(rendered, "mw := &multiWrapper{")
	if traceIdx < 0 || emitIdx < 0 || mwIdx < 0 {
		t.Fatalf("could not locate wiring order markers in rendered output:\n%s", rendered)
	}
	if !(traceIdx < emitIdx && emitIdx < mwIdx) {
		t.Fatalf("SetEmitter must be wired after SetTracePublisher and before the multiWrapper is assembled:\n%s", rendered)
	}

	if _, err := format.Source(out.Bytes()); err != nil {
		t.Fatalf("rendered go service manager is not valid Go syntax: %v\n%s", err, rendered)
	}
}

// TestGoServiceManagerOmitsAccountingWiringWithoutPublisher renders the
// template with HasAccountingPublisher false (client repos, or any build
// without platform/proto/vservices) and asserts no accounting seam appears.
func TestGoServiceManagerOmitsAccountingWiringWithoutPublisher(t *testing.T) {
	tmplStr, err := loadTemplate("service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.go.tmpl: %v", err)
	}
	tmpl, err := template.New("service_manager").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse service_manager template: %v", err)
	}
	data := serviceManagerTemplateData{
		ServiceLabel:             "demo",
		ModuleBasePath:           "example.com/demo/model",
		HasServiceHealthState:    true,
		HealthStateProtoDir:      "services/demo",
		HealthStateGroupAccessor: "State",
		HasGrpc:                  true,
		HasGrpcServers:           true,
		HasTlsGrpc:               true,
		GrpcServices: []GrpcServiceEntry{
			{
				GetterName:      "GetDemoGrpcImpl",
				ServerType:      "DemoServiceServer",
				HandlerType:     "DemoServiceHandler",
				TypeAlias:       "demo_pb",
				WrapperAlias:    "demo_fe",
				ConstructorName: "NewDemoService",
				WrapperVarName:  "wrapperDemo",
				AuthDisabled:    true,
			},
		},
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute service_manager template: %v", err)
	}
	rendered := out.String()
	for _, notWant := range []string{
		"SetEmitter(accountingEmitter)",
		"accountingCh :=",
		"vservices/accountingrecord",
	} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("rendered go service manager must omit accounting wiring when no accounting publisher is resolved, found %q:\n%s", notWant, rendered)
		}
	}
	if _, err := format.Source(out.Bytes()); err != nil {
		t.Fatalf("rendered go service manager is not valid Go syntax: %v\n%s", err, rendered)
	}
}
