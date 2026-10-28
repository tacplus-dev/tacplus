// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

func TestW2SClientTemplateRendersAllTransports(t *testing.T) {
	svc := &parser.ServiceInfo{Name: "DemoService"}
	methods := []*parser.MethodInfo{
		{
			Name:           "GetThing",
			InputType:      "GetThingRequest",
			OutputType:     "GetThingResponse",
			FullInputType:  "demo.GetThingRequest",
			FullOutputType: "demo.GetThingResponse",
			HttpMethod:     "POST",
			HttpPath:       "/rpc/demo/GetThing",
		},
		{
			Name:            "WatchThings",
			InputType:       "WatchThingsRequest",
			OutputType:      "WatchThingsResponse",
			FullInputType:   "demo.WatchThingsRequest",
			FullOutputType:  "demo.WatchThingsResponse",
			ServerStreaming: true,
			HttpMethod:      "GET",
			HttpPath:        "/rpc/demo/WatchThings",
			AuthDisabled:    true,
		},
		{
			Name:            "UploadThings",
			InputType:       "UploadThingsRequest",
			OutputType:      "UploadThingsResponse",
			FullInputType:   "demo.UploadThingsRequest",
			FullOutputType:  "demo.UploadThingsResponse",
			ClientStreaming: true,
			HttpPath:        "/rpc/demo/UploadThings",
		},
		{
			Name:            "ChatThings",
			InputType:       "ChatThingsRequest",
			OutputType:      "ChatThingsResponse",
			FullInputType:   "demo.ChatThingsRequest",
			FullOutputType:  "demo.ChatThingsResponse",
			ClientStreaming: true,
			ServerStreaming: true,
			HttpPath:        "/rpc/demo/ChatThings",
		},
	}

	var renderedMethods strings.Builder
	var extraTypes strings.Builder
	for _, method := range methods {
		result := generateGoW2SClientMethod("DemoServiceW2SClient", svc, method, "demo", !method.AuthDisabled)
		renderedMethods.WriteString(result.method)
		renderedMethods.WriteString("\n")
		extraTypes.WriteString(result.extraType)
		extraTypes.WriteString("\n")
	}

	tmplStr, err := loadTemplate("w2sclient.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate: %v", err)
	}
	tmpl, err := template.New("w2sclient").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	data := struct {
		Header                      string
		PackageName                 string
		TypesModulePath             string
		HasTypesImport              bool
		CrossPackageImports         string
		ServiceName                 string
		ClientName                  string
		Methods                     string
		ExtraTypes                  string
		HasHTTP                     bool
		HasServerStreaming          bool
		HasClientStreaming          bool
		HasBidi                     bool
		HasWebSocket                bool
		HasGRPCStreaming            bool
		HasSync                     bool
		HasRoute                    bool
		ServiceConnectionImportPath string
	}{
		Header:             header.GeneratedHeader(consts.LangGo, "demo.proto"),
		PackageName:        "demo",
		TypesModulePath:    "example.com/project/types/services/demo",
		HasTypesImport:     true,
		ServiceName:        "DemoService",
		ClientName:         "DemoServiceW2SClient",
		Methods:            renderedMethods.String(),
		ExtraTypes:         extraTypes.String(),
		HasHTTP:            true,
		HasServerStreaming: true,
		HasClientStreaming: true,
		HasBidi:            true,
		HasWebSocket:       true,
		HasGRPCStreaming:   true,
		HasSync:            true,
	}

	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute: %v", err)
	}
	src, err := format.Source([]byte(out.String()))
	if err != nil {
		t.Fatalf("format rendered W2S client: %v\n%s", err, out.String())
	}
	text := string(src)
	for _, want := range []string{
		"type DemoServiceW2SClient struct",
		"func (c *DemoServiceW2SClient) GetThing(",
		"func (c *DemoServiceW2SClient) WatchThings(",
		"func (c *DemoServiceW2SClient) UploadThings(",
		"func (c *DemoServiceW2SClient) ChatThings(",
		"streamW2SSSE(c, ctx, \"POST\", \"/rpc/demo/WatchThings\", body, false",
		"c.sendStartFrame(ctx, ws, true)",
		"type ChatThingsW2SHandle struct",
		"func (h *ChatThingsW2SHandle) Send(msg *types.ChatThingsRequest) error",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("rendered W2S client missing %q", want)
		}
	}
}

