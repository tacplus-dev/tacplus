// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

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

// pyRestCrudMessageEntry represents a single message type in a per-type HTTP client.
type pyRestCrudMessageEntry struct {
	Name      string // PascalCase: "Jwks"
	SnakeName string // snake_case: "jwks"
}

// generatedPyRestCrudClient tracks info about a generated per-type REST client for aggregators.
type generatedPyRestCrudClient struct {
	PackageName string // e.g. "jwk"
	ParentDir   string // e.g. "sys"
}

// generatePyRestCrudClients generates per-type HTTP clients for each proto package (Python).
func generatePyRestCrudClients(config *tactype.Config, packages []model.ProtoFile) error {
	var generated []generatedPyRestCrudClient

	for _, pf := range packages {
		if len(pf.Messages) == 0 {
			continue
		}

		pkgName, parentDir := pyRestCrudPackagePath(pf)

		var messages []pyRestCrudMessageEntry
		for _, msg := range pf.Messages {
			messages = append(messages, pyRestCrudMessageEntry{
				Name:      msg.Name,
				SnakeName: strutil.ToSnakeCase(msg.Name),
			})
		}

		// Compute relative import path from rest/internal/{parentDir}/{pkgName} to types
		// rest/internal/sys/jwk -> depth 4 -> .....types.sys.jwk
		parentParts := splitPyPath(parentDir)
		wrapperDepth := 2 + len(parentParts) + 1 // rest + internal + parentDir parts + pkgName
		dots := strings.Repeat(".", wrapperDepth+1)
		pbImportFrom := dots + "types." + pyProtoImportPath(filepath.Dir(pf.RelPath), pf.Package, pf.RelPath)
		pbModuleName := pyProtoFileModuleName(pf.RelPath)

		outputDir := filepath.Join(config.OutputDir, "rest", "internal", pyPath(parentDir), pkgName)
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return fmt.Errorf("failed to create REST client output directory: %w", err)
		}

		// Write __init__.py
		initPath := filepath.Join(outputDir, "__init__.py")
		if err := os.WriteFile(initPath, []byte("# Auto-generated\n"), 0644); err != nil {
			return fmt.Errorf("failed to create __init__.py: %w", err)
		}

		var buf strings.Builder
		buf.WriteString("# CODE GENERATED - DO NOT EDIT\n\n")
		buf.WriteString("from typing import Callable, Optional, Protocol, TypeVar\n\n")
		buf.WriteString("T = TypeVar(\"T\")\n\n")
		fmt.Fprintf(&buf, "from %s import %s_pb2 as types_pb2\n\n", pbImportFrom, pbModuleName)

		// HTTPClient protocol
		buf.WriteString("class HTTPClient(Protocol[T]):\n")
		buf.WriteString("    \"\"\"Generic HTTP client interface for type-safe operations.\"\"\"\n")
		buf.WriteString("    def do_get(self, url: str, from_bytes: Callable[[bytes], T]) -> T: ...\n")
		buf.WriteString("    def do_post(self, url: str, data: bytes, from_bytes: Callable[[bytes], T]) -> T: ...\n")
		buf.WriteString("    def do_mutation(self, method: str, url: str, data: Optional[bytes]) -> None: ...\n")
		buf.WriteString("\n\n")

		// TypedHttpClient class
		buf.WriteString("class TypedHttpClient:\n")
		buf.WriteString("    \"\"\"Provides type-safe HTTP methods for all message types in this package.\"\"\"\n\n")

		// __init__
		buf.WriteString("    def __init__(\n")
		buf.WriteString("        self,\n")
		for _, msg := range messages {
			fmt.Fprintf(&buf, "        %s_client,\n", msg.SnakeName)
		}
		buf.WriteString("    ):\n")
		for _, msg := range messages {
			fmt.Fprintf(&buf, "        self._%s_client = %s_client\n", msg.SnakeName, msg.SnakeName)
		}
		buf.WriteString("\n")

		for _, msg := range messages {
			// Get
			fmt.Fprintf(&buf, "    def get_%s(self, url: str) -> types_pb2.%s:\n", msg.SnakeName, msg.Name)
			fmt.Fprintf(&buf, "        \"\"\"Retrieves a %s from the specified URL.\"\"\"\n", msg.Name)
			fmt.Fprintf(&buf, "        return self._%s_client.do_get(url, types_pb2.%s.FromString)\n\n", msg.SnakeName, msg.Name)

			// Create
			fmt.Fprintf(&buf, "    def create_%s(self, url: str, req: types_pb2.%s) -> types_pb2.%s:\n", msg.SnakeName, msg.Name, msg.Name)
			fmt.Fprintf(&buf, "        \"\"\"Creates a new %s at the specified URL.\"\"\"\n", msg.Name)
			fmt.Fprintf(&buf, "        return self._%s_client.do_post(url, req.SerializeToString(), types_pb2.%s.FromString)\n\n", msg.SnakeName, msg.Name)

			// Update
			fmt.Fprintf(&buf, "    def update_%s(self, url: str, req: types_pb2.%s) -> None:\n", msg.SnakeName, msg.Name)
			fmt.Fprintf(&buf, "        \"\"\"Updates an existing %s at the specified URL.\"\"\"\n", msg.Name)
			fmt.Fprintf(&buf, "        self._%s_client.do_mutation('PUT', url, req.SerializeToString())\n\n", msg.SnakeName)

			// Delete
			fmt.Fprintf(&buf, "    def delete_%s(self, url: str) -> None:\n", msg.SnakeName)
			fmt.Fprintf(&buf, "        \"\"\"Deletes a %s at the specified URL.\"\"\"\n", msg.Name)
			fmt.Fprintf(&buf, "        self._%s_client.do_mutation('DELETE', url, None)\n\n", msg.SnakeName)
		}

		outputPath := filepath.Join(outputDir, "rest.py")
		if err := os.WriteFile(outputPath, []byte(buf.String()), 0644); err != nil {
			return fmt.Errorf("failed to write REST client: %w", err)
		}

		generated = append(generated, generatedPyRestCrudClient{
			PackageName: pkgName,
			ParentDir:   parentDir,
		})

		zap.L().Info("Generated per-type REST client (Python)",
			zap.String("package", pkgName),
			zap.Int("messages", len(messages)))
	}

	// Generate group-level aggregators and root
	if len(generated) > 0 {
		if err := generatePyRestCrudGroups(config, generated); err != nil {
			return err
		}
		if err := generatePyRestCrudRoot(config, generated); err != nil {
			return err
		}
	}

	return nil
}

