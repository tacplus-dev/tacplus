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
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// generatedRestClientJava tracks info about a generated REST client wrapper for aggregators.
type generatedRestClientJava struct {
	PackageName string
	ServiceName string
	ClassName   string
	ParentDir   string
	FullPackage string
}

// generateRestClients generates typed REST client wrappers for each gRPC service.
func generateRestClients(config *tactype.Config, servicesInfo []ServiceInfo, inputDir string) error {
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	var generated []generatedRestClientJava

	for _, svc := range servicesInfo {
		protoInfo, err := descriptorParser.ConvertToProtoInfo(svc.ProtoFile)
		if err != nil {
			return fmt.Errorf("failed to parse %s: %w", svc.ProtoFile, err)
		}

		if err := generateRestClient(svc, protoInfo); err != nil {
			return fmt.Errorf("failed to generate REST client for %s: %w", svc.ServiceName, err)
		}

		normalizedRelPath := strings.TrimPrefix(svc.ProtoFile, "tacplus"+string(filepath.Separator))
		typeDir := strings.Split(normalizedRelPath, string(filepath.Separator))[0]
		fullPackage := fmt.Sprintf("%s."+config.GetSmPrefix()+".internal.rest.%s.%s", config.JavaPackageBase, typeDir, svc.PackageName)

		className := svc.ServiceName + "RestClient"
		generated = append(generated, generatedRestClientJava{
			PackageName: svc.PackageName,
			ServiceName: svc.ServiceName,
			ClassName:   className,
			ParentDir:   strings.Split(normalizedRelPath, string(filepath.Separator))[0],
			FullPackage: fullPackage,
		})
	}

	if err := generateRestClientGroupAggregators(config, generated); err != nil {
		return fmt.Errorf("failed to generate REST client group aggregators: %w", err)
	}

	if err := generateRestClientRootAggregator(config, generated); err != nil {
		return fmt.Errorf("failed to generate root REST client aggregator: %w", err)
	}

	return nil
}

