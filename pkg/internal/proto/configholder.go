// Copyright (c) 2026 Second Star Cavalier LLC

package proto

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/eventbus"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// GrpcConfigEntry represents a single grpc_config annotation discovered
// from scanning proto messages.
type GrpcConfigEntry struct {
	ServiceName     string // e.g. "identity"
	ConfigType      string // fully qualified proto type, e.g. "identity.Config"
	FieldID         int32  // stable field number (from annotation)
	ProtoImportPath string // import path, e.g. "services/identity.proto"
}

// ServiceIdentityEntry represents a file-level service declaration.
type ServiceIdentityEntry struct {
	ServiceName string
	FieldID     int32
	ProtoPath   string
}

const (
	configSliceScopeTenant  = "CONFIG_SLICE_SCOPE_TENANT"
	configSliceScopeProject = "CONFIG_SLICE_SCOPE_PROJECT"
)

// ConfigStateRootName derives the per-service state root message name from
// the grpc_config service_name annotation. Examples:
//
//	"identity"  → "IdentityConfigState"
//	"gateway"   → "GatewayConfigState"
//	"mcp"       → "McpConfigState"
func ConfigStateRootName(serviceName string) string {
	normalized := strings.ReplaceAll(serviceName, "-", "_")
	return strutil.ToPascalCase(normalized) + "ConfigState"
}

// StateServiceName returns the gRPC service name for a state root.
// If the state root already ends with "State" (e.g. "BaseballState"),
// the result is "BaseballStateService" (not "BaseballStateStateService").
func StateServiceName(stateRootName string) string {
	if strings.HasSuffix(stateRootName, "State") {
		return stateRootName + "Service"
	}
	return stateRootName + "StateService"
}

// ServiceRPCEntry represents a discovered service with its RPCs.
type ServiceRPCEntry struct {
	GrpcName    string   // e.g. "identity" (from grpc_config service_name or package)
	EnumName    string   // PascalCase name used for RPC enum (e.g. "Sentinel", "Github")
	ProtoName   string   // e.g. "Dashboard" (from service block)
	RPCs        []string // e.g. ["GetClusterInfo", "GetClusterHealth", ...]
	GrantTokens []string // exact compressed entitlement grant tokens for this service's RPCs
	FieldID     int32    // from matching GrpcConfigEntry
}

// Regex patterns for lightweight grpc_config discovery.
// These scan raw proto text without needing protoc.
var (
	packageRe = regexp.MustCompile(`(?m)^\s*package\s+(\S+)\s*;`)
	serviceRe = regexp.MustCompile(`(?ms)^\s*option\s+\(spec\.service\)\s*=\s*\{([^}]*)\}`)
	// Matches any same-line prefix with no slash (inline `{ option` matches, `// option` does not) so a
	// COMMENTED-OUT annotation — `// option (spec.grpc_config) = {…}` — is not
	// false-matched as active. Without the anchor a developer who comments the
	// annotation out still gets a ghost config-holder generated (matches the
	// anchored serviceRe above).
	grpcConfigRe  = regexp.MustCompile(`(?ms)^[^\n/]*?option\s+\(spec\.grpc_config\)\s*=\s*\{([^}]*)\}`)
	nameRe        = regexp.MustCompile(`name:\s*"([^"]+)"`)
	serviceNameRe = regexp.MustCompile(`service_name:\s*"([^"]+)"`)
	idRe          = regexp.MustCompile(`id:\s*(\d+)`)
	// (spec.service).accepted_audiences, in either textproto spelling:
	// a bracketed list `accepted_audiences: ["a", "b"]` or repeated scalar
	// lines `accepted_audiences: "a"`. Group 1 is the raw right-hand side;
	// quotedStringRe pulls the entries out of it.
	acceptedAudsRe  = regexp.MustCompile(`accepted_audiences\s*:\s*(\[[^\]]*\]|"[^"]*")`)
	quotedStringRe  = regexp.MustCompile(`"([^"]*)"`)
	messageNameRe = regexp.MustCompile(`(?m)^\s*message\s+(\w+)\s*\{`)

	// Patterns for RPC scanning. Anchored to line-start (only tabs/spaces before
	// `option`) so a commented-out annotation is not false-matched as active —
	// the same MS-10 fix applied to grpcConfigRe.
	serviceC2sRe   = regexp.MustCompile(`(?m)^[^\n/]*?option\s+\(spec\.service_c2s\)\s*=`)
	serviceC2dRe   = regexp.MustCompile(`(?m)^[^\n/]*?option\s+\(spec\.service_c2d\)\s*=`)
	eventLabelRe   = regexp.MustCompile(`(?m)^[^\n/]*?option\s+\(spec\.event_label\)\s*=`)
	serviceStartRe = regexp.MustCompile(`(?m)^service\s+(\w+)\s*\{`)
	rpcNameRe      = regexp.MustCompile(`(?m)^\s*rpc\s+(\w+)\s*\(`)

	// Patterns for legacy pre-descriptor service scanning.
)

// VServiceEntry represents a vservice with exported topic RPCs.
type VServiceEntry struct {
	Name      string   // entitlement key (e.g. "claudesensor")
	FieldID   int32    // stable field number for TokenEntitlements
	HostLabel string   // physical service tag (e.g. "eventbus")
	RPCs      []string // generated RPC names (e.g. "PublishClaudesensorAgenticCodeEventBusEventStream")
	// Routes carries the per-topic wire coordinates for each entry in RPCs,
	// index-aligned. RPCs holds the flat historical names, which are still the
	// entitlement keys; Routes holds what a caller must actually dial. Anything
	// building a gRPC path MUST use Routes — composing one from HostLabel and an
	// RPCs entry reproduces the flat name the broker is being moved off.
	Routes []VServiceRoute
	// IsTrait records which declaration produced this entry: (spec.trait) rather
	// than (spec.vservice). Both kinds share this struct because they share an
	// identity shape, but they are not the same contract, and the generated
	// enum comment names the kind — calling a trait a vservice there is the one
	// place that mislabel reaches consumers (it lands in registry.json).
	IsTrait bool
}

// VServiceRoute is the per-topic gRPC coordinate for one topic RPC:
// a fully-qualified service ("agentsensor.AgentEventBusStateService") and its
// method ("PublishToEventStream").
type VServiceRoute struct {
	Service string
	Method  string
}

// AcceptedAudiencesForHost lists the token audiences this service accepts: its
// own service label — the identity the generated Authorizer matches `aud`
// against — plus every contract it hosts, whose name is its own audience. A
// trait's audience is the trait name, never the host label, so the host must
// accept it explicitly or it rejects calls the caller authenticated correctly.
//
// declared carries (spec.service).accepted_audiences, the service's explicit
// widening of that set (see ScanAcceptedAudiences). A service that declares
// nothing — every service today — gets exactly the derived set it got before
// the annotation existed, which is what makes this backward compatible.
func AcceptedAudiencesForHost(serviceLabel string, vsEntries []VServiceEntry, declared []string) []string {
	audiences := []string{serviceLabel}
	seen := map[string]bool{serviceLabel: true}
	for _, vs := range vsEntries {
		if vs.HostLabel == serviceLabel && vs.Name != "" && !seen[vs.Name] {
			seen[vs.Name] = true
			audiences = append(audiences, vs.Name)
		}
	}
	for _, aud := range declared {
		if aud != "" && !seen[aud] {
			seen[aud] = true
			audiences = append(audiences, aud)
		}
	}
	return audiences
}

// ScanAcceptedAudiences reads (spec.service).accepted_audiences off the proto
// that declares serviceLabel, under sourceProtoDir. It returns nil when the
// service declares none, which is the default and the pre-annotation behavior.
//
// This deliberately does NOT check the declared audiences against the
// entitlement catalog. Two reasons, both settled: the annotation does one job —
// widen the accepted `aud` set — and an audience naming no hosted surface is
// inert rather than dangerous, since entitlement checks still run per RPC. A
// catalog cross-check here would be a LINT for typos, never a security gate,
// and the declaring service owns getting its own declaration right.
func ScanAcceptedAudiences(sourceProtoDir, serviceLabel string) ([]string, error) {
	var declared []string
	err := filepath.Walk(sourceProtoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(content)
		svcName, ok, err := parseServiceAnnotation(text)
		if err != nil || !ok || svcName != serviceLabel {
			// A parse error here is not this function's to report:
			// ScanAndGenerateConfigHolder scans the same files and fails on it.
			return nil
		}
		match := serviceRe.FindStringSubmatch(text)
		if match == nil {
			return nil
		}
		audsMatch := acceptedAudsRe.FindStringSubmatch(match[1])
		if audsMatch == nil {
			return nil
		}
		for _, q := range quotedStringRe.FindAllStringSubmatch(audsMatch[1], -1) {
			declared = append(declared, q[1])
		}
		return filepath.SkipAll
	})
	if err != nil {
		return nil, err
	}
	return declared, nil
}