func TestGenerateGoW2SClientUsesConcreteClientReceiver(t *testing.T) {
	tempDir := t.TempDir()
	config := &tactype.Config{
		OutputDir:      tempDir,
		ModuleBasePath: "example.com/project/tacplus",
		SmPrefix:       "w2sm",
	}
	svc := ServiceInfo{
		ProtoFile:   "services/demo.proto",
		PackageName: "demo",
		DirName:     "demo",
		ServiceName: "DemoService",
		ParentDir:   "services",
		Config:      config,
	}
	info := &parser.ProtoInfo{
		Package:           "demo",
		CrossPackageTypes: map[string]string{},
		Services: []*parser.ServiceInfo{{
			Name: "DemoService",
			Methods: []*parser.MethodInfo{{
				Name:           "GetThing",
				InputType:      "GetThingRequest",
				OutputType:     "GetThingResponse",
				FullInputType:  "demo.GetThingRequest",
				FullOutputType: "demo.GetThingResponse",
				HttpMethod:     "POST",
				HttpPath:       "/rpc/demo/GetThing",
			}},
		}},
	}

	if err := generateGoW2SClient(svc, info); err != nil {
		t.Fatalf("generateGoW2SClient: %v", err)
	}

	outputPath := filepath.Join(tempDir, "w2sm", "internal", "grpc_over_rest", "services", "demo", "service.go")
	out, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("read generated client: %v", err)
	}
	text := string(out)
	if !strings.Contains(text, "type DemoServiceW2SClient struct") {
		t.Fatalf("generated client missing concrete W2S client type:\n%s", text)
	}
	if !strings.Contains(text, "func (c *DemoServiceW2SClient) GetThing(") {
		t.Fatalf("generated method does not use concrete W2S client receiver:\n%s", text)
	}
	if strings.Contains(text, "func (c *DemoService) GetThing(") {
		t.Fatalf("generated method used service receiver instead of W2S client receiver:\n%s", text)
	}
}

func TestW2SRootTemplatesRender(t *testing.T) {
	tmplStr, err := loadTemplate("w2s_clients.go.tmpl")
	if err != nil {
		t.Fatalf("loadTemplate w2s clients: %v", err)
	}
	tmpl, err := template.New("w2s_clients").Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		t.Fatalf("parse w2s clients: %v", err)
	}
	data := struct {
		Header  string
		Imports []goW2SImport
		Targets []goW2STargetEntry
	}{
		Header: header.GeneratedHeader(consts.LangGo, "w2s_clients"),
		Imports: []goW2SImport{{
			Alias: "services_demo_w2s",
			Path:  "example.com/project/w2sm/internal/grpc_over_rest/services/demo",
		}},
		Targets: []goW2STargetEntry{{
			Target:       "demo-host",
			StructName:   "DemoHostW2SClients",
			FieldName:    "demoHostW2SClients",
			AccessorName: "DemoHost",
			ConstName:    "TargetDemoHost",
			Services: []goW2SServiceEntry{{
				FieldName:        "demoServiceW2SClient",
				AccessorName:     "DemoServiceW2SClient",
				ClientName:       "DemoServiceW2SClient",
				ImportAlias:      "services_demo_w2s",
				ParentStructName: "DemoHostW2SClients",
			}},
		}},
	}
	var out strings.Builder
	if err := tmpl.Execute(&out, data); err != nil {
		t.Fatalf("execute w2s clients: %v", err)
	}
	if _, err := format.Source([]byte(out.String())); err != nil {
		t.Fatalf("format rendered w2s clients: %v\n%s", err, out.String())
	}
}

// crewW2SMethod is crewMethod (tenant_route_test.go) with the REST route a W2S
// client method needs.
func crewW2SMethod(name string, clientStreaming, serverStreaming bool) *parser.MethodInfo {
	m := crewMethod(name, clientStreaming, serverStreaming)
	m.HttpMethod = "POST"
	m.HttpPath = "/rpc/crew/" + name
	return m
}

