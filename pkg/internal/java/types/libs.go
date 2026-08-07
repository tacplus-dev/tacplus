// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/keepaliveconst"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	commonspiffe "gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/spiffe"
	stateproto "gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
	"gitlab.com/secondstarcavalier/schematic/tacplus/templates"
)

// writeLibTemplates processes the embedded Java library templates and writes them
// to the output directory at {langDir}/wrappers/src/main/java/{packagePath}/
func writeLibTemplates(config *tactype.Config) error {
	langDir := config.LangDir()

	// Package base path as directory structure
	packageBase := config.JavaPackageBase
	packagePath := strings.ReplaceAll(packageBase, ".", string(filepath.Separator))

	sysBaseDir := filepath.Join(langDir, "src", "main", "java", packagePath, "chassis")

	zap.L().Info("Writing Java library templates", zap.String("dir", sysBaseDir))

	// Discover the proto package containing the token mint service.
	texPkg := ""
	texOuterClass := ""
	mintServiceName := ""
	hasTokenExchanger := false
	info, err := parser.DiscoverTokenExchangerPackage(config.AnnotatedProtoDir)
	if err != nil {
		return err
	}
	if info != nil {
		texPkg = info.Package
		texOuterClass = strings.ToUpper(info.Package[:1]) + info.Package[1:]
		mintServiceName = info.ServiceName
		hasTokenExchanger = true
	}
	defaultAudience := config.ServiceLabel()
	if hasTokenExchanger {
		defaultAudience = texPkg
	}

	// Build virtual→physical audience mappings for audienceFromMethod.
	type audienceMapping struct {
		Package  string
		Audience string
	}
	var audienceMappings []audienceMapping
	// The IdentityManager template hard-codes these infra packages in the same
	// switch (they resolve to DEFAULT_AUDIENCE), so re-emitting them here would
	// produce a duplicate case label and break javac.
	mappingSeen := map[string]bool{
		"grpcconfig":      true,
		"jwkview":         true,
		"entitlementview": true,
		"healthreport":    true,
	}
	for _, entry := range config.GrpcS2SClients {
		if entry.TargetService != "" {
			pkg := strings.TrimSuffix(filepath.Base(entry.ProtoFile), ".proto")
			if pkg != entry.TargetService && !mappingSeen[pkg] {
				audienceMappings = append(audienceMappings, audienceMapping{
					Package:  pkg,
					Audience: entry.TargetService,
				})
				mappingSeen[pkg] = true
			}
		}
	}

	// Map state-service proto packages to the state store's service identity.
	// State RPCs physically go to the state store (kernel), but their gRPC
	// method names carry the owning service's proto package. Without this,
	// audienceFromMethod resolves the owning service as the audience, the
	// caller mints a token for that service, and kernel rejects it because
	// the delegate is only authorized for [self, kernel].
	stateStoreServiceID := ""
	for _, b := range config.States {
		if b.IsHealthState && b.AccessMode != tactype.AccessRO {
			if stateInfo, err := stateproto.DiscoverStateRoot(config.FlatProtoDir, b.MessageName); err == nil && stateInfo != nil {
				stateStoreServiceID = stateInfo.Package
			}
		}
	}
	if stateStoreServiceID != "" {
		for _, b := range config.States {
			var pkg string
			if stateInfo, err := stateproto.DiscoverStateRoot(config.FlatProtoDir, b.MessageName); err == nil && stateInfo != nil {
				pkg = stateInfo.Package
			} else {
				pkg = strings.TrimSuffix(filepath.Base(b.ProtoFile), ".proto")
			}
			if pkg != "" && pkg != stateStoreServiceID && !mappingSeen[pkg] {
				audienceMappings = append(audienceMappings, audienceMapping{
					Package:  pkg,
					Audience: stateStoreServiceID,
				})
				mappingSeen[pkg] = true
			}
		}
	}

	// Template data
	data := struct {
		PackageBase                string
		InterfacesBase             string
		ChassisBase                string
		TypesBase                  string
		HasTokenExchanger          bool
		TokenExchangerPackage      string // proto package name for token mint service
		TokenExchangerOuterClass   string // Java outer class for token mint service proto
		TokenRequestPackage        string // proto package for unified TokenMintRequest (sys/tokenrequest)
		TokenRequestOuterClass     string // Java outer class for TokenMintRequest proto
		MintServiceName            string // proto service name, e.g. "Mint"
		DefaultAudience            string
		AudienceMappings           []audienceMapping
		ServiceLabel               string // service identity baked into Authorizer
		Spiffe                     commonspiffe.TemplateGrammar
		KeepaliveTimeSecs          int
		KeepaliveTimeoutSecs       int
		KeepalivePermitMinTimeSecs int
	}{
		PackageBase:                packageBase,
		InterfacesBase:             config.Lang.Java.JavaImportBase(),
		ChassisBase:                packageBase + ".chassis",
		TypesBase:                  packageBase + ".types",
		HasTokenExchanger:          hasTokenExchanger,
		TokenExchangerPackage:      texPkg,
		TokenExchangerOuterClass:   texOuterClass,
		TokenRequestPackage:        "sys.tokenrequest",
		TokenRequestOuterClass:     "Tokenrequest",
		MintServiceName:            mintServiceName,
		DefaultAudience:            defaultAudience,
		AudienceMappings:           audienceMappings,
		ServiceLabel:               config.ServiceLabel(),
		Spiffe:                     commonspiffe.TemplateData(),
		KeepaliveTimeSecs:          keepaliveconst.TimeSecs,
		KeepaliveTimeoutSecs:       keepaliveconst.TimeoutSecs,
		KeepalivePermitMinTimeSecs: keepaliveconst.PermitMinTimeSecs,
	}

	langFS, err := fs.Sub(templates.FS, "java/chassis")
	if err != nil {
		return fmt.Errorf("failed to create sub filesystem: %w", err)
	}

	// Walk all embedded template files
	err = fs.WalkDir(langFS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(path, ".java.tmpl") {
			return nil
		}
		// client_im needs richer template data (the discovered client-reachable
		// C2S exchanger) than this generic walker supplies, and it is emitted
		// only for client builds. It is rendered by writeClientIdentityManager,
		// so skip it here.
		if strings.HasPrefix(path, "client_im/") {
			return nil
		}

		// Read template content
		content, err := fs.ReadFile(langFS, path)
		if err != nil {
			return fmt.Errorf("failed to read template %s: %w", path, err)
		}

		// Parse and execute template
		tmpl, err := template.New(path).Parse(string(content))
		if err != nil {
			return fmt.Errorf("failed to parse template %s: %w", path, err)
		}

		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			return fmt.Errorf("failed to execute template %s: %w", path, err)
		}

		// Output path: strip .tmpl suffix
		outputFile := strings.TrimSuffix(path, ".tmpl")
		outputPath := filepath.Join(sysBaseDir, outputFile)

		// Create directory
		if err := os.MkdirAll(filepath.Dir(outputPath), 0755); err != nil {
			return fmt.Errorf("failed to create directory for %s: %w", outputPath, err)
		}

		// Write file
		if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", outputPath, err)
		}

		zap.L().Debug("Wrote Java library file", zap.String("path", outputFile))
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to write Java library templates: %w", err)
	}

	zap.L().Info("Java library templates written successfully")
	return nil
}