// ScanAndGenerateConfigHolder scans proto files for file-level (spec.service)
// declarations plus grpc_config annotations using regex (no protoc needed)
// and writes four proto files into generated/ in each of the given directories:
//   - generated/services.proto — ServiceName enum + per-service RPC enums
//   - generated/entitlements.proto — TokenEntitlements message
//   - generated/accounting_method.proto — AccountingMethod message
//   - generated/services_config.proto — Services config holder
//
// Must be called BEFORE the descriptor parser runs, since service protos may
// import generated/services_config.proto.
func ScanAndGenerateConfigHolder(sourceProtoDir string, dirs ...string) ([]GrpcConfigEntry, []ServiceRPCEntry, []VServiceEntry, error) {
	if len(dirs) == 0 {
		return nil, nil, nil, nil
	}

	reg, err := LoadServiceRegistry(sourceProtoDir)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("loading registry: %w", err)
	}

	// Scan the first directory for annotations (all dirs have the same content).
	scanDir := dirs[0]
	servicesDir := filepath.Join(scanDir, "services")
	if _, err := os.Stat(servicesDir); os.IsNotExist(err) {
		// No services/ directory — sys-only build. Still write the minimal
		// generated/ stubs so embedded lib protos (accounting, trace) can resolve
		// their imports from generated/.
		if err := writeMinimalGeneratedStubs(dirs); err != nil {
			return nil, nil, nil, err
		}
		return nil, nil, nil, nil
	}

	var entries []GrpcConfigEntry
	serviceMap := make(map[string]ServiceIdentityEntry)
	err = filepath.Walk(servicesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return err
		}

		relPath, _ := filepath.Rel(scanDir, path)
		relPath = filepath.ToSlash(relPath)

		// Skip generated services proto (should not be in services/ dir,
		// but guard against stale files).
		if relPath == "services/config_holder.proto" {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(content)

		if svcName, ok, err := parseServiceAnnotation(text); err != nil {
			return err
		} else if ok {
			fieldID, err := reg.ServiceID(svcName)
			if err != nil {
				return fmt.Errorf("%s: %w", relPath, err)
			}
			serviceMap[svcName] = ServiceIdentityEntry{
				ServiceName: svcName,
				FieldID:     fieldID,
				ProtoPath:   relPath,
			}
		}

		// Find grpc_config annotation.
		serviceName, ok, err := parseGrpcConfigAnnotation(text)
		if err != nil {
			return fmt.Errorf("%s: %w", relPath, err)
		}
		if !ok {
			return nil
		}

		// Find package name.
		pkgMatch := packageRe.FindStringSubmatch(text)
		if pkgMatch == nil {
			return nil
		}
		pkg := pkgMatch[1]

		// Find the message name that has the annotation. Use the comment-safe
		// regex match position (not strings.Index, which would find a commented
		// `// option (spec.grpc_config)` occurring BEFORE the real one and resolve
		// the wrong containing message).
		fcLoc := grpcConfigRe.FindStringIndex(text)
		fcIdx := -1
		if fcLoc != nil {
			fcIdx = fcLoc[0]
		}
		msgName := findContainingMessage(text, fcIdx)
		if msgName == "" {
			msgName = "Config" // fallback
		}

		// service_name is required inline (parseGrpcConfigAnnotation fails loud when
		// absent), so serviceName is always non-empty here. The old file-level
		// fallback was dead code: the parser returned ok=false for a nameless
		// annotation, so the fallback never ran — and per the required-field
		// decision it must not resolve implicitly anyway.
		fieldID, err := reg.ServiceID(serviceName)
		if err != nil {
			return fmt.Errorf("%s: %w", relPath, err)
		}

		entries = append(entries, GrpcConfigEntry{
			ServiceName:     serviceName,
			ConfigType:      pkg + "." + msgName,
			FieldID:         fieldID,
			ProtoImportPath: relPath,
		})
		return nil
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("scanning for grpc_config: %w", err)
	}

	if len(entries) == 0 && len(serviceMap) == 0 {
		// No annotations found — write minimal stubs so protoc can resolve imports.
		if err := writeMinimalGeneratedStubs(dirs); err != nil {
			return nil, nil, nil, err
		}
		return nil, nil, nil, nil
	}

	// services is the union of (spec.service)-declared identities and
	// (spec.grpc_config)-only services (which have a config holder but never
	// declared (spec.service) explicitly), deduped by name. A service with
	// only a grpc_config annotation (e.g. operator) must still get a
	// SERVICE_NAME_* enum value — dropping it silently produced UNSPECIFIED
	// delegate entries for any such service (found via operator, 2026-09-03).
	var services []ServiceIdentityEntry
	for _, svc := range serviceMap {
		services = append(services, svc)
	}
	for _, entry := range entries {
		if _, ok := serviceMap[entry.ServiceName]; ok {
			continue
		}
		services = append(services, ServiceIdentityEntry{
			ServiceName: entry.ServiceName,
			FieldID:     entry.FieldID,
			ProtoPath:   entry.ProtoImportPath,
		})
	}

	// Sort by field ID for deterministic output.
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].FieldID < entries[j].FieldID
	})
	sort.Slice(services, func(i, j int) bool {
		return services[i].FieldID < services[j].FieldID
	})

	// Descriptor-backed scans below may load embedded sys that import generated
	// protos, such as sys/trace.proto -> generated/accounting_method.proto.
	// Write temporary minimal stubs now; the real generated files overwrite them
	// after service/vservice RPC discovery completes.
	if err := writeMinimalGeneratedStubs(dirs); err != nil {
		return nil, nil, nil, err
	}

	// Scan for per-service RPCs and generate service identity schema inside
	// sys/tacplus.proto.
	svcRPCs, err := scanServiceRPCs(servicesDir, entries, reg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("scanning service RPCs: %w", err)
	}

	// Scan vservices/ for exported topics that need entitlement fields.
	vsEntries, err := ScanVServiceRPCs(scanDir, reg)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("scanning vservice RPCs: %w", err)
	}

	// ── generated/services.proto ───────────────────────────────────────
	// ServiceName enum + per-service RPC enums.
	var svcsBuf strings.Builder
	svcsBuf.WriteString("// CODE GENERATED by tacplus — DO NOT EDIT\n")
	svcsBuf.WriteString("// Generated from (spec.service) and (spec.grpc_config) annotations.\n\n")
	svcsBuf.WriteString("syntax = \"proto3\";\n\n")
	svcsBuf.WriteString("package services;\n\n")

	svcsBuf.WriteString("// ServiceName provides type-safe service references for S2S authorization.\n")
	svcsBuf.WriteString("// Enum values use stable service IDs from explicit service declarations.\n")
	svcsBuf.WriteString("enum ServiceName {\n")
	svcsBuf.WriteString("  SERVICE_NAME_UNSPECIFIED = 0;\n")
	for _, e := range services {
		enumName := "SERVICE_NAME_" + strings.ToUpper(strings.ReplaceAll(e.ServiceName, "-", "_"))
		svcsBuf.WriteString(fmt.Sprintf("  %s = %d;\n", enumName, e.FieldID))
	}
	svcsBuf.WriteString("  SERVICE_NAME_ANY = 99; // wildcard: allow any service\n")
	svcsBuf.WriteString("}\n")

	for _, svc := range svcRPCs {
		prefix := strutil.ToUpperSnakeCase(svc.EnumName) + "_RPCS"
		svcsBuf.WriteString(fmt.Sprintf("\nenum %sRPCs {\n", svc.EnumName))
		svcsBuf.WriteString(fmt.Sprintf("  %s_UNSPECIFIED = 0;\n", prefix))
		for i, rpc := range svc.RPCs {
			svcsBuf.WriteString(fmt.Sprintf("  %s_%s = %d;\n", prefix, strutil.ToUpperSnakeCase(rpc), i+1))
		}
		svcsBuf.WriteString(fmt.Sprintf("  %s_ANY = 99; // wildcard: allow any RPC\n", prefix))
		svcsBuf.WriteString("}\n")
	}

	// vservice RPC enums (from exported topics).
	for _, vs := range vsEntries {
		prefix := strutil.ToUpperSnakeCase(strutil.ToPascalCase(vs.Name)) + "_RPCS"
		kind := "vservice"
		if vs.IsTrait {
			kind = "trait"
		}
		svcsBuf.WriteString(fmt.Sprintf("\n// %s (%s, hosted by %s)\n", vs.Name, kind, vs.HostLabel))
		svcsBuf.WriteString(fmt.Sprintf("enum %sRPCs {\n", strutil.ToPascalCase(vs.Name)))
		svcsBuf.WriteString(fmt.Sprintf("  %s_UNSPECIFIED = 0;\n", prefix))
		for i, rpc := range vs.RPCs {
			svcsBuf.WriteString(fmt.Sprintf("  %s_%s = %d;\n", prefix, strutil.ToUpperSnakeCase(rpc), i+1))
		}
		svcsBuf.WriteString(fmt.Sprintf("  %s_ANY = 99; // wildcard: allow any RPC\n", prefix))
		svcsBuf.WriteString("}\n")
	}

	// ── generated/accounting_method.proto ──────────────────────────────
	// AccountingMethod — identifies which RPC an event relates to.
	var amBuf strings.Builder
	amBuf.WriteString("// CODE GENERATED by tacplus — DO NOT EDIT\n\n")
	amBuf.WriteString("syntax = \"proto3\";\n\n")
	amBuf.WriteString("package accounting_method;\n\n")
	if len(svcRPCs) > 0 {
		amBuf.WriteString("import \"generated/services.proto\";\n\n")
	}
	amBuf.WriteString("// AccountingMethod identifies which RPC an event relates to.\n")
	if len(svcRPCs) > 0 {
		amBuf.WriteString("// Only one field will be set — the rest default to 0 (UNSPECIFIED)\n")
		amBuf.WriteString("// and are not encoded by proto3, so there is no overhead.\n")
	}
	amBuf.WriteString("message AccountingMethod {\n")
	for _, svc := range svcRPCs {
		amBuf.WriteString(fmt.Sprintf("  services.%sRPCs %s = %d;\n", svc.EnumName, svc.GrpcName, svc.FieldID))
	}
	amBuf.WriteString("}\n")

	// ── generated/entitlements.proto ───────────────────────────────────
	// TokenEntitlements — compressed C2S entitlements embedded in user tokens.
	var entBuf strings.Builder
	entBuf.WriteString("// CODE GENERATED by tacplus — DO NOT EDIT\n\n")
	entBuf.WriteString("syntax = \"proto3\";\n\n")
	entBuf.WriteString("package entitlements;\n\n")
	entBuf.WriteString("// TokenEntitlements holds compressed C2S entitlements embedded in user tokens.\n")
	entBuf.WriteString("// OPA returns this structure directly; the token authority embeds it in the JWT service_entitlements claim.\n")
	entBuf.WriteString("// grants may contain exact or wildcard grant tokens; generated helpers normalize them before authorization.\n")
	entBuf.WriteString("message TokenEntitlements {\n")
	entBuf.WriteString("  // grants is a flat list of generated grant tokens, e.g. PACKAGE_SERVICE_RPC.\n")
	entBuf.WriteString("  repeated string grants = 1;\n")
	entBuf.WriteString("  // data_grants is a flat list of generated data-entitlement grant tokens, e.g.\n")
	entBuf.WriteString("  // PACKAGE_RECORD_FIELD. A data grant names what the caller may SEE RAW: a MAY_*\n")
	entBuf.WriteString("  // redact field with a matching grant egresses raw; absent a grant the field gets\n")
	entBuf.WriteString("  // its most-protective default (MAY_REDACT -> REDACT, MAY_ANONYMIZE -> ANONYMIZE).\n")
	entBuf.WriteString("  // Stamped by OPA at token-exchange time alongside grants — an identity property,\n")
	entBuf.WriteString("  // never a request parameter.\n")
	entBuf.WriteString("  repeated string data_grants = 2;\n")
	entBuf.WriteString("  // principal_scope is how wide the caller's authority over PRINCIPALS is, as a\n")
	entBuf.WriteString("  // spec.PrincipalScope enum VALUE NAME (e.g. \"PRINCIPAL_SCOPE_ANY\"). It is the\n")
	entBuf.WriteString("  // claim the generated (spec.owner_default) guard reads to tell an owner acting\n")
	entBuf.WriteString("  // on its own row from a principal whose authority is wider than itself.\n")
	entBuf.WriteString("  // Absent, empty, PRINCIPAL_SCOPE_UNSPECIFIED and PRINCIPAL_SCOPE_SELF_ONLY all\n")
	entBuf.WriteString("  // mean self-scoped: the guard fails closed, so a policy that stamps nothing\n")
	entBuf.WriteString("  // leaves every caller exactly as narrow as it is today.\n")
	entBuf.WriteString("  // Stamped by OPA at token-exchange time alongside grants — an identity\n")
	entBuf.WriteString("  // property, never a request parameter.\n")
	entBuf.WriteString("  string principal_scope = 3;\n")
	entBuf.WriteString("  // azp restricts these entitlements to tokens with a matching authorized party.\n")
	entBuf.WriteString("  // Empty = entitlements apply regardless of azp.\n")
	entBuf.WriteString("  repeated string azp = 100;\n")
	entBuf.WriteString("}\n")

	// ── generated/services_config.proto ────────────────────────────────
	// Services config holder — aggregates per-service Config messages.
	var cfgBuf strings.Builder
	cfgBuf.WriteString("// CODE GENERATED by tacplus — DO NOT EDIT\n")
	cfgBuf.WriteString("// Generated from (spec.grpc_config) annotations on service Config messages.\n\n")
	cfgBuf.WriteString("syntax = \"proto3\";\n\n")
	cfgBuf.WriteString("package services_config;\n\n")

	for _, e := range entries {
		cfgBuf.WriteString(fmt.Sprintf("import \"%s\";\n", e.ProtoImportPath))
	}

	cfgBuf.WriteString("\nmessage Services {\n")
	for _, e := range entries {
		// Derive field name from the service name (from spec.grpc_config service_name),
		// so that protobuf Java accessors match the backend codegen.
		// e.g. service_name "saml" → "saml_config" (even if proto package is "samlsvc").
		fieldName := strings.ReplaceAll(e.ServiceName, "-", "_") + "_config"
		cfgBuf.WriteString(fmt.Sprintf("  %s %s = %d;\n", e.ConfigType, fieldName, e.FieldID))
	}
	cfgBuf.WriteString("}\n")

	// ── Write all 4 files to each output directory ─────────────────────
	files := map[string]string{
		"services.proto":          svcsBuf.String(),
		"accounting_method.proto": amBuf.String(),
		"entitlements.proto":      entBuf.String(),
		"services_config.proto":   cfgBuf.String(),
	}

	const tenantConfigStub = "// CODE GENERATED by tacplus — DO NOT EDIT\n\n" +
		"syntax = \"proto3\";\n\npackage tenant_config;\n\n" +
		"// Stub — will be replaced by GenerateTenantConfig.\n" +
		"message TenantConfig {}\n"

	const projectConfigStub = "// CODE GENERATED by tacplus — DO NOT EDIT\n\n" +
		"syntax = \"proto3\";\n\npackage project_config;\n\n" +
		"// Stub — will be replaced by GenerateProjectConfig.\n" +
		"message ProjectConfig {}\n"

	for _, dir := range dirs {
		// Clean up stale files from previous layout.
		_ = os.Remove(filepath.Join(dir, "sys", "servicename.proto"))
		_ = os.Remove(filepath.Join(dir, "sys", "generated.proto"))
		_ = os.Remove(filepath.Join(dir, "common", "generated_services.proto"))

		genDir := filepath.Join(dir, "generated")
		if err := os.MkdirAll(genDir, 0755); err != nil {
			return nil, nil, nil, fmt.Errorf("failed to create generated dir: %w", err)
		}
		for name, content := range files {
			if err := os.WriteFile(filepath.Join(genDir, name), []byte(content), 0644); err != nil {
				return nil, nil, nil, fmt.Errorf("failed to write generated/%s: %w", name, err)
			}
		}
		// Write a stub tenant_config.proto if it doesn't exist yet.
		// GenerateTenantConfig (called by Go generator) will overwrite with real content.
		// Other generators (Java, Python, etc.) need the stub so protoc can resolve the
		// import in services/config.proto before the real tenant_config is generated.
		stubPath := filepath.Join(genDir, "tenant_config.proto")
		if _, err := os.Stat(stubPath); os.IsNotExist(err) {
			if err := os.WriteFile(stubPath, []byte(tenantConfigStub), 0644); err != nil {
				return nil, nil, nil, fmt.Errorf("failed to write generated/tenant_config.proto stub: %w", err)
			}
		}
		projectStubPath := filepath.Join(genDir, "project_config.proto")
		if _, err := os.Stat(projectStubPath); os.IsNotExist(err) {
			if err := os.WriteFile(projectStubPath, []byte(projectConfigStub), 0644); err != nil {
				return nil, nil, nil, fmt.Errorf("failed to write generated/project_config.proto stub: %w", err)
			}
		}
	}

	if err := reg.Save(); err != nil {
		return nil, nil, nil, fmt.Errorf("saving registry.yaml: %w", err)
	}

	zap.L().Info("Generated generated/*.proto",
		zap.Int("services", len(services)),
		zap.Int("configs", len(entries)),
		zap.Int("rpc_enums", len(svcRPCs)))
	return entries, svcRPCs, vsEntries, nil
}

