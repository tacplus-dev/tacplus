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

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
)

// generateRestClients generates typed REST client wrappers for each gRPC service.
func generateRestClients(servicesInfo []ServiceInfo, inputDir string, _packageToDirMap map[string]string) error {
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	for _, svc := range servicesInfo {
		protoInfo, err := descriptorParser.ConvertToProtoInfo(svc.ProtoFile)
		if err != nil {
			return fmt.Errorf("failed to parse %s: %w", svc.ProtoFile, err)
		}

		if err := generateRustRestClient(svc, protoInfo); err != nil {
			return fmt.Errorf("failed to generate REST client for %s: %w", svc.ServiceName, err)
		}
	}

	if len(servicesInfo) > 0 {
		if err := generateRustRestClientAggregators(servicesInfo); err != nil {
			return fmt.Errorf("failed to generate REST client aggregators: %w", err)
		}
	}

	return nil
}

// generateRustRestClientAggregators creates group and root aggregator files for REST clients.
func generateRustRestClientAggregators(servicesInfo []ServiceInfo) error {
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
		groupDir := filepath.Join(smDir, parentDir)
		groupFile := filepath.Join(groupDir, "rests.rs")

		// Detect duplicate service names within this group.
		svcNameCount := make(map[string]int)
		for _, svc := range svcs {
			svcNameCount[svc.ServiceName]++
		}

		var buf strings.Builder
		buf.WriteString(header.GeneratedHeader(consts.LangRust, "rest"))
		buf.WriteString("\nuse reqwest::Client;\n\n")

		for _, svc := range svcs {
			svcSnake := strutil.StripPackagePrefix(tonicServiceModuleName(svc.ServiceName), svc.PackageName)
			origClientName := svc.ServiceName + "RestClient"
			if svcNameCount[svc.ServiceName] > 1 {
				aliasName := strutil.ToPascalCase(svc.PackageName) + svc.ServiceName + "RestClient"
				fmt.Fprintf(&buf, "use super::%s::%s::%s as %s;\n", svc.PackageName, svcSnake, origClientName, aliasName)
			} else {
				fmt.Fprintf(&buf, "use super::%s::%s::%s;\n", svc.PackageName, svcSnake, origClientName)
			}
		}

		buf.WriteString("\n/// RestClients aggregates all typed REST clients for this group.\n")
		buf.WriteString("pub struct RestClients {\n")
		for _, svc := range svcs {
			fieldName, clientName := restClientNames(svc, svcNameCount)
			fmt.Fprintf(&buf, "    %s: %s,\n", fieldName, clientName)
		}
		buf.WriteString("}\n\nimpl RestClients {\n")
		buf.WriteString("    /// Creates all typed REST clients for this group.\n")
		buf.WriteString("    pub fn new(client: Client, base_url: String) -> Self {\n")
		buf.WriteString("        Self {\n")
		for _, svc := range svcs {
			fieldName, clientName := restClientNames(svc, svcNameCount)
			fmt.Fprintf(&buf, "            %s: %s::new(client.clone(), base_url.clone()),\n", fieldName, clientName)
		}
		buf.WriteString("        }\n    }\n\n")
		for _, svc := range svcs {
			fieldName, clientName := restClientNames(svc, svcNameCount)
			fmt.Fprintf(&buf, "    /// Returns the %s wrapper.\n", clientName)
			fmt.Fprintf(&buf, "    pub fn %s(&self) -> &%s {\n        &self.%s\n    }\n\n", fieldName, clientName, fieldName)
		}
		buf.WriteString("}\n")

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

	// Root aggregator
	rootFile := filepath.Join(smDir, "rest.rs")
	var buf strings.Builder
	buf.WriteString(header.GeneratedHeader(consts.LangRust, "rest"))
	buf.WriteString("\nuse reqwest::Client;\n\n")
	for _, groupName := range groupNames {
		fmt.Fprintf(&buf, "use super::%s::rests::RestClients as %sRestClients;\n",
			groupName, strings.Title(groupName))
	}
	buf.WriteString("\n/// RestClients aggregates all typed REST client groups.\n")
	buf.WriteString("pub struct RestClients {\n")
	for _, groupName := range groupNames {
		fmt.Fprintf(&buf, "    %s: %sRestClients,\n", groupName, strings.Title(groupName))
	}
	buf.WriteString("}\n\nimpl RestClients {\n")
	buf.WriteString("    /// Creates all typed REST client groups.\n")
	buf.WriteString("    pub fn new(client: Client, base_url: String) -> Self {\n")
	buf.WriteString("        Self {\n")
	for _, groupName := range groupNames {
		fmt.Fprintf(&buf, "            %s: %sRestClients::new(client.clone(), base_url.clone()),\n",
			groupName, strings.Title(groupName))
	}
	buf.WriteString("        }\n    }\n\n")
	for _, groupName := range groupNames {
		fmt.Fprintf(&buf, "    /// Returns the %s REST clients group.\n", groupName)
		fmt.Fprintf(&buf, "    pub fn %s(&self) -> &%sRestClients {\n        &self.%s\n    }\n\n",
			groupName, strings.Title(groupName), groupName)
	}
	buf.WriteString("}\n")

	if err := os.MkdirAll(smDir, 0755); err != nil {
		return fmt.Errorf("failed to create rest directory: %w", err)
	}
	if err := os.WriteFile(rootFile, []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write root aggregator: %w", err)
	}

	return nil
}

