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
	"strconv"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/restdispatch"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// goRestDispatchArm is one dispatchable method rendered as Go references.
type goRestDispatchArm struct {
	GrpcPath       string
	ServiceID      string
	MethodName     string // protoc-gen-go-grpc client method, e.g. "GetItem"
	IsEmptyInput   bool
	IsEmptyOutput  bool
	NewClient      string // "catalog_pb.NewCatalogServiceClient"
	FromJSON       string // "catalog_pb.GetItemRequestFromJSON"
	ToJSON         string // "catalog_pb.GetItemResponseToJSON"
	ValidateInput  string // "catalog_pb.ValidateGetItemRequest", "" when the type has no validator
	ValidateOutput string
	// RouteField is (spec.rpc).route_field (the proto name) of a tenant-routed
	// method and RouteGetter its protoc-gen-go getter: the arm reads the target
	// tenant off the decoded request before it dials. Both "" for a platform
	// service, whose arm is unchanged.
	RouteField  string
	RouteGetter string
}

// goRestDispatchRoute is one route table entry.
type goRestDispatchRoute struct {
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
	RedirectFieldProto   string // the proto field name, which the Go ToJSON helpers emit
	// RouteField (proto name), RouteFieldJSON and RouteFieldNumber are the
	// tenant-routed route's (spec.rpc).route_field; ""/0 for a platform route.
	RouteField       string
	RouteFieldJSON   string
	RouteFieldNumber int32
}

type goRestDispatchImport struct {
	Alias string
	Path  string
}

type goRestDispatchData struct {
	// ModuleBasePath roots the generated module; the plane imports the
	// serviceconnection chassis from it for the tenant-routing helpers.
	ModuleBasePath string
	Imports        []goRestDispatchImport
	Routes         []goRestDispatchRoute
	UnaryArms      []goRestDispatchArm
	StreamArms     []goRestDispatchArm
}

// generateGoRestDispatch renders the REST -> gRPC dispatch plane for a
// (spec.service).rest_dispatch build, under <sm>/rest_dispatch/. Which routes
// and arms exist is decided by the shared planner; this maps them to the
// generated Go client, JSON and validator names.
func generateGoRestDispatch(config *tactype.Config, servicesInfo []ServiceInfo, protoDir string) error {
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

	data := goRestDispatchData{ModuleBasePath: config.ModuleBasePath}
	imports := map[string]string{}
	for _, a := range plan.Arms {
		pkgToDir := plan.PkgToDirFor(a)
		svcAlias := goRestDispatchTypesImport(config, imports, a.PackageName, pkgToDir)
		arm := goRestDispatchArm{
			GrpcPath:      a.GrpcPath,
			ServiceID:     a.ServiceID,
			MethodName:    a.MethodName,
			IsEmptyInput:  a.IsEmptyInput,
			IsEmptyOutput: a.IsEmptyOutput,
			NewClient:     svcAlias + ".New" + a.ServiceName + "Client",
			RouteField:    a.RouteField,
			RouteGetter:   goRouteGetter(a.RouteField),
		}
		if a.RouteField != "" && a.IsEmptyInput {
			return fmt.Errorf("%s declares route_field %q but its request is google.protobuf.Empty", a.GrpcPath, a.RouteField)
		}
		if a.IsEmptyInput {
			imports["emptypb"] = wellKnownGoType["Empty"].importPath
		} else {
			pkg := goRestDispatchTypePackage(a.InputType, pkgToDir)
			alias := goRestDispatchTypesImport(config, imports, pkg, pkgToDir)
			arm.FromJSON = alias + "." + a.InputSimpleName + "FromJSON"
			if a.ValidateInput {
				arm.ValidateInput = alias + ".Validate" + a.InputSimpleName
			}
		}
		if !a.IsEmptyOutput {
			pkg := goRestDispatchTypePackage(a.OutputType, pkgToDir)
			alias := goRestDispatchTypesImport(config, imports, pkg, pkgToDir)
			arm.ToJSON = alias + "." + a.OutputSimpleName + "ToJSON"
			if a.ValidateOutput {
				arm.ValidateOutput = alias + ".Validate" + a.OutputSimpleName
			}
		}
		if a.ServerStreaming {
			data.StreamArms = append(data.StreamArms, arm)
		} else {
			data.UnaryArms = append(data.UnaryArms, arm)
		}
	}
	for _, r := range plan.Routes {
		data.Routes = append(data.Routes, goRestDispatchRoute{
			HttpPath:             r.HttpPath,
			GrpcPath:             r.GrpcPath,
			ServiceID:            r.ServiceID,
			EntitlementService:   r.EntitlementService,
			HttpMethods:          r.HttpMethods,
			AuthPolicy:           r.AuthPolicy,
			EntitlementScopes:    r.EntitlementScopes,
			CapabilityScope:      r.CapabilityScope,
			ServerStreaming:      r.ServerStreaming,
			ClientStreaming:      r.ClientStreaming,
			TargetNamespaceField: r.TargetNamespaceField,
			FormBody:             r.FormBody,
			RedirectField:        r.RedirectJSONName,
			RedirectFieldProto:   r.RedirectField,
			RouteField:           r.RouteField,
			RouteFieldJSON:       r.RouteFieldJSON,
			RouteFieldNumber:     r.RouteFieldNumber,
		})
	}
	aliases := make([]string, 0, len(imports))
	for alias := range imports {
		aliases = append(aliases, alias)
	}
	sort.Strings(aliases)
	for _, alias := range aliases {
		data.Imports = append(data.Imports, goRestDispatchImport{Alias: alias, Path: imports[alias]})
	}

	outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "rest_dispatch")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create rest_dispatch directory: %w", err)
	}
	outputPath := filepath.Join(outputDir, "rest_dispatch.go")
	zap.L().Info("Generating REST dispatch plane",
		zap.String("path", outputPath),
		zap.Int("unaryArms", len(data.UnaryArms)),
		zap.Int("streamArms", len(data.StreamArms)),
		zap.Int("routes", len(data.Routes)),
		zap.Bool("isGateway", config.IsGateway),
	)
	rendered, err := renderGoRestDispatch(data)
	if err != nil {
		return err
	}
	return os.WriteFile(outputPath, rendered, 0644)
}

