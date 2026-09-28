// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/packages"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
)

func writeTypeScriptTestDependencies(t *testing.T, dir string) {
	t.Helper()
	content := `tools:
  typescript: ^5.3.3
typescript:
  dependencies:
    '@bufbuild/protobuf': ^2.11.0
    '@iarna/toml': ^2.2.5
    '@types/js-yaml': ^4.0.9
    js-yaml: ^4.1.0
`
	if err := os.WriteFile(filepath.Join(dir, "dependencies.yaml"), []byte(content), 0644); err != nil {
		t.Fatalf("write dependencies.yaml: %v", err)
	}
	packages.SetProtoDir(dir)
}

func TestGeneratePackageJsonFilesUsesGeneratedTypesDirectory(t *testing.T) {
	depsDir := t.TempDir()
	writeTypeScriptTestDependencies(t, depsDir)

	inputDir := t.TempDir()
	outputDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(inputDir, "generated"), 0755); err != nil {
		t.Fatalf("mkdir generated: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(inputDir, "sys"), 0755); err != nil {
		t.Fatalf("mkdir sys: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inputDir, "generated", "accounting_method.proto"), []byte("syntax = \"proto3\";\npackage accounting_method;\n"), 0644); err != nil {
		t.Fatalf("write generated proto: %v", err)
	}
	if err := os.WriteFile(filepath.Join(inputDir, "sys", "accounting.proto"), []byte("syntax = \"proto3\";\npackage accounting;\nimport \"generated/accounting_method.proto\";\n"), 0644); err != nil {
		t.Fatalf("write sys proto: %v", err)
	}

	cfg := &tactype.Config{
		OutputDir:     outputDir,
		TsPackageBase: "webapp-tacplus",
	}
	err := generatePackageJsonFiles(cfg, inputDir, []string{
		"generated/accounting_method.proto",
		"sys/accounting.proto",
	}, nil)
	if err != nil {
		t.Fatalf("generatePackageJsonFiles: %v", err)
	}

	generatedPkg := filepath.Join(outputDir, "types", "generated", "accounting_method", "package.json")
	if _, err := os.Stat(generatedPkg); err != nil {
		t.Fatalf("expected generated package.json at %s: %v", generatedPkg, err)
	}
	if _, err := os.Stat(filepath.Join(outputDir, "sys", "accounting_method", "package.json")); !os.IsNotExist(err) {
		t.Fatalf("did not expect empty sys/accounting_method package.json, stat err=%v", err)
	}

	accountingPkgBytes, err := os.ReadFile(filepath.Join(outputDir, "types", "sys", "accounting", "package.json"))
	if err != nil {
		t.Fatalf("read accounting package.json: %v", err)
	}
	if !strings.Contains(string(accountingPkgBytes), `"webapp-tacplus-generated-accounting_method": "*"`) {
		t.Fatalf("accounting package.json does not depend on generated accounting_method package:\n%s", accountingPkgBytes)
	}
}

func TestGenerateWorkspacePackageJsonCoversGeneratedPackages(t *testing.T) {
	depsDir := t.TempDir()
	writeTypeScriptTestDependencies(t, depsDir)

	outputDir := t.TempDir()
	cfg := &tactype.Config{
		OutputDir:     outputDir,
		TsPackageBase: "webapp-tacplus",
	}
	if err := generateWorkspacePackageJson(cfg); err != nil {
		t.Fatalf("generateWorkspacePackageJson: %v", err)
	}

	var pkg struct {
		Scripts    map[string]string `json:"scripts"`
		Type       string            `json:"type"`
		Workspaces []string          `json:"workspaces"`
	}
	pkgBytes, err := os.ReadFile(filepath.Join(outputDir, "package.json"))
	if err != nil {
		t.Fatalf("read package.json: %v", err)
	}
	if err := json.Unmarshal(pkgBytes, &pkg); err != nil {
		t.Fatalf("parse package.json: %v", err)
	}
	if got := pkg.Scripts["build"]; got != "tsc -p tsconfig.json" {
		t.Fatalf("build script = %q, want root tsc build", got)
	}
	if pkg.Type != "module" {
		t.Fatalf("root package type = %q, want module", pkg.Type)
	}

	for _, want := range []string{
		"types/generated/*",
		"types/clients/*",
		"types/vservices/*",
		"c2dm/internal/grpc/clients/*/*",
		"c2sm/internal/grpc/clients/*/*",
		"w2sm/internal/grpc_over_rest/*/*",
	} {
		if !containsString(pkg.Workspaces, want) {
			t.Fatalf("workspace %q missing from %v", want, pkg.Workspaces)
		}
	}

	tsconfigBytes, err := os.ReadFile(filepath.Join(outputDir, "tsconfig.json"))
	if err != nil {
		t.Fatalf("read tsconfig.json: %v", err)
	}
	if !strings.Contains(string(tsconfigBytes), `"include": ["**/*.ts"]`) {
		t.Fatalf("root tsconfig should include generated TypeScript tree:\n%s", tsconfigBytes)
	}
}

