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
	"github.com/tacplus-dev/tacplus/pkg/internal/proto"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
	"go.uber.org/zap"
)

// javaRecordReaderEntry is one (spec.record_read) declaration rendered as a
// <Record>RecordReader class plus the service manager's accessor. The facts come
// from recordread.Collect; only the Java names are decided here.
type javaRecordReaderEntry struct {
	PackageBase    string // e.g. "com.example"
	SmPrefix       string // e.g. "s2sm"
	ReaderClass    string // e.g. "MessageRecordReader"
	AccessorName   string // SM method, e.g. "messageRecordReader"
	RecordFQN      string // e.g. "messaging.Message"
	RecordFqn      string // Java type, e.g. "com.example.types.services.messaging.Messaging.Message"
	PlaneOuter     string // Java outer class of the plane's types, e.g. "com.example.types.vservices.query.Query"
	ClientFqn      string // e.g. "com.example.s2sm.internal.grpc.clients.vservices.query.RecordMapGrpcClient"
	ClientChain    string // accessor chain on GrpcClients, e.g. "vservices().recordMap()"
	PageResultFqn  string // e.g. "com.example.chassis.collection.CollectionRuntime.PageResult"
	KeyField       string // proto key field name, e.g. "delivery_id"
	KeySetter      string // builder setter, e.g. "setDeliveryId"
	KeyJavaType    string // e.g. "String"
	CreateFn       string // e.g. "createMessageQuery"
	ListFn         string // e.g. "listMessagePage"
	DeleteFn       string // e.g. "deleteMessageQuery"
	FilterType     string // e.g. "MessageFilter"
	OpenReqType    string // e.g. "OpenMessageQueryRequest"
	PullReqType    string // e.g. "PullMessageRequest"
	HasKeyFilter   bool
	HasDeletedFlag bool
}

// javaScalarKeyType maps a proto scalar key type to the Java type the plane's
// <T>Filter builder setter takes.
func javaScalarKeyType(protoType string) string {
	switch protoType {
	case "string":
		return "String"
	case "int64", "sint64", "sfixed64", "uint64", "fixed64":
		return "long"
	case "int32", "sint32", "sfixed32", "uint32", "fixed32":
		return "int"
	case "bool":
		return "boolean"
	}
	return protoType
}

// javaTypesOuterForProto returns "<types package>.<OuterClass>" for a proto
// file, the way the generated clients import it.
func javaTypesOuterForProto(config *tactype.Config, protoDir, relPath, pkg string) string {
	declaresState := false
	if content, err := os.ReadFile(filepath.Join(protoDir, relPath)); err == nil {
		declaresState = proto.DeclaresServiceStateSource(string(content))
	}
	outer := javaOuterClassForProto(protoDir, "", relPath, pkg)
	return javaTypesImportForProto(config.JavaPackageBase, relPath, pkg, outer, declaresState)
}

// collectJavaRecordReaders resolves this service's (spec.record_read)
// declarations into Java template entries. Returns nil when the service
// declares no reads, which is every service that predates the field.
func collectJavaRecordReaders(config *tactype.Config, protoDir string) ([]javaRecordReaderEntry, error) {
	if protoDir == "" {
		return nil, nil
	}
	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return nil, fmt.Errorf("failed to parse protos for record reader generation: %w", err)
	}
	var entries []javaRecordReaderEntry
	for _, r := range recordread.Collect(dp, config.SelfPackage) {
		traitParent := ""
		if parts := strings.Split(filepath.ToSlash(r.TraitRelPath), "/"); len(parts) > 1 {
			traitParent = parts[0]
		}
		entries = append(entries, javaRecordReaderEntry{
			PackageBase:    config.JavaPackageBase,
			SmPrefix:       config.GetSmPrefix(),
			ReaderClass:    r.RecordName + "RecordReader",
			AccessorName:   strutil.ToLowerFirst(r.RecordName) + "RecordReader",
			RecordFQN:      r.RecordFQN,
			RecordFqn:      javaTypesOuterForProto(config, protoDir, r.RecordRelPath, r.RecordPackage) + "." + r.RecordName,
			PlaneOuter:     javaTypesOuterForProto(config, protoDir, r.TraitRelPath, r.TraitPackage),
			ClientFqn:      fmt.Sprintf("%s.%s.internal.grpc.clients.%s.%s.%sGrpcClient", config.JavaPackageBase, config.GetSmPrefix(), traitParent, r.TraitPackage, r.PlaneService),
			ClientChain:    strutil.ToLowerCamelCase(strings.ReplaceAll(traitParent, "-", "_")) + "()." + javaGrpcClientAccessorName(r.PlaneService) + "()",
			PageResultFqn:  config.JavaPackageBase + ".chassis.collection.CollectionRuntime.PageResult",
			KeyField:       r.KeyField,
			KeySetter:      "set" + javaPascalField(r.KeyField),
			KeyJavaType:    javaScalarKeyType(r.KeyProtoType),
			CreateFn:       "create" + r.RecordName + "Query",
			ListFn:         "list" + r.RecordName + "Page",
			DeleteFn:       "delete" + r.RecordName + "Query",
			FilterType:     r.RecordName + "Filter",
			OpenReqType:    "Open" + r.RecordName + "QueryRequest",
			PullReqType:    "Pull" + r.RecordName + "Request",
			HasKeyFilter:   r.HasKeyFilter,
			HasDeletedFlag: r.HasDeletedFlag,
		})
	}
	return entries, nil
}

// renderJavaRecordReader renders one <Record>RecordReader.java.
func renderJavaRecordReader(e javaRecordReaderEntry) ([]byte, error) {
	tmplContent, err := loadTemplate("RecordReader.java.tmpl")
	if err != nil {
		return nil, fmt.Errorf("failed to load RecordReader template: %w", err)
	}
	tmpl, err := template.New("recordreader").Funcs(header.Funcs()).Parse(tmplContent)
	if err != nil {
		return nil, fmt.Errorf("failed to parse RecordReader template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, e); err != nil {
		return nil, fmt.Errorf("failed to execute RecordReader template: %w", err)
	}
	return buf.Bytes(), nil
}

// generateJavaRecordReaders writes one <Record>RecordReader.java per
// (spec.record_read) beside ServiceManager.java. No declaration, no file: the
// mechanism is additive and every service in the fleet predates it.
func generateJavaRecordReaders(config *tactype.Config, entries []javaRecordReaderEntry) error {
	if len(entries) == 0 {
		return nil
	}
	packagePath := strings.ReplaceAll(config.JavaPackageBase, ".", string(filepath.Separator))
	outputDir := filepath.Join(config.LangDir(), "src", "main", "java", packagePath, config.GetSmPrefix())
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create record reader dir: %w", err)
	}
	for _, e := range entries {
		src, err := renderJavaRecordReader(e)
		if err != nil {
			return err
		}
		outputPath := filepath.Join(outputDir, e.ReaderClass+".java")
		if err := os.WriteFile(outputPath, src, 0644); err != nil {
			return fmt.Errorf("failed to write %s: %w", outputPath, err)
		}
		zap.L().Info("Generated Java record reader", zap.String("path", outputPath))
	}
	return nil
}
