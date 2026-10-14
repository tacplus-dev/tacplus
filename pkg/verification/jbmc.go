// Copyright (c) 2026 Seabeam LLC
// SPDX-License-Identifier: Apache-2.0

package verification

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"go.uber.org/zap"

	spec "gitlab.com/seabeam/schematic/tacplus/target/proto/spec/sys"
)

// jbmcResultRe matches JBMC's per-harness status line.
// Example: "VERIFICATION SUCCESSFUL" or "VERIFICATION FAILED"
var jbmcResultRe = regexp.MustCompile(`VERIFICATION\s+(SUCCESSFUL|FAILED)`)

// jbmcHarnessRe extracts harness method names from Proof.java source.
var jbmcHarnessRe = regexp.MustCompile(`public\s+static\s+void\s+(verify_\w+)\s*\(`)

// runJbmc runs JBMC on all Proof.java files in the output directory.
// Proof files are grouped by their nearest Maven module so that classpath
// resolution targets the correct submodule (not the root aggregator).
func runJbmc(outputDir string) (*spec.VerificationReport, error) {
	zap.L().Info("searching for proof files", zap.String("dir", outputDir))
	proofFiles := findJbmcProofs(outputDir)
	if len(proofFiles) == 0 {
		zap.L().Info("no Proof.java files found")
		return nil, nil
	}
	zap.L().Info("found proof files", zap.Int("count", len(proofFiles)), zap.Strings("files", proofFiles))

	// Group proof files by their nearest Maven module
	moduleProofs := groupByMavenModule(proofFiles)
	if len(moduleProofs) == 0 {
		return nil, fmt.Errorf("no Maven project (pom.xml) found for any proof file")
	}

	version := toolVersion("jbmc", "--version")

	report := &spec.VerificationReport{
		Tool:        "jbmc",
		ToolVersion: version,
		Language:    "java",
	}

	for moduleDir, files := range moduleProofs {
		zap.L().Info("processing maven module",
			zap.String("module", filepath.Base(moduleDir)),
			zap.Int("proofs", len(files)),
		)

		// Resolve classpath for this module
		zap.L().Info("resolving maven classpath", zap.String("module", moduleDir))
		depClasspath, err := mavenDepClasspath(moduleDir)
		if err != nil {
			return nil, fmt.Errorf("failed to resolve classpath for %s: %w", filepath.Base(moduleDir), err)
		}

		// Build a CProver stub for javac (JBMC handles CProver internally at analysis time,
		// but javac needs the class to compile Proof.java)
		cproverDir, err := buildCProverStub(moduleDir)
		if err != nil {
			return nil, fmt.Errorf("failed to build CProver stub: %w", err)
		}

		// Compile and verify each Proof.java independently so that compilation
		// failures in one package don't block verification of others.
		proofClassesDir := filepath.Join(moduleDir, "target", "proof-classes")
		sourcePath := resolveSourcePath(moduleDir)
		compileCP := depClasspath + ":" + cproverDir
		jbmcCP := proofClassesDir + ":" + depClasspath

		for i, proofFile := range files {
			zap.L().Info("verifying proof file",
				zap.Int("index", i+1),
				zap.Int("total", len(files)),
				zap.String("file", proofFile),
			)

			// Clean proof-classes between files to avoid stale classes
			os.RemoveAll(proofClassesDir)

			if err := compileProofFile(proofFile, compileCP, sourcePath, proofClassesDir); err != nil {
				// Record compilation failure as failed harnesses
				harnessNames := extractJbmcHarnessNames(proofFile)
				for _, name := range harnessNames {
					report.Harnesses = append(report.Harnesses, &spec.VerificationHarness{
						HarnessName: name,
						Status:      "failed",
						Detail:      fmt.Sprintf("compilation failed: %v", err),
						Contracts:   []string{"validate() does not throw for inputs satisfying assumptions"},
					})
				}
				continue
			}

			harnesses, err := runJbmcOnProofFile(proofFile, jbmcCP)
			if err != nil {
				return nil, fmt.Errorf("jbmc on %s: %w", proofFile, err)
			}
			report.Harnesses = append(report.Harnesses, harnesses...)
		}
	}

	// Tally results
	for _, h := range report.Harnesses {
		report.Total++
		if h.Status == "verified" {
			report.Passed++
		} else {
			report.Failed++
		}
	}

	return report, nil
}

