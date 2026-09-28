// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/header"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/parser"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/consts"
	internalproto "gitlab.com/seabeam/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
)

// diskStoreEntry is the per-store template data for the typed disk-persistence
// module.
type diskStoreEntry struct {
	StoreName string // snake_case message name; file basename
	TypeName  string // exported (PascalCase) message name, used in fn names
	GoType    string // qualified Go type, e.g. "mint_pb.VerifyingKey"
	ToJSON    string // qualified ToJSON fn, e.g. "mint_pb.VerifyingKeyToJSON"
	FromJSON  string // qualified FromJSON fn, e.g. "mint_pb.VerifyingKeyFromJSON"
	Validate  string // qualified Validate fn, e.g. "mint_pb.ValidateVerifyingKey"
	KeyField  string // PascalCase Go field name of the collection_index (store_key)

	// SnapshotOnly is true when the store has no store_key field: emit UC1
	// snapshot Save/Load only (no UC2 redo log — a whole-object singleton).
	SnapshotOnly bool
}

// diskStoreImport tracks a unique types-package import for the generated module.
type diskStoreImport struct {
	Alias string
	Path  string
}

// diskStoreData is the top-level template data for diskstore_stores.go.
type diskStoreData struct {
	Header           string
	DiskStoreLibPath string
	Imports          []diskStoreImport
	Stores           []diskStoreEntry

	// AnyRedo is true when at least one store emits UC2 redo-log helpers.
	// Gates the encoding/json + fmt imports, which only the redo helpers use,
	// so a set of snapshot-only stores still compiles.
	AnyRedo bool
}

// generateDiskStore emits per-service_store typed disk-persistence helpers:
// UC1 snapshot Save/Load and UC2 redo Open/Record/Decode, wired to the generated
// ToJSON/FromJSON/Validate and the collection_index (store_key) field. It is a
// no-op when there are no service_store types.
//
// Mirrors the Rust generator (pkg/internal/rust/types/diskstore_gen.go): it
// discovers service_store-annotated messages across the whole proto set and
// emits one file with helpers per store.
func generateDiskStore(config *tactype.Config, inputDir string) error {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser for diskstore: %w", err)
	}

	var stores []diskStoreEntry
	seenImport := make(map[string]bool)
	var imports []diskStoreImport

	for relPath, fd := range dp.GetAllFiles() {
		if internalproto.IsInfraProto(relPath) {
			continue
		}
		for i := range fd.Messages {
			msg := fd.Messages[i]
			if _, ok := msg.Annotations["service_store"]; !ok {
				continue
			}

			keyField := ""
			for _, field := range msg.Fields {
				if skAnn, ok := field.Annotations["store_key"]; ok && skAnn.BoolValue {
					keyField = strutil.ToPascalCase(field.Name)
					break
				}
			}
			snapshotOnly := keyField == ""
			if snapshotOnly {
				zap.L().Info("service_store has no store_key field; emitting UC1 snapshot-only helpers (no redo log)",
					zap.String("message", msg.Name))
			}

			pkgName, parentDir := diskStorePackagePath(relPath, fd.Package)
			alias := pkgName + "_pb"
			importPath := config.ModuleBasePath + "/types/" + parentDir + "/" + pkgName

			if !seenImport[alias] {
				seenImport[alias] = true
				imports = append(imports, diskStoreImport{Alias: alias, Path: importPath})
			}

			stores = append(stores, diskStoreEntry{
				StoreName:    strutil.ToSnakeCase(msg.Name),
				TypeName:     msg.Name,
				GoType:       alias + "." + msg.Name,
				ToJSON:       alias + "." + msg.Name + "ToJSON",
				FromJSON:     alias + "." + msg.Name + "FromJSON",
				Validate:     alias + ".Validate" + msg.Name,
				KeyField:     keyField,
				SnapshotOnly: snapshotOnly,
			})
		}
	}

	if len(stores) == 0 {
		return nil
	}
	sort.Slice(stores, func(i, j int) bool { return stores[i].StoreName < stores[j].StoreName })
	sort.Slice(imports, func(i, j int) bool { return imports[i].Alias < imports[j].Alias })

	anyRedo := false
	for i := range stores {
		if !stores[i].SnapshotOnly {
			anyRedo = true
			break
		}
	}

	data := diskStoreData{
		Header:           header.GeneratedHeader(consts.LangGo, "diskstore_stores"),
		DiskStoreLibPath: config.ModuleBasePath + "/chassis/diskstore",
		Imports:          imports,
		Stores:           stores,
		AnyRedo:          anyRedo,
	}

	tmplStr, err := loadTemplate("diskstore_stores.go.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load diskstore template: %w", err)
	}
	tmpl, err := template.New("diskstore_stores").Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse diskstore template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return fmt.Errorf("failed to execute diskstore template: %w", err)
	}

	formatted, fmtErr := format.Source(buf.Bytes())
	if fmtErr != nil {
		zap.L().Warn("failed to format diskstore_stores.go, writing unformatted", zap.Error(fmtErr))
		formatted = buf.Bytes()
	}

	outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "diskstore")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create diskstore dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "diskstore.go")
	if err := os.WriteFile(outputPath, formatted, 0o644); err != nil {
		return fmt.Errorf("failed to write diskstore_stores output file: %w", err)
	}

	zap.L().Info("Generated diskstore per-store helpers",
		zap.Int("stores", len(stores)),
		zap.String("path", outputPath))
	return nil
}

// diskStorePackagePath computes (pkgName, parentDir) for a proto file, matching
// the layout used by the per-type REST clients and the types tree:
//   - parentDir is the proto file's directory ("." -> "sys"),
//   - pkgName is the proto package with a leading "services." stripped.
func diskStorePackagePath(relPath, pkg string) (pkgName, parentDir string) {
	parentDir = filepath.Dir(relPath)
	pkgName = pkg
	if strings.HasPrefix(pkgName, "services.") {
		pkgName = pkgName[len("services."):]
	}
	if parentDir == "." {
		parentDir = "sys"
	}
	return pkgName, parentDir
}
