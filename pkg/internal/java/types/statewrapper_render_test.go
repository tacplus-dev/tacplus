// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"strings"
	"testing"
	"text/template"
)

func TestStateWrapperTopicBoundaryUsesTopicRPCsNotWatchRPCs(t *testing.T) {
	// Topics are backed by bidi-streaming PublishTo*/SubscribeTo* RPCs
	// (proto/state.go), not unary/blocking-server-streaming RPCs, so a
	// topic-only state wrapper needs the async stub — but none of the
	// singleton/collection Watch-loop machinery, which is a separate concern.
	rendered := renderJavaStateWrapperForTest(t, javaStateWrapperData{
		Header:                    "// generated for test",
		Package:                   "com.example.tacplus.s2sm.internal.state.vservices.demo.demoeventbus",
		OuterClassName:            "Demo",
		StateRootName:             "DemoEventBus",
		StateServiceName:          "DemoEventBusStateService",
		GrpcServiceFQN:            "com.example.tacplus.types.vservices.demo.DemoEventBusStateServiceGrpc",
		TraceFQN:                  "com.example.tacplus.types.sys.trace.Trace",
		EventBusSupportOuterClass: "EventbusSupport",
		Topics: []javaTopicBoundary{{
			FieldName:           "eventStream",
			FieldPascal:         "EventStream",
			TypeName:            "SensorEvent",
			JavaType:            "Demo.SensorEvent",
			SubscribeRecordType: "Demo.DemoEventBusEventStreamSubscribeRecord",
		}},
	})

	for _, expected := range []string{
		"stateAsyncStub.publishToEventStream(",
		"EventbusSupport.PublishResponse publishToEventStream(Demo.SensorEvent value)",
		"stateAsyncStub.subscribeToEventStream(",
		"outbound.onNext(EventbusSupport.SubscribeRequest.getDefaultInstance());",
		"public java.util.Iterator<Demo.DemoEventBusEventStreamSubscribeRecord> subscribeToEventStreamRecords()",
		"final java.util.Iterator<Demo.DemoEventBusEventStreamSubscribeRecord> records = subscribeToEventStreamRecords();",
		"return records.next().getPayload();",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("topic boundary did not render expected RPC call %q:\n%s", expected, rendered)
		}
	}

	for _, unexpected := range []string{
		"TopicPublishReceipt",
		"stateStub.publishToEventStream(",
		"stateStub.subscribeToEventStream(",
		"watchEventStream",
		"startWatchEventStream",
		"ClientCallStreamObserver",
		"ClientResponseObserver",
	} {
		if strings.Contains(rendered, unexpected) {
			t.Fatalf("topic-only state wrapper rendered unexpected/dead contract %q:\n%s", unexpected, rendered)
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

// The singleton fetch must default the fetched value before validating it: the
// wire carries no proto defaults, so a field omitted because it declares a
// (spec.default_*) arrives zero-valued and fails its own (spec.assert_*) check
// for the whole config_fetch_timeout_seconds budget. See the 2026-09-23
// scottyagent crash and the cross-language pin in
// pkg/internal/common/state_singleton_fetch_defaults_parity_test.go.
//
// Java's generated defaults helper returns a Builder, so the value is replaced
// rather than mutated in place. Its TypeDefaults section is gated on the
// package having field defaults at all (writeTypeDefaults / hasFieldDefaults),
// so a state root whose type package emits none must fall through to validate
// unchanged rather than call a method that does not exist.
func TestStateWrapperSingletonFetchDefaultsBeforeValidate(t *testing.T) {
	base := javaStateWrapperData{
		Header:           "// generated for test",
		Package:          "com.example.tacplus.s2sm.internal.state.services.demo.config",
		OuterClassName:   "Demo",
		StateRootName:    "DemoConfigState",
		StateServiceName: "DemoConfigStateService",
		GrpcServiceFQN:   "com.example.tacplus.types.services.demo.DemoConfigStateServiceGrpc",
		TraceFQN:         "com.example.tacplus.types.sys.trace.Trace",
	}

	withDefaults := base
	withDefaults.Singletons = []javaSingletonBoundary{{
		FieldName:   "config",
		FieldPascal: "Config",
		TypeName:    "Config",
		JavaType:    "Demo.Config",
		ValidateRef: "com.example.tacplus.types.services.demo.Helpers.validateConfig",
		DefaultsRef: "com.example.tacplus.types.services.demo.Helpers.configDefaults",
	}}
	rendered := renderJavaStateWrapperForTest(t, withDefaults)
	const want = "value = com.example.tacplus.types.services.demo.Helpers.configDefaults(value).build();"
	defaultsAt := strings.Index(rendered, want)
	if defaultsAt < 0 {
		t.Fatalf("rendered Java singleton fetch does not apply configDefaults:\n%s", rendered)
	}
	validateAt := strings.Index(rendered, "com.example.tacplus.types.services.demo.Helpers.validateConfig(value);")
	if validateAt < 0 {
		t.Fatalf("rendered Java singleton fetch no longer validates the fetched value:\n%s", rendered)
	}
	if defaultsAt > validateAt {
		t.Fatalf("rendered Java singleton fetch applies defaults after validating:\n%s", rendered)
	}

	// No TypeDefaults section for that package — emit no call at all.
	withoutDefaults := base
	withoutDefaults.Singletons = []javaSingletonBoundary{{
		FieldName:   "config",
		FieldPascal: "Config",
		TypeName:    "Config",
		JavaType:    "Demo.Config",
		ValidateRef: "com.example.tacplus.types.services.demo.Helpers.validateConfig",
	}}
	bare := renderJavaStateWrapperForTest(t, withoutDefaults)
	if strings.Contains(bare, "Defaults(value).build();") {
		t.Fatalf("Java singleton fetch called a TypeDefaults method for a package that emits none:\n%s", bare)
	}
	if !strings.Contains(bare, "com.example.tacplus.types.services.demo.Helpers.validateConfig(value);") {
		t.Fatalf("Java singleton fetch without defaults no longer validates the fetched value:\n%s", bare)
	}
}

func renderJavaStateWrapperForTest(t *testing.T, data javaStateWrapperData) string {
	t.Helper()
	tmplStr, err := loadTemplate("statewrapper.java.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	tmpl, err := template.New("statewrapper").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse state wrapper template: %v", err)
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute state wrapper template: %v", err)
	}
	return out.String()
}