// findContainingMessage finds the message name that contains the text at position idx.
func findContainingMessage(text string, idx int) string {
	if idx < 0 || idx > len(text) {
		return ""
	}
	before := text[:idx]
	matches := messageNameRe.FindAllStringSubmatch(before, -1)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1][1]
}

func parseServiceAnnotation(text string) (string, bool, error) {
	match := serviceRe.FindStringSubmatch(text)
	if match == nil {
		return "", false, nil
	}
	if idRe.MatchString(match[1]) {
		return "", false, fmt.Errorf("(spec.service) annotation still contains 'id:' — remove it; IDs are now assigned by registry.yaml")
	}
	nameMatch := nameRe.FindStringSubmatch(match[1])
	if nameMatch == nil {
		// No explicit name: — the service name defaults to the proto package,
		// matching DescriptorParser.parseServiceIdentity (name == "" -> pkg).
		// Without this fallback every service declaring `option (spec.service)
		// = {}` (nearly all of them) was silently dropped from the generated
		// services.ServiceName enum, so S2S authorization resolved its name to
		// SERVICE_NAME_UNSPECIFIED and denied every token exchange for it.
		pkgMatch := packageRe.FindStringSubmatch(text)
		if pkgMatch == nil {
			return "", false, nil
		}
		return pkgMatch[1], true, nil
	}
	return nameMatch[1], true, nil
}

func parseGrpcConfigAnnotation(text string) (string, bool, error) {
	match := grpcConfigRe.FindStringSubmatch(text)
	if match == nil {
		return "", false, nil
	}
	if idRe.MatchString(match[1]) {
		return "", false, fmt.Errorf("(spec.grpc_config) annotation still contains 'id:' — remove it; IDs are now assigned by registry.yaml")
	}
	var serviceName string
	if serviceNameMatch := serviceNameRe.FindStringSubmatch(match[1]); serviceNameMatch != nil {
		serviceName = serviceNameMatch[1]
	}
	if serviceName == "" {
		// service_name is REQUIRED. A (spec.grpc_config) without it was previously
		// treated as absent — the user's declared config holder silently never
		// registered, and no ConfigState was injected. Fail loud, never assume.
		return "", false, fmt.Errorf("(spec.grpc_config) annotation is missing its required service_name field")
	}
	return serviceName, true, nil
}

// serviceBlock represents a parsed proto service block.
type serviceBlock struct {
	name string // e.g. "IdentityService"
	body string // content between { and }
}

// findServiceBlocks finds all `service X { ... }` blocks using brace matching.
func findServiceBlocks(text string) []serviceBlock {
	matches := serviceStartRe.FindAllStringSubmatchIndex(text, -1)
	var blocks []serviceBlock
	for _, m := range matches {
		name := text[m[2]:m[3]]
		braceStart := m[0] + strings.Index(text[m[0]:], "{")
		depth := 0
		end := -1
		for i := braceStart; i < len(text); i++ {
			if text[i] == '{' {
				depth++
			} else if text[i] == '}' {
				depth--
				if depth == 0 {
					end = i
					break
				}
			}
		}
		if end > 0 {
			blocks = append(blocks, serviceBlock{
				name: name,
				body: text[braceStart+1 : end],
			})
		}
	}
	return blocks
}

