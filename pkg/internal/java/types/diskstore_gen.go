// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"text/template"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/depfiles"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/consts"
	internalproto "github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// javaDiskStore is the per-store template data for the typed disk-persistence
// helpers (Java). All Java references are fully-qualified so the single
// generated DiskStores class can host stores drawn from many proto packages
// without import collisions (Java has no import aliases).
type javaDiskStore struct {
	StoreName  string // snake of the message name; file basename + method suffix
	MethodName string // PascalCase of the message name; method suffix
	TypeFqn    string // fully-qualified Java type, e.g. com...types.sys.foo.Foo.VerifyingKey
	HelpersFqn string // fully-qualified Helpers class, e.g. com...types.sys.foo.Helpers
	ToJsonFn   string // Helpers method name, e.g. verifyingKeyToJson
	FromJsonFn string // Helpers method name, e.g. verifyingKeyFromJson
	ValidateFn string // Helpers method name, e.g. validateVerifyingKey
	KeyGetter  string // getter for the collection_index (store_key) field, e.g. getKid

	// SnapshotOnly is true when the store has no store_key field: emit UC1
	// snapshot save/load only (no UC2 redo log — a whole-object singleton).
	SnapshotOnly bool

	// QuorumWrite is true when any field of the store declares
	// (spec.quorum_write): emit the generated write gate.
	QuorumWrite bool
	// MinDurableCopies is the declared (spec.min_durable_copies) floor the gate
	// enforces. Only meaningful when QuorumWrite.
	MinDurableCopies int
	// ConstName is the SCREAMING_SNAKE store name, for the emitted constant.
	ConstName string
}

type javaDiskStoreData struct {
	Header      string
	Package     string // package of the generated DiskStores class
	ChassisBase string // diskstore lib package base ({JavaPackageBase}.sys)
	Stores      []javaDiskStore
}

// generateJavaDiskStore emits per-service_store typed disk-persistence helpers
// (Java): UC1 snapshot save/load and UC2 redo open/record/decode, wired to the
// generated <type>ToJson/<type>FromJson/validate<Type> Helpers methods and the
// collection_index (store_key) field. No-op when there are no service_store
// types. Mirrors the Rust generateDiskStore.
//
// protoDir is the split/annotated proto tree (annotations still present), and
// srcDir is the generated Java source root (used to detect outer class names).
func generateJavaDiskStore(config *tactype.Config, protoDir, srcDir string) error {
	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser for diskstore: %w", err)
	}

	var stores []javaDiskStore
	for relPath, fd := range dp.GetAllFiles() {
		if internalproto.IsInfraProto(relPath) {
			continue
		}
		for msgIdx := range fd.Messages {
			msg := fd.Messages[msgIdx]
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

			parentDir := filepath.Dir(relPath)
			if parentDir == "." {
				parentDir = ""
			}
			typesPackage := javaPackageForProtoPackage(config.JavaPackageBase, fd.Package, parentDir)
			typesPackagePath := strings.ReplaceAll(typesPackage, ".", string(filepath.Separator))
			outerClass := detectOuterClassName(srcDir, typesPackagePath, filepath.Join(protoDir, relPath))

			keyGetter := ""
			if !snapshotOnly {
				keyGetter = "get" + strutil.ToPascalCase(keyField)
			}
			quorumWrite, minDurableCopies := parser.StoreQuorum(&fd.Messages[msgIdx])
			lower := strutil.ToLowerFirst(msg.Name)
			stores = append(stores, javaDiskStore{
				StoreName:        strutil.ToSnakeCase(msg.Name),
				MethodName:       msg.Name,
				TypeFqn:          typesPackage + "." + outerClass + "." + msg.Name,
				HelpersFqn:       typesPackage + ".Helpers",
				ToJsonFn:         lower + "ToJson",
				FromJsonFn:       lower + "FromJson",
				ValidateFn:       "validate" + msg.Name,
				KeyGetter:        keyGetter,
				SnapshotOnly:     snapshotOnly,
				ConstName:        strings.ToUpper(strutil.ToSnakeCase(msg.Name)),
				QuorumWrite:      quorumWrite,
				MinDurableCopies: minDurableCopies,
			})
		}
	}

	if len(stores) == 0 {
		return nil
	}
	sort.Slice(stores, func(i, j int) bool { return stores[i].StoreName < stores[j].StoreName })

	pkg := config.JavaPackageBase + ".chassis.diskstore.stores"
	data := javaDiskStoreData{
		Header:      header.GeneratedHeader(consts.LangJava, "diskstore_stores"),
		Package:     pkg,
		ChassisBase: config.JavaPackageBase + ".chassis",
		Stores:      stores,
	}

	tmplStr, err := loadTemplate("diskstore_stores.java.tmpl")
	if err != nil {
		return fmt.Errorf("failed to load diskstore template: %w", err)
	}
	tmpl, err := template.New("diskstore_stores").Parse(tmplStr)
	if err != nil {
		return fmt.Errorf("failed to parse diskstore template: %w", err)
	}

	outputDir := filepath.Join(srcDir, strings.ReplaceAll(pkg, ".", string(filepath.Separator)))
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create diskstore stores dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "DiskStores.java")

	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create diskstore output file: %w", err)
	}
	defer file.Close()
	if err := tmpl.Execute(file, data); err != nil {
		return fmt.Errorf("failed to execute diskstore template: %w", err)
	}

	zap.L().Info("Generated diskstore per-store helpers (Java)",
		zap.Int("stores", len(stores)),
		zap.String("path", outputPath))
	return nil
}

// javaGcsArchiveSDK is the Google Cloud Storage client a project pins to ship its
// redo archive to GCS. Pinning it in dependencies.yaml compiles GcsArchiveSink
// into the chassis; without the pin the chassis carries no SDK import, so a
// consumer that does not use GCS does not need the jar.
const javaGcsArchiveSDK = "com.google.cloud:google-cloud-storage"

// javaGcsArchiveEnabled reports whether the project's dependencies.yaml pins the
// GCS SDK for Java. No dependencies.yaml means no pin.
func javaGcsArchiveEnabled(config *tactype.Config) (bool, error) {
	if config.DepsYAMLPath == "" {
		return false, nil
	}
	deps, err := depfiles.LoadDepsConfig(config.DepsYAMLPath)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("failed to read dependencies.yaml for the archive sink: %w", err)
	}
	_, ok := depfiles.GetDepsMapForLang(deps, "java")[javaGcsArchiveSDK]
	return ok, nil
}
