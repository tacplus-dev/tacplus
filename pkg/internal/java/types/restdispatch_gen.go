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

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/restdispatch"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// javaRestDispatchArm is one dispatchable method rendered as Java references.
type javaRestDispatchArm struct {
	GrpcPath       string
	ServiceID      string
	MethodName     string // grpc-java stub method, e.g. "getItem"
	StubClass      string // "<types pkg>.CatalogServiceGrpc"
	StubName       string // "CatalogServiceBlockingStub"
	ReqType        string // "<types pkg>.Catalog.GetItemRequest"
	RespType       string
	FromJson       string // "<types pkg>.Helpers.getItemRequestFromJson"
	ToJson         string
	ValidateInput  string // "<types pkg>.Helpers.validateGetItemRequest", "" when the type has no validator
	ValidateOutput string
	IsEmptyInput   bool
	IsEmptyOutput  bool
}

// javaRestDispatchRoute is one route table entry. AuthPolicy is the Java enum
// constant.
type javaRestDispatchRoute struct {
	HttpPath             string
	GrpcPath             string
	ServiceID            string
	EntitlementService   string
	HttpMethods          []string
	AuthPolicy           string
	EntitlementScopes    []string
	CapabilityScope      string
	ServerStreaming      bool
	ClientStreaming      bool
	TargetNamespaceField string
	FormBody             bool
	RedirectField        string // the proto3 JSON name, the canonical key across languages
	RedirectFieldProto   string // the proto field name, which the Java Helpers emit
}

type javaRestDispatchData struct {
	PackageBase       string
	SmPrefix          string
	ProtoExceptionFqn string
	Routes            []javaRestDispatchRoute
	UnaryArms         []javaRestDispatchArm
	StreamArms        []javaRestDispatchArm
}

var javaAuthPolicyConstants = map[string]string{
	restdispatch.AuthNoAuth:   "NO_AUTH",
	restdispatch.AuthAnyUser:  "ANY_USER",
	restdispatch.AuthEntitled: "ENTITLED",
}

