// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/header"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/consts"
)

// generateW2SClients generates typed Python W2S clients for selected services.
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

		if err := generatePyW2SClient(svc, protoInfo); err != nil {
			return fmt.Errorf("failed to generate W2S client for %s: %w", svc.ServiceName, err)
		}
	}

	if len(servicesInfo) > 0 {
		if err := generatePyW2SClientAggregators(servicesInfo); err != nil {
			return fmt.Errorf("failed to generate W2S client aggregators: %w", err)
		}
	}

	return nil
}

func resolveW2STarget(svc ServiceInfo) string {
	config := svc.Config
	target := svc.PackageName
	if svc.HostLabel != "" {
		target = svc.HostLabel
	}
	for _, entry := range config.GrpcW2SClients {
		matchKey := strings.TrimSuffix(filepath.Base(entry.ProtoFile), ".proto")
		if entry.ProtoPackage != "" {
			matchKey = entry.ProtoPackage
		}
		if matchKey == svc.PackageName && entry.TargetService != "" {
			target = entry.TargetService
			break
		}
	}
	return target
}

func generatePyW2SClientAggregators(servicesInfo []ServiceInfo) error {
	if len(servicesInfo) == 0 {
		return nil
	}
	config := servicesInfo[0].Config
	smDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "internal", "grpc_over_rest")

	if err := ensurePyInit(smDir); err != nil {
		return fmt.Errorf("failed to create W2S clients directory: %w", err)
	}

	allSvcNameCount := make(map[string]int)
	for _, svc := range servicesInfo {
		allSvcNameCount[svc.ServiceName]++
	}

	type targetEntry struct {
		target    string
		fieldName string
		className string
		svcs      []ServiceInfo
	}
	var targets []targetEntry
	targetSeen := make(map[string]int)
	for _, svc := range servicesInfo {
		target := resolveW2STarget(svc)
		safeTarget := strings.ReplaceAll(target, "-", "_")
		if idx, ok := targetSeen[target]; ok {
			targets[idx].svcs = append(targets[idx].svcs, svc)
			continue
		}
		targetSeen[target] = len(targets)
		targets = append(targets, targetEntry{
			target:    target,
			fieldName: strutil.ToSnakeCase(safeTarget),
			className: strutil.ToPascalCase(safeTarget) + "W2SClients",
			svcs:      []ServiceInfo{svc},
		})
	}

	rootFile := filepath.Join(smDir, "w2s_clients.py")
	var buf strings.Builder
	buf.WriteString(header.GeneratedHeader(consts.LangPython, "w2s_clients"))
	buf.WriteString("\n\nfrom __future__ import annotations\n\n")
	buf.WriteString("from typing import Awaitable, Callable, Optional, Union\n\n")
	buf.WriteString("import httpx\n\n")
	buf.WriteString("TokenProvider = Callable[[], Union[str, Awaitable[str]]]\n")
	buf.WriteString("TokenProviderFor = Callable[[str], Optional[TokenProvider]]\n\n")

	for _, svc := range servicesInfo {
		origClientName := svc.ServiceName + "W2SClient"
		clientName := origClientName
		pkgName := pyModuleName(svc.PackageName)
		if allSvcNameCount[svc.ServiceName] > 1 {
			clientName = strutil.ToPascalCase(pkgName) + svc.ServiceName + "W2SClient"
		}
		moduleSnake := strutil.StripPackagePrefix(pyGeneratedSnakeName(svc.ServiceName), pkgName)
		importPath := pyW2SRelativeModule(svc.ParentDir, pkgName, moduleSnake)
		if clientName != origClientName {
			fmt.Fprintf(&buf, "from %s import %s as %s\n", importPath, origClientName, clientName)
		} else {
			fmt.Fprintf(&buf, "from %s import %s\n", importPath, clientName)
		}
	}

	for _, te := range targets {
		fmt.Fprintf(&buf, "\n\nclass %s:\n", te.className)
		fmt.Fprintf(&buf, "    \"\"\"Typed W2S clients for the %q service.\"\"\"\n\n", te.target)
		buf.WriteString("    def __init__(self, base_url: str, session: httpx.AsyncClient, token_provider: Optional[TokenProvider] = None) -> None:\n")
		for _, svc := range te.svcs {
			fieldName, clientName := pyW2SClientNames(svc, allSvcNameCount)
			fmt.Fprintf(&buf, "        self._%s = %s(base_url, session=session, token_provider=token_provider)\n", fieldName, clientName)
		}
		buf.WriteString("\n")
		for _, svc := range te.svcs {
			fieldName, clientName := pyW2SClientNames(svc, allSvcNameCount)
			fmt.Fprintf(&buf, "    @property\n    def %s(self) -> %s:\n        return self._%s\n\n", fieldName, clientName, fieldName)
		}
	}

	buf.WriteString("\n\n# Target service ID constants from proto annotations.\n")
	for _, te := range targets {
		constName := strings.ToUpper(strings.ReplaceAll(te.target, "-", "_"))
		fmt.Fprintf(&buf, "TARGET_%s = %q\n", constName, te.target)
	}

	buf.WriteString("\n\nclass W2SClients:\n")
	buf.WriteString("    \"\"\"W2S client hierarchy: sm.w2s_clients.{target}.{client}.\"\"\"\n\n")
	buf.WriteString("    def __init__(self, base_url: str, session: Optional[httpx.AsyncClient] = None, token_provider_for: Optional[TokenProviderFor] = None) -> None:\n")
	buf.WriteString("        self._owns_session = session is None\n")
	buf.WriteString("        self._session = session or httpx.AsyncClient()\n")
	for _, te := range targets {
		fmt.Fprintf(&buf, "        self._%s = %s(base_url, self._session, token_provider_for(%q) if token_provider_for else None)\n",
			te.fieldName, te.className, te.target)
	}
	buf.WriteString("\n")
	for _, te := range targets {
		fmt.Fprintf(&buf, "    @property\n    def %s(self) -> %s:\n        return self._%s\n\n", te.fieldName, te.className, te.fieldName)
	}
	buf.WriteString("    async def close(self) -> None:\n")
	buf.WriteString("        if self._owns_session:\n")
	buf.WriteString("            await self._session.aclose()\n")

	if err := os.WriteFile(rootFile, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write W2S clients: %w", err)
	}

	return nil
}

