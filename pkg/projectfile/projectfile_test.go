// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package projectfile

import (
	"os"
	"path/filepath"
	"testing"
)

// --- Language() tests ---

func TestLanguage_TableDriven(t *testing.T) {
	tests := []struct {
		projectType ProjectType
		wantLang    string
	}{
		{ProjectGo, "go"},
		{ProjectJava, "java"},
		{ProjectTypeScript, "typescript"},
		{ProjectPython, "python"},
		{ProjectRust, "rust"},
		{ProjectSwift, "swift"},
		{ProjectUnknown, ""},
	}

	for _, tt := range tests {
		t.Run(tt.projectType.String(), func(t *testing.T) {
			got := tt.projectType.Language()
			if got != tt.wantLang {
				t.Errorf("Language() = %q, want %q", got, tt.wantLang)
			}
		})
	}
}

// --- String() tests ---

func TestString_TableDriven(t *testing.T) {
	tests := []struct {
		projectType ProjectType
		wantStr     string
	}{
		{ProjectGo, "Go"},
		{ProjectJava, "Java"},
		{ProjectTypeScript, "TypeScript"},
		{ProjectPython, "Python"},
		{ProjectRust, "Rust"},
		{ProjectSwift, "Swift"},
		{ProjectUnknown, "Unknown"},
	}

	for _, tt := range tests {
		t.Run(tt.wantStr, func(t *testing.T) {
			got := tt.projectType.String()
			if got != tt.wantStr {
				t.Errorf("String() = %q, want %q", got, tt.wantStr)
			}
		})
	}
}

// --- ReadGoModulePath tests ---

func TestReadGoModulePath_Valid(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module github.com/example/myproject\n\ngo 1.21\n")

	got := ReadGoModulePath(dir)
	if got != "github.com/example/myproject" {
		t.Errorf("ReadGoModulePath() = %q, want %q", got, "github.com/example/myproject")
	}
}

func TestReadGoModulePath_NoGoMod(t *testing.T) {
	dir := t.TempDir()

	got := ReadGoModulePath(dir)
	if got != "" {
		t.Errorf("ReadGoModulePath() = %q, want empty string", got)
	}
}

func TestReadGoModulePath_InvalidGoMod(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "this is not a valid go.mod")

	got := ReadGoModulePath(dir)
	// modfile.Parse may or may not error on this; the function should return ""
	// if it can't extract a module path
	_ = got // just ensure no panic
}

func TestReadGoModulePath_EmptyGoMod(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "")

	got := ReadGoModulePath(dir)
	if got != "" {
		t.Errorf("ReadGoModulePath() = %q, want empty string", got)
	}
}

// --- readMavenGroupID tests ---

func Test_readMavenGroupID_Valid(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pom.xml", `<?xml version="1.0"?>
<project xmlns="http://maven.apache.org/POM/4.0.0">
  <groupId>com.example.myproject</groupId>
  <artifactId>mylib</artifactId>
</project>`)

	got := readMavenGroupID(dir)
	if got != "com.example.myproject" {
		t.Errorf("readMavenGroupID() = %q, want %q", got, "com.example.myproject")
	}
}

func Test_readMavenGroupID_NoPomXml(t *testing.T) {
	dir := t.TempDir()

	got := readMavenGroupID(dir)
	if got != "" {
		t.Errorf("readMavenGroupID() = %q, want empty string", got)
	}
}

func Test_readMavenGroupID_NoGroupId(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pom.xml", `<project><artifactId>mylib</artifactId></project>`)

	got := readMavenGroupID(dir)
	if got != "" {
		t.Errorf("readMavenGroupID() = %q, want empty string", got)
	}
}

// --- readGradleGroup tests ---

func Test_readGradleGroup_Kts(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "build.gradle.kts", `
plugins {
    java
}

group = "com.example.gradle"
version = "1.0"
`)

	got := readGradleGroup(dir)
	if got != "com.example.gradle" {
		t.Errorf("readGradleGroup() = %q, want %q", got, "com.example.gradle")
	}
}

func Test_readGradleGroup_Groovy(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "build.gradle", `
group = 'com.example.groovy'
version = '1.0'
`)

	got := readGradleGroup(dir)
	if got != "com.example.groovy" {
		t.Errorf("readGradleGroup() = %q, want %q", got, "com.example.groovy")
	}
}

func Test_readGradleGroup_NoFile(t *testing.T) {
	dir := t.TempDir()

	got := readGradleGroup(dir)
	if got != "" {
		t.Errorf("readGradleGroup() = %q, want empty string", got)
	}
}

func Test_readGradleGroup_PrefersKtsOverGroovy(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "build.gradle.kts", `group = "com.kts"`)
	writeFile(t, dir, "build.gradle", `group = 'com.groovy'`)

	got := readGradleGroup(dir)
	if got != "com.kts" {
		t.Errorf("readGradleGroup() = %q, want %q (kts should take priority)", got, "com.kts")
	}
}

// --- readPackageJSONName tests ---

func Test_readPackageJSONName_Valid(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"name": "@scope/mypackage", "version": "1.0.0"}`)

	got := readPackageJSONName(dir)
	if got != "@scope/mypackage" {
		t.Errorf("readPackageJSONName() = %q, want %q", got, "@scope/mypackage")
	}
}

