// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
)

// recordReadFds builds the file set the paired control is validated over: a
// record store, a trait hosting the record query plane on it, a record with the
// given read grant, and a reader with the given (spec.record_read) list.
//
// grant nil = the record declares no s2s_read block at all; a non-nil empty
// slice = it declares one naming nobody, which is the case the validator refuses
// rather than folding back into the trait-wide default.
func recordReadFds(grant []string, grantDeclared bool, reads []string, readerService string) []*FileDescriptor {
	rec := MessageDescriptor{
		Name:        "Row",
		Annotations: map[string]model.AnnotationValue{"record": {Type: model.AnnotationTypeBool, BoolValue: true}},
	}
	if grantDeclared {
		rec.Annotations["record_s2s_read"] = model.AnnotationValue{
			Type:             model.AnnotationTypeBool,
			BoolValue:        true,
			StringSliceValue: grant,
		}
	}
	return []*FileDescriptor{
		{Path: "store.proto", Package: "store", ServiceName: "stratum", RecordStore: true},
		{Path: "query.proto", Package: "query", Trait: &TraitDescriptor{
			Name: "query", Host: "stratum", S2SService: "RecordMap", C2SService: "RecordQuery",
			AuthServices: []string{"stratum"},
		}},
		{Path: "owner.proto", Package: "owner", ServiceName: "owner", Messages: []MessageDescriptor{rec}},
		{Path: "reader.proto", Package: "reader", ServiceName: readerService, RecordReads: reads},
	}
}

// The happy path: grant names the reader, reader declares the read.
func TestRecordReadPairedControlAccepted(t *testing.T) {
	err := validateRecordReadAnnotations(recordReadFds([]string{"reader"}, true, []string{"owner.Row"}, "reader"))
	if err != nil {
		t.Fatalf("a matched grant/use pair must validate, got: %v", err)
	}
}

// USE WITHOUT GRANT is the failure that would otherwise surface in production:
// the reader gets a typed client, calls it, and takes PERMISSION_DENIED from a
// surface the author had every reason to believe was wired up.
func TestRecordReadUseWithoutGrantRejected(t *testing.T) {
	err := validateRecordReadAnnotations(recordReadFds(nil, false, []string{"owner.Row"}, "reader"))
	if err == nil {
		t.Fatal("expected failure when a reader declares record_read on a record granting nobody")
	}
	if !strings.Contains(err.Error(), "s2s_read") || !strings.Contains(err.Error(), "owner.Row") {
		t.Fatalf("error must name the missing grant and the record, got: %v", err)
	}
}

// A grant that names OTHER services but not this reader is the same runtime
// denial, and the message must say which names were granted so the fix is
// obvious without opening the other proto.
func TestRecordReadGrantNamingSomeoneElseRejected(t *testing.T) {
	err := validateRecordReadAnnotations(recordReadFds([]string{"someoneelse"}, true, []string{"owner.Row"}, "reader"))
	if err == nil {
		t.Fatal("expected failure when the grant does not name the reader")
	}
	if !strings.Contains(err.Error(), "someoneelse") || !strings.Contains(err.Error(), `"reader"`) {
		t.Fatalf("error must name both the granted services and the reader, got: %v", err)
	}
}

// A wildcard grant covers any authenticated service, so it covers this reader.
func TestRecordReadWildcardGrantAccepted(t *testing.T) {
	if err := validateRecordReadAnnotations(recordReadFds([]string{"*"}, true, []string{"owner.Row"}, "reader")); err != nil {
		t.Fatalf("a wildcard grant must cover any reader, got: %v", err)
	}
}

// GRANT WITHOUT USE is not a runtime failure, which is exactly what makes it
// worth failing the build over: a standing read grant nobody exercises is
// invisible, survives whatever once used it, and widens the store's reach for as
// long as nobody notices.
func TestRecordReadGrantWithoutUseRejected(t *testing.T) {
	err := validateRecordReadAnnotations(recordReadFds([]string{"reader"}, true, nil, "reader"))
	if err == nil {
		t.Fatal("expected failure for a read grant no service exercises")
	}
	if !strings.Contains(err.Error(), "record_read") || !strings.Contains(err.Error(), "owner.Row") {
		t.Fatalf("error must name the unexercised grant, got: %v", err)
	}
}

// A wildcard grant is exempt from the unused check: ["*"] is a deliberate
// statement that the reader set is open-ended, so there is no specific unused
// name to point at.
func TestRecordReadWildcardGrantExemptFromUnusedCheck(t *testing.T) {
	if err := validateRecordReadAnnotations(recordReadFds([]string{"*"}, true, nil, "reader")); err != nil {
		t.Fatalf("a wildcard grant must not trip the unused-grant check, got: %v", err)
	}
}

// A declared-but-empty grant is refused rather than folded back into the
// trait-wide default — silently substituting the fleet-wide list would turn an
// attempt to NARROW access into the widest grant available.
func TestRecordReadEmptyGrantRejected(t *testing.T) {
	err := validateRecordReadAnnotations(recordReadFds([]string{}, true, nil, "reader"))
	if err == nil {
		t.Fatal("expected failure for an s2s_read granting nobody")
	}
	if !strings.Contains(err.Error(), "callable by nobody") {
		t.Fatalf("error must say the surface would be callable by nobody, got: %v", err)
	}
}

// A record_read naming something that is not a record at all.
func TestRecordReadUnknownRecordRejected(t *testing.T) {
	err := validateRecordReadAnnotations(recordReadFds([]string{"reader"}, true, []string{"owner.NotARecord"}, "reader"))
	if err == nil {
		t.Fatal("expected failure when record_read names a non-record")
	}
	if !strings.Contains(err.Error(), "not a (spec.record)") {
		t.Fatalf("error must say the name is not a record, got: %v", err)
	}
}

// auth_services names SERVICES; a proto with no service identity could never
// match any grant, so a reader without one is refused rather than generating a
// client whose identity nothing can authorize.
func TestRecordReadWithoutServiceIdentityRejected(t *testing.T) {
	err := validateRecordReadAnnotations(recordReadFds([]string{"reader"}, true, []string{"owner.Row"}, ""))
	if err == nil {
		t.Fatal("expected failure when a reader declares no service identity")
	}
	if !strings.Contains(err.Error(), "no service identity") {
		t.Fatalf("error must name the missing service identity, got: %v", err)
	}
}

// The grant is enforced on the record query plane's S2S half. Without a trait
// hosting that plane there is no surface to scope, and an unread annotation is a
// generation failure here the same way min_group_size's is.
func TestRecordReadGrantWithNoQueryPlaneRejected(t *testing.T) {
	fds := recordReadFds([]string{"reader"}, true, []string{"owner.Row"}, "reader")
	fds[1].Trait = nil // drop the trait; no plane exists
	err := validateRecordReadAnnotations(fds)
	if err == nil {
		t.Fatal("expected failure when no trait hosts a record query plane")
	}
	if !strings.Contains(err.Error(), "record query plane") {
		t.Fatalf("error must say no plane would enforce it, got: %v", err)
	}
}

// A fleet declaring neither half validates unchanged — the mechanism is additive
// and every record in the fleet predates it.
func TestRecordReadAbsentEverywhereAccepted(t *testing.T) {
	if err := validateRecordReadAnnotations(recordReadFds(nil, false, nil, "reader")); err != nil {
		t.Fatalf("a fleet declaring neither half must validate, got: %v", err)
	}
}