func pyW2SClientNames(svc ServiceInfo, svcNameCount map[string]int) (fieldName, clientName string) {
	pkgName := pyModuleName(svc.PackageName)
	if svcNameCount[svc.ServiceName] > 1 {
		fieldName = pkgName + "_" + pyGeneratedSnakeName(svc.ServiceName) + "_client"
		clientName = strutil.ToPascalCase(pkgName) + svc.ServiceName + "W2SClient"
	} else {
		fieldName = pyGeneratedSnakeName(svc.ServiceName) + "_client"
		clientName = svc.ServiceName + "W2SClient"
	}
	return
}

func generatePyW2SClient(svc ServiceInfo, info *parser.ProtoInfo) error {
	pkgName := pyModuleName(svc.PackageName)
	outputDir := filepath.Join(svc.Config.OutputDir, svc.Config.GetSmPrefix(), "internal", "grpc_over_rest", pyPath(svc.ParentDir), pkgName)
	if err := ensurePyW2SPackageDirs(svc.Config.OutputDir, svc.Config.GetSmPrefix(), svc.ParentDir, pkgName); err != nil {
		return err
	}

	wrapperFile := strutil.StripPackagePrefix(pyGeneratedSnakeName(svc.ServiceName), pkgName) + ".py"
	outputPath := filepath.Join(outputDir, wrapperFile)

	zap.L().Info("Generating Python W2S client", zap.String("path", outputPath))

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
	parentParts := splitPyPath(svc.ParentDir)
	wrapperDepth := 3 + len(parentParts) + 1 // w2sm + internal + grpc_over_rest + parentDir parts + packageName
	dots := strings.Repeat(".", wrapperDepth+1)
	pbImportFrom := dots + "types." + pyProtoImportPath(svc.ParentDir, svc.PackageName, svc.ProtoFile)

	usedPkgs := make(map[string]bool)
	for _, method := range targetService.Methods {
		if method.HttpPath == "" {
			return fmt.Errorf("%s.%s requires W2S REST route metadata; clients.w2s.enabled cannot emit a method without an HTTP path",
				targetService.Name, method.Name)
		}
		for _, fullType := range []string{method.FullInputType, method.FullOutputType} {
			pkg := pyW2SFullTypePackage(fullType)
			if pkg != "" && pkg != info.Package && !strings.HasPrefix(pkg, "google.") {
				usedPkgs[pkg] = true
			}
		}
	}

	crossAliases := make(map[string]string)
	var crossPkgImports strings.Builder
	var usedPkgNames []string
	for pkg := range usedPkgs {
		usedPkgNames = append(usedPkgNames, pkg)
	}
	sort.Strings(usedPkgNames)
	for _, pkg := range usedPkgNames {
		alias := pyW2SSafeIdent(pkg) + "_pb"
		crossAliases[pkg] = alias
		crossImportPath := pyCrossProtoImportPath(info, pkg)
		crossPbModule := pyProtoFileModuleName(pyCrossProtoFile(info, pkg))
		fmt.Fprintf(&crossPkgImports, "from %stypes.%s import %s_pb2 as %s\n",
			dots, crossImportPath, crossPbModule, alias)
		fmt.Fprintf(&crossPkgImports, "from %stypes.%s import helpers as %s_helpers\n",
			dots, crossImportPath, pyModuleName(strutil.ToSnakeCase(pkg)))
	}

	var methods strings.Builder
	var extraTypes strings.Builder
	for _, method := range targetService.Methods {
		result := generatePyW2SClientMethod(targetService, method, info.Package, crossAliases)
		methods.WriteString(result.method)
		methods.WriteString("\n")
		if result.extraType != "" {
			extraTypes.WriteString(result.extraType)
			extraTypes.WriteString("\n")
		}
	}

	clientName := svc.ServiceName + "W2SClient"
	data := struct {
		Header               string
		PbModuleName         string
		PbImportFrom         string
		ExceptionsImportFrom string
		CrossPackageImports  string
		ServiceName          string
		ClientName           string
		Methods              string
		ExtraTypes           string
	}{
		Header:               header.GeneratedHeader(consts.LangPython, svc.ProtoFile),
		PbModuleName:         pbModuleName,
		PbImportFrom:         pbImportFrom,
		ExceptionsImportFrom: pyExceptionsImportFrom(dots),
		CrossPackageImports:  crossPkgImports.String(),
		ServiceName:          svc.ServiceName,
		ClientName:           clientName,
		Methods:              methods.String(),
		ExtraTypes:           extraTypes.String(),
	}

	tmplContent, err := loadTemplate("w2sclient.py.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load w2sclient template: %w", err)
	}

	t, err := template.New("w2sclient").Parse(tmplContent)
	if err != nil {
		return fmt.Errorf("failed to parse w2sclient template: %w", err)
	}

	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	if err := t.Execute(file, data); err != nil {
		return fmt.Errorf("failed to execute w2sclient template: %w", err)
	}

	return nil
}

