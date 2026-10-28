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
	"unicode"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// generateW2SClients generates typed W2S clients for each selected gRPC service.
// Unary RPCs use HTTP/JSON, server-streaming RPCs use SSE, and client-streaming
// or bidirectional RPCs use the gateway WebSocket stream contract.
func generateW2SClients(servicesInfo []ServiceInfo, inputDir string) error {
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	for _, svc := range servicesInfo {
		protoInfo, err := descriptorParser.ConvertToProtoInfo(svc.ProtoFile)
		if err != nil {
			return fmt.Errorf("failed to parse %s: %w", svc.ProtoFile, err)
		}

		if err := generateGoW2SClient(svc, protoInfo); err != nil {
			return fmt.Errorf("failed to generate W2S client for %s: %w", svc.ServiceName, err)
		}
	}

	if len(servicesInfo) > 0 {
		if err := generateGoW2SClientAggregators(servicesInfo); err != nil {
			return fmt.Errorf("failed to generate W2S client aggregators: %w", err)
		}
	}

	return nil
}

// generateW2SServiceManager generates the w2sm/ entry point, a lightweight
// client-only ServiceManager for HTTP/SSE/WebSocket W2S clients.
func generateW2SServiceManager(config *tactype.Config) error {
	tmplContent, err := loadTemplate("w2sm.go.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load w2sm.go template: %w", err)
	}

	tmpl, err := template.New("w2sm").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse w2sm.go template: %w", err)
	}

	data := struct {
		Header         string
		ModuleBasePath string
	}{
		Header:         header.GeneratedHeader(consts.LangGo, "w2sm"),
		ModuleBasePath: config.ModuleBasePath,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute w2sm.go template: %w", err)
	}

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("failed to format w2sm.go: %w\nraw output:\n%s", err, buf.String())
	}

	outputDir := filepath.Join(config.OutputDir, "w2sm")
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create w2sm dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "w2sm.go")
	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write w2sm.go: %w", err)
	}

	zap.L().Info("Generated w2sm entry point", zap.String("path", outputPath))
	return nil
}

// resolveW2STarget determines the physical service ID for W2S routing groups.
func resolveW2STarget(svc ServiceInfo) string {
	config := svc.Config
	meshTarget := svc.PackageName
	if svc.HostLabel != "" {
		meshTarget = svc.HostLabel
	}
	for _, entry := range config.GrpcW2SClients {
		matchKey := strings.TrimSuffix(filepath.Base(entry.ProtoFile), ".proto")
		if entry.ProtoPackage != "" {
			matchKey = entry.ProtoPackage
		}
		if matchKey == svc.PackageName && entry.TargetService != "" {
			meshTarget = entry.TargetService
			break
		}
	}
	return meshTarget
}

type goW2SImport struct {
	Alias string
	Path  string
}

type goW2SServiceEntry struct {
	FieldName        string
	AccessorName     string
	ClientName       string
	ImportAlias      string
	ParentStructName string
}

type goW2STargetEntry struct {
	Target       string
	StructName   string
	FieldName    string
	AccessorName string
	ConstName    string
	Services     []goW2SServiceEntry
}

