// Copyright (c) 2026 Second Star Cavalier LLC

package registry

import (
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
)

// The accounting record topic is published by the chassis in every service and
// declared by none. The operator derives a delegate's authorized_services —
// i.e. whether kernel will mint an aud=eventbus token at all — from
// event_publishes[].broker_label, so without this synthesized entry a service
// is denied at the mint and its accounting queue backs up behind a broker it
// can never reach. Deleting this entry is a fleet outage, not a lost row.
func TestAccountingPublishEdgeIsSynthesizedForEveryService(t *testing.T) {
	allFds := accountingProtoFixture()
	echo := []*parser.FileDescriptor{fdFor(allFds, "services/echo.proto")}

	got, err := CollectEventPublishProtos(echo, allFds)
	if err != nil {
		t.Fatalf("CollectEventPublishProtos: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("a service declaring no event_publish must still carry the accounting edge, got %d entries", len(got))
	}
	if got[0].GetTopicRef() != "accountingrecord.AccountingRecordBus.stream" {
		t.Fatalf("topic_ref = %q", got[0].GetTopicRef())
	}
	if got[0].GetBrokerLabel() != "eventbus" {
		t.Fatalf("broker_label = %q — this is the field the delegate's mint edge is derived from", got[0].GetBrokerLabel())
	}
	if got[0].GetActorPackage() != "echo" {
		t.Fatalf("actor_package = %q, want echo", got[0].GetActorPackage())
	}
}

// A leaf that declares no physical service (a sys/* library proto, a vservice)
// must not claim to publish anything.
func TestAccountingPublishEdgeSkipsNonServiceLeaves(t *testing.T) {
	allFds := accountingProtoFixture()
	vservice := []*parser.FileDescriptor{fdFor(allFds, "vservices/accounting-record.proto")}

	got, err := CollectEventPublishProtos(vservice, allFds)
	if err != nil {
		t.Fatalf("CollectEventPublishProtos: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("a leaf with no (spec.service) must publish nothing, got %#v", got)
	}
}

func fdFor(fds []*parser.FileDescriptor, path string) *parser.FileDescriptor {
	for _, fd := range fds {
		if fd.Path == path {
			return fd
		}
	}
	return nil
}

// accountingProtoFixture is the minimum descriptor set the topic registry needs:
// a broker service (eventbus), a plain service (echo), and the synthesized
// AccountingRecordBus topic wrapper carrying AccountingRecord.
func accountingProtoFixture() []*parser.FileDescriptor {
	return []*parser.FileDescriptor{
		{
			Path:        "services/eventbus.proto",
			Package:     "eventbus",
			ServiceName: "eventbus",
			Services:    []parser.ServiceDescriptor{{Name: "Admin", GrpcName: "eventbus"}},
		},
		{
			Path:        "services/echo.proto",
			Package:     "echo",
			ServiceName: "echo",
		},
		{
			Path:    "vservices/accounting-record.proto",
			Package: "accountingrecord",
			Messages: []parser.MessageDescriptor{
				{
					Name: "AccountingRecordBus",
					Annotations: map[string]model.AnnotationValue{
						"service_event_label": {StringValue: "eventbus"},
					},
					Fields: []parser.FieldDescriptor{{
						Name:        "stream",
						Type:        "accountingrecord.AccountingRecord",
						Annotations: map[string]model.AnnotationValue{"topic": {BoolValue: true}},
					}},
				},
				{
					Name: "AccountingRecord",
					Annotations: map[string]model.AnnotationValue{
						"event_surface": {EventPublishEntries: []model.EventBusBindingEntry{{
							MethodS2S: &model.EventBusMethodS2S{AuthServices: []string{"*"}},
						}}},
					},
				},
			},
		},
	}
}