// generatePyRestCrudGroups generates rest/internal/{group}/rests.py
func generatePyRestCrudGroups(config *tactype.Config, clients []generatedPyRestCrudClient) error {
	groups := make(map[string][]generatedPyRestCrudClient)
	for _, c := range clients {
		groups[c.ParentDir] = append(groups[c.ParentDir], c)
	}

	for group, groupClients := range groups {
		groupDir := filepath.Join(config.OutputDir, "rest", "internal", pyPath(group))
		if err := os.MkdirAll(groupDir, 0755); err != nil {
			return fmt.Errorf("failed to create group directory: %w", err)
		}

		// __init__.py for group
		initPath := filepath.Join(groupDir, "__init__.py")
		if err := os.WriteFile(initPath, []byte("# Auto-generated\n"), 0644); err != nil {
			return fmt.Errorf("failed to create __init__.py: %w", err)
		}

		var buf strings.Builder
		buf.WriteString("# CODE GENERATED - DO NOT EDIT\n\n")

		for _, c := range groupClients {
			fmt.Fprintf(&buf, "from .%s.rest import TypedHttpClient as %sTypedHttpClient\n",
				c.PackageName, strings.ToUpper(c.PackageName[:1])+c.PackageName[1:])
		}

		buf.WriteString("\n\nclass RestClients:\n")
		buf.WriteString("    \"\"\"Aggregates all per-type HTTP clients for this group.\"\"\"\n\n")
		buf.WriteString("    def __init__(self,\n")
		for _, c := range groupClients {
			accessor := strings.ToUpper(c.PackageName[:1]) + c.PackageName[1:]
			fmt.Fprintf(&buf, "        %s: %sTypedHttpClient,\n", c.PackageName, accessor)
		}
		buf.WriteString("    ):\n")
		for _, c := range groupClients {
			fmt.Fprintf(&buf, "        self.%s = %s\n", c.PackageName, c.PackageName)
		}
		buf.WriteString("\n")

		if err := os.WriteFile(filepath.Join(groupDir, "rests.py"), []byte(buf.String()), 0644); err != nil {
			return fmt.Errorf("failed to write rests.py for %s: %w", group, err)
		}
	}

	// __init__.py for rest/internal
	internalDir := filepath.Join(config.OutputDir, "rest", "internal")
	if err := os.MkdirAll(internalDir, 0755); err != nil {
		return fmt.Errorf("failed to create rest/internal: %w", err)
	}
	initPath := filepath.Join(internalDir, "__init__.py")
	if err := os.WriteFile(initPath, []byte("# Auto-generated\n"), 0644); err != nil {
		return fmt.Errorf("failed to create __init__.py: %w", err)
	}

	return nil
}

