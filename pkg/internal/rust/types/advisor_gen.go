// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/tmplutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"github.com/tacplus-dev/tacplus/templates"
)

// The LLM rule as a generated build gate (tacplus#909). A service whose
// tacplus.yaml declares an advisor gets, in its generated workspace:
//
//   - advisor/: the model port crate. Its ModelPort trait is typed on the model
//     service's own messages, which it re-exports (the messages only, never the
//     raw transport stubs beside them in the types crate); and its build script
//     fails the build when the advisor's crate depends on anything of the
//     service but this crate.
//   - advisor_port.rs in the model crate: ModelPortAdapter, the core's
//     implementation of the port over the generated S2S client.
//
// The core builds the adapter and passes it in; the advisor holds no client and
// no token, and the core makes every call that carries the service's token.

// advisorPortDir is the port crate's directory in the generated workspace.
const advisorPortDir = "advisor"

type advisorMethod struct {
	Name   string // the rpc, e.g. "GenerateCompletionAsService"
	Snake  string // its tonic method, e.g. "generate_completion_as_service"
	Input  string // its request message, a name in wire::
	Output string // its response message, a name in wire::
}

type advisorData struct {
	ServiceLabel    string
	AdvisorPath     string // as tacplus.yaml names it, for the build script's comment
	AdvisorRel      string // the advisor's directory, from the port crate's
	CrateName       string // the model crate, e.g. "skipper-model"
	CrateIdent      string // the same, as a Rust path segment
	PortCrate       string // e.g. "skipper-model-advisor"
	PortCrateIdent  string
	TypesCrateIdent string
	TypesPath       string // the model service's module in the types crate, e.g. "services::llmproxy"
	ModelService    string // the model service's registry name, consts.AdvisorModelService
	ProtoPackage    string
	ProtoService    string // e.g. "llmproxy.LlmProxy"
	TargetAccessor  string // sm.s2s().<target>()
	ClientAccessor  string // .<client>()
	SmPrefix        string
	Methods         []advisorMethod
	Wire            []string // what wire:: re-exports
	Forbidden       []string // the generated crates the advisor may not depend on
	License         string   // SPDX id of the configured header; the manifest omits license when empty
}

// advisorPortCrate is the port crate's package name for a model crate.
func advisorPortCrate(crateName string) string {
	return crateName + "-advisor"
}

// generateRustAdvisorPort writes the advisor's model port crate and the core's
// adapter. It runs after the chassis crates are written (it forbids each of
// them) and before the workspace Cargo.toml (which lists the port crate).
func generateRustAdvisorPort(config *tactype.Config, clientServices []ServiceInfo) error {
	if config.AdvisorDir == "" {
		return nil
	}
	data, err := buildRustAdvisorData(config, clientServices)
	if err != nil {
		return err
	}
	typeFS, err := fs.Sub(templates.FS, "rust/advisor")
	if err != nil {
		return fmt.Errorf("advisor templates: %w", err)
	}
	portDir := filepath.Join(config.OutputDir, advisorPortDir)
	if err := os.MkdirAll(portDir, 0o755); err != nil {
		return fmt.Errorf("advisor: %w", err)
	}
	for _, f := range []struct{ tmpl, path string }{
		{"Cargo.toml.tmpl", filepath.Join(portDir, "Cargo.toml")},
		{"lib.rs.tmpl", filepath.Join(portDir, "lib.rs")},
		{"build.rs.tmpl", filepath.Join(portDir, "build.rs")},
		{"adapter.rs.tmpl", filepath.Join(config.OutputDir, "advisor_port.rs")},
	} {
		out, err := tmplutil.ExecuteTemplate(typeFS, f.tmpl, tmplutil.BaseFuncMap(), data)
		if err != nil {
			return fmt.Errorf("advisor: %s: %w", f.tmpl, err)
		}
		if err := os.WriteFile(f.path, out, 0o644); err != nil {
			return fmt.Errorf("advisor: %w", err)
		}
	}
	return declareLibModule(config.OutputDir, "advisor_port")
}

// declareLibModule declares module name in the model crate's lib.rs, unless it
// already does.
func declareLibModule(outputDir, name string) error {
	libPath := filepath.Join(outputDir, "lib.rs")
	content, err := os.ReadFile(libPath)
	if err != nil {
		return fmt.Errorf("advisor: %w", err)
	}
	decl := "pub mod " + name + ";"
	for _, line := range strings.Split(string(content), "\n") {
		if strings.TrimSpace(line) == decl {
			return nil
		}
	}
	if len(content) > 0 && !strings.HasSuffix(string(content), "\n") {
		content = append(content, '\n')
	}
	return os.WriteFile(libPath, append(content, decl+"\n"...), 0o644)
}

