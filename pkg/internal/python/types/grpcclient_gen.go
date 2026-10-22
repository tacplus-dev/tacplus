// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// generateGrpcClients generates typed gRPC client wrappers for each gRPC service.
func generateGrpcClients(servicesInfo []ServiceInfo, inputDir string) error {
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	for _, svc := range servicesInfo {
		protoInfo, err := descriptorParser.ConvertToProtoInfo(svc.ProtoFile)
		if err != nil {
			return fmt.Errorf("failed to parse %s: %w", svc.ProtoFile, err)
		}

		if err := generateGrpcClient(svc, protoInfo); err != nil {
			return fmt.Errorf("failed to generate gRPC client for %s: %w", svc.ServiceName, err)
		}
	}

	// Generate aggregator files
	if len(servicesInfo) > 0 {
		if err := generatePyGrpcClientAggregators(servicesInfo); err != nil {
			return fmt.Errorf("failed to generate gRPC client aggregators: %w", err)
		}
	}

	return nil
}

// resolveTarget determines the physical service ID for routing.
// Priority: TargetService from the active client surface > HostLabel > PackageName.
func resolveTarget(svc ServiceInfo) string {
	config := svc.Config
	target := svc.PackageName
	if svc.HostLabel != "" {
		target = svc.HostLabel
	}
	for _, entry := range pythonGrpcTargetEntries(config) {
		if pythonGrpcEntryMatchesService(entry, svc) && entry.TargetService != "" {
			target = entry.TargetService
			break
		}
	}
	return target
}

func pythonGrpcTargetEntries(config *tactype.Config) []tactype.GrpcEntry {
	switch config.GetSmPrefix() {
	case "c2sm":
		return config.GrpcC2SClients
	case "c2dm":
		return config.GrpcC2DClients
	case "w2sm":
		return config.GrpcW2SClients
	default:
		return config.GrpcS2SClients
	}
}

func pythonGrpcEntryMatchesService(entry tactype.GrpcEntry, svc ServiceInfo) bool {
	if entry.ProtoPackage != "" {
		return entry.ProtoPackage == svc.PackageName
	}
	if entry.ProtoFile == svc.ProtoFile {
		return true
	}
	return strings.TrimSuffix(filepath.Base(entry.ProtoFile), ".proto") == svc.PackageName
}

