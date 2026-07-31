// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// restClientMessageEntry represents a single message type in a per-type HTTP client.
type restClientMessageEntry struct {
	Name      string // exported message name, e.g. "Jwks"
	FieldName string // unexported field name, e.g. "jwks"
}

// generatedRestClient tracks info about a generated per-type REST client for aggregators.
type generatedRestClient struct {
	PackageName string // e.g. "jwk"
	ImportAlias string // e.g. "jwk_rc"
	ImportPath  string // e.g. "example.com/myproj/tacplus/rest/internal/chassis/jwk"
	ParentDir   string // e.g. "sys"
}

// generateRestClients generates per-type HTTP clients for each proto package.
func generateRestClients(config *tactype.Config, packages []model.ProtoFile) ([]generatedRestClient, error) {
	var generated []generatedRestClient

	for _, pf := range packages {
		if len(pf.Messages) == 0 {
			continue
		}

		pkgName, parentDir := restClientPackagePath(pf)

		// Build message entries
		var messages []restClientMessageEntry
		for _, msg := range pf.Messages {
			messages = append(messages, restClientMessageEntry{
				Name:      msg.Name,
				FieldName: strings.ToLower(msg.Name[:1]) + msg.Name[1:],
			})
		}

		// Compute import paths
		typesImportPath := config.ModuleBasePath + "/types/" + parentDir + "/" + pkgName
		if parentDir == "services" {
			typesImportPath = config.ModuleBasePath + "/types/services/" + pkgName
		}
		httpClientImportPath := config.ModuleBasePath + "/chassis/httpclient"

		// Output directory
		outputDir := filepath.Join(config.OutputDir, "rest", "internal", parentDir, pkgName)
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create REST client output directory: %w", err)
		}

		data := struct {
			PackageName          string
			TypesImportPath      string
			HttpClientImportPath string
			Messages             []restClientMessageEntry
		}{
			PackageName:          pkgName,
			TypesImportPath:      typesImportPath,
			HttpClientImportPath: httpClientImportPath,
			Messages:             messages,
		}

		tmplStr, err := loadTemplate("restclient.go.tmpl")
		if err != nil {
			return nil, fmt.Errorf("failed to load restclient template: %w", err)
		}

		t, err := template.New("restclient").Parse(tmplStr)
		if err != nil {
			return nil, fmt.Errorf("failed to parse restclient template: %w", err)
		}

		var buf bytes.Buffer
		if err := t.Execute(&buf, data); err != nil {
			return nil, fmt.Errorf("failed to execute restclient template for %s: %w", pkgName, err)
		}

		formatted, err := format.Source(buf.Bytes())
		if err != nil {
			zap.L().Warn("failed to format REST client", zap.Error(err), zap.String("package", pkgName))
			formatted = buf.Bytes()
		}

		outputPath := filepath.Join(outputDir, "rest.go")
		if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
			return nil, fmt.Errorf("failed to write REST client: %w", err)
		}

		clientImportPath := config.ModuleBasePath + "/rest/internal/" + parentDir + "/" + pkgName

		generated = append(generated, generatedRestClient{
			PackageName: pkgName,
			ImportAlias: pkgName + "_rc",
			ImportPath:  clientImportPath,
			ParentDir:   parentDir,
		})

		zap.L().Info("Generated per-type REST client",
			zap.String("package", pkgName),
			zap.Int("messages", len(messages)))
	}

	// Generate per-group aggregators
	if err := generateRestClientGroupFiles(config, generated); err != nil {
		return nil, fmt.Errorf("failed to generate REST client group aggregators: %w", err)
	}

	return generated, nil
}

// restClientPackagePath computes (pkgName, parentDir) for a proto file.
// e.g. RelPath="sys/jwk.proto" Package="jwk" → ("jwk", "sys")
//
//	RelPath="services/identity.proto" Package="identity" → ("identity", "services")
func restClientPackagePath(pf model.ProtoFile) (pkgName, parentDir string) {
	parentDir = filepath.Dir(pf.RelPath)
	pkgName = pf.Package
	// Strip leading "services." prefix from proto package names
	if strings.HasPrefix(pkgName, "services.") {
		pkgName = pkgName[len("services."):]
	}
	// For service protos, parentDir is already "services"
	if parentDir == "." {
		parentDir = "sys"
	}
	return pkgName, parentDir
}

// restClientGroupEntry tracks a package for the group-level rest.go aggregator.
type restClientGroupEntry struct {
	FieldName    string // unexported field: "jwk"
	AccessorName string // exported accessor: "Jwk"
	ImportAlias  string // "jwk_rc"
}

// restClientGroupImport tracks a unique import for the group template.
type restClientGroupImport struct {
	Alias string
	Path  string
}

