// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"go/format"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	stateproto "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// A product custodian's Go ServiceManager runs its OnTenantDeleted (#475): the
// Handler requires it, and the manager feeds the runner every snapshot of the
// fleet's TenantDeletions root and starts it. A service that is no custodian
// gets none of it.
func TestGoServiceManagerRunsOnTenantDeleted(t *testing.T) {
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
		cfg := &tactype.Config{ModuleBasePath: "example.com/ledger/model", SelfPackage: "billing", FlatProtoDir: dir}
		if custodian {
			cfg.States = []tactype.StateEntry{{
				ProtoFile:             "sys/tenantdeletion.proto",
				MessageName:           stateproto.TenantDeletionStateName,
				AccessMode:            tactype.AccessRO,
				IsTenantDeletionState: true,
			}}
		}
		svcs := []ServiceInfo{{ProtoFile: "svc/billing.proto", PackageName: "billing", ServiceName: "BillingService", GrpcName: "billing", ParentDir: "svc", IsS2S: true, Config: cfg}}
		data, err := buildServiceManagerData(cfg, svcs, nil)
		if err != nil {
			t.Fatalf("buildServiceManagerData: %v", err)
		}
		if data.HasTenantDeletion != custodian {
			t.Fatalf("HasTenantDeletion = %v, want %v", data.HasTenantDeletion, custodian)
		}
		if custodian && (data.TenantDeletionProtoDir != "sys/tenantdeletion" || data.TenantDeletionGroupAccessor != "Sys" || data.HasSysState) {
			t.Fatalf("tenant deletion data = %q, %q, HasSysState %v", data.TenantDeletionProtoDir, data.TenantDeletionGroupAccessor, data.HasSysState)
		}
		tmplContent, err := loadTemplate(file)
		if err != nil {
			t.Fatal(err)
		}
		tmpl, err := template.New(file).Funcs(header.Funcs()).Parse(tmplContent)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			t.Fatalf("execute %s: %v", file, err)
		}
		formatted, err := format.Source(buf.Bytes())
		if err != nil {
			t.Fatalf("format %s: %v\n%s", file, err, buf.Bytes())
		}
		return string(formatted)
	}

	sm := render("service_manager.go.tmpl", true)
	for _, want := range []string{
		`"example.com/ledger/model/chassis/tenantdeletion"`,
		`tenantdeletion_pb "example.com/ledger/model/types/sys/tenantdeletion"`,
		"tenantDeletions := tenantdeletion.New(h.OnTenantDeleted)",
		"tenantDeletionState := sm.bc.Sys().TenantDeletions()",
		"tenantDeletionState.OnDeletedChange(func(rows []*tenantdeletion_pb.TenantDeletion) {",
		"tenantDeletions.Observe(deletedTids(tenantDeletionState.GetDeleted()))",
		"sm.tasks.Go(tenantDeletions.Run)",
		"// trace: unrelated:",
	} {
		if !strings.Contains(sm, want) {
			t.Fatalf("the custodian's ServiceManager is missing %q", want)
		}
	}
	// The callback is registered before the first snapshot is read, so no
	// deletion falls between the two.
	if strings.Index(sm, "OnDeletedChange(") > strings.Index(sm, "GetDeleted()") {
		t.Fatal("the first snapshot is read before the change callback is registered")
	}
	handler := render("handler.go.tmpl", true)
	if !strings.Contains(handler, "OnTenantDeleted(ctx context.Context, tid string) error") {
		t.Fatal("the custodian's Handler does not require OnTenantDeleted")
	}

	for _, file := range []string{"service_manager.go.tmpl", "handler.go.tmpl"} {
		if src := render(file, false); strings.Contains(src, "TenantDeleted") || strings.Contains(src, "tenantdeletion") {
			t.Fatalf("%s: a service that is no custodian runs OnTenantDeleted", file)
		}
	}
}
