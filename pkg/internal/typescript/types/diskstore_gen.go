// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/header"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/consts"
	internalproto "gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// tsDiskStore is the per-store template data for the typed disk-persistence
// module (TypeScript). Mirrors the Rust diskStore struct, but the serializer
// surface is protobuf-es: <Type>Schema + toJson/fromJson + validate<Type>.
type tsDiskStore struct {
	StoreName   string // snake_case message name; file basename + fn suffix
	StorePascal string // PascalCase message name; method-name suffix
	TypeRef     string // import-aliased TS type, e.g. "mint_types.VerifyingKey"
	SchemaRef   string // import-aliased Schema, e.g. "mint_types.VerifyingKeySchema"
	ValidateRef string // import-aliased validate fn, e.g. "mint_types.validateVerifyingKey"
	KeyField    string // camelCase collection_index (store_key) field accessor

	// SnapshotOnly is true when the store has no store_key field: emit UC1
	// snapshot save/load only (no UC2 redo log — a whole-object singleton).
	SnapshotOnly bool
}

// tsDiskStoreImport describes an import of a generated types package used by a store.
type tsDiskStoreImport struct {
	Alias      string // import alias: "mint_types"
	ImportPath string // relative path to the types index, e.g. "../../types/services/mint/index.js"
}

type tsDiskStoreData struct {
	Header          string
	DiskstoreImport string // relative path to the diskstore lib index, e.g. "../../chassis/diskstore/index.js"
	Imports         []tsDiskStoreImport
	Stores          []tsDiskStore
}

// generateTsDiskStore emits per-service_store typed disk-persistence helpers
// (TypeScript): UC1 snapshot save/load and UC2 redo open/record/decode, wired
// to the generated toJson/fromJson + validate and the collection_index
// (store_key) field. No-op when there are no service_store types.
func generateTsDiskStore(config *tactype.Config, servicesInfo []ServiceInfo, inputDir string) error {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser for diskstore: %w", err)
	}

	// package -> parentDir, mirroring the REST/state import resolution.
	pkgToDir := make(map[string]string)
	for _, svc := range servicesInfo {
		if _, ok := pkgToDir[svc.PackageName]; !ok {
			pkgToDir[svc.PackageName] = svc.ParentDir
		}
	}
	for path, desc := range dp.GetAllFiles() {
		if _, ok := pkgToDir[desc.Package]; !ok {
			dir := filepath.Dir(path)
			if dir == "." {
				dir = "sys"
			}
			pkgToDir[desc.Package] = dir
		}
	}

	// Output dir: <SmPrefix>/diskstore_stores/. Imports resolve from here.
	wrapperDir := filepath.Join(config.GetSmPrefix(), "diskstore_stores")

	var stores []tsDiskStore
	importsByAlias := make(map[string]tsDiskStoreImport)
	for relPath, fd := range dp.GetAllFiles() {
		if internalproto.IsInfraProto(relPath) {
			continue
		}
		for _, msg := range fd.Messages {
			if _, ok := msg.Annotations["service_store"]; !ok {
				continue
			}
			keyField := ""
			for _, field := range msg.Fields {
				if skAnn, ok := field.Annotations["store_key"]; ok && skAnn.BoolValue {
					keyField = strutil.ToLowerCamelCase(field.Name)
					break
				}
			}
			snapshotOnly := keyField == ""
			if snapshotOnly {
				zap.L().Info("service_store has no store_key field; emitting UC1 snapshot-only helpers (no redo log)",
					zap.String("message", msg.Name))
			}

			alias := tsPkgImportAlias(fd.Package)
			if _, ok := importsByAlias[alias]; !ok {
				parentDir := pkgToDir[fd.Package]
				typesDir := tsTypesDirForPkg(fd.Package, parentDir)
				rel, _ := filepath.Rel(wrapperDir, typesDir)
				importsByAlias[alias] = tsDiskStoreImport{
					Alias:      alias,
					ImportPath: filepath.ToSlash(rel) + "/index.js",
				}
			}

			stores = append(stores, tsDiskStore{
				StoreName:    strutil.ToSnakeCase(msg.Name),
				StorePascal:  msg.Name,
				TypeRef:      alias + "." + msg.Name,
				SchemaRef:    alias + "." + msg.Name + "Schema",
				ValidateRef:  alias + ".validate" + msg.Name,
				KeyField:     keyField,
				SnapshotOnly: snapshotOnly,
			})
		}
	}

	if len(stores) == 0 {
		return nil
	}
	sort.Slice(stores, func(i, j int) bool { return stores[i].StoreName < stores[j].StoreName })

	var imports []tsDiskStoreImport
	for _, imp := range importsByAlias {
		imports = append(imports, imp)
	}
	sort.Slice(imports, func(i, j int) bool { return imports[i].Alias < imports[j].Alias })

	diskstoreDir := filepath.Join("chassis", "diskstore")
	diskstoreRel, _ := filepath.Rel(wrapperDir, diskstoreDir)

	data := tsDiskStoreData{
		Header:          header.GeneratedHeader(consts.LangTypeScript, "diskstore_stores"),
		DiskstoreImport: filepath.ToSlash(diskstoreRel) + "/index.js",
		Imports:         imports,
		Stores:          stores,
	}

	outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "diskstore_stores")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create diskstore_stores dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "stores.ts")

	tmplStr, err := loadTemplate("diskstore_stores.ts.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load diskstore template: %w", err)
	}
	tmpl, err := template.New("diskstore_stores").Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse diskstore template: %w", err)
	}

	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create diskstore output file: %w", err)
	}
	defer file.Close()
	if err := tmpl.Execute(file, data); err != nil {
		return fmt.Errorf("failed to execute diskstore template: %w", err)
	}

	zap.L().Info("Generated diskstore per-store helpers (TS)",
		zap.Int("stores", len(stores)),
		zap.String("path", outputPath))
	return nil
}

// tsPkgImportAlias builds a deterministic import alias for a proto package's
// generated types, e.g. "services.mint" -> "mint_types", "x509" -> "x509_types".
func tsPkgImportAlias(pkg string) string {
	name := pkg
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	name = strings.ReplaceAll(name, "-", "_")
	return name + "_types"
}

// tsTypesDirForPkg returns the generated types directory for a proto package,
// mirroring the layout used by the REST/state wrappers: types/<parentDir>/<pkg>
// (sys packages use parentDir "sys").
func tsTypesDirForPkg(pkg, parentDir string) string {
	if parentDir == "" {
		parentDir = "sys"
	}
	return filepath.Join("types", parentDir, pkg)
}
