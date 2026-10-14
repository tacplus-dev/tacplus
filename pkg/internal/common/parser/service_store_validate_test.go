// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package parser

import (
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
)

// These tests pin the service_store uniqueness rule: two service_store-annotated
// messages whose names collide (exactly, or after snake_casing) would share a
// PersistedStore enum value, generated diskstore function names, and an on-disk
// snapshot path. Generation must fail loudly for every language instead.

func storeMsg(name string) model.Message {
	return model.Message{
		Name: name,
		Annotations: map[string]model.AnnotationValue{
			"service_store": {BoolValue: true},
		},
		Fields: []model.Field{{Name: "id", Type: "string"}},
	}
}

func TestValidateServiceStores_DuplicateNameAcrossPackages(t *testing.T) {
	files := []model.ProtoFile{
		{Package: "alpha", Messages: []model.Message{storeMsg("SessionCache")}},
		{Package: "beta", Messages: []model.Message{storeMsg("SessionCache")}},
	}
	err := ValidateServiceStores(files)
	if err == nil {
		t.Fatal("expected error for duplicate service_store message name across packages, got nil")
	}
	for _, want := range []string{"alpha.SessionCache", "beta.SessionCache", `"session_cache"`, "must be unique"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestValidateServiceStores_SnakeCaseCollision(t *testing.T) {
	// Distinct message names that snake_case to the same store identity.
	files := []model.ProtoFile{
		{Package: "alpha", Messages: []model.Message{storeMsg("HTTPServer")}},
		{Package: "beta", Messages: []model.Message{storeMsg("HttpServer")}},
	}
	err := ValidateServiceStores(files)
	if err == nil {
		t.Fatal("expected error for snake_case service_store collision, got nil")
	}
	for _, want := range []string{"alpha.HTTPServer", "beta.HttpServer", `"http_server"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestValidateServiceStores_ReportsAllCollisions(t *testing.T) {
	files := []model.ProtoFile{
		{Package: "alpha", Messages: []model.Message{storeMsg("FooStore"), storeMsg("BarStore")}},
		{Package: "beta", Messages: []model.Message{storeMsg("FooStore"), storeMsg("BarStore")}},
	}
	err := ValidateServiceStores(files)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	for _, want := range []string{`"foo_store"`, `"bar_store"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q missing %q", err.Error(), want)
		}
	}
}

func TestValidateServiceStores_NoCollision(t *testing.T) {
	files := []model.ProtoFile{
		{Package: "alpha", Messages: []model.Message{storeMsg("FooStore")}},
		{Package: "beta", Messages: []model.Message{storeMsg("BarStore")}},
		// Same name without the annotation is not a store and must not collide.
		{Package: "gamma", Messages: []model.Message{{Name: "FooStore"}}},
	}
	if err := ValidateServiceStores(files); err != nil {
		t.Fatalf("expected no error for unique store names, got: %v", err)
	}
}