// groupByMavenModule groups proof files by their nearest Maven module directory
// (the nearest ancestor containing a pom.xml).
func groupByMavenModule(proofFiles []string) map[string][]string {
	groups := make(map[string][]string)
	for _, pf := range proofFiles {
		moduleDir := findMavenProjectDir(filepath.Dir(pf))
		if moduleDir == "" {
			zap.L().Warn("no Maven module found for proof file, skipping", zap.String("file", pf))
			continue
		}
		groups[moduleDir] = append(groups[moduleDir], pf)
	}
	return groups
}

// findJbmcProofs walks a directory for Proof.java files containing CProver references.
func findJbmcProofs(dir string) []string {
	var files []string
	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if info.Name() != "Proof.java" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if strings.Contains(string(data), "CProver") {
			files = append(files, path)
		}
		return nil
	})
	return files
}

// cproverStubSource is a minimal CProver class that satisfies javac.
// JBMC replaces these with its own internal models at analysis time.
const cproverStubSource = `package org.cprover;
public class CProver {
    public static void assume(boolean condition) {}
    public static boolean nondetBoolean() { return false; }
    public static int nondetInt() { return 0; }
    public static long nondetLong() { return 0L; }
    public static float nondetFloat() { return 0.0f; }
    public static double nondetDouble() { return 0.0; }
}
`

// buildCProverStub compiles a minimal CProver stub class into target/cprover-stub/
// and returns the directory path (for use as a classpath entry).
func buildCProverStub(projectDir string) (string, error) {
	stubDir := filepath.Join(projectDir, "target", "cprover-stub")
	srcDir := filepath.Join(stubDir, "org", "cprover")
	if err := os.MkdirAll(srcDir, 0755); err != nil {
		return "", err
	}

	srcFile := filepath.Join(srcDir, "CProver.java")
	if err := os.WriteFile(srcFile, []byte(cproverStubSource), 0644); err != nil {
		return "", err
	}

	cmd := exec.Command("javac", "-d", stubDir, srcFile)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("javac CProver stub: %w\n%s", err, string(output))
	}

	return stubDir, nil
}

// compileProofFile compiles a single Proof.java file with javac into the specified output directory.
// Uses -sourcepath so javac can resolve types/helpers from the main source tree.
func compileProofFile(proofFile, classpath, sourcePath, outputDir string) error {
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return err
	}

	args := []string{"-cp", classpath, "-sourcepath", sourcePath, "-d", outputDir, proofFile}
	cmd := exec.Command("javac", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("javac: %w\n%s", err, string(output))
	}
	return nil
}

// resolveSourcePath finds the Java source directory for the Maven project.
func resolveSourcePath(projectDir string) string {
	// Standard tacplus layout: target/generated-sources/tacplus/src/main/java
	candidate := filepath.Join(projectDir, "target", "generated-sources", "tacplus", "src", "main", "java")
	if _, err := os.Stat(candidate); err == nil {
		return candidate
	}
	// Fallback: src/main/java
	return filepath.Join(projectDir, "src", "main", "java")
}

// findMavenProjectDir walks up from outputDir to find the directory containing pom.xml.
func findMavenProjectDir(outputDir string) string {
	dir := outputDir
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

// mavenDepClasspath uses mvn dependency:build-classpath to get the dependency classpath.
// Uses a writable temp local repo because the host .m2 may be mounted read-only.
func mavenDepClasspath(projectDir string) (string, error) {
	tmpRepo := filepath.Join(projectDir, "target", ".m2repo")
	cmd := exec.Command("mvn", "dependency:build-classpath", "-q",
		"-Dmdep.outputFile=/dev/stdout",
		"-Dmaven.repo.local="+tmpRepo)
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

// runJbmcOnProofFile runs JBMC on each harness in a Proof.java file.
func runJbmcOnProofFile(proofFile, classpath string) ([]*spec.VerificationHarness, error) {
	// Extract harness method names from source
	harnessNames := extractJbmcHarnessNames(proofFile)
	if len(harnessNames) == 0 {
		return nil, nil
	}

	// Determine the fully qualified class name from the Java package declaration
	className := resolveJbmcClassName(proofFile)

	var harnesses []*spec.VerificationHarness
	for _, name := range harnessNames {
		h, err := runJbmcOnHarness(classpath, className, name)
		if err != nil {
			// Record as failed rather than aborting
			harnesses = append(harnesses, &spec.VerificationHarness{
				HarnessName: name,
				Status:      "failed",
				Detail:      err.Error(),
				Contracts:   []string{"validate() does not throw for inputs satisfying assumptions"},
			})
			continue
		}
		harnesses = append(harnesses, h)
	}

	return harnesses, nil
}

// extractJbmcHarnessNames reads Proof.java and extracts verify_* method names.
func extractJbmcHarnessNames(proofFile string) []string {
	data, err := os.ReadFile(proofFile)
	if err != nil {
		return nil
	}
	matches := jbmcHarnessRe.FindAllStringSubmatch(string(data), -1)
	var names []string
	for _, m := range matches {
		if len(m) >= 2 {
			names = append(names, m[1])
		}
	}
	return names
}

// resolveJbmcClassName reads the package declaration from Proof.java and
// returns the fully qualified class name (e.g., "com.example.types.sys.state.Proof").
func resolveJbmcClassName(proofFile string) string {
	data, err := os.ReadFile(proofFile)
	if err != nil {
		return "Proof"
	}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "package ") {
			pkg := strings.TrimPrefix(trimmed, "package ")
			pkg = strings.TrimSuffix(pkg, ";")
			pkg = strings.TrimSpace(pkg)
			return pkg + ".Proof"
		}
	}
	return "Proof"
}

