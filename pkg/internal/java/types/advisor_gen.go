// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"fmt"
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

// The LLM rule as a generated gate (tacplus#909), in Java. A service whose
// tacplus.yaml declares an advisor (a package directory of the service's own
// sources, src/main/java/<package>) gets, in its generated tree:
//
//   - <base>.advisor: the model port package. ModelPort is typed on the model
//     service's own message classes (never the raw transport stub beside
//     them), ModelPortException carries a failed call's status code, and
//     LlmRule is the gate: it runs the JDK's jdeps over the compiled classes
//     and fails when a class of the advisor's packages references any class
//     of the service but the port and what the port itself references.
//   - src/test/java/<base>/advisor/LlmRuleTest.java: the JUnit test that runs it.
//   - <base>.s2sm.ModelPortAdapter: the core's implementation of the port over
//     the generated S2S client.
//
// The core builds the adapter and passes it in; the advisor holds no client and
// no token, and the core makes every call that carries the service's token.
// One Maven module, as every Java service is: the gate reads the class files'
// references, as the JVM links them, so the layout needs no module split.

type javaAdvisorMethod struct {
	Rpc    string // the rpc, e.g. "Generate"
	Name   string // its Java method on the client and the port, e.g. "generate"
	Input  string // its request class, as imported, e.g. "Upstreamllm.CompleteRequest"
	Output string // its response class
}

type javaAdvisorData struct {
	ServiceLabel   string
	AdvisorPath    string // as tacplus.yaml names it, for the comments
	AdvisorPackage string
	PortPackage    string
	SmPackage      string
	ProtoException string
	ModelService   string
	ProtoService   string
	TypeImports    []string
	GroupAccessor  string
	ClientAccessor string
	Methods        []javaAdvisorMethod
}

// javaAdvisorPortPackage is the port package under the generated base.
func javaAdvisorPortPackage(base string) string {
	return base + ".advisor"
}

// javaAdvisorPackage is the Java package of the advisor at rel (its directory,
// from the project's): the directories after src/main/java. The gate finds
// the advisor's classes by it, so it must name one, and one outside the
// generated tree's base package.
func javaAdvisorPackage(rel, base string) (string, error) {
	parts := strings.Split(filepath.ToSlash(filepath.Clean(rel)), "/")
	at := -1
	for i := 0; i+2 < len(parts); i++ {
		if parts[i] == "src" && parts[i+1] == "main" && parts[i+2] == "java" {
			at = i + 3
		}
	}
	if at < 0 || at >= len(parts) {
		return "", fmt.Errorf("tacplus.yaml advisor: path %q is not a package directory under src/main/java: a Java advisor is a package of the service's sources (e.g. src/main/java/com/example/svc/advisor), which the gate finds by its package", filepath.ToSlash(rel))
	}
	pkgParts := parts[at:]
	for _, p := range pkgParts {
		if !isJavaIdentifier(p) {
			return "", fmt.Errorf("tacplus.yaml advisor: path %q: %q is not a Java package name", filepath.ToSlash(rel), p)
		}
	}
	pkg := strings.Join(pkgParts, ".")
	if pkg == base || strings.HasPrefix(pkg, base+".") {
		return "", fmt.Errorf("tacplus.yaml advisor: package %s is inside the generated package %s: the advisor must be a package apart from the generated tree", pkg, base)
	}
	if strings.HasPrefix(base, pkg+".") {
		return "", fmt.Errorf("tacplus.yaml advisor: package %s contains the generated package %s: the advisor's subpackages would be the generated tree", pkg, base)
	}
	return pkg, nil
}

func isJavaIdentifier(s string) bool {
	if s == "" || javaReservedWords[s] {
		return false
	}
	for i, r := range s {
		letter := r == '_' || r == '$' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !letter && (i == 0 || r < '0' || r > '9') {
			return false
		}
	}
	return true
}

// javaReservedWords are the keywords and literals a package name part may not be.
var javaReservedWords = map[string]bool{
	"abstract": true, "assert": true, "boolean": true, "break": true, "byte": true, "case": true,
	"catch": true, "char": true, "class": true, "const": true, "continue": true, "default": true,
	"do": true, "double": true, "else": true, "enum": true, "extends": true, "final": true,
	"finally": true, "float": true, "for": true, "goto": true, "if": true, "implements": true,
	"import": true, "instanceof": true, "int": true, "interface": true, "long": true, "native": true,
	"new": true, "package": true, "private": true, "protected": true, "public": true, "return": true,
	"short": true, "static": true, "strictfp": true, "super": true, "switch": true, "synchronized": true,
	"this": true, "throw": true, "throws": true, "transient": true, "try": true, "void": true,
	"volatile": true, "while": true, "true": true, "false": true, "null": true, "_": true,
}