// generateGoW2SClientAggregators creates the target hierarchy:
//
//	sm.W2SClients().Target().ServiceNameW2SClient()
func generateGoW2SClientAggregators(servicesInfo []ServiceInfo) error {
	if len(servicesInfo) == 0 {
		return nil
	}
	config := servicesInfo[0].Config
	smDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "internal", "grpc_over_rest")

	allSvcNameCount := make(map[string]int)
	for _, svc := range servicesInfo {
		allSvcNameCount[svc.ServiceName]++
	}

	importByAlias := make(map[string]string)
	var imports []goW2SImport
	targetIndex := make(map[string]int)
	var targets []goW2STargetEntry

	for _, svc := range servicesInfo {
		target := resolveW2STarget(svc)
		if _, ok := targetIndex[target]; !ok {
			base := goW2SExportedIdent(target)
			targetIndex[target] = len(targets)
			targets = append(targets, goW2STargetEntry{
				Target:       target,
				StructName:   base + "W2SClients",
				FieldName:    strutil.Decapitalize(base) + "W2SClients",
				AccessorName: base,
				ConstName:    "Target" + base,
			})
		}

		alias := goW2SImportAlias(svc)
		importPath := config.ModuleBasePath + "/" + config.GetSmPrefix() + "/internal/grpc_over_rest/" + svc.ParentDir + "/" + svc.ImportDirName()
		if seenPath, ok := importByAlias[alias]; ok && seenPath != importPath {
			return fmt.Errorf("W2S import alias collision for %s: %s and %s", alias, seenPath, importPath)
		}
		if _, ok := importByAlias[alias]; !ok {
			importByAlias[alias] = importPath
			imports = append(imports, goW2SImport{Alias: alias, Path: importPath})
		}

		fieldName, accessorName, clientName := goW2SClientNames(svc, allSvcNameCount)
		idx := targetIndex[target]
		targets[idx].Services = append(targets[idx].Services, goW2SServiceEntry{
			FieldName:        fieldName,
			AccessorName:     accessorName,
			ClientName:       clientName,
			ImportAlias:      alias,
			ParentStructName: targets[idx].StructName,
		})
	}

	tmplStr, err := loadTemplate("w2s_clients.go.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load w2s clients template: %w", err)
	}
	tmpl, err := template.New("w2s_clients").Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse w2s clients template: %w", err)
	}

	data := struct {
		Header  string
		Imports []goW2SImport
		Targets []goW2STargetEntry
	}{
		Header:  header.GeneratedHeader(consts.LangGo, "w2s_clients"),
		Imports: imports,
		Targets: targets,
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute w2s clients template: %w", err)
	}

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("failed to format w2s clients: %w\nraw output:\n%s", err, buf.String())
	}

	if err := os.MkdirAll(smDir, 0755); err != nil {
		return fmt.Errorf("failed to create W2S clients directory: %w", err)
	}
	outputPath := filepath.Join(smDir, "w2s_clients.go")
	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write W2S clients: %w", err)
	}

	return nil
}

func goW2SClientNames(svc ServiceInfo, svcNameCount map[string]int) (fieldName, accessorName, clientName string) {
	clientName = svc.ServiceName + "W2SClient"
	accessorName = clientName
	if svcNameCount[svc.ServiceName] > 1 {
		accessorName = strutil.ToPascalCase(svc.PackageName) + clientName
	}
	fieldName = strutil.Decapitalize(accessorName)
	return
}

func goW2SImportAlias(svc ServiceInfo) string {
	return goW2SSafeIdent(strings.ToLower(svc.ParentDir+"_"+svc.ImportDirName()+"_w2s"), false)
}

func goW2SExportedIdent(value string) string {
	value = goW2SSafeIdent(value, false)
	if value == "" {
		return "Default"
	}
	return strutil.ToPascalCase(value)
}

func goW2SSafeIdent(value string, exported bool) string {
	var b strings.Builder
	for _, r := range value {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return ""
	}
	if unicode.IsDigit([]rune(out)[0]) {
		out = "_" + out
	}
	if exported {
		return strutil.ToPascalCase(out)
	}
	return out
}