// generatePyRestCrudRoot generates rest/rest.py
func generatePyRestCrudRoot(config *tactype.Config, clients []generatedPyRestCrudClient) error {
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

	// __init__.py for rest
	initPath := filepath.Join(restDir, "__init__.py")
	if err := os.WriteFile(initPath, []byte("# Auto-generated\n"), 0644); err != nil {
		return fmt.Errorf("failed to create __init__.py: %w", err)
	}

	var buf strings.Builder
	buf.WriteString("# CODE GENERATED - DO NOT EDIT\n\n")

	for _, group := range groupOrder {
		accessor := strings.ToUpper(group[:1]) + group[1:]
		fmt.Fprintf(&buf, "from .internal.%s.rests import RestClients as %sRestClients\n", group, accessor)
	}

	buf.WriteString("\n\nclass RestClients:\n")
	buf.WriteString("    \"\"\"Aggregates all per-type HTTP clients.\"\"\"\n\n")
	buf.WriteString("    def __init__(self,\n")
	for _, group := range groupOrder {
		accessor := strings.ToUpper(group[:1]) + group[1:]
		fmt.Fprintf(&buf, "        %s: %sRestClients,\n", group, accessor)
	}
	buf.WriteString("    ):\n")
	for _, group := range groupOrder {
		fmt.Fprintf(&buf, "        self.%s = %s\n", group, group)
	}
	buf.WriteString("\n")

	if err := os.WriteFile(filepath.Join(restDir, "rest.py"), []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write rest.py: %w", err)
	}

	zap.L().Info("Generated root RestClients (Python)", zap.Int("groups", len(groupOrder)))
	return nil
}

func pyRestCrudPackagePath(pf model.ProtoFile) (pkgName, parentDir string) {
	parentDir = filepath.Dir(pf.RelPath)
	pkgName = pf.Package
	if strings.HasPrefix(pkgName, "services.") {
		pkgName = pkgName[len("services."):]
	}
	pkgName = pyModuleName(pkgName)
	if parentDir == "." {
		parentDir = "sys"
	} else {
		parentDir = pyPath(parentDir)
	}
	return pkgName, parentDir
}

// resolveRestCrudPackagesPy returns the proto packages that should get per-type HTTP clients.
func resolveRestCrudPackagesPy(config *tactype.Config, allParsedFiles []model.ProtoFile) []model.ProtoFile {
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
