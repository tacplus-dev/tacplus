// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/header"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/recordread"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"go.uber.org/zap"
)

// javaRecordC2SMethod is one record type's pair of generated RPCs on the
// client-facing record read plane, in Java names.
type javaRecordC2SMethod struct {
	RecordName         string   // "VaultRow"
	RecordFQN          string   // "egressvault.VaultRow"
	RecordFqn          string   // Java type of the record
	GetMethodName      string   // "getVaultRow"
	ListMethodName     string   // "getVaultRowPage"
	GetRpcName         string   // "GetVaultRow", for status text
	ListRpcName        string   // "GetVaultRowPage"
	GetReq             string   // "GetVaultRowRequest"
	GetResp            string   // "GetVaultRowResponse"
	ListReq            string   // "GetVaultRowPageRequest"
	ListResp           string   // "GetVaultRowPageResponse"
	FilterType         string   // plane filter, "VaultRowFilter"
	ReaderAccessor     string   // "vaultRowRecordReader"
	KeyGetter          string   // "getRowId"
	KeySetter          string   // "setRowId"
	PrincipalPinSetter string   // "setOwnerSubject"
	NamespacePinSetter string   // "setTenantNamespace"
	Criteria           []string // PascalCase field names copied through from the client's filter
}

// javaRecordC2SFile is the template data for <Svc>RecordsGeneratedHandler.java.
type javaRecordC2SFile struct {
	PackageBase       string
	ChassisBase       string
	SmPrefix          string
	ServiceName       string // "EgressvaultRecords"
	ClassName         string // "EgressvaultRecordsGeneratedHandler"
	HandlerIfaceFqn   string // "<base>.s2sm.internal.grpc.servers.services.egressvault.EgressvaultRecords.EgressvaultRecordsHandler"
	SelfOuter         string // "<base>.types.services.egressvault.Egressvault"
	PlaneOuter        string // "<base>.types.vservices.egressquery.Egressquery"
	PageResultFqn     string
	ProtoExceptionFqn string // "<base>.types.sys.errors.ProtoException"
	Methods           []javaRecordC2SMethod
}

// collectJavaRecordC2SPlane resolves this service's (spec.record_read).c2s
// declarations into the generated handler's template data, or nil when it
// declares none.
func collectJavaRecordC2SPlane(config *tactype.Config, protoDir string) (*javaRecordC2SFile, error) {
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
	selfParent := ""
	if parts := strings.Split(filepath.ToSlash(plane.SelfRelPath), "/"); len(parts) > 1 {
		selfParent = parts[0]
	}
	selfOuter := javaTypesOuterForProto(config, protoDir, plane.SelfRelPath, plane.SelfPackage)
	out := &javaRecordC2SFile{
		PackageBase:       config.JavaPackageBase,
		ChassisBase:       config.JavaPackageBase + ".chassis",
		SmPrefix:          config.GetSmPrefix(),
		ServiceName:       plane.ServiceName,
		ClassName:         plane.ServiceName + "GeneratedHandler",
		HandlerIfaceFqn:   fmt.Sprintf("%s.%s.internal.grpc.servers.%s.%s.%s.%sHandler", config.JavaPackageBase, config.GetSmPrefix(), selfParent, plane.SelfPackage, plane.ServiceName, plane.ServiceName),
		SelfOuter:         selfOuter,
		PlaneOuter:        javaTypesOuterForProto(config, protoDir, plane.TraitRelPath, plane.TraitPackage),
		PageResultFqn:     config.JavaPackageBase + ".chassis.collection.CollectionRuntime.PageResult",
		ProtoExceptionFqn: config.JavaPackageBase + ".types.sys.errors.ProtoException",
	}
	for _, m := range plane.Methods {
		var criteria []string
		for _, c := range m.Criteria {
			criteria = append(criteria, javaPascalField(c))
		}
		out.Methods = append(out.Methods, javaRecordC2SMethod{
			RecordName:         m.RecordName,
			RecordFQN:          m.RecordFQN,
			RecordFqn:          selfOuter + "." + m.RecordName,
			GetMethodName:      "get" + m.RecordName,
			ListMethodName:     "get" + m.RecordName + "Page",
			GetRpcName:         "Get" + m.RecordName,
			ListRpcName:        "Get" + m.RecordName + "Page",
			GetReq:             "Get" + m.RecordName + "Request",
			GetResp:            "Get" + m.RecordName + "Response",
			ListReq:            "Get" + m.RecordName + "PageRequest",
			ListResp:           "Get" + m.RecordName + "PageResponse",
			FilterType:         m.RecordName + "Filter",
			ReaderAccessor:     strutil.ToLowerFirst(m.RecordName) + "RecordReader",
			KeyGetter:          "get" + javaPascalField(m.KeyField),
			KeySetter:          "set" + javaPascalField(m.KeyField),
			PrincipalPinSetter: "set" + javaPascalField(m.PrincipalPin),
			NamespacePinSetter: "set" + javaPascalField(m.NamespacePin),
			Criteria:           criteria,
		})
	}
	return out, nil
}

// renderJavaRecordC2SPlane renders <Svc>RecordsGeneratedHandler.java.
func renderJavaRecordC2SPlane(data *javaRecordC2SFile) ([]byte, error) {
	tmplContent, err := loadTemplate("RecordC2SHandler.java.tmpl")
	if err != nil {
		return nil, fmt.Errorf("failed to load RecordC2SHandler template: %w", err)
	}
	tmpl, err := template.New("recordc2s").Funcs(header.Funcs()).Parse(tmplContent)
	if err != nil {
		return nil, fmt.Errorf("failed to parse RecordC2SHandler template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("failed to execute RecordC2SHandler template: %w", err)
	}
	return buf.Bytes(), nil
}

// generateJavaRecordC2SPlane writes <Svc>RecordsGeneratedHandler.java beside
// ServiceManager.java when this service declares any (spec.record_read).c2s.
func generateJavaRecordC2SPlane(config *tactype.Config, data *javaRecordC2SFile) error {
	if data == nil {
		return nil
	}
	src, err := renderJavaRecordC2SPlane(data)
	if err != nil {
		return err
	}
	packagePath := strings.ReplaceAll(config.JavaPackageBase, ".", string(filepath.Separator))
	outputDir := filepath.Join(config.LangDir(), "src", "main", "java", packagePath, config.GetSmPrefix())
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create record_c2s dir: %w", err)
	}
	outputPath := filepath.Join(outputDir, data.ClassName+".java")
	if err := os.WriteFile(outputPath, src, 0644); err != nil {
		return fmt.Errorf("failed to write %s: %w", outputPath, err)
	}
	zap.L().Info("Generated the client-facing record read plane handler",
		zap.String("path", outputPath), zap.Int("records", len(data.Methods)))
	return nil
}