// writeClientIdentityManager renders chassis/client_im/{TokenSource,ClientIdentityManager}.java —
// the client identity manager for CLI tools/daemons/webapps that authenticate as
// users (OAuth) or via credential pairs. Clients have no S2S identity, so the
// manager mints via the client-reachable (C2S) exchanger (a {proxy: true} minter
// front, or the authoritative minter as a fallback), NOT the authoritative S2S
// mint that service_im (IdentityManager) uses.
//
// Emitted only for builds with a client surface (c2sm/w2sm/c2dm). When no
// client-reachable minter is discovered the manager still compiles but every
// mint call throws "token minter is not configured".
func writeClientIdentityManager(config *tactype.Config) error {
	langDir := config.LangDir()
	packageBase := config.JavaPackageBase
	packagePath := strings.ReplaceAll(packageBase, ".", string(filepath.Separator))
	clientIMDir := filepath.Join(langDir, "src", "main", "java", packagePath, "chassis", "client_im")

	// Discover the client-reachable (C2S) mint exchanger — the {proxy: true}
	// front the client mints through. Falls back to the authoritative minter if
	// no proxy is declared. Kept SEPARATE from the authoritative S2S exchanger
	// that service_im discovers (mirrors rust's libs.go two-exchanger split).
	clientInfo, err := parser.DiscoverClientTokenExchangerPackage(config.AnnotatedProtoDir)
	if err != nil {
		return fmt.Errorf("client token exchanger discovery: %w", err)
	}

	hasClientTokenExchanger := false
	clientMintExchangerPackage := ""
	clientMintServiceName := ""
	clientTokenMintMethod := ""
	if clientInfo != nil {
		if clientInfo.TokenMintMethod == "" {
			return fmt.Errorf("client token minter %s.%s must define an RPC accepting TokenMintRequest and returning TokenMintResponse", clientInfo.Package, clientInfo.ServiceName)
		}
		hasClientTokenExchanger = true
		clientMintExchangerPackage = clientInfo.Package
		clientMintServiceName = clientInfo.ServiceName
		clientTokenMintMethod = clientInfo.TokenMintMethod
	}

	// Java gRPC blocking-stub method names are the RPC name with the first letter
	// lowercased (e.g. RPC "TokenMint" -> stub.tokenMint(...)).
	clientTokenMintMethodLower := clientTokenMintMethod
	if clientTokenMintMethodLower != "" {
		clientTokenMintMethodLower = strings.ToLower(clientTokenMintMethodLower[:1]) + clientTokenMintMethodLower[1:]
	}

	data := struct {
		ChassisBase                string
		TypesBase                  string
		TokenRequestPackage        string
		TokenRequestOuterClass     string
		HasClientTokenExchanger    bool
		ClientMintExchangerPackage string // proto package for the C2S mint service (services.<pkg>)
		ClientMintServiceName      string // C2S mint proto service name (<Name>Grpc)
		ClientTokenMintMethod      string // C2S TokenMint RPC name
		ClientTokenMintMethodLower string // stub method name (first letter lowercased)
	}{
		ChassisBase:                packageBase + ".chassis",
		TypesBase:                  packageBase + ".types",
		TokenRequestPackage:        "sys.tokenrequest",
		TokenRequestOuterClass:     "Tokenrequest",
		HasClientTokenExchanger:    hasClientTokenExchanger,
		ClientMintExchangerPackage: clientMintExchangerPackage,
		ClientMintServiceName:      clientMintServiceName,
		ClientTokenMintMethod:      clientTokenMintMethod,
		ClientTokenMintMethodLower: clientTokenMintMethodLower,
	}

	langFS, err := fs.Sub(templates.FS, "java/chassis/client_im")
	if err != nil {
		return fmt.Errorf("failed to create client_im sub filesystem: %w", err)
	}

	if err := os.MkdirAll(clientIMDir, 0755); err != nil {
		return fmt.Errorf("failed to create client_im directory: %w", err)
	}

	// Sorted for deterministic output (map/dir iteration order must not leak).
	names := []string{"ClientIdentityManager.java.tmpl", "TokenSource.java.tmpl"}
	for _, name := range names {
		content, err := fs.ReadFile(langFS, name)
		if err != nil {
			return fmt.Errorf("failed to read client_im template %s: %w", name, err)
		}
		tmpl, err := template.New(name).Parse(string(content))
		if err != nil {
			return fmt.Errorf("failed to parse client_im template %s: %w", name, err)
		}
		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			return fmt.Errorf("failed to execute client_im template %s: %w", name, err)
		}
		outputPath := filepath.Join(clientIMDir, strings.TrimSuffix(name, ".tmpl"))
		if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", outputPath, err)
		}
	}

	zap.L().Info("Java client identity manager written", zap.String("dir", clientIMDir))
	return nil
}
