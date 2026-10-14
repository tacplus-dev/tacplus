// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gitlab.com/seabeam/schematic/tacplus/pkg/internal/common/strutil"
)

// extractJavaOuterClassName reads a proto file and returns the java_outer_classname option value.
// Returns empty string if the option is not set or the file cannot be read.
func extractJavaOuterClassName(protoPath string) string {
	content, err := os.ReadFile(protoPath)
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`(?m)^[^\n/]*?option\s+java_outer_classname\s*=\s*"([^"]+)"\s*;`)
	matches := re.FindSubmatch(content)
	if len(matches) > 1 {
		return string(matches[1])
	}
	return ""
}

// extractJavaPackageName reads a proto file and returns the java_package option value.
// Returns empty string if the option is not set or the file cannot be read.
func extractJavaPackageName(protoPath string) string {
	content, err := os.ReadFile(protoPath)
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`(?m)^[^\n/]*?option\s+java_package\s*=\s*"([^"]+)"\s*;`)
	matches := re.FindSubmatch(content)
	if len(matches) > 1 {
		return string(matches[1])
	}
	return ""
}

// detectOuterClassName finds the actual outer class name from generated Java file
// Protoc may use *OuterClass suffix for naming conflicts
func detectOuterClassName(baseDir, subdir, protoRelPath string) string {
	// First, check if the proto file has an explicit java_outer_classname option.
	// This handles service subdirectory protos where filenames like "types.proto"
	// don't match the actual outer class name (e.g., "Baseball").
	if name := extractJavaOuterClassName(protoRelPath); name != "" {
		return name
	}

	// Default: convert proto filename from snake_case to PascalCase
	// Protoc converts snake_case filenames to PascalCase for Java outer class names
	// e.g., "baseball_state.proto" -> "BaseballState", "user.proto" -> "User"
	protoFileName := filepath.Base(protoRelPath)
	defaultName := strings.TrimSuffix(protoFileName, ".proto")
	defaultName = strutil.ToCamelCase(defaultName)

	// Convert baseDir from test to main (protobuf files are in src/main/java)
	mainBaseDir := strings.Replace(baseDir, "/test/java", "/main/java", 1)

	// Try to find generated Java file in both main and test directories
	// Look for both Name.java and NameOuterClass.java
	baseDirs := []string{mainBaseDir, baseDir}
	for _, dir := range baseDirs {
		patterns := []string{
			filepath.Join(dir, subdir, defaultName+"OuterClass.java"),
			filepath.Join(dir, subdir, defaultName+".java"),
		}

		for _, pattern := range patterns {
			if _, err := os.Stat(pattern); err == nil {
				// File exists, extract class name from filename
				base := filepath.Base(pattern)
				return strings.TrimSuffix(base, ".java")
			}
		}
	}

	// Fallback: check if proto file has naming conflict
	// Protoc adds "OuterClass" suffix when there's a message/enum with same name as file
	if hasNamingConflict(protoRelPath, defaultName) {
		return defaultName + "OuterClass"
	}

	return defaultName
}

// hasNamingConflict checks if a proto file contains a message, enum, or service with the same
// name as the file. This determines whether protoc will add "OuterClass" suffix to the generated
// Java class (protoc considers service names as conflicts too, not just message/enum).
func hasNamingConflict(protoPath, className string) bool {
	content, err := os.ReadFile(protoPath)
	if err != nil {
		// Can't read file, assume OuterClass for safety
		return true
	}

	// Look for message, enum, or service declarations with matching name.
	// Matches: "message ClassName {", "enum ClassName {", or "service ClassName {"
	qn := regexp.QuoteMeta(className)
	for _, kw := range []string{"message", "enum", "service"} {
		p := regexp.MustCompile(`(?m)^\s*` + kw + `\s+` + qn + `\s*\{`)
		if p.Match(content) {
			return true
		}
	}
	return false
}
