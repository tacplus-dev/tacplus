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

// pyDiskStore is the per-store template data for the typed disk-persistence module.
type pyDiskStore struct {
	SnakeName    string // snake of the message name; file basename + fn suffix + helper fn prefix
	TypeName     string // the proto message name (the _pb2 class)
	PbModule     string // proto file basename (the _pb2 module name)
	HelperModule string // dotted path under types/, e.g. "services.mint.mint"
	KeyField     string // proto field name of the collection_index (store_key) field

	// SnapshotOnly is true when the store has no store_key field: emit UC1
	// snapshot save/load only (no UC2 redo log — a whole-object singleton).
	SnapshotOnly bool
}

type pyDiskStoreData struct {
	Header            string
	PythonPackageBase string
	Stores            []pyDiskStore
}

// generatePyDiskStore emits per-service_store typed disk-persistence helpers:
// UC1 snapshot save/load and UC2 redo append/decode, wired to the generated
// to_json/from_json/validate and the collection_index (store_key) field. No-op
// when there are no service_store types. Mirrors the Rust generateDiskStore.
func generatePyDiskStore(config *tactype.Config, inputDir string) error {
	dp, err := parser.NewDescriptorParser(inputDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser for diskstore: %w", err)
	}

	var stores []pyDiskStore
	for relPath, fd := range dp.GetAllFiles() {
		if internalproto.IsInfraProto(relPath) {
			continue
		}
		parentDir := filepath.Dir(relPath)
		protoFilename := pyModuleName(strings.TrimSuffix(filepath.Base(relPath), ".proto"))
		helperModule := pyProtoImportPath(parentDir, fd.Package, relPath)

		for _, msg := range fd.Messages {
			if _, ok := msg.Annotations["service_store"]; !ok {
				continue
			}
			keyField := ""
			for _, field := range msg.Fields {
				if skAnn, ok := field.Annotations["store_key"]; ok && skAnn.BoolValue {
					keyField = field.Name
					break
				}
			}
			snapshotOnly := keyField == ""
			if snapshotOnly {
				zap.L().Info("service_store has no store_key field; emitting UC1 snapshot-only helpers (no redo log)",
					zap.String("message", msg.Name))
			}
			stores = append(stores, pyDiskStore{
				SnakeName:    strutil.ToSnakeCase(msg.Name),
				TypeName:     msg.Name,
				PbModule:     protoFilename,
				HelperModule: helperModule,
				KeyField:     keyField,
				SnapshotOnly: snapshotOnly,
			})
		}
	}

	if len(stores) == 0 {
		return nil
	}
	sort.Slice(stores, func(i, j int) bool { return stores[i].SnakeName < stores[j].SnakeName })

	data := pyDiskStoreData{
		Header:            header.GeneratedHeader(consts.LangPython, "diskstore_stores"),
		PythonPackageBase: config.PythonPackageBase,
		Stores:            stores,
	}

	outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix(), "diskstore_stores")
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create diskstore_stores dir: %w", err)
	}
	// Ensure the sm-prefix package itself is importable.
	smInit := filepath.Join(config.OutputDir, config.GetSmPrefix(), "__init__.py")
	if _, statErr := os.Stat(smInit); os.IsNotExist(statErr) {
		if err := os.WriteFile(smInit, []byte("# Auto-generated sm package\n"), 0o644); err != nil {
			return fmt.Errorf("failed to create sm __init__.py: %w", err)
		}
	}
	outputPath := filepath.Join(outputDir, "__init__.py")

	tmplStr, err := loadTemplate("diskstore_stores.py.tmpl")
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

	zap.L().Info("Generated Python diskstore per-store helpers",
		zap.Int("stores", len(stores)),
		zap.String("path", outputPath))
	return nil
}
