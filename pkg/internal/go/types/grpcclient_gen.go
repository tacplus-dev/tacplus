// Copyright (c) 2026 Second Star Cavalier LLC

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

	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// generatedGrpcClient tracks info about a generated gRPC client wrapper for the aggregators.
type generatedGrpcClient struct {
	PackageName      string // e.g. "baseball"
	ClientName       string // e.g. "BaseballServiceClient" (typed client wrapper)
	ServiceName      string // proto service name: "BaseballService"
	TargetServiceID  string // service identity of the hosting service (e.g. "sentinel") used for mesh routing
	ImportAlias      string // e.g. "baseball_gc"
	ImportPath       string // e.g. "example.com/myproj/tacplus/sm/internal/grpc_clients/services/baseball"
	TypesImportAlias string // e.g. "baseball_types"
	TypesImportPath  string // e.g. "example.com/myproj/tacplus/types/services/baseball"
	ParentDir        string // e.g. "services"
}

func uniqueGrpcClientTargets(clients []generatedGrpcClient) []string {
	seen := make(map[string]bool)
	var targets []string
	for _, c := range clients {
		if c.TargetServiceID == "" || seen[c.TargetServiceID] {
			continue
		}
		seen[c.TargetServiceID] = true
		targets = append(targets, c.TargetServiceID)
	}
	sort.Strings(targets)
	return targets
}

// generateGrpcClients generates typed gRPC client wrappers for each selected gRPC service.
func generateGrpcClients(config *tactype.Config, servicesInfo []ServiceInfo, inputDir string) ([]generatedGrpcClient, error) {
	// Create descriptor parser with the proto root directory
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	var generated []generatedGrpcClient

	for _, svc := range servicesInfo {
		// Parse the proto file to get method information
		protoInfo, err := descriptorParser.ConvertToProtoInfo(svc.ProtoFile)
		if err != nil {
			return nil, fmt.Errorf("failed to parse %s: %w", svc.ProtoFile, err)
		}

		// Generate gRPC client wrapper in the grpc_clients output directory
		if err := generateGrpcClient(svc, protoInfo); err != nil {
			return nil, fmt.Errorf("failed to generate gRPC client for %s: %w", svc.ServiceName, err)
		}

		wrapperRelPath := svc.ParentDir + "/" + svc.ImportDirName()
		var typesImportPath string
		if svc.ParentDir == "services" {
			typesImportPath = fmt.Sprintf("%s/types/services/%s", config.ModuleBasePath, svc.ImportDirName())
		} else {
			typesImportPath = fmt.Sprintf("%s/types/%s/%s", config.ModuleBasePath, svc.ParentDir, svc.ImportDirName())
		}
		// Mesh routing target: use TargetService from GrpcEntry if set (virtual services
		// route to the physical host, e.g., OpaEval → opa), otherwise use the
		// proto package name which matches the service_id in ServiceHealthState.
		targetServiceID := svc.PackageName
		targetEntries := config.GrpcS2SClients
		switch config.GetSmPrefix() {
		case "c2sm":
			targetEntries = config.GrpcC2SClients
		case "c2dm":
			targetEntries = config.GrpcC2DClients
		}
		for _, entry := range targetEntries {
			if entry.ProtoFile == svc.ProtoFile && entry.TargetService != "" {
				targetServiceID = entry.TargetService
				break
			}
		}
		generated = append(generated, generatedGrpcClient{
			PackageName:      svc.PackageName,
			ClientName:       svc.ServiceName + "Client",
			ServiceName:      svc.ServiceName,
			TargetServiceID:  targetServiceID,
			ImportAlias:      svc.PackageName + "_gc",
			ImportPath:       config.ModuleBasePath + "/" + config.GetSmPrefix() + "/grpc/clients/" + wrapperRelPath,
			TypesImportAlias: svc.PackageName + "_types",
			TypesImportPath:  typesImportPath,
			ParentDir:        svc.ParentDir,
		})
	}

	// Generate per-group aggregators (e.g., grpc_clients/services/grpc_clients.go)
	if err := generateGrpcClientGroupFiles(config, generated); err != nil {
		return nil, fmt.Errorf("failed to generate gRPC client group aggregators: %w", err)
	}

	return generated, nil
}

// grpcClientGroupServiceEntry tracks a service for the group-level grpc_clients.go aggregator.
type grpcClientGroupServiceEntry struct {
	FieldName       string // unexported field: "baseballServiceClient"
	AccessorName    string // exported accessor: "BaseballServiceClient"
	ClientName      string // struct name: "BaseballServiceClient"
	ImportAlias     string // "baseball_gc"
	TargetServiceID string // service identity for mesh routing (e.g. "sentinel")
}

// grpcClientGroupImport tracks a unique import for the group template.
type grpcClientGroupImport struct {
	Alias string
	Path  string
}