func TestTypeScriptClientPackageJsonNamesIncludeServiceName(t *testing.T) {
	depsDir := t.TempDir()
	writeTypeScriptTestDependencies(t, depsDir)

	cfg := &tactype.Config{
		TsPackageBase: "webapp-tacplus",
	}
	c2sCfg := &tactype.Config{
		TsPackageBase: "webapp-tacplus",
		SmPrefix:      "c2sm",
	}
	argus := ServiceInfo{
		PackageName: "argus",
		ParentDir:   "services",
		Config:      cfg,
	}
	gateway := ServiceInfo{
		PackageName: "gateway",
		ParentDir:   "services",
		Config:      cfg,
	}
	c2sArgus := argus
	c2sArgus.Config = c2sCfg
	c2sGateway := gateway
	c2sGateway.Config = c2sCfg

	c2sArgusDir := t.TempDir()
	c2sGatewayDir := t.TempDir()
	s2sArgusDir := t.TempDir()
	w2sArgusDir := t.TempDir()
	w2sGatewayDir := t.TempDir()
	restArgusDir := t.TempDir()
	restGatewayDir := t.TempDir()

	if err := generateGrpcClientPackageJson(c2sArgus, c2sArgusDir); err != nil {
		t.Fatalf("generateGrpcClientPackageJson argus: %v", err)
	}
	if err := generateGrpcClientPackageJson(c2sGateway, c2sGatewayDir); err != nil {
		t.Fatalf("generateGrpcClientPackageJson gateway: %v", err)
	}
	if err := generateGrpcClientPackageJson(argus, s2sArgusDir); err != nil {
		t.Fatalf("generateGrpcClientPackageJson s2s argus: %v", err)
	}
	if err := generateGrpcOverRestPackageJson(argus, w2sArgusDir); err != nil {
		t.Fatalf("generateGrpcOverRestPackageJson argus: %v", err)
	}
	if err := generateGrpcOverRestPackageJson(gateway, w2sGatewayDir); err != nil {
		t.Fatalf("generateGrpcOverRestPackageJson gateway: %v", err)
	}
	if err := generateRestClientPackageJson(argus, restArgusDir); err != nil {
		t.Fatalf("generateRestClientPackageJson argus: %v", err)
	}
	if err := generateRestClientPackageJson(gateway, restGatewayDir); err != nil {
		t.Fatalf("generateRestClientPackageJson gateway: %v", err)
	}

	seen := map[string]string{}
	for label, dir := range map[string]string{
		"c2s argus":    c2sArgusDir,
		"c2s gateway":  c2sGatewayDir,
		"s2s argus":    s2sArgusDir,
		"w2s argus":    w2sArgusDir,
		"w2s gateway":  w2sGatewayDir,
		"rest argus":   restArgusDir,
		"rest gateway": restGatewayDir,
	} {
		name := readPackageName(t, filepath.Join(dir, "package.json"))
		if prev, ok := seen[name]; ok {
			t.Fatalf("duplicate package name %q for %s and %s", name, prev, label)
		}
		seen[name] = label
	}

	for _, want := range []string{
		"webapp-tacplus-services-argus-c2sm-grpcclients",
		"webapp-tacplus-services-gateway-c2sm-grpcclients",
		"webapp-tacplus-services-argus-s2sm-grpcclients",
		"webapp-tacplus-services-argus-grpcoverrest",
		"webapp-tacplus-services-gateway-grpcoverrest",
		"webapp-tacplus-services-argus-restclients",
		"webapp-tacplus-services-gateway-restclients",
	} {
		if _, ok := seen[want]; !ok {
			t.Fatalf("package name %q missing from generated names %v", want, seen)
		}
	}
}