// generatePyGrpcClientAggregators creates a single grpc_clients.py with per-target
// client groups and a root GrpcClients class that takes per-target ChannelProviders.
func generatePyGrpcClientAggregators(servicesInfo []ServiceInfo) error {
	if len(servicesInfo) == 0 {
		return nil
	}
	config := servicesInfo[0].Config
	smDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "internal", "grpc", "clients")

	if err := os.MkdirAll(smDir, 0755); err != nil {
		return fmt.Errorf("failed to create clients directory: %w", err)
	}
	// Ensure __init__.py exists
	initPath := filepath.Join(smDir, "__init__.py")
	if _, err := os.Stat(initPath); os.IsNotExist(err) {
		os.WriteFile(initPath, []byte("# Auto-generated sm package\n"), 0644)
	}

	// Group by target service (like Rust does)
	type targetEntry struct {
		target    string
		fieldName string
		className string
		svcs      []ServiceInfo
	}
	var targets []targetEntry
	targetSeen := make(map[string]int)
	for _, svc := range servicesInfo {
		target := resolveTarget(svc)
		safeTarget := strings.ReplaceAll(target, "-", "_")
		if idx, ok := targetSeen[target]; ok {
			targets[idx].svcs = append(targets[idx].svcs, svc)
		} else {
			targetSeen[target] = len(targets)
			targets = append(targets, targetEntry{
				target:    target,
				fieldName: strutil.ToSnakeCase(safeTarget),
				className: strutil.ToPascalCase(safeTarget) + "Clients",
				svcs:      []ServiceInfo{svc},
			})
		}
	}

	rootFile := filepath.Join(smDir, "grpc_clients.py")
	var buf strings.Builder
	buf.WriteString(header.GeneratedHeader(consts.LangPython, "grpc_clients"))
	buf.WriteString("\n")

	// Import all individual client classes
	for _, svc := range servicesInfo {
		clientName := svc.ServiceName + "GrpcClient"
		pkgName := pyModuleName(svc.PackageName)
		moduleSnake := strutil.StripPackagePrefix(pyGeneratedSnakeName(svc.ServiceName), pkgName)
		fmt.Fprintf(&buf, "from .%s.%s.%s import %s\n", pyDottedPath(svc.ParentDir), pkgName, moduleSnake, clientName)
	}
	buf.WriteString("\n")

	// Per-target {Target}Clients classes
	for _, te := range targets {
		fmt.Fprintf(&buf, "\nclass %s:\n", te.className)
		fmt.Fprintf(&buf, "    \"\"\"Typed gRPC clients for the \\\"%s\\\" service.\"\"\"\n\n", te.target)
		fmt.Fprintf(&buf, "    def __init__(self, cp, token_provider=None):\n")
		for _, svc := range te.svcs {
			fieldName := pyGeneratedSnakeName(svc.ServiceName) + "_client"
			clientName := svc.ServiceName + "GrpcClient"
			fmt.Fprintf(&buf, "        self._%s = %s(cp, token_provider)\n", fieldName, clientName)
		}
		buf.WriteString("\n")
		for _, svc := range te.svcs {
			fieldName := pyGeneratedSnakeName(svc.ServiceName) + "_client"
			clientName := svc.ServiceName + "GrpcClient"
			fmt.Fprintf(&buf, "    @property\n    def %s(self) -> %s:\n        return self._%s\n\n", fieldName, clientName, fieldName)
		}
	}

	// GrpcClients is the root class holding all targets. Managers expose it through
	// transport-neutral accessors such as grpc_clients and grpc().
	buf.WriteString("\nclass GrpcClients:\n")
	buf.WriteString("    \"\"\"gRPC client hierarchy: sm.grpc().{target}().{client}()\"\"\"\n\n")
	buf.WriteString("    def __init__(self,\n")
	for _, te := range targets {
		fmt.Fprintf(&buf, "                 %s_cp,\n", te.fieldName)
	}
	buf.WriteString("                 token_provider_for=None):\n")
	for _, te := range targets {
		fmt.Fprintf(&buf, "        self._%s = %s(%s_cp, token_provider_for('%s') if token_provider_for else None)\n",
			te.fieldName, te.className, te.fieldName, te.target)
	}
	buf.WriteString("\n")
	for _, te := range targets {
		fmt.Fprintf(&buf, "    @property\n    def %s(self) -> %s:\n        return self._%s\n\n",
			te.fieldName, te.className, te.fieldName)
	}

	if err := os.WriteFile(rootFile, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write gRPC clients: %w", err)
	}

	// Also write per-parentDir group files (individual client re-exports for existing import paths)
	groups := make(map[string][]ServiceInfo)
	for _, svc := range servicesInfo {
		groups[svc.ParentDir] = append(groups[svc.ParentDir], svc)
	}
	for parentDir, svcs := range groups {
		groupDir := filepath.Join(smDir, pyPath(parentDir))
		if err := os.MkdirAll(groupDir, 0755); err != nil {
			return fmt.Errorf("failed to create group directory: %w", err)
		}
		groupFile := filepath.Join(groupDir, "grpc_clients.py")
		var gbuf strings.Builder
		gbuf.WriteString(header.GeneratedHeader(consts.LangPython, "grpc_clients"))
		gbuf.WriteString("\n")
		for _, svc := range svcs {
			clientName := svc.ServiceName + "GrpcClient"
			pkgName := pyModuleName(svc.PackageName)
			moduleSnake := strutil.StripPackagePrefix(pyGeneratedSnakeName(svc.ServiceName), pkgName)
			fmt.Fprintf(&gbuf, "from .%s.%s import %s\n", pkgName, moduleSnake, clientName)
		}
		if err := os.WriteFile(groupFile, []byte(gbuf.String()), 0644); err != nil {
			return fmt.Errorf("failed to write group aggregator: %w", err)
		}
	}

	return nil
}

