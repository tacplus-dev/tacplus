// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/strutil"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
)

// tsRestCrudMessageEntry represents a single message type in a per-type HTTP client.
type tsRestCrudMessageEntry struct {
	Name      string // exported message name, e.g. "Jwks"
	FieldName string // camelCase field name, e.g. "jwks"
}

// generatedTsRestCrudClient tracks info about a generated per-type REST client for aggregators.
type generatedTsRestCrudClient struct {
	PackageName string // path slug, e.g. "jwk" or "gitlab-connector"
	FieldName   string // TypeScript field name, e.g. "jwk" or "gitlabConnector"
	ParentDir   string // e.g. "sys"
}

// generateTsRestCrudClients generates per-type HTTP clients for each proto package (TypeScript).
func generateTsRestCrudClients(config *tactype.Config, packages []model.ProtoFile) error {
	var generated []generatedTsRestCrudClient

	for _, pf := range packages {
		if len(pf.Messages) == 0 {
			continue
		}

		pkgName, parentDir := tsRestCrudPackagePath(pf)
		fieldName := tsRestCrudFieldName(pkgName)

		var messages []tsRestCrudMessageEntry
		for _, msg := range pf.Messages {
			messages = append(messages, tsRestCrudMessageEntry{
				Name:      msg.Name,
				FieldName: strings.ToLower(msg.Name[:1]) + msg.Name[1:],
			})
		}

		// Compute relative import path from rest/internal/{parentDir}/{pkgName} to types
		wrapperDir := filepath.Join("rest", "internal", parentDir, pkgName)
		typesDir := typescriptModuleDirForProtoFile("", pf)
		typesRel, _ := filepath.Rel(wrapperDir, typesDir)
		typesFile := strings.TrimSuffix(filepath.Base(pf.RelPath), ".proto") + "_pb.js"
		typesImport := filepath.ToSlash(typesRel) + "/" + typesFile

		outputDir := filepath.Join(config.OutputDir, "rest", "internal", parentDir, pkgName)
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return fmt.Errorf("failed to create REST client output directory: %w", err)
		}

		var buf strings.Builder
		buf.WriteString("// CODE GENERATED - DO NOT EDIT\n\n")
		buf.WriteString("import { fromBinary, toBinary } from '@bufbuild/protobuf';\n")
		fmt.Fprintf(&buf, "import * as types from '%s';\n\n", typesImport)

		// HTTPClient interface
		buf.WriteString("/** Generic HTTP client interface for type-safe operations. */\n")
		buf.WriteString("export interface HTTPClient<T> {\n")
		buf.WriteString("    doGet(url: string, fromBytes: (data: Uint8Array) => T): Promise<T>;\n")
		buf.WriteString("    doPost(url: string, data: Uint8Array, fromBytes: (data: Uint8Array) => T): Promise<T>;\n")
		buf.WriteString("    doMutation(method: string, url: string, data: Uint8Array | null): Promise<void>;\n")
		buf.WriteString("}\n\n")

		// TypedHttpClient class
		buf.WriteString("/** TypedHttpClient provides type-safe HTTP methods for all message types in this package. */\n")
		buf.WriteString("export class TypedHttpClient {\n")
		for _, msg := range messages {
			fmt.Fprintf(&buf, "    private readonly %sClient: HTTPClient<types.%s>;\n", msg.FieldName, msg.Name)
		}
		buf.WriteString("\n")
		buf.WriteString("    constructor(\n")
		for i, msg := range messages {
			comma := ","
			if i == len(messages)-1 {
				comma = ""
			}
			fmt.Fprintf(&buf, "        %sClient: HTTPClient<types.%s>%s\n", msg.FieldName, msg.Name, comma)
		}
		buf.WriteString("    ) {\n")
		for _, msg := range messages {
			fmt.Fprintf(&buf, "        this.%sClient = %sClient;\n", msg.FieldName, msg.FieldName)
		}
		buf.WriteString("    }\n")

		for _, msg := range messages {
			// Get
			fmt.Fprintf(&buf, "\n    /** Retrieves a %s from the specified URL. */\n", msg.Name)
			fmt.Fprintf(&buf, "    async get%s(url: string): Promise<types.%s> {\n", msg.Name, msg.Name)
			fmt.Fprintf(&buf, "        return this.%sClient.doGet(url, (data) => fromBinary(types.%sSchema, data));\n", msg.FieldName, msg.Name)
			buf.WriteString("    }\n")

			// Create
			fmt.Fprintf(&buf, "\n    /** Creates a new %s at the specified URL. */\n", msg.Name)
			fmt.Fprintf(&buf, "    async create%s(url: string, req: types.%s): Promise<types.%s> {\n", msg.Name, msg.Name, msg.Name)
			fmt.Fprintf(&buf, "        return this.%sClient.doPost(url, toBinary(types.%sSchema, req), (data) => fromBinary(types.%sSchema, data));\n", msg.FieldName, msg.Name, msg.Name)
			buf.WriteString("    }\n")

			// Update
			fmt.Fprintf(&buf, "\n    /** Updates an existing %s at the specified URL. */\n", msg.Name)
			fmt.Fprintf(&buf, "    async update%s(url: string, req: types.%s): Promise<void> {\n", msg.Name, msg.Name)
			fmt.Fprintf(&buf, "        return this.%sClient.doMutation('PUT', url, toBinary(types.%sSchema, req));\n", msg.FieldName, msg.Name)
			buf.WriteString("    }\n")

			// Delete
			fmt.Fprintf(&buf, "\n    /** Deletes a %s at the specified URL. */\n", msg.Name)
			fmt.Fprintf(&buf, "    async delete%s(url: string): Promise<void> {\n", msg.Name)
			fmt.Fprintf(&buf, "        return this.%sClient.doMutation('DELETE', url, null);\n", msg.FieldName)
			buf.WriteString("    }\n")
		}
		buf.WriteString("}\n")

		outputPath := filepath.Join(outputDir, "rest.ts")
		if err := os.WriteFile(outputPath, []byte(buf.String()), 0644); err != nil {
			return fmt.Errorf("failed to write REST client: %w", err)
		}

		generated = append(generated, generatedTsRestCrudClient{
			PackageName: pkgName,
			FieldName:   fieldName,
			ParentDir:   parentDir,
		})

		zap.L().Info("Generated per-type REST client (TS)",
			zap.String("package", pkgName),
			zap.Int("messages", len(messages)))
	}

	// Generate group-level aggregators
	if len(generated) > 0 {
		if err := generateTsRestCrudGroups(config, generated); err != nil {
			return err
		}
		if err := generateTsRestCrudRoot(config, generated); err != nil {
			return err
		}
	}

	return nil
}

