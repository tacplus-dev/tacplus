// Copyright (c) 2026 Second Star Cavalier LLC

package metadata

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/header"
	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/consts"
)

// GenerateMetaData writes a metadata file containing the PROJECT_NAME constant
// (derived from the last segment of the canonical package name in tacplus.yaml).
//
// For most languages, the file is written to outputDir directly.
// For Java, pass the Java source base directory as outputDir and the package
// subdirectory (e.g., "com/gitlab/.../eventbus") as javaPackagePath.
func GenerateMetaData(language, selfPackage, outputDir, javaPackagePath string) error {
	if selfPackage == "" {
		return nil
	}

	var filename, content string

	switch language {
	case consts.LangRust:
		filename = "meta_data.rs"
		content = fmt.Sprintf(
			"%s"+
				"/// The proto package name of this service, derived from tacplus.yaml.\n"+
				"pub const PROJECT_NAME: &str = %q;\n",
			header.SimpleHeader(language), selfPackage)

	case consts.LangJava:
		filename = "MetaData.java"
		packageDecl := ""
		if javaPackagePath != "" {
			// javaPackagePath is a directory path (e.g., "com/gitlab/.../eventbus").
			// Convert to dot-separated for the package declaration.
			dotPath := filepath.ToSlash(javaPackagePath)
			dotPath = dotPath[0:] // ensure forward slashes
			packageDecl = fmt.Sprintf("package %s;\n\n", strings.ReplaceAll(dotPath, "/", "."))
			// Java files go into the package subdirectory.
			outputDir = filepath.Join(outputDir, javaPackagePath)
		}
		content = fmt.Sprintf(
			"%s"+
				"%s"+
				"public final class MetaData {\n"+
				"    private MetaData() {}\n"+
				"\n"+
				"    /** The proto package name of this service, derived from tacplus.yaml. */\n"+
				"    public static final String PROJECT_NAME = %q;\n"+
				"}\n",
			header.SimpleHeader(language), packageDecl, selfPackage)

	case consts.LangGo:
		filename = "meta_data.go"
		content = fmt.Sprintf(
			"%s"+
				"package types\n"+
				"\n"+
				"// ProjectName is the proto package name of this service, derived from tacplus.yaml.\n"+
				"const ProjectName = %q\n",
			header.SimpleHeader(language), selfPackage)

	case consts.LangTypeScript:
		filename = "metaData.ts"
		content = fmt.Sprintf(
			"%s"+
				"/** The proto package name of this service, derived from tacplus.yaml. */\n"+
				"export const PROJECT_NAME: string = %q;\n",
			header.SimpleHeader(language), selfPackage)

	case consts.LangPython:
		filename = "meta_data.py"
		content = fmt.Sprintf(
			"%s"+
				"\"\"\"The proto package name of this service, derived from tacplus.yaml.\"\"\"\n"+
				"PROJECT_NAME: str = %q\n",
			header.SimpleHeader(language), selfPackage)

	case consts.LangSwift:
		filename = "MetaData.swift"
		content = fmt.Sprintf(
			"%s"+
				"/// Metadata for this tacplus-generated Swift package.\n"+
				"public enum TacplusMetaData {\n"+
				"    /// The proto package name of this service, derived from tacplus.yaml.\n"+
				"    public static let projectName = %q\n"+
				"}\n",
			header.SimpleHeader(language), selfPackage)

	case consts.LangCSharp:
		filename = "MetaData.cs"
		content = fmt.Sprintf(
			"%s"+
				"/// <summary>Metadata for this tacplus-generated C# package.</summary>\n"+
				"public static class TacplusMetaData\n"+
				"{\n"+
				"    /// <summary>The proto package name of this service, derived from tacplus.yaml.</summary>\n"+
				"    public const string ProjectName = %q;\n"+
				"}\n",
			header.SimpleHeader(language), selfPackage)

	default:
		return fmt.Errorf("unsupported language for metadata generation: %s", language)
	}

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return fmt.Errorf("failed to create output directory for metadata: %w", err)
	}

	outPath := filepath.Join(outputDir, filename)
	return os.WriteFile(outPath, []byte(content), 0644)
}
