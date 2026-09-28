// Copyright (c) 2026 Second Star Cavalier LLC

package parser

import (
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
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
		{Path: "reader.proto", Package: "reader", ServiceName: readerService, RecordReads: recordReadDecls(reads)},
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

// recordReadDecls lifts a list of record names into (spec.record_read)
// declarations with no c2s block — the s2s-only shape every test above uses.
func recordReadDecls(names []string) []RecordRead {
	out := make([]RecordRead, 0, len(names))
	for _, n := range names {
		out = append(out, RecordRead{Record: n})
	}
	return out
}

// ---------------------------------------------------------------------------
// The c2s pair: (spec.record).c2s_read + (spec.record_read).c2s.
//
// Each test below triggers exactly one refusal, because each refusal exists for
// a different failure and the error text is what tells an author which one they
// hit.
// ---------------------------------------------------------------------------

// c2sField describes one field on the fixture record.
type c2sField struct {
	name      string
	principal bool
	namespace bool
	queryEq   bool
}

// recordReadC2SFds builds the same fleet as recordReadFds with a record shaped
// for the client-facing plane: an s2s_read grant naming the reader, the given
// c2s_read grant, and the given field set.
func recordReadC2SFds(c2sGrant []string, c2sDeclared bool, fields []c2sField, use *RecordReadC2S) []*FileDescriptor {
	rec := MessageDescriptor{
		Name: "Row",
		Annotations: map[string]model.AnnotationValue{
			"record": {Type: model.AnnotationTypeBool, BoolValue: true},
			"record_s2s_read": {
				Type: model.AnnotationTypeBool, BoolValue: true,
				StringSliceValue: []string{"reader"},
			},
		},
	}
	if c2sDeclared {
		rec.Annotations["record_c2s_read"] = model.AnnotationValue{
			Type: model.AnnotationTypeBool, BoolValue: true, StringSliceValue: c2sGrant,
		}
	}
	for _, f := range fields {
		fd := FieldDescriptor{Name: f.name, Type: "string", Annotations: map[string]model.AnnotationValue{}}
		if f.principal {
			fd.Annotations["store_principal"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
		}
		if f.namespace {
			fd.Annotations["store_namespace"] = model.AnnotationValue{Type: model.AnnotationTypeBool, BoolValue: true}
		}
		if f.queryEq {
			fd.Annotations["query_field"] = model.AnnotationValue{Type: model.AnnotationTypeString, StringValue: "eq"}
		}
		rec.Fields = append(rec.Fields, fd)
	}
	reads := []RecordRead{{Record: "owner.Row", C2S: use}}
	return []*FileDescriptor{
		{Path: "store.proto", Package: "store", ServiceName: "stratum", RecordStore: true},
		{Path: "query.proto", Package: "query", Trait: &TraitDescriptor{
			Name: "query", Host: "stratum", S2SService: "RecordMap", C2SService: "RecordQuery",
			AuthServices: []string{"stratum"},
		}},
		{Path: "owner.proto", Package: "owner", ServiceName: "owner", Messages: []MessageDescriptor{rec}},
		{Path: "reader.proto", Package: "reader", ServiceName: "reader", RecordReads: reads},
	}
}

// wellFormedC2SFields is the record shape the plane needs: a principal field and
// a namespace field, both EQ-queryable so both pins have a criterion.
func wellFormedC2SFields() []c2sField {
	return []c2sField{
		{name: "acted_by", principal: true, queryEq: true},
		{name: "namespace", namespace: true, queryEq: true},
	}
}

func principalScope() *RecordReadC2S {
	return &RecordReadC2S{Scope: "C2S_SCOPE_PRINCIPAL", Exposure: "RPC_EXPOSURE_EXTERNAL"}
}

// The happy path, so every refusal below is attributable to the one thing it
// changes rather than to the fixture being wrong.
func TestRecordReadC2SPairedControlAccepted(t *testing.T) {
	err := validateRecordReadAnnotations(
		recordReadC2SFds([]string{"reader"}, true, wellFormedC2SFields(), principalScope()))
	if err != nil {
		t.Fatalf("a matched c2s grant/use pair must validate, got: %v", err)
	}
}

// ["*"] on c2s_read is refused where the same wildcard on s2s_read is accepted:
// "any service may show my rows to any of its users" is not a statement a record
// owner makes.
func TestRecordReadC2SWildcardGrantRejected(t *testing.T) {
	err := validateRecordReadAnnotations(
		recordReadC2SFds([]string{"*"}, true, wellFormedC2SFields(), principalScope()))
	if err == nil {
		t.Fatal("expected failure for c2s_read: [\"*\"]")
	}
	if !strings.Contains(err.Error(), `c2s_read may not use ["*"]`) {
		t.Fatalf("error must name the wildcard refusal, got: %v", err)
	}
}

// USE WITHOUT THE DISCLOSURE GRANT. The reader holds s2s_read and asks for a
// client-facing surface anyway; the record never agreed to the disclosure.
func TestRecordReadC2SUseWithoutGrantRejected(t *testing.T) {
	err := validateRecordReadAnnotations(
		recordReadC2SFds(nil, false, wellFormedC2SFields(), principalScope()))
	if err == nil {
		t.Fatal("expected failure when a reader asks for a c2s read the record grants nobody")
	}
	if !strings.Contains(err.Error(), "declares no (spec.record).c2s_read") {
		t.Fatalf("error must name the missing disclosure grant, got: %v", err)
	}
}

// GRANT WITHOUT USE, on the disclosure half. A standing permission to re-expose
// a record to end users that nothing exercises is the worse of the two unused
// grants, so it has no wildcard exemption to fall through.
func TestRecordReadC2SGrantWithoutUseRejected(t *testing.T) {
	fds := recordReadC2SFds([]string{"reader"}, true, wellFormedC2SFields(), principalScope())
	// Drop the c2s block from the use half, keeping the s2s read so only the
	// disclosure grant goes unexercised.
	for _, fd := range fds {
		if fd.Package == "reader" {
			fd.RecordReads = []RecordRead{{Record: "owner.Row"}}
		}
	}
	err := validateRecordReadAnnotations(fds)
	if err == nil {
		t.Fatal("expected failure for a c2s_read grant no reader exercises")
	}
	if !strings.Contains(err.Error(), "unexercised disclosure grant") {
		t.Fatalf("error must name the unexercised grant, got: %v", err)
	}
}

// c2s_read WITHOUT s2s_read for the same service. The c2s handler's data path IS
// the s2s reader, so the pair with the second half missing is a surface that
// cannot execute.
func TestRecordReadC2SWithoutS2SReadRejected(t *testing.T) {
	fds := recordReadC2SFds([]string{"reader"}, true, wellFormedC2SFields(), principalScope())
	for _, fd := range fds {
		for mi := range fd.Messages {
			delete(fd.Messages[mi].Annotations, "record_s2s_read")
		}
	}
	err := validateRecordReadAnnotations(fds)
	if err == nil {
		t.Fatal("expected failure for c2s_read on a record granting nobody s2s_read")
	}
	if !strings.Contains(err.Error(), "s2s_read") {
		t.Fatalf("error must name the missing s2s_read, got: %v", err)
	}
}

// C2S_SCOPE_NAMESPACE is in the enum and not in the generator. The refusal says
// so rather than the value silently behaving like PRINCIPAL.
func TestRecordReadC2SNamespaceScopeRejected(t *testing.T) {
	err := validateRecordReadAnnotations(recordReadC2SFds([]string{"reader"}, true, wellFormedC2SFields(),
		&RecordReadC2S{Scope: "C2S_SCOPE_NAMESPACE", Exposure: "RPC_EXPOSURE_EXTERNAL"}))
	if err == nil {
		t.Fatal("expected failure for the unimplemented C2S_SCOPE_NAMESPACE")
	}
	if !strings.Contains(err.Error(), "declared but NOT IMPLEMENTED") {
		t.Fatalf("error must say the scope is declared and not implemented, got: %v", err)
	}
}

// An owning principal that cannot become an EQ criterion. Generation fails
// rather than degrading to a tenant-wide read that LOOKS per-person.
func TestRecordReadC2SUnenforceablePrincipalRejected(t *testing.T) {
	fields := []c2sField{
		{name: "acted_by", principal: true}, // no query_field: nothing to pin into
		{name: "namespace", namespace: true, queryEq: true},
	}
	err := validateRecordReadAnnotations(
		recordReadC2SFds([]string{"reader"}, true, fields, principalScope()))
	if err == nil {
		t.Fatal("expected failure for a principal field with no EQ query_field")
	}
	if !strings.Contains(err.Error(), "C2S_SCOPE_PRINCIPAL") || !strings.Contains(err.Error(), "acted_by") {
		t.Fatalf("error must name the scope and the unusable field, got: %v", err)
	}
}

// A namespace field with no criterion. The tenant pin would be generated and
// silently ignored by the store, leaving the aborting egress filter as the only
// boundary — the one arrangement the design rules out.
func TestRecordReadC2SUnqueryableNamespaceRejected(t *testing.T) {
	fields := []c2sField{
		{name: "acted_by", principal: true, queryEq: true},
		{name: "namespace", namespace: true}, // no query_field
	}
	err := validateRecordReadAnnotations(
		recordReadC2SFds([]string{"reader"}, true, fields, principalScope()))
	if err == nil {
		t.Fatal("expected failure for a namespace field with no EQ query_field")
	}
	if !strings.Contains(err.Error(), "store_namespace") || !strings.Contains(err.Error(), "silently ignored") {
		t.Fatalf("error must explain the inert pin, got: %v", err)
	}
}

// The three SERVER languages are accepted and the CLIENT-ONLY ones are refused
// by name. Go was refused here too until its record egress filters landed; a
// language that generates no service wrapper still has nowhere to put the pin,
// so the gate is narrowed rather than removed.
func TestRecordReadC2SServerLanguagesAcceptedClientOnlyRejected(t *testing.T) {
	fds := recordReadC2SFds([]string{"reader"}, true, wellFormedC2SFields(), principalScope())
	for _, lang := range []string{"go", "rust", "java"} {
		if err := validateRecordReadC2SLanguageSupport(fds, "reader", lang); err != nil {
			t.Fatalf("%s carries both egress filters and must be accepted, got: %v", lang, err)
		}
	}
	for _, lang := range []string{"python", "typescript", "swift", "csharp"} {
		err := validateRecordReadC2SLanguageSupport(fds, "reader", lang)
		if err == nil {
			t.Fatalf("expected failure for client-only target %q declaring (spec.record_read).c2s", lang)
		}
		for _, want := range []string{"store_principal", "store_namespace", `"` + lang + `"`} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("error must name %s, got: %v", want, err)
			}
		}
	}
	// A client-only target that declares no c2s block is untouched: this is additive.
	for _, fd := range fds {
		if fd.Package == "reader" {
			fd.RecordReads = []RecordRead{{Record: "owner.Row"}}
		}
	}
	if err := validateRecordReadC2SLanguageSupport(fds, "reader", "python"); err != nil {
		t.Fatalf("an s2s-only reader must build for a client-only target, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The private record's c2s read.
//
// A (spec.record).private record may never declare s2s_read — ValidateRecord-
// PrivateAnnotations refuses it. C2S_SCOPE_PRINCIPAL is the one legitimate read
// of a private row the s2s_read prerequisite would otherwise block: the c2s half
// reads through the caller's delegated user token under the mandatory owner pin,
// with no s2s hop to authorize. It is ACCEPTED — the generator half landed with
// these tests: a private record's typed reader targets the query plane's C2S
// service (the half record_map.go emits the full lifecycle for on a private
// type) instead of its s2s sibling, and the record's key is folded into its
// filter without an s2s_read declaration. Until then this same shape was refused
// as declared-but-not-implemented; see
// decisions/private-record-c2s-read-without-s2s-read-2026-09-23.md and
// decisions/private-record-c2s-read-not-yet-implemented-2026-09-23.md.
// ---------------------------------------------------------------------------

// makePrivate turns the fixture record into a private one: sets
// (spec.record).private and drops the s2s_read grant a private record can never
// carry.
func makePrivate(fds []*FileDescriptor) []*FileDescriptor {
	for _, fd := range fds {
		for mi := range fd.Messages {
			msg := &fd.Messages[mi]
			if !msg.Annotations["record"].BoolValue {
				continue
			}
			delete(msg.Annotations, "record_s2s_read")
			msg.Annotations["record_private"] = model.AnnotationValue{
				Type: model.AnnotationTypeBool, BoolValue: true,
			}
		}
	}
	return fds
}

// The shape this whole exception exists for: a private record read at
// C2S_SCOPE_PRINCIPAL, with no s2s_read grant anywhere, is ACCEPTED.
func TestRecordReadPrivateC2SPrincipalAccepted(t *testing.T) {
	err := validateRecordReadAnnotations(
		makePrivate(recordReadC2SFds([]string{"reader"}, true, wellFormedC2SFields(), principalScope())))
	if err != nil {
		t.Fatalf("a private record's C2S_SCOPE_PRINCIPAL read is the one read it has, and must validate; got: %v", err)
	}
}

// The data path is the plane's C2S service BY NAME. A trait that declares none
// leaves the read with nothing to dial — the s2s half is not a fallback, since
// it emits no rpcs for a private type at all — so the shape is refused here
// rather than three times over in the per-language reader generators.
func TestRecordReadPrivateC2SWithoutPlaneC2SServiceRejected(t *testing.T) {
	fds := makePrivate(recordReadC2SFds([]string{"reader"}, true, wellFormedC2SFields(), principalScope()))
	for _, fd := range fds {
		if fd.Trait != nil {
			fd.Trait.C2SService = ""
		}
	}
	err := validateRecordReadAnnotations(fds)
	if err == nil {
		t.Fatal("expected failure when the plane's trait declares no c2s_service to read a private record through")
	}
	if !strings.Contains(err.Error(), "declares no c2s_service") {
		t.Fatalf("error must name the missing c2s_service, got: %v", err)
	}
}

// The DISCLOSURE grant check runs on the private path too, so a missing
// c2s_read grant is still refused rather than waved through with the s2s_read
// prerequisite.
func TestRecordReadPrivateC2SWithoutDisclosureGrantRejected(t *testing.T) {
	err := validateRecordReadAnnotations(
		makePrivate(recordReadC2SFds(nil, false, wellFormedC2SFields(), principalScope())))
	if err == nil {
		t.Fatal("expected failure when a private record's c2s read has no (spec.record).c2s_read grant")
	}
	if !strings.Contains(err.Error(), "declares no (spec.record).c2s_read") {
		t.Fatalf("error must name the missing disclosure grant, got: %v", err)
	}
}

// Negative control for the shape the exception does NOT cover: an s2s-only read
// of a private record. There is no legitimate one — a peer calls with its own
// service identity and the owner pin has no caller principal to bind to — so the
// "no s2s_read" refusal is still the right answer.
func TestRecordReadPrivateWithoutC2SBlockRejected(t *testing.T) {
	err := validateRecordReadAnnotations(
		makePrivate(recordReadC2SFds(nil, false, wellFormedC2SFields(), nil)))
	if err == nil {
		t.Fatal("expected failure for an s2s-only record_read naming a private record")
	}
	if !strings.Contains(err.Error(), "declares no (spec.record).s2s_read") {
		t.Fatalf("error must still refuse the s2s-only read for want of a grant, got: %v", err)
	}
}

// The unimplemented scope is not reachable through the exception either: the
// skip is C2S_SCOPE_PRINCIPAL's alone, so a private record asking for
// C2S_SCOPE_NAMESPACE is still refused as not implemented.
func TestRecordReadPrivateC2SNamespaceScopeRejected(t *testing.T) {
	err := validateRecordReadAnnotations(makePrivate(recordReadC2SFds([]string{"reader"}, true,
		wellFormedC2SFields(), &RecordReadC2S{Scope: "C2S_SCOPE_NAMESPACE", Exposure: "RPC_EXPOSURE_EXTERNAL"})))
	if err == nil {
		t.Fatal("expected failure for C2S_SCOPE_NAMESPACE on a private record")
	}
	if !strings.Contains(err.Error(), "declared but NOT IMPLEMENTED") {
		t.Fatalf("error must say the scope is declared and not implemented, got: %v", err)
	}
}
