// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package tactype

// LanguageConfig represents language-specific configuration
type LanguageConfig struct {
	Interfaces       string            `yaml:"interfaces,omitempty"`
	InterfacesImport string            `yaml:"interfaces_import,omitempty"` // Java import package (when different from Maven groupId)
	Version          string            `yaml:"version,omitempty"`
	Dependencies     map[string]string `yaml:"dependencies,omitempty"`
}

// JavaImportBase returns the Java import base package for interfaces.
// Uses InterfacesImport if set (when Maven groupId differs from Java package),
// otherwise falls back to Interfaces.
func (c LanguageConfig) JavaImportBase() string {
	if c.InterfacesImport != "" {
		return c.InterfacesImport
	}
	return c.Interfaces
}

// LanguagesConfig represents all language configurations
type LanguagesConfig struct {
	Go         LanguageConfig `yaml:"go,omitempty"`
	Java       LanguageConfig `yaml:"java,omitempty"`
	TypeScript LanguageConfig `yaml:"typescript,omitempty"`
	Python     LanguageConfig `yaml:"python,omitempty"`
	Rust       LanguageConfig `yaml:"rust,omitempty"`
	Swift      LanguageConfig `yaml:"swift,omitempty"`
	CSharp     LanguageConfig `yaml:"csharp,omitempty"`
}

// RegistryConfig represents package registry configuration
type RegistryConfig struct {
	GitLabProjectID string            `yaml:"gitlab_project_id,omitempty"`
	GitLabGroupID   string            `yaml:"gitlab_group_id,omitempty"`
	Interfaces      map[string]string `yaml:"interfaces,omitempty"`
	Imports         map[string]string `yaml:"imports,omitempty"`
}

// GitLabPackagesURL returns the base GitLab packages API URL.
// Prefers group-level registry (gitlab_group_id) which allows cross-project
// access within the group. Falls back to project-level (gitlab_project_id).
func (r RegistryConfig) GitLabPackagesURL() string {
	if r.GitLabGroupID != "" {
		return "https://gitlab.com/api/v4/groups/" + r.GitLabGroupID + "/-/packages"
	}
	if r.GitLabProjectID == "" {
		return ""
	}
	return "https://gitlab.com/api/v4/projects/" + r.GitLabProjectID + "/packages"
}

// GitLabNpmURL returns the GitLab npm registry URL
func (r RegistryConfig) GitLabNpmURL() string {
	base := r.GitLabPackagesURL()
	if base == "" {
		return ""
	}
	return base + "/npm"
}

// GitLabMavenURL returns the GitLab Maven registry URL
func (r RegistryConfig) GitLabMavenURL() string {
	base := r.GitLabPackagesURL()
	if base == "" {
		return ""
	}
	return base + "/maven"
}

// GitLabPyPIURL returns the GitLab PyPI registry URL
func (r RegistryConfig) GitLabPyPIURL() string {
	base := r.GitLabPackagesURL()
	if base == "" {
		return ""
	}
	return base + "/pypi/simple"
}