// generateTsRestCrudGroups generates rest/internal/{group}/rests.ts
func generateTsRestCrudGroups(config *tactype.Config, clients []generatedTsRestCrudClient) error {
	groups := make(map[string][]generatedTsRestCrudClient)
	for _, c := range clients {
		groups[c.ParentDir] = append(groups[c.ParentDir], c)
	}

	for group, groupClients := range groups {
		groupDir := filepath.Join(config.OutputDir, "rest", "internal", group)
		if err := os.MkdirAll(groupDir, 0755); err != nil {
			return fmt.Errorf("failed to create group directory: %w", err)
		}

		var buf strings.Builder
		buf.WriteString("// CODE GENERATED - DO NOT EDIT\n\n")

		for _, c := range groupClients {
			fmt.Fprintf(&buf, "import { TypedHttpClient as %sClient } from './%s/rest.js';\n",
				tsPascalIdentifier(c.PackageName), c.PackageName)
		}

		buf.WriteString("\n/** RestClients aggregates all per-type HTTP clients for this group. */\n")
		buf.WriteString("export class RestClients {\n")
		buf.WriteString("    constructor(\n")
		for i, c := range groupClients {
			accessor := tsPascalIdentifier(c.PackageName)
			comma := ","
			if i == len(groupClients)-1 {
				comma = ""
			}
			fmt.Fprintf(&buf, "        public readonly %s: %sClient%s\n", c.FieldName, accessor, comma)
		}
		buf.WriteString("    ) {}\n")
		buf.WriteString("}\n")

		// Re-export
		for _, c := range groupClients {
			fmt.Fprintf(&buf, "export { TypedHttpClient as %sTypedHttpClient } from './%s/rest.js';\n",
				tsPascalIdentifier(c.PackageName), c.PackageName)
		}

		if err := os.WriteFile(filepath.Join(groupDir, "rests.ts"), []byte(buf.String()), 0644); err != nil {
			return fmt.Errorf("failed to write rests.ts for %s: %w", group, err)
		}
	}
	return nil
}

