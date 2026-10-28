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

	"github.com/tacplus-dev/tacplus/pkg/internal/common/boundaries"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/service"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// ServiceInfo is a type alias for common service.ServiceInfo.
type ServiceInfo = service.ServiceInfo

// detectServicesForWrapper scans proto files to find service definitions that need wrapper generation.
// Includes annotated gRPC services and state services (service_state on messages).
func detectServicesForWrapper(config *tactype.Config, inputDir string) ([]ServiceInfo, error) {
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	var servicesInfo []ServiceInfo

	err = filepath.Walk(inputDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}

		relPath, _ := filepath.Rel(inputDir, path)
		fileDesc, err := descriptorParser.GetFileDescriptor(relPath)
		if err != nil {
			zap.L().Debug("Skipping proto file (no descriptor)", zap.String("path", relPath), zap.Error(err))
			return nil
		}

		if len(fileDesc.Services) == 0 {
			return nil
		}

		// Check if any service has a gRPC name, or any message has service_state
		hasGrpc := false
		for _, svc := range fileDesc.Services {
			if svc.GrpcName != "" {
				hasGrpc = true
				break
			}
		}
		hasState := false
		for _, msg := range fileDesc.Messages {
			if ann, ok := msg.Annotations["service_state"]; ok && ann.StringValue != "" {
				hasState = true
				break
			}
		}
		if !hasGrpc && !hasState {
			return nil
		}

		parentDir := filepath.Dir(relPath)
		packageName := fileDesc.Package
		// Strip dotted sub-packages (e.g., "baseball.services" → "baseball")
		if idx := strings.Index(packageName, "."); idx > 0 {
			packageName = packageName[:idx]
		}

		// Python service wrappers go to: outputDir/sm/internal/grpc/servers/{parentDir}/{packageName}/
		subdir := filepath.Join(config.GetSmPrefix(), "internal", "grpc", "servers", pyPath(parentDir), pyModuleName(packageName))
		outputDir := filepath.Join(config.OutputDir, subdir)

		for _, service := range fileDesc.Services {
			isGrpc := service.GrpcName != ""
			if !isGrpc && !hasState {
				continue
			}
			anyUser, hasMethodAuth := derivePyServiceAuthFlags(service)
			servicesInfo = append(servicesInfo, ServiceInfo{
				ProtoFile:     relPath,
				PackageName:   packageName,
				ServiceName:   service.Name,
				GrpcName:      service.GrpcName,
				IsGrpc:        isGrpc,
				IsS2S:         service.IsS2S,
				IsC2S:         service.IsC2S,
				IsC2D:         service.IsC2D,
				OutputDir:     outputDir,
				ParentDir:     parentDir,
				Config:        config,
				AuthDisabled:  service.AuthDisabled,
				AuthAnyUser:   anyUser,
				AuthServices:  service.AuthServices,
				HasMethodAuth: hasMethodAuth,
				HostLabel:     service.HostLabel,
			})
		}

		return nil
	})

	return servicesInfo, err
}

// derivePyServiceAuthFlags derives a service's auth_any_user and HasMethodAuth
// flags from its own annotation plus its methods'.
//
// hasMethodAuth answers one question only: will generateServiceWrapper emit a
// register_method_auth function for this service? service_manager.py.tmpl calls
// that function whenever the flag is set and never calls it when it is not, so
// the conditions below must mirror the methodAuthLines conditions in
// generateServiceWrapper exactly.
func derivePyServiceAuthFlags(desc parser.ServiceDescriptor) (anyUser bool, hasMethodAuth bool) {
	anyUser = desc.AuthAnyUser
	// A service with no RPCs registers nothing, and its wrapper emits no
	// register-method-auth function; see the Go generator for the
	// ServiceManager call this would otherwise dangle.
	hasMethodAuth = len(desc.Methods) > 0 && len(desc.AuthServices) > 0
	for _, m := range desc.Methods {
		if m.AuthAnyUser {
			anyUser = true
		}
		if len(m.AuthServices) > 0 {
			hasMethodAuth = true
		}
		// Mirrors the set_method_target_namespace_field condition in
		// generateServiceWrapper: an auth_any_user method uses
		// target_namespace_field as a fixed namespace literal in its own gate
		// and registers nothing, so only an ordinary method sets the flag.
		// Without this the C2S shape (target_namespace_field, no auth_services
		// anywhere) left the flag false while the wrapper registered the field,
		// so the ServiceManager never called register_method_auth and the
		// authorizer's level-or-below check silently had nothing to enforce.
		if m.TargetNamespaceField != "" && !m.AuthAnyUser {
			hasMethodAuth = true
		}
	}
	return anyUser, hasMethodAuth
}

// generateServiceWrappers generates servicewrapper.py for each service in the types directory.
// Uses a single DescriptorParser rooted at inputDir so protoc can resolve cross-package imports.
func generateServiceWrappers(servicesInfo []ServiceInfo, inputDir string, accountingMap map[string]service.AccountingInfo) error {
	// Create one descriptor parser for the entire proto root
	descriptorParser, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	// Q2: resolve the accounting topic once so every wrapper can drop that
	// topic's own publish RPC from its accounting map. See
	// accounting_exclusion.go for why this exclusion is mandatory.
	accountingTopic, hasAccountingTopic, err := boundaries.LookupAccountingRecordTopic(descriptorParser)
	if err != nil {
		return fmt.Errorf("resolve accounting record topic: %w", err)
	}

	for _, svc := range servicesInfo {
		// Use the descriptor parser to get method information
		protoInfo, err := descriptorParser.ConvertToProtoInfo(svc.ProtoFile)
		if err != nil {
			return fmt.Errorf("failed to parse %s: %w", svc.ProtoFile, err)
		}

		// Generate servicewrapper.py in the types output directory
		svcAccountingMap := excludeAccountingSelfPublish(
			accountingMap, svc.GrpcName, protoInfo.Package, svc.ServiceName,
			accountingTopic, hasAccountingTopic)
		if err := generateServiceWrapper(svc, protoInfo, svcAccountingMap); err != nil {
			return fmt.Errorf("failed to generate servicewrapper for %s: %w", svc.ServiceName, err)
		}
	}
	return nil
}