func generateGoW2SClient(svc ServiceInfo, info *parser.ProtoInfo) error {
	outputDir := filepath.Join(svc.Config.OutputDir, svc.Config.GetSmPrefix(), "internal", "grpc_over_rest", svc.ParentDir, svc.ImportDirName())
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	svcSnake := strutil.ToSnakeCase(svc.ServiceName)
	wrapperFile := strutil.StripPackagePrefix(svcSnake, svc.ImportDirName()) + ".go"
	outputPath := filepath.Join(outputDir, wrapperFile)

	zap.L().Info("Generating Go W2S client", zap.String("path", outputPath))

	var targetService *parser.ServiceInfo
	for _, service := range info.Services {
		if service.Name == svc.ServiceName {
			targetService = service
			break
		}
	}
	if targetService == nil {
		return fmt.Errorf("service %s not found in proto info for %s", svc.ServiceName, svc.ProtoFile)
	}

	clientName := svc.ServiceName + "W2SClient"
	methodImports := collectGoW2SReferencedTypeImports(targetService, info.Package, info.CrossPackageTypes, svc.Config.ModuleBasePath, svc.ParentDir, svc.ImportDirName())

	var methods strings.Builder
	var extraTypes strings.Builder
	hasHTTP := false
	hasServerStreaming := false
	hasClientStreaming := false
	hasClientOnlyStreaming := false
	hasBidi := false
	hasRoute := false
	for _, method := range targetService.Methods {
		// An RPC_PRINCIPAL_MCP rpc carries no REST route by construction — the
		// parser leaves HttpPath/HttpMethods empty for it — so there is no w2s
		// client method to emit. Without this it would trip the missing-route
		// refusal below and fail generation outright.
		if method.IsMcp {
			continue
		}
		if method.HttpPath == "" {
			return fmt.Errorf("%s.%s requires W2S REST route metadata; clients.w2s.enabled cannot emit a method without an HTTP path",
				targetService.Name, method.Name)
		}
		authRequired := !(svc.AuthDisabled || targetService.AuthDisabled || method.AuthDisabled)
		result := generateGoW2SClientMethod(clientName, targetService, method, info.Package, authRequired)
		methods.WriteString(result.method)
		methods.WriteString("\n")
		if result.extraType != "" {
			extraTypes.WriteString(result.extraType)
			extraTypes.WriteString("\n")
		}
		if method.ClientStreaming {
			hasClientStreaming = true
		} else {
			hasHTTP = true
		}
		if method.ClientStreaming && !method.ServerStreaming {
			hasClientOnlyStreaming = true
		}
		if method.ServerStreaming && !method.ClientStreaming {
			hasServerStreaming = true
		}
		if method.ClientStreaming && method.ServerStreaming {
			hasBidi = true
		}
		if goW2SRouteCheck(method, "req", "") != "" {
			hasRoute = true
		}
	}

	data := struct {
		Header              string
		PackageName         string
		TypesModulePath     string
		HasTypesImport      bool
		CrossPackageImports string
		ServiceName         string
		ClientName          string
		Methods             string
		ExtraTypes          string
		HasHTTP             bool
		HasServerStreaming  bool
		HasClientStreaming  bool
		HasBidi             bool
		HasWebSocket        bool
		HasGRPCStreaming    bool
		HasSync             bool
		// HasRoute: some rpc declares (spec.rpc).route_field, whose value the
		// client checks with the serviceconnection chassis before sending.
		HasRoute                    bool
		ServiceConnectionImportPath string
	}{
		Header:                      header.GeneratedHeader(consts.LangGo, svc.ProtoFile),
		PackageName:                 svc.PackageName,
		TypesModulePath:             goW2STypesModulePath(svc.Config.ModuleBasePath, svc.ParentDir, svc.ImportDirName()),
		HasTypesImport:              methodImports.hasTypesImport,
		CrossPackageImports:         methodImports.crossPackageImports,
		ServiceName:                 svc.ServiceName,
		ClientName:                  clientName,
		Methods:                     methods.String(),
		ExtraTypes:                  extraTypes.String(),
		HasHTTP:                     hasHTTP,
		HasServerStreaming:          hasServerStreaming,
		HasClientStreaming:          hasClientOnlyStreaming,
		HasBidi:                     hasBidi,
		HasWebSocket:                hasClientStreaming,
		HasGRPCStreaming:            hasServerStreaming || hasClientOnlyStreaming,
		HasSync:                     hasClientOnlyStreaming || hasBidi,
		HasRoute:                    hasRoute,
		ServiceConnectionImportPath: svc.Config.ModuleBasePath + "/chassis/serviceconnection",
	}

	tmplStr, err := loadTemplate("w2sclient.go.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load w2sclient template: %w", err)
	}
	tmpl, err := template.New("w2sclient").Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse w2sclient template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute w2sclient template: %w", err)
	}

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return fmt.Errorf("failed to format w2s client: %w\nraw output:\n%s", err, buf.String())
	}

	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write W2S client: %w", err)
	}

	return nil
}

