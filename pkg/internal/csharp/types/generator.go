// Copyright (c) 2026 Second Star Cavalier LLC

// Package types generates a C# (.NET) client-lite package from proto files.
// tacplus emits a .csproj manifest, stripped proto import closure, and thin C2D
// facade; Grpc.Tools generates message and gRPC stubs at dotnet build time.
package types

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/service"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/tmplutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
	"gitlab.com/secondstarcavalier/schematic/tacplus/templates"
)

const (
	defaultCSharpPackageName  = "Tacplus"
	defaultProtobufVersion    = "3.27.2"
	defaultGrpcNetClientVer   = "2.65.0"
	defaultGrpcCoreApiVersion = "2.65.0"
	defaultGrpcToolsVersion   = "2.65.0"
	defaultDaemonSocketDir    = "tacplus"
)

type projectTemplateData struct {
	PackageName          string
	ProtobufVersion      string
	GrpcNetClientVersion string
	GrpcCoreApiVersion   string
	GrpcToolsVersion     string
	ClientProtoFiles     []string
}

type c2dClientTemplateData struct {
	PackageName     string
	DaemonDirectory string
	Services        []c2dServiceTemplateData
}

type c2dServiceTemplateData struct {
	MethodName string // e.g. "WithContractApi"
	ClientType string // fully-qualified generated client, e.g. "Common2.Product.ProductApi.ProductApiClient"
}

type c2sClientTemplateData struct {
	PackageName string
	Services    []c2dServiceTemplateData
}

type w2sClientTemplateData struct {
	PackageName string
	Services    []w2sServiceTemplateData
}

type w2sServiceTemplateData struct {
	ServiceName    string
	ClientTypeName string
	PackageName    string
	Methods        []w2sMethodTemplateData
}

type w2sMethodTemplateData struct {
	MethodName   string
	HTTPMethod   string
	HTTPPath     string
	AuthRequired bool
	InputType    string
	OutputType   string
	// IsStreaming is true for methods that are client- or server-streaming.
	// The C# lite client emits a throwing stub for these so a consumer sees
	// the method at compile time instead of a silent omission.
	IsStreaming bool
}