type pyW2SMethodResult struct {
	method    string
	extraType string
}

func generatePyW2SClientMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, servicePackage string, crossAliases map[string]string) pyW2SMethodResult {
	methodSnake := pyGeneratedSnakeName(method.Name)
	inputType := pyW2STypeRef(method.FullInputType, method.InputType, servicePackage, crossAliases)
	outputType := pyW2STypeRef(method.FullOutputType, method.OutputType, servicePackage, crossAliases)
	isEmptyInput := pyW2SIsEmpty(method.FullInputType, method.InputType)
	isEmptyOutput := pyW2SIsEmpty(method.FullOutputType, method.OutputType)
	httpMethod := pyW2SHttpMethod(method, isEmptyInput)
	authRequired := !(svc.AuthDisabled || method.AuthDisabled)
	authRequiredPy := pyBool(authRequired)
	requestValidation := pyW2SValidationStatement("request", method.FullInputType, method.InputType, servicePackage, "        ")
	streamRequestValidation := pyW2SValidationStatement("req", method.FullInputType, method.InputType, servicePackage, "                ")
	bidiRequestValidation := pyW2SValidationStatement("msg", method.FullInputType, method.InputType, servicePackage, "        ")
	responseValidator := pyW2SValidationArg(method.FullOutputType, method.OutputType, servicePackage)

	if method.ClientStreaming && method.ServerStreaming {
		handleType := strutil.ToPascalCase(method.Name) + "W2SHandle"
		methodCode := fmt.Sprintf(`    def %s(self, on_connect, on_message) -> "%s":
        """Open a managed W2S bidirectional stream to %s.%s."""
        handle = %s(self, %s)
        handle.start("%s", on_connect, on_message, %s, %s)
        return handle
`, methodSnake, handleType, svc.Name, method.Name, handleType, authRequiredPy, method.HttpPath, outputType, responseValidator)

		bodyExpr := "self._client._message_to_body(msg)"
		if isEmptyInput {
			bodyExpr = "{}"
		}
		handleCode := fmt.Sprintf(`class %s(_W2SBidiHandle):
    """Handle for the managed %s W2S bidirectional stream."""

    async def send(self, msg: %s) -> None:
%s        body = %s
        await self._send_body(body)
`, handleType, method.Name, inputType, bidiRequestValidation, bodyExpr)
		return pyW2SMethodResult{method: methodCode, extraType: handleCode}
	}

	if method.ClientStreaming {
		if isEmptyOutput {
			return pyW2SMethodResult{method: fmt.Sprintf(`    async def %s(self, request_iterator) -> None:
        """Call %s.%s via W2S WebSocket client streaming."""
        async with self._connect_websocket("%s") as ws:
            await self._send_start_frame(ws, auth_required=%s)
            async for req in self._iterate_requests(request_iterator):
%s                body = %s
                await self._send_message_frame(ws, body, auth_required=%s)
            await self._send_complete_frame(ws)
            await self._recv_ws_unit(ws)
`, methodSnake, svc.Name, method.Name, method.HttpPath, authRequiredPy, streamRequestValidation, pyW2SBodyExpr("req", isEmptyInput), authRequiredPy)}
		}
		return pyW2SMethodResult{method: fmt.Sprintf(`    async def %s(self, request_iterator) -> %s:
        """Call %s.%s via W2S WebSocket client streaming."""
        async with self._connect_websocket("%s") as ws:
            await self._send_start_frame(ws, auth_required=%s)
            async for req in self._iterate_requests(request_iterator):
%s                body = %s
                await self._send_message_frame(ws, body, auth_required=%s)
            await self._send_complete_frame(ws)
            return await self._recv_ws_json(ws, %s, %s)
`, methodSnake, outputType, svc.Name, method.Name, method.HttpPath, authRequiredPy, streamRequestValidation, pyW2SBodyExpr("req", isEmptyInput), authRequiredPy, outputType, responseValidator)}
	}

	if method.ServerStreaming {
		if isEmptyInput {
			return pyW2SMethodResult{method: fmt.Sprintf(`    async def %s(self):
        """Call %s.%s via W2S SSE server streaming."""
        async for item in self._stream_sse("%s", "%s", {}, %s, auth_required=%s, validate_response=%s):
            yield item
`, methodSnake, svc.Name, method.Name, httpMethod, method.HttpPath, outputType, authRequiredPy, responseValidator)}
		}
		return pyW2SMethodResult{method: fmt.Sprintf(`    async def %s(self, request: %s):
        """Call %s.%s via W2S SSE server streaming."""
%s        body = self._message_to_body(request)
        async for item in self._stream_sse("%s", "%s", body, %s, auth_required=%s, validate_response=%s):
            yield item
`, methodSnake, inputType, svc.Name, method.Name, requestValidation, httpMethod, method.HttpPath, outputType, authRequiredPy, responseValidator)}
	}

	if isEmptyOutput {
		if isEmptyInput {
			return pyW2SMethodResult{method: fmt.Sprintf(`    async def %s(self) -> None:
        """Call %s.%s via W2S HTTP/JSON."""
        await self._do_json_unit("%s", "%s", {}, auth_required=%s)
`, methodSnake, svc.Name, method.Name, httpMethod, method.HttpPath, authRequiredPy)}
		}
		return pyW2SMethodResult{method: fmt.Sprintf(`    async def %s(self, request: %s) -> None:
        """Call %s.%s via W2S HTTP/JSON."""
%s        body = self._message_to_body(request)
        await self._do_json_unit("%s", "%s", body, auth_required=%s)
`, methodSnake, inputType, svc.Name, method.Name, requestValidation, httpMethod, method.HttpPath, authRequiredPy)}
	}

	if isEmptyInput {
		return pyW2SMethodResult{method: fmt.Sprintf(`    async def %s(self) -> %s:
        """Call %s.%s via W2S HTTP/JSON."""
        return await self._do_json_value("%s", "%s", {}, %s, auth_required=%s, validate_response=%s)
`, methodSnake, outputType, svc.Name, method.Name, httpMethod, method.HttpPath, outputType, authRequiredPy, responseValidator)}
	}
	return pyW2SMethodResult{method: fmt.Sprintf(`    async def %s(self, request: %s) -> %s:
        """Call %s.%s via W2S HTTP/JSON."""
%s        body = self._message_to_body(request)
        return await self._do_json_value("%s", "%s", body, %s, auth_required=%s, validate_response=%s)
`, methodSnake, inputType, outputType, svc.Name, method.Name, requestValidation, httpMethod, method.HttpPath, outputType, authRequiredPy, responseValidator)}
}

