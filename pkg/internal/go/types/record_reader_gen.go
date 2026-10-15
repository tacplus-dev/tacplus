// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

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

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/recordread"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"go.uber.org/zap"
)

// goRecordReaderEntry is one (spec.record_read) declaration rendered for
// record_readers.go and the service manager's accessor: the typed reader this
// service gets over ONE record type's stored rows. The facts come from
// recordread.Collect; only the Go names are decided here.
type goRecordReaderEntry struct {
	StructName     string // e.g. "MessageRecordReader"
	AccessorName   string // SM method, e.g. "MessageRecordReader"
	RecordFQN      string // e.g. "messaging.Message"
	RecordName     string // e.g. "Message"
	RecordAlias    string // import alias of the record's types package, e.g. "messaging_pb"
	PlaneAlias     string // import alias of the plane's types package, e.g. "query_pb"
	ClientAlias    string // import alias of the plane client package, e.g. "query_gc"
	ClientStruct   string // e.g. "RecordMapClient"
	GroupAccessor  string // GrpcClients group accessor, e.g. "Vservices"
	ClientFn       string // client accessor on the group, e.g. "RecordMapClient"
	KeyField       string // proto key field name, e.g. "delivery_id"
	KeyGoField     string // Go struct field of the key on <T>Filter, e.g. "DeliveryId"
	KeyGoType      string // Go type of the key, e.g. "string"
	CreateFn       string // e.g. "CreateMessageQuery"
	ListFn         string // e.g. "ListMessagePage"
	DeleteFn       string // e.g. "DeleteMessageQuery"
	FilterType     string // e.g. "MessageFilter"
	OpenReqType    string // e.g. "OpenMessageQueryRequest"
	PullReqType    string // e.g. "PullMessageRequest"
	HasKeyFilter   bool
	HasDeletedFlag bool
}

// goRecordReaderImport is one import line of record_readers.go.
type goRecordReaderImport struct {
	Alias string
	Path  string
}

// goRecordReaderFile is the template data for record_readers.go.
type goRecordReaderFile struct {
	RecordReaders []goRecordReaderEntry
	Imports       []goRecordReaderImport
}

// collectGoRecordReaders resolves this service's (spec.record_read)
// declarations into Go template entries. Returns nil when the service declares
// no reads, which is every service that predates the field.
func collectGoRecordReaders(config *tactype.Config, protoDir string) ([]goRecordReaderEntry, error) {
	if protoDir == "" {
		return nil, nil
	}
	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return nil, fmt.Errorf("failed to parse protos for record reader generation: %w", err)
	}
	var entries []goRecordReaderEntry
	for _, r := range recordread.Collect(dp, config.SelfPackage) {
		traitParent := firstPathSegment(r.TraitRelPath)
		entries = append(entries, goRecordReaderEntry{
			StructName:     r.RecordName + "RecordReader",
			AccessorName:   r.RecordName + "RecordReader",
			RecordFQN:      r.RecordFQN,
			RecordName:     r.RecordName,
			RecordAlias:    r.RecordPackage + "_pb",
			PlaneAlias:     r.TraitPackage + "_pb",
			ClientAlias:    r.TraitPackage + "_gc",
			ClientStruct:   r.PlaneService + "Client",
			GroupAccessor:  goAccessorName(traitParent),
			ClientFn:       r.PlaneService + "Client",
			KeyField:       r.KeyField,
			KeyGoField:     toGoFieldName(r.KeyField),
			KeyGoType:      protoScalarGoType(r.KeyProtoType),
			CreateFn:       "Create" + r.RecordName + "Query",
			ListFn:         "List" + r.RecordName + "Page",
			DeleteFn:       "Delete" + r.RecordName + "Query",
			FilterType:     r.RecordName + "Filter",
			OpenReqType:    "Open" + r.RecordName + "QueryRequest",
			PullReqType:    "Pull" + r.RecordName + "Request",
			HasKeyFilter:   r.HasKeyFilter,
			HasDeletedFlag: r.HasDeletedFlag,
		})
	}
	return entries, nil
}

// goRecordReaderImports returns the deduplicated, sorted import list the
// readers need: each record's types package, the plane's types package and
// each plane client package.
func goRecordReaderImports(config *tactype.Config, readers []recordread.Reader) []goRecordReaderImport {
	seen := map[string]bool{}
	var imports []goRecordReaderImport
	add := func(alias, path string) {
		if seen[alias] {
			return
		}
		seen[alias] = true
		imports = append(imports, goRecordReaderImport{Alias: alias, Path: path})
	}
	for _, r := range readers {
		add(r.RecordPackage+"_pb", config.ModuleBasePath+"/types/"+firstPathSegment(r.RecordRelPath)+"/"+r.RecordPackage)
		traitParent := firstPathSegment(r.TraitRelPath)
		add(r.TraitPackage+"_pb", config.ModuleBasePath+"/types/"+traitParent+"/"+r.TraitPackage)
		add(r.TraitPackage+"_gc", config.ModuleBasePath+"/"+config.GetSmPrefix()+"/grpc/clients/"+traitParent+"/"+r.TraitPackage)
	}
	sort.Slice(imports, func(i, j int) bool { return imports[i].Path < imports[j].Path })
	return imports
}

// firstPathSegment returns the top-level directory of a proto rel path, e.g.
// "vservices" for "vservices/query.proto".
func firstPathSegment(relPath string) string {
	parts := strings.Split(filepath.ToSlash(relPath), "/")
	if len(parts) > 1 {
		return parts[0]
	}
	return ""
}

// renderGoRecordReaders renders record_readers.go from its template data.
func renderGoRecordReaders(data goRecordReaderFile) ([]byte, error) {
	tmplContent, err := loadTemplate("record_readers.go.tmpl")
	if err != nil {
		return nil, fmt.Errorf("failed to load record_readers template: %w", err)
	}
	tmpl, err := template.New("recordreaders").Funcs(header.Funcs()).Parse(tmplContent)
	if err != nil {
		return nil, fmt.Errorf("failed to parse record_readers template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("failed to execute record_readers template: %w", err)
	}
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("failed to format record_readers.go: %w\nraw output:\n%s", err, buf.String())
	}
	return formatted, nil
}

// generateGoRecordReaders writes s2sm/record_readers.go when this service
// declares any (spec.record_read). No declaration, no file: the mechanism is
// additive and every service in the fleet predates it.
func generateGoRecordReaders(config *tactype.Config, protoDir string, entries []goRecordReaderEntry) error {
	if len(entries) == 0 {
		return nil
	}
	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return fmt.Errorf("failed to parse protos for record reader imports: %w", err)
	}
	data := goRecordReaderFile{
		RecordReaders: entries,
		Imports:       goRecordReaderImports(config, recordread.Collect(dp, config.SelfPackage)),
	}
	formatted, err := renderGoRecordReaders(data)
	if err != nil {
		return err
	}
	outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix())
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create record_readers dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "record_readers.go")
	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write record_readers.go: %w", err)
	}
	zap.L().Info("Generated Go record readers", zap.String("path", outputPath), zap.Int("records", len(entries)))
	return nil
}