// scanServiceRPCs walks service protos, finds `service X { ... }` blocks with
// service_c2s annotations, extracts RPC names,
// and resolves stable field IDs. Config-holder services key off their
// grpc_config service_name; services without a grpc_config fall back to the proto
// package name and are accepted only if that name is registered in registry.yaml.
// The latter never enter services_config.proto, so they get a TokenEntitlements
// field + RPC enum without creating a config-holder import cycle.
func scanServiceRPCs(servicesDir string, entries []GrpcConfigEntry, reg *ServiceRegistry) ([]ServiceRPCEntry, error) {
	entryMap := make(map[string]GrpcConfigEntry)
	for _, e := range entries {
		entryMap[e.ServiceName] = e
	}

	// Accumulate RPCs per grpcName across service blocks. Multiple C2S
	// services in the same file (e.g. Token + TokenAdmin in fleetwood.proto)
	// share one service key and
	// their RPCs are merged.
	type rpcAccum struct {
		fieldID     int32
		protoName   string // first proto service name seen (for ProtoName field)
		rpcs        []string
		grantTokens []string
	}
	accum := make(map[string]*rpcAccum)

	err := filepath.Walk(servicesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return err
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(content)

		// Extract grpc_config service_name from this file as fallback
		// when the service annotation has no name: field.
		var fileServiceName string
		if gcMatch := grpcConfigRe.FindString(text); gcMatch != "" {
			if snMatch := serviceNameRe.FindStringSubmatch(gcMatch); snMatch != nil {
				fileServiceName = snMatch[1]
			}
		}

		// Package name — used as the service key for C2S services that have
		// no grpc_config (non-config-holders).
		var pkgName string
		if pkgMatch := packageRe.FindStringSubmatch(text); pkgMatch != nil {
			pkgName = pkgMatch[1]
		}

		blocks := findServiceBlocks(text)

		for _, block := range blocks {
			// Skip injected event state services (e.g. AgenticCodeEventBusStateService).
			if eventLabelRe.MatchString(block.body) {
				continue
			}

			var grpcName string
			if serviceC2sRe.MatchString(block.body) {
				// C2S service. Prefer the file's grpc_config service_name; if
				// the file has no grpc_config, fall back to the package name.
				// S2S services are excluded: they use service identity auth,
				// not user entitlements, so their RPCs don't belong in the
				// entitlement profile or accounting enums.
				if fileServiceName != "" {
					grpcName = fileServiceName
				} else {
					grpcName = pkgName
				}
			} else {
				continue
			}
			if grpcName == "" {
				continue
			}

			// Resolve a stable field ID. Config-holder services come from the
			// scanned grpc_config entries; non-config-holders resolve from the
			// registry (without being auto-registered), so an unknown package
			// is simply skipped rather than silently grabbing an ID.
			var fieldID int32
			if entry, ok := entryMap[grpcName]; ok {
				fieldID = entry.FieldID
			} else if id, ok := reg.ServiceIDIfPresent(grpcName); ok {
				fieldID = id
			} else {
				continue
			}

			var rpcs []string
			var grantTokens []string
			for _, m := range rpcNameRe.FindAllStringSubmatch(block.body, -1) {
				rpc := m[1]
				rpcs = append(rpcs, rpc)
				grantTokens = append(grantTokens, exactGrantToken(pkgName, block.name, rpc))
			}

			if a, ok := accum[grpcName]; ok {
				a.rpcs = append(a.rpcs, rpcs...)
				a.grantTokens = append(a.grantTokens, grantTokens...)
			} else {
				accum[grpcName] = &rpcAccum{
					fieldID:     fieldID,
					protoName:   block.name,
					rpcs:        rpcs,
					grantTokens: grantTokens,
				}
			}
		}

		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("scanning for service RPCs: %w", err)
	}

	var result []ServiceRPCEntry
	for grpcName, a := range accum {
		result = append(result, ServiceRPCEntry{
			GrpcName:    grpcName,
			EnumName:    strutil.ToPascalCase(grpcName),
			ProtoName:   a.protoName,
			RPCs:        a.rpcs,
			GrantTokens: a.grantTokens,
			FieldID:     a.fieldID,
		})
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].FieldID < result[j].FieldID
	})

	return result, nil
}

func exactGrantToken(pkg, service, rpc string) string {
	return grantTokenPart(pkg) + "_" + grantTokenPart(service) + "_" + grantTokenPart(rpc)
}

func grantTokenPart(s string) string {
	normalized := strings.NewReplacer("-", "_", ".", "_").Replace(s)
	return strings.ToUpper(strutil.ToSnakeCase(normalized))
}

// pascalToUpperSnake converts PascalCase to UPPER_SNAKE_CASE.
// e.g. "GetClusterInfo" → "GET_CLUSTER_INFO"

// ScanVServiceRPCs scans vservices/ for spec.vservice annotations and exported
// topics using a scoped descriptor parse. For each exported topic, it generates
// Publish and Subscribe RPC names. Field IDs are looked up (or auto-assigned)
// via the unified registry, falling back to the descriptor annotation id when
// callers intentionally pass an empty, read-only registry.
func ScanVServiceRPCs(scanDir string, reg *ServiceRegistry) ([]VServiceEntry, error) {
	vservicesDir := filepath.Join(scanDir, "vservices")
	if _, err := os.Stat(vservicesDir); os.IsNotExist(err) {
		return nil, nil
	}

	var protoFiles []string
	if err := filepath.Walk(vservicesDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return err
		}
		relPath, err := filepath.Rel(scanDir, path)
		if err != nil {
			return err
		}
		protoFiles = append(protoFiles, filepath.ToSlash(relPath))
		return nil
	}); err != nil {
		return nil, fmt.Errorf("finding vservice protos: %w", err)
	}
	if len(protoFiles) == 0 {
		return nil, nil
	}
	sort.Strings(protoFiles)

	dp, err := parser.NewDescriptorParserForFiles(scanDir, protoFiles)
	if err != nil {
		return nil, fmt.Errorf("parsing vservice descriptors: %w", err)
	}

	var result []VServiceEntry
	for _, path := range protoFiles {
		fd, err := dp.GetFileDescriptor(path)
		if err != nil {
			return nil, fmt.Errorf("loading vservice descriptor %s: %w", path, err)
		}
		if fd.VServiceName == "" {
			continue
		}
		vsID, err := reg.VServiceFieldID(fd.VServiceName)
		if err != nil {
			if fd.VServiceID == 0 {
				return nil, fmt.Errorf("%s: vservice %q has no registry entry and no descriptor id fallback: %w", path, fd.VServiceName, err)
			}
			vsID = fd.VServiceID
		}

		var rpcs []string
		var routes []VServiceRoute
		var hostLabel string
		for _, msg := range fd.Messages {
			labelAnn, ok := msg.Annotations["service_event_label"]
			if !ok || labelAnn.StringValue == "" {
				continue
			}
			exportAnn, ok := msg.Annotations["service_state_export"]
			if !ok || !exportAnn.BoolValue {
				continue
			}
			if hostLabel == "" {
				hostLabel = labelAnn.StringValue
			} else if hostLabel != labelAnn.StringValue {
				return nil, fmt.Errorf("%s: vservice %q exports topics on multiple broker labels (%q and %q)", path, fd.VServiceName, hostLabel, labelAnn.StringValue)
			}
			for _, field := range msg.Fields {
				topicAnn, ok := field.Annotations["topic"]
				if !ok || !topicAnn.BoolValue {
					continue
				}
				if fd.Package == "" {
					return nil, fmt.Errorf("%s: exported EventBus topic %s.%s must declare a proto package", path, msg.Name, field.Name)
				}
				rpcSuffix := eventbus.RPCStemForTopicParts(fd.Package, msg.Name, field.Name)
				rpcs = append(rpcs, "Publish"+rpcSuffix)
				rpcs = append(rpcs, "Subscribe"+rpcSuffix)

				// Per-topic coordinates, index-aligned with rpcs above. Built
				// from the same three components the flat stem uses, through the
				// shared eventbus helpers so this cannot drift from what
				// publishers and subscribers emit.
				ref := eventbus.TopicRef{Package: fd.Package, Message: msg.Name, Field: field.Name}
				svcFQN := eventbus.PerTopicServiceFullName(ref)
				routes = append(routes,
					VServiceRoute{Service: svcFQN, Method: eventbus.PerTopicPublishRPC(ref)},
					VServiceRoute{Service: svcFQN, Method: eventbus.PerTopicSubscribeRPC(ref)},
				)
			}
		}

		// A contract whose RPCs do not route through topics still needs an
		// entry. Exported topics are only ONE way a contract names its host;
		// a trait names it directly, and its RPCs are plain request/response
		// calls. Keying the entry on topics alone left such a contract with no
		// entry at all, so its name never reached the host's accepted-audience
		// set (java/types/generator.go builds that set from HostLabel/Name) and
		// the host rejected calls the caller had authenticated correctly.
		//
		// RPCs/Routes stay topic-only: every consumer of those two fields reads
		// them as topic coordinates (the gateway's REST table dispatches them as
		// Publish/Subscribe), and a trait's plain RPCs already get their routes
		// through the ordinary service path. What changes here is whether the
		// entry EXISTS, not what a topic entry contains.
		if hostLabel == "" {
			hostLabel = nonTopicHostLabel(fd)
		}
		if len(rpcs) > 0 || hostLabel != "" {
			result = append(result, VServiceEntry{
				Name:      fd.VServiceName,
				FieldID:   int32(vsID),
				HostLabel: hostLabel,
				RPCs:      rpcs,
				Routes:    routes,
				IsTrait:   fd.Trait != nil,
			})
		}
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].FieldID < result[j].FieldID
	})
	return result, nil
}