// generateServiceWrapper creates the servicewrapper.py file for a service
func generateServiceWrapper(svc ServiceInfo, info *parser.ProtoInfo, accountingMap map[string]service.AccountingInfo) error {
	// Determine wrapper filename, stripping redundant package prefix
	pkgName := pyModuleName(svc.PackageName)
	wrapperFile := strutil.StripPackagePrefix(pyGeneratedSnakeName(svc.ServiceName), pkgName) + ".py"
	outputDir := filepath.Join(svc.Config.OutputDir, svc.Config.GetSmPrefix(), "internal", "grpc", "servers", pyPath(svc.ParentDir), pkgName)
	outputPath := filepath.Join(outputDir, wrapperFile)

	// Create __init__.py files for each directory in the sm path (Python package requirement)
	smBase := filepath.Join(svc.Config.OutputDir, svc.Config.GetSmPrefix())
	for _, dir := range []string{
		smBase,
		filepath.Join(smBase, "internal"),
		filepath.Join(smBase, "internal", "grpc"),
		filepath.Join(smBase, "internal", "grpc", "servers"),
		filepath.Join(smBase, "internal", "grpc", "servers", pyPath(svc.ParentDir)),
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

	zap.L().Info("Generated service wrapper", zap.String("path", outputPath))

	serviceName := svc.ServiceName
	serviceLabel := svc.GrpcName
	if serviceLabel == "" {
		serviceLabel = svc.ServiceName
	}

	pbModuleName := pyProtoFileModuleName(svc.ProtoFile)

	// Compute relative import path from wrapper dir to types dir.
	parentParts := strings.Split(svc.ParentDir, string(filepath.Separator))
	wrapperDepth := 4 + len(parentParts) + 1 // sm + internal + grpc + servers + parentDir parts + packageName
	dots := strings.Repeat(".", wrapperDepth+1)
	pbImportFrom := dots + "types." + pyProtoImportPath(svc.ParentDir, svc.PackageName, svc.ProtoFile)
	helpersImportFrom := pbImportFrom + ".helpers"
	collectionImportFrom := dots + "chassis.collection"
	traceImportFrom := dots + "types.sys.trace"
	accountingImportFrom := dots + "chassis.am"
	accountingMethodImportFrom := dots + "types.generated.accounting_method"

	// Build cross-package imports for Python (only for packages referenced by the target service)
	var targetService *parser.ServiceInfo
	for i := range info.Services {
		if info.Services[i].Name == svc.ServiceName {
			targetService = info.Services[i]
			break
		}
	}

	usedPkgs := make(map[string]bool)
	if targetService != nil {
		for _, method := range targetService.Methods {
			for _, fullType := range []string{method.FullInputType, method.FullOutputType} {
				fullType = strings.TrimPrefix(fullType, ".")
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
	var crossPkgHelperImports strings.Builder
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
		crossImportFrom := crossDots + "types." + pyCrossProtoImportPath(info, pkg)
		crossPbModule := pyProtoFileModuleName(pyCrossProtoFile(info, pkg))
		fmt.Fprintf(&crossPkgImports, "from %s import %s_pb2 as %s\n",
			crossImportFrom, crossPbModule, crossAlias)
		fmt.Fprintf(&crossPkgHelperImports, "from %s import helpers as %s_helpers\n",
			crossImportFrom, crossPkgSnake)
	}
	if needsGoogleProtobuf {
		crossPkgImports.WriteString("from google.protobuf import empty_pb2\n")
	}

	// Determine wrapper name from annotation
	wrapperName := "GrpcServer"
	if !svc.IsGrpc {
		wrapperName = "StateClient"
	}

	// Build handler interface, gRPC method handlers, and service registration
	var handlerInterface, grpcHandlerMethods, serviceRegistration, watchFields strings.Builder
	var methodAuthLines []string
	hasHandler := targetService != nil && len(targetService.Methods) > 0

	if hasHandler {
		// Generate ABC handler interface
		fmt.Fprintf(&handlerInterface, "class %sHandler(ABC):\n", serviceName)
		fmt.Fprintf(&handlerInterface, "    \"\"\"Handler interface for %s.\n    Implement this class to provide business logic for each RPC method.\"\"\"\n\n", serviceName)
		for _, method := range targetService.Methods {
			handlerInterface.WriteString(generatePyHandlerMethodSignature(method, info.Package))
		}

		// Generate per-method wrapper functions
		for _, method := range targetService.Methods {
			if method.IsWatchCollection() {
				fmt.Fprintf(&watchFields, "        self._%s_watch_hub = WatchHub(%d, %d)\n",
					pyGeneratedSnakeName(method.Name), method.Resolved.ReplayDepth, method.Resolved.IdleSecs)
			}
			grpcHandlerMethods.WriteString(generatePyGRPCWrapperMethod(targetService, method, info.Package, svc.ParentDir, svc.GrpcName, accountingMap))

			// Per-method policy is keyed by the SERVICE-QUALIFIED method
			// identifier the authorizer derives from the incoming full gRPC
			// method (_qualified_method_key), never by the bare RPC name: one
			// process may host several services behind one authorizer, and a
			// bare-name key lets a second service's identically named RPC
			// silently overwrite the first's auth_services.
			methodAuthKey := pyQualifiedMethodAuthKey(info.Package, targetService.Name, method.Name)

			// Resolve effective auth_services: method-level overrides service-level.
			effectiveAuth := svc.AuthServices
			if len(method.AuthServices) > 0 {
				effectiveAuth = method.AuthServices
			}
			if len(effectiveAuth) > 0 {
				var quoted []string
				for _, pattern := range effectiveAuth {
					quoted = append(quoted, fmt.Sprintf("%q", pattern))
				}
				methodAuthLines = append(methodAuthLines, fmt.Sprintf(
					"        authorizer.set_method_auth(%q, [%s])",
					methodAuthKey, strings.Join(quoted, ", ")))
			}

			// RPC_FLAG_ANY_TENANT: the tenant-owned door is open on this rpc only.
			if method.AnyTenant {
				methodAuthLines = append(methodAuthLines, fmt.Sprintf(
					"        authorizer.set_method_any_tenant(%q)", methodAuthKey))
			}

			// Register request-field target_namespace_field for level-or-below enforcement.
			// auth_any_user uses target_namespace_field as a fixed namespace literal instead.
			if method.TargetNamespaceField != "" && !method.AuthAnyUser {
				methodAuthLines = append(methodAuthLines, fmt.Sprintf(
					"        authorizer.set_method_target_namespace_field(%q, %q)",
					methodAuthKey, method.TargetNamespaceField))
			}
		}

		// Generate service registration
		serviceRegistration.WriteString(generatePyServiceRegistration(targetService, info.Package))
	}

	// Build register_method_auth function.
	var registerMethodAuth string
	if len(methodAuthLines) > 0 {
		registerMethodAuth = "    def register_method_auth(self, authorizer):\n        \"\"\"Registers per-method auth_services patterns with the Authorizer.\"\"\"\n" +
			strings.Join(methodAuthLines, "\n") + "\n"
	}

	data := struct {
		Header                     string
		ProtoFile                  string
		ServiceName                string
		ServiceLabel               string
		ServiceLower               string
		WrapperName                string
		PbModuleName               string
		PbImportFrom               string
		HelpersImportFrom          string
		CollectionImportFrom       string
		TraceImportFrom            string
		AccountingImportFrom       string
		AccountingMethodImportFrom string
		CrossPackageImports        string
		CrossHelperImports         string
		HasAccounting              bool
		HasHandler                 bool
		HandlerInterface           string
		WatchFields                string
		GRPCHandlerMethods         string
		ServiceRegistration        string
		ProtoPackage               string
		RegisterMethodAuth         string
	}{
		Header:                     header.GeneratedHeader(consts.LangPython, svc.ProtoFile),
		ProtoFile:                  svc.ProtoFile,
		ServiceName:                serviceName,
		ServiceLabel:               serviceLabel,
		ServiceLower:               strutil.ToSnakeCase(serviceName),
		WrapperName:                wrapperName,
		PbModuleName:               pbModuleName,
		PbImportFrom:               pbImportFrom,
		HelpersImportFrom:          helpersImportFrom,
		CollectionImportFrom:       collectionImportFrom,
		TraceImportFrom:            traceImportFrom,
		AccountingImportFrom:       accountingImportFrom,
		AccountingMethodImportFrom: accountingMethodImportFrom,
		CrossPackageImports:        crossPkgImports.String(),
		CrossHelperImports:         crossPkgHelperImports.String(),
		HasAccounting:              serviceHasAccounting(targetService, svc.GrpcName, accountingMap),
		HasHandler:                 hasHandler,
		HandlerInterface:           handlerInterface.String(),
		WatchFields:                watchFields.String(),
		GRPCHandlerMethods:         grpcHandlerMethods.String(),
		ServiceRegistration:        serviceRegistration.String(),
		ProtoPackage:               info.Package,
		RegisterMethodAuth:         registerMethodAuth,
	}

	tmplContent, err := loadTemplate("servicewrapper.py.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load template: %w", err)
	}

	t, err := template.New("servicewrapper").Parse(tmplContent)
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

// pyWrapperTypeRef returns the Python protobuf type reference for a method's input/output type
// within service wrapper code (uses pb.X / cross_pb.X module-qualified references).
func pyWrapperTypeRef(fullType, simpleType, servicePackage string) string {
	fullType = strings.TrimPrefix(fullType, ".")
	if fullType == "google.protobuf.Empty" {
		return "empty_pb2.Empty"
	}
	parts := strings.Split(fullType, ".")
	if len(parts) > 1 {
		pkg := strings.Join(parts[:len(parts)-1], ".")
		if pkg != servicePackage && pkg != "" && pkg != "google.protobuf" {
			return pyModuleName(strutil.ToSnakeCase(pkg)) + "_pb." + simpleType
		}
	}
	return "pb." + simpleType
}

func simpleTypeName(fullType string) string {
	if i := strings.LastIndex(fullType, "."); i >= 0 {
		return fullType[i+1:]
	}
	return fullType
}

// pyValidateRef returns the Python validation function name for a type.
// Returns empty string for google.protobuf well-known types.
func pyValidateRef(fullType, simpleType, servicePackage string) string {
	fullType = strings.TrimPrefix(fullType, ".")
	if strings.HasPrefix(fullType, "google.protobuf.") {
		return ""
	}
	// Python validation functions use snake_case: validate_type_name
	validateName := "validate_" + strutil.ToSnakeCase(simpleType)
	parts := strings.Split(fullType, ".")
	if len(parts) > 1 {
		pkg := strings.Join(parts[:len(parts)-1], ".")
		if pkg != servicePackage && pkg != "" && pkg != "google.protobuf" {
			return pyModuleName(strutil.ToSnakeCase(pkg)) + "_helpers." + validateName
		}
	}
	return validateName
}

// generatePyHandlerMethodSignature generates a single handler ABC method signature.
func generatePyHandlerMethodSignature(method *parser.MethodInfo, servicePackage string) string {
	methodName := pyGeneratedSnakeName(method.Name)
	inputType := pyWrapperTypeRef(method.FullInputType, method.InputType, servicePackage)
	outputType := pyWrapperTypeRef(method.FullOutputType, method.OutputType, servicePackage)
	emptyInput := method.FullInputType == "google.protobuf.Empty"
	emptyOutput := method.FullOutputType == "google.protobuf.Empty"

	switch {
	case method.ClientStreaming && method.ServerStreaming:
		return fmt.Sprintf("    @abstractmethod\n    def %s(self, request_iterator, context):\n        \"\"\"Bidirectional streaming RPC.\"\"\"\n        pass\n\n", methodName)
	case method.IsTier2SnapshotCollection():
		// Tier-2 SNAPSHOT collection: return a materialized list; the wrapper
		// chunks it into the server-stream.
		if emptyInput {
			return fmt.Sprintf("    @abstractmethod\n    def %s(self, context) -> list[%s]:\n        \"\"\"Tier-2 SNAPSHOT collection. Returns a materialized list of %s.\"\"\"\n        pass\n\n", methodName, outputType, outputType)
		}
		return fmt.Sprintf("    @abstractmethod\n    def %s(self, req: %s, context) -> list[%s]:\n        \"\"\"Tier-2 SNAPSHOT collection. Returns a materialized list of %s.\"\"\"\n        pass\n\n", methodName, inputType, outputType, outputType)
	case method.IsWatchCollection():
		itemType := pyWrapperTypeRef(method.Resolved.WatchItemType, simpleTypeName(method.Resolved.WatchItemType), servicePackage)
		changeType := pyWrapperTypeRef(method.Resolved.WatchChangeType, simpleTypeName(method.Resolved.WatchChangeType), servicePackage)
		if emptyInput {
			return fmt.Sprintf("    @abstractmethod\n    def %s(self, context) -> WatchSource[%s, %s]:\n        \"\"\"WATCH collection. Returns snapshot plus live changes.\"\"\"\n        pass\n\n", methodName, itemType, changeType)
		}
		return fmt.Sprintf("    @abstractmethod\n    def %s(self, req: %s, context) -> WatchSource[%s, %s]:\n        \"\"\"WATCH collection. Returns snapshot plus live changes.\"\"\"\n        pass\n\n", methodName, inputType, itemType, changeType)
	case method.IsPageCollection():
		// PAGE: unary, client-driven window. The wrapper owns the page_token codec
		// and the page_size bound; the handler is a keyset producer: given
		// PageParams(limit, after) (plus req for the filter fields) it returns one
		// Page (items + the cursor to resume after + an optional total).
		itemType := pyWrapperTypeRef(method.Resolved.PageItemType, simpleTypeName(method.Resolved.PageItemType), servicePackage)
		if emptyInput {
			return fmt.Sprintf("    @abstractmethod\n    def %s(self, page: PageParams, context) -> Page[%s]:\n        \"\"\"PAGE collection. Keyset producer: returns one Page of %s.\"\"\"\n        pass\n\n", methodName, itemType, itemType)
		}
		return fmt.Sprintf("    @abstractmethod\n    def %s(self, req: %s, page: PageParams, context) -> Page[%s]:\n        \"\"\"PAGE collection. Keyset producer: returns one Page of %s.\"\"\"\n        pass\n\n", methodName, inputType, itemType, itemType)
	case method.ServerStreaming:
		if emptyInput {
			return fmt.Sprintf("    @abstractmethod\n    def %s(self, context):\n        \"\"\"Server streaming RPC. Yields %s.\"\"\"\n        pass\n\n", methodName, outputType)
		}
		return fmt.Sprintf("    @abstractmethod\n    def %s(self, req: %s, context):\n        \"\"\"Server streaming RPC. Yields %s.\"\"\"\n        pass\n\n", methodName, inputType, outputType)
	case method.ClientStreaming:
		if emptyOutput {
			return fmt.Sprintf("    @abstractmethod\n    def %s(self, request_iterator, context) -> None:\n        \"\"\"Client streaming RPC.\"\"\"\n        pass\n\n", methodName)
		}
		return fmt.Sprintf("    @abstractmethod\n    def %s(self, request_iterator, context) -> %s:\n        \"\"\"Client streaming RPC.\"\"\"\n        pass\n\n", methodName, outputType)
	default:
		// Unary
		if emptyInput && emptyOutput {
			return fmt.Sprintf("    @abstractmethod\n    def %s(self) -> None:\n        pass\n\n", methodName)
		} else if emptyInput {
			return fmt.Sprintf("    @abstractmethod\n    def %s(self) -> %s:\n        pass\n\n", methodName, outputType)
		} else if emptyOutput {
			return fmt.Sprintf("    @abstractmethod\n    def %s(self, req: %s) -> None:\n        pass\n\n", methodName, inputType)
		}
		return fmt.Sprintf("    @abstractmethod\n    def %s(self, req: %s) -> %s:\n        pass\n\n", methodName, inputType, outputType)
	}
}

func pyAuthBlock(authCall string) string {
	if strings.TrimSpace(authCall) == "" {
		return ""
	}
	return fmt.Sprintf(`        try:
            %s
        except Exception as e:
            self._abort_handler_error(context, e)
`, authCall)
}

func pyIndentBlock(block string, spaces int) string {
	if strings.TrimSpace(block) == "" {
		return ""
	}
	prefix := strings.Repeat(" ", spaces)
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = prefix + line
		}
	}
	return strings.Join(lines, "\n")
}

func serviceHasAccounting(svc *parser.ServiceInfo, grpcName string, accountingMap map[string]service.AccountingInfo) bool {
	if svc == nil || grpcName == "" {
		return false
	}
	info, ok := accountingMap[grpcName]
	if !ok {
		return false
	}
	for _, method := range svc.Methods {
		if info.RPCs[method.Name] {
			return true
		}
	}
	return false
}

func pyAccountingRPCOrdinal(info service.AccountingInfo, methodName string) int {
	if info.RPCOrdinals == nil {
		return 0
	}
	return info.RPCOrdinals[methodName]
}

func pyAccountingMethodExpression(grpcName, methodName string, accountingMap map[string]service.AccountingInfo) string {
	if grpcName == "" {
		return ""
	}
	info, ok := accountingMap[grpcName]
	if !ok || !info.RPCs[methodName] {
		return ""
	}
	ordinal := pyAccountingRPCOrdinal(info, methodName)
	if ordinal == 0 {
		return ""
	}
	return fmt.Sprintf("accounting_method_pb2.AccountingMethod(%s=%d)", grpcName, ordinal)
}

func pyAccountingPreamble(methodExpr string) string {
	if methodExpr == "" {
		return ""
	}
	return `        __tacplus_accounting_start = time.time()
        __tacplus_accounting_error = None
`
}

func pyAccountingCatchMark(methodExpr string) string {
	if methodExpr == "" {
		return ""
	}
	return "            __tacplus_accounting_error = e\n"
}

func pyAccountingFinally(methodExpr string) string {
	if methodExpr == "" {
		return ""
	}
	return fmt.Sprintf(`        finally:
            self._emit_accounting(%s, __tacplus_accounting_start, __tacplus_accounting_error, context)
`, methodExpr)
}

// generatePyGRPCWrapperMethod generates a single gRPC method handler for the wrapper class.
func generatePyGRPCWrapperMethod(svc *parser.ServiceInfo, method *parser.MethodInfo, servicePackage, parentDir, grpcName string, accountingMap map[string]service.AccountingInfo) string {
	methodName := pyGeneratedSnakeName(method.Name)
	emptyInput := method.FullInputType == "google.protobuf.Empty"
	emptyOutput := method.FullOutputType == "google.protobuf.Empty"
	fullMethodPath := fmt.Sprintf("/%s.%s/%s", servicePackage, svc.Name, method.Name)

	authDisabled := method.AuthDisabled || svc.AuthDisabled || svc.IsC2D
	authAnyUser := method.AuthAnyUser

	validateInput := pyValidateRef(method.FullInputType, method.InputType, servicePackage)
	validateOutput := pyValidateRef(method.FullOutputType, method.OutputType, servicePackage)
	outputType := pyWrapperTypeRef(method.FullOutputType, method.OutputType, servicePackage)
	accountingMethodExpr := pyAccountingMethodExpression(grpcName, method.Name, accountingMap)
	traceMethodArg := ""
	if accountingMethodExpr != "" {
		traceMethodArg = fmt.Sprintf(", method=%s", accountingMethodExpr)
	}

	// Build auth call based on auth mode
	authCallNoReq := ""
	authCallWithReq := ""
	if !authDisabled {
		if authAnyUser {
			// On auth_any_user methods target_namespace_field is an authored fixed
			// namespace literal, not a request field name. Unset means no namespace
			// check at all — "any authenticated user can call this RPC". A "/"
			// default would gate on is_at_or_below(caller, "/"), true only for a
			// caller sitting exactly at "/", denying every real caller.
			targetNamespace := strings.TrimSpace(method.TargetNamespaceField)
			switch {
			case method.AnyTenant && targetNamespace == "":
				authCallNoReq = fmt.Sprintf("self._authenticate(context, None, '%s')", fullMethodPath)
			case method.AnyTenant:
				authCallNoReq = fmt.Sprintf("self._authenticate(context, %q, '%s')", targetNamespace, fullMethodPath)
			case targetNamespace == "":
				authCallNoReq = "self._authenticate(context)"
			default:
				authCallNoReq = fmt.Sprintf("self._authenticate(context, %q)", targetNamespace)
			}
			authCallWithReq = authCallNoReq
		} else {
			authCallNoReq = fmt.Sprintf("self._authorize(context, '%s')", fullMethodPath)
			authCallWithReq = fmt.Sprintf("self._authorize(context, '%s', request)", fullMethodPath)
		}
	}
	authNoReqBlock := pyAuthBlock(authCallNoReq)
	authWithReqBlock := pyAuthBlock(authCallWithReq)
	streamMessageAuthorize := "None"
	if method.ClientStreaming && method.TargetNamespaceField != "" && !authDisabled && !authAnyUser {
		streamMessageAuthorize = fmt.Sprintf("lambda request: self._authorize(context, '%s', request)", fullMethodPath)
	}
	streamOpenAuthBlock := authNoReqBlock
	if streamMessageAuthorize != "None" {
		// An RPC_FLAG_ANY_TENANT method names itself so the opened door holds.
		if method.AnyTenant {
			streamOpenAuthBlock = pyAuthBlock(fmt.Sprintf("self._authenticate(context, None, '%s')", fullMethodPath))
		} else {
			streamOpenAuthBlock = pyAuthBlock("self._authenticate(context)")
		}
	}

	requestValidation := ""
	if validateInput != "" && !emptyInput {
		requestValidation = fmt.Sprintf(`        try:
            %s(request)
        except Exception as e:
            self._abort_request_validation(context, e)

`, validateInput)
	}

	responseValidation := ""
	if validateOutput != "" && !emptyOutput {
		responseValidation = fmt.Sprintf(`            try:
                %s(response)
            except Exception as e:
                self._abort_response_validation(context, e)
`, validateOutput)
	}

	streamRequestIterator := "request_iterator"
	if validateInput != "" || streamMessageAuthorize != "None" {
		validateArg := "None"
		if validateInput != "" {
			validateArg = validateInput
		}
		streamRequestIterator = fmt.Sprintf("self._validating_request_iterator(request_iterator, context, %s, %s)", validateArg, streamMessageAuthorize)
	}

	// Streaming methods
	if method.ClientStreaming && method.ServerStreaming {
		streamResponseValidation := pyIndentBlock(responseValidation, 4)
		return fmt.Sprintf(`    def _handle_%s(self, request_iterator, context):
        """Handles %s (bidirectional streaming)."""
%s%s        try:
            responses = self.handler.%s(%s, context)
            for response in responses:
%s                yield response
        except Exception as e:
%s            self._abort_handler_error(context, e)
%s

`, methodName, method.Name, streamOpenAuthBlock, pyAccountingPreamble(accountingMethodExpr), methodName, streamRequestIterator, streamResponseValidation, pyAccountingCatchMark(accountingMethodExpr), pyAccountingFinally(accountingMethodExpr))
	}

	if method.IsWatchCollection() {
		handlerCall := fmt.Sprintf("self.handler.%s(request, context)", methodName)
		if emptyInput {
			handlerCall = fmt.Sprintf("self.handler.%s(context)", methodName)
		}
		snapshotResponseValidation := pyIndentBlock(responseValidation, 8)
		watchResponseValidation := pyIndentBlock(responseValidation, 4)
		return fmt.Sprintf(`    def _handle_%s(self, request, context):
        """Handles %s (WATCH collection)."""
%s
%s%s        try:
            source = %s
            from_revision = getattr(request, "from_revision", 0)
            resume_revision = from_revision
            if from_revision == 0:
                for item in source.snapshot:
                    response = pb.%s(snapshot_item=item)
%s                    yield response
                response = pb.%s(snapshot_done=source.revision)
%s                yield response
                resume_revision = source.revision
            for change in self._%s_watch_hub.watch(source.scope, resume_revision, source.changes, lambda c: getattr(c, "revision")):
                response = pb.%s(delta=change)
%s                yield response
        except Exception as e:
%s
            self._abort_handler_error(context, e)
%s

`, methodName, method.Name, authWithReqBlock, requestValidation, pyAccountingPreamble(accountingMethodExpr),
			handlerCall, method.OutputType, snapshotResponseValidation,
			method.OutputType, watchResponseValidation,
			methodName, method.OutputType, watchResponseValidation, pyAccountingCatchMark(accountingMethodExpr), pyAccountingFinally(accountingMethodExpr))
	}

	if method.ServerStreaming {
		handlerCall := fmt.Sprintf("self.handler.%s(request, context)", methodName)
		if emptyInput {
			handlerCall = fmt.Sprintf("self.handler.%s(context)", methodName)
		}
		streamResponseValidation := pyIndentBlock(responseValidation, 4)
		return fmt.Sprintf(`    def _handle_%s(self, request, context):
        """Handles %s (server streaming)."""
%s
%s%s        try:
            responses = %s
            for response in responses:
%s                yield response
        except Exception as e:
%s            self._abort_handler_error(context, e)
%s

`, methodName, method.Name, authWithReqBlock, requestValidation, pyAccountingPreamble(accountingMethodExpr), handlerCall, streamResponseValidation, pyAccountingCatchMark(accountingMethodExpr), pyAccountingFinally(accountingMethodExpr))
	}

	if method.ClientStreaming {
		clientStreamResponseValidation := ""
		if validateOutput != "" && !emptyOutput {
			clientStreamResponseValidation = fmt.Sprintf(`            try:
                %s(resp)
            except Exception as e:
                self._abort_response_validation(context, e)
`, validateOutput)
		}
		if emptyOutput {
			return fmt.Sprintf(`    def _handle_%s(self, request_iterator, context):
        """Handles %s (client streaming)."""
%s%s        try:
            self.handler.%s(%s, context)
        except Exception as e:
%s
            self._abort_handler_error(context, e)
%s
        return empty_pb2.Empty()

`, methodName, method.Name, streamOpenAuthBlock, pyAccountingPreamble(accountingMethodExpr), methodName, streamRequestIterator, pyAccountingCatchMark(accountingMethodExpr), pyAccountingFinally(accountingMethodExpr))
		}
		return fmt.Sprintf(`    def _handle_%s(self, request_iterator, context):
        """Handles %s (client streaming)."""
%s%s        try:
            resp = self.handler.%s(%s, context)
%s
            return resp
        except Exception as e:
%s
            self._abort_handler_error(context, e)
%s

`, methodName, method.Name, streamOpenAuthBlock, pyAccountingPreamble(accountingMethodExpr), methodName, streamRequestIterator, clientStreamResponseValidation, pyAccountingCatchMark(accountingMethodExpr), pyAccountingFinally(accountingMethodExpr))
	}

	if method.IsPageCollection() {
		// PAGE: unary on the wire. The wrapper owns the page_token codec and the
		// trust-tier page_size bound; the handler is a keyset producer returning a
		// Page. A malformed page_token raises BadPageToken -> INVALID_ARGUMENT via
		// _abort_handler_error (never a silent reset to page 1).
		itemsField := method.Resolved.PageItemsField
		pageInputValidation := ""
		if validateInput != "" && !emptyInput {
			pageInputValidation = fmt.Sprintf(`
        try:
            %s(request)
        except Exception as e:
            self._abort_request_validation(context, e)`, validateInput)
		}
		handlerCall := fmt.Sprintf("self.handler.%s(request, PageParams(limit=limit, after=after), context)", methodName)
		if emptyInput {
			handlerCall = fmt.Sprintf("self.handler.%s(PageParams(limit=limit, after=after), context)", methodName)
		}
		totalBlock := ""
		if method.Resolved.PageHasTotal {
			// total_count is reported only when the handler computes it (>= 0); a
			// negative total leaves the field at 0 = "unknown", not "empty".
			totalBlock = `
            if page.total >= 0:
                resp.total_count = page.total`
		}
		pageOutputValidation := ""
		if validateOutput != "" {
			pageOutputValidation = fmt.Sprintf(`
            try:
                %s(resp)
            except Exception as e:
                self._abort_response_validation(context, e)`, validateOutput)
		}
		return fmt.Sprintf(`    def _handle_%s(self, request, context):
        """Handles %s (PAGE collection - keyset pagination)."""
%s%s
%s        try:
            after = decode_page_token(getattr(request, "page_token", ""))
            limit = effective_page_size(getattr(request, "page_size", 0), %d, %d)
            page = %s
            resp = %s()
            resp.%s.extend(page.items)
            resp.next_page_token = encode_page_token(page.next_after)%s%s
            return resp
        except Exception as e:
%s            self._abort_handler_error(context, e)
%s

`, methodName, method.Name, authWithReqBlock, pageInputValidation, pyAccountingPreamble(accountingMethodExpr),
			method.Resolved.DefaultPageSize, method.Resolved.MaxPageSize, handlerCall,
			outputType, itemsField, totalBlock, pageOutputValidation,
			pyAccountingCatchMark(accountingMethodExpr), pyAccountingFinally(accountingMethodExpr))
	}

	// Unary method
	var inputValidationBlock, outputValidationBlock string
	if validateInput != "" && !emptyInput {
		inputValidationBlock = fmt.Sprintf(`
        try:
            %s(request)
        except Exception as e:
            self._abort_request_validation(context, e)`, validateInput)
	}
	if validateOutput != "" && !emptyOutput {
		outputValidationBlock = fmt.Sprintf(`
        try:
            %s(resp)
        except Exception as e:
            self._abort_response_validation(context, e)`, validateOutput)
	}

	handlerCall := fmt.Sprintf("self.handler.%s(request)", methodName)
	if emptyInput {
		handlerCall = fmt.Sprintf("self.handler.%s()", methodName)
	}

	if emptyOutput {
		return fmt.Sprintf(`    def _handle_%s(self, request, context):
        """Handles %s with authorization and validation."""
%s%s
%s        trace_id = ""
        try:
            trace_id = self._trace_id_from_request(request)
            is_noop = bool(trace_id and self._noop_from_request(request))
            if trace_id:
                self._publish_trace(trace_id, trace_pb.TRACE_EVENT_KIND_INTERNAL_REQUEST%s)
                if is_noop:
                    return empty_pb2.Empty()
            %s
            if trace_id:
                self._publish_trace(trace_id, trace_pb.TRACE_EVENT_KIND_INTERNAL_RESPONSE%s)
            return empty_pb2.Empty()
        except Exception as e:
%s            if trace_id:
                self._publish_trace(trace_id, trace_pb.TRACE_EVENT_KIND_INTERNAL_RESPONSE, str(e)%s)
            self._abort_handler_error(context, e)
%s

`, methodName, method.Name, authWithReqBlock, inputValidationBlock, pyAccountingPreamble(accountingMethodExpr), traceMethodArg, handlerCall, traceMethodArg, pyAccountingCatchMark(accountingMethodExpr), traceMethodArg, pyAccountingFinally(accountingMethodExpr))
	}

	return fmt.Sprintf(`    def _handle_%s(self, request, context):
        """Handles %s with authorization and validation."""
%s%s
%s        trace_id = ""
        try:
            trace_id = self._trace_id_from_request(request)
            is_noop = bool(trace_id and self._noop_from_request(request))
            if trace_id:
                self._publish_trace(trace_id, trace_pb.TRACE_EVENT_KIND_INTERNAL_REQUEST%s)
                if is_noop:
                    return %s()
            resp = %s
%s
            if trace_id:
                self._publish_trace(trace_id, trace_pb.TRACE_EVENT_KIND_INTERNAL_RESPONSE%s)
            return resp
        except Exception as e:
%s            if trace_id:
                self._publish_trace(trace_id, trace_pb.TRACE_EVENT_KIND_INTERNAL_RESPONSE, str(e)%s)
            self._abort_handler_error(context, e)
%s

`, methodName, method.Name, authWithReqBlock, inputValidationBlock, pyAccountingPreamble(accountingMethodExpr), traceMethodArg, outputType, handlerCall, pyIndentBlock(outputValidationBlock, 4), traceMethodArg, pyAccountingCatchMark(accountingMethodExpr), traceMethodArg, pyAccountingFinally(accountingMethodExpr))
}

// generatePyServiceRegistration generates the register_services method body.
func generatePyServiceRegistration(svc *parser.ServiceInfo, servicePackage string) string {
	var b strings.Builder

	b.WriteString("        method_handlers = {\n")
	for _, method := range svc.Methods {
		inputType := pyWrapperTypeRef(method.FullInputType, method.InputType, servicePackage)
		outputType := pyWrapperTypeRef(method.FullOutputType, method.OutputType, servicePackage)
		methodName := pyGeneratedSnakeName(method.Name)
		fullPath := fmt.Sprintf("/%s.%s/%s", servicePackage, svc.Name, method.Name)

		switch {
		case method.ClientStreaming && method.ServerStreaming:
			fmt.Fprintf(&b, "            '%s': grpc.stream_stream_rpc_method_handler(\n", fullPath)
			fmt.Fprintf(&b, "                self._handle_%s,\n", methodName)
			fmt.Fprintf(&b, "                request_deserializer=%s.FromString,\n", inputType)
			fmt.Fprintf(&b, "                response_serializer=%s.SerializeToString,\n", outputType)
			b.WriteString("            ),\n")
		case method.ServerStreaming:
			fmt.Fprintf(&b, "            '%s': grpc.unary_stream_rpc_method_handler(\n", fullPath)
			fmt.Fprintf(&b, "                self._handle_%s,\n", methodName)
			fmt.Fprintf(&b, "                request_deserializer=%s.FromString,\n", inputType)
			fmt.Fprintf(&b, "                response_serializer=%s.SerializeToString,\n", outputType)
			b.WriteString("            ),\n")
		case method.ClientStreaming:
			fmt.Fprintf(&b, "            '%s': grpc.stream_unary_rpc_method_handler(\n", fullPath)
			fmt.Fprintf(&b, "                self._handle_%s,\n", methodName)
			fmt.Fprintf(&b, "                request_deserializer=%s.FromString,\n", inputType)
			fmt.Fprintf(&b, "                response_serializer=%s.SerializeToString,\n", outputType)
			b.WriteString("            ),\n")
		default:
			fmt.Fprintf(&b, "            '%s': grpc.unary_unary_rpc_method_handler(\n", fullPath)
			fmt.Fprintf(&b, "                self._handle_%s,\n", methodName)
			fmt.Fprintf(&b, "                request_deserializer=%s.FromString,\n", inputType)
			fmt.Fprintf(&b, "                response_serializer=%s.SerializeToString,\n", outputType)
			b.WriteString("            ),\n")
		}
	}
	b.WriteString("        }\n")

	// Generate the generic handler class and register it
	serviceName := fmt.Sprintf("%s.%s", servicePackage, svc.Name)
	fmt.Fprintf(&b, `
        class _Handler(grpc.GenericRpcHandler):
            def __init__(self, handlers):
                self._handlers = handlers

            def service(self, handler_call_details):
                return self._handlers.get(handler_call_details.method)

            def service_name(self):
                return '%s'

        server.add_generic_rpc_handlers([_Handler(method_handlers)])
`, serviceName)

	return b.String()
}

// generateAggregationFiles creates group-level and root-level aggregation files
// for gRPC servers, HTTP clients, and state clients to match Go's layout.
func generateAggregationFiles(config *tactype.Config) error {
	smDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "internal")

	categories := []struct {
		dir  string
		name string
	}{
		{"grpc/servers", "grpc_servers"},
		{"state", "state"},
	}

	for _, cat := range categories {
		catDir := filepath.Join(smDir, cat.dir)
		if _, err := os.Stat(catDir); os.IsNotExist(err) {
			continue
		}

		entries, err := os.ReadDir(catDir)
		if err != nil {
			continue
		}

		var groups []string
		for _, e := range entries {
			if e.IsDir() {
				groups = append(groups, e.Name())
			}
		}

		for _, group := range groups {
			groupDir := filepath.Join(catDir, group)
			if err := generatePyGroupAggregator(groupDir, cat.name); err != nil {
				return fmt.Errorf("failed to generate group %s/%s: %w", cat.dir, group, err)
			}
		}

		if err := generatePyRootAggregator(catDir, cat.name, groups); err != nil {
			return fmt.Errorf("failed to generate root %s: %w", cat.dir, err)
		}
	}

	return nil
}

