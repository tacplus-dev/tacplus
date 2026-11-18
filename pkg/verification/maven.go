// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package verification

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"go.uber.org/zap"
)

// groupByMavenModule groups files by their nearest Maven module directory
// (the nearest ancestor containing a pom.xml).
func groupByMavenModule(files []string) map[string][]string {
	groups := make(map[string][]string)
	for _, f := range files {
		moduleDir := findMavenProjectDir(filepath.Dir(f))
		if moduleDir == "" {
			zap.L().Warn("no Maven module found for file, skipping", zap.String("file", f))
			continue
		}
		groups[moduleDir] = append(groups[moduleDir], f)
	}
	return groups
}

// findMavenProjectDir walks up from dir to find the directory containing pom.xml.
func findMavenProjectDir(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, "pom.xml")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// mavenRepo is the Maven local repository a module's verification uses: a
// writable one under its target/, because the host .m2 may be mounted read-only.
func mavenRepo(projectDir string) string {
	return filepath.Join(projectDir, "target", ".m2repo")
}

// mavenCompile compiles a Maven module's main sources into target/classes.
func mavenCompile(projectDir string) error {
	cmd := exec.Command("mvn", "-q", "-DskipTests", "-Dmaven.repo.local="+mavenRepo(projectDir), "compile")
	cmd.Dir = projectDir
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mvn compile: %w\n%s", err, string(output))
	}
	return nil
}

// mavenDepClasspath uses mvn dependency:build-classpath to get the dependency classpath.
func mavenDepClasspath(projectDir string) (string, error) {
	cmd := exec.Command("mvn", "dependency:build-classpath", "-q",
		"-Dmdep.outputFile=/dev/stdout",
		"-Dmaven.repo.local="+mavenRepo(projectDir))
	cmd.Dir = projectDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("mvn dependency:build-classpath: %w\n%s", err, string(output))
	}

	depCP := extractClasspathLine(string(output))
	if depCP == "" {
		return "", fmt.Errorf("no dependency classpath found in maven output")
	}
	return depCP, nil
}

// extractClasspathLine finds the classpath line from Maven output.
// It's typically the longest line containing jar paths separated by :.
func extractClasspathLine(output string) string {
	var best string
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		line = strings.TrimSpace(line)
		if len(line) > len(best) && (strings.Contains(line, ".jar") || strings.Contains(line, "/classes")) {
			best = line
		}
	}
	return best
}
