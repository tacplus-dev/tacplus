// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

func TestTypeScriptStateWrapperWatchLifecycleRenders(t *testing.T) {
	rendered := renderTSStateWrapperForTest(t, tsStateWrapperData{
		Header:               "// generated for test",
		TypesImport:          "../../types/services/demo/index.js",
		TraceImport:          "../../../types/sys/trace/index.js",
		ServiceManagerImport: "../../../chassis/servicemanager/index.js",
		ProtoPackage:         "demo",
		StateRootName:        "DemoState",
		StateServiceName:     "DemoStateService",
		Singletons: []tsSingletonBoundary{{
			FieldName:   "config",
			FieldPascal: "Config",
			TypeName:    "Config",
			TypePrefix:  "types",
		}},
		Collections: []tsCollectionBoundary{{
			FieldName:    "items",
			FieldPascal:  "Items",
			TypeName:     "Item",
			TypePrefix:   "types",
			KeyFieldName: "itemId",
		}},
		StateImports: []tsStateImportField{{
			FieldName:   "baseState",
			FieldPascal: "BaseState",
			TypeName:    "BaseState",
			ImportAlias: "baseTypes",
		}},
		CrossImports: []tsCrossImport{{
			Alias:      "baseTypes",
			ImportPath: "../../types/services/base/index.js",
		}},
	})

	for _, expected := range []string{
		"import * as grpc from '@grpc/grpc-js';",
		"import { create, fromBinary, toBinary } from '@bufbuild/protobuf';",
		"import { type ChannelProvider } from '../../../chassis/servicemanager/index.js';",
		"import * as traceWatchTypes from '../../../types/sys/trace/index.js';",
		"export interface WatchStatus",
		"private _watchStreams: Set<grpc.ClientReadableStream<any>> = new Set();",
		"private _watchStatuses: Map<string, WatchStatus> = new Map();",
		"setWatchStatusListener(listener: ((status: WatchStatus) => void) | null): void",
		"watchStatuses(): Map<string, WatchStatus>",
		"watchesHealthy(): boolean",
		"setChannelProvider(channelProvider: ChannelProvider, tokenProvider?: () => Promise<string>): void",
		"this.stopWatches();",
		"void this.runWatchConfig(0);",
		"void this.runWatchItems(0);",
		"void this.runWatchBaseState(0);",
		"stream.cancel();",
		"this._channelProvider.channel();",
		"throw unauthenticatedError('auth token is empty');",
		"this.channel().makeServerStreamRequest<traceWatchTypes.TraceRequest, types.Config>",
		"this.recordWatchHealthy('DemoState.Config', false);",
		"this.recordWatchRetrying(watchName, received, attempt, err ? String(err) : 'stream completed', err);",
		"'/demo.DemoStateService/WatchConfig'",
		"toBinary(traceWatchTypes.TraceRequestSchema, msg)",
		"fromBinary(types.ConfigSchema, new Uint8Array(buf))",
		"types.ItemsEntriesResponse",
		"fromBinary(types.ItemsEntriesResponseSchema, new Uint8Array(buf))",
		"Object.entries(value.entries ?? {}).map(([k, v]) => [k, v as types.Item])",
		"this._notifyItemsChange();",
		"this.channel().makeServerStreamRequest<traceWatchTypes.TraceRequest, baseTypes.BaseState>",
		"fromBinary(baseTypes.BaseStateSchema, new Uint8Array(buf))",
		"this._notifyItemsChange();",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered TypeScript state wrapper missing %q:\n%s", expected, rendered)
		}
	}
}

func TestTypeScriptStateClientAggregatorsUseWrapperClientClassNames(t *testing.T) {
	outputDir := t.TempDir()
	err := generateTSStateClientAggregators(&tactype.Config{
		OutputDir: outputDir,
		SmPrefix:  "s2sm",
	}, []tsGeneratedStateClient{{
		DirName:       "demo_config",
		StateRootName: "DemoConfigState",
		Group:         "services",
	}})
	if err != nil {
		t.Fatalf("generateTSStateClientAggregators: %v", err)
	}

	groupBytes, err := os.ReadFile(filepath.Join(outputDir, "s2sm", "internal", "state", "services", "state_clients.ts"))
	if err != nil {
		t.Fatalf("read group state_clients.ts: %v", err)
	}
	groupRendered := string(groupBytes)
	for _, expected := range []string{
		"import { DemoConfigStateClient } from './demo_config/state.js';",
		"import { type ChannelProvider } from '../../../../chassis/servicemanager/index.js';",
		"private readonly _demoConfigState = new DemoConfigStateClient();",
		"this._demoConfigState.setChannelProvider(channelProvider, tokenProvider);",
		"this._demoConfigState.close();",
		"demoConfigState(): DemoConfigStateClient { return this._demoConfigState; }",
	} {
		if !strings.Contains(groupRendered, expected) {
			t.Fatalf("rendered TypeScript group state aggregator missing %q:\n%s", expected, groupRendered)
		}
	}
	if strings.Contains(groupRendered, "DemoConfigStateStateClient") {
		t.Fatalf("rendered TypeScript group state aggregator still uses stale StateStateClient name:\n%s", groupRendered)
	}

	rootBytes, err := os.ReadFile(filepath.Join(outputDir, "s2sm", "internal", "state", "state_clients.ts"))
	if err != nil {
		t.Fatalf("read root state_clients.ts: %v", err)
	}
	rootRendered := string(rootBytes)
	for _, expected := range []string{
		"import { type ChannelProvider } from '../../../chassis/servicemanager/index.js';",
		"this._services.setChannelProvider(channelProvider, tokenProvider);",
		"this._services.close();",
	} {
		if !strings.Contains(rootRendered, expected) {
			t.Fatalf("rendered TypeScript root state aggregator missing %q:\n%s", expected, rootRendered)
		}
	}
}

