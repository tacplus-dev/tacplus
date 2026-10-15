// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"
)

func TestTracePublisherTemplatesRetryEventBusResolutionAndLogDrops(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		required []string
	}{
		{
			name: "go",
			path: "../../../templates/go/types/service_manager.go.tmpl",
			required: []string{
				"{{.TracePublisherTopicRef}}",
				"{{.TracePublisherTarget}}",
				"trace publisher: failed to resolve broker, retrying",
				"trace publisher queue full, dropping trace event",
				"tracePublishLoop(taskCtx,",
				"pending = event",
				"receive trace publish ack",
				"{{.TracePublishStreamName}}",
				"{{.TracePublishGrpcPath}}",
				"{{if .HasPublishers}}",
				"func (sm *ServiceManager) {{.MethodName}}() func(context.Context, *{{.TypePkgAlias}}.{{.EventTypeName}}) error",
				"EventBus publisher queue full for {{.Label}}",
				"publisher failed to resolve EventBus broker, retrying",
				"receive {{.Label}} ack",
				"{{.PublishPath}}",
			},
		},
		{
			name: "java",
			path: "../../../templates/java/types/ServiceManager.java.tmpl",
			required: []string{
				"{{.TracePublisherTopicRef}}",
				"{{.TracePublisherTarget}}",
				"tracePublishResolveLoop(traceQueue)",
				"awaitPublisherReady(requestRef.get(), streamReady)",
				"trace publisher: publish attempt failed for broker",
				"trace publisher queue full, dropping trace event",
				"{{.TracePublishFullMethod}}",
				"public interface EventPublisher<T>",
				"public EventPublisher<{{.TypeOuterClass}}.{{.TypeName}}> {{.MethodName}}()",
				"publisher {{.Label}} publish attempt failed for broker",
				"publisher {{.Label}} stream completed before ack",
				"{{.PublishFullMethod}}",
			},
		},
		{
			name: "python",
			path: "../../../templates/python/types/service_manager.py.tmpl",
			required: []string{
				"{{.TracePublisherTopicRef}}",
				"trace publisher: failed to resolve eventbus, retrying",
				"trace publisher queue full, dropping trace event",
				"{{.TracePublishGrpcPath}}",
			},
		},
		{
			name: "typescript",
			path: "../../../templates/typescript/types/service_manager.ts.tmpl",
			required: []string{
				"{{.TracePublisherTopicRef}}",
				"trace publisher: failed to resolve eventbus, retrying",
				"trace publisher queue full, dropping trace event",
				"{{.TracePublishGrpcPath}}",
			},
		},
		{
			name: "rust",
			path: "../../../templates/rust/types/service_manager.rs.tmpl",
			required: []string{
				"Services that",
				"event_publish topic",
				"resolve_eventbus_for_publisher(",
				"{{.TargetService}}",
				"publisher failed to resolve EventBus broker, retrying",
				"trace publisher queue full, dropping trace event",
				"TrySendError<{{.EventTypeName}}>",
				"pending.as_ref().expect(\"pending event\")",
				".message().await",
				"stream completed before ack",
				"{{.PublishPath}}",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			text := readTemplateForTest(t, tc.path)
			for _, want := range tc.required {
				if !strings.Contains(text, want) {
					t.Fatalf("%s missing required trace publisher invariant %q", tc.path, want)
				}
			}
		})
	}
}

