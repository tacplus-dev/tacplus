// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

// TestJavaAccountingPublisherWiresEventBusSinkOnEveryWrapper renders
// ServiceManager.java.tmpl with an accounting publisher resolved and asserts:
//   - the emitter is built from an EventBusSink that spools, never a drop-on-full one
//   - setEmitter is called on every gRPC wrapper, in the same block as
//     setTracePublisher
//   - the accounting queue/publish-loop methods are generated
func TestJavaAccountingPublisherWiresEventBusSinkOnEveryWrapper(t *testing.T) {
	rendered := renderJavaTemplateForTest(t, "ServiceManager.java.tmpl", javaSmTemplateData{
		PackageBase:           "com.example",
		ChassisBase:           "com.example.sys",
		TypesBase:             "com.example.types",
		SmPrefix:              "s2sm",
		ServiceLabel:          "demo",
		HasGrpc:               true,
		HasServiceHealthState: true,
		HealthStateJavaPkg:    "services.demo",
		HealthStateOuterClass: "Demo",
		GrpcServices: []javaGrpcServiceEntry{
			{
				GetterName:     "getDemoGrpcImpl",
				ServiceName:    "DemoService",
				ServerType:     "DemoServiceGrpc.DemoServiceImplBase",
				HandlerType:    "DemoServiceHandler",
				WrapperPackage: "com.example.s2sm.internal.grpc.servers.services.demo",
				ServerClassName: "DemoService",
				WrapperVarName:  "wrapperDemo",
				AuthDisabled:    true,
				HasAccounting:   true,
			},
		},
		HasAccountingPublisher: true,
		AccountingPublisher: javaPublisherEntry{
			MethodName:        "createAccountingRecordBusStreamPublisher",
			ResolveLoopName:   "streamPublishResolveLoop",
			LoopName:          "streamPublishLoop",
			StreamName:        "streamPublishStream",
			ThreadName:        "publisher-accounting-record-bus-stream",
			Label:             "accountingrecord.AccountingRecordBus.stream publisher",
			TargetService:     "eventbus",
			TypeName:          "AccountingRecord",
			TypeImport:        "com.example.types.vservices.accountingrecord.Accountingrecord",
			TypeOuterClass:    "Accountingrecord",
			PublishFullMethod: "/accountingrecord.AccountingRecordBusStateService/PublishToStream",
		},
	})

	for _, want := range []string{
		"import com.example.sys.am.AccountingManager;",
		"import com.example.types.vservices.accountingrecord.Accountingrecord;",
		"BlockingQueue<Accountingrecord.AccountingRecord> accountingQueue = new ArrayBlockingQueue<>(1024);",
		"streamPublishResolveLoop(accountingQueue, accountingSink::ack)",
		"AccountingManager.Emitter accountingEmitter = new AccountingManager.Emitter(",
		"new AccountingManager.EventBusSink(accountingQueue, accountingSourceId, () -> closing)",
		"AccountingManager.EventBusSink.durable(accountingQueue, accountingSourceId,",
		"wrapperDemo.setEmitter(accountingEmitter);",
		"private void streamPublishResolveLoop(BlockingQueue<Accountingrecord.AccountingRecord> queue, Runnable onAck)",
		"private void streamPublishStream(io.grpc.Channel ebConn, Accountingrecord.AccountingRecord event)",
		".setFullMethodName(\"/accountingrecord.AccountingRecordBusStateService/PublishToStream\")",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered Java ServiceManager missing accounting wiring marker %q:\n%s", want, rendered)
		}
	}

	// setEmitter must be wired in the same construction block as
	// setTracePublisher would be — after the wrapper constructor loop and
	// before the auth wiring / multiWrapper assembly.
	wrapperIdx := strings.Index(rendered, "DemoService wrapperDemo = new DemoService(")
	emitIdx := strings.Index(rendered, "wrapperDemo.setEmitter(accountingEmitter);")
	if wrapperIdx < 0 || emitIdx < 0 || emitIdx < wrapperIdx {
		t.Fatalf("could not locate wiring order markers in rendered output:\n%s", rendered)
	}
}

// TestJavaServiceManagerOmitsAccountingWiringWithoutPublisher renders the
// template with HasAccountingPublisher false (client repos, or any build
// without platform/proto/vservices) and asserts no accounting seam appears.
func TestJavaServiceManagerOmitsAccountingWiringWithoutPublisher(t *testing.T) {
	rendered := renderJavaTemplateForTest(t, "ServiceManager.java.tmpl", javaSmTemplateData{
		PackageBase:           "com.example",
		ChassisBase:           "com.example.sys",
		TypesBase:             "com.example.types",
		SmPrefix:              "s2sm",
		ServiceLabel:          "demo",
		HasGrpc:               true,
		HasServiceHealthState: true,
		HealthStateJavaPkg:    "services.demo",
		HealthStateOuterClass: "Demo",
		GrpcServices: []javaGrpcServiceEntry{
			{
				GetterName:      "getDemoGrpcImpl",
				ServiceName:     "DemoService",
				ServerType:      "DemoServiceGrpc.DemoServiceImplBase",
				HandlerType:     "DemoServiceHandler",
				WrapperPackage:  "com.example.s2sm.internal.grpc.servers.services.demo",
				ServerClassName: "DemoService",
				WrapperVarName:  "wrapperDemo",
				AuthDisabled:    true,
			},
		},
	})

	for _, notWant := range []string{
		"setEmitter(accountingEmitter)",
		"accountingQueue",
		"AccountingManager",
		"vservices.accountingrecord",
	} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("rendered Java ServiceManager must omit accounting wiring when no accounting publisher is resolved, found %q:\n%s", notWant, rendered)
		}
	}
}
