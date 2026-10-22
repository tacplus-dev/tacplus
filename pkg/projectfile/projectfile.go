// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package projectfile

import (
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/BurntSushi/toml"
	"golang.org/x/mod/modfile"
)

// ProjectType represents a detected project type.
type ProjectType int

const (
	ProjectUnknown    ProjectType = iota
	ProjectJava                   // Java (Maven or Gradle)
	ProjectGo                     // go.mod
	ProjectTypeScript             // package.json
	ProjectPython                 // pyproject.toml
	ProjectRust                   // Cargo.toml
	ProjectSwift                  // Package.swift
	ProjectCSharp                 // *.csproj
)

func (p ProjectType) String() string {
	switch p {
	case ProjectJava:
		return "Java"
	case ProjectGo:
		return "Go"
	case ProjectTypeScript:
		return "TypeScript"
	case ProjectPython:
		return "Python"
	case ProjectRust:
		return "Rust"
	case ProjectSwift:
		return "Swift"
	case ProjectCSharp:
		return "C#"
	default:
		return "Unknown"
	}
}

// Language returns the tacplus language name for this project type.
func (p ProjectType) Language() string {
	switch p {
	case ProjectJava:
		return "java"
	case ProjectGo:
		return "go"
	case ProjectTypeScript:
		return "typescript"
	case ProjectPython:
		return "python"
	case ProjectRust:
		return "rust"
	case ProjectSwift:
		return "swift"
	case ProjectCSharp:
		return "csharp"
	default:
		return ""
	}
}

// ReadGoModulePath reads the module path from go.mod in dir.
func ReadGoModulePath(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return ""
	}
	f, err := modfile.Parse("go.mod", data, nil)
	if err != nil {
		return ""
	}
	if f.Module != nil {
		return f.Module.Mod.Path
	}
	return ""
}

// readMavenGroupID reads the groupId from pom.xml in dir.
func readMavenGroupID(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "pom.xml"))
	if err != nil {
		return ""
	}
	var proj struct {
		XMLName xml.Name `xml:"project"`
		GroupID string   `xml:"groupId"`
	}
	if err := xml.Unmarshal(data, &proj); err != nil {
		return ""
	}
	return proj.GroupID
}

// readGradleGroup reads the group from build.gradle(.kts) in dir.
func readGradleGroup(dir string) string {
	for _, name := range []string{"build.gradle.kts", "build.gradle"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		re := regexp.MustCompile(`group\s*=?\s*["']([^"']+)["']`)
		m := re.FindStringSubmatch(string(data))
		if len(m) > 1 {
			return m[1]
		}
	}
	return ""
}

// readPackageJSONName reads the name from package.json in dir.
func readPackageJSONName(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return ""
	}
	var pkg struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return ""
	}
	return pkg.Name
}

// readPyprojectName reads the project name from pyproject.toml in dir.
func readPyprojectName(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "pyproject.toml"))
	if err != nil {
		return ""
	}
	var cfg struct {
		Project struct {
			Name string `toml:"name"`
		} `toml:"project"`
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	return cfg.Project.Name
}

// readCargoName reads the package name from Cargo.toml in dir.
func readCargoName(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "Cargo.toml"))
	if err != nil {
		return ""
	}
	var cfg struct {
		Package struct {
			Name string `toml:"name"`
		} `toml:"package"`
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return ""
	}
	return cfg.Package.Name
}

// readSwiftPackageName reads the package name from Package.swift in dir.
func readSwiftPackageName(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, "Package.swift"))
	if err != nil {
		return ""
	}
	re := regexp.MustCompile(`name\s*:\s*"([^"]+)"`)
	m := re.FindSubmatch(data)
	if len(m) > 1 {
		return string(m[1])
	}
	return ""
}

// DerivePackage derives the tacplus package name from a project file in dir.
func DerivePackage(projectType ProjectType, dir string) string {
	switch projectType {
	case ProjectGo:
		if mod := ReadGoModulePath(dir); mod != "" {
			return mod + "/tacplus"
		}
	case ProjectJava:
		if gid := readMavenGroupID(dir); gid != "" {
			return gid + ".tacplus"
		}
		if g := readGradleGroup(dir); g != "" {
			return g + ".tacplus"
		}
	case ProjectTypeScript:
		if name := readPackageJSONName(dir); name != "" {
			return name + "-tacplus"
		}
	case ProjectPython:
		if name := readPyprojectName(dir); name != "" {
			return name + "-tacplus"
		}
	case ProjectRust:
		if name := readCargoName(dir); name != "" {
			return name + "-tacplus"
		}
	case ProjectSwift:
		if name := readSwiftPackageName(dir); name != "" {
			return swiftModuleName(name) + "Tacplus"
		}
	}
	return "tacplus"
}

func swiftModuleName(s string) string {
	parts := regexp.MustCompile(`[^A-Za-z0-9]+`).Split(s, -1)
	for i := range parts {
		if parts[i] == "" {
			continue
		}
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}