func TestTypeScriptFixImportPathsHandlesClientsAndVservices(t *testing.T) {
	outputDir := t.TempDir()

	files := map[string]string{
		"types/clients/myclient/myclient_pb.ts": `import { file_sys_spec } from "../sys/spec_pb.js";
import { file_sys_grpcconfig } from "../sys/grpcconfig_pb.js";
`,
		"types/vservices/claude-sensor/claude-sensor_pb.ts": `import { file_sys_spec } from "../sys/spec_pb.js";
import { file_sys_trace } from "../sys/trace_pb.js";
`,
		"types/services/argus/argus_pb.ts": `import { file_vservices_claude_sensor } from "../vservices/claude-sensor_pb.js";
`,
		"types/contract/contractclient/contractclient_pb.ts": `import { file_sys_spec } from "../sys/spec_pb.js";
`,
	}
	for relPath, content := range files {
		path := filepath.Join(outputDir, filepath.FromSlash(relPath))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", relPath, err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", relPath, err)
		}
	}

	if err := fixImportPaths(outputDir, nil); err != nil {
		t.Fatalf("fixImportPaths: %v", err)
	}

	for relPath, expected := range map[string][]string{
		"types/clients/myclient/myclient_pb.ts": {
			`from "../../sys/spec/spec_pb.js"`,
			`from "../../sys/grpcconfig/grpcconfig_pb.js"`,
		},
		"types/vservices/claude-sensor/claude-sensor_pb.ts": {
			`from "../../sys/spec/spec_pb.js"`,
			`from "../../sys/trace/trace_pb.js"`,
		},
		"types/services/argus/argus_pb.ts": {
			`from "../../vservices/claude-sensor/claude-sensor_pb.js"`,
		},
		"types/contract/contractclient/contractclient_pb.ts": {
			`from "../../sys/spec/spec_pb.js"`,
		},
	} {
		gotBytes, err := os.ReadFile(filepath.Join(outputDir, filepath.FromSlash(relPath)))
		if err != nil {
			t.Fatalf("read %s: %v", relPath, err)
		}
		got := string(gotBytes)
		for _, want := range expected {
			if !strings.Contains(got, want) {
				t.Fatalf("%s missing %q after import rewrite:\n%s", relPath, want, got)
			}
		}
	}
}