// generateRestClientGroupFiles generates rests.go at each parent dir.
func generateRestClientGroupFiles(config *tactype.Config, clients []generatedRestClient) error {
	type groupInfo struct {
		parentDir string
		entries   []restClientGroupEntry
		imports   []restClientGroupImport
	}
	groups := make(map[string]*groupInfo)
	seenImport := make(map[string]bool)

	for _, c := range clients {
		g, ok := groups[c.ParentDir]
		if !ok {
			g = &groupInfo{parentDir: c.ParentDir}
			groups[c.ParentDir] = g
		}

		alias := c.PackageName + "_rc"
		if !seenImport[alias] {
			seenImport[alias] = true
			g.imports = append(g.imports, restClientGroupImport{Alias: alias, Path: c.ImportPath})
		}

		accessorName := strings.ToUpper(c.PackageName[:1]) + c.PackageName[1:]
		g.entries = append(g.entries, restClientGroupEntry{
			FieldName:    c.PackageName,
			AccessorName: accessorName,
			ImportAlias:  alias,
		})
	}

	for _, g := range groups {
		data := struct {
			GroupPackageName string
			Imports          []restClientGroupImport
			Entries          []restClientGroupEntry
		}{
			GroupPackageName: g.parentDir,
			Imports:          g.imports,
			Entries:          g.entries,
		}

		tmplStr, err := loadTemplate("restclient_group.go.tmpl")
		if err != nil {
			return fmt.Errorf("failed to load restclient group template: %w", err)
		}
		tmpl, err := template.New("restclient_group").Parse(tmplStr)
		if err != nil {
			return fmt.Errorf("failed to parse restclient group template: %w", err)
		}

		var buf bytes.Buffer
		if err := tmpl.Execute(&buf, data); err != nil {
			return fmt.Errorf("failed to execute restclient group template: %w", err)
		}

		formatted, fmtErr := format.Source(buf.Bytes())
		if fmtErr != nil {
			zap.L().Warn("failed to format rests.go, writing unformatted", zap.Error(fmtErr))
			formatted = buf.Bytes()
		}

		groupDir := filepath.Join(config.OutputDir, "rest", "internal", g.parentDir)
		if err := os.MkdirAll(groupDir, 0755); err != nil {
			return fmt.Errorf("failed to create group directory: %w", err)
		}
		if err := os.WriteFile(filepath.Join(groupDir, "rests.go"), formatted, 0644); err != nil {
			return fmt.Errorf("failed to write rests.go for %s: %w", g.parentDir, err)
		}

		zap.L().Info("Generated group RestClients", zap.String("group", g.parentDir))
	}
	return nil
}

// restClientRootGroup tracks a group for the root REST client aggregator.
type restClientRootGroup struct {
	FieldName   string // "services"
	ImportAlias string // "services_rc"
	ImportPath  string // ".../rest/internal/services"
}

// generateRestClientRoot generates rest/rest.go — the root REST client aggregator.
func generateRestClientRoot(config *tactype.Config, clients []generatedRestClient) error {
	groupMap := make(map[string]bool)
	var groupOrder []string
	for _, c := range clients {
		if !groupMap[c.ParentDir] {
			groupMap[c.ParentDir] = true
			groupOrder = append(groupOrder, c.ParentDir)
		}
	}

	var groups []restClientRootGroup
	for _, gName := range groupOrder {
		groups = append(groups, restClientRootGroup{
			FieldName:   gName,
			ImportAlias: gName + "_rc",
			ImportPath:  config.ModuleBasePath + "/rest/internal/" + gName,
		})
	}

	funcMap := template.FuncMap{
		"Title": func(s string) string {
			if len(s) == 0 {
				return s
			}
			return strings.ToUpper(s[:1]) + s[1:]
		},
	}

	data := struct {
		Groups []restClientRootGroup
	}{
		Groups: groups,
	}

	tmplStr, err := loadTemplate("restclient_root.go.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load restclient root template: %w", err)
	}
	tmpl, err := template.New("restclient_root").Funcs(funcMap).Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse restclient root template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute restclient root template: %w", err)
	}

	formatted, fmtErr := format.Source(buf.Bytes())
	if fmtErr != nil {
		zap.L().Warn("failed to format rest.go, writing unformatted", zap.Error(fmtErr))
		formatted = buf.Bytes()
	}

	restDir := filepath.Join(config.OutputDir, "rest")
	if err := os.MkdirAll(restDir, 0755); err != nil {
		return fmt.Errorf("failed to create rest directory: %w", err)
	}

	outputPath := filepath.Join(restDir, "rest.go")
	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write rest.go: %w", err)
	}

	zap.L().Info("Generated root RestClients", zap.String("path", outputPath))
	return nil
}

// resolveRestClientPackages returns the proto packages that should get per-type HTTP clients.
func resolveRestClientPackages(config *tactype.Config, allParsedFiles []model.ProtoFile) []model.ProtoFile {
	// Filter out google well-known types
	var candidates []model.ProtoFile
	for _, pf := range allParsedFiles {
		if strings.HasPrefix(pf.RelPath, "google/") {
			continue
		}
		if len(pf.Messages) == 0 {
			continue
		}
		candidates = append(candidates, pf)
	}

	if config.RestClientAll {
		return candidates
	}

	if len(config.RestClients) > 0 {
		// RestClients entries use ProtoFile field as package path (e.g. "sys/jwk", "services/identity")
		wanted := make(map[string]bool)
		for _, entry := range config.RestClients {
			// Normalize: strip .proto suffix if present
			path := strings.TrimSuffix(entry.ProtoFile, ".proto")
			wanted[path] = true
		}
		var result []model.ProtoFile
		for _, pf := range candidates {
			relNoExt := strings.TrimSuffix(pf.RelPath, ".proto")
			if wanted[relNoExt] {
				result = append(result, pf)
			}
		}
		return result
	}

	return nil
}
