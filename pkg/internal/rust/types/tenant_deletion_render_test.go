// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	stateproto "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"github.com/tacplus-dev/tacplus/templates"
)

// A product custodian's Rust ServiceManager runs its on_tenant_deleted (#475),
// as Go's does: the Handler requires it, and the manager feeds the
// tenantdeletion Runner every snapshot of the fleet's TenantDeletions root,
// starts it and stops it at shutdown. A service that is no custodian gets none
// of it.
func TestRustServiceManagerRunsOnTenantDeleted(t *testing.T) {
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
		cfg := &tactype.Config{CrateBasePath: "ledger-model", SelfPackage: "billing", FlatProtoDir: dir, AnnotatedProtoDir: dir}
		if custodian {
			cfg.States = []tactype.StateEntry{{
				ProtoFile:             "sys/tenantdeletion.proto",
				MessageName:           stateproto.TenantDeletionStateName,
				AccessMode:            tactype.AccessRO,
				IsTenantDeletionState: true,
			}}
		}
		data, err := buildSMTemplateData(cfg, nil, nil, nil)
		if err != nil {
			t.Fatalf("buildSMTemplateData: %v", err)
		}
		if data.HasTenantDeletion != custodian {
			t.Fatalf("HasTenantDeletion = %v, want %v", data.HasTenantDeletion, custodian)
		}
		if custodian && (data.TenantDeletionGroupAccessor != "sys" || data.TenantDeletionClientAccessor != "tenant_deletions" ||
			data.TenantDeletionModPath != "crate::types::sys::tenantdeletion::tenantdeletion" || data.HasSysState) {
			t.Fatalf("tenant deletion data = %q, %q, %q, HasSysState %v", data.TenantDeletionGroupAccessor,
				data.TenantDeletionClientAccessor, data.TenantDeletionModPath, data.HasSysState)
		}
		tmplContent, err := loadTemplate(file)
		if err != nil {
			t.Fatal(err)
		}
		tmpl, err := template.New(file).Funcs(header.Funcs()).Parse(tmplContent)
		if err != nil {
			t.Fatal(err)
		}
		var out strings.Builder
		if err := tmpl.Execute(&out, data); err != nil {
			t.Fatalf("execute %s: %v", file, err)
		}
		return out.String()
	}

	sm := render("service_manager.rs.tmpl", true)
	for _, want := range []string{
		"let tenant_deletions = Arc::new(tenantdeletion::Runner::new({",
		"async move { h.on_tenant_deleted(&tid).await }",
		"let tenant_deletion_state = self.state_clients.sys().tenant_deletions();",
		"tenant_deletion_state.on_deleted_change(Arc::new(",
		"move |rows: &std::collections::HashMap<String, crate::types::sys::tenantdeletion::tenantdeletion::TenantDeletion>| {",
		"tenant_deletions.observe(tenant_deletion_state.get_deleted().iter().map(|row| row.tid.as_str()));",
		"self.spawn_owned(tenant_deletions.run(tenant_deletion_stop_rx));",
		"let _ = tenant_deletion_stop.send(true);",
		"// trace: unrelated: deletion is driven by the state host's record, not a request.",
	} {
		if !strings.Contains(sm, want) {
			t.Fatalf("the custodian's ServiceManager is missing %q", want)
		}
	}
	// The callback is registered before the first snapshot is read, so no
	// deletion falls between the two; the runner stops before the Handler's
	// shutdown.
	if strings.Index(sm, "on_deleted_change(") > strings.Index(sm, "get_deleted()") {
		t.Fatal("the first snapshot is read before the change callback is registered")
	}
	if strings.Index(sm, "tenant_deletion_stop.send(true)") > strings.Index(sm, "_ = handler.shutdown() =>") {
		t.Fatal("the runner is stopped after the Handler's shutdown")
	}
	handler := render("handler.rs.tmpl", true)
	if !strings.Contains(handler, "    async fn on_tenant_deleted(&self, tid: &str) -> Result<(), Box<dyn std::error::Error + Send + Sync>>;") {
		t.Fatal("the custodian's Handler does not require on_tenant_deleted")
	}

	for _, file := range []string{"service_manager.rs.tmpl", "handler.rs.tmpl"} {
		if src := render(file, false); strings.Contains(src, "tenant_deleted") || strings.Contains(src, "tenantdeletion") {
			t.Fatalf("%s: a service that is no custodian runs on_tenant_deleted", file)
		}
	}
}

// The runner chassis keeps the rules Go's chassis/tenantdeletion keeps: each
// tid once, the lowest ready first, empty ignored, a failure or a panic retried
// with a backoff from 1s doubling to 5m, ended by its stop signal.
func TestRustTenantDeletionChassisKeepsItsRules(t *testing.T) {
	src, err := templates.FS.ReadFile("rust/chassis/tenantdeletion/src/lib.rs.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"pub const DEFAULT_MIN_BACKOFF: Duration = Duration::from_secs(1);",
		"pub const DEFAULT_MAX_BACKOFF: Duration = Duration::from_secs(5 * 60);",
		"pending: BTreeMap<String, Attempt>,",
		"if tid.is_empty() || state.done.contains(tid) || state.pending.contains_key(tid) {",
		"Err(e) if e.is_panic() => {",
		"pub async fn run(self: Arc<Self>, mut stop: watch::Receiver<bool>) {",
		"fn runner_backoff()",
		"async fn runner_recovers_a_panic()",
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("chassis/tenantdeletion is missing %q", want)
		}
	}
	manifest, err := templates.FS.ReadFile("rust/chassis/tenantdeletion/Cargo.toml.tmpl")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(manifest), "name = \"tenantdeletion\"") {
		t.Fatal("chassis/tenantdeletion's Cargo.toml does not name the crate tenantdeletion")
	}
}