func pyW2SHttpMethod(method *parser.MethodInfo, isEmptyInput bool) string {
	if isEmptyInput && method.HttpMethod != "" {
		return method.HttpMethod
	}
	return "POST"
}

func pyW2SBodyExpr(requestVar string, isEmptyInput bool) string {
	if isEmptyInput {
		return "{}"
	}
	return fmt.Sprintf("self._message_to_body(%s)", requestVar)
}

func pyW2SValidationArg(fullType, simpleType, servicePackage string) string {
	validate := pyValidateRef(fullType, simpleType, servicePackage)
	if validate == "" {
		return "None"
	}
	return validate
}

func pyW2SValidationStatement(varName, fullType, simpleType, servicePackage, indent string) string {
	validate := pyValidateRef(fullType, simpleType, servicePackage)
	if validate == "" {
		return ""
	}
	return fmt.Sprintf("%s%s(%s)\n", indent, validate, varName)
}

func pyW2STypeRef(fullType, simpleType, servicePackage string, crossAliases map[string]string) string {
	if pyW2SIsEmpty(fullType, simpleType) {
		return "empty_pb2.Empty"
	}
	pkg := pyW2SFullTypePackage(fullType)
	if pkg != "" && pkg != servicePackage {
		if alias, ok := crossAliases[pkg]; ok {
			return alias + "." + simpleType
		}
		return pyW2SSafeIdent(pkg) + "_pb." + simpleType
	}
	return "pb." + simpleType
}