func TestTypeScriptFixImportPathsUsesGeneratedProtoHeaderForFlatServiceState(t *testing.T) {
	outputDir := t.TempDir()
	relPath := "types/services/argus/services/argus_pb.ts"
	path := filepath.Join(outputDir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`// @generated from file services/argus.proto (package argus, syntax proto3)
import { file_sys_spec } from "../sys/spec_pb.js";
import { file_vservices_claude_sensor } from "../vservices/claude-sensor_pb.js";
`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := fixImportPaths(outputDir, map[string]bool{
		"services/argus.proto":          true,
		"vservices/claude-sensor.proto": true,
	}); err != nil {
		t.Fatalf("fixImportPaths: %v", err)
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(gotBytes)
	for _, want := range []string{
		`from "../../../sys/spec/spec_pb.js"`,
		`from "../../claude-sensor/services/claude-sensor_pb.js"`,
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("import rewrite missing %q:\n%s", want, got)
		}
	}
}

func TestTypeScriptModuleDirUsesServiceStateAnnotation(t *testing.T) {
	stateFile := model.ProtoFile{
		RelPath: "services/demo/config.proto",
		Messages: []model.Message{{
			Name: "DemoState",
			Annotations: map[string]model.AnnotationValue{
				"service_state": {Type: model.AnnotationTypeString, StringValue: "DemoState"},
			},
		}},
	}
	if got, want := typescriptModuleDirForProtoFile("out", stateFile), filepath.Join("out", "types", "services", "demo", "services"); got != want {
		t.Fatalf("state source module dir = %q, want %q", got, want)
	}

	regularFile := stateFile
	regularFile.RelPath = "services/demo/config_types.proto"
	regularFile.Messages[0].Annotations = nil
	if got, want := typescriptModuleDirForProtoFile("out", regularFile), filepath.Join("out", "types", "services", "demo", "config_types"); got != want {
		t.Fatalf("regular module dir = %q, want %q", got, want)
	}
}

func TestTypeScriptHelpersUseServiceStateModuleRouting(t *testing.T) {
	outputDir := t.TempDir()
	pf := model.ProtoFile{
		RelPath: "services/argus.proto",
		Package: "argus",
		Imports: []model.Import{{
			Path:    "vservices/claude-sensor.proto",
			Package: "claudesensor",
		}},
		Messages: []model.Message{{
			Name: "GetDashboardRequest",
			Fields: []model.Field{{
				Name:        "range",
				Type:        "EventTimeRange",
				TypePackage: "claudesensor",
				IsMessage:   true,
			}},
		}, {
			Name: "ArgusState",
			Annotations: map[string]model.AnnotationValue{
				"service_state": {Type: model.AnnotationTypeString, StringValue: "ArgusState"},
			},
		}},
	}
	vpf := model.ProtoFile{
		RelPath: "vservices/claude-sensor.proto",
		Package: "claudesensor",
		Messages: []model.Message{{
			Name: "EventTimeRange",
		}, {
			Name: "AgenticCodeEventBus",
			Annotations: map[string]model.AnnotationValue{
				"service_state": {Type: model.AnnotationTypeString, StringValue: "AgenticCodeEventBus"},
			},
		}},
	}

	if err := generateTypeScriptHelpers(pf, outputDir, "services", []model.ProtoFile{pf, vpf}); err != nil {
		t.Fatalf("generateTypeScriptHelpers: %v", err)
	}

	wantPath := filepath.Join(outputDir, "typescript", "types", "services", "argus", "services", "helpers.ts")
	gotBytes, err := os.ReadFile(wantPath)
	if err != nil {
		t.Fatalf("read helpers at routed path: %v", err)
	}
	if got := string(gotBytes); !strings.Contains(got, "export function validateGetDashboardRequest") {
		t.Fatalf("helpers missing service message validator:\n%s", got)
	}
	if got := string(gotBytes); !strings.Contains(got, `from '../../../sys/errors/errors.js'`) {
		t.Fatalf("helpers missing routed errors import:\n%s", got)
	}
	if got := string(gotBytes); !strings.Contains(got, `from '../../claude-sensor/services/helpers.js'`) {
		t.Fatalf("helpers missing routed service-state cross import:\n%s", got)
	}

	wrongPath := filepath.Join(outputDir, "typescript", "types", "services", "argus", "helpers.ts")
	if _, err := os.Stat(wrongPath); !os.IsNotExist(err) {
		t.Fatalf("helpers should not be written to legacy flat path %s", wrongPath)
	}
}

func TestTypeScriptNpmPackageNameForProtoUsesServiceStateRouting(t *testing.T) {
	stateFiles := map[string]bool{"services/demo/config.proto": true}
	if got, want := typeScriptNpmPackageNameForProto("webapp-tacplus", "services/demo/config.proto", stateFiles), "webapp-tacplus-services-demo-services"; got != want {
		t.Fatalf("state source package name = %q, want %q", got, want)
	}
	if got, want := typeScriptNpmPackageNameForProto("webapp-tacplus", "services/demo/config.proto", nil), "webapp-tacplus-services-demo-config"; got != want {
		t.Fatalf("regular package name = %q, want %q", got, want)
	}
	if got, want := typeScriptNpmPackageNameForProto("webapp-tacplus", "sys/accounting.proto", nil), "webapp-tacplus-sys-accounting"; got != want {
		t.Fatalf("sys package name = %q, want %q", got, want)
	}
}

func TestTypeScriptFixImportPathsUsesServiceStateMap(t *testing.T) {
	outputDir := t.TempDir()
	relPath := "types/services/demo/types/types_pb.ts"
	path := filepath.Join(outputDir, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(`import { file_services_demo_config } from "./config_pb.js";
`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := fixImportPaths(outputDir, map[string]bool{"services/demo/config.proto": true}); err != nil {
		t.Fatalf("fixImportPaths: %v", err)
	}

	gotBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got := string(gotBytes)
	if want := `from "../services/config_pb.js"`; !strings.Contains(got, want) {
		t.Fatalf("import rewrite missing %q:\n%s", want, got)
	}
}

func TestTypeScriptModuleDirForPackageUsesGeneratedPackageHeader(t *testing.T) {
	outputDir := t.TempDir()

	files := map[string]string{
		"types/vservices/claude-sensor/claude-sensor_pb.ts": `// @generated from file vservices/claude-sensor.proto (package claudesensor, syntax proto3)
`,
		"types/services/gitlab-connector/gitlab-connector_pb.ts": `// @generated from file services/gitlab-connector.proto (package gitlabconnectorsvc, syntax proto3)
`,
		"types/sys/grpcconfig/grpcconfig_pb.ts": `// @generated from file sys/grpcconfig.proto (package grpcconfig, syntax proto3)
`,
	}
	for relPath, content := range files {
		path := filepath.Join(outputDir, filepath.FromSlash(relPath))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", relPath, err)
		}
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", relPath, err)
		}
	}

	for pkg, want := range map[string]string{
		"claudesensor":       "types/vservices/claude-sensor",
		"gitlabconnectorsvc": "types/services/gitlab-connector",
		"grpcconfig":         "types/sys/grpcconfig",
	} {
		got, ok := typescriptModuleDirForPackage(outputDir, pkg)
		if !ok {
			t.Fatalf("package %q was not resolved", pkg)
		}
		if got != want {
			t.Fatalf("package %q resolved to %q, want %q", pkg, got, want)
		}
	}
}

func TestCreateSpecPackagePreservesGeneratedHelpers(t *testing.T) {
	outputDir := t.TempDir()
	specDir := filepath.Join(outputDir, "types", "sys", "spec")
	if err := os.MkdirAll(specDir, 0755); err != nil {
		t.Fatalf("mkdir spec dir: %v", err)
	}
	helpersPath := filepath.Join(specDir, "helpers.ts")
	want := "export function typeKindToString() { return 'ok'; }\n"
	if err := os.WriteFile(helpersPath, []byte(want), 0644); err != nil {
		t.Fatalf("write helpers.ts: %v", err)
	}

	if err := createSpecPackage(outputDir); err != nil {
		t.Fatalf("createSpecPackage: %v", err)
	}

	gotBytes, err := os.ReadFile(helpersPath)
	if err != nil {
		t.Fatalf("read helpers.ts: %v", err)
	}
	if got := string(gotBytes); got != want {
		t.Fatalf("helpers.ts was overwritten:\n%s", got)
	}
}

func TestTypeScriptEnumValueNameMatchesBufPrefixRules(t *testing.T) {
	for _, tc := range []struct {
		name      string
		enumName  string
		values    []string
		valueName string
		want      string
	}{
		{
			name:      "strips ordinary enum type prefix",
			enumName:  "EventLevel",
			values:    []string{"EVENT_LEVEL_UNDEFINED", "EVENT_LEVEL_DEBUG"},
			valueName: "EVENT_LEVEL_DEBUG",
			want:      "DEBUG",
		},
		{
			name:      "keeps acronym prefix when buf cannot strip it",
			enumName:  "ALPNProtocol",
			values:    []string{"ALPN_PROTOCOL_UNSPECIFIED", "ALPN_PROTOCOL_H2"},
			valueName: "ALPN_PROTOCOL_H2",
			want:      "ALPN_PROTOCOL_H2",
		},
		{
			name:      "keeps prefix when stripped value would start with digit",
			enumName:  "HttpStatus",
			values:    []string{"HTTP_STATUS_200"},
			valueName: "HTTP_STATUS_200",
			want:      "HTTP_STATUS_200",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			values := make([]model.EnumValue, 0, len(tc.values))
			for i, name := range tc.values {
				values = append(values, model.EnumValue{Name: name, Number: i})
			}
			got := typescriptEnumValueName(model.Enum{Name: tc.enumName, Values: values}, tc.valueName)
			if got != tc.want {
				t.Fatalf("typescriptEnumValueName(%s, %s) = %q, want %q", tc.enumName, tc.valueName, got, tc.want)
			}
		})
	}
}

