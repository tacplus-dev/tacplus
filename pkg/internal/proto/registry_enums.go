// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package proto

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/parser"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
)

const registryEnumBanner = `
// ============================================================================
// Generated Proto Registry IDs
// Auto-injected by tacplus for shared type/RPC identifiers.
// ============================================================================
`

// InjectRegistryEnums appends shared TypeID/RpcID enums to rewritten proto
// files after tacplus has finished injecting synthetic messages/services.
func InjectRegistryEnums(protoDir string) error {
	dp, err := parser.NewDescriptorParser(protoDir)
	if err != nil {
		return fmt.Errorf("failed to create descriptor parser: %w", err)
	}

	// Track which proto packages already have a TypeID enum injected.
	// Multiple files can share the same package, and proto3 enum values
	// are package-scoped, so duplicates cause compile errors.
	typeIDInjected := map[string]bool{}

	return filepath.Walk(protoDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || !strings.HasSuffix(path, ".proto") {
			return nil
		}

		relPath, err := filepath.Rel(protoDir, path)
		if err != nil {
			return fmt.Errorf("failed to compute relative path for %s: %w", path, err)
		}
		relPath = filepath.ToSlash(relPath)
		if IsInfraProto(relPath) {
			return nil
		}

		pf, err := dp.ConvertToProtoFile(path)
		if err != nil {
			return fmt.Errorf("failed to parse proto file %s: %w", relPath, err)
		}

		fd, err := dp.GetFileDescriptor(relPath)
		if err != nil {
			return fmt.Errorf("failed to get file descriptor for %s: %w", relPath, err)
		}

		typeNames := make([]string, 0, len(pf.Messages))
		for _, msg := range pf.Messages {
			typeNames = append(typeNames, msg.Name)
		}

		type rpcMethod struct {
			service string
			method  string
		}
		var rpcMethods []rpcMethod
		for _, svc := range fd.Services {
			for _, method := range svc.Methods {
				rpcMethods = append(rpcMethods, rpcMethod{
					service: svc.Name,
					method:  method.Name,
				})
			}
		}

		if len(typeNames) == 0 && len(rpcMethods) == 0 {
			return nil
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("failed to read proto file %s: %w", relPath, err)
		}
		text := string(content)

		hasTypeEnum := strings.Contains(text, "enum TypeID {") || typeIDInjected[pf.Package]
		// Group RPCs by service, preserving order.
		type svcMethods struct {
			name    string
			methods []string
		}
		var svcOrder []svcMethods
		svcIdx := map[string]int{}
		for _, rm := range rpcMethods {
			if i, ok := svcIdx[rm.service]; ok {
				svcOrder[i].methods = append(svcOrder[i].methods, rm.method)
			} else {
				svcIdx[rm.service] = len(svcOrder)
				svcOrder = append(svcOrder, svcMethods{name: rm.service, methods: []string{rm.method}})
			}
		}

		// Check which per-service RpcId enums already exist.
		needsTypeEnum := len(typeNames) > 0 && !hasTypeEnum
		var svcsNeedingEnum []svcMethods
		for _, svc := range svcOrder {
			enumName := strutil.ToPascalCase(svc.name) + "RpcId"
			if !strings.Contains(text, "enum "+enumName+" {") {
				svcsNeedingEnum = append(svcsNeedingEnum, svc)
			}
		}

		if !needsTypeEnum && len(svcsNeedingEnum) == 0 {
			return nil
		}

		var block strings.Builder
		block.WriteString(registryEnumBanner)

		if needsTypeEnum {
			typeIDInjected[pf.Package] = true
			block.WriteString("enum TypeID {\n")
			block.WriteString("  TYPE_ID_UNSPECIFIED = 0;\n")
			for i, name := range typeNames {
				block.WriteString(fmt.Sprintf("  TYPE_ID_%s = %d;\n", strutil.ToUpperSnakeCase(name), i+1))
			}
			block.WriteString("}\n")
		}

		for si, svc := range svcsNeedingEnum {
			svcUpper := strutil.ToUpperSnakeCase(svc.name)
			enumName := strutil.ToPascalCase(svc.name) + "RpcId"
			prefix := svcUpper + "_RPC_ID"

			if needsTypeEnum || si > 0 {
				block.WriteString("\n")
			}
			block.WriteString(fmt.Sprintf("enum %s {\n", enumName))
			block.WriteString(fmt.Sprintf("  %s_UNSPECIFIED = 0;\n", prefix))
			block.WriteString(fmt.Sprintf("  %s_ALL_ACCESSORS = 1;\n", prefix))
			block.WriteString(fmt.Sprintf("  %s_ALL_MUTATORS = 2;\n", prefix))
			block.WriteString(fmt.Sprintf("  %s_ALL_EXECUTORS = 3;\n", prefix))
			for i, method := range svc.methods {
				block.WriteString(fmt.Sprintf("  %s_%s = %d;\n", prefix, strutil.ToUpperSnakeCase(method), i+4))
			}
			block.WriteString("}\n")
		}

		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		text += block.String()

		if err := os.WriteFile(path, []byte(text), 0644); err != nil {
			return fmt.Errorf("failed to write registry enums to %s: %w", relPath, err)
		}

		zap.L().Info("Injected registry enums into proto file",
			zap.String("file", relPath),
			zap.Bool("type_enum", len(typeNames) > 0),
			zap.Bool("rpc_enum", len(rpcMethods) > 0))
		return nil
	})
}
