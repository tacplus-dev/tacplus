// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
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
	defaultSwiftPackageName      = "Tacplus"
	defaultSwiftProtobufVersion  = "1.38.0"
	defaultGRPCSwiftVersion      = "2.4.0"
	defaultGRPCSwiftPBVersion    = "2.4.0"
	defaultGRPCSwiftNIOVersion   = "2.4.0"
	defaultDaemonSocketDirectory = "tacplus"
)

type packageTemplateData struct {
	PackageName                  string
	SwiftProtobufVersion         string
	GRPCSwiftVersion             string
	GRPCSwiftProtobufVersion     string
	GRPCSwiftNIOTransportVersion string
}

type c2dClientTemplateData struct {
	PackageName     string
	DaemonDirectory string
	Services        []c2dServiceTemplateData
}

type c2dServiceTemplateData struct {
	MethodName string
	ClientType string
}

type c2sClientTemplateData struct {
	PackageName string
	Services    []c2dServiceTemplateData
}

type w2sClientTemplateData struct {
	Services []w2sServiceTemplateData
}

type w2sServiceTemplateData struct {
	ServiceName    string
	ClientTypeName string
	PackageName    string
	Methods        []w2sMethodTemplateData
}

type w2sMethodTemplateData struct {
	MethodName      string
	SwiftMethodName string
	HTTPMethod      string
	HTTPPath        string
	AuthRequired    bool
	InputType       string
	OutputType      string
	// IsStreaming is true for methods that are client- or server-streaming.
	// The Swift lite client emits a throwing stub for these so a consumer sees
	// the method at compile time instead of a silent omission.
	IsStreaming bool
}

