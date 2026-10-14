// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"strings"
	"testing"
	"text/template"
)

// tsConfigDefaultsSmData is the minimal service_manager.ts.tmpl input that
// reaches the state-singleton config delivery block: HasGrpc + HasStates, with
// one gRPC service that has a config state.
func tsConfigDefaultsSmData(hasConfigState bool) tsSmTemplateData {
	entry := tsGrpcServiceEntry{
		GetterName:        "demoGrpcImpl",
		HandleConfigName:  "demoConfig",
		ServiceName:       "DemoService",
		HandlerTypeName:   "DemoServiceHandler",
		PackageName:       "demo",
		ImportPath:        "../types/services/demo/index.js",
		WrapperImportPath: "./internal/grpc/servers/services/demo/service.js",
		WrapperVarName:    "wrapperDemo",
		WrapperClassName:  "GrpcServer",
		AuthDisabled:      true,
	}
	data := tsSmTemplateData{
		TsPackageBase:   "demo-tacplus",
		TsPackagePrefix: "demo-tacplus-",
		ServiceLabel:    "demo",
		HasGrpc:         true,
		HasStates:       true,
	}
	if hasConfigState {
		entry.ConfigStateGroupAccessor = "services"
		entry.ConfigStateAccessor = "demoConfigState"
		entry.HasConfigState = true
		entry.ConfigDefaultsFn = "demoConfigDefaults"
		data.ConfigDefaultsImports = []tsConfigDefaultsImport{{
			Alias:      "demoConfigDefaults",
			ImportPath: "../types/services/demo/index.js",
		}}
	}
	data.GrpcServices = []tsGrpcServiceEntry{entry}
	return data
}

func renderTsSm(t *testing.T, data tsSmTemplateData) string {
	t.Helper()
	tmplStr, err := loadTemplate("service_manager.ts.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate service_manager.ts.tmpl: %v", err)
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

// TestTypeScriptServiceManagerAppliesConfigDefaultsOnBothDeliveryPaths asserts
// the generated TypeScript ServiceManager applies the proto-declared field
// defaults to the state-singleton config on BOTH delivery paths — the initial
// fetch and the hot-reload onChange callback — the same way Go
// (templates/go/types/service_manager.go.tmpl), Rust and Java already do.
//
// Without this the declared `[(spec.default_int) = 30]` on a config field has
// no runtime effect in TypeScript: kernel seeds config state from the deploy
// bundle, and a field the bundle omits arrives at the proto3 zero.
func TestTypeScriptServiceManagerAppliesConfigDefaultsOnBothDeliveryPaths(t *testing.T) {
	rendered := renderTsSm(t, tsConfigDefaultsSmData(true))

	if !strings.Contains(rendered, "import { configDefaults as demoConfigDefaults } from '../types/services/demo/index.js';") {
		t.Fatalf("rendered typescript service manager missing configDefaults import:\n%s", rendered)
	}

	fetchIdx := strings.Index(rendered, "const initialConfig = await configAccessor.fetch();")
	initialDefaultsIdx := strings.Index(rendered, "demoConfigDefaults(initialConfig);")
	deliverIdx := strings.Index(rendered, "await handler.demoConfig(initialConfig);")
	onChangeIdx := strings.Index(rendered, "configAccessor.onChange((nextConfig) => {")
	reloadDefaultsIdx := strings.Index(rendered, "demoConfigDefaults(nextConfig);")
	dispatchIdx := strings.Index(rendered, "this.trackTask('demoConfig config update'")
	if fetchIdx < 0 || initialDefaultsIdx < 0 || deliverIdx < 0 || onChangeIdx < 0 || reloadDefaultsIdx < 0 || dispatchIdx < 0 {
		t.Fatalf("could not locate config-defaults wiring markers in rendered output:\n%s", rendered)
	}
	if !(fetchIdx < initialDefaultsIdx && initialDefaultsIdx < deliverIdx) {
		t.Fatalf("initial fetch must apply defaults after fetch() and before the handler call:\n%s", rendered)
	}
	if !(onChangeIdx < reloadDefaultsIdx && reloadDefaultsIdx < dispatchIdx) {
		t.Fatalf("hot-reload path must apply defaults inside the onChange callback before dispatching:\n%s", rendered)
	}

	tsSyntaxCheck(t, rendered, "service_manager.ts")
}

// TestTypeScriptServiceManagerOmitsConfigDefaultsWithoutConfigState asserts no
// defaults seam is emitted for a service with no config state, so the import
// and the call stay tied to the state-singleton delivery path.
func TestTypeScriptServiceManagerOmitsConfigDefaultsWithoutConfigState(t *testing.T) {
	rendered := renderTsSm(t, tsConfigDefaultsSmData(false))

	for _, notWant := range []string{"configDefaults", "configAccessor"} {
		if strings.Contains(rendered, notWant) {
			t.Fatalf("rendered typescript service manager must omit %q without a config state:\n%s", notWant, rendered)
		}
	}

	tsSyntaxCheck(t, rendered, "service_manager_no_config_state.ts")
}