func Test_readPackageJSONName_NoFile(t *testing.T) {
	dir := t.TempDir()

	got := readPackageJSONName(dir)
	if got != "" {
		t.Errorf("readPackageJSONName() = %q, want empty string", got)
	}
}

func Test_readPackageJSONName_NoName(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"version": "1.0.0"}`)

	got := readPackageJSONName(dir)
	if got != "" {
		t.Errorf("readPackageJSONName() = %q, want empty string", got)
	}
}

func Test_readPackageJSONName_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `not valid json`)

	got := readPackageJSONName(dir)
	if got != "" {
		t.Errorf("readPackageJSONName() = %q, want empty string", got)
	}
}

// --- readPyprojectName tests ---

func Test_readPyprojectName_Valid(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", `
[project]
name = "my-python-lib"
version = "0.1.0"
`)

	got := readPyprojectName(dir)
	if got != "my-python-lib" {
		t.Errorf("readPyprojectName() = %q, want %q", got, "my-python-lib")
	}
}

func Test_readPyprojectName_NoFile(t *testing.T) {
	dir := t.TempDir()

	got := readPyprojectName(dir)
	if got != "" {
		t.Errorf("readPyprojectName() = %q, want empty string", got)
	}
}

func Test_readPyprojectName_NoProjectSection(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", `
[build-system]
requires = ["setuptools"]
`)

	got := readPyprojectName(dir)
	if got != "" {
		t.Errorf("readPyprojectName() = %q, want empty string", got)
	}
}

// --- readCargoName tests ---

func Test_readCargoName_Valid(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Cargo.toml", `
[package]
name = "my-rust-crate"
version = "0.1.0"
edition = "2021"
`)

	got := readCargoName(dir)
	if got != "my-rust-crate" {
		t.Errorf("readCargoName() = %q, want %q", got, "my-rust-crate")
	}
}

func Test_readCargoName_NoFile(t *testing.T) {
	dir := t.TempDir()

	got := readCargoName(dir)
	if got != "" {
		t.Errorf("readCargoName() = %q, want empty string", got)
	}
}

func Test_readCargoName_WorkspaceOnly(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Cargo.toml", `
[workspace]
members = ["crate-a", "crate-b"]
`)

	got := readCargoName(dir)
	if got != "" {
		t.Errorf("readCargoName() = %q, want empty string for workspace-only Cargo.toml", got)
	}
}

// --- readSwiftPackageName tests ---

func Test_readSwiftPackageName_Valid(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Package.swift", `
// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "Menu Bar Client",
    products: []
)
`)

	got := readSwiftPackageName(dir)
	if got != "Menu Bar Client" {
		t.Errorf("readSwiftPackageName() = %q, want %q", got, "Menu Bar Client")
	}
}

func Test_readSwiftPackageName_NoFile(t *testing.T) {
	dir := t.TempDir()

	got := readSwiftPackageName(dir)
	if got != "" {
		t.Errorf("readSwiftPackageName() = %q, want empty string", got)
	}
}

// --- DerivePackage tests ---

func TestDerivePackage_Go(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "go.mod", "module github.com/example/myproject\n\ngo 1.21\n")

	got := DerivePackage(ProjectGo, dir)
	if got != "github.com/example/myproject/tacplus" {
		t.Errorf("DerivePackage(Go) = %q, want %q", got, "github.com/example/myproject/tacplus")
	}
}

func TestDerivePackage_JavaMaven(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pom.xml", `<project><groupId>com.example</groupId></project>`)

	got := DerivePackage(ProjectJava, dir)
	if got != "com.example.tacplus" {
		t.Errorf("DerivePackage(Java/Maven) = %q, want %q", got, "com.example.tacplus")
	}
}

func TestDerivePackage_JavaGradle(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "build.gradle.kts", `group = "com.gradle.example"`)

	got := DerivePackage(ProjectJava, dir)
	if got != "com.gradle.example.tacplus" {
		t.Errorf("DerivePackage(Java/Gradle) = %q, want %q", got, "com.gradle.example.tacplus")
	}
}

func TestDerivePackage_TypeScript(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "package.json", `{"name": "@scope/mylib"}`)

	got := DerivePackage(ProjectTypeScript, dir)
	if got != "@scope/mylib-tacplus" {
		t.Errorf("DerivePackage(TypeScript) = %q, want %q", got, "@scope/mylib-tacplus")
	}
}

func TestDerivePackage_Python(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "pyproject.toml", "[project]\nname = \"mylib\"\n")

	got := DerivePackage(ProjectPython, dir)
	if got != "mylib-tacplus" {
		t.Errorf("DerivePackage(Python) = %q, want %q", got, "mylib-tacplus")
	}
}

func TestDerivePackage_Rust(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Cargo.toml", "[package]\nname = \"mylib\"\n")

	got := DerivePackage(ProjectRust, dir)
	if got != "mylib-tacplus" {
		t.Errorf("DerivePackage(Rust) = %q, want %q", got, "mylib-tacplus")
	}
}

func TestDerivePackage_Swift(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "Package.swift", `let package = Package(name: "menu-bar-client")`)

	got := DerivePackage(ProjectSwift, dir)
	if got != "MenuBarClientTacplus" {
		t.Errorf("DerivePackage(Swift) = %q, want %q", got, "MenuBarClientTacplus")
	}
}

func TestDerivePackage_Fallback(t *testing.T) {
	dir := t.TempDir()
	// No build files -- DerivePackage should fall through to the default

	got := DerivePackage(ProjectGo, dir)
	if got != "tacplus" {
		t.Errorf("DerivePackage(Go, no go.mod) = %q, want %q", got, "tacplus")
	}
}

func TestDerivePackage_Unknown(t *testing.T) {
	dir := t.TempDir()

	got := DerivePackage(ProjectUnknown, dir)
	if got != "tacplus" {
		t.Errorf("DerivePackage(Unknown) = %q, want %q", got, "tacplus")
	}
}

func TestDerivePackage_TableDriven(t *testing.T) {
	tests := []struct {
		name        string
		projectType ProjectType
		files       map[string]string
		want        string
	}{
		{
			name:        "Go with module",
			projectType: ProjectGo,
			files:       map[string]string{"go.mod": "module gitlab.com/org/repo\n\ngo 1.22\n"},
			want:        "gitlab.com/org/repo/tacplus",
		},
		{
			name:        "Go without go.mod",
			projectType: ProjectGo,
			files:       map[string]string{},
			want:        "tacplus",
		},
		{
			name:        "Java with pom.xml",
			projectType: ProjectJava,
			files:       map[string]string{"pom.xml": `<project><groupId>org.example</groupId></project>`},
			want:        "org.example.tacplus",
		},
		{
			name:        "Java without build file",
			projectType: ProjectJava,
			files:       map[string]string{},
			want:        "tacplus",
		},
		{
			name:        "TypeScript with name",
			projectType: ProjectTypeScript,
			files:       map[string]string{"package.json": `{"name": "my-app"}`},
			want:        "my-app-tacplus",
		},
		{
			name:        "TypeScript without package.json",
			projectType: ProjectTypeScript,
			files:       map[string]string{},
			want:        "tacplus",
		},
		{
			name:        "Python with name",
			projectType: ProjectPython,
			files:       map[string]string{"pyproject.toml": "[project]\nname = \"pylib\"\n"},
			want:        "pylib-tacplus",
		},
		{
			name:        "Python without pyproject.toml",
			projectType: ProjectPython,
			files:       map[string]string{},
			want:        "tacplus",
		},
		{
			name:        "Rust with name",
			projectType: ProjectRust,
			files:       map[string]string{"Cargo.toml": "[package]\nname = \"rustcrate\"\n"},
			want:        "rustcrate-tacplus",
		},
		{
			name:        "Rust without Cargo.toml",
			projectType: ProjectRust,
			files:       map[string]string{},
			want:        "tacplus",
		},
		{
			name:        "Swift with Package.swift",
			projectType: ProjectSwift,
			files:       map[string]string{"Package.swift": `let package = Package(name: "client-app")`},
			want:        "ClientAppTacplus",
		},
		{
			name:        "Swift without Package.swift",
			projectType: ProjectSwift,
			files:       map[string]string{},
			want:        "tacplus",
		},
		{
			name:        "Unknown type",
			projectType: ProjectUnknown,
			files:       map[string]string{},
			want:        "tacplus",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			for name, content := range tt.files {
				writeFile(t, dir, name, content)
			}

			got := DerivePackage(tt.projectType, dir)
			if got != tt.want {
				t.Errorf("DerivePackage(%v) = %q, want %q", tt.projectType, got, tt.want)
			}
		})
	}
}

// --- Helper ---

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write %s: %v", name, err)
	}
}