func TestGoServiceManagerSubscriptionCallbacksArePerTopicWithMeta(t *testing.T) {
	text := readTemplateForTest(t, "../../../templates/go/types/service_manager.go.tmpl")
	// The event type is branched on .RoutingOnly: a subscriber named in the
	// source type's s2s_subscribe.routing_only_services takes the narrow
	// <T>RoutingEnvelope instead of <T> (see spec.proto). The callback seam
	// itself — per-topic, meta-carrying — is unchanged either way, and that is
	// what this pin is about.
	const eventType = "*{{.TypePkgAlias}}.{{if .RoutingOnly}}{{.EnvelopeTypeName}}{{else}}{{.TypeName}}{{end}}"
	required := []string{
		"{{.HandlerFuncFieldName}} func(ctx context.Context, event " + eventType + ", meta *eb_pb.RecordMeta) error",
		"func (sm *ServiceManager) {{.HandlerFuncSetterName}}(fn func(ctx context.Context, event " + eventType + ", meta *eb_pb.RecordMeta) error)",
		"if sm.{{.HandlerFuncFieldName}} != nil {",
		"sm.{{.HandlerFuncFieldName}}(ctx, event, meta)",
	}
	for _, want := range required {
		if !strings.Contains(text, want) {
			t.Fatalf("go service manager template missing per-topic subscription callback invariant %q", want)
		}
	}
	if strings.Contains(text, "sensorEventFunc") || strings.Contains(text, "SetSensorEventFunc") {
		t.Fatalf("go service manager template must not keep the first-subscription-only sensorEventFunc seam")
	}
}

func TestGoServiceManagerRendersDistinctSubscriptionCallbacksWithMeta(t *testing.T) {
	tmplStr, err := loadTemplate("service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.go.tmpl: %v", err)
	}
	tmpl, err := template.New("service_manager").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse service_manager template: %v", err)
	}
	data := serviceManagerTemplateData{
		ServiceLabel:          "demo",
		ModuleBasePath:        "example.com/demo/model",
		HasServiceHealthState: true,
		HealthStateProtoDir:   "services/demo",
		HasSubscriptions:      true,
		Subscriptions: []SubscriptionTemplateEntry{
			{
				SourcePackage:         "sensor",
				SourceField:           "events",
				TypeName:              "SensorEvent",
				HandlerFuncFieldName:  "onEventsFunc",
				HandlerFuncSetterName: "SetOnEventsFunc",
				SubPkgAlias:           "sensor_events_sub",
				TypePkgAlias:          "sensor_pb",
				TypePkgImport:         "example.com/demo/model/types/services/sensor",
				EventBusImport:        "example.com/demo/model/types/sys/eventbus",
				TargetService:         "eventbus",
			},
			{
				SourcePackage:         "alerts",
				SourceField:           "alerts",
				TypeName:              "AlertEvent",
				HandlerFuncFieldName:  "onAlertsFunc",
				HandlerFuncSetterName: "SetOnAlertsFunc",
				SubPkgAlias:           "alerts_sub",
				TypePkgAlias:          "alerts_pb",
				TypePkgImport:         "example.com/demo/model/types/services/alerts",
				EventBusImport:        "example.com/demo/model/types/sys/eventbus",
				TargetService:         "eventbus",
			},
		},
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute service_manager template: %v", err)
	}
	rendered := out.String()
	required := []string{
		"onEventsFunc func(ctx context.Context, event *sensor_pb.SensorEvent, meta *eb_pb.RecordMeta) error",
		"onAlertsFunc func(ctx context.Context, event *alerts_pb.AlertEvent, meta *eb_pb.RecordMeta) error",
		"func (sm *ServiceManager) SetOnEventsFunc(fn func(ctx context.Context, event *sensor_pb.SensorEvent, meta *eb_pb.RecordMeta) error)",
		"func (sm *ServiceManager) SetOnAlertsFunc(fn func(ctx context.Context, event *alerts_pb.AlertEvent, meta *eb_pb.RecordMeta) error)",
		"sm.onEventsFunc(ctx, event, meta)",
		"sm.onAlertsFunc(ctx, event, meta)",
	}
	for _, want := range required {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered go service manager missing subscription callback seam %q", want)
		}
	}
	if strings.Contains(rendered, "sensorEventFunc") || strings.Contains(rendered, "SetSensorEventFunc") {
		t.Fatalf("rendered go service manager must not keep the first-subscription-only sensorEventFunc seam")
	}
}

