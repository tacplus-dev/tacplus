// Copyright (c) 2026 Second Star Cavalier LLC

package metadata

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/consts"
)

func TestGenerateMetaData(t *testing.T) {
	tests := []struct {
		lang            string
		selfPackage     string
		javaPackagePath string
		wantFile        string
		wantContains    []string
	}{
		{
			lang:         consts.LangRust,
			selfPackage:  "gateway",
			wantFile:     "meta_data.rs",
			wantContains: []string{`pub const PROJECT_NAME: &str = "gateway";`},
		},
		{
			lang:            consts.LangJava,
			selfPackage:     "eventbus",
			javaPackagePath: "com/example/eventbus",
			wantFile:        "com/example/eventbus/MetaData.java",
			wantContains:    []string{`package com.example.eventbus;`, `public static final String PROJECT_NAME = "eventbus";`},
		},
		{
			lang:         consts.LangGo,
			selfPackage:  "identity",
			wantFile:     "meta_data.go",
			wantContains: []string{`package types`, `const ProjectName = "identity"`},
		},
		{
			lang:         consts.LangTypeScript,
			selfPackage:  "sentinel",
			wantFile:     "metaData.ts",
			wantContains: []string{`export const PROJECT_NAME: string = "sentinel";`},
		},
		{
			lang:         consts.LangPython,
			selfPackage:  "argus",
			wantFile:     "meta_data.py",
			wantContains: []string{`PROJECT_NAME: str = "argus"`},
		},
	}

	for _, tt := range tests {
		t.Run(tt.lang, func(t *testing.T) {
			tmpDir := t.TempDir()
			err := GenerateMetaData(tt.lang, tt.selfPackage, tmpDir, tt.javaPackagePath)
			if err != nil {
				t.Fatalf("GenerateMetaData() error = %v", err)
			}

			outPath := filepath.Join(tmpDir, tt.wantFile)
			data, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatalf("expected file %s not found: %v", tt.wantFile, err)
			}

			content := string(data)
			for _, want := range tt.wantContains {
				if !strings.Contains(content, want) {
					t.Errorf("file content missing %q\ngot:\n%s", want, content)
				}
			}
		})
	}
}

func TestGenerateMetaData_EmptySelfPackage(t *testing.T) {
	tmpDir := t.TempDir()
	err := GenerateMetaData(consts.LangRust, "", tmpDir, "")
	if err != nil {
		t.Fatalf("expected no error for empty selfPackage, got %v", err)
	}
	// Should not create any file.
	entries, _ := os.ReadDir(tmpDir)
	if len(entries) != 0 {
		t.Errorf("expected no files, got %d", len(entries))
	}
}