// Generate generates a .NET client-lite package from proto files.
// C# is a client-only target: it generates C2D client surfaces from the in-scope
// service_c2d services declared in the proto graph. There is no YAML
// clients.c2d.enabled precondition.
func Generate(config *tactype.Config) error {
	if config.CSharpPackageBase == "" {
		config.CSharpPackageBase = defaultCSharpPackageName
	}
	config.CSharpPackageBase = csharpModuleName(config.CSharpPackageBase)
	if config.CSharpPackageBase == "" {
		config.CSharpPackageBase = defaultCSharpPackageName
	}

	if err := config.ResolveProtocPaths(); err != nil {
		return err
	}

	if _, err := os.Stat(config.OutputDir); err == nil {
		if err := os.RemoveAll(config.OutputDir); err != nil {
			return fmt.Errorf("failed to remove existing output directory: %w", err)
		}
	}

	protoSrcDir, err := proto.CopyAndPrepareProtoDir(config.InputDir, config.OutputDir)
	if err != nil {
		return fmt.Errorf("failed to copy proto directory: %w", err)
	}

	// Client data-entitlement parity: inject the <field>_redacted indicators and the
	// <T>Entitlement messages BEFORE the service closure + spec-option strip so a C#
	// client sees the same egress types as every other language (it consumes
	// sanitized records; it emits no Sanitize transform).
	if err := proto.InjectRedactIndicatorFields(protoSrcDir, protoSrcDir); err != nil {
		return fmt.Errorf("failed to inject redact indicator fields: %w", err)
	}
	if err := proto.AppendEntitlementMessagesToProtos(protoSrcDir, protoSrcDir); err != nil {
		return fmt.Errorf("failed to append entitlement messages: %w", err)
	}
	if err := proto.AppendEventBusWrappersToProtos(protoSrcDir, protoSrcDir); err != nil {
		return fmt.Errorf("failed to append eventbus topic wrappers to protos: %w", err)
	}
	if err := proto.AppendRoutingEnvelopesToProtos(protoSrcDir, protoSrcDir); err != nil {
		return fmt.Errorf("failed to append routing envelopes to protos: %w", err)
	}
	if proto.NeedsEventBusSupportProto(protoSrcDir) {
		if _, err := proto.EnsureEventBusSupportProto(protoSrcDir, proto.EventBusSupportProtoOptions{}); err != nil {
			return fmt.Errorf("failed to generate eventbus support proto: %w", err)
		}
	}
	if err := proto.AppendStateServiceToProtos(protoSrcDir, protoSrcDir); err != nil {
		return fmt.Errorf("failed to append state service to protos: %w", err)
	}

	// Populate config.GrpcC2SClients / GrpcW2SClients from the proto graph's
	// grpc_c2s_client / grpc_w2s_client declarations (the same proto-driven path
	// the go/ts/rust/java/swift generators use). C2D is resolved separately below.
	if err := service.InferGrpcClientsFromAnnotations(config, protoSrcDir); err != nil {
		return fmt.Errorf("failed to infer C# client surfaces: %w", err)
	}

	servicesInfo, err := service.DetectServicesForWrapper(config, protoSrcDir, func(config *tactype.Config, _, _, _ string) string {
		return config.OutputDir
	})
	if err != nil {
		return fmt.Errorf("failed to detect C2D services: %w", err)
	}

	c2dServices := service.ResolveC2DClientServices(config, servicesInfo)
	c2sServices := service.ResolveC2SClientServices(config, servicesInfo)
	w2sServices := service.ResolveW2SClientServices(config, servicesInfo)
	if len(c2dServices) == 0 && len(c2sServices) == 0 && len(w2sServices) == 0 {
		return fmt.Errorf("csharp lite found no service_c2d services for package %q", config.SelfPackage)
	}

	closureServices := make([]service.ServiceInfo, 0, len(c2dServices)+len(c2sServices)+len(w2sServices))
	closureServices = append(closureServices, c2dServices...)
	closureServices = append(closureServices, c2sServices...)
	closureServices = append(closureServices, w2sServices...)

	protoFiles, err := collectServiceProtoClosure(protoSrcDir, closureServices)
	if err != nil {
		return fmt.Errorf("failed to collect C# proto inputs: %w", err)
	}
	if len(protoFiles) == 0 {
		return fmt.Errorf("csharp lite collected no proto inputs")
	}

	// W2S method metadata (REST route + input/output types) is derived from spec
	// annotations, so resolve it BEFORE the spec options are stripped below.
	var w2sData []w2sServiceTemplateData
	if len(w2sServices) > 0 {
		w2sData, err = w2sServicesTemplateData(protoSrcDir, w2sServices)
		if err != nil {
			return fmt.Errorf("failed to resolve C# W2S routes: %w", err)
		}
	}

	if err := proto.StripSpecOptionsFromDir(protoSrcDir); err != nil {
		return fmt.Errorf("failed to strip spec annotations: %w", err)
	}

	// Both C2D and C2S service protos need `GrpcServices="Client"` stubs; W2S
	// protos need message stubs only (the default `GrpcServices="None"` include).
	clientStubServices := make([]service.ServiceInfo, 0, len(c2dServices)+len(c2sServices))
	clientStubServices = append(clientStubServices, c2dServices...)
	clientStubServices = append(clientStubServices, c2sServices...)

	if err := renderProject(config, c2dClientProtoFiles(clientStubServices)); err != nil {
		return err
	}
	if len(c2dServices) > 0 {
		if err := renderC2DClient(config, c2dServices); err != nil {
			return err
		}
		if err := renderNamedPipesConnectionFactory(config); err != nil {
			return err
		}
	}
	if len(c2sServices) > 0 {
		if err := renderC2SClient(config, c2sServices); err != nil {
			return err
		}
	}
	if len(w2sData) > 0 {
		if err := renderW2SClient(config, w2sData); err != nil {
			return err
		}
	}
	if err := copyCSharpProtoClosure(protoSrcDir, filepath.Join(config.OutputDir, "Protos"), protoFiles); err != nil {
		return err
	}
	if err := os.RemoveAll(protoSrcDir); err != nil {
		return fmt.Errorf("failed to remove temporary C# proto directory: %w", err)
	}

	zap.L().Info("Generated C# lite package",
		zap.String("package", config.CSharpPackageBase),
		zap.Int("proto_files", len(protoFiles)),
		zap.Int("c2d_services", len(c2dServices)),
		zap.Int("c2s_services", len(c2sServices)),
		zap.Int("w2s_services", len(w2sServices)))

	return nil
}

