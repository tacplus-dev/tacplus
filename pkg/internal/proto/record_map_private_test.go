// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"strings"
	"testing"
)

// The record query plane's two halves are NOT symmetric for a
// (spec.record).private type, and this is the pin on that asymmetry.
//
// The S2S half is peer-facing: a caller reaches it with its own SERVICE
// identity, so there is no principal for the owner pin to compare a row's owner
// against and the only gate the three wrapper generators can apply is the
// (spec.store_namespace) tenant backstop. Emitting a private type's reads there
// hands every service the grant names every principal's rows in the tenant —
// the exact disclosure `private` exists to prevent. The skip lives in the
// injector rather than in three wrapper generators so Go, Java and Rust agree
// with no per-language check: the RPCs a wrapper would have to refuse are never
// emitted.
//
// The C2S half carries a USER TOKEN, which is the one identity the pin can bind
// to, so it keeps the private type's full lifecycle. A change that suppressed
// the type on both halves would close the hole and take the record's only
// generated read with it, so both directions are asserted here.

func privatePlaneRecords() []recordType {
	return []recordType{
		{
			typeName: "VaultRow", pkg: "vault", relPath: "vservices/vault.proto", key: "row_id",
			filters:   []recordFilter{{name: "owner", protoType: "string"}},
			principal: "owner",
		},
		{
			typeName: "VaultSecretRow", pkg: "vault", relPath: "vservices/vault.proto", key: "secret_id",
			filters:   []recordFilter{{name: "owner", protoType: "string"}},
			principal: "owner",
			private:   true,
		},
	}
}

func privateTestPlane() traitPlane {
	return traitPlane{
		relPath: "vservices/query.proto", name: "query", host: "stratum",
		s2sService: "RecordMap", c2sService: "RecordQuery", authServices: []string{"reporter"},
	}
}

func TestRecordPlaneS2SHalfEmitsNoReadForAPrivateRecord(t *testing.T) {
	s2s := recordMapS2SService(privateTestPlane(), privatePlaneRecords())

	if strings.Contains(s2s, "VaultSecretRow") {
		t.Fatalf("the plane's S2S half must emit no rpc for a (spec.record).private type; got:\n%s", s2s)
	}
	// Paired control: the plain record on the same plane keeps its full
	// lifecycle, so the skip is proven to be about `private` and not about the
	// emitter having stopped emitting.
	for _, want := range []string{
		"rpc CreateVaultRowQuery(", "rpc GetVaultRowCount(", "rpc GetVaultRowSample(",
		"rpc ListVaultRow(", "rpc DeleteVaultRowQuery(", "rpc UpdateVaultRowQuery(",
		"rpc WatchVaultRow(", "rpc SubscribeVaultRowCount(",
	} {
		if !strings.Contains(s2s, want) {
			t.Fatalf("the plane's S2S half dropped %q for the non-private record; got:\n%s", want, s2s)
		}
	}
}

func TestRecordPlaneC2SHalfKeepsAPrivateRecordsLifecycle(t *testing.T) {
	c2s := recordQueryC2SService(privateTestPlane(), privatePlaneRecords())

	for _, want := range []string{
		"rpc CreateVaultSecretRowQuery(", "rpc GetVaultSecretRowCount(", "rpc GetVaultSecretRowSample(",
		"rpc ListVaultSecretRow(", "rpc DeleteVaultSecretRowQuery(", "rpc UpdateVaultSecretRowQuery(",
		"rpc WatchVaultSecretRow(", "rpc SubscribeVaultSecretRowCount(",
	} {
		if !strings.Contains(c2s, want) {
			t.Fatalf("the plane's C2S half must keep %q for a private record — it carries the user token the owner pin binds to; got:\n%s", want, c2s)
		}
	}
}