// Issue #170: a client-only target refuses an empty or malformed route field
// before sending. Every W2S shape of a routed rpc checks the field after
// request validation and before the request is encoded; the streaming shapes
// check every message.
func TestGoW2SClientRoutedMethodsRefuseBadRouteField(t *testing.T) {
	const reqCheck = `serviceconnection.RouteTenant("target_tenant", req.GetTargetTenant())`
	for _, tc := range []struct {
		method *parser.MethodInfo
		check  string
		before string // the send the check must precede
	}{
		{crewW2SMethod("Hail", false, false), reqCheck, "encodeW2SProto(req)"},
		{crewW2SMethod("Watch", false, true), reqCheck, "encodeW2SProto(req)"},
		{crewW2SMethod("Upload", true, false), reqCheck, "return encodeW2SProto(req)"},
		{crewW2SMethod("Chat", true, true), `serviceconnection.RouteTenant("target_tenant", msg.GetTargetTenant())`, "encodeW2SProto(msg)"},
	} {
		res := generateGoW2SClientMethod("CrewW2SClient", &parser.ServiceInfo{Name: "Crew"}, tc.method, "crew", true)
		src := res.method + res.extraType
		validate := strings.Index(src, "ValidateHailRequest(")
		check := strings.Index(src, tc.check)
		send := strings.Index(src, tc.before)
		if validate < 0 || check < 0 || send < 0 || !(validate < check && check < send) {
			t.Fatalf("%s: routed W2S client must validate, check the route field, then encode (validate=%d check=%d send=%d):\n%s",
				tc.method.Name, validate, check, send, src)
		}
		if tc.method.ClientStreaming && tc.method.ServerStreaming {
			// The bidi handle's Send and the onConnect send both check.
			if n := strings.Count(src, tc.check); n != 2 {
				t.Fatalf("%s: every bidi send path must check the route field, got %d checks:\n%s", tc.method.Name, n, src)
			}
		}

		platform := crewW2SMethod(tc.method.Name, tc.method.ClientStreaming, tc.method.ServerStreaming)
		platform.RouteField = nil
		res = generateGoW2SClientMethod("CrewW2SClient", &parser.ServiceInfo{Name: "Crew"}, platform, "crew", true)
		if src := res.method + res.extraType; strings.Contains(src, "serviceconnection.") {
			t.Fatalf("%s: a platform W2S client method must not check a route field:\n%s", platform.Name, src)
		}
	}
}

// The W2S client file imports the serviceconnection chassis only when some
// rpc routes by tenant.
func TestGenerateGoW2SClientImportsServiceConnectionOnlyWhenRouted(t *testing.T) {
	for _, routed := range []bool{true, false} {
		tempDir := t.TempDir()
		svc := ServiceInfo{
			ProtoFile:   "services/crew.proto",
			PackageName: "crew",
			DirName:     "crew",
			ServiceName: "Crew",
			ParentDir:   "services",
			Config:      &tactype.Config{OutputDir: tempDir, ModuleBasePath: "example.com/project/tacplus", SmPrefix: "w2sm"},
		}
		var methods []*parser.MethodInfo
		for _, m := range []*parser.MethodInfo{crewW2SMethod("Hail", false, false), crewW2SMethod("Chat", true, true)} {
			if !routed {
				m.RouteField = nil
			}
			methods = append(methods, m)
		}
		info := &parser.ProtoInfo{Package: "crew", CrossPackageTypes: map[string]string{},
			Services: []*parser.ServiceInfo{{Name: "Crew", Methods: methods}}}
		if err := generateGoW2SClient(svc, info); err != nil {
			t.Fatalf("generateGoW2SClient(routed=%v): %v", routed, err)
		}
		out, err := os.ReadFile(filepath.Join(tempDir, "w2sm", "internal", "grpc_over_rest", "services", "crew", "crew.go"))
		if err != nil {
			t.Fatal(err)
		}
		imported := strings.Contains(string(out), `"example.com/project/tacplus/chassis/serviceconnection"`)
		if imported != routed {
			t.Fatalf("routed=%v: serviceconnection imported=%v:\n%s", routed, imported, out)
		}
	}
}
