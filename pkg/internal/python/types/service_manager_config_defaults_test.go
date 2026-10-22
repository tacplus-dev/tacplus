// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"strings"
	"testing"
	"text/template"
)

// pyConfigDefaultsSmData is the minimal service_manager.py.tmpl input that
// reaches the state-singleton config delivery block: HasGrpc + HasStates, with
// one gRPC service that has a config state.
func pyConfigDefaultsSmData(hasConfigState bool) pySmTemplateData {
	entry := pyGrpcServiceEntry{
		GetterName:        "demo_grpc_impl",
		HandleConfigName:  "demo_config",
		ServiceName:       "DemoService",
		PackageName:       "demo",
		WrapperImportPath: "svc.types.demo.service",
		WrapperClassName:  "GrpcServer",
		WrapperVarName:    "wrapper_demo",
		AuthDisabled:      true,
	}
	data := pySmTemplateData{
		PythonPackageBase: "svc",
		ServiceLabel:      "demo",
		HasGrpc:           true,
		HasStates:         true,
	}
	if hasConfigState {
		entry.ConfigStateGroupAccessor = "services"
		entry.ConfigStateAccessor = "demo_config_state"
		entry.HasConfigState = true
		entry.ConfigDefaultsFn = "demo_config_defaults"
		data.ConfigDefaultsImports = []pyConfigDefaultsImport{{
			Alias:      "demo_config_defaults",
			ImportFrom: "svc.types.services.demo.helpers",
		}}
	}
	data.GrpcServices = []pyGrpcServiceEntry{entry}
	return data
}

func renderPySm(t *testing.T, data pySmTemplateData) string {
	t.Helper()
	tmplStr, err := loadTemplate("service_manager.py.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.py.tmpl: %v", err)
	}
	tmpl, err := template.New("service_manager").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse service_manager template: %v", err)
	}
	var out bytes.Buffer
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute service_manager template: %v", err)
	}
	return out.String()
}

// TestPythonServiceManagerAppliesConfigDefaultsOnBothDeliveryPaths asserts the
// generated Python ServiceManager applies the proto-declared field defaults to
// the state-singleton config on BOTH delivery paths — the initial fetch and the
// hot-reload on_change callback — the same way Go
// (templates/go/types/service_manager.go.tmpl), Rust and Java already do.
//
// Without this the declared `[(spec.default_int) = 30]` on a config field has
// no runtime effect in Python: kernel seeds config state from the deploy
// bundle, and a field the bundle omits arrives at the proto3 zero.
func TestPythonServiceManagerAppliesConfigDefaultsOnBothDeliveryPaths(t *testing.T) {
	rendered := renderPySm(t, pyConfigDefaultsSmData(true))

	if !strings.Contains(rendered, "from svc.types.services.demo.helpers import config_defaults as demo_config_defaults") {
		t.Fatalf("rendered python service manager missing config_defaults import:\n%s", rendered)
	}

	fetchIdx := strings.Index(rendered, "initial_config = await config_accessor.fetch()")
	initialDefaultsIdx := strings.Index(rendered, "demo_config_defaults(initial_config)")
	deliverIdx := strings.Index(rendered, "await handler.demo_config(initial_config)")
	onChangeIdx := strings.Index(rendered, "def _on_demo_config_change(next_config) -> None:")
	reloadDefaultsIdx := strings.Index(rendered, "demo_config_defaults(next_config)")
	applyIdx := strings.Index(rendered, "self._create_task(self._apply_config_update(")
	if fetchIdx < 0 || initialDefaultsIdx < 0 || deliverIdx < 0 || onChangeIdx < 0 || reloadDefaultsIdx < 0 || applyIdx < 0 {
		t.Fatalf("could not locate config-defaults wiring markers in rendered output:\n%s", rendered)
	}
	if !(fetchIdx < initialDefaultsIdx && initialDefaultsIdx < deliverIdx) {
		t.Fatalf("initial fetch must apply defaults after fetch() and before the handler call:\n%s", rendered)
	}
	if !(onChangeIdx < reloadDefaultsIdx && reloadDefaultsIdx < applyIdx) {
		t.Fatalf("hot-reload path must apply defaults inside the on_change callback before dispatching:\n%s", rendered)
	}

	pyCompileCheck(t, rendered, "service_manager.py")
}

// TestPythonServiceManagerOmitsConfigDefaultsWithoutConfigState asserts no
// defaults seam is emitted for a service with no config state, so the import
// and the call stay tied to the state-singleton delivery path.
func TestPythonServiceManagerOmitsConfigDefaultsWithoutConfigState(t *testing.T) {
	rendered := renderPySm(t, pyConfigDefaultsSmData(false))

	for _, notWant := range []string{"config_defaults", "config_accessor"} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("rendered python service manager must omit %q without a config state:\n%s", notWant, rendered)
		}
	}

	pyCompileCheck(t, rendered, "service_manager_no_config_state.py")
}