type goW2SMethodImports struct {
	hasTypesImport      bool
	crossPackageImports string
}

func collectGoW2SReferencedTypeImports(svc *parser.ServiceInfo, servicePackage string, crossPkgTypes map[string]string, moduleBasePath, parentDir, importDirName string) goW2SMethodImports {
	usedPkgs := make(map[string]bool)
	usedWKT := make(map[string]bool)
	hasTypesImport := false

	for _, method := range svc.Methods {
		// Skipped for the same reason as the method emission itself: an MCP
		// tool gets no w2s client method, so its types are not referenced. The
		// import block is rendered by format.Source, which does not prune an
		// unused import, so this must stay in step with the emission loop.
		if method.IsMcp {
			continue
		}
		for _, fullType := range goW2SReferencedTypes(method) {
			pkg, simple := splitProtoType(fullType)
			switch {
			case pkg == "google.protobuf":
				usedWKT[simple] = true
			case pkg != "" && pkg != servicePackage:
				usedPkgs[pkg] = true
			default:
				hasTypesImport = true
			}
		}
	}

	var imports strings.Builder
	emitted := make(map[string]bool)
	var wktNames []string
	for typeName := range usedWKT {
		wktNames = append(wktNames, typeName)
	}
	sort.Strings(wktNames)
	for _, typeName := range wktNames {
		if wkt, ok := wellKnownGoType[typeName]; ok && !emitted[wkt.alias] {
			fmt.Fprintf(&imports, "\n%s \"%s\"", wkt.alias, wkt.importPath)
			emitted[wkt.alias] = true
		}
	}
	var pkgs []string
	for pkg := range crossPkgTypes {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)
	for _, pkg := range pkgs {
		dir := crossPkgTypes[pkg]
		if dir == "" || !usedPkgs[pkg] || pkg == "google.protobuf" {
			continue
		}
		importAlias := pkg + "_pb"
		if emitted[importAlias] {
			continue
		}
		var importPath string
		if filepath.Base(dir) == pkg {
			importPath = fmt.Sprintf("%s/types/%s", moduleBasePath, dir)
		} else {
			importPath = fmt.Sprintf("%s/types/%s/%s", moduleBasePath, dir, pkg)
		}
		if strings.HasPrefix(dir, "services") {
			importPath = fmt.Sprintf("%s/types/%s", moduleBasePath, dir)
		}
		fmt.Fprintf(&imports, "\n%s \"%s\"", importAlias, importPath)
		emitted[importAlias] = true
	}

	_ = goW2STypesModulePath(moduleBasePath, parentDir, importDirName)
	return goW2SMethodImports{
		hasTypesImport:      hasTypesImport,
		crossPackageImports: imports.String(),
	}
}

func goW2SReferencedTypes(method *parser.MethodInfo) []string {
	var refs []string
	if method.ClientStreaming || method.ServerStreaming || !isEmptyType(method.FullInputType) {
		refs = append(refs, method.FullInputType)
	}
	if method.ClientStreaming || method.ServerStreaming || !isEmptyType(method.FullOutputType) {
		refs = append(refs, method.FullOutputType)
	}
	return refs
}

func splitProtoType(fullType string) (pkg, simple string) {
	parts := strings.Split(fullType, ".")
	if len(parts) <= 1 {
		return "", fullType
	}
	return strings.Join(parts[:len(parts)-1], "."), parts[len(parts)-1]
}

func goW2STypesModulePath(moduleBasePath, parentDir, importDirName string) string {
	if parentDir == "services" {
		return fmt.Sprintf("%s/types/services/%s", moduleBasePath, importDirName)
	}
	return fmt.Sprintf("%s/types/%s/%s", moduleBasePath, parentDir, importDirName)
}