func renderProject(config *tactype.Config, clientProtoFiles []string) error {
	data := projectTemplateData{
		PackageName:          config.CSharpPackageBase,
		ProtobufVersion:      csharpDependencyVersion(config, "Google.Protobuf", defaultProtobufVersion),
		GrpcNetClientVersion: csharpDependencyVersion(config, "Grpc.Net.Client", defaultGrpcNetClientVer),
		GrpcCoreApiVersion:   csharpDependencyVersion(config, "Grpc.Core.Api", defaultGrpcCoreApiVersion),
		GrpcToolsVersion:     csharpDependencyVersion(config, "Grpc.Tools", defaultGrpcToolsVersion),
		ClientProtoFiles:     clientProtoFiles,
	}
	out, err := executeCSharpTemplate("Tacplus.csproj.tmpl", data)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.OutputDir, 0755); err != nil {
		return fmt.Errorf("failed to create C# project directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(config.OutputDir, config.CSharpPackageBase+".csproj"), out, 0644); err != nil {
		return fmt.Errorf("failed to write .csproj: %w", err)
	}
	return nil
}

func renderC2DClient(config *tactype.Config, servicesInfo []service.ServiceInfo) error {
	data := c2dClientTemplateData{
		PackageName:     config.CSharpPackageBase,
		DaemonDirectory: daemonDirectory(config, servicesInfo),
		Services:        c2dServicesTemplateData(servicesInfo),
	}
	out, err := executeCSharpTemplate("C2DClient.cs.tmpl", data)
	if err != nil {
		return err
	}
	outPath := filepath.Join(config.OutputDir, "C2DClient.cs")
	if err := os.WriteFile(outPath, out, 0644); err != nil {
		return fmt.Errorf("failed to write C2DClient.cs: %w", err)
	}
	return nil
}

func renderNamedPipesConnectionFactory(config *tactype.Config) error {
	out, err := executeCSharpTemplate("NamedPipesConnectionFactory.cs.tmpl", c2dClientTemplateData{
		PackageName: config.CSharpPackageBase,
	})
	if err != nil {
		return err
	}
	outPath := filepath.Join(config.OutputDir, "NamedPipesConnectionFactory.cs")
	if err := os.WriteFile(outPath, out, 0644); err != nil {
		return fmt.Errorf("failed to write NamedPipesConnectionFactory.cs: %w", err)
	}
	return nil
}

func renderC2SClient(config *tactype.Config, servicesInfo []service.ServiceInfo) error {
	data := c2sClientTemplateData{
		PackageName: config.CSharpPackageBase,
		Services:    c2dServicesTemplateData(servicesInfo),
	}
	out, err := executeCSharpTemplate("C2SClient.cs.tmpl", data)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(config.OutputDir, "C2SClient.cs"), out, 0644); err != nil {
		return fmt.Errorf("failed to write C2SClient.cs: %w", err)
	}
	return nil
}

func renderW2SClient(config *tactype.Config, services []w2sServiceTemplateData) error {
	data := w2sClientTemplateData{
		PackageName: config.CSharpPackageBase,
		Services:    services,
	}
	out, err := executeCSharpTemplate("W2SClient.cs.tmpl", data)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(config.OutputDir, "W2SClient.cs"), out, 0644); err != nil {
		return fmt.Errorf("failed to write W2SClient.cs: %w", err)
	}
	return nil
}

// w2sServicesTemplateData resolves per-method REST route + type metadata for the
// W2S (gRPC-over-REST) services. It parses each service's proto (before spec
// options are stripped) to read the convention HTTP path, verb and I/O types.
// W2S is unary-only for C#: streaming methods are skipped, mirroring the swift
// generator, so a mixed service still yields a compilable unary-only surface.
func w2sServicesTemplateData(protoDir string, servicesInfo []service.ServiceInfo) ([]w2sServiceTemplateData, error) {
	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return nil, err
	}

	result := make([]w2sServiceTemplateData, 0, len(servicesInfo))
	for _, svc := range servicesInfo {
		info, err := dp.ConvertToProtoInfo(svc.ProtoFile)
		if err != nil {
			return nil, fmt.Errorf("failed to parse proto %q for W2S service %q: %w", svc.ProtoFile, svc.ServiceName, err)
		}

		var target *parser.ServiceInfo
		for _, s := range info.Services {
			if s.Name == svc.ServiceName {
				target = s
				break
			}
		}
		if target == nil {
			return nil, fmt.Errorf("W2S service %q not found in proto %q", svc.ServiceName, svc.ProtoFile)
		}

		methods := make([]w2sMethodTemplateData, 0, len(target.Methods))
		for _, method := range target.Methods {
			// C# W2S is unary-only: streaming methods are emitted as throwing
			// stubs so a consumer sees them at compile time. A silent continue-skip
			// at Info level was the previous behavior — it was invisible at default
			// CLI log levels (the recurring "silent skip" defect shape).
			if method.ClientStreaming || method.ServerStreaming {
				methods = append(methods, w2sMethodTemplateData{
					MethodName: csharpIdentifier(method.Name),
					InputType:  csharpProtoTypeName(method.FullInputType, method.InputType, info.Package),
					OutputType: csharpProtoTypeName(method.FullOutputType, method.OutputType, info.Package),
					IsStreaming: true,
				})
				continue
			}
			if method.HttpPath == "" {
				return nil, fmt.Errorf("csharp W2S method %s.%s requires REST route metadata (no HTTP path)", target.Name, method.Name)
			}
			authRequired := !(svc.AuthDisabled || target.AuthDisabled || method.AuthDisabled)
			httpMethod := "POST"
			if isEmptyCSharpType(method.FullInputType) && method.HttpMethod != "" {
				httpMethod = method.HttpMethod
			}
			methods = append(methods, w2sMethodTemplateData{
				MethodName:   csharpIdentifier(method.Name),
				HTTPMethod:   httpMethod,
				HTTPPath:     method.HttpPath,
				AuthRequired: authRequired,
				InputType:    csharpProtoTypeName(method.FullInputType, method.InputType, info.Package),
				OutputType:   csharpProtoTypeName(method.FullOutputType, method.OutputType, info.Package),
			})
		}

		result = append(result, w2sServiceTemplateData{
			ServiceName:    svc.ServiceName,
			ClientTypeName: csharpIdentifier(svc.ServiceName) + "W2SClient",
			PackageName:    info.Package,
			Methods:        methods,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].ClientTypeName < result[j].ClientTypeName
	})
	return result, nil
}