// generateJavaAdvisorPort writes the advisor's model port package, its gate
// and its test, and the core's adapter, after the S2S clients (whose accessors
// the adapter calls). clients are the generated S2S clients.
func generateJavaAdvisorPort(config *tactype.Config, protoDir string, clients []generatedGrpcClientJava) error {
	if config.AdvisorDir == "" {
		return nil
	}
	data, err := buildJavaAdvisorData(config, protoDir, clients)
	if err != nil {
		return err
	}
	pkgDir := func(root, pkg string) string {
		return filepath.Join(config.OutputDir, "src", root, "java", strings.ReplaceAll(pkg, ".", string(filepath.Separator)))
	}
	for _, f := range []struct{ tmpl, path string }{
		{"ModelPort.java.tmpl", filepath.Join(pkgDir("main", data.PortPackage), "ModelPort.java")},
		{"ModelPortException.java.tmpl", filepath.Join(pkgDir("main", data.PortPackage), "ModelPortException.java")},
		{"LlmRule.java.tmpl", filepath.Join(pkgDir("main", data.PortPackage), "LlmRule.java")},
		{"LlmRuleTest.java.tmpl", filepath.Join(pkgDir("test", data.PortPackage), "LlmRuleTest.java")},
		{"ModelPortAdapter.java.tmpl", filepath.Join(pkgDir("main", data.SmPackage), "ModelPortAdapter.java")},
	} {
		text, err := templates.FS.ReadFile("java/advisor/" + f.tmpl)
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
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			return fmt.Errorf("advisor: %w", err)
		}
		if err := os.WriteFile(f.path, buf.Bytes(), 0o644); err != nil {
			return fmt.Errorf("advisor: %w", err)
		}
	}
	return nil
}

func buildJavaAdvisorData(config *tactype.Config, protoDir string, clients []generatedGrpcClientJava) (javaAdvisorData, error) {
	advisorPath, err := filepath.Rel(config.ProjectDir, config.AdvisorDir)
	if err != nil {
		advisorPath = config.AdvisorDir
	}
	advisorPkg, err := javaAdvisorPackage(advisorPath, config.JavaPackageBase)
	if err != nil {
		return javaAdvisorData{}, err
	}
	refuse := fmt.Errorf("tacplus.yaml advisor: the advisor reaches its model only through %s, and %s has no S2S client to it: add %s to depends and declare its service with (spec.grpc_s2s_client)", consts.AdvisorModelService, config.ServiceLabel(), consts.AdvisorModelService)
	if config.GetSmPrefix() != "s2sm" || len(clients) == 0 {
		return javaAdvisorData{}, refuse
	}
	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return javaAdvisorData{}, fmt.Errorf("advisor: %w", err)
	}
	// The model service is the one whose file declares (spec.service).name
	// consts.AdvisorModelService: its registry identity, whatever its package.
	var model *generatedGrpcClientJava
	for i, c := range clients {
		fd, err := dp.GetFileDescriptor(c.ProtoFile)
		if err != nil || fd.ServiceName != consts.AdvisorModelService {
			continue
		}
		if model != nil {
			return javaAdvisorData{}, fmt.Errorf("tacplus.yaml advisor: %s declares more than one S2S client to %s (%s.%s, %s.%s); the advisor's model port is typed on one", config.ServiceLabel(), consts.AdvisorModelService, model.PackageName, model.ServiceName, c.PackageName, c.ServiceName)
		}
		model = &clients[i]
	}
	if model == nil {
		return javaAdvisorData{}, refuse
	}

	// The port is narrow: the plain unary rpcs on the model service's own
	// messages. A stream, a collection read or a foreign message has no place
	// on it.
	imports := map[string]bool{}
	var methods []javaAdvisorMethod
	for _, m := range model.Methods {
		mi := m.method
		if mi.ClientStreaming || mi.ServerStreaming || mi.IsPageCollection() || mi.IsTier2SnapshotCollection() || mi.IsTier3SnapshotCollection() || mi.IsWatchCollection() {
			continue
		}
		inOuter, inOK := javaAdvisorOuterClass(m.inputType, mi.FullInputType, model.PackageName)
		outOuter, outOK := javaAdvisorOuterClass(m.outputType, mi.FullOutputType, model.PackageName)
		if !inOK || !outOK {
			continue
		}
		imports[model.TypesPackage+"."+inOuter] = true
		imports[model.TypesPackage+"."+outOuter] = true
		methods = append(methods, javaAdvisorMethod{Rpc: mi.Name, Name: javaGrpcClientMethodName(mi), Input: m.inputType, Output: m.outputType})
	}
	if len(methods) == 0 {
		return javaAdvisorData{}, fmt.Errorf("tacplus.yaml advisor: %s.%s has no unary rpc on its own messages for the advisor's model port", model.PackageName, model.ServiceName)
	}
	var typeImports []string
	for imp := range imports {
		typeImports = append(typeImports, imp)
	}
	sort.Strings(typeImports)

	return javaAdvisorData{
		ServiceLabel:   config.ServiceLabel(),
		AdvisorPath:    filepath.ToSlash(advisorPath),
		AdvisorPackage: advisorPkg,
		PortPackage:    javaAdvisorPortPackage(config.JavaPackageBase),
		SmPackage:      config.JavaPackageBase + "." + config.GetSmPrefix(),
		ProtoException: config.JavaPackageBase + ".types.sys.errors.ProtoException",
		ModelService:   consts.AdvisorModelService,
		ProtoService:   model.PackageName + "." + model.ServiceName,
		TypeImports:    typeImports,
		GroupAccessor:  model.GroupAccessor,
		ClientAccessor: model.ClientAccessor,
		Methods:        methods,
	}, nil
}

// javaAdvisorOuterClass is the outer class of a message of the model service's
// own proto package, as the client resolved it ("Upstreamllm.CompleteRequest"
// -> "Upstreamllm"); false for a message of another package.
func javaAdvisorOuterClass(resolved, fullType, pkg string) (string, bool) {
	i := strings.LastIndex(fullType, ".")
	if i <= 0 || fullType[:i] != pkg {
		return "", false
	}
	outer, _, found := strings.Cut(resolved, ".")
	if !found || outer == "" || strings.Contains(resolved, "..") {
		return "", false
	}
	return outer, true
}
