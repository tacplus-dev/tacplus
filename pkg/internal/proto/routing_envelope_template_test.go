// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"bytes"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/header"
	"strings"
	"testing"
	"text/template"

	"gitlab.com/seabeam/schematic/tacplus/templates"
)

// The owner-declared routing envelope is one boundary with five renderings, and
// the only thing that makes it a boundary at all is the generated Handler's
// ARGUMENT TYPE. A backend that kept spelling the full type would leave its
// subscribers able to read the body while the other four could not — the exact
// per-language drift the cross-language template pins in this tree exist to
// catch. So all five subscriber templates (and the four handler-interface
// templates that restate the same signature) are asserted here, together, from
// the same data.
//
// Rendering is done with a plain map rather than each backend's unexported
// template-data struct: those live in five different packages and cannot be
// reached from one test. A key a given template does not use is simply unread.
var routingTemplateData = map[string]any{
	"RoutingOnly": true,

	"SourcePackage":   "mail",
	"SourceStateRoot": "MailBus",
	"SourceField":     "delivery_stream",
	"FieldPascal":     "DeliveryStream",
	"FieldSnake":      "delivery_stream",
	"TypeName":        "Delivery",
	"GrpcService":     "mail.MailBusStateService",
	"GrpcMethod":      "SubscribeToDeliveryStream",

	// go
	"TypeImport":           "example/types/services/mail",
	"EventBusImport":       "example/types/generated/eventbus_support",
	"ServiceIMImport":      "example/chassis/service_im",
	"EnvelopeTypeName":     "DeliveryRoutingEnvelope",
	"KeyFieldPascal":       "DeliveryId",
	"PrincipalFieldPascal": "Recipient",
	"NamespaceFieldPascal": "Tenant",

	// rust
	"TypeProtoDir":        "services",
	"TypePackage":         "mail",
	"KeyFieldSnake":       "delivery_id",
	"PrincipalFieldSnake": "recipient",
	"NamespaceFieldSnake": "tenant",
	"HandlerMethodName":   "on_delivery_stream",

	// java
	"PackageBase":        "com.example.s2sm.subscribe",
	"TypeOuterClass":     "Mail",
	"ClassName":          "MailDeliveryStreamSubscriber",
	"IdentityManagerFQN": "com.example.chassis.service_im.IdentityManager",

	// python
	"TypeImportFrom":       "example.types.services.mail",
	"TypePbModule":         "mail_pb2",
	"ExceptionsImportFrom": "example.types.sys.errors",
	"EnvelopeName":         "MailBusDeliveryStreamSubscribeRecord",
	"KeyField":             "delivery_id",
	"PrincipalField":       "recipient",
	"NamespaceField":       "tenant",
	"TypeAlias":            "DeliveryStreamEvent",

	// typescript
	"TypeSchemaName":        "DeliverySchema",
	"EnvelopeSchemaName":    "MailBusDeliveryStreamSubscribeRecordSchema",
	"TypeImportPath":        "../../types/services/mail/index.js",
	"RoutingEnvelopeName":   "DeliveryRoutingEnvelope",
	"RoutingEnvelopeSchema": "DeliveryRoutingEnvelopeSchema",
	"KeyFieldCamel":         "deliveryId",
	"PrincipalFieldCamel":   "recipient",
	"NamespaceFieldCamel":   "tenant",
}

type routingTemplateCase struct {
	name string
	path string
	// wantNarrow is the Handler signature the flagged subscriber must get.
	wantNarrow string
	// wantWide is the same signature spelled with the full type. It must appear
	// when the subscription is NOT flagged and must NOT appear when it is.
	wantWide string
}

