// Copyright (c) 2026 Second Star Cavalier LLC

package types

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.uber.org/zap"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/model"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/tactype"
)

// javaRestCrudMessageEntry represents a single message type in a per-type HTTP client.
type javaRestCrudMessageEntry struct {
	Name      string // PascalCase: "Jwks"
	FieldName string // camelCase: "jwks"
}

// generatedJavaRestCrudClient tracks info about a generated per-type REST client for aggregators.
type generatedJavaRestCrudClient struct {
	PackageName string // e.g. "jwk"
	ParentDir   string // e.g. "sys"
	FullPackage string // e.g. "com.example.rest.internal.chassis.jwk"
}

// generateJavaRestCrudClients generates per-type HTTP clients for each proto package (Java).
func generateJavaRestCrudClients(config *tactype.Config, packages []model.ProtoFile) error {
	var generated []generatedJavaRestCrudClient

	for _, pf := range packages {
		if len(pf.Messages) == 0 {
			continue
		}

		pkgName, parentDir := javaRestCrudPackagePath(pf)

		var messages []javaRestCrudMessageEntry
		for _, msg := range pf.Messages {
			if isGeneratedCrudType(msg.Name) {
				continue
			}
			messages = append(messages, javaRestCrudMessageEntry{
				Name:      msg.Name,
				FieldName: strings.ToLower(msg.Name[:1]) + msg.Name[1:],
			})
		}

		// Compute the types Java package
		typesPackage := javaPackageForProtoPackage(config.JavaPackageBase, pkgName, parentDir)

		fullPackage := fmt.Sprintf("%s.rest.internal.%s.%s", config.JavaPackageBase, parentDir, pkgName)
		packagePath := strings.ReplaceAll(fullPackage, ".", string(filepath.Separator))
		outputDir := filepath.Join(config.OutputDir, "src", "main", "java", packagePath)
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return fmt.Errorf("failed to create REST client output directory: %w", err)
		}

		// Resolve outer class name
		typesSrcDir := filepath.Join(config.OutputDir, "src", "main", "java")
		typesPath := filepath.Join(typesSrcDir, strings.ReplaceAll(typesPackage, ".", string(filepath.Separator)))
		outerClass := getOuterClassNameForWrapper(typesPath, filepath.Join(parentDir, pkgName+".proto"))

		// HTTPClient interface — must be in its own file (Java public type rule)
		var ifaceBuf strings.Builder
		ifaceBuf.WriteString("// CODE GENERATED - DO NOT EDIT\n\n")
		fmt.Fprintf(&ifaceBuf, "package %s;\n\n", fullPackage)
		ifaceBuf.WriteString("/** Generic HTTP client interface for type-safe operations. */\n")
		ifaceBuf.WriteString("public interface HTTPClient<T extends com.google.protobuf.MessageLite> {\n")
		ifaceBuf.WriteString("    T doGet(String url, com.google.protobuf.Parser<T> parser) throws Exception;\n")
		ifaceBuf.WriteString("    T doPost(String url, byte[] data, com.google.protobuf.Parser<T> parser) throws Exception;\n")
		ifaceBuf.WriteString("    void doMutation(String method, String url, byte[] data) throws Exception;\n")
		ifaceBuf.WriteString("}\n")
		ifacePath := filepath.Join(outputDir, "HTTPClient.java")
		if err := os.WriteFile(ifacePath, []byte(ifaceBuf.String()), 0644); err != nil {
			return fmt.Errorf("failed to write HTTPClient interface: %w", err)
		}

		// TypedHttpClient class
		var buf strings.Builder
		buf.WriteString("// CODE GENERATED - DO NOT EDIT\n\n")
		fmt.Fprintf(&buf, "package %s;\n\n", fullPackage)

		fmt.Fprintf(&buf, "import %s.%s.*;\n\n", typesPackage, outerClass)

		buf.WriteString("/** TypedHttpClient provides type-safe HTTP methods for all message types in this package. */\n")
		buf.WriteString("public class TypedHttpClient {\n\n")

		for _, msg := range messages {
			fmt.Fprintf(&buf, "    private final HTTPClient<%s> %sClient;\n", msg.Name, msg.FieldName)
		}

		buf.WriteString("\n    public TypedHttpClient(\n")
		for i, msg := range messages {
			comma := ","
			if i == len(messages)-1 {
				comma = ""
			}
			fmt.Fprintf(&buf, "            HTTPClient<%s> %sClient%s\n", msg.Name, msg.FieldName, comma)
		}
		buf.WriteString("    ) {\n")
		for _, msg := range messages {
			fmt.Fprintf(&buf, "        this.%sClient = %sClient;\n", msg.FieldName, msg.FieldName)
		}
		buf.WriteString("    }\n")

		for _, msg := range messages {
			// Get
			fmt.Fprintf(&buf, "\n    /** Retrieves a %s from the specified URL. */\n", msg.Name)
			fmt.Fprintf(&buf, "    public %s get%s(String url) throws Exception {\n", msg.Name, msg.Name)
			fmt.Fprintf(&buf, "        return %sClient.doGet(url, %s.parser());\n", msg.FieldName, msg.Name)
			buf.WriteString("    }\n")

			// Create
			fmt.Fprintf(&buf, "\n    /** Creates a new %s at the specified URL. */\n", msg.Name)
			fmt.Fprintf(&buf, "    public %s create%s(String url, %s req) throws Exception {\n", msg.Name, msg.Name, msg.Name)
			fmt.Fprintf(&buf, "        return %sClient.doPost(url, req.toByteArray(), %s.parser());\n", msg.FieldName, msg.Name)
			buf.WriteString("    }\n")

			// Update
			fmt.Fprintf(&buf, "\n    /** Updates an existing %s at the specified URL. */\n", msg.Name)
			fmt.Fprintf(&buf, "    public void update%s(String url, %s req) throws Exception {\n", msg.Name, msg.Name)
			fmt.Fprintf(&buf, "        %sClient.doMutation(\"PUT\", url, req.toByteArray());\n", msg.FieldName)
			buf.WriteString("    }\n")

			// Delete
			fmt.Fprintf(&buf, "\n    /** Deletes a %s at the specified URL. */\n", msg.Name)
			fmt.Fprintf(&buf, "    public void delete%s(String url) throws Exception {\n", msg.Name)
			fmt.Fprintf(&buf, "        %sClient.doMutation(\"DELETE\", url, null);\n", msg.FieldName)
			buf.WriteString("    }\n")
		}
		buf.WriteString("}\n")

		outputPath := filepath.Join(outputDir, "TypedHttpClient.java")
		if err := os.WriteFile(outputPath, []byte(buf.String()), 0644); err != nil {
			return fmt.Errorf("failed to write REST client: %w", err)
		}

		generated = append(generated, generatedJavaRestCrudClient{
			PackageName: pkgName,
			ParentDir:   parentDir,
			FullPackage: fullPackage,
		})

		zap.L().Info("Generated per-type REST client (Java)",
			zap.String("package", pkgName),
			zap.Int("messages", len(messages)))
	}

	// Generate group-level aggregators and root
	if len(generated) > 0 {
		if err := generateJavaRestCrudGroups(config, generated); err != nil {
			return err
		}
		if err := generateJavaRestCrudRoot(config, generated); err != nil {
			return err
		}
	}

	return nil
}