// nonTopicHostLabel resolves the physical service hosting a contract that
// exports no topics, from the two places such a contract names its host: the
// (spec.service_s2s)/(spec.service_c2s) label on a surface it declares itself,
// and the (spec.trait).host on the declaration. Empty means the file names no
// host and asks for nothing — an identity-only declaration gets no entry.
//
// The trait fallback is not redundant with the surface label: a trait's plane is
// APPENDED to its proto later in the pipeline (AppendRecordMapServiceToProtos),
// after this scan has already read the tree, so at scan time such a trait
// declares its host and no services at all.
func nonTopicHostLabel(fd *parser.FileDescriptor) string {
	for _, svc := range fd.Services {
		if svc.HostLabel != "" && len(svc.Methods) > 0 {
			return svc.HostLabel
		}
	}
	if fd.Trait != nil {
		return fd.Trait.Host
	}
	return ""
}

// resolvePrimaryStore returns the package of THE canonical backing store — the
// one service that hosts config state and physically holds every distributed
// state partition in the build. It returns "" (no error) when the proto set has
// no backing store at all; callers that require one say so themselves.
//
// option (spec.service) = {store: true} says a service CAN back state;
// option (spec.service) = {config_store: true} says THAT service is the
// canonical one. The two are deliberately separate: previously this picked the
// alphabetically first store: true package, which silently routed every
// service's config through eventbus (< kernel) because eventbus is a store for
// its own topic state. Alphabetical order is not an architectural decision, so
// the canonical store is declared, not derived.
//
// GetAllFiles ranges a MAP, so both loops below sort rather than break at a
// "first" match — an unsorted scan flipped config routing across otherwise
// identical builds.
func resolvePrimaryStore(allFiles map[string]*parser.FileDescriptor) (string, error) {
	var configStorePkgs, storePkgs []string
	for _, fileDesc := range allFiles {
		if fileDesc.ServiceConfigStore {
			configStorePkgs = append(configStorePkgs, fileDesc.Package)
		}
		if fileDesc.ServiceStore {
			storePkgs = append(storePkgs, fileDesc.Package)
		}
	}
	sort.Strings(configStorePkgs)
	sort.Strings(storePkgs)

	switch {
	case len(configStorePkgs) > 1:
		return "", fmt.Errorf("multiple services declare config_store: true (%s) — exactly one service may host config state", strings.Join(configStorePkgs, ", "))
	case len(configStorePkgs) == 1:
		return configStorePkgs[0], nil
	case len(storePkgs) == 0:
		return "", nil
	case len(storePkgs) == 1:
		// Unambiguous: one store, nothing to choose between.
		return storePkgs[0], nil
	default:
		return "", fmt.Errorf("proto set has %d services with store: true (%s) and none with config_store: true — mark the service that hosts config state with option (spec.service) = {config_store: true}", len(storePkgs), strings.Join(storePkgs, ", "))
	}
}

// InjectGrpcConfigState scans proto files for messages with (spec.grpc_config)
// annotations and appends a per-service *ConfigState message to each service's own
// proto file. The ConfigState is a service_state boundary wrapping the service's
// Config message, enabling typed Get/Watch access from the state service.
//
// For example, if identity.proto has Config with grpc_config "identity",
// this appends IdentityConfigState to identity.proto.
//
// ServiceConfigState (shared runtime config) is defined in sys/grpcconfig.proto
// and does not need injection here.
//
// Must be called BEFORE AppendStateServiceToProtos.
func InjectGrpcConfigState(sourceProtoDir, tempProtoDir, flatProtoDir string) ([]GrpcConfigEntry, error) {
	reg, err := LoadServiceRegistry(sourceProtoDir)
	if err != nil {
		return nil, fmt.Errorf("loading registry: %w", err)
	}

	dp, err := parser.NewDescriptorParser(flatProtoDir)
	if err != nil {
		return nil, fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	// Collect all grpc_config annotations
	var configs []GrpcConfigEntry
	for relPath, fileDesc := range dp.GetAllFiles() {
		if IsInfraProtoFile(relPath, fileDesc) {
			continue
		}

		for _, msg := range fileDesc.Messages {
			ann, ok := msg.Annotations["grpc_config"]
			if !ok {
				continue
			}

			// Prefer the explicit service_name from grpc_config annotation
			// (e.g. {service_name: "saml"}) over the package/identity name.
			// This ensures registry lookups match ScanAndGenerateConfigHolder.
			serviceName := ann.StringValue
			if serviceName == "" {
				serviceName = fileDesc.ServiceName
			}
			if serviceName == "" {
				continue
			}

			fieldID, err := reg.ServiceID(serviceName)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", relPath, err)
			}

			configs = append(configs, GrpcConfigEntry{
				ServiceName:     serviceName,
				ConfigType:      fileDesc.Package + "." + msg.Name,
				FieldID:         fieldID,
				ProtoImportPath: relPath,
			})
		}
	}

	if len(configs) == 0 {
		zap.L().Info("No grpc_config annotations found, skipping ConfigState injection")
		return nil, nil
	}

	// Discover the backing store that hosts config state.
	primaryStore, err := resolvePrimaryStore(dp.GetAllFiles())
	if err != nil {
		return nil, err
	}
	if primaryStore == "" {
		return nil, fmt.Errorf("no service with store: true found in proto set — config state requires a backing store")
	}

	// Inject ConfigState into each service's proto file.
	for _, cfg := range configs {
		stateName := ConfigStateRootName(cfg.ServiceName)

		// Build the ConfigState message to append. Uses the local Config type
		// (not fully-qualified) since it's in the same proto file.
		// The label routes to the store discovered from (spec.service).config_store.
		configState := fmt.Sprintf(`
// %s is the state boundary for %s service configuration.
// Auto-injected by tacplus from (spec.grpc_config) annotation.
message %s {
  option (spec.service_state) = {name: "%s", label: ["%s"]};

  %s config = 1 [
    (spec.persistence) = true
  ];
}
`, stateName, cfg.ServiceName, stateName, stateName, primaryStore, cfg.ConfigType)

		// Append to both working copy and flat copy.
		for _, dir := range []string{tempProtoDir, flatProtoDir} {
			protoPath := filepath.Join(dir, cfg.ProtoImportPath)
			existing, err := os.ReadFile(protoPath)
			if err != nil {
				return nil, fmt.Errorf("failed to read %s: %w", cfg.ProtoImportPath, err)
			}

			// Skip if already injected (idempotent).
			if strings.Contains(string(existing), "message "+stateName+" {") {
				continue
			}

			if err := os.WriteFile(protoPath, append(existing, []byte(configState)...), 0644); err != nil {
				return nil, fmt.Errorf("failed to write ConfigState to %s: %w", cfg.ProtoImportPath, err)
			}
		}

		zap.L().Info("Injected ConfigState into service proto",
			zap.String("service", cfg.ServiceName),
			zap.String("state", stateName),
			zap.String("proto", cfg.ProtoImportPath))
	}

	if err := reg.Save(); err != nil {
		return nil, fmt.Errorf("saving registry.yaml: %w", err)
	}
	return configs, nil
}

