// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package formatter

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/codefile"
)

const timeout = 2 * time.Minute

// Format runs the language-appropriate formatter on the generated code in dir.
// Go formatting is handled in-process by the generators (goimports/gofmt).
// Rust, Java, and TypeScript use codefile.IndentCode for brace-counting
// auto-indentation, with no external tool dependencies.
// Python uses black (external).
func Format(lang, dir string) error {
	switch lang {
	case "go":
		return nil
	case "java":
		return indentFiles(dir, ".java", codefile.Java)
	case "typescript":
		return indentFiles(dir, ".ts", codefile.TypeScript)
	case "python":
		return formatPython(dir)
	case "rust":
		return indentFiles(dir, ".rs", codefile.Rust)
	default:
		return nil
	}
}

// indentFiles walks dir, reads each file with the given extension, applies
// codefile.IndentCode to auto-indent based on brace nesting, and writes the
// result back. Every read and write goes through an os.Root, so no path
// resolves outside dir, and a symlink is never written through (gosec G703;
// tacplus#578).
func indentFiles(dir, ext string, lang codefile.Language) error {
	r, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("opening %s: %w", dir, err)
	}
	defer r.Close()

	files, err := collectFiles(r, ext)
	if err != nil {
		return fmt.Errorf("collecting %s files: %w", ext, err)
	}
	if len(files) == 0 {
		return nil
	}

	zap.L().Info("formatting files with codefile.IndentCode",
		zap.String("ext", ext),
		zap.Int("count", len(files)))

	for _, rel := range files {
		path := filepath.Join(dir, rel)
		content, err := r.ReadFile(rel)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}

		indented := codefile.IndentCode(lang, string(content))

		if err := r.WriteFile(rel, []byte(indented), 0644); err != nil {
			return fmt.Errorf("writing %s: %w", path, err)
		}
	}
	return nil
}

func formatPython(dir string) error {
	tool, err := exec.LookPath("black")
	if err != nil {
		zap.L().Info("black not found, skipping optional Python formatting")
		return nil
	}

	return run(tool, []string{"."}, dir)
}

// collectFiles walks r and returns the paths, relative to it, of the regular
// files with the given extension. A symlink is not followed or collected.
func collectFiles(r *os.Root, ext string) ([]string, error) {
	var files []string
	err := fs.WalkDir(r.FS(), ".", func(rel string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type().IsRegular() && filepath.Ext(rel) == ext {
			files = append(files, filepath.FromSlash(rel))
		}
		return nil
	})
	return files, err
}

// run executes a command with a timeout. If workDir is non-empty it sets the working directory.
func run(tool string, args []string, workDir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, tool, args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	zap.L().Debug("running formatter", zap.String("tool", filepath.Base(tool)), zap.Strings("args", args))

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", filepath.Base(tool), err)
	}
	return nil
}