// csharpProtoTypeName maps a proto message type to its Grpc.Tools-generated C#
// type name ({Namespace}.{Message}). fullType is the fully-qualified proto type
// (e.g. "contractclient.ContractRequest"); its package prefix determines the C#
// namespace so cross-package types resolve correctly.
func csharpProtoTypeName(fullType, simpleType, servicePackage string) string {
	t := strings.TrimPrefix(fullType, ".")
	pkg := servicePackage
	msg := simpleType
	if idx := strings.LastIndex(t, "."); idx >= 0 {
		pkg = t[:idx]
		msg = t[idx+1:]
	} else if t != "" {
		pkg = ""
		msg = t
	}
	msgIdent := csharpIdentifier(msg)
	ns := csharpNamespace(pkg)
	if ns == "" {
		return msgIdent
	}
	return ns + "." + msgIdent
}

func isEmptyCSharpType(fullType string) bool {
	return strings.TrimPrefix(fullType, ".") == "google.protobuf.Empty"
}

func executeCSharpTemplate(name string, data any) ([]byte, error) {
	typeFS, err := fs.Sub(templates.FS, "csharp/types")
	if err != nil {
		return nil, fmt.Errorf("failed to create C# template filesystem: %w", err)
	}
	return tmplutil.ExecuteTemplate(typeFS, name, tmplutil.BaseFuncMap(), data)
}

func collectServiceProtoClosure(protoDir string, servicesInfo []service.ServiceInfo) ([]string, error) {
	roots := make([]string, 0, len(servicesInfo))
	for _, svc := range servicesInfo {
		if svc.ProtoFile == "" {
			continue
		}
		roots = append(roots, filepath.ToSlash(svc.ProtoFile))
	}
	sort.Strings(roots)
	roots = uniqueStrings(roots)

	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return nil, err
	}
	allFiles := dp.GetAllFiles()

	seen := make(map[string]bool)
	var visit func(string) error
	visit = func(rel string) error {
		rel = filepath.ToSlash(rel)
		if shouldSkipGeneratedInput(rel) || seen[rel] {
			return nil
		}
		fd, ok := allFiles[rel]
		if !ok {
			return fmt.Errorf("descriptor not found for imported proto %q", rel)
		}
		seen[rel] = true
		for _, imp := range fd.Imports {
			if err := visit(imp); err != nil {
				return err
			}
		}
		return nil
	}

	for _, root := range roots {
		if err := visit(root); err != nil {
			return nil, err
		}
	}

	result := make([]string, 0, len(seen))
	for rel := range seen {
		result = append(result, rel)
	}
	sort.Strings(result)
	return result, nil
}