func buildRustAdvisorData(config *tactype.Config, clientServices []ServiceInfo) (advisorData, error) {
	crateName := strings.ReplaceAll(config.CrateBasePath, ".", "-")
	ident := func(name string) string { return strings.ReplaceAll(name, "-", "_") }

	nameCount := make(map[string]int, len(clientServices))
	for _, svc := range clientServices {
		nameCount[svc.ServiceName]++
	}
	dp, err := parser.NewDescriptorParser(config.AnnotatedProtoDir)
	if err != nil {
		return advisorData{}, fmt.Errorf("advisor: %w", err)
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
			return advisorData{}, fmt.Errorf("tacplus.yaml advisor: %s declares more than one S2S client to %s (%s.%s, %s.%s); the advisor's model port is typed on one", config.ServiceLabel(), consts.AdvisorModelService, model.PackageName, model.ServiceName, svc.PackageName, svc.ServiceName)
		}
		model, fd = &clientServices[i], svcFd
	}
	if model == nil || config.GetSmPrefix() != "s2sm" {
		return advisorData{}, fmt.Errorf("tacplus.yaml advisor: the advisor reaches its model only through %s, and %s has no S2S client to it: add %s to depends and declare its service with (spec.grpc_s2s_client)", consts.AdvisorModelService, config.ServiceLabel(), consts.AdvisorModelService)
	}
	info, err := dp.ConvertToProtoInfo(model.ProtoFile)
	if err != nil {
		return advisorData{}, fmt.Errorf("advisor: parse %s: %w", model.ProtoFile, err)
	}
	var methods []advisorMethod
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
			if !inPackage(m.FullInputType, info.Package) || !inPackage(m.FullOutputType, info.Package) {
				continue
			}
			methods = append(methods, advisorMethod{
				Name:   m.Name,
				Snake:  prostSnakeName(m.Name),
				Input:  toProstTypeName(m.InputType),
				Output: toProstTypeName(m.OutputType),
			})
		}
	}
	if len(methods) == 0 {
		return advisorData{}, fmt.Errorf("tacplus.yaml advisor: %s.%s has no unary rpc on its own messages for the advisor's model port", model.PackageName, model.ServiceName)
	}

	// wire:: re-exports the model service's messages and enums, and the module
	// prost gives a message for its oneofs; nothing else of the types module,
	// whose <service>_client and _server modules are raw transport stubs.
	var wire []string
	for _, msg := range fd.Messages {
		wire = append(wire, toProstTypeName(msg.Name))
		for _, f := range msg.Fields {
			if f.IsOneOf {
				wire = append(wire, prostSnakeName(msg.Name))
				break
			}
		}
	}
	for _, e := range fd.Enums {
		wire = append(wire, toProstTypeName(e.Name))
	}
	sort.Strings(wire)

	forbidden, err := advisorForbiddenCrates(config.OutputDir, crateName)
	if err != nil {
		return advisorData{}, err
	}
	portDir := filepath.Join(config.OutputDir, advisorPortDir)
	rel, err := filepath.Rel(portDir, config.AdvisorDir)
	if err != nil {
		return advisorData{}, fmt.Errorf("advisor: %w", err)
	}
	advisorPath, err := filepath.Rel(config.ProjectDir, config.AdvisorDir)
	if err != nil {
		advisorPath = config.AdvisorDir
	}
	clientField, _ := grpcClientNames(*model, nameCount)
	return advisorData{
		License:         header.License(),
		ServiceLabel:    config.ServiceLabel(),
		AdvisorPath:     filepath.ToSlash(advisorPath),
		AdvisorRel:      filepath.ToSlash(rel),
		CrateName:       crateName,
		CrateIdent:      ident(crateName),
		PortCrate:       advisorPortCrate(crateName),
		PortCrateIdent:  ident(advisorPortCrate(crateName)),
		TypesCrateIdent: ident(crateName + "-types"),
		TypesPath:       strings.ReplaceAll(strings.ReplaceAll(model.ParentDir, "-", "_"), string(filepath.Separator), "::") + "::" + model.PackageName,
		ModelService:    consts.AdvisorModelService,
		ProtoPackage:    info.Package,
		ProtoService:    model.PackageName + "." + model.ServiceName,
		TargetAccessor:  strutil.ToSnakeCase(strings.ReplaceAll(resolveMeshTarget(*model), "-", "_")),
		ClientAccessor:  clientField,
		SmPrefix:        config.GetSmPrefix(),
		Methods:         methods,
		Wire:            wire,
		Forbidden:       forbidden,
	}, nil
}

func inPackage(fullType, pkg string) bool {
	i := strings.LastIndex(fullType, ".")
	return i > 0 && fullType[:i] == pkg
}

// advisorForbiddenCrates names every crate of the generated tree but the port:
// the model crate, its types crate and each chassis crate, by the package name
// its own manifest declares.
func advisorForbiddenCrates(outputDir, crateName string) ([]string, error) {
	forbidden := []string{crateName, crateName + "-types"}
	entries, err := os.ReadDir(filepath.Join(outputDir, "chassis"))
	if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("advisor: %w", err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		manifest := filepath.Join(outputDir, "chassis", e.Name(), "Cargo.toml")
		if _, err := os.Stat(manifest); err != nil {
			continue
		}
		var m struct {
			Package struct {
				Name string `toml:"name"`
			} `toml:"package"`
		}
		if _, err := toml.DecodeFile(manifest, &m); err != nil {
			return nil, fmt.Errorf("advisor: read %s: %w", manifest, err)
		}
		if m.Package.Name == "" {
			return nil, fmt.Errorf("advisor: %s names no package", manifest)
		}
		forbidden = append(forbidden, m.Package.Name)
	}
	sort.Strings(forbidden)
	return forbidden, nil
}
