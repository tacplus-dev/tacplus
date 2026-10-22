// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"text/template"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/recordread"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"go.uber.org/zap"
)

// goRecordC2SMethod is one record type's pair of generated RPCs on the
// client-facing record read plane, in Go names.
type goRecordC2SMethod struct {
	RecordName          string   // "VaultRow"
	RecordFQN           string   // "egressvault.VaultRow"
	GetMethodName       string   // "GetVaultRow"
	ListMethodName      string   // "GetVaultRowPage"
	GetReq              string   // "GetVaultRowRequest"
	GetResp             string   // "GetVaultRowResponse"
	ListReq             string   // "GetVaultRowPageRequest"
	ListResp            string   // "GetVaultRowPageResponse"
	FilterType          string   // plane filter, "VaultRowFilter"
	ReaderAccessor      string   // "VaultRowRecordReader"
	KeyGoField          string   // "RowId"
	PrincipalPinGoField string   // <T>Filter field pinned to the caller's subject
	NamespacePinGoField string   // <T>Filter field pinned to the caller's namespace
	Criteria            []string // Go field names copied straight through from the client's filter
}

// goRecordC2SFile is the template data for s2sm/record_c2s.go.
type goRecordC2SFile struct {
	ModuleBasePath string
	ServiceName    string // "EgressvaultRecords"
	StructName     string // "EgressvaultRecordsGeneratedHandler"
	HandlerType    string // "EgressvaultRecordsHandler"
	SelfAlias      string // "egressvault_pb"
	SelfImport     string
	PlaneAlias     string // "egressquery_pb"
	PlaneImport    string
	Methods        []goRecordC2SMethod
}

// collectGoRecordC2SPlane resolves this service's (spec.record_read).c2s
// declarations into the generated handler's template data, or nil when it
// declares none.
func collectGoRecordC2SPlane(config *tactype.Config, protoDir string) (*goRecordC2SFile, error) {
	if protoDir == "" {
		return nil, nil
	}
	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return nil, fmt.Errorf("failed to parse protos for the client-facing record read plane: %w", err)
	}
	plane := recordread.CollectC2SPlane(dp, config.SelfPackage)
	if plane == nil {
		return nil, nil
	}
	out := &goRecordC2SFile{
		ModuleBasePath: config.ModuleBasePath,
		ServiceName:    plane.ServiceName,
		StructName:     plane.ServiceName + "GeneratedHandler",
		HandlerType:    plane.ServiceName + "Handler",
		SelfAlias:      plane.SelfPackage + "_pb",
		SelfImport:     config.ModuleBasePath + "/types/" + firstPathSegment(plane.SelfRelPath) + "/" + plane.SelfPackage,
		PlaneAlias:     plane.TraitPackage + "_pb",
		PlaneImport:    config.ModuleBasePath + "/types/" + firstPathSegment(plane.TraitRelPath) + "/" + plane.TraitPackage,
	}
	for _, m := range plane.Methods {
		var criteria []string
		for _, c := range m.Criteria {
			criteria = append(criteria, toGoFieldName(c))
		}
		out.Methods = append(out.Methods, goRecordC2SMethod{
			RecordName:          m.RecordName,
			RecordFQN:           m.RecordFQN,
			GetMethodName:       "Get" + m.RecordName,
			ListMethodName:      "Get" + m.RecordName + "Page",
			GetReq:              "Get" + m.RecordName + "Request",
			GetResp:             "Get" + m.RecordName + "Response",
			ListReq:             "Get" + m.RecordName + "PageRequest",
			ListResp:            "Get" + m.RecordName + "PageResponse",
			FilterType:          m.RecordName + "Filter",
			ReaderAccessor:      m.RecordName + "RecordReader",
			KeyGoField:          toGoFieldName(m.KeyField),
			PrincipalPinGoField: toGoFieldName(m.PrincipalPin),
			NamespacePinGoField: toGoFieldName(m.NamespacePin),
			Criteria:            criteria,
		})
	}
	return out, nil
}

// renderGoRecordC2SPlane renders record_c2s.go from its template data.
func renderGoRecordC2SPlane(data *goRecordC2SFile) ([]byte, error) {
	tmplContent, err := loadTemplate("record_c2s.go.tmpl")
	if err != nil {
		return nil, fmt.Errorf("failed to load record_c2s template: %w", err)
	}
	tmpl, err := template.New("recordc2s").Funcs(header.Funcs()).Parse(tmplContent)
	if err != nil {
		return nil, fmt.Errorf("failed to parse record_c2s template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("failed to execute record_c2s template: %w", err)
	}
	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("failed to format record_c2s.go: %w\nraw output:\n%s", err, buf.String())
	}
	return formatted, nil
}

// generateGoRecordC2SPlane writes s2sm/record_c2s.go when this service
// declares any (spec.record_read).c2s. No declaration, no file.
func generateGoRecordC2SPlane(config *tactype.Config, data *goRecordC2SFile) error {
	if data == nil {
		return nil
	}
	formatted, err := renderGoRecordC2SPlane(data)
	if err != nil {
		return err
	}
	outputDir := filepath.Join(config.OutputDir, config.GetSmPrefix())
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create record_c2s dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, "record_c2s.go")
	if err := os.WriteFile(outputPath, formatted, 0644); err != nil {
		return fmt.Errorf("failed to write record_c2s.go: %w", err)
	}
	zap.L().Info("Generated the client-facing record read plane handler",
		zap.String("path", outputPath), zap.Int("records", len(data.Methods)))
	return nil
}