type goW2SMethodResult struct {
	method    string
	extraType string
}

func goW2SHTTPMethod(method *parser.MethodInfo, isEmptyInput bool) string {
	if isEmptyInput && method.HttpMethod != "" {
		return method.HttpMethod
	}
	return "POST"
}

func goW2SRequestValidation(requestVar, fullType, simpleType, servicePackage, returnExpr string) string {
	validate := goValidateRef(fullType, simpleType, servicePackage)
	if validate == "" {
		return ""
	}
	return fmt.Sprintf(`	if err := %s(%s); err != nil {
		return %s
	}
`, validate, requestVar, returnExpr)
}

// goW2SRouteCheck returns, for a routed rpc ((spec.rpc).route_field), the
// check that refuses a request whose route field is empty or not a tid
// (INVALID_ARGUMENT naming the field) before anything is sent: the gateway
// routes the call by that field, so the client never sends one it would
// refuse. ret is the return prefix for the method's non-error results. A
// platform rpc (or an Empty request, which carries no field) gets "".
func goW2SRouteCheck(method *parser.MethodInfo, requestVar, ret string) string {
	if method.RouteField == nil || isEmptyType(method.FullInputType) {
		return ""
	}
	return fmt.Sprintf(`	if _, err := serviceconnection.RouteTenant(%q, %s.%s()); err != nil {
		return %serr
	}
`, method.RouteField.Field, requestVar, goRouteGetter(method.RouteField.Field), ret)
}

// goW2SValidateClosure is the per-message check of a client stream: request
// validation, then routeCheck (goW2SRouteCheck, "" for a platform rpc).
func goW2SValidateClosure(fullType, simpleType, servicePackage, routeCheck string) string {
	validate := goValidateRef(fullType, simpleType, servicePackage)
	if validate == "" && routeCheck == "" {
		return "nil"
	}
	var validation string
	if validate != "" {
		validation = fmt.Sprintf(`
			if err := %s(req); err != nil {
				return status.Error(codes.InvalidArgument, fmt.Sprintf("validation failed: %%v", err))
			}`, validate)
	}
	if routeCheck != "" {
		validation += "\n" + strings.TrimRight(routeCheck, "\n")
	}
	return fmt.Sprintf(`func(req *%s) error {%s
			return nil
		}`, goTypeRef(fullType, simpleType, servicePackage), validation)
}

func goW2SBodyStatements(requestVar, fullType, simpleType, servicePackage string, isEmptyInput bool, returnExpr, routeCheck string) string {
	if isEmptyInput {
		return "\tbody := []byte(\"{}\")"
	}
	validation := goW2SRequestValidation(requestVar, fullType, simpleType, servicePackage, returnExpr) + routeCheck
	return fmt.Sprintf(`%s	body, err := encodeW2SProto(%s)
	if err != nil {
		return %s
	}`, validation, requestVar, returnExpr)
}

func goW2SDecodeClosure(goOutput, fullType, simpleType, servicePackage string) string {
	validate := goValidateRef(fullType, simpleType, servicePackage)
	var validation string
	if validate != "" {
		validation = fmt.Sprintf(`
		if err := %s(&resp); err != nil {
			return resp, status.Error(codes.Internal, fmt.Sprintf("response validation failed: %%v", err))
		}`, validate)
	}
	return fmt.Sprintf(`func(data []byte) (%s, error) {
		var resp %s
		if err := decodeW2SProtoPayload(data, &resp); err != nil {
			return resp, err
		}
%s
		return resp, nil
	}`, goOutput, goOutput, validation)
}

func goW2SResponseValidation(varName, fullType, simpleType, servicePackage, returnExpr string) string {
	validate := goValidateRef(fullType, simpleType, servicePackage)
	if validate == "" {
		return ""
	}
	return fmt.Sprintf(`	if err := %s(&%s); err != nil {
		return %s
	}
`, validate, varName, returnExpr)
}