func TestGoServiceManagerRendersDiskStoreRollSchedulers(t *testing.T) {
	tmplStr, err := loadTemplate("service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.go.tmpl: %v", err)
	}
	tmpl, err := template.New("service_manager").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse service_manager template: %v", err)
	}
	data := serviceManagerTemplateData{
		ServiceLabel:   "demo",
		ModuleBasePath: "example.com/demo/model",
		SmPrefix:       "s2sm",
		DiskStores: []DiskStoreRuntimeEntry{{
			StoreName:     "cred_pair",
			TypeName:      "CredPair",
			LogField:      "credPairLog",
			HandlerGetter: "GetCredPairStoreGrpcImpl",
		}},
		HasDiskStores: true,
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute service_manager template: %v", err)
	}
	if _, err := format.Source(out.Bytes()); err != nil {
		t.Fatalf("rendered go service manager is not valid Go syntax: %v\n%s", err, out.String())
	}
	rendered := out.String()
	for _, want := range []string{
		"type DiskStoreLogs struct",
		"rollInterval time.Duration",
		"OpenCredPairLog(base, diskstore.RootTenant, archiveCount, codec)",
		"func (sm *ServiceManager) startDiskStoreRollSchedulers(h Handler) error",
		"interval := sm.diskStoreLogs.rollInterval",
		"diskstore.RunRollScheduler(ctx, storeLog, interval, nodeID+\"/cred_pair\"",
		"store := h.GetCredPairStoreGrpcImpl()",
		"resp, err := store.List(ctx, nil)",
		"diskstores.SnapshotCredPair(resp.GetItems())",
		// The roll baseline is narrowed through the chassis LocalRows seam.
		"return diskstore.LocalRecords(store, records)",
		"if err := sm.startDiskStoreRollSchedulers(h); err != nil",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered go service manager missing diskstore scheduler marker %q:\n%s", want, rendered)
		}
	}
	closeIdx := strings.Index(rendered, "if sm.diskStoreLogs != nil {\n_ = sm.diskStoreLogs.Close()")
	waitIdx := strings.Index(rendered, "if !sm.tasks.WaitContext(waitCtx)")
	cancelIdx := strings.Index(rendered, "defer sm.tasks.Cancel()")
	if closeIdx < 0 || waitIdx < 0 || cancelIdx < 0 || !(closeIdx < waitIdx && waitIdx < cancelIdx) {
		t.Fatalf("diskstore log close defer must be registered before task wait/cancel defers so it runs after schedulers stop:\n%s", rendered)
	}
}

func readTemplateForTest(t *testing.T, path string) string {
	t.Helper()
	text, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(text)
}

// Java (getInstanceId, getTlsCertChain, getTlsPrivateKey) and Rust
// (instance_id, tls_cert_chain_*, tls_private_key_*) expose the instance id and
// the current auto-TLS material on every ServiceManager, nil/None without
// auto-TLS. Go now does too, auto-TLS or not, so the accessors render — and
// their x509_pb / crypto imports resolve — with no auto-TLS declared.
func TestGoServiceManagerExposesInstanceIDAndTLSMaterial(t *testing.T) {
	tmplStr, err := loadTemplate("service_manager.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.go.tmpl: %v", err)
	}
	tmpl := template.Must(template.New("service_manager").Funcs(header.Funcs()).Parse(tmplStr))
	var out bytes.Buffer
	if err := tmpl.Execute(&out, serviceManagerTemplateData{ServiceLabel: "demo", ModuleBasePath: "example.com/demo/model", SmPrefix: "s2sm"}); err != nil {
		t.Fatalf("execute service_manager template: %v", err)
	}
	if _, err := format.Source(out.Bytes()); err != nil {
		t.Fatalf("rendered go service manager is not valid Go syntax: %v\n%s", err, out.String())
	}
	for _, want := range []string{
		"func (sm *ServiceManager) InstanceID() string {\nreturn sm.identity.InstanceId()",
		"func (sm *ServiceManager) TLSCertChain() *x509_pb.X509CertChain {\nreturn sm.sc.TLSCertChain()",
		"func (sm *ServiceManager) TLSPrivateKey() crypto.PrivateKey {\nreturn sm.sc.TLSPrivateKey()",
		`x509_pb "example.com/demo/model/types/sys/x509"`,
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("rendered go service manager missing %q", want)
		}
	}
}