// GenerateTenantConfig scans flatProtoDir for messages annotated with
// (spec.config_slice) tenant scope, assigns stable field numbers from the registry,
// and writes generated/tenant_config.proto into each of dirs.
// Must be called after ScanAndGenerateConfigHolder (registry already has service IDs).
func GenerateTenantConfig(sourceProtoDir, flatProtoDir string, dirs ...string) error {
	if len(dirs) == 0 {
		return nil
	}

	reg, err := LoadServiceRegistry(sourceProtoDir)
	if err != nil {
		return fmt.Errorf("loading registry: %w", err)
	}

	dp, err := parser.NewDescriptorParser(flatProtoDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	// Collect all messages with tenant config-slice annotations.
	type sliceEntry struct {
		pkg        string // proto package, e.g. "identity_tenant" (used for type qualifier)
		svcName    string // service name for field name, e.g. "identity" (may differ from pkg)
		msgName    string // e.g. "IdentityTenantConfig"
		importPath string // e.g. "services/identity_tenant.proto"
		fieldNum   int32
	}
	var slices []sliceEntry
	for relPath, fileDesc := range dp.GetAllFiles() {
		if IsInfraProtoFile(relPath, fileDesc) {
			continue
		}
		for _, msg := range fileDesc.Messages {
			serviceName, ok, err := configSliceOwner(relPath, fileDesc, msg, configSliceScopeTenant)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			// Ensure service is in the registry (should already be from ScanAndGenerateConfigHolder).
			if _, err := reg.ServiceID(serviceName); err != nil {
				return fmt.Errorf("%s: %w", relPath, err)
			}
			fieldNum, err := reg.AssignTenantSliceField(serviceName)
			if err != nil {
				return fmt.Errorf("%s: %w", relPath, err)
			}
			slices = append(slices, sliceEntry{
				pkg:        fileDesc.Package,
				svcName:    serviceName,
				msgName:    msg.Name,
				importPath: relPath,
				fieldNum:   fieldNum,
			})
		}
	}

	if len(slices) == 0 {
		return nil
	}

	// Sort by field number for deterministic output.
	for i := 1; i < len(slices); i++ {
		for j := i; j > 0 && slices[j].fieldNum < slices[j-1].fieldNum; j-- {
			slices[j], slices[j-1] = slices[j-1], slices[j]
		}
	}

	var buf strings.Builder
	buf.WriteString("// CODE GENERATED by tacplus — DO NOT EDIT\n")
	buf.WriteString("// Generated from (spec.config_slice) tenant-scope annotations.\n\n")
	buf.WriteString("syntax = \"proto3\";\n\n")
	buf.WriteString("package tenant_config;\n\n")
	buf.WriteString("import \"sys/spec.proto\";\n")
	var tenantImports []string
	for _, s := range slices {
		tenantImports = append(tenantImports, s.importPath)
	}
	for _, importPath := range uniqueImportPaths(tenantImports) {
		buf.WriteString(fmt.Sprintf("import \"%s\";\n", importPath))
	}
	buf.WriteString("\n// TenantConfig aggregates per-service tenant configuration slices.\n")
	buf.WriteString("// Field names match service names; field numbers are stable via registry.yaml.\n")
	buf.WriteString("message TenantConfig {\n")
	buf.WriteString("  option (spec.per_service_config) = true;\n\n")
	for _, s := range slices {
		buf.WriteString(fmt.Sprintf("  %s.%s %s = %d;\n", s.pkg, s.msgName, s.svcName, s.fieldNum))
	}
	buf.WriteString("}\n")

	content := buf.String()
	for _, dir := range dirs {
		genDir := filepath.Join(dir, "generated")
		if err := os.MkdirAll(genDir, 0755); err != nil {
			return fmt.Errorf("creating generated dir: %w", err)
		}
		if err := os.WriteFile(filepath.Join(genDir, "tenant_config.proto"), []byte(content), 0644); err != nil {
			return fmt.Errorf("writing generated/tenant_config.proto: %w", err)
		}
	}

	if err := reg.Save(); err != nil {
		return fmt.Errorf("saving registry.yaml: %w", err)
	}

	zap.L().Info("Generated generated/tenant_config.proto", zap.Int("slices", len(slices)))
	return nil
}

// GenerateProjectConfig scans flatProtoDir for messages annotated with
// (spec.config_slice) project scope, assigns stable field numbers from the registry,
// and writes generated/project_config.proto into each of dirs.
// Must be called after ScanAndGenerateConfigHolder (registry already has service IDs).
func GenerateProjectConfig(sourceProtoDir, flatProtoDir string, dirs ...string) error {
	if len(dirs) == 0 {
		return nil
	}

	reg, err := LoadServiceRegistry(sourceProtoDir)
	if err != nil {
		return fmt.Errorf("loading registry: %w", err)
	}

	dp, err := parser.NewDescriptorParser(flatProtoDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	// Collect all messages with project config-slice annotations.
	type sliceEntry struct {
		pkg        string // proto package, e.g. "oauth_project" (used for type qualifier)
		svcName    string // service name for field name, e.g. "oauth" (may differ from pkg)
		msgName    string // e.g. "OAuthProjectConfig"
		importPath string // e.g. "services/oauth_project.proto"
		fieldNum   int32
	}
	var slices []sliceEntry
	for relPath, fileDesc := range dp.GetAllFiles() {
		if IsInfraProtoFile(relPath, fileDesc) {
			continue
		}
		for _, msg := range fileDesc.Messages {
			serviceName, ok, err := configSliceOwner(relPath, fileDesc, msg, configSliceScopeProject)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			// Ensure service is in the registry (should already be from ScanAndGenerateConfigHolder).
			if _, err := reg.ServiceID(serviceName); err != nil {
				return fmt.Errorf("%s: %w", relPath, err)
			}
			fieldNum, err := reg.AssignProjectSliceField(serviceName)
			if err != nil {
				return fmt.Errorf("%s: %w", relPath, err)
			}
			slices = append(slices, sliceEntry{
				pkg:        fileDesc.Package,
				svcName:    serviceName,
				msgName:    msg.Name,
				importPath: relPath,
				fieldNum:   fieldNum,
			})
		}
	}

	if len(slices) == 0 {
		return nil
	}

	// Sort by field number for deterministic output.
	for i := 1; i < len(slices); i++ {
		for j := i; j > 0 && slices[j].fieldNum < slices[j-1].fieldNum; j-- {
			slices[j], slices[j-1] = slices[j-1], slices[j]
		}
	}

	var buf strings.Builder
	buf.WriteString("// CODE GENERATED by tacplus — DO NOT EDIT\n")
	buf.WriteString("// Generated from (spec.config_slice) project-scope annotations.\n\n")
	buf.WriteString("syntax = \"proto3\";\n\n")
	buf.WriteString("package project_config;\n\n")
	buf.WriteString("import \"sys/spec.proto\";\n")
	var projectImports []string
	for _, s := range slices {
		projectImports = append(projectImports, s.importPath)
	}
	for _, importPath := range uniqueImportPaths(projectImports) {
		buf.WriteString(fmt.Sprintf("import \"%s\";\n", importPath))
	}
	buf.WriteString("\n// ProjectConfig aggregates per-service project configuration slices.\n")
	buf.WriteString("// Field names match service names; field numbers are stable via registry.yaml.\n")
	buf.WriteString("message ProjectConfig {\n")
	buf.WriteString("  option (spec.per_service_config) = true;\n\n")
	for _, s := range slices {
		buf.WriteString(fmt.Sprintf("  %s.%s %s = %d;\n", s.pkg, s.msgName, s.svcName, s.fieldNum))
	}
	buf.WriteString("}\n")

	content := buf.String()
	for _, dir := range dirs {
		genDir := filepath.Join(dir, "generated")
		if err := os.MkdirAll(genDir, 0755); err != nil {
			return fmt.Errorf("creating generated dir: %w", err)
		}
		if err := os.WriteFile(filepath.Join(genDir, "project_config.proto"), []byte(content), 0644); err != nil {
			return fmt.Errorf("writing generated/project_config.proto: %w", err)
		}
	}

	if err := reg.Save(); err != nil {
		return fmt.Errorf("saving registry.yaml: %w", err)
	}

	zap.L().Info("Generated generated/project_config.proto", zap.Int("slices", len(slices)))
	return nil
}

func uniqueImportPaths(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	var unique []string
	for _, path := range paths {
		if seen[path] {
			continue
		}
		seen[path] = true
		unique = append(unique, path)
	}
	sort.Strings(unique)
	return unique
}

func configSliceOwner(relPath string, fileDesc *parser.FileDescriptor, msg parser.MessageDescriptor, wantScope string) (string, bool, error) {
	scopeAnn, ok := msg.Annotations["config_slice_scope"]
	if !ok {
		return "", false, nil
	}
	if scopeAnn.EnumValue != wantScope {
		return "", false, nil
	}
	serviceAnn, ok := msg.Annotations["config_slice_service"]
	if !ok || serviceAnn.StringValue == "" {
		return "", false, fmt.Errorf("%s: %s.%s (spec.config_slice).service_name is required", relPath, fileDesc.Package, msg.Name)
	}
	return serviceAnn.StringValue, true, nil
}

// ResolveStatesFromProto derives config.States from proto annotations.
// Own states (service_state in the service's own package) become RW.
// Imported states are derived from field-level state_import annotations and
// file-level state_imports declarations. Field-level imports additionally drive
// projection accessors; file-level imports declare read-only root access when a
// service needs the whole imported state client. Store services get RW to all
// tagged states.
func ResolveStatesFromProto(config *tactype.Config, inputDir string) error {
	if config.SelfPackage == "" {
		return nil // No self package — can't resolve ownership
	}

	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		// Fatal: a silent return here drops every state's resolution (including
		// the package-qualified auto_tls key type), so generation would proceed
		// with no states rather than fail (OF-19).
		return fmt.Errorf("state resolution: parse proto descriptors in %q: %w", inputDir, err)
	}

	allFiles := dp.GetAllFiles()

	// Build index: "package.StateName" → {protoFile, autoTls, exported}
	type stateInfo struct {
		protoFile string
		autoTls   string
		exported  bool
	}
	stateIndex := make(map[string]stateInfo)

	for path, fd := range allFiles {
		for _, msg := range fd.Messages {
			if ann, ok := msg.Annotations["service_state"]; ok && ann.StringValue != "" {
				key := fd.Package + "." + ann.StringValue
				info := stateInfo{protoFile: path}
				if tlsAnn, ok := msg.Annotations["service_state_auto_tls"]; ok {
					info.autoTls = tlsAnn.StringValue
				}
				if expAnn, ok := msg.Annotations["service_state_export"]; ok && expAnn.BoolValue {
					info.exported = true
				}
				stateIndex[key] = info
			}
		}
	}

	// Detect THE canonical backing store (the store: true service that also
	// declares config_store: true, or the sole store: true service). That store
	// PHYSICALLY HOLDS every distributed state partition in the build, so it
	// gets RW to ALL states by design — this is not per-state tagging and not
	// over-broad: a backing store that could not reach a partition could not
	// back it. Ordinary services get only their own states (RW) plus explicit
	// state_import projections (RO).
	//
	// This must be the SAME resolution config routing uses: keying off any
	// store: true package gave eventbus — a store for its own topic state only —
	// RW plus an auto-started Watch on every root in the build, which OPA
	// correctly denied, producing a permanent fleet-wide PERMISSION_DENIED storm.
	primaryStore, err := resolvePrimaryStore(allFiles)
	if err != nil {
		return err
	}
	isStore := config.AllStates || config.SelfPackage == primaryStore

	var states []tactype.StateEntry

	// 1. Own states: service_state messages in the service's own package → RW
	for path, fd := range allFiles {
		if fd.Package != config.SelfPackage {
			continue
		}
		for _, msg := range fd.Messages {
			if ann, ok := msg.Annotations["service_state"]; ok && ann.StringValue != "" {
				entry := tactype.StateEntry{
					ProtoFile:   path,
					MessageName: ann.StringValue,
					AccessMode:  tactype.AccessRW,
				}
				if tlsAnn, ok := msg.Annotations["service_state_auto_tls"]; ok {
					entry.AutoTlsKeyType = tlsAnn.StringValue
				}
				states = append(states, entry)
				zap.L().Info("State (own, RW)", zap.String("state", ann.StringValue), zap.String("proto", path))
			}
		}
	}

	// 2. Imported states: field-level state_import projections and file-level
	// state_imports declarations in the service's own proto files → RO.
	importedRoots := make(map[string]string)
	for path, fd := range allFiles {
		if fd.Package != config.SelfPackage {
			continue
		}
		for _, imp := range fd.FieldStateImports {
			if imp.Root == "" {
				return fmt.Errorf("%s: field %s.%s has invalid (spec.state_import) %q; expected package.StateRoot.field", path, imp.MessageName, imp.FieldName, imp.Source)
			}
			importedRoots[imp.Root] = path
		}
		for _, root := range fd.FileStateImports {
			if stateRootName(root) == "" {
				return fmt.Errorf("%s: file-level (spec.state_imports) source %q is invalid; expected package.StateRoot", path, root)
			}
			if _, ok := importedRoots[root]; !ok {
				importedRoots[root] = path
			}
		}
	}
	imported := make([]string, 0, len(importedRoots))
	for root := range importedRoots {
		imported = append(imported, root)
	}
	sort.Strings(imported)
	for _, imp := range imported {
		info, found := stateIndex[imp]
		if !found {
			return fmt.Errorf("state_import/state_imports references unknown state %q in %s", imp, importedRoots[imp])
		}
		if !info.exported {
			return fmt.Errorf("state_import/state_imports references non-exported state %q in %s", imp, importedRoots[imp])
		}
		entry := tactype.StateEntry{
			ProtoFile:   info.protoFile,
			MessageName: stateRootName(imp),
			AccessMode:  tactype.AccessRO,
		}
		states = append(states, entry)
		zap.L().Info("State (import, RO)", zap.String("state", imp), zap.String("proto", info.protoFile))
	}

	// 3. Backing store (proto store: true): RW to ALL state partitions in the
	// build — the store physically holds every partition, so this is by design,
	// not over-broad. Own-package states are already added RW above and skipped.
	if isStore {
		for key, info := range stateIndex {
			msgName := stateRootName(key)
			if msgName == "" {
				continue
			}
			// Skip own states (already added as RW above)
			if info.protoFile != "" && strings.HasPrefix(key, config.SelfPackage+".") {
				continue
			}
			entry := tactype.StateEntry{
				ProtoFile:   info.protoFile,
				MessageName: msgName,
				AccessMode:  tactype.AccessRW,
			}
			if info.autoTls != "" {
				entry.AutoTlsKeyType = info.autoTls
			}
			states = append(states, entry)
		}
	}

	// Only replace existing resolved state entries if proto annotations produced
	// a non-empty result or marked this service as a store.
	if len(states) > 0 || isStore {
		config.States = states
	}
	return nil
}

func stateRootName(ref string) string {
	idx := strings.LastIndex(ref, ".")
	if idx <= 0 || idx == len(ref)-1 {
		return ""
	}
	return ref[idx+1:]
}

// InjectConfigStates adds the per-service ConfigState and ServiceConfigState
// as :ro states for any gRPC server whose proto file matches a discovered
// grpc_config annotation. This lets the service's generated state
// wrapper provide typed Get/Watch access to its own config.
//
// The ConfigState state is now in the service's own proto file (not config_holder.proto).
// ServiceConfigState is in sys/grpcconfig.proto.
func InjectConfigStates(config *tactype.Config, entries []GrpcConfigEntry) {
	if len(entries) == 0 {
		return
	}

	// Build set of gRPC server proto files for this service.
	feProtos := make(map[string]bool)
	for _, fe := range config.GrpcServers {
		feProtos[fe.ProtoFile] = true
	}

	for _, entry := range entries {
		if !feProtos[entry.ProtoImportPath] {
			continue
		}
		stateName := ConfigStateRootName(entry.ServiceName)

		// Check if already present.
		found := false
		for _, b := range config.States {
			if b.MessageName == stateName {
				found = true
				break
			}
		}
		if found {
			continue
		}

		// ConfigState is now in the service's own proto file.
		config.States = append(config.States, tactype.StateEntry{
			ProtoFile:   entry.ProtoImportPath,
			MessageName: stateName,
			AccessMode:  tactype.AccessRO,
		})
		zap.L().Info("Auto-injected config state",
			zap.String("service", entry.ServiceName),
			zap.String("state", stateName),
			zap.String("proto", entry.ProtoImportPath))
	}

	// Inject ServiceConfigState as :ro for all gRPC services.
	// ServiceConfigState is now in sys/grpcconfig.proto.
	if len(config.GrpcServers) > 0 {
		const serviceConfigState = "ServiceConfigState"
		const serviceConfigProto = "sys/grpcconfig.proto"
		found := false
		for _, b := range config.States {
			if b.MessageName == serviceConfigState {
				found = true
				break
			}
		}
		if !found {
			config.States = append(config.States, tactype.StateEntry{
				ProtoFile:   serviceConfigProto,
				MessageName: serviceConfigState,
				AccessMode:  tactype.AccessRO,
			})
			zap.L().Info("Auto-injected ServiceConfigState state for runtime config")
		}
	}
}

// InjectTenantConfigSubscriptions automatically subscribes each service to its
// own per-service config slice. It scans all proto messages for the
// (spec.per_service_config) annotation. For each such message M (e.g. TenantConfig):
//
//   - It finds the grpc_config service that owns M (directly or via a map wrapper).
//   - It checks whether M has a field whose name matches the current service's package name.
//   - If so, it adds a read-only StateEntry for the owner's ConfigState to config.States.
//
// This means consuming services never need to declare
// state_imports in their own proto files — tacplus derives the subscription
// automatically from the TenantConfig field names.
func InjectTenantConfigSubscriptions(config *tactype.Config, inputDir string, entries []GrpcConfigEntry) {
	if config.SelfPackage == "" || len(entries) == 0 {
		return
	}

	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		zap.L().Warn("InjectTenantConfigSubscriptions: failed to create descriptor parser", zap.Error(err))
		return
	}

	allFiles := dp.GetAllFiles()

	// Build a map from fully-qualified type name → (protoFile, fieldNames).
	type msgInfo struct {
		protoFile  string
		fieldNames []string
	}
	msgIndex := make(map[string]msgInfo) // "pkg.MsgName" → info
	for path, fd := range allFiles {
		for _, msg := range fd.Messages {
			if _, ok := msg.Annotations["per_service_config"]; ok {
				var fields []string
				for _, f := range msg.Fields {
					fields = append(fields, f.Name)
				}
				key := fd.Package + "." + msg.Name
				msgIndex[key] = msgInfo{protoFile: path, fieldNames: fields}
			}
		}
	}
	if len(msgIndex) == 0 {
		return
	}

	// For each grpc_config entry, check whether its Config type transitively
	// references a per_service_config message. We do a one-level and two-level
	// field type walk (Config → field type → field type) to handle map wrappers.
	for _, entry := range entries {
		if entry.ServiceName == config.SelfPackage {
			continue // owning service — no subscription needed
		}

		// Find the Config message for this grpc_config entry.
		var configMsg *parser.MessageDescriptor
		var configFile *parser.FileDescriptor
		for _, fd := range allFiles {
			fdCopy := fd
			for i := range fd.Messages {
				fqn := fd.Package + "." + fd.Messages[i].Name
				if fqn == entry.ConfigType {
					configMsg = &fd.Messages[i]
					configFile = fdCopy
					break
				}
			}
			if configMsg != nil {
				break
			}
		}
		if configMsg == nil || configFile == nil {
			continue
		}

		// Walk Config's fields. For each field type (and one level of nesting),
		// check whether it is a per_service_config message containing a field
		// named after the current service.
		//
		// For map<K, V> fields, field.Type is the synthetic map-entry type name
		// (not the value type). Use field.MapValueType instead when field.IsMap.
		found := false
		for _, field := range configMsg.Fields {
			// Resolve the effective type: for map fields, check the value type.
			fieldType := field.Type
			if field.IsMap {
				fieldType = field.MapValueType
			}

			// Direct match: fieldType is itself a per_service_config message.
			if info, ok := msgIndex[fieldType]; ok {
				for _, fn := range info.fieldNames {
					if fn == config.SelfPackage {
						found = true
						break
					}
				}
			}
			if found {
				break
			}

			// One level of nesting: look inside the field's message type for
			// a per_service_config field (handles message wrappers).
			for _, fd := range allFiles {
				for _, msg := range fd.Messages {
					if fd.Package+"."+msg.Name == fieldType {
						for _, innerField := range msg.Fields {
							// Also handle map fields at the inner level.
							innerType := innerField.Type
							if innerField.IsMap {
								innerType = innerField.MapValueType
							}
							if info, ok := msgIndex[innerType]; ok {
								for _, fn := range info.fieldNames {
									if fn == config.SelfPackage {
										found = true
										break
									}
								}
							}
							if found {
								break
							}
						}
					}
					if found {
						break
					}
				}
				if found {
					break
				}
			}
			if found {
				break
			}
		}

		if !found {
			continue
		}

		// Subscribe to the owner's ConfigState as RO.
		stateName := ConfigStateRootName(entry.ServiceName)
		alreadyPresent := false
		for _, existing := range config.States {
			if existing.MessageName == stateName {
				alreadyPresent = true
				break
			}
		}
		if alreadyPresent {
			continue
		}

		config.States = append(config.States, tactype.StateEntry{
			ProtoFile:   entry.ProtoImportPath,
			MessageName: stateName,
			AccessMode:  tactype.AccessRO,
		})
		zap.L().Info("Auto-injected per-service config subscription",
			zap.String("self", config.SelfPackage),
			zap.String("owner", entry.ServiceName),
			zap.String("state", stateName))
	}
}