func generateGoW2SClientMethod(clientName string, svc *parser.ServiceInfo, method *parser.MethodInfo, servicePackage string, authRequired bool) goW2SMethodResult {
	goInput := goTypeRef(method.FullInputType, method.InputType, servicePackage)
	goOutput := goTypeRef(method.FullOutputType, method.OutputType, servicePackage)
	isEmptyInput := isEmptyType(method.FullInputType)
	isEmptyOutput := isEmptyType(method.FullOutputType)
	httpMethod := goW2SHTTPMethod(method, isEmptyInput)
	authLiteral := fmt.Sprintf("%t", authRequired)

	if method.ClientStreaming && method.ServerStreaming {
		return generateGoW2SBidiMethod(clientName, svc, method, goInput, goOutput, authLiteral, servicePackage)
	}

	if method.ClientStreaming {
		methodCode := fmt.Sprintf(`// %s calls %s.%s via W2S WebSocket client streaming.
func (c *%s) %s(ctx context.Context, opts ...grpc.CallOption) (grpc.ClientStreamingClient[%s, %s], error) {
	ws, err := c.connectWebSocket(ctx, %q)
	if err != nil {
		return nil, err
	}
	if err := c.sendStartFrame(ctx, ws, %s); err != nil {
		ws.Close()
		return nil, err
	}
	return &w2sClientStream[%s, %s]{
		ctx: ctx,
		ws: ws,
		client: c,
		authRequired: %s,
		validate: %s,
		encode: func(req *%s) ([]byte, error) {
			return encodeW2SProto(req)
		},
		decode: %s,
	}, nil
}`, method.Name, svc.Name, method.Name, clientName, method.Name, goInput, goOutput, method.HttpPath, authLiteral,
			goInput, goOutput, authLiteral, goW2SValidateClosure(method.FullInputType, method.InputType, servicePackage, goW2SRouteCheck(method, "req", "")), goInput, goW2SDecodeClosure(goOutput, method.FullOutputType, method.OutputType, servicePackage))
		return goW2SMethodResult{method: methodCode}
	}

	if method.ServerStreaming {
		body := goW2SBodyStatements("req", method.FullInputType, method.InputType, servicePackage, isEmptyInput, "nil, status.Error(codes.InvalidArgument, fmt.Sprintf(\"validation failed: %v\", err))", goW2SRouteCheck(method, "req", "nil, "))
		methodCode := fmt.Sprintf(`// %s calls %s.%s via W2S SSE server streaming.
func (c *%s) %s(ctx context.Context, req *%s, opts ...grpc.CallOption) (grpc.ServerStreamingClient[%s], error) {
%s
	return streamW2SSSE(c, ctx, %q, %q, body, %s, %s)
}`, method.Name, svc.Name, method.Name, clientName, method.Name, goInput, goOutput,
			body, httpMethod, method.HttpPath, authLiteral, goW2SDecodeClosure(goOutput, method.FullOutputType, method.OutputType, servicePackage))
		return goW2SMethodResult{method: methodCode}
	}

	bodyReturn, routeReturn := "nil, err", "nil, "
	if isEmptyOutput {
		bodyReturn, routeReturn = "err", ""
	}
	body := goW2SBodyStatements("req", method.FullInputType, method.InputType, servicePackage, isEmptyInput, bodyReturn, goW2SRouteCheck(method, "req", routeReturn))

	if isEmptyOutput {
		if isEmptyInput {
			return goW2SMethodResult{method: fmt.Sprintf(`// %s calls %s.%s via W2S HTTP/JSON.
func (c *%s) %s(ctx context.Context) error {
%s
	_, err := c.doJSON(ctx, %q, %q, body, %s)
	return err
}`, method.Name, svc.Name, method.Name, clientName, method.Name, body, httpMethod, method.HttpPath, authLiteral)}
		}
		return goW2SMethodResult{method: fmt.Sprintf(`// %s calls %s.%s via W2S HTTP/JSON.
func (c *%s) %s(ctx context.Context, req *%s) error {
%s
	_, err := c.doJSON(ctx, %q, %q, body, %s)
	return err
}`, method.Name, svc.Name, method.Name, clientName, method.Name, goInput, body, httpMethod, method.HttpPath, authLiteral)}
	}

	if isEmptyInput {
		return goW2SMethodResult{method: fmt.Sprintf(`// %s calls %s.%s via W2S HTTP/JSON.
func (c *%s) %s(ctx context.Context) (*%s, error) {
%s
	data, err := c.doJSON(ctx, %q, %q, body, %s)
	if err != nil {
		return nil, err
	}
	var resp %s
	if err := decodeW2SProtoPayload(data, &resp); err != nil {
		return nil, err
	}
%s
	return &resp, nil
}`, method.Name, svc.Name, method.Name, clientName, method.Name, goOutput, body, httpMethod, method.HttpPath, authLiteral, goOutput,
			goW2SResponseValidation("resp", method.FullOutputType, method.OutputType, servicePackage, `nil, status.Error(codes.Internal, fmt.Sprintf("response validation failed: %v", err))`))}
	}

	return goW2SMethodResult{method: fmt.Sprintf(`// %s calls %s.%s via W2S HTTP/JSON.
func (c *%s) %s(ctx context.Context, req *%s) (*%s, error) {
%s
	data, err := c.doJSON(ctx, %q, %q, body, %s)
	if err != nil {
		return nil, err
	}
	var resp %s
	if err := decodeW2SProtoPayload(data, &resp); err != nil {
		return nil, err
	}
%s
	return &resp, nil
}`, method.Name, svc.Name, method.Name, clientName, method.Name, goInput, goOutput, body, httpMethod, method.HttpPath, authLiteral, goOutput,
		goW2SResponseValidation("resp", method.FullOutputType, method.OutputType, servicePackage, `nil, status.Error(codes.Internal, fmt.Sprintf("response validation failed: %v", err))`))}
}

