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

// generateRestClients generates typed REST client wrappers for each gRPC service.
func generateRestClients(servicesInfo []ServiceInfo, inputDir string) error {
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	for _, svc := range servicesInfo {
		protoInfo, err := descriptorParser.ConvertToProtoInfo(svc.ProtoFile)
		if err != nil {
			return fmt.Errorf("failed to parse %s: %w", svc.ProtoFile, err)
		}

		if err := generatePyRestClient(svc, protoInfo); err != nil {
			return fmt.Errorf("failed to generate REST client for %s: %w", svc.ServiceName, err)
		}
	}

	if len(servicesInfo) > 0 {
		if err := generatePyRestClientAggregators(servicesInfo); err != nil {
			return fmt.Errorf("failed to generate REST client aggregators: %w", err)
		}
	}

	return nil
}

// generatePyRestClientAggregators creates group and root aggregator files for REST clients.
func generatePyRestClientAggregators(servicesInfo []ServiceInfo) error {
	if len(servicesInfo) == 0 {
		return nil
	}
	config := servicesInfo[0].Config
	smDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "internal", "rest")

	groups := make(map[string][]ServiceInfo)
	for _, svc := range servicesInfo {
		groups[svc.ParentDir] = append(groups[svc.ParentDir], svc)
	}

	var groupNames []string
	for parentDir, svcs := range groups {
		groupNames = append(groupNames, parentDir)
		groupDir := filepath.Join(smDir, pyPath(parentDir))
		groupFile := filepath.Join(groupDir, "rests.py")

		var buf strings.Builder
		buf.WriteString(header.GeneratedHeader(consts.LangPython, "rest"))
		buf.WriteString("\n")

		for _, svc := range svcs {
			clientName := svc.ServiceName + "RestClient"
			pkgName := pyModuleName(svc.PackageName)
			moduleSnake := strutil.StripPackagePrefix(pyGeneratedSnakeName(svc.ServiceName), pkgName)
			fmt.Fprintf(&buf, "from .%s.%s import %s\n", pkgName, moduleSnake, clientName)
		}

		buf.WriteString("\n\nclass RestClients:\n")
		buf.WriteString("    \"\"\"Aggregates all typed REST clients for this group.\"\"\"\n\n")
		buf.WriteString("    def __init__(self, base_url: str, session=None):\n")
		for _, svc := range svcs {
			fieldName := pyGeneratedSnakeName(svc.ServiceName) + "_client"
			clientName := svc.ServiceName + "RestClient"
			fmt.Fprintf(&buf, "        self._%s = %s(base_url, session)\n", fieldName, clientName)
		}
		buf.WriteString("\n")
		for _, svc := range svcs {
			fieldName := pyGeneratedSnakeName(svc.ServiceName) + "_client"
			clientName := svc.ServiceName + "RestClient"
			fmt.Fprintf(&buf, "    @property\n    def %s(self) -> %s:\n        return self._%s\n\n", fieldName, clientName, fieldName)
		}

		if err := os.MkdirAll(groupDir, 0755); err != nil {
			return fmt.Errorf("failed to create group directory: %w", err)
		}
		if err := os.WriteFile(groupFile, []byte(buf.String()), 0644); err != nil {
			return fmt.Errorf("failed to write group aggregator: %w", err)
		}
	}

	// Sort the group names before emitting the root aggregator: `groups` is a Go
	// map, so ranging it leaks Go's randomized map order into generated output
	// and byte-diffs across otherwise-identical runs (GEN-DET-1).
	sort.Strings(groupNames)

	// Generate root aggregator
	rootFile := filepath.Join(smDir, "rest.py")
	var buf strings.Builder
	buf.WriteString(header.GeneratedHeader(consts.LangPython, "rest"))
	buf.WriteString("\n")
	for _, groupName := range groupNames {
		fmt.Fprintf(&buf, "from .%s.rests import RestClients as %sRestClients\n",
			pyDottedPath(groupName), strings.Title(pyModuleName(groupName)))
	}
	buf.WriteString("\n\nclass RestClients:\n")
	buf.WriteString("    \"\"\"Aggregates all typed REST client groups.\"\"\"\n\n")
	buf.WriteString("    def __init__(self, base_url: str, session=None):\n")
	for _, groupName := range groupNames {
		fmt.Fprintf(&buf, "        self._%s = %sRestClients(base_url, session)\n",
			pyModuleName(groupName), strings.Title(pyModuleName(groupName)))
	}
	buf.WriteString("\n")
	for _, groupName := range groupNames {
		fmt.Fprintf(&buf, "    @property\n    def %s(self):\n        return self._%s\n\n",
			pyModuleName(groupName), pyModuleName(groupName))
	}

	if err := os.MkdirAll(smDir, 0755); err != nil {
		return fmt.Errorf("failed to create rest directory: %w", err)
	}
	if err := os.WriteFile(rootFile, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write root aggregator: %w", err)
	}

	return nil
}

