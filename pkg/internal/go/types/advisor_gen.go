// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"github.com/tacplus-dev/tacplus/templates"
)

// The LLM rule as a generated gate (tacplus#909), in Go. A service whose
// tacplus.yaml declares an advisor gets, in its generated tree:
//
//   - advisor/: the model port package. ModelPort is typed on the model
//     service's own messages, which it aliases (the messages only, never the
//     raw transport stubs beside them in the types package); and
//     llm_rule_test.go fails when the advisor's packages could reach anything
//     of the service but this package.
//   - <sm>/advisor_port.go: ModelPortAdapter, the core's implementation of the
//     port over the generated S2S client.
//
// The core builds the adapter and passes it in; the advisor holds no client and
// no token, and the core makes every call that carries the service's token.

// goAdvisorPortDir is the port package's directory in the generated tree.
const goAdvisorPortDir = "advisor"

type goAdvisorMethod struct {
	Name   string // the rpc and the client method, e.g. "GenerateCompletionAsService"
	Input  string // its request message, a name in the port package
	Output string // its response message
}

type goAdvisorData struct {
	ServiceLabel   string
	AdvisorPath    string // as tacplus.yaml names it, for the test's comment
	AdvisorRel     string // the advisor's directory, from the port package's
	ModelService   string
	ProtoPackage   string
	ProtoService   string
	TypesImport    string
	PortImport     string
	GroupAccessor  string // sm.Grpc().Clients().<Group>()
	ClientAccessor string // .<Client>()
	Methods        []goAdvisorMethod
	WireTypes      []string // the port's aliases of the model service's types
	WireConsts     []string // and of its enum values
}

