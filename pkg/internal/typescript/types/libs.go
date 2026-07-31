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

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	commonspiffe "gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/spiffe"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
	"gitlab.com/secondstarcavalier/schematic/tacplus/templates"
)

// writeLibTemplates processes the embedded TypeScript library templates and writes them
// to the output directory at {langDir}/sys/
//
// The two token-mint exchangers are discovered by the caller BEFORE the proto
// spec annotations are stripped (StripSpecOptionsFromDir) and passed in here:
//   - info: the authoritative (S2S) mint — used by service_im semantics.
//   - clientInfo: the client-reachable (C2S) mint — used by client_im. Clients
//     have no S2S identity and cannot call the authoritative mint directly; they
//     mint through the C2S proxy (or the authoritative minter as a fallback when
//     no proxy is declared). Mirrors rust's libs.go two-exchanger computation.
func writeLibTemplates(config *tactype.Config, info, clientInfo *parser.TokenExchangerInfo) error {
	langDir := config.LangDir()
	sysBaseDir := filepath.Join(langDir, "chassis")

	zap.L().Info("Writing TypeScript library templates", zap.String("dir", sysBaseDir))

	// Compute package prefix for lib package names.
	// For scoped packages without "/": @myclient-proto → @myclient-proto/
	// For scoped packages with "/": @example/proto → @example/proto-
	// For unscoped: mypackage → mypackage-
	tsPackagePrefix := config.TsPackageBase + "-"
	if strings.HasPrefix(config.TsPackageBase, "@") && !strings.Contains(config.TsPackageBase, "/") { // npm scoped package without org separator
		tsPackagePrefix = config.TsPackageBase + "/"
	}

	bufbuildVersion, err := typeScriptDependencyVersion("@bufbuild/protobuf")
	if err != nil {
		return err
	}

	// The proto package containing the authoritative token mint service.
	texPkg := ""
	mintServiceName := ""
	hasTokenExchanger := false
	if info != nil {
		texPkg = info.Package
		mintServiceName = info.ServiceName
		hasTokenExchanger = true
	}
	defaultAudience := config.ServiceLabel()
	if hasTokenExchanger {
		defaultAudience = texPkg
	}

	// client_im exchanger — the client-reachable (C2S) token minter. The mint
	// method is invoked over a raw grpc-js unary call at "/pkg.Service/Method",
	// so we surface the proto package, service name and RPC method to the
	// template. Computed separately from the authoritative (service_im)
	// exchanger, mirroring rust's libs.go.
	clientTexPkg := ""
	clientMintServiceName := ""
	clientTokenMintMethod := ""
	hasClientTokenExchanger := false
	if clientInfo != nil {
		if clientInfo.TokenMintMethod == "" {
			return fmt.Errorf("client token minter %s.%s must define an RPC accepting TokenMintRequest and returning TokenMintResponse", clientInfo.Package, clientInfo.ServiceName)
		}
		clientTexPkg = clientInfo.Package
		clientMintServiceName = clientInfo.ServiceName
		clientTokenMintMethod = clientInfo.TokenMintMethod
		hasClientTokenExchanger = true
	}

	// Template data
	data := struct {
		TsPackageBase         string
		TsPackagePrefix       string // includes trailing / or - for constructing lib package names
		TsSpecialPackage      string // full npm package name for the spec proto package
		TokenRequestPackage   string // full npm package name for sys/tokenrequest.proto
		HasTokenExchanger     bool
		TokenExchangerPackage string // proto package name for token mint service
		MintServiceName       string // proto service name, e.g. "Mint"
		DefaultAudience       string
		ServiceLabel          string // service identity baked into Authorizer
		BufbuildProtobuf      string // @bufbuild/protobuf version from dependencies.yaml
		ClientOnly            bool
		// Client-reachable (C2S) mint — used by client_im.
		HasClientTokenExchanger     bool
		ClientTokenExchangerPackage string // C2S mint proto package name
		ClientMintServiceName       string // C2S mint proto service name
		ClientTokenMintMethod       string // C2S TokenMint RPC name
		Spiffe                      commonspiffe.TemplateGrammar
	}{
		TsPackageBase:               config.TsPackageBase,
		TsPackagePrefix:             tsPackagePrefix,
		TsSpecialPackage:            buildNpmPackageName(config.TsPackageBase, "sys", "spec"),
		TokenRequestPackage:         buildNpmPackageName(config.TsPackageBase, "sys", "tokenrequest"),
		HasTokenExchanger:           hasTokenExchanger,
		TokenExchangerPackage:       texPkg,
		MintServiceName:             mintServiceName,
		DefaultAudience:             defaultAudience,
		ServiceLabel:                config.ServiceLabel(),
		BufbuildProtobuf:            bufbuildVersion,
		ClientOnly:                  true,
		HasClientTokenExchanger:     hasClientTokenExchanger,
		ClientTokenExchangerPackage: clientTexPkg,
		ClientMintServiceName:       clientMintServiceName,
		ClientTokenMintMethod:       clientTokenMintMethod,
		Spiffe:                      commonspiffe.TemplateData(),
	}

	langFS, err := fs.Sub(templates.FS, "typescript/chassis")
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
		if !strings.HasSuffix(path, ".tmpl") {
			return nil
		}
		if !typeScriptClientOnlyLibTemplate(path) {
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

		zap.L().Debug("Wrote TypeScript library file", zap.String("path", outputFile))
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to write TypeScript library templates: %w", err)
	}

	zap.L().Info("TypeScript library templates written successfully")
	return nil
}

func typeScriptClientOnlyLibTemplate(path string) bool {
	switch path {
	// client_im: client identity manager for CLI tools/daemons that
	// authenticate as users (OAuth) or via credential pairs. Mints via the
	// C2S-reachable exchanger (never the authoritative S2S mint, which
	// client-only targets do not emit). This is the TS client's identity
	// manager — client builds skip service_im.
	case "client_im/ClientIdentityManager.ts.tmpl",
		"client_im/TokenSource.ts.tmpl",
		"client_im/index.ts.tmpl",
		"client_im/package.json.tmpl",
		"client_im/tsconfig.json.tmpl":
		return true
	case "jwk/ImmutableJwk.ts.tmpl",
		"jwk/index.ts.tmpl",
		"jwk/package.json.tmpl",
		"jwk/tsconfig.json.tmpl",
		"key/ImmutableKey.ts.tmpl",
		"key/index.ts.tmpl",
		"key/package.json.tmpl",
		"key/tsconfig.json.tmpl",
		"servicemanager/EndpointConn.ts.tmpl",
		"servicemanager/ServiceConnection.ts.tmpl",
		"servicemanager/index.ts.tmpl",
		"servicemanager/package.json.tmpl",
		"servicemanager/tsconfig.json.tmpl",
		"spiffe/SpiffeId.ts.tmpl",
		"spiffe/index.ts.tmpl",
		"spiffe/package.json.tmpl",
		"spiffe/tsconfig.json.tmpl",
		"tags/index.ts.tmpl",
		"tags/package.json.tmpl",
		"tags/tsconfig.json.tmpl",
		"token/TokenWrapper.ts.tmpl",
		"token/index.ts.tmpl",
		"token/package.json.tmpl",
		"token/tsconfig.json.tmpl":
		return true
	default:
		return false
	}
}