// generatePyRestClient creates the typed REST client wrapper Python file for a service.
func generatePyRestClient(svc ServiceInfo, info *parser.ProtoInfo) error {
	pkgName := pyModuleName(svc.PackageName)
	outputDir := filepath.Join(svc.Config.OutputDir, svc.Config.GetSmPrefix(), "internal", "rest", pyPath(svc.ParentDir), pkgName)

	// Create __init__.py files
	smBase := filepath.Join(svc.Config.OutputDir, svc.Config.GetSmPrefix())
	for _, dir := range []string{
		smBase,
		filepath.Join(smBase, "internal"),
		filepath.Join(smBase, "internal", "rest"),
		filepath.Join(smBase, "internal", "rest", pyPath(svc.ParentDir)),
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

	zap.L().Info("Generating Python REST client", zap.String("path", outputPath))

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
	wrapperDepth := 3 + len(parentParts) + 1
	dots := strings.Repeat(".", wrapperDepth+1)
	pbImportFrom := dots + "types." + pyProtoImportPath(svc.ParentDir, svc.PackageName, svc.ProtoFile)

	// Build cross-package imports
	usedPkgs := make(map[string]bool)
	for _, method := range targetService.Methods {
		if method.HttpMethod == "" || method.ClientStreaming {
			continue
		}
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
	}
	if needsGoogleProtobuf {
		crossPkgImports.WriteString("from google.protobuf import empty_pb2\n")
	}

	// Build method implementations
	var methods strings.Builder
	for _, method := range targetService.Methods {
		if method.HttpMethod == "" || method.ClientStreaming {
			continue
		}
		methods.WriteString(generatePyRestClientMethod(targetService, method, info.Package))
		methods.WriteString("\n")
	}

	clientName := svc.ServiceName + "RestClient"

	data := struct {
		Header              string
		PbModuleName        string
		PbImportFrom        string
		CrossPackageImports string
		ServiceName         string
		ClientName          string
		Methods             string
	}{
		Header:              header.GeneratedHeader(consts.LangPython, svc.ProtoFile),
		PbModuleName:        pbModuleName,
		PbImportFrom:        pbImportFrom,
		CrossPackageImports: crossPkgImports.String(),
		ServiceName:         svc.ServiceName,
		ClientName:          clientName,
		Methods:             methods.String(),
	}

	tmplContent, err := loadTemplate("restclient.py.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load restclient template: %w", err)
	}

	t, err := template.New("restclient").Parse(tmplContent)
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

// generatePyRestClientMethod generates a Python method for the REST client.
func generatePyRestClientMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, servicePackage string) string {
	methodSnake := pyGeneratedSnakeName(method.Name)
	pyInput := pyTypeRef(method.FullInputType, method.InputType, servicePackage)
	pyOutput := pyTypeRef(method.FullOutputType, method.OutputType, servicePackage)
	isEmptyInput := method.FullInputType == "google.protobuf.Empty" || method.InputType == "Empty"

	if method.ServerStreaming {
		if isEmptyInput {
			return fmt.Sprintf(`    def %s(self):
        """Call %s.%s via %s %s (NDJSON streaming)."""
        yield from self._stream_ndjson("%s", "%s", None, %s)
`, methodSnake, svc.Name, method.Name, method.HttpMethod, method.HttpPath,
				method.HttpMethod, method.HttpPath, pyOutput)
		}
		return fmt.Sprintf(`    def %s(self, request: %s):
        """Call %s.%s via %s %s (NDJSON streaming)."""
        yield from self._stream_ndjson("%s", "%s", request, %s)
`, methodSnake, pyInput,
			svc.Name, method.Name, method.HttpMethod, method.HttpPath,
			method.HttpMethod, method.HttpPath, pyOutput)
	}

	// Unary
	if isEmptyInput {
		return fmt.Sprintf(`    def %s(self) -> %s:
        """Call %s.%s via %s %s."""
        return self._do_json("%s", "%s", None, %s)
`, methodSnake, pyOutput,
			svc.Name, method.Name, method.HttpMethod, method.HttpPath,
			method.HttpMethod, method.HttpPath, pyOutput)
	}

	return fmt.Sprintf(`    def %s(self, request: %s) -> %s:
        """Call %s.%s via %s %s."""
        return self._do_json("%s", "%s", request, %s)
`, methodSnake, pyInput, pyOutput,
		svc.Name, method.Name, method.HttpMethod, method.HttpPath,
		method.HttpMethod, method.HttpPath, pyOutput)
}