// Generate generates a SwiftPM client-lite package from proto files.
// Swift is a client-only target: it generates C2D client surfaces from the
// in-scope service_c2d services declared in the proto graph. There is no YAML
// clients.c2d.enabled precondition.
func Generate(config *tactype.Config) error {
	if config.SwiftPackageBase == "" {
		config.SwiftPackageBase = defaultSwiftPackageName
	}
	config.SwiftPackageBase = swiftModuleName(config.SwiftPackageBase)
	if config.SwiftPackageBase == "" {
		config.SwiftPackageBase = defaultSwiftPackageName
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
	// <T>Entitlement messages BEFORE the service closure + spec-option strip so a
	// Swift client sees the same egress types as every other language (it consumes
	// sanitized records; it emits no Sanitize transform).
	if err := proto.InjectRedactIndicatorFields(protoSrcDir, protoSrcDir); err != nil {
		return fmt.Errorf("failed to inject redact indicator fields: %w", err)
	}
	if err := proto.AppendEntitlementMessagesToProtos(protoSrcDir, protoSrcDir); err != nil {
		return fmt.Errorf("failed to append entitlement messages: %w", err)
	}

	// Populate config.GrpcC2SClients / GrpcW2SClients from the proto graph's
	// grpc_c2s_client / grpc_w2s_client declarations (the same proto-driven path
	// the go/ts/rust/java generators use). C2D is resolved separately below.
	if err := service.InferGrpcClientsFromAnnotations(config, protoSrcDir); err != nil {
		return fmt.Errorf("failed to infer Swift client surfaces: %w", err)
	}

	servicesInfo, err := service.DetectServicesForWrapper(config, protoSrcDir, func(config *tactype.Config, _, _, _ string) string {
		return filepath.Join(config.OutputDir, "Sources", config.SwiftPackageBase)
	})
	if err != nil {
		return fmt.Errorf("failed to detect C2D services: %w", err)
	}

	c2dServices := service.ResolveC2DClientServices(config, servicesInfo)
	c2sServices := service.ResolveC2SClientServices(config, servicesInfo)
	w2sServices := service.ResolveW2SClientServices(config, servicesInfo)
	if len(c2dServices) == 0 && len(c2sServices) == 0 && len(w2sServices) == 0 {
		return fmt.Errorf("swift lite found no service_c2d services for package %q", config.SelfPackage)
	}

	closureServices := make([]service.ServiceInfo, 0, len(c2dServices)+len(c2sServices)+len(w2sServices))
	closureServices = append(closureServices, c2dServices...)
	closureServices = append(closureServices, c2sServices...)
	closureServices = append(closureServices, w2sServices...)

	protoFiles, err := collectServiceProtoClosure(protoSrcDir, closureServices)
	if err != nil {
		return fmt.Errorf("failed to collect Swift proto inputs: %w", err)
	}
	if len(protoFiles) == 0 {
		return fmt.Errorf("swift lite collected no proto inputs")
	}

	// W2S method metadata (REST route + input/output types) is derived from spec
	// annotations, so resolve it BEFORE the spec options are stripped from the
	// proto tree below.
	var w2sData []w2sServiceTemplateData
	if len(w2sServices) > 0 {
		w2sData, err = w2sServicesTemplateData(protoSrcDir, w2sServices)
		if err != nil {
			return fmt.Errorf("failed to resolve Swift W2S routes: %w", err)
		}
	}

	if err := proto.StripSpecOptionsFromDir(protoSrcDir); err != nil {
		return fmt.Errorf("failed to strip spec annotations: %w", err)
	}

	sourceDir := filepath.Join(config.OutputDir, "Sources", config.SwiftPackageBase)
	generatedDir := filepath.Join(sourceDir, "Generated")
	if err := os.MkdirAll(generatedDir, 0755); err != nil {
		return fmt.Errorf("failed to create Swift generated directory: %w", err)
	}

	if err := renderPackageSwift(config); err != nil {
		return err
	}
	if len(c2dServices) > 0 {
		if err := renderC2DClient(config, c2dServices); err != nil {
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

	if err := runSwiftProtoc(config, protoSrcDir, generatedDir, protoFiles); err != nil {
		return err
	}

	zap.L().Info("Generated Swift lite package",
		zap.String("package", config.SwiftPackageBase),
		zap.Int("proto_files", len(protoFiles)),
		zap.Int("c2d_services", len(c2dServices)),
		zap.Int("c2s_services", len(c2sServices)),
		zap.Int("w2s_services", len(w2sServices)))

	return nil
}

func renderPackageSwift(config *tactype.Config) error {
	data := packageTemplateData{
		PackageName:                  config.SwiftPackageBase,
		SwiftProtobufVersion:         swiftDependencyVersion(config, "swift-protobuf", defaultSwiftProtobufVersion),
		GRPCSwiftVersion:             swiftDependencyVersion(config, "grpc-swift-2", defaultGRPCSwiftVersion),
		GRPCSwiftProtobufVersion:     swiftDependencyVersion(config, "grpc-swift-protobuf", defaultGRPCSwiftPBVersion),
		GRPCSwiftNIOTransportVersion: swiftDependencyVersion(config, "grpc-swift-nio-transport", defaultGRPCSwiftNIOVersion),
	}

	out, err := executeSwiftTemplate("Package.swift.tmpl", data)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.OutputDir, 0755); err != nil {
		return fmt.Errorf("failed to create Swift package directory: %w", err)
	}
	if err := os.WriteFile(filepath.Join(config.OutputDir, "Package.swift"), out, 0644); err != nil {
		return fmt.Errorf("failed to write Package.swift: %w", err)
	}
	return nil
}

func renderC2DClient(config *tactype.Config, servicesInfo []service.ServiceInfo) error {
	data := c2dClientTemplateData{
		PackageName:     config.SwiftPackageBase,
		DaemonDirectory: daemonDirectory(config, servicesInfo),
		Services:        c2dServicesTemplateData(servicesInfo),
	}

	out, err := executeSwiftTemplate("C2DClient.swift.tmpl", data)
	if err != nil {
		return err
	}
	outPath := filepath.Join(config.OutputDir, "Sources", config.SwiftPackageBase, "C2DClient.swift")
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return fmt.Errorf("failed to create Swift source directory: %w", err)
	}
	if err := os.WriteFile(outPath, out, 0644); err != nil {
		return fmt.Errorf("failed to write C2DClient.swift: %w", err)
	}
	return nil
}

func renderC2SClient(config *tactype.Config, servicesInfo []service.ServiceInfo) error {
	data := c2sClientTemplateData{
		PackageName: config.SwiftPackageBase,
		Services:    c2dServicesTemplateData(servicesInfo),
	}

	out, err := executeSwiftTemplate("C2SClient.swift.tmpl", data)
	if err != nil {
		return err
	}
	outPath := filepath.Join(config.OutputDir, "Sources", config.SwiftPackageBase, "C2SClient.swift")
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return fmt.Errorf("failed to create Swift source directory: %w", err)
	}
	if err := os.WriteFile(outPath, out, 0644); err != nil {
		return fmt.Errorf("failed to write C2SClient.swift: %w", err)
	}
	return nil
}

