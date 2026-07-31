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
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/consts"
	internalproto "gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/proto"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/tactype"
)

// diskStore is the per-store template data for the typed disk-persistence module.
type diskStore struct {
	StoreName    string // prost snake of the message name; file basename + fn suffix
	TypePath     string // crate::types::<dir>::<Type>
	ToJsonPath   string // crate::types::<dir>::<snake>_to_json
	FromJsonPath string // crate::types::<dir>::<snake>_from_json
	KeyField     string // prost snake of the collection_index (store_key) field

	// SnapshotOnly is true when the store has no store_key field: emit UC1
	// snapshot save/load only (no UC2 redo log — a whole-object singleton).
	SnapshotOnly bool
}

type diskStoreData struct {
	Header string
	Stores []diskStore
}

// generateDiskStore emits per-service_store typed disk-persistence helpers:
// UC1 snapshot save/load and UC2 redo append/decode, wired to the generated
// to_json/from_json/validate and the collection_index (store_key) field. No-op
// when there are no service_store types.
func generateDiskStore(config *tactype.Config, servicesInfo []ServiceInfo, inputDir string) error {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser for diskstore: %w", err)
	}

	// package -> parentDir, for Rust path resolution (mirrors rest dispatch).
	pkgToDir := buildPkgToDirMap(servicesInfo)
	for path, desc := range dp.GetAllFiles() {
		if _, ok := pkgToDir[desc.Package]; !ok {
			dir := filepath.Dir(path)
			if dir == "." {
				dir = ""
			}
			pkgToDir[desc.Package] = dir
		}
	}
	const cratePrefix = "crate"

	var stores []diskStore
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
					keyField = prostSnakeName(field.Name)
					break
				}
			}
			snapshotOnly := keyField == ""
			if snapshotOnly {
				zap.L().Info("service_store has no store_key field; emitting UC1 snapshot-only helpers (no redo log)",
					zap.String("message", msg.Name))
			}
			fqn := fd.Package + "." + msg.Name
			toJSON := resolveToJsonPath(fqn, pkgToDir, cratePrefix)
			fromJSON := resolveFromJsonPath(fqn, pkgToDir, cratePrefix)
			stores = append(stores, diskStore{
				StoreName:    prostSnakeName(msg.Name),
				TypePath:     modulePathOf(toJSON) + "::" + msg.Name,
				ToJsonPath:   toJSON,
				FromJsonPath: fromJSON,
				KeyField:     keyField,
				SnapshotOnly: snapshotOnly,
			})
		}
	}

	if len(stores) == 0 {
		return nil
	}
	sort.Slice(stores, func(i, j int) bool { return stores[i].StoreName < stores[j].StoreName })

	data := diskStoreData{
		Header: header.GeneratedHeader(consts.LangRust, "diskstore_stores"),
		Stores: stores,
	}

	outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "diskstore_stores")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create diskstore_stores dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "mod.rs")

	tmplStr, err := loadTemplate("diskstore_stores.rs.tmpl")
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

	zap.L().Info("Generated diskstore per-store helpers",
		zap.Int("stores", len(stores)),
		zap.String("path", outputPath))
	return nil
}

// modulePathOf returns the Rust module path of a fully-qualified function path
// (everything before the final "::segment").
func modulePathOf(fnPath string) string {
	if i := strings.LastIndex(fnPath, "::"); i >= 0 {
		return fnPath[:i]
	}
	return fnPath
}