// generateJavaRestCrudGroups generates rest/internal/{group}/RestClients.java
func generateJavaRestCrudGroups(config *tactype.Config, clients []generatedJavaRestCrudClient) error {
	groups := make(map[string][]generatedJavaRestCrudClient)
	for _, c := range clients {
		groups[c.ParentDir] = append(groups[c.ParentDir], c)
	}

	for group, groupClients := range groups {
		fullPackage := fmt.Sprintf("%s.rest.internal.%s", config.JavaPackageBase, group)
		packagePath := strings.ReplaceAll(fullPackage, ".", string(filepath.Separator))
		outputDir := filepath.Join(config.OutputDir, "src", "main", "java", packagePath)
		if err := os.MkdirAll(outputDir, 0755); err != nil {
			return fmt.Errorf("failed to create group directory: %w", err)
		}

		// Build map of package → fully-qualified TypedHttpClient name.
		// Java doesn't support import aliases, and multiple clients in the same group
		// share the class name "TypedHttpClient". Use FQN everywhere.
		clientFQN := make(map[string]string)
		for _, c := range groupClients {
			clientFQN[c.PackageName] = c.FullPackage + ".TypedHttpClient"
		}

		var buf strings.Builder
		buf.WriteString("// CODE GENERATED - DO NOT EDIT\n\n")
		fmt.Fprintf(&buf, "package %s;\n\n", fullPackage)

		buf.WriteString("/** RestClients aggregates all per-type HTTP clients for this group. */\n")
		buf.WriteString("public class RestClients {\n\n")

		for _, c := range groupClients {
			fmt.Fprintf(&buf, "    private final %s %s;\n", clientFQN[c.PackageName], c.PackageName)
		}

		buf.WriteString("\n    public RestClients(\n")
		for i, c := range groupClients {
			comma := ","
			if i == len(groupClients)-1 {
				comma = ""
			}
			fmt.Fprintf(&buf, "            %s %s%s\n", clientFQN[c.PackageName], c.PackageName, comma)
		}
		buf.WriteString("    ) {\n")
		for _, c := range groupClients {
			fmt.Fprintf(&buf, "        this.%s = %s;\n", c.PackageName, c.PackageName)
		}
		buf.WriteString("    }\n")

		for _, c := range groupClients {
			accessor := strings.ToUpper(c.PackageName[:1]) + c.PackageName[1:]
			fmt.Fprintf(&buf, "\n    /** Returns the TypedHttpClient for the %s package. */\n", c.PackageName)
			fmt.Fprintf(&buf, "    public %s %s() {\n", clientFQN[c.PackageName], accessor)
			fmt.Fprintf(&buf, "        return %s;\n", c.PackageName)
			buf.WriteString("    }\n")
		}
		buf.WriteString("}\n")

		if err := os.WriteFile(filepath.Join(outputDir, "RestClients.java"), []byte(buf.String()), 0644); err != nil {
			return fmt.Errorf("failed to write RestClients.java for %s: %w", group, err)
		}
	}
	return nil
}