// generateGrpcClientGroupFiles generates grpc_clients.go at each parent dir (e.g., grpc_clients/services/).
func generateGrpcClientGroupFiles(config *tactype.Config, clients []generatedGrpcClient) error {
	// Group clients by ParentDir
	type groupInfo struct {
		parentDir string
		services  []grpcClientGroupServiceEntry
		imports   []grpcClientGroupImport
	}
	groups := make(map[string]*groupInfo)
	seenImport := make(map[string]bool)

	for _, c := range clients {
		g, ok := groups[c.ParentDir]
		if !ok {
			g = &groupInfo{parentDir: c.ParentDir}
			groups[c.ParentDir] = g
		}

		alias := c.PackageName + "_gc"
		if !seenImport[alias] {
			seenImport[alias] = true
			g.imports = append(g.imports, grpcClientGroupImport{Alias: alias, Path: c.ImportPath})
		}

		fieldName := strings.ToLower(c.ClientName[:1]) + c.ClientName[1:]
		g.services = append(g.services, grpcClientGroupServiceEntry{
			FieldName:       fieldName,
			AccessorName:    c.ClientName,
			ClientName:      c.ClientName,
			ImportAlias:     alias,
			TargetServiceID: c.TargetServiceID,
		})
	}

	funcMap := template.FuncMap{
		"Title": func(s string) string {
			if len(s) == 0 {
				return s
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
	}

	for _, g := range groups {
		// Dedupe TargetServiceIDs — multiple proto services in the same
		// package (e.g., Token and TokenAdmin in the same proto package) share
		// one TargetServiceID, so a per-service emit would produce
		// duplicate Target<Foo> const declarations.
		seenTarget := make(map[string]bool)
		var uniqueTargets []string
		for _, s := range g.services {
			if !seenTarget[s.TargetServiceID] {
				seenTarget[s.TargetServiceID] = true
				uniqueTargets = append(uniqueTargets, s.TargetServiceID)
			}
		}

		data := struct {
			GroupPackageName         string
			Imports                  []grpcClientGroupImport
			Services                 []grpcClientGroupServiceEntry
			Targets                  []string
			ServiceConnectionLibPath string
		}{
			GroupPackageName:         g.parentDir,
			Imports:                  g.imports,
			Services:                 g.services,
			Targets:                  uniqueTargets,
			ServiceConnectionLibPath: config.ModuleBasePath + "/chassis/serviceconnection",
		}

		tmplStr, err := loadTemplate("grpcclient_group.go.tmpl")
		if err != nil {
			return fmt.Errorf("failed to load grpcclient group template: %w", err)
		}
		tmpl, err := template.New("grpcclient_group").Funcs(funcMap).Parse(tmplStr)
		if err != nil {
			return fmt.Errorf("failed to parse grpcclient group template: %w", err)
		}

		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			return fmt.Errorf("failed to execute grpcclient group template: %w", err)
		}

		formatted, fmtErr := format.Source(buf.Bytes())
		if fmtErr != nil {
			zap.L().Warn("failed to format grpcs.go, writing unformatted", zap.Error(fmtErr))
			formatted = buf.Bytes()
		}

		groupDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "grpc", "clients", g.parentDir)
		if err := os.MkdirAll(groupDir, 0755); err != nil {
			return fmt.Errorf("failed to create group directory: %w", err)
		}
		if err := os.WriteFile(filepath.Join(groupDir, "grpcs.go"), formatted, 0644); err != nil {
			return fmt.Errorf("failed to write grpcs.go for %s: %w", g.parentDir, err)
		}

		zap.L().Info("Generated group GrpcClients", zap.String("group", g.parentDir))
	}
	return nil
}

// grpcClientRootGroup tracks a group for the root gRPC client aggregator.
type grpcClientRootGroup struct {
	FieldName   string // "services"
	ImportAlias string // "services_gc"
	ImportPath  string // ".../sm/internal/grpc_clients/services"
}