// generateRestClient creates the typed REST client wrapper Java file for a service.
func generateRestClient(svc ServiceInfo, info *parser.ProtoInfo) error {
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

	normalizedRelPath := strings.TrimPrefix(svc.ProtoFile, "tacplus"+string(filepath.Separator))
	protoDir := filepath.Join(svc.Config.OutputDir, "proto")
	typesProtoFileName := svc.PackageName

	typeDir := strings.Split(normalizedRelPath, string(filepath.Separator))[0]
	var fullPackage, typesPackage string
	fullPackage = fmt.Sprintf("%s."+svc.Config.GetSmPrefix()+".internal.rest.%s.%s", svc.Config.JavaPackageBase, typeDir, svc.PackageName)
	typesPackage = fmt.Sprintf("%s.types.%s.%s", svc.Config.JavaPackageBase, typeDir, svc.PackageName)

	svcName, svcFileName := proto.ServiceProtoParts(svc.ProtoFile)
	isFlatServiceProto := svcName == svcFileName

	srcDir := filepath.Join(svc.Config.OutputDir, "src", "main", "java")
	packagePath := strings.ReplaceAll(fullPackage, ".", string(filepath.Separator))
	outputDir := filepath.Join(srcDir, packagePath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}

	// Detect outer class name
	var outerClassName string
	if isFlatServiceProto {
		typesProtoPath := filepath.Join(protoDir, svc.ProtoFile)
		if name := extractJavaOuterClassName(typesProtoPath); name != "" {
			outerClassName = name
		} else {
			outerClassName = getOuterClassNameForWrapper(svc.TypesOutputDir, svc.ProtoFile)
		}
	} else if typesProtoFileName != "" {
		typesProtoPath := filepath.Join(protoDir, typeDir, svc.PackageName, typesProtoFileName+".proto")
		if name := extractJavaOuterClassName(typesProtoPath); name != "" {
			outerClassName = name
		} else {
			outerClassName = getOuterClassNameForWrapper(svc.TypesOutputDir, svc.ProtoFile)
		}
	} else {
		outerClassName = getOuterClassNameForWrapper(svc.TypesOutputDir, svc.ProtoFile)
	}

	// Build cross-package outer class name and Java package maps
	crossPkgOuterClasses := make(map[string]string)
	crossPkgJavaPackages := make(map[string]string)
	for pkg, dir := range info.CrossPackageTypes {
		if dir == "" {
			continue
		}
		dirParts := strings.Split(dir, string(filepath.Separator))
		crossJavaPackage := javaPackageForProtoPackage(svc.Config.JavaPackageBase, pkg, dir)
		crossPkgJavaPackages[pkg] = crossJavaPackage
		var typeSubPkg string
		if len(dirParts) >= 2 {
			typeSubPkg = pkg
		}
		crossTypesSrcDir := filepath.Join(svc.Config.OutputDir, "src", "main", "java")
		var crossTypesPath string
		if typeSubPkg != "" {
			crossTypesPath = filepath.Join(crossTypesSrcDir, strings.ReplaceAll(crossJavaPackage+"."+typeSubPkg, ".", string(filepath.Separator)))
		} else {
			crossTypesPath = filepath.Join(crossTypesSrcDir, strings.ReplaceAll(crossJavaPackage, ".", string(filepath.Separator)))
		}
		crossProtoFile := filepath.Join(dir, pkg+".proto")
		crossOuterClass := getOuterClassNameForWrapper(crossTypesPath, crossProtoFile)
		crossPkgOuterClasses[pkg] = crossOuterClass
	}

	// Build methods
	neededImports := map[string]struct{}{}
	hasStreaming := false
	var methods strings.Builder
	for _, method := range targetService.Methods {
		if method.HttpMethod == "" || method.ClientStreaming {
			continue
		}
		inputType := resolveJavaMethodType(method.FullInputType, method.InputType, info.Package, outerClassName, crossPkgOuterClasses)
		outputType := resolveJavaMethodType(method.FullOutputType, method.OutputType, info.Package, outerClassName, crossPkgOuterClasses)
		collectGrpcClientTypeImport(neededImports, method.FullInputType, info.Package, typesPackage, inputType, crossPkgJavaPackages)
		collectGrpcClientTypeImport(neededImports, method.FullOutputType, info.Package, typesPackage, outputType, crossPkgJavaPackages)
		if method.ServerStreaming {
			hasStreaming = true
		}
		methods.WriteString(generateJavaRestClientMethod(method, inputType, outputType))
		methods.WriteString("\n")
	}

	// Build imports
	importLines := make(map[string]struct{})
	for importLine := range neededImports {
		importLines[importLine] = struct{}{}
	}
	importLines[fmt.Sprintf("import %s.types.sys.errors.ProtoException;", svc.Config.JavaPackageBase)] = struct{}{}

	var sortedImportLines []string
	for importLine := range importLines {
		sortedImportLines = append(sortedImportLines, importLine)
	}
	sort.Strings(sortedImportLines)
	typesImport := strings.Join(sortedImportLines, "\n")

	className := svc.ServiceName + "RestClient"
	outputPath := filepath.Join(outputDir, className+".java")

	zap.L().Info("Generating Java REST client", zap.String("path", outputPath))

	data := struct {
		Header       string
		FullPackage  string
		TypesImport  string
		ServiceName  string
		ClientName   string
		Methods      string
		HasStreaming bool
	}{
		Header:       header.GeneratedHeader(consts.LangJava, svc.ProtoFile),
		FullPackage:  fullPackage,
		TypesImport:  typesImport,
		ServiceName:  svc.ServiceName,
		ClientName:   className,
		Methods:      methods.String(),
		HasStreaming: hasStreaming,
	}

	tmplStr, err := loadTemplate("restclient.java.tmpl")
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

// generateJavaRestClientMethod generates a Java method for the REST client wrapper.
func generateJavaRestClientMethod(method *parser.MethodInfo, inputType, outputType string) string {
	methodName := strutil.ToCamelCase(strutil.ToSnakeCase(method.Name))
	if len(methodName) > 0 {
		methodName = strings.ToLower(methodName[:1]) + methodName[1:]
	}

	isEmptyInput := method.FullInputType == "google.protobuf.Empty" || method.InputType == "Empty"

	if method.ServerStreaming {
		if isEmptyInput {
			return fmt.Sprintf(`    /**
     * %s calls %s via %s %s (NDJSON streaming).
     */
    public java.util.Iterator<%s> %s() {
        return streamNdjson("%s", "%s", null, %s.class);
    }
`, method.Name, method.HttpPath, method.HttpMethod, method.HttpPath,
				outputType, methodName,
				method.HttpMethod, method.HttpPath, outputType)
		}
		return fmt.Sprintf(`    /**
     * %s calls %s via %s %s (NDJSON streaming).
     */
    public java.util.Iterator<%s> %s(%s request) {
        return streamNdjson("%s", "%s", request, %s.class);
    }
`, method.Name, method.HttpPath, method.HttpMethod, method.HttpPath,
			outputType, methodName, inputType,
			method.HttpMethod, method.HttpPath, outputType)
	}

	// Unary method
	if isEmptyInput {
		return fmt.Sprintf(`    /**
     * %s calls %s via %s %s.
     */
    public %s %s() {
        return doJson("%s", "%s", null, %s.class);
    }
`, method.Name, method.HttpPath, method.HttpMethod, method.HttpPath,
			outputType, methodName,
			method.HttpMethod, method.HttpPath, outputType)
	}

	return fmt.Sprintf(`    /**
     * %s calls %s via %s %s.
     */
    public %s %s(%s request) {
        return doJson("%s", "%s", request, %s.class);
    }
`, method.Name, method.HttpPath, method.HttpMethod, method.HttpPath,
		outputType, methodName, inputType,
		method.HttpMethod, method.HttpPath, outputType)
}

// restClientGroupEntryJava tracks a service for the REST client group aggregator.
type restClientGroupEntryJava struct {
	FieldName   string
	MethodName  string
	ClassName   string
	TypeRef     string // simple name or FQN when there's a class-name collision
	ImportLine  string // "pkg.ClassName" to import, or "" when using FQN
	FullPackage string
}

// generateRestClientGroupAggregators creates RestClients.java at each group level.
func generateRestClientGroupAggregators(config *tactype.Config, wrappers []generatedRestClientJava) error {
	if len(wrappers) == 0 {
		return nil
	}

	groups := make(map[string][]generatedRestClientJava)
	for _, w := range wrappers {
		groups[w.ParentDir] = append(groups[w.ParentDir], w)
	}

	for group, groupWrappers := range groups {
		groupPackage := fmt.Sprintf("%s."+config.GetSmPrefix()+".internal.rest.%s", config.JavaPackageBase, group)

		// Count occurrences of each ClassName so we can detect collisions.
		classNameCount := make(map[string]int)
		for _, w := range groupWrappers {
			classNameCount[w.ClassName]++
		}

		var entries []restClientGroupEntryJava
		seen := make(map[string]bool)
		seenFields := make(map[string]bool)
		for _, w := range groupWrappers {
			fieldName := strings.ToLower(w.ClassName[:1]) + w.ClassName[1:]
			if classNameCount[w.ClassName] > 1 || seenFields[fieldName] {
				fieldName = w.PackageName + strings.ToUpper(fieldName[:1]) + fieldName[1:]
			}
			seenFields[fieldName] = true
			methodName := w.ServiceName
			methodName = strings.TrimSuffix(methodName, "Service")
			methodName = strings.ToLower(methodName[:1]) + methodName[1:]
			if classNameCount[w.ClassName] > 1 {
				methodName = w.PackageName + strings.ToUpper(methodName[:1]) + methodName[1:]
			}
			if seen[methodName] {
				methodName = fieldName
			}
			seen[methodName] = true
			typeRef := w.ClassName
			importLine := w.FullPackage + "." + w.ClassName
			if classNameCount[w.ClassName] > 1 {
				typeRef = w.FullPackage + "." + w.ClassName
				importLine = ""
			}
			entries = append(entries, restClientGroupEntryJava{
				FieldName:   fieldName,
				MethodName:  methodName,
				ClassName:   w.ClassName,
				TypeRef:     typeRef,
				ImportLine:  importLine,
				FullPackage: w.FullPackage,
			})
		}

		data := struct {
			Header  string
			Package string
			Entries []restClientGroupEntryJava
		}{
			Header:  header.GeneratedHeader(consts.LangJava, "restclients/"+group),
			Package: groupPackage,
			Entries: entries,
		}

		tmplStr, err := loadTemplate("restclient_group.java.tmpl")
		if err != nil {
			return fmt.Errorf("failed to load restclient group template: %w", err)
		}
		tmpl, err := template.New("rcgroup").Parse(tmplStr)
		if err != nil {
			return fmt.Errorf("failed to parse restclient group template: %w", err)
		}

		packagePath := strings.ReplaceAll(groupPackage, ".", string(filepath.Separator))
		outputDir := filepath.Join(config.OutputDir, "src", "main", "java", packagePath)
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return fmt.Errorf("failed to create group directory: %w", err)
		}

		outputPath := filepath.Join(outputDir, "RestClients.java")
		file, err := os.Create(outputPath)
		if err != nil {
			return fmt.Errorf("failed to create RestClients.java: %w", err)
		}
		defer file.Close()

		if err := tmpl.Execute(file, data); err != nil {
			return fmt.Errorf("failed to execute restclient group template: %w", err)
		}

		zap.L().Info("Generated group RestClients.java", zap.String("group", group), zap.String("path", outputPath))
	}

	return nil
}