func renderW2SClient(config *tactype.Config, services []w2sServiceTemplateData) error {
	data := w2sClientTemplateData{Services: services}

	out, err := executeSwiftTemplate("W2SClient.swift.tmpl", data)
	if err != nil {
		return err
	}
	outPath := filepath.Join(config.OutputDir, "Sources", config.SwiftPackageBase, "W2SClient.swift")
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return fmt.Errorf("failed to create Swift source directory: %w", err)
	}
	if err := os.WriteFile(outPath, out, 0644); err != nil {
		return fmt.Errorf("failed to write W2SClient.swift: %w", err)
	}
	return nil
}

func executeSwiftTemplate(name string, data any) ([]byte, error) {
	typeFS, err := fs.Sub(templates.FS, "swift/types")
	if err != nil {
		return nil, fmt.Errorf("failed to create Swift template filesystem: %w", err)
	}
	return tmplutil.ExecuteTemplate(typeFS, name, tmplutil.BaseFuncMap(), data)
}

func runSwiftProtoc(config *tactype.Config, protoSrcDir, generatedDir string, protoFiles []string) error {
	args := []string{
		"--plugin=protoc-gen-swift=" + config.ProtocGenSwift,
		"--plugin=protoc-gen-grpc-swift=" + config.ProtocGenGrpcSwift,
		"--swift_out=" + generatedDir,
		"--swift_opt=Visibility=Public",
		"--swift_opt=FileNaming=PathToUnderscores",
		"--swift_opt=UseAccessLevelOnImports=true",
		"--grpc-swift_out=" + generatedDir,
		"--grpc-swift_opt=Visibility=Public",
		"--grpc-swift_opt=Server=false",
		"--grpc-swift_opt=Client=true",
		"--grpc-swift_opt=FileNaming=PathToUnderscores",
		"--grpc-swift_opt=UseAccessLevelOnImports=true",
		"--proto_path=" + protoSrcDir,
	}
	args = append(args, protoFiles...)

	cmd := exec.Command(config.ProtocPath, args...)
	cmd.Dir = protoSrcDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("protoc Swift generation failed: %w\n%s", err, string(output))
	}
	return nil
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
		if shouldSkipGeneratedSwiftInput(rel) || seen[rel] {
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

func shouldSkipGeneratedSwiftInput(rel string) bool {
	return strings.HasPrefix(rel, "google/protobuf/") || rel == "sys/spec.proto"
}

func c2dServicesTemplateData(servicesInfo []service.ServiceInfo) []c2dServiceTemplateData {
	result := make([]c2dServiceTemplateData, 0, len(servicesInfo))
	seen := make(map[string]int)
	for _, svc := range servicesInfo {
		methodBase := "with" + swiftIdentifier(svc.ServiceName)
		methodName := methodBase
		if n := seen[methodBase]; n > 0 {
			methodName = fmt.Sprintf("%s%d", methodBase, n+1)
		}
		seen[methodBase]++
		result = append(result, c2dServiceTemplateData{
			MethodName: methodName,
			ClientType: swiftServiceNamespace(svc.PackageName, svc.ServiceName),
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].MethodName < result[j].MethodName
	})
	return result
}

// w2sServicesTemplateData resolves per-method REST route + type metadata for the
// W2S (gRPC-over-REST) services. It parses each service's proto (before spec
// options are stripped) to read the convention HTTP path, verb and I/O types.
// W2S is unary-only for Swift: streaming methods are SKIPPED (not emitted, and
// not a hard error) so a mixed service still yields a compilable unary-only
// surface — the drop is logged so it is not silent. A missing HTTP path
// on a UNARY method, by contrast, is still rejected loudly.
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
			// Swift W2S is unary-only: streaming methods are emitted as throwing
			// stubs so a consumer sees them at compile time. A silent continue-skip
			// at Info level was the previous behavior — it was invisible at default
			// CLI log levels (the recurring "silent skip" defect shape).
			if method.ClientStreaming || method.ServerStreaming {
				methods = append(methods, w2sMethodTemplateData{
					MethodName:      method.Name,
					SwiftMethodName: swiftLowerIdentifier(method.Name),
					InputType:       swiftProtoTypeName(method.FullInputType, method.InputType, info.Package),
					OutputType:      swiftProtoTypeName(method.FullOutputType, method.OutputType, info.Package),
					IsStreaming:     true,
				})
				continue
			}
			if method.HttpPath == "" {
				return nil, fmt.Errorf("swift W2S method %s.%s requires REST route metadata (no HTTP path)", target.Name, method.Name)
			}
			authRequired := !(svc.AuthDisabled || target.AuthDisabled || method.AuthDisabled)
			httpMethod := "POST"
			if isEmptySwiftType(method.FullInputType) && method.HttpMethod != "" {
				httpMethod = method.HttpMethod
			}
			methods = append(methods, w2sMethodTemplateData{
				MethodName:      method.Name,
				SwiftMethodName: swiftLowerIdentifier(method.Name),
				HTTPMethod:      httpMethod,
				HTTPPath:        method.HttpPath,
				AuthRequired:    authRequired,
				InputType:       swiftProtoTypeName(method.FullInputType, method.InputType, info.Package),
				OutputType:      swiftProtoTypeName(method.FullOutputType, method.OutputType, info.Package),
			})
		}

		result = append(result, w2sServiceTemplateData{
			ServiceName:    svc.ServiceName,
			ClientTypeName: swiftIdentifier(svc.ServiceName) + "W2SClient",
			PackageName:    info.Package,
			Methods:        methods,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].ClientTypeName < result[j].ClientTypeName
	})
	return result, nil
}