// generateGrpcClientRoot generates sm/internal/grpc_clients/grpc_clients.go — the root gRPC client aggregator.
func generateGrpcClientRoot(config *tactype.Config, clients []generatedGrpcClient) error {
	// Group clients by ParentDir
	groupMap := make(map[string]bool)
	var groupOrder []string
	for _, c := range clients {
		if !groupMap[c.ParentDir] {
			groupMap[c.ParentDir] = true
			groupOrder = append(groupOrder, c.ParentDir)
		}
	}

	var groups []grpcClientRootGroup
	for _, gName := range groupOrder {
		groups = append(groups, grpcClientRootGroup{
			FieldName:   gName,
			ImportAlias: gName + "_gc",
			ImportPath:  config.ModuleBasePath + "/" + config.GetSmPrefix() + "/grpc/clients/" + gName,
		})
	}

	funcMap := template.FuncMap{
		"Title": func(s string) string {
			if len(s) == 0 {
				return s
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
	}

	data := struct {
		Groups                   []grpcClientRootGroup
		ServiceConnectionLibPath string
	}{
		Groups:                   groups,
		ServiceConnectionLibPath: config.ModuleBasePath + "/chassis/serviceconnection",
	}

	tmplStr, err := loadTemplate("grpcclient_root.go.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load grpcclient root template: %w", err)
	}
	tmpl, err := template.New("grpcclient_root").Funcs(funcMap).Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse grpcclient root template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute grpcclient root template: %w", err)
	}

	formatted, fmtErr := format.Source(buf.Bytes())
	if fmtErr != nil {
		zap.L().Warn("failed to format grpcs.go, writing unformatted", zap.Error(fmtErr))
		formatted = buf.Bytes()
	}

	grpcClientsDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "grpc", "clients")
	if err := os.MkdirAll(grpcClientsDir, 0755); err != nil {
		return fmt.Errorf("failed to create grpc_clients directory: %w", err)
	}

	outputPath := filepath.Join(grpcClientsDir, "grpcs.go")
	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write grpcs.go: %w", err)
	}

	zap.L().Info("Generated root GrpcClients", zap.String("path", outputPath))
	return nil
}