func copyCSharpProtoClosure(srcDir, dstDir string, protoFiles []string) error {
	if err := os.RemoveAll(dstDir); err != nil {
		return fmt.Errorf("failed to clean C# Protos directory: %w", err)
	}
	for _, rel := range protoFiles {
		rel = filepath.ToSlash(rel)
		src := filepath.Join(srcDir, filepath.FromSlash(rel))
		dst := filepath.Join(dstDir, filepath.FromSlash(rel))
		content, err := os.ReadFile(src)
		if err != nil {
			return fmt.Errorf("failed to read C# proto %s: %w", rel, err)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
			return fmt.Errorf("failed to create C# proto directory for %s: %w", rel, err)
		}
		if err := os.WriteFile(dst, content, 0644); err != nil {
			return fmt.Errorf("failed to write C# proto %s: %w", rel, err)
		}
	}
	return nil
}

func shouldSkipGeneratedInput(rel string) bool {
	return strings.HasPrefix(rel, "google/protobuf/") || rel == "sys/spec.proto"
}

func c2dServicesTemplateData(servicesInfo []service.ServiceInfo) []c2dServiceTemplateData {
	result := make([]c2dServiceTemplateData, 0, len(servicesInfo))
	seen := make(map[string]int)
	for _, svc := range servicesInfo {
		methodBase := "With" + csharpIdentifier(svc.ServiceName)
		methodName := methodBase
		if n := seen[methodBase]; n > 0 {
			methodName = fmt.Sprintf("%s%d", methodBase, n+1)
		}
		seen[methodBase]++
		result = append(result, c2dServiceTemplateData{
			MethodName: methodName,
			ClientType: csharpServiceClientType(svc.PackageName, svc.ServiceName),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].MethodName < result[j].MethodName
	})
	return result
}

func c2dClientProtoFiles(servicesInfo []service.ServiceInfo) []string {
	seen := make(map[string]bool)
	var result []string
	for _, svc := range servicesInfo {
		rel := filepath.ToSlash(svc.ProtoFile)
		if rel == "" || seen[rel] {
			continue
		}
		seen[rel] = true
		result = append(result, rel)
	}
	sort.Strings(result)
	return result
}

// csharpServiceClientType returns the fully-qualified generated gRPC client type
// for a service, as emitted by Grpc.Tools: {Namespace}.{Service}.{Service}Client,
// where {Namespace} is protoc's default C# namespace (PascalCase of the proto package).
func csharpServiceClientType(packageName, serviceName string) string {
	svc := csharpIdentifier(serviceName)
	ns := csharpNamespace(packageName)
	if ns == "" {
		return fmt.Sprintf("%s.%sClient", svc, svc)
	}
	return fmt.Sprintf("%s.%s.%sClient", ns, svc, svc)
}

// csharpNamespace mirrors protoc's default C# namespace for a proto package:
// each dot-separated segment is PascalCased and rejoined with dots.
func csharpNamespace(packageName string) string {
	if packageName == "" {
		return ""
	}
	parts := strings.Split(packageName, ".")
	var clean []string
	for _, part := range parts {
		id := csharpIdentifier(part)
		if id != "" && id != "Generated" {
			clean = append(clean, id)
		}
	}
	return strings.Join(clean, ".")
}

func csharpIdentifier(s string) string {
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	if len(parts) == 0 {
		return "Generated"
	}
	for i := range parts {
		if parts[i] == "" {
			continue
		}
		runes := []rune(parts[i])
		runes[0] = unicode.ToUpper(runes[0])
		parts[i] = string(runes)
	}
	joined := strings.Join(parts, "")
	if joined == "" {
		return "Generated"
	}
	if first := []rune(joined)[0]; unicode.IsDigit(first) {
		return "Generated" + joined
	}
	return joined
}

func csharpModuleName(s string) string {
	ident := csharpIdentifier(s)
	if ident == "Generated" {
		return ""
	}
	return ident
}

func daemonDirectory(config *tactype.Config, servicesInfo []service.ServiceInfo) string {
	if config.SelfPackage != "" {
		return sanitizeDaemonDirectory(config.SelfPackage)
	}
	for _, svc := range servicesInfo {
		if svc.PackageName != "" {
			return sanitizeDaemonDirectory(svc.PackageName)
		}
	}
	return defaultDaemonSocketDir
}

func sanitizeDaemonDirectory(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer(".", "-", "_", "-").Replace(s)
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return defaultDaemonSocketDir
	}
	return b.String()
}

func csharpDependencyVersion(config *tactype.Config, packageName, fallback string) string {
	if config.Lang.CSharp.Dependencies != nil {
		if v := strings.TrimPrefix(config.Lang.CSharp.Dependencies[packageName], "v"); v != "" {
			return v
		}
	}
	return fallback
}

func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	result := values[:0]
	var prev string
	for i, value := range values {
		if i == 0 || value != prev {
			result = append(result, value)
			prev = value
		}
	}
	return result
}