// generatePyGroupAggregator creates a group-level aggregation file (e.g., sm/internal/grpc_clients/services/http_clients.py).
func generatePyGroupAggregator(groupDir, fileName string) error {
	entries, err := os.ReadDir(groupDir)
	if err != nil {
		return nil
	}

	var imports []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		subDir := filepath.Join(groupDir, e.Name())
		subEntries, err := os.ReadDir(subDir)
		if err != nil {
			continue
		}
		for _, se := range subEntries {
			if se.IsDir() || !strings.HasSuffix(se.Name(), ".py") {
				continue
			}
			if se.Name() == "__init__.py" || se.Name() == fileName+".py" {
				continue
			}
			baseName := strings.TrimSuffix(se.Name(), ".py")
			imports = append(imports, fmt.Sprintf("from .%s.%s import *  # noqa: F401,F403", e.Name(), baseName))
		}
	}

	if len(imports) == 0 {
		return nil
	}

	content := "# Auto-generated aggregator\n" + strings.Join(imports, "\n") + "\n"
	outputPath := filepath.Join(groupDir, fileName+".py")
	if err := os.WriteFile(outputPath, []byte(content), 0644); err != nil {
		return err
	}

	// Ensure __init__.py exists
	initPath := filepath.Join(groupDir, "__init__.py")
	if _, err := os.Stat(initPath); os.IsNotExist(err) {
		os.WriteFile(initPath, []byte("# Auto-generated\n"), 0644)
	}

	return nil
}