// generateTsRestCrudRoot generates rest/rest.ts
func generateTsRestCrudRoot(config *tactype.Config, clients []generatedTsRestCrudClient) error {
	groups := make(map[string]bool)
	var groupOrder []string
	for _, c := range clients {
		if !groups[c.ParentDir] {
			groups[c.ParentDir] = true
			groupOrder = append(groupOrder, c.ParentDir)
		}
	}

	restDir := filepath.Join(config.OutputDir, "rest")
	if err := os.MkdirAll(restDir, 0755); err != nil {
		return fmt.Errorf("failed to create rest directory: %w", err)
	}

	var buf strings.Builder
	buf.WriteString("// CODE GENERATED - DO NOT EDIT\n\n")

	for _, group := range groupOrder {
		accessor := tsPascalIdentifier(group)
		fmt.Fprintf(&buf, "import { RestClients as %sRestClients } from './internal/%s/rests.js';\n", accessor, group)
	}

	buf.WriteString("\n/** RestClients aggregates all per-type HTTP clients. */\n")
	buf.WriteString("export class RestClients {\n")
	buf.WriteString("    constructor(\n")
	for i, group := range groupOrder {
		accessor := tsPascalIdentifier(group)
		comma := ","
		if i == len(groupOrder)-1 {
			comma = ""
		}
		fmt.Fprintf(&buf, "        public readonly %s: %sRestClients%s\n", tsRestCrudFieldName(group), accessor, comma)
	}
	buf.WriteString("    ) {}\n")
	buf.WriteString("}\n")

	if err := os.WriteFile(filepath.Join(restDir, "rest.ts"), []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write rest.ts: %w", err)
	}

	zap.L().Info("Generated root RestClients (TS)", zap.Int("groups", len(groupOrder)))
	return nil
}

func tsRestCrudPackagePath(pf model.ProtoFile) (pkgName, parentDir string) {
	moduleDir := filepath.ToSlash(typescriptModuleDirForProtoFile("", pf))
	if strings.HasPrefix(moduleDir, "types/") {
		moduleDir = strings.TrimPrefix(moduleDir, "types/")
	}
	parentDir = filepath.Dir(moduleDir)
	pkgName = filepath.Base(moduleDir)
	if parentDir == "." {
		parentDir = "sys"
	}
	return pkgName, parentDir
}

func tsRestCrudFieldName(pathSlug string) string {
	pascal := tsPascalIdentifier(pathSlug)
	if pascal == "" {
		return "client"
	}
	return strutil.Decapitalize(pascal)
}

// resolveRestCrudPackages returns the proto packages that should get per-type HTTP clients.
func resolveRestCrudPackages(config *tactype.Config, allParsedFiles []model.ProtoFile) []model.ProtoFile {
	var candidates []model.ProtoFile
	for _, pf := range allParsedFiles {
		if strings.HasPrefix(pf.RelPath, "google/") {
			continue
		}
		if len(pf.Messages) == 0 {
			continue
		}
		candidates = append(candidates, pf)
	}

	if config.RestClientAll {
		return candidates
	}

	if len(config.RestClients) > 0 {
		wanted := make(map[string]bool)
		for _, entry := range config.RestClients {
			path := strings.TrimSuffix(entry.ProtoFile, ".proto")
			wanted[path] = true
		}
		var result []model.ProtoFile
		for _, pf := range candidates {
			relNoExt := strings.TrimSuffix(pf.RelPath, ".proto")
			if wanted[relNoExt] {
				result = append(result, pf)
			}
		}
		return result
	}

	return nil
}