// generateJavaRestCrudRoot generates rest/RestClients.java
func generateJavaRestCrudRoot(config *tactype.Config, clients []generatedJavaRestCrudClient) error {
	groups := make(map[string]bool)
	var groupOrder []string
	for _, c := range clients {
		if !groups[c.ParentDir] {
			groups[c.ParentDir] = true
			groupOrder = append(groupOrder, c.ParentDir)
		}
	}

	fullPackage := fmt.Sprintf("%s.rest", config.JavaPackageBase)
	packagePath := strings.ReplaceAll(fullPackage, ".", string(filepath.Separator))
	outputDir := filepath.Join(config.OutputDir, "src", "main", "java", packagePath)
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create rest directory: %w", err)
	}

	// Build map of group → fully-qualified RestClients class name.
	// Java doesn't support import aliases, and all groups share the class name
	// "RestClients", which also conflicts with *this* class. Use FQN everywhere.
	groupFQN := make(map[string]string)
	for _, group := range groupOrder {
		groupFQN[group] = fmt.Sprintf("%s.rest.internal.%s.RestClients", config.JavaPackageBase, group)
	}

	var buf strings.Builder
	buf.WriteString("// CODE GENERATED - DO NOT EDIT\n\n")
	fmt.Fprintf(&buf, "package %s;\n\n", fullPackage)

	buf.WriteString("/** RestClients aggregates all per-type HTTP clients. */\n")
	buf.WriteString("public class RestClients {\n\n")

	for _, group := range groupOrder {
		fmt.Fprintf(&buf, "    private final %s %s;\n", groupFQN[group], group)
	}

	buf.WriteString("\n    public RestClients(\n")
	for i, group := range groupOrder {
		comma := ","
		if i == len(groupOrder)-1 {
			comma = ""
		}
		fmt.Fprintf(&buf, "            %s %s%s\n", groupFQN[group], group, comma)
	}
	buf.WriteString("    ) {\n")
	for _, group := range groupOrder {
		fmt.Fprintf(&buf, "        this.%s = %s;\n", group, group)
	}
	buf.WriteString("    }\n")

	for _, group := range groupOrder {
		accessor := strings.ToUpper(group[:1]) + group[1:]
		fmt.Fprintf(&buf, "\n    /** Returns the %s REST clients group. */\n", group)
		fmt.Fprintf(&buf, "    public %s %s() {\n", groupFQN[group], accessor)
		fmt.Fprintf(&buf, "        return %s;\n", group)
		buf.WriteString("    }\n")
	}
	buf.WriteString("}\n")

	if err := os.WriteFile(filepath.Join(outputDir, "RestClients.java"), []byte(buf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write rest/RestClients.java: %w", err)
	}

	zap.L().Info("Generated root RestClients (Java)", zap.Int("groups", len(groupOrder)))
	return nil
}

func javaRestCrudPackagePath(pf model.ProtoFile) (pkgName, parentDir string) {
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

// resolveRestCrudPackagesJava returns the proto packages that should get per-type HTTP clients.
func resolveRestCrudPackagesJava(config *tactype.Config, allParsedFiles []model.ProtoFile) []model.ProtoFile {
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

// isGeneratedCrudType returns true for message names that are generated by
// tacplus state CRUD code generation (e.g. *GetRequest, *ValueResponse, MapEmpty).
// These should not appear in the TypedHttpClient because they are internal
// RPC wrapper types, not user-defined proto messages.
func isGeneratedCrudType(name string) bool {
	suffixes := []string{
		"GetRequest", "PutRequest", "DeleteRequest",
		"GetAllRequest", "KeysRequest", "SizeRequest", "ClearRequest",
		"ValueResponse", "EntriesResponse", "KeysResponse", "SizeResponse",
		"QueryRequest", "QueryResponse",
	}
	for _, s := range suffixes {
		if strings.HasSuffix(name, s) {
			return true
		}
	}
	exact := []string{"MapEmpty", "CollectionQuery", "QueryValue", "Predicate", "PredicateGroup", "SortField"}
	for _, e := range exact {
		if name == e {
			return true
		}
	}
	return false
}
