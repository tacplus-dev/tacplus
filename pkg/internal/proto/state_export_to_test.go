// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"reflect"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// (spec.service_state).export_to (#928): an exported root names the packages
// allowed to import it. These fixtures reuse the owner/consumer pair of
// state_import_unification_test.go with the owner's export narrowed.

func ownerExportToProto(option string) string {
	return strings.Replace(ownerStateImportProto,
		`{name: "OwnerState", label: ["owner"], export: true}`, option, 1)
}

const fileLevelConsumerStateImportProto = `syntax = "proto3";
package consumer;

import "sys/spec.proto";

option (spec.state_imports) = {source: "owner.OwnerState"};

message ConsumerState {
  option (spec.service_state) = {name: "ConsumerState", label: ["consumer"]};

  string id = 1;
}
`

func TestStateExportToAllowsListedImporter(t *testing.T) {
	owner := ownerExportToProto(`{name: "OwnerState", label: ["owner"], export: true, export_to: ["admiral", "consumer"]}`)
	dir := prepareStateImportFixtureWithOwner(t, owner, consumerStateImportProto)

	if err := ValidateStateConsistency(dir); err != nil {
		t.Fatalf("ValidateStateConsistency: %v", err)
	}
	cfg := &tactype.Config{SelfPackage: "consumer"}
	if err := ResolveStatesFromProto(cfg, dir); err != nil {
		t.Fatalf("ResolveStatesFromProto: %v", err)
	}
	var foundImport bool
	for _, state := range cfg.States {
		if state.ProtoFile == "owner.proto" && state.MessageName == "OwnerState" && state.AccessMode == tactype.AccessRO {
			foundImport = true
		}
	}
	if !foundImport {
		t.Fatalf("resolved states = %+v, want imported RO OwnerState", cfg.States)
	}
}

func TestStateExportToRefusesUnlistedFieldImport(t *testing.T) {
	owner := ownerExportToProto(`{name: "OwnerState", label: ["owner"], export: true, export_to: ["admiral"]}`)
	dir := prepareStateImportFixtureWithOwner(t, owner, consumerStateImportProto)

	err := ValidateStateConsistency(dir)
	if err == nil {
		t.Fatal("ValidateStateConsistency succeeded, want the unlisted importer refused")
	}
	for _, want := range []string{`"owner.OwnerState"`, `package "consumer"`, `export_to is ["admiral"]`, "owner.proto"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ValidateStateConsistency error = %v, want it to name %s", err, want)
		}
	}

	err = ResolveStatesFromProto(&tactype.Config{SelfPackage: "consumer"}, dir)
	if err == nil {
		t.Fatal("ResolveStatesFromProto succeeded, want the unlisted importer refused")
	}
	for _, want := range []string{`"owner.OwnerState"`, `"consumer"`, `export_to is ["admiral"]`} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ResolveStatesFromProto error = %v, want it to name %s", err, want)
		}
	}
}

func TestStateExportToRefusesUnlistedFileLevelImport(t *testing.T) {
	owner := ownerExportToProto(`{name: "OwnerState", label: ["owner"], export: true, export_to: ["admiral"]}`)
	dir := prepareStateImportFixtureWithOwner(t, owner, fileLevelConsumerStateImportProto)

	err := ResolveStatesFromProto(&tactype.Config{SelfPackage: "consumer"}, dir)
	if err == nil {
		t.Fatal("ResolveStatesFromProto succeeded, want the unlisted file-level importer refused")
	}
	for _, want := range []string{`"owner.OwnerState"`, `"consumer"`, `export_to is ["admiral"]`, "consumer.proto"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("ResolveStatesFromProto error = %v, want it to name %s", err, want)
		}
	}
}