var routingTemplateCases = []routingTemplateCase{
	{
		name:       "go/subscriber",
		path:       "go/types/subscriber.go.tmpl",
		wantNarrow: "type Handler func(ctx context.Context, event *source.DeliveryRoutingEnvelope, meta *eb.RecordMeta) error",
		wantWide:   "type Handler func(ctx context.Context, event *source.Delivery, meta *eb.RecordMeta) error",
	},
	{
		name:       "rust/subscriber",
		path:       "rust/types/subscriber.rs.tmpl",
		wantNarrow: "dyn Fn(DeliveryRoutingEnvelope, RecordMeta)",
		wantWide:   "dyn Fn(Delivery, RecordMeta)",
	},
	{
		name:       "rust/handler",
		path:       "rust/types/handler.rs.tmpl",
		wantNarrow: "event: DeliveryRoutingEnvelope, meta: RecordMeta",
		wantWide:   "event: Delivery, meta: RecordMeta",
	},
	{
		name:       "java/subscriber",
		path:       "java/types/Subscriber.java.tmpl",
		wantNarrow: "void handle(Mail.DeliveryRoutingEnvelope event, RecordMeta meta)",
		wantWide:   "void handle(Mail.Delivery event, RecordMeta meta)",
	},
	{
		name:       "java/handler",
		path:       "java/types/Handler.java.tmpl",
		wantNarrow: "on_delivery_stream(Mail.DeliveryRoutingEnvelope event, SubscriptionMeta meta)",
		wantWide:   "on_delivery_stream(Mail.Delivery event, SubscriptionMeta meta)",
	},
	{
		name:       "python/subscriber",
		path:       "python/types/subscriber.py.tmpl",
		wantNarrow: "envelope = event_pb.DeliveryRoutingEnvelope(",
		wantWide:   "await handler(\n                    record.payload,",
	},
	{
		name:       "python/handler",
		path:       "python/types/handler.py.tmpl",
		wantNarrow: "import DeliveryRoutingEnvelope as DeliveryStreamEvent",
		wantWide:   "import Delivery as DeliveryStreamEvent",
	},
	{
		name:       "typescript/subscriber",
		path:       "typescript/types/subscriber.ts.tmpl",
		wantNarrow: "export type Handler = (event: DeliveryRoutingEnvelope, meta: RecordMeta) => Promise<void>;",
		wantWide:   "export type Handler = (event: Delivery, meta: RecordMeta) => Promise<void>;",
	},
	{
		name:       "typescript/handler",
		path:       "typescript/types/handler.ts.tmpl",
		wantNarrow: "type DeliveryRoutingEnvelope as DeliveryStreamEvent",
		wantWide:   "type Delivery as DeliveryStreamEvent",
	},
}

func renderRoutingTemplate(t *testing.T, path string, routingOnly bool) string {
	t.Helper()
	body, err := templates.FS.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	tmpl, err := template.New("t").Funcs(header.Funcs()).Parse(string(body))
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	// The handler-interface templates render a whole service, so the one
	// subscription under test is handed to them as the Subscriptions list.
	sub := make(map[string]any, len(routingTemplateData))
	for k, v := range routingTemplateData {
		sub[k] = v
	}
	sub["RoutingOnly"] = routingOnly
	data := map[string]any{
		"HasSubscriptions": true,
		"Subscriptions":    []map[string]any{sub},
	}
	for k, v := range sub {
		data[k] = v
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		t.Fatalf("execute %s: %v", path, err)
	}
	return buf.String()
}

// A subscriber named in routing_only_services gets a Handler typed for the
// narrow envelope — in every backend that generates one.
func TestRoutingOnlyHandlerTakesEnvelopeInEveryLanguage(t *testing.T) {
	for _, tc := range routingTemplateCases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderRoutingTemplate(t, tc.path, true)
			if !strings.Contains(got, tc.wantNarrow) {
				t.Fatalf("%s: routing-only Handler must take the envelope; missing %q", tc.path, tc.wantNarrow)
			}
			if strings.Contains(got, tc.wantWide) {
				t.Fatalf("%s: routing-only Handler must NOT also take the full type; found %q", tc.path, tc.wantWide)
			}
		})
	}
}

// PAIRED CONTROL. routing_only_services is additive: a subscription that does
// not declare it must render exactly what it rendered before the field existed.
func TestUnflaggedHandlerKeepsFullTypeInEveryLanguage(t *testing.T) {
	for _, tc := range routingTemplateCases {
		t.Run(tc.name, func(t *testing.T) {
			got := renderRoutingTemplate(t, tc.path, false)
			if !strings.Contains(got, tc.wantWide) {
				t.Fatalf("%s: an unflagged Handler must keep the full type; missing %q", tc.path, tc.wantWide)
			}
			if strings.Contains(got, tc.wantNarrow) {
				t.Fatalf("%s: an unflagged Handler must not be narrowed; found %q", tc.path, tc.wantNarrow)
			}
		})
	}
}
