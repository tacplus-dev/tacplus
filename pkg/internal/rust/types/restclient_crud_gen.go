// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/model"
	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

// rustRestCrudMessageEntry represents a single message type in a per-type HTTP client.
type rustRestCrudMessageEntry struct {
	Name      string // PascalCase: "Jwks"
	SnakeName string // snake_case: "jwks"
}

// generatedRustRestCrudClient tracks info about a generated per-type REST client for aggregators.
type generatedRustRestCrudClient struct {
	PackageName string // e.g. "jwk"
	ParentDir   string // e.g. "sys"
}

// generateRustRestCrudClients generates per-type HTTP clients for each proto package (Rust).
func generateRustRestCrudClients(config *tactype.Config, packages []model.ProtoFile) error {
	var generated []generatedRustRestCrudClient

	for _, pf := range packages {
		if len(pf.Messages) == 0 {
			continue
		}

		pkgName, parentDir := rustRestCrudPackagePath(pf)

		var messages []rustRestCrudMessageEntry
		for _, msg := range pf.Messages {
			prostName := toProstTypeName(msg.Name)
			messages = append(messages, rustRestCrudMessageEntry{
				Name:      prostName,
				SnakeName: strutil.ToSnakeCase(prostName),
			})
		}

		// Crate path to types module
		typesCratePath := fmt.Sprintf("crate::types::%s::%s",
			strings.ReplaceAll(strings.ReplaceAll(parentDir, "-", "_"), string(filepath.Separator), "::"), pkgName)

		outputDir := filepath.Join(config.OutputDir, "rest", "internal", parentDir, pkgName)
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return fmt.Errorf("failed to create REST client output directory: %w", err)
		}

		var buf strings.Builder
		buf.WriteString("// CODE GENERATED - DO NOT EDIT\n\n")
		buf.WriteString("#![allow(dead_code)]\n\n")
		buf.WriteString("use async_trait::async_trait;\n\n")
		fmt.Fprintf(&buf, "use %s::*;\n\n", typesCratePath)

		// HttpClient trait
		buf.WriteString("/// Generic HTTP client trait for type-safe operations.\n")
		buf.WriteString("#[async_trait]\n")
		buf.WriteString("pub trait HttpClient<T: prost::Message + Default + Send + Sync>: Send + Sync {\n")
		buf.WriteString("    async fn do_get(&self, url: &str) -> Result<T, Box<dyn std::error::Error>>;\n")
		buf.WriteString("    async fn do_post(&self, url: &str, data: &[u8]) -> Result<T, Box<dyn std::error::Error>>;\n")
		buf.WriteString("    async fn do_mutation(&self, method: &str, url: &str, data: Option<&[u8]>) -> Result<(), Box<dyn std::error::Error>>;\n")
		buf.WriteString("}\n\n")

		// TypedHttpClient struct
		buf.WriteString("/// TypedHttpClient provides type-safe HTTP methods for all message types in this package.\n")
		buf.WriteString("pub struct TypedHttpClient {\n")
		for _, msg := range messages {
			fmt.Fprintf(&buf, "    %s_client: Box<dyn HttpClient<%s>>,\n", msg.SnakeName, msg.Name)
		}
		buf.WriteString("}\n\n")

		buf.WriteString("impl TypedHttpClient {\n")
		buf.WriteString("    /// Creates a new typed HTTP client.\n")
		buf.WriteString("    pub fn new(\n")
		for _, msg := range messages {
			fmt.Fprintf(&buf, "        %s_client: Box<dyn HttpClient<%s>>,\n", msg.SnakeName, msg.Name)
		}
		buf.WriteString("    ) -> Self {\n")
		buf.WriteString("        Self {\n")
		for _, msg := range messages {
			fmt.Fprintf(&buf, "            %s_client,\n", msg.SnakeName)
		}
		buf.WriteString("        }\n")
		buf.WriteString("    }\n")

		for _, msg := range messages {
			// Get
			fmt.Fprintf(&buf, "\n    /// Retrieves a %s from the specified URL.\n", msg.Name)
			fmt.Fprintf(&buf, "    pub async fn get_%s(&self, url: &str) -> Result<%s, Box<dyn std::error::Error>> {\n", msg.SnakeName, msg.Name)
			fmt.Fprintf(&buf, "        self.%s_client.do_get(url).await\n", msg.SnakeName)
			buf.WriteString("    }\n")

			// Create
			fmt.Fprintf(&buf, "\n    /// Creates a new %s at the specified URL.\n", msg.Name)
			fmt.Fprintf(&buf, "    pub async fn create_%s(&self, url: &str, req: &%s) -> Result<%s, Box<dyn std::error::Error>> {\n", msg.SnakeName, msg.Name, msg.Name)
			buf.WriteString("        use prost::Message;\n")
			fmt.Fprintf(&buf, "        self.%s_client.do_post(url, &req.encode_to_vec()).await\n", msg.SnakeName)
			buf.WriteString("    }\n")

			// Update
			fmt.Fprintf(&buf, "\n    /// Updates an existing %s at the specified URL.\n", msg.Name)
			fmt.Fprintf(&buf, "    pub async fn update_%s(&self, url: &str, req: &%s) -> Result<(), Box<dyn std::error::Error>> {\n", msg.SnakeName, msg.Name)
			buf.WriteString("        use prost::Message;\n")
			fmt.Fprintf(&buf, "        self.%s_client.do_mutation(\"PUT\", url, Some(&req.encode_to_vec())).await\n", msg.SnakeName)
			buf.WriteString("    }\n")

			// Delete
			fmt.Fprintf(&buf, "\n    /// Deletes a %s at the specified URL.\n", msg.Name)
			fmt.Fprintf(&buf, "    pub async fn delete_%s(&self, url: &str) -> Result<(), Box<dyn std::error::Error>> {\n", msg.SnakeName)
			fmt.Fprintf(&buf, "        self.%s_client.do_mutation(\"DELETE\", url, None).await\n", msg.SnakeName)
			buf.WriteString("    }\n")
		}
		buf.WriteString("}\n")

		outputPath := filepath.Join(outputDir, "rest.rs")
		if err := os.WriteFile(outputPath, []byte(buf.String()), 0644); err != nil {
			return fmt.Errorf("failed to write REST client: %w", err)
		}

		generated = append(generated, generatedRustRestCrudClient{
			PackageName: pkgName,
			ParentDir:   parentDir,
		})

		zap.L().Info("Generated per-type REST client (Rust)",
			zap.String("package", pkgName),
			zap.Int("messages", len(messages)))
	}

	// Generate group-level aggregators and root
	if len(generated) > 0 {
		if err := generateRustRestCrudGroups(config, generated); err != nil {
			return err
		}
		if err := generateRustRestCrudRoot(config, generated); err != nil {
			return err
		}
	}

	return nil
}