// restClientRootGroupJava tracks a group for the root REST client aggregator.
type restClientRootGroupJava struct {
	Name         string
	MethodName   string
	GroupPackage string
}

// generateRestClientRootAggregator creates the root RestClients.java aggregator.
func generateRestClientRootAggregator(config *tactype.Config, wrappers []generatedRestClientJava) error {
	if len(wrappers) == 0 {
		return nil
	}

	rootPackage := config.JavaPackageBase + "." + config.GetSmPrefix() + ".internal.rest"

	seen := make(map[string]bool)
	var groups []restClientRootGroupJava
	for _, w := range wrappers {
		if seen[w.ParentDir] {
			continue
		}
		seen[w.ParentDir] = true
		groups = append(groups, restClientRootGroupJava{
			Name:         w.ParentDir,
			MethodName:   strings.ToUpper(w.ParentDir[:1]) + w.ParentDir[1:],
			GroupPackage: fmt.Sprintf("%s."+config.GetSmPrefix()+".internal.rest.%s", config.JavaPackageBase, w.ParentDir),
		})
	}

	data := struct {
		Header  string
		Package string
		Groups  []restClientRootGroupJava
	}{
		Header:  header.GeneratedHeader(consts.LangJava, "restclients"),
		Package: rootPackage,
		Groups:  groups,
	}

	tmplStr, err := loadTemplate("restclient_root.java.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load restclient root template: %w", err)
	}
	tmpl, err := template.New("rcroot").Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse restclient root template: %w", err)
	}

	packagePath := strings.ReplaceAll(rootPackage, ".", string(filepath.Separator))
	outputDir := filepath.Join(config.OutputDir, "src", "main", "java", packagePath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create restclients directory: %w", err)
	}

	outputPath := filepath.Join(outputDir, "RestClients.java")
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create root RestClients.java: %w", err)
	}
	defer file.Close()

	if err := tmpl.Execute(file, data); err != nil {
		return fmt.Errorf("failed to execute restclient root template: %w", err)
	}

	zap.L().Info("Generated root RestClients.java", zap.String("path", outputPath))
	return nil
}