// writeMinimalGeneratedStubs writes placeholder generated/*.proto files into
// each of the given directories. These stubs satisfy protoc import resolution
// for embedded lib protos (accounting.proto, trace.proto, entitlementview.proto)
// when no service protos are present (sys-only build or no annotations found).
func writeMinimalGeneratedStubs(dirs []string) error {
	stubs := map[string]string{
		"services.proto": "// CODE GENERATED by tacplus — DO NOT EDIT\n" +
			"// Minimal stub — no (spec.service) or (spec.grpc_config) annotations found.\n\n" +
			"syntax = \"proto3\";\n\npackage services;\n\n" +
			"enum ServiceName {\n  SERVICE_NAME_UNSPECIFIED = 0;\n  SERVICE_NAME_ANY = 99;\n}\n",
		"accounting_method.proto": "// CODE GENERATED by tacplus — DO NOT EDIT\n\n" +
			"syntax = \"proto3\";\n\npackage accounting_method;\n\n" +
			"// AccountingMethod is empty when no gRPC services are discovered.\n" +
			"message AccountingMethod {}\n",
		"entitlements.proto": "// CODE GENERATED by tacplus — DO NOT EDIT\n\n" +
			"syntax = \"proto3\";\n\npackage entitlements;\n\n" +
			"message TokenEntitlements {\n  repeated string grants = 1;\n  repeated string azp = 100;\n}\n",
		"tenant_config.proto": "// CODE GENERATED by tacplus — DO NOT EDIT\n\n" +
			"syntax = \"proto3\";\n\npackage tenant_config;\n\n" +
			"// Stub — will be replaced by GenerateTenantConfig.\n" +
			"message TenantConfig {}\n",
		"project_config.proto": "// CODE GENERATED by tacplus — DO NOT EDIT\n\n" +
			"syntax = \"proto3\";\n\npackage project_config;\n\n" +
			"// Stub — will be replaced by GenerateProjectConfig.\n" +
			"message ProjectConfig {}\n",
	}

	for _, dir := range dirs {
		genDir := filepath.Join(dir, "generated")
		if err := os.MkdirAll(genDir, 0755); err != nil {
			return fmt.Errorf("failed to create generated dir: %w", err)
		}
		for name, content := range stubs {
			if err := os.WriteFile(filepath.Join(genDir, name), []byte(content), 0644); err != nil {
				return fmt.Errorf("failed to write generated/%s stub: %w", name, err)
			}
		}
	}
	zap.L().Info("Generated minimal generated/*.proto stubs (no annotations found)")
	return nil
}