// swiftProtoTypeName maps a proto message type to its swift-protobuf name
// (<Package>_<Message>). fullType is the fully-qualified proto type
// (e.g. "contractclient.ContractRequest"); its package prefix determines the
// swift namespace so cross-package types resolve correctly.
func swiftProtoTypeName(fullType, simpleType, servicePackage string) string {
	t := strings.TrimPrefix(fullType, ".")
	if idx := strings.LastIndex(t, "."); idx >= 0 {
		return swiftServiceNamespace(t[:idx], t[idx+1:])
	}
	return swiftServiceNamespace(servicePackage, simpleType)
}

func isEmptySwiftType(fullType string) bool {
	t := strings.TrimPrefix(fullType, ".")
	return t == "google.protobuf.Empty"
}

func swiftLowerIdentifier(s string) string {
	ident := swiftIdentifier(s)
	if ident == "" {
		return ident
	}
	runes := []rune(ident)
	runes[0] = unicode.ToLower(runes[0])
	return string(runes)
}

func swiftServiceNamespace(packageName, serviceName string) string {
	serviceIdent := swiftIdentifier(serviceName)
	if packageName == "" {
		return serviceIdent
	}
	parts := strings.FieldsFunc(packageName, func(r rune) bool {
		return r == '.' || r == '-' || r == '_'
	})
	var clean []string
	for _, part := range parts {
		if part == "" {
			continue
		}
		clean = append(clean, swiftIdentifier(part))
	}
	if len(clean) == 0 {
		return serviceIdent
	}
	return strings.Join(clean, "_") + "_" + serviceIdent
}

func swiftIdentifier(s string) string {
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

func swiftModuleName(s string) string {
	ident := swiftIdentifier(s)
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
	return defaultDaemonSocketDirectory
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
		return defaultDaemonSocketDirectory
	}
	return b.String()
}

func swiftDependencyVersion(config *tactype.Config, packageName, fallback string) string {
	if config.Lang.Swift.Dependencies != nil {
		if v := strings.TrimPrefix(config.Lang.Swift.Dependencies[packageName], "v"); v != "" {
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
