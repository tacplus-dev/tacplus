// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"strings"
	"testing"
	"text/template"
)

// renderJavaSm renders ServiceManager.java.tmpl over the given subscriptions,
// exactly as generateJavaServiceManager does (plain Parse, no funcs).
func renderJavaSm(t *testing.T, subs []javaSubscriptionEntry) string {
	t.Helper()
	src, err := loadTemplate("ServiceManager.java.tmpl")
	if err != nil {
		t.Fatalf("load ServiceManager.java.tmpl: %v", err)
	}
	tmpl, err := template.New("servicemanager").Funcs(header.Funcs()).Parse(src)
	if err != nil {
		t.Fatalf("parse ServiceManager.java.tmpl: %v", err)
	}
	hasRecord := false
	for _, s := range subs {
		if s.PerInstanceConsumerGroup {
			hasRecord = true
		}
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, javaSmTemplateData{
		PackageBase:            "com.example",
		ChassisBase:            "com.example.chassis",
		TypesBase:              "com.example.types",
		SmPrefix:               "s2sm",
		ServiceLabel:           "stratum",
		HasSubscriptions:       len(subs) > 0,
		HasRecordSubscriptions: hasRecord,
		Subscriptions:          subs,
	}); err != nil {
		t.Fatalf("execute ServiceManager.java.tmpl: %v", err)
	}
	return buf.String()
}

// The paired control for the consumer-group split. A topic carrying a stored
// record feeds a node-local store, so its subscription must take a per-INSTANCE
// group (the broker then fans the topic out to every instance); a plain event
// topic keeps the shared service-label group it has always had, byte for byte.
func TestRecordSubscriptionTakesPerInstanceConsumerGroup(t *testing.T) {
	record := javaSubscriptionEntry{
		FieldPascal: "Stream", FieldCamel: "stream",
		HandlerMethodName: "onStreamTasksTaskActivityBusStream",
		SourcePackage:     "tasks", SourceField: "stream",
		TypeName: "TaskActivity", TypeImport: "com.example.types.tasks.Tasks",
		TypeOuterClass: "Tasks", ClassName: "TasksTaskActivityBusStreamSubscriber",
		TargetService:            "eventbus",
		PerInstanceConsumerGroup: true,
	}
	event := javaSubscriptionEntry{
		FieldPascal: "Stream", FieldCamel: "stream",
		HandlerMethodName: "onStreamAccountingrecordAccountingRecordBusStream",
		SourcePackage:     "accountingrecord", SourceField: "stream",
		TypeName: "AccountingRecord", TypeImport: "com.example.types.accountingrecord.Accountingrecord",
		TypeOuterClass: "Accountingrecord", ClassName: "AccountingrecordAccountingRecordBusStreamSubscriber",
		TargetService:            "eventbus",
		PerInstanceConsumerGroup: false,
	}

	rendered := renderJavaSm(t, []javaSubscriptionEntry{record, event})

	if !strings.Contains(rendered, `String serviceLabel = perInstanceConsumerGroup("stratum");`) {
		t.Fatal("a record subscription must subscribe under a per-instance consumer group")
	}
	if !strings.Contains(rendered, `String serviceLabel = "stratum";`) {
		t.Fatal("a plain-event subscription must keep the shared service-label consumer group")
	}
	if got := strings.Count(rendered, `String serviceLabel = perInstanceConsumerGroup("stratum");`); got != 1 {
		t.Fatalf("exactly the record subscription takes the per-instance group, got %d", got)
	}
	if got := strings.Count(rendered, `String serviceLabel = "stratum";`); got != 1 {
		t.Fatalf("exactly the plain-event subscription keeps the shared group, got %d", got)
	}
	for _, want := range []string{
		`private static String perInstanceConsumerGroup(String serviceLabel) {`,
		`System.getenv("POD_NAME")`,
		`java.net.InetAddress.getLocalHost().getHostName()`,
		`return serviceLabel + "-" + instance;`,
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("per-instance consumer group helper missing %q", want)
		}
	}
	// The helper must never fall back to the shared label: doing so would
	// silently re-shard a node-local store, which is the whole failure this
	// change exists to prevent.
	helper := rendered[strings.Index(rendered, "private static String perInstanceConsumerGroup"):]
	helper = helper[:strings.Index(helper, "\n    private Thread startManagedThread")]
	if strings.Contains(helper, "return serviceLabel;") {
		t.Fatal("perInstanceConsumerGroup must not fall back to the shared service label")
	}
}

// The other half of the control: a service with no record subscription renders
// no trace of the split at all — neither the call site nor the helper — so every
// existing service generates exactly what it generated before.
func TestEventOnlyServiceRendersNoPerInstanceConsumerGroup(t *testing.T) {
	rendered := renderJavaSm(t, []javaSubscriptionEntry{{
		FieldPascal: "Stream", FieldCamel: "stream",
		HandlerMethodName: "onStreamAccountingrecordAccountingRecordBusStream",
		SourcePackage:     "accountingrecord", SourceField: "stream",
		TypeName: "AccountingRecord", TypeImport: "com.example.types.accountingrecord.Accountingrecord",
		TypeOuterClass: "Accountingrecord", ClassName: "AccountingrecordAccountingRecordBusStreamSubscriber",
		TargetService: "eventbus",
	}})
	if strings.Contains(rendered, "perInstanceConsumerGroup") {
		t.Fatal("a service with no record subscription must not render the per-instance group at all")
	}
	// Byte-for-byte, not just "the string appears": the record branch must leave a
	// plain-event subscription block spelled exactly as it was, since the
	// determinism and current-generation gates compare whole trees.
	want := "        {\n" +
		"            String serviceLabel = \"stratum\";\n" +
		"            // The target is re-resolved from ServiceHealthState on every\n"
	if !strings.Contains(rendered, want) {
		t.Fatalf("non-warm subscription block must be unchanged byte for byte; want:\n%s", want)
	}
}