// generatePyRootAggregator creates a root-level aggregation file (e.g., sm/internal/grpc_clients/http_clients.py).
func generatePyRootAggregator(catDir, fileName string, groups []string) error {
	var imports []string
	for _, group := range groups {
		groupAgg := filepath.Join(catDir, group, fileName+".py")
		if _, err := os.Stat(groupAgg); err == nil {
			imports = append(imports, fmt.Sprintf("from .%s import %s as %s_%s  # noqa: F401", group, fileName, group, fileName))
		}
	}

	if len(imports) == 0 {
		return nil
	}

	content := "# Auto-generated root aggregator\n" + strings.Join(imports, "\n") + "\n"
	outputPath := filepath.Join(catDir, fileName+".py")
	if err := os.WriteFile(outputPath, []byte(content), 0644); err != nil {
		return err
	}

	// Ensure __init__.py exists
	initPath := filepath.Join(catDir, "__init__.py")
	if _, err := os.Stat(initPath); os.IsNotExist(err) {
		os.WriteFile(initPath, []byte("# Auto-generated\n"), 0644)
	}

	return nil
}

// pyQualifiedMethodAuthKey builds the SERVICE-QUALIFIED method identifier that
// keys the authorizer's per-method policy maps: "pkg.Service/Method", the last
// two segments of the full gRPC method path the wrapper passes to authorize().
// Must stay in lockstep with _qualified_method_key in the grpcserver chassis
// (pkg/templates/python/chassis/grpcserver/authorizer.py.tmpl).
func pyQualifiedMethodAuthKey(protoPackage, serviceName, methodName string) string {
	if protoPackage == "" {
		return serviceName + "/" + methodName
	}
	return protoPackage + "." + serviceName + "/" + methodName
}