// StateRootInfo identifies a state root discovered from proto descriptors.
type StateRootInfo struct {
	ProtoFile      string
	ProtoDir       string
	Package        string
	StateName      string
	JavaOuterClass string
}

// DiscoverStateRoot finds the proto file that declares the named service_state
// root. The name is the logical state root name after proto's defaulting rules,
// not a tacplus-specific file or service identity.
func DiscoverStateRoot(inputDir, stateName string) (*StateRootInfo, error) {
	if inputDir == "" {
		return nil, fmt.Errorf("cannot discover %s state root without an input proto directory", stateName)
	}
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to parse protos while discovering %s state root: %w", stateName, err)
	}

	allFiles := dp.GetAllFiles()
	paths := make([]string, 0, len(allFiles))
	for path := range allFiles {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var found *StateRootInfo
	for _, path := range paths {
		fd := allFiles[path]
		for _, msg := range fd.Messages {
			ann, ok := msg.Annotations["service_state"]
			if !ok || ann.StringValue != stateName {
				continue
			}
			if found != nil {
				return nil, fmt.Errorf("multiple %s state roots found: %s and %s", stateName, found.ProtoFile, path)
			}
			found = &StateRootInfo{
				ProtoFile: path,
				ProtoDir:  strings.TrimSuffix(path, ".proto"),
				Package:   fd.Package,
				StateName: ann.StringValue,
			}
		}
	}
	if found == nil {
		return nil, fmt.Errorf("no service_state root named %s found in %s", stateName, inputDir)
	}
	return found, nil
}

// ConfigStateAccessors resolves the actual generated state-client accessor for
// each proto package's typed Config boundary. Only states present in
// config.States are returned, so callers do not emit callbacks for states this
// package is not generating.
func ConfigStateAccessors(config *tactype.Config, inputDir string) (map[string]StateRootInfo, error) {
	result := make(map[string]StateRootInfo)
	if inputDir == "" || len(config.States) == 0 {
		return result, nil
	}
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return nil, fmt.Errorf("failed to parse protos while resolving config states: %w", err)
	}

	stateSet := make(map[string]bool)
	for _, st := range config.States {
		stateSet[st.ProtoFile+"\x00"+st.MessageName] = true
	}

	allFiles := dp.GetAllFiles()
	configStateRoots := declaredConfigStateRoots(allFiles)
	paths := make([]string, 0, len(allFiles))
	for path := range allFiles {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	for _, path := range paths {
		fd := allFiles[path]
		for _, msg := range fd.Messages {
			ann, ok := msg.Annotations["service_state"]
			if !ok || ann.StringValue == "" {
				continue
			}
			if !stateSet[path+"\x00"+ann.StringValue] {
				continue
			}
			if !configStateRoots[path+"\x00"+ann.StringValue] {
				continue
			}
			if _, exists := result[fd.Package]; exists {
				return nil, fmt.Errorf("multiple config state roots found for package %s", fd.Package)
			}
			result[fd.Package] = StateRootInfo{
				ProtoFile: path,
				ProtoDir:  strings.TrimSuffix(path, ".proto"),
				Package:   fd.Package,
				StateName: ann.StringValue,
			}
		}
	}
	return result, nil
}

func declaredConfigStateRoots(allFiles map[string]*parser.FileDescriptor) map[string]bool {
	roots := make(map[string]bool)
	for path, fd := range allFiles {
		for _, msg := range fd.Messages {
			ann, ok := msg.Annotations["grpc_config"]
			if !ok {
				continue
			}
			serviceName := ann.StringValue
			if serviceName == "" {
				serviceName = fd.ServiceName
			}
			if serviceName == "" {
				continue
			}
			roots[path+"\x00"+ConfigStateRootName(serviceName)] = true
		}
		if IsRuntimeConfigStateProto(path) {
			for _, msg := range fd.Messages {
				ann, ok := msg.Annotations["service_state"]
				if ok && ann.StringValue == "ServiceConfigState" {
					roots[path+"\x00"+ann.StringValue] = true
				}
			}
		}
	}
	return roots
}

// InjectHealthState adds ServiceHealth as :rw for all services.
// Every service must report its instance health so the generated service
// discovery path can route to healthy instances. The backing state service
// enforces per-instance write isolation via the token's instance_id claim.
func InjectHealthState(config *tactype.Config, inputDir string) error {
	// Only inject ServiceHealth for services that already have states or are
	// configured as gRPC servers. CLI tools and other non-state consumers don't
	// need health state tracking.
	if len(config.States) == 0 && !config.GrpcServerAll && len(config.GrpcServers) == 0 && !config.AllStates && !hasOwnGrpcServer(config, inputDir) {
		return nil
	}
	const healthStateName = "ServiceHealth"
	for i, b := range config.States {
		if b.MessageName == healthStateName {
			// The service may own the platform ServiceHealth root itself (kernel
			// does this). Mark that structural root so service-manager generation
			// can use it for health reporting and SMB authority discovery. Keep the
			// OF-4 protection for arbitrary user states that merely share the name.
			if b.IsHealthState || isDeclaredServiceHealthRoot(inputDir, b) {
				config.States[i].IsHealthState = true
				config.HealthStateName = healthStateName
			}
			return nil
		}
	}
	healthState, err := DiscoverStateRoot(inputDir, healthStateName)
	if err != nil {
		return err
	}
	accessMode := tactype.AccessRW
	config.States = append(config.States, tactype.StateEntry{
		ProtoFile:     healthState.ProtoFile,
		MessageName:   healthStateName,
		AccessMode:    accessMode,
		IsHealthState: true,
	})
	config.HealthStateName = healthStateName
	return nil
}

func isDeclaredServiceHealthRoot(inputDir string, entry tactype.StateEntry) bool {
	if inputDir == "" || entry.ProtoFile == "" || entry.MessageName == "" {
		return false
	}
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return false
	}
	fd := dp.GetAllFiles()[entry.ProtoFile]
	if fd == nil {
		return false
	}
	for _, msg := range fd.Messages {
		if msg.Name != entry.MessageName {
			continue
		}
		ann, ok := msg.Annotations["service_state"]
		if !ok || ann.StringValue != "ServiceHealth" {
			return false
		}
		exp, ok := msg.Annotations["service_state_export"]
		if !ok || !exp.BoolValue {
			return false
		}
		for _, field := range msg.Fields {
			if field.Name != "instances" ||
				!field.IsRepeated ||
				field.TypeKind != "message" ||
				field.Type != "healthreport.ServiceInstanceHealth" {
				continue
			}
			if keyed, ok := field.Annotations["instance_keyed"]; ok && keyed.BoolValue {
				return true
			}
		}
		return false
	}
	return false
}

func hasOwnGrpcServer(config *tactype.Config, inputDir string) bool {
	if config == nil || config.SelfPackage == "" || inputDir == "" {
		return false
	}
	p, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return hasOwnGrpcServerText(config, inputDir)
	}
	for _, fd := range p.GetAllFiles() {
		if fd.Package != config.SelfPackage && fd.ServiceName != config.SelfPackage {
			continue
		}
		for _, svc := range fd.Services {
			if svc.Synthesized || svc.IsC2D {
				continue
			}
			if svc.GrpcName != "" || svc.IsS2S || svc.IsC2S || svc.IsSMB {
				return true
			}
		}
	}
	return hasOwnGrpcServerText(config, inputDir)
}

func hasOwnGrpcServerText(config *tactype.Config, inputDir string) bool {
	found := false
	_ = filepath.Walk(inputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}
		relPath, relErr := filepath.Rel(inputDir, path)
		if relErr == nil && IsInfraProto(relPath) {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		text := string(data)
		if !protoTextOwnedByService(text, config.SelfPackage) {
			return nil
		}
		if strings.Contains(text, "(spec.service_s2s)") ||
			serviceC2sRe.MatchString(text) ||
			strings.Contains(text, "(spec.smb)") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

func protoTextOwnedByService(text, selfPackage string) bool {
	if pkgMatch := packageRe.FindStringSubmatch(text); pkgMatch != nil && pkgMatch[1] == selfPackage {
		return true
	}
	if svcMatch := serviceRe.FindStringSubmatch(text); svcMatch != nil {
		if nameMatch := nameRe.FindStringSubmatch(svcMatch[1]); nameMatch != nil && nameMatch[1] == selfPackage {
			return true
		}
	}
	return false
}