// generateGoAdvisorPort writes the advisor's model port package, its gate and
// the core's adapter, after the S2S clients (whose accessors the adapter calls).
func generateGoAdvisorPort(config *tactype.Config, clientServices []ServiceInfo, clients []generatedGrpcClient) error {
	if config.AdvisorDir == "" {
		return nil
	}
	data, err := buildGoAdvisorData(config, clientServices, clients)
	if err != nil {
		return err
	}
	portDir := filepath.Join(config.OutputDir, goAdvisorPortDir)
	if err := os.MkdirAll(portDir, 0o755); err != nil {
		return fmt.Errorf("advisor: %w", err)
	}
	for _, f := range []struct{ tmpl, path string }{
		{"port.go.tmpl", filepath.Join(portDir, "port.go")},
		{"llm_rule_test.go.tmpl", filepath.Join(portDir, "llm_rule_test.go")},
		{"adapter.go.tmpl", filepath.Join(config.OutputDir, config.GetSmPrefix(), "advisor_port.go")},
	} {
		text, err := templates.FS.ReadFile("go/advisor/" + f.tmpl)
		if err != nil {
			return fmt.Errorf("advisor: %w", err)
		}
		tmpl, err := template.New(f.tmpl).Funcs(header.Funcs()).Parse(string(text))
		if err != nil {
			return fmt.Errorf("advisor: %s: %w", f.tmpl, err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			return fmt.Errorf("advisor: %s: %w", f.tmpl, err)
		}
		out, err := format.Source(buf.Bytes())
		if err != nil {
			return fmt.Errorf("advisor: %s does not format: %w", f.tmpl, err)
		}
		if err := os.WriteFile(f.path, out, 0o644); err != nil {
			return fmt.Errorf("advisor: %w", err)
		}
	}
	return nil
}

func buildGoAdvisorData(config *tactype.Config, clientServices []ServiceInfo, clients []generatedGrpcClient) (goAdvisorData, error) {
	refuse := fmt.Errorf("tacplus.yaml advisor: the advisor reaches its model only through %s, and %s has no S2S client to it: add %s to depends and declare its service with (spec.grpc_s2s_client)", consts.AdvisorModelService, config.ServiceLabel(), consts.AdvisorModelService)
	if config.GetSmPrefix() != "s2sm" {
		return goAdvisorData{}, refuse
	}
	dp, err := parser.NewDescriptorParser(config.AnnotatedProtoDir)
	if err != nil {
		return goAdvisorData{}, fmt.Errorf("advisor: %w", err)
	}
	// The model service is the one whose file declares (spec.service).name
	// consts.AdvisorModelService: its registry identity, whatever its package.
	var model *ServiceInfo
	var fd *parser.FileDescriptor
	for i, svc := range clientServices {
		svcFd, err := dp.GetFileDescriptor(svc.ProtoFile)
		if err != nil || svcFd.ServiceName != consts.AdvisorModelService {
			continue
		}
		if model != nil {
			return goAdvisorData{}, fmt.Errorf("tacplus.yaml advisor: %s declares more than one S2S client to %s (%s.%s, %s.%s); the advisor's model port is typed on one", config.ServiceLabel(), consts.AdvisorModelService, model.PackageName, model.ServiceName, svc.PackageName, svc.ServiceName)
		}
		model, fd = &clientServices[i], svcFd
	}
	if model == nil {
		return goAdvisorData{}, refuse
	}
	var client *generatedGrpcClient
	for i, c := range clients {
		if c.PackageName == model.PackageName && c.ServiceName == model.ServiceName {
			client = &clients[i]
		}
	}
	if client == nil {
		return goAdvisorData{}, refuse
	}
	info, err := dp.ConvertToProtoInfo(model.ProtoFile)
	if err != nil {
		return goAdvisorData{}, fmt.Errorf("advisor: parse %s: %w", model.ProtoFile, err)
	}
	var methods []goAdvisorMethod
	for _, svc := range info.Services {
		if svc.Name != model.ServiceName {
			continue
		}
		for _, m := range svc.Methods {
			// The port is narrow: the plain unary rpcs on the model service's own
			// messages. A stream, a collection read or a foreign message has no
			// place on it.
			if m.ClientStreaming || m.ServerStreaming || m.IsPageCollection() || m.IsTier2SnapshotCollection() {
				continue
			}
			if !goInPackage(m.FullInputType, info.Package) || !goInPackage(m.FullOutputType, info.Package) {
				continue
			}
			methods = append(methods, goAdvisorMethod{Name: m.Name, Input: goCamelCase(m.InputType), Output: goCamelCase(m.OutputType)})
		}
	}
	if len(methods) == 0 {
		return goAdvisorData{}, fmt.Errorf("tacplus.yaml advisor: %s.%s has no unary rpc on its own messages for the advisor's model port", model.PackageName, model.ServiceName)
	}

	// The port aliases the model service's messages and enums, the wrapper
	// types of their oneofs and their enums' values; nothing else of the types
	// package, whose <Service>Client and server are raw transport stubs.
	var types, enumValues []string
	for _, msg := range fd.Messages {
		name := goCamelCase(msg.Name)
		types = append(types, name)
		for _, f := range msg.Fields {
			if f.IsOneOf {
				types = append(types, name+"_"+goCamelCase(f.Name))
			}
		}
	}
	for _, e := range fd.Enums {
		name := goCamelCase(e.Name)
		types = append(types, name)
		for _, v := range e.Values {
			enumValues = append(enumValues, name+"_"+v.Name)
		}
	}
	sort.Strings(types)
	sort.Strings(enumValues)

	portDir := filepath.Join(config.OutputDir, goAdvisorPortDir)
	rel, err := filepath.Rel(portDir, config.AdvisorDir)
	if err != nil {
		return goAdvisorData{}, fmt.Errorf("advisor: %w", err)
	}
	advisorPath, err := filepath.Rel(config.ProjectDir, config.AdvisorDir)
	if err != nil {
		advisorPath = config.AdvisorDir
	}
	return goAdvisorData{
		ServiceLabel:   config.ServiceLabel(),
		AdvisorPath:    filepath.ToSlash(advisorPath),
		AdvisorRel:     filepath.ToSlash(rel),
		ModelService:   consts.AdvisorModelService,
		ProtoPackage:   info.Package,
		ProtoService:   model.PackageName + "." + model.ServiceName,
		TypesImport:    client.TypesImportPath,
		PortImport:     config.ModuleBasePath + "/" + goAdvisorPortDir,
		GroupAccessor:  goAccessorName(client.ParentDir),
		ClientAccessor: client.ClientName,
		Methods:        methods,
		WireTypes:      types,
		WireConsts:     enumValues,
	}, nil
}

func goInPackage(fullType, pkg string) bool {
	i := strings.LastIndex(fullType, ".")
	return i > 0 && fullType[:i] == pkg
}