// restClientNames returns the field name and client type name for a service,
// prefixing with the package name when the service name collides with another.
func restClientNames(svc ServiceInfo, svcNameCount map[string]int) (fieldName, clientName string) {
	if svcNameCount[svc.ServiceName] > 1 {
		fieldName = svc.PackageName + "_" + tonicServiceModuleName(svc.ServiceName) + "_client"
		clientName = strutil.ToPascalCase(svc.PackageName) + svc.ServiceName + "RestClient"
	} else {
		fieldName = tonicServiceModuleName(svc.ServiceName) + "_client"
		clientName = svc.ServiceName + "RestClient"
	}
	return
}

// generateRustRestClient creates the typed REST client wrapper Rust file for a service.
func generateRustRestClient(svc ServiceInfo, info *parser.ProtoInfo) error {
	outputDir := filepath.Join(svc.Config.OutputDir, svc.Config.GetSmPrefix(), "internal", "rest", svc.ParentDir, svc.PackageName)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	svcSnake := tonicServiceModuleName(svc.ServiceName)
	wrapperFile := strutil.StripPackagePrefix(svcSnake, svc.PackageName) + ".rs"
	outputPath := filepath.Join(outputDir, wrapperFile)

	zap.L().Info("Generating Rust REST client", zap.String("path", outputPath))

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

	// Build imports
	typesCratePathStr := strings.ReplaceAll(strings.ReplaceAll(svc.ParentDir, "-", "_"), string(filepath.Separator), "::") + "::" + svc.PackageName
	var serviceImports strings.Builder
	fmt.Fprintf(&serviceImports, "#[allow(unused_imports)]\nuse crate::types::%s::*;\n", typesCratePathStr)

	// Build method implementations
	var methods strings.Builder
	hasStreaming := false
	for _, method := range targetService.Methods {
		if method.HttpMethod == "" || method.ClientStreaming {
			continue
		}
		if method.ServerStreaming {
			hasStreaming = true
		}
		methods.WriteString(generateRustRestClientMethod(targetService, method, info.Package, info.CrossPackageTypes))
		methods.WriteString("\n")
	}

	clientName := svc.ServiceName + "RestClient"

	data := struct {
		Header         string
		ServiceName    string
		ClientName     string
		ServiceImports string
		Methods        string
		HasStreaming   bool
	}{
		Header:         header.GeneratedHeader(consts.LangRust, svc.ProtoFile),
		ServiceName:    svc.ServiceName,
		ClientName:     clientName,
		ServiceImports: serviceImports.String(),
		Methods:        methods.String(),
		HasStreaming:   hasStreaming,
	}

	tmplStr, err := loadTemplate("restclient.rs.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load restclient template: %w", err)
	}

	tmpl, err := template.New("restclient").Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse template: %w", err)
	}

	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, data); err != nil {
		return fmt.Errorf("failed to execute template: %w", err)
	}

	return nil
}

// generateRustRestClientMethod generates a Rust method for the REST client wrapper.
func generateRustRestClientMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, servicePkg string, crossPkgTypes map[string]string) string {
	methodSnake := prostSnakeName(method.Name)
	inputType, outputType := resolveRustTypeQualified(method, servicePkg, crossPkgTypes)

	isEmptyInput := method.FullInputType == "google.protobuf.Empty" || method.InputType == "Empty"

	if method.ServerStreaming {
		var reqArg, reqBody string
		if isEmptyInput {
			reqArg = ""
			reqBody = "None"
		} else {
			reqArg = fmt.Sprintf(",\n        request: &%s", inputType)
			reqBody = "Some(serde_json::to_string(request)?)"
		}
		return fmt.Sprintf(`    /// %s calls %s.%s via %s %s (NDJSON streaming).
    pub async fn %s(
        &self%s,
    ) -> Result<impl futures::Stream<Item = Result<%s, Box<dyn std::error::Error>>>, Box<dyn std::error::Error>> {
        self.stream_ndjson("%s", "%s", %s).await
    }`, method.Name, svc.Name, method.Name, method.HttpMethod, method.HttpPath,
			methodSnake, reqArg, outputType,
			method.HttpMethod, method.HttpPath, reqBody)
	}

	// Unary
	if isEmptyInput {
		return fmt.Sprintf(`    /// %s calls %s.%s via %s %s.
    pub async fn %s(
        &self,
    ) -> Result<%s, Box<dyn std::error::Error>> {
        self.do_json("%s", "%s", None).await
    }`, method.Name, svc.Name, method.Name, method.HttpMethod, method.HttpPath,
			methodSnake, outputType,
			method.HttpMethod, method.HttpPath)
	}

	return fmt.Sprintf(`    /// %s calls %s.%s via %s %s.
    pub async fn %s(
        &self,
        request: &%s,
    ) -> Result<%s, Box<dyn std::error::Error>> {
        self.do_json("%s", "%s", Some(serde_json::to_string(request)?)).await
    }`, method.Name, svc.Name, method.Name, method.HttpMethod, method.HttpPath,
		methodSnake, inputType, outputType,
		method.HttpMethod, method.HttpPath)
}