// generateJavaRestDispatch renders RestDispatch.java for a
// (spec.service).rest_dispatch build, under <sm>/restdispatch/. Which routes
// and arms exist is decided by the shared planner; this maps them to the
// generated Java stub, Helpers and validator names.
func generateJavaRestDispatch(config *tactype.Config, servicesInfo []ServiceInfo, protoDir string) error {
	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}
	services := make([]restdispatch.Service, 0, len(servicesInfo))
	for _, svc := range servicesInfo {
		services = append(services, restdispatch.Service{
			ProtoFile:   svc.ProtoFile,
			PackageName: svc.PackageName,
			ServiceName: svc.ServiceName,
			ParentDir:   svc.ParentDir,
			IsS2S:       svc.IsS2S,
			EventLabel:  svc.EventLabel,
			HostLabel:   svc.HostLabel,
		})
	}
	plan, err := restdispatch.Collect(dp, config, services, protoDir)
	if err != nil {
		return err
	}

	// Java type references are file-based (the outer class comes from the
	// proto file), so map each proto package to the file that declares it. The
	// Java generators assume one proto file per package, named after it
	// (<dir>/<pkg>.proto, see the cross-package Helpers resolution in the
	// service wrapper); prefer that file, and otherwise the first in path
	// order, so the choice is deterministic.
	pkgToFile := make(map[string]string)
	paths := make([]string, 0)
	for path := range dp.GetAllFiles() {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		pkg := dp.GetAllFiles()[path].Package
		canonical := strings.TrimSuffix(filepath.Base(path), ".proto") == pkg
		if _, exists := pkgToFile[pkg]; !exists || canonical {
			pkgToFile[pkg] = path
		}
	}
	for _, svc := range servicesInfo {
		pkgToFile[svc.PackageName] = svc.ProtoFile
	}
	typesPackage := func(pkg string) string {
		outer := javaTypesOuterForProto(config, protoDir, pkgToFile[pkg], pkg)
		return outer[:strings.LastIndex(outer, ".")]
	}

	data := javaRestDispatchData{
		PackageBase:       config.JavaPackageBase,
		SmPrefix:          config.GetSmPrefix(),
		ProtoExceptionFqn: config.JavaPackageBase + ".types.sys.errors.ProtoException",
	}
	for _, a := range plan.Arms {
		svcPkg := typesPackage(a.PackageName)
		arm := javaRestDispatchArm{
			GrpcPath:      a.GrpcPath,
			ServiceID:     a.ServiceID,
			MethodName:    javaRpcMethodName(a.MethodName),
			StubClass:     svcPkg + "." + a.ServiceName + "Grpc",
			StubName:      a.ServiceName + "BlockingStub",
			IsEmptyInput:  a.IsEmptyInput,
			IsEmptyOutput: a.IsEmptyOutput,
		}
		if !a.IsEmptyInput {
			pkg := restdispatch.TypePackage(a.InputType, pkgToFile)
			outer := javaTypesOuterForProto(config, protoDir, pkgToFile[pkg], pkg)
			helpers := typesPackage(pkg) + ".Helpers"
			arm.ReqType = outer + "." + a.InputSimpleName
			arm.FromJson = helpers + "." + strutil.ToLowerFirst(a.InputSimpleName) + "FromJson"
			if a.ValidateInput {
				arm.ValidateInput = helpers + ".validate" + a.InputSimpleName
			}
		}
		if !a.IsEmptyOutput {
			pkg := restdispatch.TypePackage(a.OutputType, pkgToFile)
			outer := javaTypesOuterForProto(config, protoDir, pkgToFile[pkg], pkg)
			helpers := typesPackage(pkg) + ".Helpers"
			arm.RespType = outer + "." + a.OutputSimpleName
			arm.ToJson = helpers + "." + strutil.ToLowerFirst(a.OutputSimpleName) + "ToJson"
			if a.ValidateOutput {
				arm.ValidateOutput = helpers + ".validate" + a.OutputSimpleName
			}
		}
		if a.ServerStreaming {
			data.StreamArms = append(data.StreamArms, arm)
		} else {
			data.UnaryArms = append(data.UnaryArms, arm)
		}
	}
	for _, r := range plan.Routes {
		data.Routes = append(data.Routes, javaRestDispatchRoute{
			HttpPath:             r.HttpPath,
			GrpcPath:             r.GrpcPath,
			ServiceID:            r.ServiceID,
			EntitlementService:   r.EntitlementService,
			HttpMethods:          r.HttpMethods,
			AuthPolicy:           javaAuthPolicyConstants[r.AuthPolicy],
			EntitlementScopes:    r.EntitlementScopes,
			CapabilityScope:      r.CapabilityScope,
			ServerStreaming:      r.ServerStreaming,
			ClientStreaming:      r.ClientStreaming,
			TargetNamespaceField: r.TargetNamespaceField,
			FormBody:             r.FormBody,
			RedirectField:        r.RedirectJSONName,
			RedirectFieldProto:   r.RedirectField,
		})
	}

	src, err := renderJavaRestDispatch(data)
	if err != nil {
		return err
	}
	packagePath := strings.ReplaceAll(config.JavaPackageBase, ".", string(filepath.Separator))
	outputDir := filepath.Join(config.LangDir(), "src", "main", "java", packagePath, config.GetSmPrefix(), "restdispatch")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create restdispatch dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "RestDispatch.java")
	if err := os.WriteFile(outputPath, src, 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", outputPath, err)
	}
	zap.L().Info("Generated the REST dispatch plane",
		zap.String("path", outputPath),
		zap.Int("unaryArms", len(data.UnaryArms)),
		zap.Int("streamArms", len(data.StreamArms)),
		zap.Int("routes", len(data.Routes)),
		zap.Bool("isGateway", config.IsGateway),
	)
	return nil
}

// renderJavaRestDispatch renders RestDispatch.java from its template data.
func renderJavaRestDispatch(data javaRestDispatchData) ([]byte, error) {
	tmplContent, err := loadTemplate("RestDispatch.java.tmpl")
	if err != nil {
		return nil, fmt.Errorf("failed to load RestDispatch template: %w", err)
	}
	funcs := template.FuncMap{
		"javaStr": javaStringLiteral,
		"javaStrList": func(values []string) string {
			quoted := make([]string, len(values))
			for i, v := range values {
				quoted[i] = javaStringLiteral(v)
			}
			return "List.of(" + strings.Join(quoted, ", ") + ")"
		},
	}
	tmpl, err := template.New("restdispatch").Funcs(funcs).Funcs(header.Funcs()).Parse(tmplContent)
	if err != nil {
		return nil, fmt.Errorf("failed to parse RestDispatch template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("failed to execute RestDispatch template: %w", err)
	}
	return buf.Bytes(), nil
}

// javaStringLiteral quotes a value as a Java string literal. The REST paths,
// gRPC paths and service IDs it guards are proto-annotation-derived, so an
// author-supplied quote, backslash or control character would otherwise
// terminate or corrupt the emitted literal.
func javaStringLiteral(value string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u%04x`, r)
				continue
			}
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