func pyW2SIsEmpty(fullType, simpleType string) bool {
	return fullType == "google.protobuf.Empty" || strings.TrimPrefix(fullType, ".") == "google.protobuf.Empty" || simpleType == "Empty"
}

func pyW2SFullTypePackage(fullType string) string {
	ft := strings.TrimPrefix(fullType, ".")
	parts := strings.Split(ft, ".")
	if len(parts) <= 1 {
		return ""
	}
	return strings.Join(parts[:len(parts)-1], ".")
}

func pyW2SProtoPackageLeaf(pkg string) string {
	parts := strings.Split(pkg, ".")
	return parts[len(parts)-1]
}

func pyW2SSafeIdent(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r + 32)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return strings.Trim(b.String(), "_")
}

func pyBool(v bool) string {
	if v {
		return "True"
	}
	return "False"
}

func ensurePyW2SPackageDirs(outputDir, smPrefix, parentDir, packageName string) error {
	smBase := filepath.Join(outputDir, smPrefix)
	dirs := []string{
		smBase,
		filepath.Join(smBase, "internal"),
		filepath.Join(smBase, "internal", "grpc_over_rest"),
	}
	cur := filepath.Join(smBase, "internal", "grpc_over_rest")
	for _, part := range pyPathParts(parentDir) {
		cur = filepath.Join(cur, part)
		dirs = append(dirs, cur)
	}
	dirs = append(dirs, filepath.Join(cur, pyModuleName(packageName)))
	for _, dir := range dirs {
		if err := ensurePyInit(dir); err != nil {
			return fmt.Errorf("failed to create Python package directory %s: %w", dir, err)
		}
	}
	return nil
}

func ensurePyInit(dir string) error {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	initPath := filepath.Join(dir, "__init__.py")
	if _, err := os.Stat(initPath); os.IsNotExist(err) {
		return os.WriteFile(initPath, []byte("# Auto-generated package\n"), 0644)
	}
	return nil
}

func pyW2SRelativeModule(parentDir, packageName, moduleName string) string {
	dotted := pyJoinDotted(pyDottedPath(parentDir), pyModuleName(packageName), pyModuleName(moduleName))
	return "." + dotted
}