// generateRustRestCrudGroups generates rest/internal/{group}/rests.rs
func generateRustRestCrudGroups(config *tactype.Config, clients []generatedRustRestCrudClient) error {
	groups := make(map[string][]generatedRustRestCrudClient)
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
			fmt.Fprintf(&buf, "pub mod %s;\n", c.PackageName)
		}

		buf.WriteString("\nuse super::*;\n\n")
		buf.WriteString("/// RestClients aggregates all per-type HTTP clients for this group.\n")
		buf.WriteString("pub struct RestClients {\n")
		for _, c := range groupClients {
			fmt.Fprintf(&buf, "    pub %s: %s::rest::TypedHttpClient,\n", c.PackageName, c.PackageName)
		}
		buf.WriteString("}\n")

		if err := os.WriteFile(filepath.Join(groupDir, "mod.rs"), []byte(buf.String()), 0644); err != nil {
			return fmt.Errorf("failed to write mod.rs for %s: %w", group, err)
		}
	}
	return nil
}

// generateRustRestCrudRoot generates rest/mod.rs
func generateRustRestCrudRoot(config *tactype.Config, clients []generatedRustRestCrudClient) error {
	groups := make(map[string]bool)
	var groupOrder []string
	for _, c := range clients {
		if !groups[c.ParentDir] {
			groups[c.ParentDir] = true
			groupOrder = append(groupOrder, c.ParentDir)
		}
	}

	restDir := filepath.Join(config.OutputDir, "rest")
	internalDir := filepath.Join(restDir, "internal")
	if err := os.MkdirAll(internalDir, 0755); err != nil {
		return fmt.Errorf("failed to create rest/internal directory: %w", err)
	}

	// Write rest/internal/mod.rs — declares group submodules
	var internalBuf strings.Builder
	internalBuf.WriteString("// CODE GENERATED - DO NOT EDIT\n\n")
	for _, group := range groupOrder {
		fmt.Fprintf(&internalBuf, "pub mod %s;\n", group)
	}
	if err := os.WriteFile(filepath.Join(internalDir, "mod.rs"), []byte(internalBuf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write rest/internal/mod.rs: %w", err)
	}

	// Write rest/mod.rs — declares internal module and root RestClients
	var buf strings.Builder
	buf.WriteString("// CODE GENERATED - DO NOT EDIT\n\n")
	buf.WriteString("pub mod internal;\n\n")

	buf.WriteString("/// RestClients aggregates all per-type HTTP clients.\n")
	buf.WriteString("pub struct RestClients {\n")
	for _, group := range groupOrder {
		fmt.Fprintf(&buf, "    pub %s: internal::%s::RestClients,\n", group, group)
	}
	buf.WriteString("}\n")

	if err := os.WriteFile(filepath.Join(restDir, "mod.rs"), []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write rest/mod.rs: %w", err)
	}

	zap.L().Info("Generated root RestClients (Rust)", zap.Int("groups", len(groupOrder)))
	return nil
}

func rustRestCrudPackagePath(pf model.ProtoFile) (pkgName, parentDir string) {
	parentDir = filepath.Dir(pf.RelPath)
	pkgName = pf.Package
	if strings.HasPrefix(pkgName, "services.") {
		pkgName = pkgName[len("services."):]
	}
	if parentDir == "." {
		parentDir = "sys"
	}
	return pkgName, parentDir
}

// resolveRestCrudPackagesRust returns the proto packages that should get per-type HTTP clients.
func resolveRestCrudPackagesRust(config *tactype.Config, allParsedFiles []model.ProtoFile) []model.ProtoFile {
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
