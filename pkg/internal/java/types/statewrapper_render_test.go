// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"strings"
	"testing"
	"text/template"
)

func TestStateWrapperTopicBoundaryUsesTopicRPCsNotWatchRPCs(t *testing.T) {
	rendered := renderJavaStateWrapperForTest(t, javaStateWrapperData{
		Header:           "// generated for test",
		Package:          "com.example.tacplus.s2sm.internal.state.vservices.demo.demoeventbus",
		OuterClassName:   "Demo",
		StateRootName:    "DemoEventBus",
		StateServiceName: "DemoEventBusStateService",
		GrpcServiceFQN:   "com.example.tacplus.types.vservices.demo.DemoEventBusStateServiceGrpc",
		TraceFQN:         "com.example.tacplus.types.sys.trace.Trace",
		Topics: []javaTopicBoundary{{
			FieldName:           "eventStream",
			FieldPascal:         "EventStream",
			TypeName:            "SensorEvent",
			JavaType:            "Demo.SensorEvent",
			SubscribeRecordType: "Demo.DemoEventBusEventStreamSubscribeRecord",
		}},
	})

	for _, expected := range []string{
		"stateStub.publishToEventStream(value)",
		"stateStub.subscribeToEventStream(Trace.TraceRequest.getDefaultInstance())",
		"public java.util.Iterator<Demo.DemoEventBusEventStreamSubscribeRecord> subscribeToEventStreamRecords()",
		"final java.util.Iterator<Demo.DemoEventBusEventStreamSubscribeRecord> records = subscribeToEventStreamRecords();",
		"return records.next().getPayload();",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("topic boundary did not render expected RPC call %q:\n%s", expected, rendered)
		}
	}

	for _, unexpected := range []string{
		"watchEventStream",
		"startWatchEventStream",
		"stateAsyncStub",
		"ClientCallStreamObserver",
		"ClientResponseObserver",
	} {
		if strings.Contains(rendered, unexpected) {
			t.Fatalf("topic-only state wrapper rendered watch machinery %q:\n%s", unexpected, rendered)
		}
	}
}

func TestStateWrapperWatchStatusHookRendersForWatchedBoundaries(t *testing.T) {
	rendered := renderJavaStateWrapperForTest(t, javaStateWrapperData{
		Header:           "// generated for test",
		Package:          "com.example.tacplus.s2sm.internal.state.services.demo.config",
		OuterClassName:   "Demo",
		StateRootName:    "DemoConfigState",
		StateServiceName: "DemoConfigStateService",
		GrpcServiceFQN:   "com.example.tacplus.types.services.demo.DemoConfigStateServiceGrpc",
		TraceFQN:         "com.example.tacplus.types.sys.trace.Trace",
		Singletons: []javaSingletonBoundary{{
			FieldName:   "config",
			FieldPascal: "Config",
			TypeName:    "Config",
			JavaType:    "Demo.Config",
		}},
	})

	for _, expected := range []string{
		"public interface WatchStatusListener",
		"public static final class WatchStatus",
		"public void setWatchStatusListener(WatchStatusListener listener)",
		"public Map<String, WatchStatus> watchStatuses()",
		"public boolean watchesHealthy()",
		"recordWatchHealthy(\"WatchConfig\", false)",
		"recordWatchHealthy(\"WatchConfig\", true)",
		"recordWatchRetrying(\"WatchConfig\", received.get(), nextAttempt, t.getMessage(), t)",
		"recordWatchRetrying(\"WatchConfig\", received.get(), nextAttempt, \"stream completed\", null)",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("watched state wrapper did not render expected watch-status hook %q:\n%s", expected, rendered)
		}
	}
}

func renderJavaStateWrapperForTest(t *testing.T, data javaStateWrapperData) string {
	t.Helper()
	tmplStr, err := loadTemplate("statewrapper.java.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	tmpl, err := template.New("statewrapper").Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse state wrapper template: %v", err)
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute state wrapper template: %v", err)
	}
	return out.String()
}