func generateGoW2SBidiMethod(clientName string, svc *parser.ServiceInfo, method *parser.MethodInfo, goInput, goOutput, authLiteral, servicePackage string) goW2SMethodResult {
	handleType := method.Name + "W2SHandle"
	loopFnName := strutil.ToLowerCamelCase(method.Name) + "W2SLoop"
	runFnName := strutil.ToLowerCamelCase(method.Name) + "W2SRun"
	inputValidation := goW2SRequestValidation("msg", method.FullInputType, method.InputType, servicePackage, `status.Error(codes.InvalidArgument, fmt.Sprintf("validation failed: %v", err))`) +
		goW2SRouteCheck(method, "msg", "")
	var outputValidation string
	if validateOutput := goValidateRef(method.FullOutputType, method.OutputType, servicePackage); validateOutput != "" {
		outputValidation = fmt.Sprintf(`			if err := %s(&msg); err != nil {
				recvErrCh <- status.Error(codes.Internal, fmt.Sprintf("response validation failed: %%v", err))
				return
			}
`, validateOutput)
	}

	methodCode := fmt.Sprintf(`// %s opens a managed W2S bidirectional stream to %s.%s.
func (c *%s) %s(
	ctx context.Context,
	onConnect func(send func(*%s) error),
	onMessage func(*%s),
) *%s {
	h := &%s{
		client: c,
		authRequired: %s,
		userCh: make(chan w2sQueuedMessage, 32),
		doneCh: make(chan struct{}),
	}
	hCtx, hCancel := context.WithCancel(ctx)
	h.ctx = hCtx
	h.cancel = hCancel

	go c.%s(hCtx, h, onConnect, onMessage)
	return h
}`, method.Name, svc.Name, method.Name, clientName, method.Name, goInput, goOutput, handleType,
		handleType, authLiteral, loopFnName)

	extraType := fmt.Sprintf(`func (c *%s) %s(
	ctx context.Context,
	h *%s,
	onConnect func(send func(*%s) error),
	onMessage func(*%s),
) {
	defer close(h.doneCh)

	const minBackoff = 1 * time.Second
	const maxBackoff = 30 * time.Second
	backoff := minBackoff

	for {
		if ctx.Err() != nil {
			return
		}
		err := c.%s(ctx, h, onConnect, onMessage)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			zap.L().Warn("%s: W2S stream error, reconnecting", zap.Error(err), zap.Duration("backoff", backoff))
		}
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		if err != nil && backoff < maxBackoff {
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		} else if err == nil {
			backoff = minBackoff
		}
	}
}

func (c *%s) %s(
	ctx context.Context,
	h *%s,
	onConnect func(send func(*%s) error),
	onMessage func(*%s),
) error {
	ws, err := c.connectWebSocket(ctx, %q)
	if err != nil {
		return err
	}
	defer ws.Close()

	if err := c.sendStartFrame(ctx, ws, h.authRequired); err != nil {
		return err
	}

	send := func(msg *%s) error {
%s
		body, err := encodeW2SProto(msg)
		if err != nil {
			return err
		}
		return c.sendMessageFrame(ctx, ws, body, h.authRequired)
	}
	if onConnect != nil {
		onConnect(send)
	}

	recvErrCh := make(chan error, 1)
	go func() {
		for {
			payload, err := c.recvWebSocketPayload(ws)
			if err != nil {
				recvErrCh <- err
				return
			}
			var msg %s
			if err := decodeW2SProtoPayload(payload, &msg); err != nil {
				recvErrCh <- err
				return
			}
%s
			if onMessage != nil {
				onMessage(&msg)
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case err := <-recvErrCh:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case queued, ok := <-h.userCh:
			if !ok {
				return c.sendCompleteFrame(ctx, ws)
			}
			if err := c.sendMessageFrameWithAuth(ctx, ws, queued.body, queued.authorization); err != nil {
				if queued.ack != nil {
					queued.ack <- err
				}
				return err
			}
			if queued.ack != nil {
				queued.ack <- nil
			}
		}
	}
}

// %s is the handle for the managed %s W2S bidirectional stream.
type %s struct {
	mu           sync.Mutex
	closed       bool
	client       *%s
	authRequired bool
	ctx          context.Context
	userCh       chan w2sQueuedMessage
	doneCh       chan struct{}
	cancel       context.CancelFunc
}

// Send queues a message to be sent on the W2S bidirectional stream.
func (h *%s) Send(msg *%s) error {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return fmt.Errorf("%s W2S handle closed")
	}
	h.mu.Unlock()

%s
	body, err := encodeW2SProto(msg)
	if err != nil {
		return err
	}
	authorization, err := h.client.authHeader(h.ctx, h.authRequired)
	if err != nil {
		return err
	}
	ack := make(chan error, 1)

	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		return fmt.Errorf("%s W2S handle closed")
	}

	select {
	case h.userCh <- w2sQueuedMessage{body: body, authorization: authorization, ack: ack}:
		h.mu.Unlock()
	default:
		h.mu.Unlock()
		return fmt.Errorf("%s W2S send buffer full")
	}
	select {
	case err := <-ack:
		return err
	case <-h.ctx.Done():
		return h.ctx.Err()
	}
}

// Close tears down the W2S bidirectional stream and stops the reconnect loop.
func (h *%s) Close() {
	h.mu.Lock()
	if h.closed {
		h.mu.Unlock()
		<-h.doneCh
		return
	}
	h.closed = true
	close(h.userCh)
	h.mu.Unlock()
	if h.cancel != nil {
		h.cancel()
	}
	<-h.doneCh
}

// Done returns a channel closed when the lifecycle goroutine has exited.
func (h *%s) Done() <-chan struct{} { return h.doneCh }
`, clientName, loopFnName, handleType, goInput, goOutput, runFnName, method.Name,
		clientName, runFnName, handleType, goInput, goOutput, method.HttpPath, goInput, inputValidation, goOutput, outputValidation,
		handleType, method.Name, handleType, clientName, handleType, goInput, method.Name, inputValidation, method.Name, method.Name, handleType, handleType)

	return goW2SMethodResult{method: methodCode, extraType: extraType}
}