// generateGrpcClient creates the typed gRPC client wrapper file for a service.
func generateGrpcClient(svc ServiceInfo, info *parser.ProtoInfo) error {
	// Output to sm/internal/grpc_clients/{parentDir}/{name}/
	wrapperOutputDir := filepath.Join(svc.Config.OutputDir, svc.Config.GetSmPrefix(), "grpc", "clients", svc.ParentDir, svc.ImportDirName())
	if err := os.MkdirAll(wrapperOutputDir, 0755); err != nil {
		return fmt.Errorf("failed to create gRPC client output directory: %w", err)
	}
	// Determine filename, stripping redundant package prefix
	// (e.g., IdentityService in identity/ → service.go, not identity_service.go)
	clientFile := strutil.StripPackagePrefix(strutil.ToSnakeCase(svc.ServiceName), svc.ImportDirName()) + ".go"
	outputPath := filepath.Join(wrapperOutputDir, clientFile)

	zap.L().Debug("Generating gRPC client", zap.String("path", outputPath))

	// Filter to only the service being wrapped
	var targetServices []*parser.ServiceInfo
	for _, service := range info.Services {
		if service.Name == svc.ServiceName {
			targetServices = append(targetServices, service)
		}
	}

	// Build gRPC client method wrappers with validation
	var grpcMethods strings.Builder
	hasBidi := false
	hasNonBidi := false
	hasClientStreaming := false
	hasCollectionStream := false
	hasCollectionCursor := false
	hasPage := false
	for _, service := range targetServices {
		for _, method := range service.Methods {
			methodCode, err := generateGrpcClientMethod(service, method, info.Package, svc.ParentDir)
			if err != nil {
				return fmt.Errorf("failed to generate gRPC client method %s.%s: %w", service.Name, method.Name, err)
			}
			grpcMethods.WriteString(methodCode)
			grpcMethods.WriteString("\n")
			if method.ClientStreaming && method.ServerStreaming {
				hasBidi = true
			} else {
				hasNonBidi = true
			}
			if method.ClientStreaming && !method.ServerStreaming {
				hasClientStreaming = true
			}
			// Tier-2/3/WATCH collection clients drain the server stream with io.EOF.
			if isTier2SnapshotCollection(method) || method.IsTier3SnapshotCollection() || method.IsWatchCollection() {
				hasCollectionStream = true
			}
			// Tier-3 cursor and WATCH clients return an iter.Seq2; so does the PAGE
			// auto-pager (which also needs proto.Clone to advance the token).
			if method.IsTier3SnapshotCollection() || method.IsWatchCollection() {
				hasCollectionCursor = true
			}
			if method.IsPageCollection() {
				hasCollectionCursor = true
				hasPage = true
			}
		}
	}

	// Calculate types import path
	var typesModulePath string
	if svc.ParentDir == "services" {
		typesModulePath = fmt.Sprintf("%s/types/services/%s", svc.Config.ModuleBasePath, svc.ImportDirName())
	} else {
		typesModulePath = fmt.Sprintf("%s/types/%s/%s", svc.Config.ModuleBasePath, svc.ParentDir, svc.ImportDirName())
	}
	grpcPkgName := svc.PackageName

	// Build cross-package import lines
	usedPkgs := make(map[string]bool)
	for _, service := range targetServices {
		for _, method := range service.Methods {
			for _, fullType := range []string{method.FullInputType, method.FullOutputType} {
				parts := strings.Split(fullType, ".")
				if len(parts) > 1 {
					pkg := strings.Join(parts[:len(parts)-1], ".")
					if pkg != info.Package {
						usedPkgs[pkg] = true
					}
				}
			}
		}
	}
	var crossPkgImports strings.Builder
	emitted := make(map[string]bool)
	// Handle well-known google.protobuf types with proper Go imports.
	// Both input and output types always need the import when they are a
	// well-known type: for empty-output unary methods, *emptypb.Empty is
	// still referenced as the retry closure's return type even though the
	// value itself is discarded (_, err := ...), and for empty-input
	// methods the &emptypb.Empty{} literal is constructed directly.
	for _, service := range targetServices {
		for _, method := range service.Methods {
			typesToCheck := []string{method.FullInputType, method.FullOutputType}
			for _, fullType := range typesToCheck {
				if strings.HasPrefix(fullType, "google.protobuf.") {
					typeName := fullType[len("google.protobuf."):]
					if wkt, ok := wellKnownGoType[typeName]; ok && !emitted[wkt.alias] {
						fmt.Fprintf(&crossPkgImports, "\n%s \"%s\"", wkt.alias, wkt.importPath)
						emitted[wkt.alias] = true
					}
				}
			}
		}
	}
	for pkg, dir := range info.CrossPackageTypes {
		if dir == "" || !usedPkgs[pkg] || pkg == "google.protobuf" {
			continue
		}
		importAlias := pkg + "_pb"
		var importPath string
		if filepath.Base(dir) == pkg {
			importPath = fmt.Sprintf("%s/types/%s", svc.Config.ModuleBasePath, dir)
		} else {
			importPath = fmt.Sprintf("%s/types/%s/%s", svc.Config.ModuleBasePath, dir, pkg)
		}
		if strings.HasPrefix(dir, "services") {
			importPath = fmt.Sprintf("%s/types/%s", svc.Config.ModuleBasePath, dir)
		}
		fmt.Fprintf(&crossPkgImports, "\n%s \"%s\"", importAlias, importPath)
	}

	clientName := svc.ServiceName + "Client"

	// Replace {{.ClientName}} placeholder in helper-generated strings
	replaceClient := func(s string) string {
		return strings.ReplaceAll(s, "{{.ClientName}}", clientName)
	}

	// Count total methods and methods that need fmt (non-streaming methods with validation).
	totalMethods := 0
	hasValidationMethods := false
	hasAuthMethods := false
	hasRequiresDevice := false
	for _, service := range targetServices {
		totalMethods += len(service.Methods)
		for _, m := range service.Methods {
			if m.RequiresDevice {
				hasRequiresDevice = true
			}
			if !(m.ClientStreaming && m.ServerStreaming) && !m.ClientStreaming {
				// Server streaming and unary methods use fmt.Sprintf in validation code
				hasValidationMethods = true
			}
			if grpcClientAuthRequired(service, m) {
				hasAuthMethods = true
			}
		}
	}

	data := struct {
		GrpcPackageName       string
		TypesModulePath       string
		CrossPackageImports   string
		ServiceName           string
		ClientName            string
		GRPCMethods           string
		HasMethods            bool
		HasValidation         bool
		HasBidi               bool
		HasNonBidi            bool
		HasClientStreaming    bool
		HasAuth               bool
		HasCollectionStream   bool
		HasCollectionCursor   bool
		HasPage               bool
		HasRequiresDevice     bool
		ApprovalLibImportPath string
	}{
		GrpcPackageName:       grpcPkgName,
		TypesModulePath:       typesModulePath,
		CrossPackageImports:   crossPkgImports.String(),
		ServiceName:           svc.ServiceName,
		ClientName:            clientName,
		GRPCMethods:           replaceClient(grpcMethods.String()),
		HasMethods:            totalMethods > 0,
		HasValidation:         hasValidationMethods,
		HasBidi:               hasBidi,
		HasNonBidi:            hasNonBidi,
		HasClientStreaming:    hasClientStreaming,
		HasAuth:               hasAuthMethods,
		HasCollectionStream:   hasCollectionStream,
		HasCollectionCursor:   hasCollectionCursor,
		HasPage:               hasPage,
		HasRequiresDevice:     hasRequiresDevice,
		ApprovalLibImportPath: svc.Config.ModuleBasePath + "/chassis/approval",
	}

	tmpl, err := loadTemplate("grpcclient.go.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load grpcclient template: %w", err)
	}

	t, err := template.New("grpcclient").Parse(tmpl)
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	// Format the generated code
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		zap.L().Warn("failed to format gRPC client", zap.Error(err))
		formatted = buf.Bytes()
	}

	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write gRPC client: %w", err)
	}

	return nil
}

// authSnippet returns a Go code block that injects a Bearer token into the
// outgoing gRPC context for methods that require authentication.
// When errorOnly is true, the return statement omits the nil first value
// (for methods that return just error, not (T, error)).
func grpcClientAuthRequired(svc *parser.ServiceInfo, method *parser.MethodInfo) bool {
	return !(svc.AuthDisabled || svc.IsC2D || method.AuthDisabled)
}