func TestTypeScriptServiceManagerWiresStateClients(t *testing.T) {
	outputDir := t.TempDir()
	inputDir := t.TempDir()
	cfg := &tactype.Config{
		OutputDir:     outputDir,
		InputDir:      inputDir,
		TsPackageBase: "demo-tacplus",
		SelfPackage:   "demo",
		States: []tactype.StateEntry{
			{ProtoFile: "services/demo.proto", MessageName: "DemoState"},
		},
	}

	if err := generateTsServiceManager(cfg, nil, nil); err != nil {
		t.Fatalf("generateTsServiceManager: %v", err)
	}
	renderedBytes, err := os.ReadFile(filepath.Join(outputDir, "s2sm", "index.ts"))
	if err != nil {
		t.Fatalf("read generated service manager: %v", err)
	}
	rendered := string(renderedBytes)

	for _, expected := range []string{
		"private readonly _stateClients = new StateClients();",
		"this._stateClients.setConfigFetchTimeoutSeconds(configFetchTimeoutSeconds(cfg));",
		"this._stateClients.setChannelProvider(new ChannelProvider(stateStoreConn), () => this.getToken());",
		"this._stateClients.close();",
		"stateClients(): StateClients { return this._stateClients; }",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered TypeScript service manager missing %q:\n%s", expected, rendered)
		}
	}
}

// The singleton fetch retries while the fetched value fails its own generated
// validation, bounded by config_fetch_timeout_seconds — see
// decisions/state-singleton-startup-retry-2026-09-20.md.
func TestTypeScriptStateWrapperSingletonFetchRetriesOnValidationFailure(t *testing.T) {
	rendered := renderTSStateWrapperForTest(t, tsStateWrapperData{
		Header:               "// generated for test",
		TypesImport:          "../../types/services/demo/index.js",
		TraceImport:          "../../../types/sys/trace/index.js",
		ServiceManagerImport: "../../../chassis/servicemanager/index.js",
		ProtoPackage:         "demo",
		StateRootName:        "DemoState",
		StateServiceName:     "DemoStateService",
		Singletons: []tsSingletonBoundary{{
			FieldName:   "config",
			FieldPascal: "Config",
			TypeName:    "Config",
			TypePrefix:  "types",
		}},
	})

	for _, expected := range []string{
		"function failedPreconditionError(message: string): Error & { code?: number } {",
		"err.code = grpc.status.FAILED_PRECONDITION;",
		"private _configFetchTimeoutSeconds = 30;",
		"private watchRetryDelayMillis(attempt: number): number {",
		"setConfigFetchTimeoutSeconds(seconds: number): void {",
		"private async sleepBeforeFetchRetry(attempt: number, deadlineMillis: number): Promise<boolean> {",
		"const delay = Math.min(this.watchRetryDelayMillis(attempt), remaining);",
		"const timeoutSeconds = this._configFetchTimeoutSeconds;",
		"const deadlineMillis = Date.now() + timeoutSeconds * 1000;",
		"types.validateConfig(value);",
		"if (!(await this.sleepBeforeFetchRetry(attempt, deadlineMillis))) {",
		"throw failedPreconditionError(",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("rendered TypeScript state wrapper missing singleton fetch retry surface %q:\n%s", expected, rendered)
		}
	}
}

// The singleton fetch must default the fetched value before validating it: the
// wire carries no proto defaults, so a field omitted because it declares a
// (spec.default_*) arrives zero-valued and fails its own (spec.assert_*) check
// for the whole config_fetch_timeout_seconds budget. See the 2026-09-23
// scottyagent crash and the cross-language pin in
// pkg/internal/common/state_singleton_fetch_defaults_parity_test.go.
func TestTypeScriptStateWrapperSingletonFetchDefaultsBeforeValidate(t *testing.T) {
	rendered := renderTSStateWrapperForTest(t, tsStateWrapperData{
		Header:               "// generated for test",
		TypesImport:          "../../types/services/demo/index.js",
		TraceImport:          "../../../types/sys/trace/index.js",
		ServiceManagerImport: "../../../chassis/servicemanager/index.js",
		ProtoPackage:         "demo",
		StateRootName:        "DemoState",
		StateServiceName:     "DemoStateService",
		Singletons: []tsSingletonBoundary{{
			FieldName:   "config",
			FieldPascal: "Config",
			TypeName:    "Config",
			TypePrefix:  "types",
			DefaultsFn:  "configDefaults",
		}},
	})

	const want = "            types.configDefaults(value);\n            try {\n                types.validateConfig(value);"
	if !strings.Contains(rendered, want) {
		t.Fatalf("rendered TypeScript singleton fetch does not apply configDefaults before validate:\n%s", rendered)
	}
}

func renderTSStateWrapperForTest(t *testing.T, data tsStateWrapperData) string {
	t.Helper()
	tmplStr, err := loadTemplate("statewrapper.ts.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	tmpl, err := template.New("statewrapper").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse state wrapper template: %v", err)
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute state wrapper template: %v", err)
	}
	return out.String()
}
