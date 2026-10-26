// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

func labelAnn(labels ...string) model.AnnotationValue {
	return model.AnnotationValue{Type: model.AnnotationTypeString, StringSliceValue: labels}
}

func hostFile() *FileDescriptor {
	return &FileDescriptor{
		Path:     "services/host.proto",
		Package:  "host",
		Services: []ServiceDescriptor{{Name: "HostService", GrpcName: "host"}},
	}
}

func TestValidateRoutingLabelsAcceptsTwoServedLabels(t *testing.T) {
	other := &FileDescriptor{
		Path:     "services/other.proto",
		Package:  "other",
		Services: []ServiceDescriptor{{Name: "OtherService", GrpcName: "other"}},
	}
	record := &FileDescriptor{
		Path:    "services/record.proto",
		Package: "record",
		Messages: []MessageDescriptor{{
			Name:        "Entry",
			Annotations: map[string]model.AnnotationValue{"service_store_label": labelAnn("host", "other")},
		}},
	}
	if err := validateRoutingLabels([]*FileDescriptor{hostFile(), other, record}); err != nil {
		t.Fatalf("two served labels must generate: %v", err)
	}
}

func TestValidateRoutingLabelsRejectsUnservedLabel(t *testing.T) {
	record := &FileDescriptor{
		Path:    "services/record.proto",
		Package: "record",
		Messages: []MessageDescriptor{{
			Name:        "Entry",
			Annotations: map[string]model.AnnotationValue{"service_store_label": labelAnn("host", "nowhere")},
		}},
	}
	err := validateRoutingLabels([]*FileDescriptor{hostFile(), record})
	if err == nil {
		t.Fatal("a label no service serves must fail generation")
	}
	if !strings.Contains(err.Error(), `record.Entry`) || !strings.Contains(err.Error(), `"nowhere"`) {
		t.Fatalf("error must name the message and the unserved label, got: %v", err)
	}
	if strings.Contains(err.Error(), `"host"`) {
		t.Fatalf("the served label must not be reported, got: %v", err)
	}
}

func TestValidateRoutingLabelsRejectsUnservedStateLabel(t *testing.T) {
	state := &FileDescriptor{
		Path:    "services/state.proto",
		Package: "state",
		Messages: []MessageDescriptor{{
			Name:        "Root",
			Annotations: map[string]model.AnnotationValue{"service_state_label": labelAnn("nowhere")},
		}},
	}
	err := validateRoutingLabels([]*FileDescriptor{hostFile(), state})
	if err == nil || !strings.Contains(err.Error(), "spec.service_state") {
		t.Fatalf("an unserved service_state label must fail and name the annotation, got: %v", err)
	}
}

// A backing store declares the label it serves through (spec.service).store, not
// through a gRPC service: a store-only proto set has no service to match against.
func TestValidateRoutingLabelsAcceptsDeclaredStore(t *testing.T) {
	store := &FileDescriptor{
		Path:         "services/store.proto",
		Package:      "coldstore",
		ServiceName:  "coldstore",
		ServiceStore: true,
	}
	state := &FileDescriptor{
		Path:    "services/state.proto",
		Package: "state",
		Messages: []MessageDescriptor{{
			Name:        "Root",
			Annotations: map[string]model.AnnotationValue{"service_state_label": labelAnn("coldstore")},
		}},
	}
	if err := validateRoutingLabels([]*FileDescriptor{store, state}); err != nil {
		t.Fatalf("a declared backing store must serve its label: %v", err)
	}
}

// A service generated alone declares its state root routed to the fleet's state
// host without that host's proto in the run. The well-known host label counts
// as served; any other label still needs a loaded service.
func TestValidateRoutingLabelsServesWellKnownStateHostWithoutItsProto(t *testing.T) {
	state := &FileDescriptor{
		Path:    "services/llmproxy.proto",
		Package: "llmproxy",
		Messages: []MessageDescriptor{{
			Name:        "LlmProxyState",
			Annotations: map[string]model.AnnotationValue{"service_state_label": labelAnn(WellKnownStateHostLabel)},
		}},
	}
	if err := validateRoutingLabels([]*FileDescriptor{state}); err != nil {
		t.Fatalf("the well-known state host label must count as served: %v", err)
	}
	other := &FileDescriptor{
		Path:    "services/other.proto",
		Package: "other",
		Messages: []MessageDescriptor{{
			Name:        "Root",
			Annotations: map[string]model.AnnotationValue{"service_state_label": labelAnn("stratum")},
		}},
	}
	if err := validateRoutingLabels([]*FileDescriptor{other}); err == nil {
		t.Fatal("only the well-known host label is implied; another unserved label must still fail")
	}
}
