// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package formatter

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"go.uber.org/zap"

	"github.com/tacplus-dev/tacplus/pkg/codefile"
)

// The deadline of an external formatter scales with the files it formats: a
// fixed one sized for an idle machine killed black on a loaded build pod
// (tacplus#918). It only bounds a formatter that hangs, so it is generous.
// Variables so tests can shorten them.
var (
	formatBaseTimeout    = 5 * time.Minute
	formatPerFileTimeout = 5 * time.Second
)

// formatDeadline is the time an external formatter is given for files files.
func formatDeadline(files int) time.Duration {
	return formatBaseTimeout + time.Duration(files)*formatPerFileTimeout
}

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
// result back.
func indentFiles(dir, ext string, lang codefile.Language) error {
	files, err := collectFiles(dir, ext)
	if err != nil {
		return fmt.Errorf("collecting %s files: %w", ext, err)
	}
	if len(files) == 0 {
		return nil
	}

	zap.L().Info("formatting files with codefile.IndentCode",
		zap.String("ext", ext),
		zap.Int("count", len(files)))

	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("reading %s: %w", path, err)
		}

		indented := codefile.IndentCode(lang, string(content))

		if err := os.WriteFile(path, []byte(indented), 0644); err != nil {
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

	files := 0
	for _, ext := range []string{".py", ".pyi"} {
		found, err := collectFiles(dir, ext)
		if err != nil {
			return fmt.Errorf("collecting %s files: %w", ext, err)
		}
		files += len(found)
	}

	return run(tool, []string{"."}, dir, files)
}

// collectFiles walks dir and returns absolute paths of files with the given extension.
func collectFiles(dir, ext string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(path) == ext {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

// run executes a formatter over files files under the deadline formatDeadline
// gives them. If workDir is non-empty it sets the working directory.
func run(tool string, args []string, workDir string, files int) error {
	deadline := formatDeadline(files)
	ctx, cancel := context.WithTimeout(context.Background(), deadline)
	defer cancel()

	cmd := exec.CommandContext(ctx, tool, args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	zap.L().Debug("running formatter", zap.String("tool", filepath.Base(tool)), zap.Strings("args", args),
		zap.Int("files", files), zap.Duration("deadline", deadline))

	if err := cmd.Run(); err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("%s killed at its deadline of %s (%d files: %s + %s per file): %w",
				filepath.Base(tool), deadline, files, formatBaseTimeout, formatPerFileTimeout, err)
		}
		return fmt.Errorf("%s failed: %w", filepath.Base(tool), err)
	}
	return nil
}
