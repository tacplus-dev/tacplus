// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	stateproto "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// A product custodian's Java ServiceManager runs its onTenantDeleted (#475):
// the Handler requires it, and the manager feeds the runner every snapshot of
// the fleet's TenantDeletions root and starts it, and stops it on shutdown. A
// service that is no custodian gets none of it. Go twin:
// TestGoServiceManagerRunsOnTenantDeleted.
func TestJavaServiceManagerRunsOnTenantDeleted(t *testing.T) {
	if _, err := exec.LookPath("protoc"); err != nil {
		t.Skip("protoc not available")
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sys"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"spec.proto", "tenantdeletion.proto"} {
		src, err := os.ReadFile(filepath.Join("..", "..", "..", "proto", "sys", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sys", name), src, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	render := func(file string, custodian bool) string {
		t.Helper()
		cfg := &tactype.Config{JavaPackageBase: "com.example", SelfPackage: "billing", FlatProtoDir: dir}
		if custodian {
			cfg.States = []tactype.StateEntry{{
				ProtoFile:             "sys/tenantdeletion.proto",
				MessageName:           stateproto.TenantDeletionStateName,
				AccessMode:            tactype.AccessRO,
				IsTenantDeletionState: true,
			}}
		}
		svcs := []ServiceInfo{{ProtoFile: "svc/billing.proto", PackageName: "billing", ServiceName: "BillingService", GrpcName: "billing", ParentDir: "svc", IsGrpc: true, IsS2S: true, Config: cfg}}
		data, err := buildJavaSmTemplateData(cfg, svcs, nil, "", nil, nil)
		if err != nil {
			t.Fatalf("buildJavaSmTemplateData: %v", err)
		}
		if data.HasTenantDeletion != custodian {
			t.Fatalf("HasTenantDeletion = %v, want %v", data.HasTenantDeletion, custodian)
		}
		if custodian && (data.TenantDeletionGroupAccessor != "sys" || data.TenantDeletionStateAccessor != "tenantDeletions" || data.HasSysState) {
			t.Fatalf("tenant deletion data = %q, %q, HasSysState %v", data.TenantDeletionGroupAccessor, data.TenantDeletionStateAccessor, data.HasSysState)
		}
		return renderJavaTemplateForTest(t, file, data)
	}

	sm := render("ServiceManager.java.tmpl", true)
	for _, want := range []string{
		"import com.example.chassis.tenantdeletion.TenantDeletionRunner;",
		"TenantDeletionRunner tenantDeletions = new TenantDeletionRunner(handler::onTenantDeleted);",
		"var tenantDeletionState = stateClients.sys().tenantDeletions();",
		"tenantDeletionState.subscribeDeleted(rows -> {",
		"for (var row : tenantDeletionState.getDeleted()) {",
		"tenantDeletions.start();",
		"tenantDeletions.shutdown(",
		"// trace: unrelated: deletion is driven by the state host's record, not a request.",
	} {
		if !strings.Contains(sm, want) {
			t.Fatalf("the custodian's ServiceManager is missing %q", want)
		}
	}
	// The callback is registered before the first snapshot is read, so no
	// deletion falls between the two; the runner starts after both, and stops
	// before the handler shuts down.
	subscribe := strings.Index(sm, "subscribeDeleted(")
	snapshot := strings.Index(sm, "tenantDeletionState.getDeleted()")
	start := strings.Index(sm, "tenantDeletions.start();")
	if !(subscribe < snapshot && snapshot < start) {
		t.Fatal("the custodian's ServiceManager must subscribe, then read the snapshot, then start the runner")
	}
	if stop, handlerShutdown := strings.Index(sm, "tenantDeletions.shutdown("), strings.LastIndex(sm, "handler.shutdown();"); stop < start || stop > handlerShutdown {
		t.Fatal("the tenant deletion runner must stop before the handler shuts down")
	}
	handler := render("Handler.java.tmpl", true)
	if !strings.Contains(handler, "void onTenantDeleted(String tid) throws Exception;") {
		t.Fatal("the custodian's Handler does not require onTenantDeleted")
	}

	for _, file := range []string{"ServiceManager.java.tmpl", "Handler.java.tmpl"} {
		if src := render(file, false); strings.Contains(src, "TenantDeleted") || strings.Contains(src, "tenantDeletion") || strings.Contains(src, "TenantDeletion") {
			t.Fatalf("%s: a service that is no custodian runs onTenantDeleted", file)
		}
	}
}