// generateGrpcClient creates the typed gRPC client wrapper Python file for a service.
func generateGrpcClient(svc ServiceInfo, info *parser.ProtoInfo) error {
	// Output to sm/internal/grpc/clients/{parentDir}/{packageName}/
	pkgName := pyModuleName(svc.PackageName)
	outputDir := filepath.Join(svc.Config.OutputDir, svc.Config.GetSmPrefix(), "internal", "grpc", "clients", pyPath(svc.ParentDir), pkgName)

	// Create __init__.py files for each directory in the sm path
	smBase := filepath.Join(svc.Config.OutputDir, svc.Config.GetSmPrefix())
	for _, dir := range []string{
		smBase,
		filepath.Join(smBase, "internal"),
		filepath.Join(smBase, "internal", "grpc"),
		filepath.Join(smBase, "internal", "grpc", "clients"),
		filepath.Join(smBase, "internal", "grpc", "clients", pyPath(svc.ParentDir)),
		outputDir,
	} {
		initPath := filepath.Join(dir, "__init__.py")
		if _, err := os.Stat(initPath); os.IsNotExist(err) {
			os.MkdirAll(dir, 0755)
			if err := os.WriteFile(initPath, []byte("# Auto-generated sm package\n"), 0644); err != nil {
				return fmt.Errorf("failed to create __init__.py: %w", err)
			}
		}
	}

	wrapperFile := strutil.StripPackagePrefix(pyGeneratedSnakeName(svc.ServiceName), pkgName) + ".py"
	outputPath := filepath.Join(outputDir, wrapperFile)

	zap.L().Info("Generating Python gRPC client", zap.String("path", outputPath))

	// Find the target service
	var targetService *parser.ServiceInfo
	for i := range info.Services {
		if info.Services[i].Name == svc.ServiceName {
			targetService = info.Services[i]
			break
		}
	}
	if targetService == nil {
		return fmt.Errorf("service %s not found in proto info for %s", svc.ServiceName, svc.ProtoFile)
	}

	pbModuleName := pyProtoFileModuleName(svc.ProtoFile)

	// Compute relative import path from wrapper dir to types dir
	parentParts := strings.Split(svc.ParentDir, string(filepath.Separator))
	wrapperDepth := 4 + len(parentParts) + 1 // sm + internal + grpc + clients + parentDir parts + packageName
	dots := strings.Repeat(".", wrapperDepth+1)
	pbImportFrom := dots + "types." + pyProtoImportPath(svc.ParentDir, svc.PackageName, svc.ProtoFile)

	// Build cross-package imports
	usedPkgs := make(map[string]bool)
	for _, method := range targetService.Methods {
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

	var crossPkgImports strings.Builder
	needsGoogleProtobuf := usedPkgs["google.protobuf"]
	for pkg, dir := range info.CrossPackageTypes {
		if dir == "" || !usedPkgs[pkg] {
			continue
		}
		if strings.HasPrefix(pkg, "google.protobuf") {
			continue
		}
		crossDots := strings.Repeat(".", wrapperDepth+1)
		crossPkgSnake := pyModuleName(strutil.ToSnakeCase(pkg))
		crossAlias := crossPkgSnake + "_pb"
		crossImportPath := pyCrossProtoImportPath(info, pkg)
		crossPbModule := pyProtoFileModuleName(pyCrossProtoFile(info, pkg))
		fmt.Fprintf(&crossPkgImports, "from %stypes.%s import %s_pb2 as %s\n",
			crossDots, crossImportPath, crossPbModule, crossAlias)
		fmt.Fprintf(&crossPkgImports, "from %stypes.%s import helpers as %s_helpers\n",
			crossDots, crossImportPath, crossPkgSnake)
	}
	if needsGoogleProtobuf {
		crossPkgImports.WriteString("from google.protobuf import empty_pb2\n")
	}

	// Build method implementations
	var methods strings.Builder
	for _, method := range targetService.Methods {
		methods.WriteString(generatePyGrpcClientMethod(targetService, method, info.Package))
		methods.WriteString("\n")
	}

	clientName := svc.ServiceName + "GrpcClient"

	data := struct {
		Header               string
		PbModuleName         string
		PbImportFrom         string
		ExceptionsImportFrom string
		CrossPackageImports  string
		ServiceName          string
		ClientName           string
		Methods              string
	}{
		Header:               header.GeneratedHeader(consts.LangPython, svc.ProtoFile),
		PbModuleName:         pbModuleName,
		PbImportFrom:         pbImportFrom,
		ExceptionsImportFrom: pyExceptionsImportFrom(dots),
		CrossPackageImports:  crossPkgImports.String(),
		ServiceName:          svc.ServiceName,
		ClientName:           clientName,
		Methods:              methods.String(),
	}

	tmplContent, err := loadTemplate("grpcclient.py.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load grpcclient template: %w", err)
	}

	t, err := template.New("grpcclient").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	if err := t.Execute(file, data); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	return nil
}

// pyTypeRef converts a protobuf fully-qualified type to a Python type reference.
func pyTypeRef(fullType, shortType, servicePackage string) string {
	ft := strings.TrimPrefix(fullType, ".")
	if ft == "google.protobuf.Empty" || shortType == "Empty" {
		return "empty_pb2.Empty"
	}
	parts := strings.Split(ft, ".")
	if len(parts) > 1 {
		pkg := strings.Join(parts[:len(parts)-1], ".")
		if pkg != servicePackage && pkg != "" && pkg != "google.protobuf" {
			return pyModuleName(strutil.ToSnakeCase(pkg)) + "_pb." + shortType
		}
	}
	return "pb." + shortType
}