func readPackageName(t *testing.T, path string) string {
	t.Helper()
	var pkg struct {
		Name string `json:"name"`
	}
	pkgBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read package.json: %v", err)
	}
	if err := json.Unmarshal(pkgBytes, &pkg); err != nil {
		t.Fatalf("parse package.json: %v", err)
	}
	return pkg.Name
}

func containsString(values []string, want string) bool {
	for _, got := range values {
		if got == want {
			return true
		}
	}
	return false
}

// TestRepointPackageEntrypoints is the OF-29 regression: every workspace
// package's main/types must point at the package's compiled output inside the
// single rooted dist/ tree, regardless of which shape the producer emitted, and
// any source-pointing exports must be dropped so node resolves by-name imports
// through main at runtime (previously ERR_MODULE_NOT_FOUND).
func TestRepointPackageEntrypoints(t *testing.T) {
	outputDir := t.TempDir()

	// A lib-template-shaped package: main at a bogus per-package dist.
	libDir := filepath.Join(outputDir, "sys", "spiffe")
	mustMkdir(t, libDir)
	mustWrite(t, filepath.Join(libDir, "package.json"), `{
  "name": "x-model-sys-spiffe",
  "version": "1.0.0",
  "main": "dist/index.js",
  "types": "dist/index.d.ts",
  "type": "module"
}`)

	// A createPackageJson-shaped package: source main + source-pointing exports.
	deepDir := filepath.Join(outputDir, "types", "generated", "services")
	mustMkdir(t, deepDir)
	mustWrite(t, filepath.Join(deepDir, "package.json"), `{
  "name": "x-model-types-generated-services",
  "main": "index.ts",
  "exports": { ".": "./index.ts", "./*": "./*" },
  "dependencies": { "x-model-sys-spiffe": "*" }
}`)

	// Root package.json must be left untouched.
	mustWrite(t, filepath.Join(outputDir, "package.json"), `{"name":"root","workspaces":["sys/*"]}`)

	cfg := &tactype.Config{OutputDir: outputDir}
	if err := repointPackageEntrypoints(cfg); err != nil {
		t.Fatalf("repointPackageEntrypoints: %v", err)
	}

	type pj struct {
		Main    string          `json:"main"`
		Types   string          `json:"types"`
		Exports json.RawMessage `json:"exports"`
		Deps    map[string]any  `json:"dependencies"`
	}
	read := func(rel string) pj {
		var p pj
		b, err := os.ReadFile(filepath.Join(outputDir, rel, "package.json"))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		if err := json.Unmarshal(b, &p); err != nil {
			t.Fatalf("parse %s: %v", rel, err)
		}
		return p
	}

	lib := read("sys/spiffe")
	if lib.Main != "../../dist/sys/spiffe/index.js" {
		t.Errorf("sys/spiffe main = %q, want ../../dist/sys/spiffe/index.js", lib.Main)
	}
	if lib.Types != "../../dist/sys/spiffe/index.d.ts" {
		t.Errorf("sys/spiffe types = %q", lib.Types)
	}

	deep := read("types/generated/services")
	if deep.Main != "../../../dist/types/generated/services/index.js" {
		t.Errorf("services main = %q, want ../../../dist/types/generated/services/index.js", deep.Main)
	}
	if deep.Exports != nil {
		t.Errorf("source-pointing exports must be removed, got %s", deep.Exports)
	}
	if _, ok := deep.Deps["x-model-sys-spiffe"]; !ok {
		t.Errorf("dependencies must be preserved verbatim, got %v", deep.Deps)
	}

	// Root must be untouched (no main rewrite).
	rootBytes, _ := os.ReadFile(filepath.Join(outputDir, "package.json"))
	if strings.Contains(string(rootBytes), "dist/") {
		t.Errorf("root package.json must not be repointed: %s", rootBytes)
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// NEW-33 pin: a client-only target with a non-empty subscription set must FAIL
// LOUDLY — the runtime SM its subscribers need is service-side and client
// builds do not emit it. A silent revert to generating the broken-import
// branch must not survive this test.
func TestTypescriptClientOnlySubscriptionsFailLoudly(t *testing.T) {
	src, err := os.ReadFile("generator.go")
	if err != nil {
		t.Fatalf("read generator.go: %v", err)
	}
	for _, want := range []string{
		`if len(config.Subscriptions) > 0 {`,
		`return fmt.Errorf("typescript is a client-only target; event-bus subscriptions require the service-runtime ServiceManager`,
	} {
		if !strings.Contains(string(src), want) {
			t.Fatalf("typescript generator must fail loudly on client-only subscriptions (NEW-33); missing %q", want)
		}
	}
}