func authSnippet(authRequired, errorOnly bool) string {
	if !authRequired {
		return ""
	}
	missingRet := `return nil, status.Error(codes.Unauthenticated, "auth token provider is required")`
	errRet := `return nil, status.Errorf(codes.Unauthenticated, "auth token: %v", err)`
	emptyRet := `return nil, status.Error(codes.Unauthenticated, "auth token is empty")`
	if errorOnly {
		missingRet = `return status.Error(codes.Unauthenticated, "auth token provider is required")`
		errRet = `return status.Errorf(codes.Unauthenticated, "auth token: %v", err)`
		emptyRet = `return status.Error(codes.Unauthenticated, "auth token is empty")`
	}
	return fmt.Sprintf(`// Inject auth token.
	if c.tokenProvider == nil {
		%s
	}
	token, err := c.tokenProvider()
	if err != nil {
		%s
	}
	if token == "" {
		%s
	}
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token)`, missingRet, errRet, emptyRet)
}

// generateGrpcClientMethod generates a typed method for the gRPC client wrapper.
// Handles all four streaming types.
func generateGrpcClientMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, servicePackage, _parentDir string) (string, error) {
	goInput := goTypeRef(method.FullInputType, method.InputType, servicePackage)
	goOutput := goTypeRef(method.FullOutputType, method.OutputType, servicePackage)

	// Determine if this is an error-only return method (empty output, unary)
	emptyOut := isEmptyType(method.FullOutputType)
	isUnary := !method.ClientStreaming && !method.ServerStreaming
	errorOnly := emptyOut && isUnary
	authRequired := grpcClientAuthRequired(svc, method)
	auth := authSnippet(authRequired, errorOnly)

	if method.ClientStreaming && method.ServerStreaming {
		// Bidirectional streaming — managed lifecycle with reconnection.
		// See pkg/templates/go/types/grpcclient_bidi.go.tmpl for the body.
		return generateGoBidiMethod(svc, method, goInput, goOutput, authRequired, goValidateRef(method.FullInputType, method.InputType, servicePackage), goValidateRef(method.FullOutputType, method.OutputType, servicePackage))
	} else if isTier2SnapshotCollection(method) {
		// Tier-2 SNAPSHOT collection: server-streaming on the wire, but the client
		// seam returns a materialized collection — drain the stream into a slice.
		validateInput := goValidateRef(method.FullInputType, method.InputType, servicePackage)
		var inputVal string
		if validateInput != "" {
			inputVal = fmt.Sprintf(`
	// Validate request before sending.
	if err := %s(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("validation failed: %%v", err))
	}
`, validateInput)
		}
		return fmt.Sprintf(`// %s calls %s.%s (Tier-2 SNAPSHOT collection over server streaming).
func (c *{{.ClientName}}) %s(ctx context.Context, req *%s, opts ...grpc.CallOption) ([]*%s, error) {
	%s
%s
	stream, err := c.client.%s(ctx, req, opts...)
	if err != nil {
		return nil, err
	}
	var items []*%s
	for {
		item, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}`, method.Name, svc.Name, method.Name, method.Name, goInput, goOutput, auth, inputVal, method.Name, goOutput), nil
	} else if method.IsTier3SnapshotCollection() || method.IsWatchCollection() {
		// Tier-3 SNAPSHOT cursor / WATCH: server-streaming on the wire, surfaced as
		// a lazy iterator over the streamed message (the item for Tier-3, the
		// {Method}WatchEvent envelope for WATCH; from_revision rides in the request).
		// Breaking out of the range (or finishing) cancels the stream context, which
		// tears down the server-side producer (deterministic teardown).
		validateInput := goValidateRef(method.FullInputType, method.InputType, servicePackage)
		var inputVal string
		if validateInput != "" {
			inputVal = fmt.Sprintf(`
	// Validate request before sending.
	if err := %s(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("validation failed: %%v", err))
	}
`, validateInput)
		}
		return fmt.Sprintf(`// %s calls %s.%s (Tier-3 SNAPSHOT cursor over server streaming).
func (c *{{.ClientName}}) %s(ctx context.Context, req *%s, opts ...grpc.CallOption) (iter.Seq2[*%s, error], error) {
	%s
%s
	streamCtx, cancel := context.WithCancel(ctx)
	stream, err := c.client.%s(streamCtx, req, opts...)
	if err != nil {
		cancel()
		return nil, err
	}
	return func(yield func(*%s, error) bool) {
		defer cancel()
		for {
			item, err := stream.Recv()
			if err == io.EOF {
				return
			}
			if err != nil {
				yield(nil, err)
				return
			}
			if !yield(item, nil) {
				return
			}
		}
	}, nil
}`, method.Name, svc.Name, method.Name, method.Name, goInput, goOutput, auth, inputVal, method.Name, goOutput), nil
	} else if method.IsPageCollection() {
		// PAGE: unary on the wire. Two client seams — an explicit single-page call
		// (%sPage → items, next_page_token, total_count) and an auto-pager (the
		// natural method name) that follows next_page_token until exhausted,
		// yielding each item. The auto-pager advances the token on a clone, so the
		// caller's request is never mutated.
		item := goWatchType(method.Resolved.PageItemType, servicePackage)
		itemsGetter := "Get" + strutil.ToPascalCase(method.Resolved.PageItemsField)
		validateInput := goValidateRef(method.FullInputType, method.InputType, servicePackage)
		retryAuth := authSnippet(authRequired, false)
		var inputVal string
		if validateInput != "" {
			inputVal = fmt.Sprintf(`
	// Validate request before sending.
	if err := %s(req); err != nil {
		return nil, "", 0, status.Error(codes.InvalidArgument, fmt.Sprintf("validation failed: %%v", err))
	}
`, validateInput)
		}
		totalExpr := "-1" // unknown when the response declares no total_count
		if method.Resolved.PageHasTotal {
			totalExpr = "resp.GetTotalCount()"
		}
		return fmt.Sprintf(`// %sPage calls %s.%s and returns a single page: the items, the
// next_page_token (empty when this is the last page), and total_count (-1 when
// the response carries no count).
func (c *{{.ClientName}}) %sPage(ctx context.Context, req *%s, opts ...grpc.CallOption) ([]*%s, string, int64, error) {
%s
	resp, err := {{.ClientName}}UnaryWithRetry(c, ctx, true, func(callCtx context.Context) (*%s, error) {
		ctx := callCtx
		%s
		return c.client.%s(ctx, req, opts...)
	})
	if err != nil {
		return nil, "", 0, err
	}
	return resp.%s(), resp.GetNextPageToken(), %s, nil
}

// %s calls %s.%s as an auto-pager: it follows next_page_token until exhausted,
// yielding each item across pages. Break out of the range to stop early. The
// caller's req is not mutated — the page token is advanced on a clone.
func (c *{{.ClientName}}) %s(ctx context.Context, req *%s, opts ...grpc.CallOption) iter.Seq2[*%s, error] {
	return func(yield func(*%s, error) bool) {
		cur := proto.Clone(req).(*%s)
		for {
			items, next, _, err := c.%sPage(ctx, cur, opts...)
			if err != nil {
				yield(nil, err)
				return
			}
			for _, it := range items {
				if !yield(it, nil) {
					return
				}
			}
			if next == "" {
				return
			}
			cur.PageToken = next
		}
	}
}`, method.Name, svc.Name, method.Name,
			method.Name, goInput, item, inputVal, goOutput, retryAuth, method.Name, itemsGetter, totalExpr,
			method.Name, svc.Name, method.Name,
			method.Name, goInput, item, item, goInput, method.Name), nil
	} else if method.ServerStreaming {
		// Server streaming — validate request, call, return stream
		validateInput := goValidateRef(method.FullInputType, method.InputType, servicePackage)
		var inputVal string
		if validateInput != "" {
			inputVal = fmt.Sprintf(`
	// Validate request before sending.
	if err := %s(req); err != nil {
		return nil, status.Error(codes.InvalidArgument, fmt.Sprintf("validation failed: %%v", err))
	}
`, validateInput)
		}
		return fmt.Sprintf(`// %s calls %s.%s (server streaming).
func (c *{{.ClientName}}) %s(ctx context.Context, req *%s, opts ...grpc.CallOption) (grpc.ServerStreamingClient[%s], error) {
	%s
%s
	return c.client.%s(ctx, req, opts...)
}`, method.Name, svc.Name, method.Name, method.Name, goInput, goOutput, auth, inputVal, method.Name), nil
	} else if method.ClientStreaming {
		// Client streaming — call, return stream sender. Auth is stream-open
		// scoped (the `auth` block below attaches the token to the stream
		// headers); no per-send token preflight (PC-W-2, matches bidi).
		validateInput := validationFuncOrNil(goValidateRef(method.FullInputType, method.InputType, servicePackage))
		validateOutput := validationFuncOrNil(goValidateRef(method.FullOutputType, method.OutputType, servicePackage))
		return fmt.Sprintf(`// %s calls %s.%s (client streaming).
func (c *{{.ClientName}}) %s(ctx context.Context, opts ...grpc.CallOption) (grpc.ClientStreamingClient[%s, %s], error) {
	%s

	stream, err := c.client.%s(ctx, opts...)
	if err != nil {
		return nil, err
	}
	return &{{.ClientName}}ClientStreamingClient[%s, %s]{
		ClientStreamingClient: stream,
		validateReq: %s,
		validateResp: %s,
	}, nil
}`, method.Name, svc.Name, method.Name, method.Name, goInput, goOutput, auth, method.Name, goInput, goOutput, validateInput, validateOutput), nil
	}

	// Unary method — validate request, call, validate response, map errors
	emptyInput := isEmptyType(method.FullInputType)
	emptyOutput := isEmptyType(method.FullOutputType)
	validateInput := goValidateRef(method.FullInputType, method.InputType, servicePackage)
	validateOutput := goValidateRef(method.FullOutputType, method.OutputType, servicePackage)
	retryAuth := authSnippet(authRequired, false)
	retrySafe := method.IsRetrySafeUnary()
	deviceHelpers := goRequiresDeviceClientHelpers(svc, method, goInput, goOutput, emptyInput, emptyOutput)

	// Build input validation block (skip for Empty input)
	var inputValidation string
	if !emptyInput && validateInput != "" {
		inputValidation = fmt.Sprintf(`
	// Validate request before sending.
	if err := %s(req); err != nil {
		return %s
	}
`, validateInput, "%s") // %s is filled per-branch below
	}

	// When output is Empty, return just error; when input is Empty, omit req param
	if emptyOutput {
		// Empty output: client method returns error only
		if emptyInput {
			return fmt.Sprintf(`// %s calls %s.%s with optional validation and error mapping.
func (c *{{.ClientName}}) %s(ctx context.Context, opts ...grpc.CallOption) error {
	_, err := {{.ClientName}}UnaryWithRetry(c, ctx, %t, func(callCtx context.Context) (*emptypb.Empty, error) {
		ctx := callCtx
		%s
		return c.client.%s(ctx, &emptypb.Empty{}, opts...)
	})
	return err
}`+deviceHelpers, method.Name, svc.Name, method.Name, method.Name, retrySafe, retryAuth, method.Name), nil
		}
		inputVal := ""
		if inputValidation != "" {
			inputVal = fmt.Sprintf(inputValidation, `status.Error(codes.InvalidArgument, fmt.Sprintf("validation failed: %v", err))`)
		}
		return fmt.Sprintf(`// %s calls %s.%s with optional validation and error mapping.
func (c *{{.ClientName}}) %s(ctx context.Context, req *%s, opts ...grpc.CallOption) error {
%s
	_, err := {{.ClientName}}UnaryWithRetry(c, ctx, %t, func(callCtx context.Context) (*emptypb.Empty, error) {
		ctx := callCtx
		%s
		return c.client.%s(ctx, req, opts...)
	})
	return err
}`+deviceHelpers, method.Name, svc.Name, method.Name, method.Name, goInput, inputVal, retrySafe, retryAuth, method.Name), nil
	}

	// Build output validation block (skip for Empty output)
	var outputValidation string
	if !emptyOutput && validateOutput != "" {
		outputValidation = fmt.Sprintf(`
	// Validate response after receiving.
	if err := %s(resp); err != nil {
		return nil, status.Error(codes.Internal, fmt.Sprintf("response validation failed: %%v", err))
	}
`, validateOutput)
	}

	inputValStr := ""
	if inputValidation != "" {
		inputValStr = fmt.Sprintf(inputValidation, `nil, status.Error(codes.InvalidArgument, fmt.Sprintf("validation failed: %v", err))`)
	}

	if emptyInput {
		return fmt.Sprintf(`// %s calls %s.%s with optional validation and error mapping.
func (c *{{.ClientName}}) %s(ctx context.Context, opts ...grpc.CallOption) (*%s, error) {
	resp, err := {{.ClientName}}UnaryWithRetry(c, ctx, %t, func(callCtx context.Context) (*%s, error) {
		ctx := callCtx
		%s
		return c.client.%s(ctx, &emptypb.Empty{}, opts...)
	})
	if err != nil {
		return nil, err
	}
%s
	return resp, nil
}`+deviceHelpers, method.Name, svc.Name, method.Name, method.Name, goOutput, retrySafe, goOutput, retryAuth, method.Name, outputValidation), nil
	}

	return fmt.Sprintf(`// %s calls %s.%s with optional validation and error mapping.
func (c *{{.ClientName}}) %s(ctx context.Context, req *%s, opts ...grpc.CallOption) (*%s, error) {
%s
	resp, err := {{.ClientName}}UnaryWithRetry(c, ctx, %t, func(callCtx context.Context) (*%s, error) {
		ctx := callCtx
		%s
		return c.client.%s(ctx, req, opts...)
	})
	if err != nil {
		return nil, err
	}
%s
	return resp, nil
}`+deviceHelpers, method.Name, svc.Name, method.Name, method.Name, goInput, goOutput, inputValStr, retrySafe, goOutput, retryAuth, method.Name, outputValidation), nil
}