// pyAuthSnippet returns Python code that builds metadata with a Bearer token
// when the client has a token_provider configured.
func grpcClientAuthRequired(svc *parser.ServiceInfo, method *parser.MethodInfo) bool {
	return !(svc.AuthDisabled || svc.IsC2D || method.AuthDisabled)
}

func pyAuthSnippet(authRequired bool) string {
	return fmt.Sprintf(`metadata = await self._metadata(auth_required=%s)`, pyBool(authRequired))
}

func pyValidatorOrNone(validateRef string) string {
	if validateRef == "" {
		return "None"
	}
	return validateRef
}

// generatePyGrpcClientMethod generates a Python method for the gRPC client.
func generatePyGrpcClientMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, servicePackage string) string {
	methodSnake := pyGeneratedSnakeName(method.Name)
	pyInput := pyTypeRef(method.FullInputType, method.InputType, servicePackage)
	pyOutput := pyTypeRef(method.FullOutputType, method.OutputType, servicePackage)
	authRequired := grpcClientAuthRequired(svc, method)
	auth := pyAuthSnippet(authRequired)
	authRequiredLit := pyBool(authRequired)
	retrySafeLit := pyBool(method.IsRetrySafeUnary())
	emptyInput := method.FullInputType == "google.protobuf.Empty"
	emptyOutput := method.FullOutputType == "google.protobuf.Empty"
	validateInput := pyValidateRef(method.FullInputType, method.InputType, servicePackage)
	validateOutput := pyValidateRef(method.FullOutputType, method.OutputType, servicePackage)
	inputValidation := ""
	if !emptyInput && validateInput != "" {
		inputValidation = fmt.Sprintf("        %s(request)\n", validateInput)
	}
	outputValidation := ""
	if !emptyOutput && validateOutput != "" {
		outputValidation = fmt.Sprintf("            %s(response)\n", validateOutput)
	}
	streamOutputValidation := ""
	if !emptyOutput && validateOutput != "" {
		streamOutputValidation = fmt.Sprintf("                %s(item)\n", validateOutput)
	}
	inputValidatorArg := pyValidatorOrNone(validateInput)
	outputValidatorArg := pyValidatorOrNone(validateOutput)

	if method.ClientStreaming && method.ServerStreaming {
		// Bidirectional streaming
		return fmt.Sprintf(`    async def %s(self, request_iterator):
        """Call %s.%s (bidirectional streaming)."""
        %s
        try:
            requests = self._validating_request_iterator(request_iterator, %s)
            responses = self._stub.%s(requests, metadata=metadata)
            return self._validating_response_iterator(responses, %s)
        except grpc.RpcError as e:
            raise e
`, methodSnake, svc.Name, method.Name, auth, inputValidatorArg, method.Name, outputValidatorArg)
	}

	if method.ServerStreaming && method.IsTier2SnapshotCollection() {
		// Tier-2 SNAPSHOT collection: server-streaming on the wire, but the client
		// seam returns a materialized list — drain the stream into a list.
		return fmt.Sprintf(`    async def %s(self, request: %s) -> list[%s]:
        """Call %s.%s (Tier-2 SNAPSHOT collection over server streaming)."""
%s
        %s
        try:
            items = []
            async for item in self._stub.%s(request, metadata=metadata, timeout=self._timeout_arg()):
%s
                items.append(item)
            return items
        except grpc.RpcError as e:
            raise e
`, methodSnake, pyInput, pyOutput, svc.Name, method.Name, inputValidation, auth, method.Name, streamOutputValidation)
	}

	if method.ServerStreaming && method.IsWatchCollection() {
		// WATCH collection: return the async stream iterator over the generated
		// {Method}WatchEvent envelope. Breaking iteration cancels the call.
		return fmt.Sprintf(`    async def %s(self, request: %s):
        """Call %s.%s (WATCH collection over server streaming)."""
%s
        %s
        try:
            responses = self._stub.%s(request, metadata=metadata)
            return self._validating_response_iterator(responses, %s)
        except grpc.RpcError as e:
            raise e
`, methodSnake, pyInput, svc.Name, method.Name, inputValidation, auth, method.Name, outputValidatorArg)
	}

	if method.ServerStreaming {
		// Server streaming
		return fmt.Sprintf(`    async def %s(self, request: %s):
        """Call %s.%s (server streaming)."""
%s
        %s
        try:
            responses = self._stub.%s(request, metadata=metadata)
            return self._validating_response_iterator(responses, %s)
        except grpc.RpcError as e:
            raise e
`, methodSnake, pyInput, svc.Name, method.Name, inputValidation, auth, method.Name, outputValidatorArg)
	}

	if method.ClientStreaming {
		// Client streaming
		return fmt.Sprintf(`    async def %s(self, request_iterator):
        """Call %s.%s (client streaming)."""
        %s
        try:
            requests = self._validating_request_iterator(request_iterator, %s)
            response = await self._stub.%s(requests, metadata=metadata)
%s
            return response
        except grpc.RpcError as e:
            raise e
`, methodSnake, svc.Name, method.Name, auth, inputValidatorArg, method.Name, outputValidation)
	}

	if method.IsPageCollection() {
		// PAGE: unary on the wire. Two client seams — an explicit single-page call
		// ({method}_page → (items, next_page_token, total)) and an auto-pager (the
		// natural method name, an async generator) that follows next_page_token
		// until exhausted, yielding each item. The auto-pager advances the token on
		// a request clone, so the caller's request is never mutated.
		pyItem := pyTypeRef(method.Resolved.PageItemType, simpleTypeName(method.Resolved.PageItemType), servicePackage)
		itemsField := method.Resolved.PageItemsField
		totalExpr := "-1"
		if method.Resolved.PageHasTotal {
			totalExpr = "int(getattr(response, 'total_count', 0))"
		}
		return fmt.Sprintf(`    async def %s_page(self, request: %s) -> tuple[list[%s], str, int]:
        """Call %s.%s and return a single page: (items, next_page_token, total).

        next_page_token is empty on the last page; total is -1 when the response
        carries no count.
        """
%s
        try:
            response = await self._call_unary(%s, True, lambda metadata, timeout: self._stub.%s(request, metadata=metadata, timeout=timeout))
%s
            return (list(response.%s), response.next_page_token, %s)
        except grpc.RpcError as e:
            raise e

    async def %s(self, request: %s):
        """Call %s.%s as an auto-pager: follow next_page_token until exhausted,
        yielding each item across pages. The caller's request is not mutated.
        """
        cur = %s()
        cur.CopyFrom(request)
        while True:
            items, next_page_token, _ = await self.%s_page(cur)
            for item in items:
                yield item
            if not next_page_token:
                return
            cur.page_token = next_page_token
`, methodSnake, pyInput, pyItem, svc.Name, method.Name,
			inputValidation, authRequiredLit, method.Name, outputValidation,
			itemsField, totalExpr,
			methodSnake, pyInput, svc.Name, method.Name, pyInput, methodSnake)
	}

	// Unary method — omit Empty input param, return None for Empty output.

	if emptyOutput && emptyInput {
		return fmt.Sprintf(`    async def %s(self) -> None:
        """Call %s.%s with optional validation."""
        try:
            await self._call_unary(%s, %s, lambda metadata, timeout: self._stub.%s(empty_pb2.Empty(), metadata=metadata, timeout=timeout))
        except grpc.RpcError as e:
            raise e
`, methodSnake, svc.Name, method.Name, authRequiredLit, retrySafeLit, method.Name)
	}
	if emptyOutput {
		return fmt.Sprintf(`    async def %s(self, request: %s) -> None:
        """Call %s.%s with optional validation."""
%s
        try:
            await self._call_unary(%s, %s, lambda metadata, timeout: self._stub.%s(request, metadata=metadata, timeout=timeout))
        except grpc.RpcError as e:
            raise e
`, methodSnake, pyInput, svc.Name, method.Name, inputValidation, authRequiredLit, retrySafeLit, method.Name)
	}
	if emptyInput {
		return fmt.Sprintf(`    async def %s(self) -> %s:
        """Call %s.%s with optional validation."""
        try:
            response = await self._call_unary(%s, %s, lambda metadata, timeout: self._stub.%s(empty_pb2.Empty(), metadata=metadata, timeout=timeout))
%s
            return response
        except grpc.RpcError as e:
            raise e
`, methodSnake, pyOutput, svc.Name, method.Name, authRequiredLit, retrySafeLit, method.Name, outputValidation)
	}
	return fmt.Sprintf(`    async def %s(self, request: %s) -> %s:
        """Call %s.%s with optional validation."""
%s
        try:
            response = await self._call_unary(%s, %s, lambda metadata, timeout: self._stub.%s(request, metadata=metadata, timeout=timeout))
%s
            return response
        except grpc.RpcError as e:
            raise e
`, methodSnake, pyInput, pyOutput, svc.Name, method.Name, inputValidation, authRequiredLit, retrySafeLit, method.Name, outputValidation)
}
