// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

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

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	commonspiffe "github.com/tacplus-dev/tacplus/pkg/internal/common/spiffe"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"github.com/tacplus-dev/tacplus/templates"
)

// writeLibTemplates processes the embedded Python library templates and writes them
// to the output directory at {langDir}/sys/
//
// The two token-mint exchangers are discovered by the caller BEFORE the proto
// spec annotations are stripped (StripSpecOptionsFromDir) and passed in here:
//   - info: the authoritative (S2S) mint — used by service_im semantics.
//   - clientInfo: the client-reachable (C2S) mint — used by client_im. Clients
//     have no S2S identity and cannot call the authoritative mint directly; they
//     mint through the C2S proxy (or the authoritative minter as a fallback when
//     no proxy is declared). Mirrors rust's libs.go two-exchanger computation.
func writeLibTemplates(config *tactype.Config, info, clientInfo *parser.TokenExchangerInfo, acceptedAudiences []string) error {
	langDir := config.LangDir()
	sysBaseDir := filepath.Join(langDir, "chassis")

	zap.L().Info("Writing Python library templates", zap.String("dir", sysBaseDir))

	// The proto package containing the authoritative token mint service.
	texPkg := ""
	mintServiceName := ""
	hasTokenExchanger := false
	if info != nil {
		texPkg = pyModuleName(info.Package)
		mintServiceName = info.ServiceName
		hasTokenExchanger = true
	}
	defaultAudience := config.ServiceLabel()
	if hasTokenExchanger {
		defaultAudience = texPkg
	}

	// client_im exchanger — the client-reachable (C2S) token minter. Resolve
	// the grpc stub's python module from the minter proto's actual file path
	// (Dir = proto path minus .proto), NOT just its package name: the minter may
	// live in any file (e.g. a gateway proxy front), so we build the import the
	// same way generated cross-package proto imports do.
	clientMintGrpcModule := ""
	clientMintServiceName := ""
	clientTokenMintMethod := ""
	hasClientTokenExchanger := false
	if clientInfo != nil {
		if clientInfo.TokenMintMethod == "" {
			return fmt.Errorf("client token minter %s.%s must define an RPC accepting TokenMintRequest and returning TokenMintResponse", clientInfo.Package, clientInfo.ServiceName)
		}
		// clientInfo.Dir is the proto file path minus its extension, e.g.
		// "services/mint" for services/mint.proto or "contract/mintfront".
		protoFile := clientInfo.Dir + ".proto"
		parentDir := filepath.Dir(clientInfo.Dir)
		if parentDir == "." {
			parentDir = ""
		}
		importPath := pyProtoImportPath(parentDir, clientInfo.Package, protoFile)
		grpcModule := pyProtoFileModuleName(protoFile) + "_pb2_grpc"
		clientMintGrpcModule = pyJoinDotted(config.PythonPackageBase, "types", importPath, grpcModule)
		clientMintServiceName = clientInfo.ServiceName
		clientTokenMintMethod = clientInfo.TokenMintMethod
		hasClientTokenExchanger = true
	}

	// Template data
	data := struct {
		PythonPackageBase       string
		HasTokenExchanger       bool
		TokenExchangerPackage   string // proto package name for token mint service
		MintServiceName         string // proto service name, e.g. "Mint"
		DefaultAudience         string
		BootstrapServiceID      string
		ServiceLabel            string   // service identity baked into Authorizer
		AcceptedAudiences       []string // every `aud` the Authorizer accepts: own label + hosted traits
		ClientOnly              bool
		HasClientTokenExchanger bool
		ClientMintGrpcModule    string // full dotted python module for the C2S mint grpc stub
		ClientMintServiceName   string // C2S mint proto service name (stub = Name+"Stub")
		ClientTokenMintMethod   string // C2S TokenMint RPC name
		Spiffe                  commonspiffe.TemplateGrammar
	}{
		PythonPackageBase:       config.PythonPackageBase,
		HasTokenExchanger:       hasTokenExchanger,
		TokenExchangerPackage:   texPkg,
		MintServiceName:         mintServiceName,
		DefaultAudience:         defaultAudience,
		BootstrapServiceID:      defaultAudience,
		ServiceLabel:            config.ServiceLabel(),
		AcceptedAudiences:       acceptedAudiences,
		ClientOnly:              true,
		HasClientTokenExchanger: hasClientTokenExchanger,
		ClientMintGrpcModule:    clientMintGrpcModule,
		ClientMintServiceName:   clientMintServiceName,
		ClientTokenMintMethod:   clientTokenMintMethod,
		Spiffe:                  commonspiffe.TemplateData(),
	}

	langFS, err := fs.Sub(templates.FS, "python/chassis")
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
		if !pythonClientOnlyLibTemplate(path) {
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

		zap.L().Debug("Wrote Python library file", zap.String("path", outputFile))
		return nil
	})

	if err != nil {
		return fmt.Errorf("failed to write Python library templates: %w", err)
	}

	zap.L().Info("Python library templates written successfully")
	return nil
}

func pythonClientOnlyLibTemplate(path string) bool {
	switch path {
	case "servicemanager/__init__.py.tmpl",
		"servicemanager/connection.py.tmpl",
		"servicemanager/endpoint.py.tmpl",
		"tags/__init__.py.tmpl",
		// client_im: client identity manager for CLI tools/daemons that
		// authenticate as users (OAuth) or via credential pairs. Mints via the
		// C2S-reachable exchanger (never the authoritative S2S mint, which
		// client-only targets do not emit).
		"client_im/__init__.py.tmpl",
		"client_im/manager.py.tmpl",
		"client_im/source.py.tmpl":
		return true
	default:
		return false
	}
}