// No export_to: export: true keeps its meaning, any package may import, and
// every importer in the build is authorized.
func TestStateExportToAbsentKeepsExportOpen(t *testing.T) {
	dir := prepareStateImportFixture(t, consumerStateImportProto)
	writeProto(t, dir, "other.proto", strings.Replace(fileLevelConsumerStateImportProto, "consumer", "other", -1))

	if err := ValidateStateConsistency(dir); err != nil {
		t.Fatalf("ValidateStateConsistency: %v", err)
	}
	for _, pkg := range []string{"consumer", "other"} {
		if err := ResolveStatesFromProto(&tactype.Config{SelfPackage: pkg}, dir); err != nil {
			t.Fatalf("ResolveStatesFromProto(%s): %v", pkg, err)
		}
	}
	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	got := stateImportersByRoot(dp.GetAllFiles())["owner.OwnerState"]
	if want := []string{"consumer", "other"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("importers of owner.OwnerState = %v, want %v", got, want)
	}
}

// The owner's generated state service authorizes the owner and the listed
// importers only, even when an unlisted importer's proto is in the build.
func TestStateExportToNarrowsStateServiceAuth(t *testing.T) {
	owner := ownerExportToProto(`{name: "OwnerState", label: ["owner"], export: true, export_to: ["consumer"]}`)
	dir := prepareStateImportFixtureWithOwner(t, owner, consumerStateImportProto)
	writeProto(t, dir, "other.proto", strings.Replace(fileLevelConsumerStateImportProto, "consumer", "other", -1))

	dp, err := parser.NewDescriptorParser(dir)
	if err != nil {
		t.Fatalf("NewDescriptorParser: %v", err)
	}
	importers := stateImportersByRoot(dp.GetAllFiles())["owner.OwnerState"]
	got := stateServiceAuthServices("owner", importers)
	if want := []string{"owner", "consumer"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("auth_services of OwnerStateService = %v, want %v", got, want)
	}
}

func TestStateExportToRootValidation(t *testing.T) {
	topicOwner := strings.Replace(ownerExportToProto(`{name: "OwnerState", label: ["owner"], export: true, export_to: ["consumer"]}`),
		"  OwnerRecord current = 1;",
		"  OwnerRecord current = 1;\n  OwnerRecord events = 2 [(spec.topic) = true];", 1)
	cases := []struct {
		name  string
		owner string
		want  string
	}{
		{"without export", ownerExportToProto(`{name: "OwnerState", label: ["owner"], export_to: ["consumer"]}`),
			"sets export_to without export: true"},
		{"empty entry", ownerExportToProto(`{name: "OwnerState", label: ["owner"], export: true, export_to: ["consumer", ""]}`),
			`export_to entry "" is not a proto package name`},
		{"invalid entry", ownerExportToProto(`{name: "OwnerState", label: ["owner"], export: true, export_to: ["consumer", "ad-miral"]}`),
			`export_to entry "ad-miral" is not a proto package name`},
		{"duplicate entry", ownerExportToProto(`{name: "OwnerState", label: ["owner"], export: true, export_to: ["consumer", "consumer"]}`),
			`export_to lists "consumer" twice`},
		{"owner entry", ownerExportToProto(`{name: "OwnerState", label: ["owner"], export: true, export_to: ["consumer", "owner"]}`),
			`export_to lists its own package "owner"`},
		{"topic root", topicOwner, "export_to on a state root with topic boundaries"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := prepareStateImportFixtureWithOwner(t, tc.owner, consumerStateImportProto)
			err := ValidateStateConsistency(dir)
			if err == nil {
				t.Fatalf("ValidateStateConsistency succeeded, want %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "OwnerState") {
				t.Fatalf("ValidateStateConsistency error = %v, want %q naming OwnerState", err, tc.want)
			}
		})
	}
}

// A package absent from this build is accepted: the importer may be built in
// another project (the Admiral is).
func TestStateExportToAcceptsPackageOutsideBuild(t *testing.T) {
	owner := ownerExportToProto(`{name: "OwnerState", label: ["owner"], export: true, export_to: ["admiral", "consumer"]}`)
	dir := prepareStateImportFixtureWithOwner(t, owner, consumerStateImportProto)
	if err := ValidateStateConsistency(dir); err != nil {
		t.Fatalf("ValidateStateConsistency: %v", err)
	}
}