// goRequiresDeviceClientHelpers renders the caller-side half of
// (spec.rpc).requires_device for one unary rpc, or "" when the rpc declares
// none. Two seams, because the caller owns two distinct steps and tacplus owns
// neither of them wholly:
//
//   - <Method>ApprovalHash gives the caller the request hash it must propose the
//     pending approval with. It is the SAME shared canonicalization the
//     server-side check recomputes from the live request, so the two ends cannot
//     drift; every product hand-rolling this is exactly what it prevents.
//   - <Method>WithApproval attaches the approval id and the device assertion as
//     CALL METADATA and makes the ordinary call. Metadata is outside the request
//     message, which is what keeps both values outside the hashed bytes.
//
// Proposing the approval itself (kernel PersonalIdentity.CreatePendingApproval)
// and obtaining the device assertion are the caller's own business — not
// generated here, and deliberately so: an approval is proposed by a person, not
// by a service's client stub.
func goRequiresDeviceClientHelpers(svc *parser.ServiceInfo, method *parser.MethodInfo, goInput, goOutput string, emptyInput, emptyOutput bool) string {
	if !method.RequiresDevice {
		return ""
	}
	hashParams := fmt.Sprintf("req *%s", goInput)
	hashArg := "req"
	callParams := fmt.Sprintf("ctx context.Context, req *%s, approvalID string, deviceAssertion []byte, opts ...grpc.CallOption", goInput)
	callArgs := "req, opts..."
	if emptyInput {
		hashParams = ""
		hashArg = "&emptypb.Empty{}"
		callParams = "ctx context.Context, approvalID string, deviceAssertion []byte, opts ...grpc.CallOption"
		callArgs = "opts..."
	}
	callResult := fmt.Sprintf("(*%s, error)", goOutput)
	if emptyOutput {
		callResult = "error"
	}
	return fmt.Sprintf(`

// %sApprovalHash is the request_hash a pending approval for this call must be
// created with. It is computed by the same shared canonicalization the
// generated server-side (spec.rpc).requires_device check recomputes from the
// live request, so the proposed approval and the served call cannot disagree
// about what was approved.
func (c *{{.ClientName}}) %sApprovalHash(%s) ([]byte, error) {
	return approval.RequestHash(%s)
}

// %sWithApproval calls %s.%s carrying a device-only approval: approvalID names
// the pending approval the person proposed, and deviceAssertion is the
// serialized WebauthnAssertion of the approving tap. Both ride as call
// metadata — never as request fields — which is what keeps them outside the
// hashed request.
func (c *{{.ClientName}}) %sWithApproval(%s) %s {
	return c.%s(approval.AttachOutgoing(ctx, approvalID, deviceAssertion), %s)
}`,
		method.Name, method.Name, hashParams, hashArg,
		method.Name, svc.Name, method.Name,
		method.Name, callParams, callResult,
		method.Name, callArgs)
}