// renderGoRestDispatch renders rest_dispatch.go from its template data.
func renderGoRestDispatch(data goRestDispatchData) ([]byte, error) {
	tmplStr, err := loadTemplate("rest_dispatch.go.tmpl")
	if err != nil {
		return nil, fmt.Errorf("failed to load rest_dispatch template: %w", err)
	}
	funcs := template.FuncMap{
		"goStr": strconv.Quote,
		"goStrSlice": func(values []string) string {
			if len(values) == 0 {
				return "nil"
			}
			quoted := make([]string, len(values))
			for i, v := range values {
				quoted[i] = strconv.Quote(v)
			}
			return "[]string{" + strings.Join(quoted, ", ") + "}"
		},
	}
	tmpl, err := template.New("rest_dispatch").Funcs(funcs).Funcs(header.Funcs()).Parse(tmplStr)
	if err != nil {
		return nil, fmt.Errorf("failed to parse rest_dispatch template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("failed to execute rest_dispatch template: %w", err)
	}
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("failed to format rest_dispatch.go: %w\n%s", err, buf.String())
	}
	return formatted, nil
}

// goRestDispatchTypesImport records the types import for a proto package and
// returns its alias, "<pkg>_pb" as the service wrappers spell it.
func goRestDispatchTypesImport(config *tactype.Config, imports map[string]string, pkg string, pkgToDir map[string]string) string {
	alias := strings.ReplaceAll(pkg, ".", "_") + "_pb"
	dir := pkgToDir[pkg]
	var importPath string
	switch {
	case strings.HasPrefix(dir, "services"):
		importPath = fmt.Sprintf("%s/types/%s/%s", config.ModuleBasePath, dir, pkg)
	case filepath.Base(dir) == pkg:
		importPath = fmt.Sprintf("%s/types/%s", config.ModuleBasePath, dir)
	default:
		importPath = fmt.Sprintf("%s/types/%s/%s", config.ModuleBasePath, dir, pkg)
	}
	imports[alias] = importPath
	return alias
}

// goRestDispatchTypePackage is the proto package of a fully-qualified type:
// the longest known package prefix, so a dotted package resolves whole.
func goRestDispatchTypePackage(fullType string, pkgToDir map[string]string) string {
	return restdispatch.TypePackage(fullType, pkgToDir)
}

// goRouteGetter is the protoc-gen-go getter of a route field ("tenant" ->
// "GetTenant"), or "" for a platform method, which has none.
func goRouteGetter(field string) string {
	if field == "" {
		return ""
	}
	return "Get" + toGoFieldName(field)
}
