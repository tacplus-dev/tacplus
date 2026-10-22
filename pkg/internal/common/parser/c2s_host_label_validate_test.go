// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"
)

// stratumHost is a physical service: a file-level (spec.service) identity and an
// S2S service that is not itself host-labelled.
func stratumHost() *FileDescriptor {
	return &FileDescriptor{
		Path:        "services/stratum.proto",
		Package:     "stratum",
		ServiceName: "stratum",
		Services:    []ServiceDescriptor{{Name: "RecordStore", GrpcName: "stratum", IsS2S: true}},
	}
}

// traitFile is a trait proto: it declares a trait identity and a C2S surface
// hosted by the named physical service.
func traitFile(hostLabel, traitName string) *FileDescriptor {
	return &FileDescriptor{
		Path:         "vservices/query.proto",
		Package:      "taskquery",
		VServiceName: traitName,
		VServiceID:   40,
		Services: []ServiceDescriptor{{
			Name:      "QueryApi",
			GrpcName:  "taskquery",
			IsC2S:     true,
			HostLabel: hostLabel,
		}},
	}
}

// The correct declaration generates: the host label names a service the proto
// set actually contains, and the file carries a trait name to entitle against.
func TestValidateC2SHostLabelsAcceptsServedHost(t *testing.T) {
	if err := validateC2SHostLabels([]*FileDescriptor{stratumHost(), traitFile("stratum", "query")}); err != nil {
		t.Fatalf("a C2S host label served by a loaded service must generate: %v", err)
	}
}

// The red control: a host label no service in the fleet serves must fail
// generation, the same way an unserved (spec.service_store).label does.
func TestValidateC2SHostLabelsRejectsUnservedHost(t *testing.T) {
	err := validateC2SHostLabels([]*FileDescriptor{stratumHost(), traitFile("nowhere", "query")})
	if err == nil {
		t.Fatal("a C2S host label no service serves must fail generation")
	}
	if !strings.Contains(err.Error(), "taskquery.QueryApi") || !strings.Contains(err.Error(), `"nowhere"`) {
		t.Fatalf("error must name the service and the unserved label, got: %v", err)
	}
	if strings.Contains(err.Error(), `"stratum"`) {
		t.Fatalf("the served label must not be reported, got: %v", err)
	}
}

// A host label on a file that declares no trait has no audience to entitle
// callers against: the route's ServiceID would exist but its
// EntitlementService would be empty.
func TestValidateC2SHostLabelsRejectsMissingTraitName(t *testing.T) {
	err := validateC2SHostLabels([]*FileDescriptor{stratumHost(), traitFile("stratum", "")})
	if err == nil {
		t.Fatal("a C2S host label with no (spec.trait).name must fail generation")
	}
	if !strings.Contains(err.Error(), "spec.trait") {
		t.Fatalf("error must name the missing trait declaration, got: %v", err)
	}
}

// The host label must be checked against services that PHYSICALLY exist, not
// against CollectServingLabels — that set counts a declared host label as
// served, so it can never reject one.
func TestC2SHostLabelIsNotSelfServing(t *testing.T) {
	trait := traitFile("nowhere", "query")
	if !CollectServingLabels([]*FileDescriptor{trait})["nowhere"] {
		t.Fatal("precondition: CollectServingLabels is expected to treat a host label as served")
	}
	if physicalServingLabels([]*FileDescriptor{trait})["nowhere"] {
		t.Fatal("physicalServingLabels must not count a host-label declaration as a served service")
	}
}

// An ordinary C2S service (no host label) is untouched by this control.
func TestValidateC2SHostLabelsIgnoresUnlabelledC2S(t *testing.T) {
	plain := &FileDescriptor{
		Path:     "services/gateway.proto",
		Package:  "gateway",
		Services: []ServiceDescriptor{{Name: "UserApi", GrpcName: "gateway", IsC2S: true}},
	}
	if err := validateC2SHostLabels([]*FileDescriptor{plain}); err != nil {
		t.Fatalf("an ordinary C2S service must not be affected: %v", err)
	}
}