// generateGoBidiMethod renders the managed bidi-stream lifecycle wrapper for
// a single bidi RPC. It loads pkg/templates/go/types/grpcclient_bidi.go.tmpl
// and substitutes per-method names/types. The {{.ClientName}} placeholder is
// left in place so the outer generator can apply replaceClient.
func generateGoBidiMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, goInput, goOutput string, authRequired bool, validateInput, validateOutput string) (string, error) {
	handleType := svc.Name + method.Name + "BidiHandle"
	loopFnName := strutil.ToLowerCamelCase(method.Name) + "BidiLoop"
	runFnName := strutil.ToLowerCamelCase(method.Name) + "BidiRun"

	tmplStr, err := loadTemplate("grpcclient_bidi.go.tmpl")
	if err != nil {
		return "", fmt.Errorf("failed to load go bidi template: %w", err)
	}

	t, err := template.New("grpcclient_bidi").Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("failed to parse go bidi template: %w", err)
	}

	data := struct {
		MethodName     string
		ServiceName    string
		ClientName     string
		GoInput        string
		GoOutput       string
		HandleType     string
		LoopFnName     string
		RunFnName      string
		MinBackoff     string
		MaxBackoff     string
		AuthRequired   bool
		ValidateInput  string
		ValidateOutput string
	}{
		MethodName:     method.Name,
		ServiceName:    svc.Name,
		ClientName:     "{{.ClientName}}",
		GoInput:        goInput,
		GoOutput:       goOutput,
		HandleType:     handleType,
		LoopFnName:     loopFnName,
		RunFnName:      runFnName,
		MinBackoff:     "1 * time.Second",
		MaxBackoff:     "30 * time.Second",
		AuthRequired:   authRequired,
		ValidateInput:  validateInput,
		ValidateOutput: validateOutput,
	}

	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute go bidi template: %w", err)
	}
	return buf.String(), nil
}