// runJbmcOnHarness runs JBMC on a single harness method and parses the result.
func runJbmcOnHarness(classpath, className, functionName string) (*spec.VerificationHarness, error) {
	// JBMC requires JVM method descriptors — our harnesses are all void(), so :()V
	qualifiedFunc := className + "." + functionName + ":()V"
	args := []string{
		"--classpath", classpath,
		className,
		"--function", qualifiedFunc,
		"--unwind", "10",
		"--max-nondet-array-length", "5",
		"--max-nondet-tree-depth", "3",
		"--disable-uncaught-exception-check",
	}

	cmd := exec.Command("jbmc", args...)
	output, err := cmd.CombinedOutput()
	outputStr := string(output)

	status, detail := parseJbmcOutput(outputStr, qualifiedFunc)

	if status == "" && err != nil {
		return nil, fmt.Errorf("jbmc failed: %w\n%s", err, outputStr)
	}

	return &spec.VerificationHarness{
		HarnessName: functionName,
		Status:      status,
		Detail:      detail,
		Contracts:   []string{"validate() does not throw for inputs satisfying assumptions"},
	}, nil
}

// jbmcAssertionRe matches per-property result lines from JBMC output.
// Format: [java::package.Class.method:desc.assertion.N] ... : SUCCESS/FAILURE
var jbmcAssertionRe = regexp.MustCompile(`\[java::(.+?)\.assertion\.\d+\].*:\s+(SUCCESS|FAILURE)`)

// parseJbmcOutput checks if our harness assertion passed or failed.
// JBMC checks thousands of internal properties (null pointer, bounds, etc.)
// in protobuf code. We only care about our user assertion in the Proof class.
// If no assertion property is found, falls back to the overall VERIFICATION verdict.
func parseJbmcOutput(output, qualifiedFunc string) (status string, detail string) {
	// Convert qualified function for matching: replace : and () with regex-safe form
	// The property line looks like: [java::pkg.Proof.verify_x:()V.assertion.1]
	funcInProp := "java::" + qualifiedFunc

	scanner := bufio.NewScanner(strings.NewReader(output))
	foundAssertion := false
	allAssertionsPass := true

	for scanner.Scan() {
		line := scanner.Text()

		// Check for our harness assertion property
		if strings.Contains(line, funcInProp) && strings.Contains(line, ".assertion.") {
			foundAssertion = true
			if strings.HasSuffix(strings.TrimSpace(line), "FAILURE") {
				allAssertionsPass = false
			}
			continue
		}

		// Also check overall verdict as fallback
		matches := jbmcResultRe.FindStringSubmatch(line)
		if len(matches) >= 2 && !foundAssertion {
			if matches[1] == "SUCCESSFUL" {
				status = "verified"
			} else {
				status = "failed"
				detail = extractJbmcCounterexample(output)
			}
		}
	}

	// Prefer our assertion-level result over the overall verdict
	if foundAssertion {
		if allAssertionsPass {
			return "verified", ""
		}
		return "failed", extractJbmcCounterexample(output)
	}

	return status, detail
}

// extractJbmcCounterexample extracts counterexample trace from JBMC output (if present).
func extractJbmcCounterexample(output string) string {
	idx := strings.Index(output, "Counterexample:")
	if idx < 0 {
		idx = strings.Index(output, "Trace")
		if idx < 0 {
			return ""
		}
	}
	detail := output[idx:]
	if len(detail) > 500 {
		detail = detail[:500] + "\n... (truncated)"
	}
	return detail
}
