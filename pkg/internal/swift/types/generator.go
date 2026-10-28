// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

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

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/tmplutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"github.com/tacplus-dev/tacplus/templates"
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
	// RouteMethods is every tenant-routed rpc reachable through the C2S client
	// (issue #170). Non-empty, it installs the route interceptor that refuses an
	// empty or malformed route field before the request is written; empty, the
	// template renders exactly as for a platform-only build.
	RouteMethods []c2sRouteMethodTemplateData
}

// c2sRouteMethodTemplateData is one tenant-routed rpc in the C2S route table.
type c2sRouteMethodTemplateData struct {
	// FullMethod is the grpc-swift MethodDescriptor.fullyQualifiedMethod,
	// "<package>.<Service>/<Method>", the key the interceptor looks up.
	FullMethod       string
	RouteField       string // proto name of the route field, for the refusal message
	RouteFieldNumber int32  // wire number the interceptor reads off the serialized request
	// ClientStreaming rpcs pin the tenant on the first message and refuse any
	// later message naming another; single-request rpcs are checked whole
	// before anything is written.
	ClientStreaming bool
}

type w2sClientTemplateData struct {
	Services []w2sServiceTemplateData
	// HasRoutedMethods gates the route-field check helper and its error case,
	// so a platform-only build renders unchanged.
	HasRoutedMethods bool
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
	// IsStreaming is true for server-streaming methods, which the Swift W2S
	// client consumes as SSE and surfaces as an AsyncThrowingStream.
	IsStreaming bool
	// IsUnsupportedStream is true for client-streaming and bidi methods. The
	// gateway carries those over WebSocket, which the Swift W2S client does not
	// speak, so they are emitted as unavailable stubs — a consumer sees the
	// method at compile time instead of a silent omission.
	IsUnsupportedStream bool
	// RouteField / RouteFieldNumber name the request field carrying the target
	// tenant of a tenant-routed rpc (issue #170): the generated method refuses
	// an empty or malformed value before sending. Empty for platform rpcs and
	// for the unsupported client-streaming/bidi stubs.
	RouteField       string
	RouteFieldNumber int32
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
	// <T>Public for every (spec.public_view) message: its SHOW fields only.
	if err := proto.AppendPublicViewMessagesToProtos(protoSrcDir); err != nil {
		return fmt.Errorf("failed to append public view messages: %w", err)
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
	if err := proto.AppendRecordMapServiceToProtos(protoSrcDir, protoSrcDir, config.SelfPackage); err != nil {
		return fmt.Errorf("failed to append record map service to protos: %w", err)
	}
	// The CLIENT-FACING half, after the record plane above because it folds the
	// same criteria and must see the same records. Emits nothing unless a
	// service declares (spec.record_read).c2s. Every other language backend
	// (go/rust/java/typescript/python) runs both injectors; Swift needs them too
	// so a peer's grpc_c2s_client against a synthesized <Svc>Records service
	// resolves during inference below.
	if err := proto.AppendRecordC2SServiceToProtos(protoSrcDir, protoSrcDir, config.SelfPackage); err != nil {
		return fmt.Errorf("failed to append client-facing record read plane to protos: %w", err)
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
	// The C2S tenant route table reads (spec.rpc).route_field, so it too is
	// resolved before the strip.
	var c2sRoutes []c2sRouteMethodTemplateData
	if len(c2sServices) > 0 {
		c2sRoutes, err = c2sRouteMethods(protoSrcDir, c2sServices)
		if err != nil {
			return fmt.Errorf("failed to resolve Swift C2S tenant routes: %w", err)
		}
	}
	hasRoutedMethods := len(c2sRoutes) > 0 || w2sHasRoutedMethods(w2sData)

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
		if err := renderC2SClient(config, c2sServices, c2sRoutes); err != nil {
			return err
		}
	}
	if len(w2sData) > 0 {
		if err := renderW2SClient(config, w2sData); err != nil {
			return err
		}
	}
	if hasRoutedMethods {
		if err := renderTenantRoute(config); err != nil {
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

func renderC2SClient(config *tactype.Config, servicesInfo []service.ServiceInfo, routes []c2sRouteMethodTemplateData) error {
	data := c2sClientTemplateData{
		PackageName:  config.SwiftPackageBase,
		Services:     c2dServicesTemplateData(servicesInfo),
		RouteMethods: routes,
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
	data := w2sClientTemplateData{Services: services, HasRoutedMethods: w2sHasRoutedMethods(services)}

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

// renderTenantRoute writes the tenant-route helper (tid check + protobuf wire
// walker) the C2S route interceptor and the W2S route check share. Emitted only
// when the build has a tenant-routed rpc.
func renderTenantRoute(config *tactype.Config) error {
	out, err := executeSwiftTemplate("TenantRoute.swift.tmpl", nil)
	if err != nil {
		return err
	}
	outPath := filepath.Join(config.OutputDir, "Sources", config.SwiftPackageBase, "TenantRoute.swift")
	if err := os.MkdirAll(filepath.Dir(outPath), 0755); err != nil {
		return fmt.Errorf("failed to create Swift source directory: %w", err)
	}
	if err := os.WriteFile(outPath, out, 0644); err != nil {
		return fmt.Errorf("failed to write TenantRoute.swift: %w", err)
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
// W2S is unary-only for Swift: a streaming method is emitted as a throwing
// "unsupported" stub rather than dropped (NEW-44), so a mixed service still
// yields a compilable unary-only surface AND the consumer meets the omission at
// compile time instead of finding the method simply absent. A missing HTTP path
// on a UNARY method, by contrast, is still rejected loudly.
// w2sMethodData shapes one W2S method for the Swift client template.
//
// Unary and server-streaming methods both ride the gateway's REST route (the
// latter as SSE), so both carry HTTPPath/HTTPMethod/AuthRequired. Client-
// streaming and bidi go over WebSocket at the gateway, which the Swift W2S
// client does not speak: they are emitted as unavailable stubs so a consumer
// sees them at compile time. A silent continue-skip at Info level was the
// original behavior — it was invisible at default CLI log levels (the recurring
// "silent skip" defect shape).
func w2sMethodData(svc service.ServiceInfo, target *parser.ServiceInfo, method *parser.MethodInfo, protoPackage string) (w2sMethodTemplateData, error) {
	data := w2sMethodTemplateData{
		MethodName:      method.Name,
		SwiftMethodName: swiftLowerIdentifier(method.Name),
		InputType:       swiftProtoTypeName(method.FullInputType, method.InputType, protoPackage),
		OutputType:      swiftProtoTypeName(method.FullOutputType, method.OutputType, protoPackage),
	}
	if method.ClientStreaming {
		data.IsUnsupportedStream = true
		return data, nil
	}
	if method.RouteField != nil {
		if err := checkRouteFieldDescriptor(target.Name, method.Name, method.RouteField); err != nil {
			return w2sMethodTemplateData{}, err
		}
		data.RouteField = method.RouteField.Field
		data.RouteFieldNumber = method.RouteField.Number
	}
	if method.HttpPath == "" {
		return w2sMethodTemplateData{}, fmt.Errorf("swift W2S method %s.%s requires REST route metadata (no HTTP path)", target.Name, method.Name)
	}
	data.HTTPPath = method.HttpPath
	data.AuthRequired = !(svc.AuthDisabled || target.AuthDisabled || method.AuthDisabled)
	data.HTTPMethod = "POST"
	if isEmptySwiftType(method.FullInputType) && method.HttpMethod != "" {
		data.HTTPMethod = method.HttpMethod
	}
	data.IsStreaming = method.ServerStreaming
	return data, nil
}

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
			shaped, err := w2sMethodData(svc, target, method, info.Package)
			if err != nil {
				return nil, err
			}
			methods = append(methods, shaped)
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

func w2sHasRoutedMethods(services []w2sServiceTemplateData) bool {
	for _, svc := range services {
		for _, m := range svc.Methods {
			if m.RouteField != "" {
				return true
			}
		}
	}
	return false
}

// checkRouteFieldDescriptor refuses a route field the generated check could not
// read. The parser guarantees both; this keeps a generator bug from emitting a
// check that refuses every call (number 0) or names no field.
func checkRouteFieldDescriptor(serviceName, methodName string, rf *parser.RouteFieldDescriptor) error {
	if rf.Field == "" || rf.Number <= 0 {
		return fmt.Errorf("swift: %s.%s has an unresolved (spec.rpc).route_field (field %q, number %d)", serviceName, methodName, rf.Field, rf.Number)
	}
	return nil
}

// c2sRouteMethods resolves the C2S tenant route table: every rpc of the C2S
// client services that declares (spec.rpc).route_field. Read from the parsed
// descriptors before the spec options are stripped.
func c2sRouteMethods(protoDir string, servicesInfo []service.ServiceInfo) ([]c2sRouteMethodTemplateData, error) {
	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return nil, err
	}
	allFiles := dp.GetAllFiles()
	var result []c2sRouteMethodTemplateData
	for _, svc := range servicesInfo {
		fd, ok := allFiles[filepath.ToSlash(svc.ProtoFile)]
		if !ok {
			return nil, fmt.Errorf("descriptor not found for C2S service %q proto %q", svc.ServiceName, svc.ProtoFile)
		}
		routes, err := c2sRouteMethodsForService(fd, svc.ServiceName)
		if err != nil {
			return nil, fmt.Errorf("C2S service %q in proto %q: %w", svc.ServiceName, svc.ProtoFile, err)
		}
		result = append(result, routes...)
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].FullMethod < result[j].FullMethod
	})
	// Two client services resolving to the same proto service yield the same
	// entries; a Swift dictionary literal with a duplicate key traps at run time.
	deduped := result[:0]
	for i, r := range result {
		if i > 0 && r.FullMethod == result[i-1].FullMethod {
			if r != result[i-1] {
				return nil, fmt.Errorf("C2S method %s resolved to two different route fields", r.FullMethod)
			}
			continue
		}
		deduped = append(deduped, r)
	}
	return deduped, nil
}

// c2sRouteMethodsForService returns the routed rpcs of serviceName in fd,
// keyed the way grpc-swift names the method on the wire.
func c2sRouteMethodsForService(fd *parser.FileDescriptor, serviceName string) ([]c2sRouteMethodTemplateData, error) {
	var target *parser.ServiceDescriptor
	for i := range fd.Services {
		if fd.Services[i].Name == serviceName {
			target = &fd.Services[i]
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("service %q not found", serviceName)
	}
	fullService := target.Name
	if fd.Package != "" {
		fullService = fd.Package + "." + target.Name
	}
	var result []c2sRouteMethodTemplateData
	for i := range target.Methods {
		method := &target.Methods[i]
		rf := method.RouteField
		if rf == nil {
			continue
		}
		if err := checkRouteFieldDescriptor(target.Name, method.Name, rf); err != nil {
			return nil, err
		}
		result = append(result, c2sRouteMethodTemplateData{
			FullMethod:       fullService + "/" + method.Name,
			RouteField:       rf.Field,
			RouteFieldNumber: rf.Number,
			ClientStreaming:  method.ClientStreaming,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].FullMethod < result[j].FullMethod
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
