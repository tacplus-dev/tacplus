// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"encoding/xml"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Without a configuration logback logs everything at DEBUG, and grpc-java's
// Netty frame and TLS lines alone (some 10,500 a run) cut the gates job's
// trace at GitLab's 4 MiB, hiding which gate failed or hung (tacplus#877,
// tacplus#893). So every JVM a cross-test script starts names a logback
// configuration, and every surefire run it starts gets
// cross-test/lib/logback-test.xml as its test resource.
var (
	// javaLaunch is a JVM a script starts: java itself, or the java binary
	// a script resolved into a variable.
	javaLaunch = regexp.MustCompile(`(^|[\s;(&|])("\$\{?(java_cmd|java_bin|JAVA_BIN)\}?"|"\$\{JDK_BIN\}java")\s+-|^\s*java\s+-`)
	// mvnTest is a Maven run of the test phase, which runs surefire.
	mvnTest      = regexp.MustCompile(`\bmvn(\s+-\S+)*\s+test(\s|\)|$)`)
	testLogback  = regexp.MustCompile(`cp "\$ROOT_DIR/cross-test/lib/logback-test\.xml" "[^"]*/src/test/resources/logback-test\.xml"`)
	quietLevels  = map[string]bool{"INFO": true, "WARN": true, "ERROR": true, "OFF": true}
	noisyLoggers = []string{"io.grpc", "io.netty"}
)

// scriptLines are the lines of a script with their continuations joined,
// comments left out.
func scriptLines(text string) []string {
	var lines []string
	for _, line := range strings.Split(strings.ReplaceAll(text, "\\\n", " "), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		lines = append(lines, line)
	}
	return lines
}

func TestCrossTestJavaRunsKeepGrpcLoggingQuiet(t *testing.T) {
	scripts, err := filepath.Glob("../cross-test/*/run.sh")
	if err != nil || len(scripts) == 0 {
		t.Fatalf("no cross-test scripts: %v", err)
	}
	launches, surefires := 0, 0
	for _, script := range scripts {
		text, err := os.ReadFile(script)
		if err != nil {
			t.Fatal(err)
		}
		runsSurefire := false
		for _, line := range scriptLines(string(text)) {
			if javaLaunch.MatchString(line) {
				launches++
				if !strings.Contains(line, "-Dlogback.configurationFile=") {
					t.Errorf("%s starts a JVM with no -Dlogback.configurationFile: logback logs io.grpc at DEBUG:\n%s", script, strings.TrimSpace(line))
				}
			}
			if mvnTest.MatchString(line) {
				runsSurefire = true
			}
		}
		if runsSurefire {
			surefires++
			if !testLogback.Match(text) {
				t.Errorf("%s runs surefire without copying cross-test/lib/logback-test.xml into the tests' src/test/resources", script)
			}
		}
	}
	if launches == 0 || surefires == 0 {
		t.Errorf("found %d JVM launches and %d surefire scripts: the patterns no longer match the scripts", launches, surefires)
	}
}

type logbackConfig struct {
	Root struct {
		Level string `xml:"level,attr"`
	} `xml:"root"`
	Loggers []struct {
		Name  string `xml:"name,attr"`
		Level string `xml:"level,attr"`
	} `xml:"logger"`
}

// Every logback configuration under cross-test logs io.grpc and io.netty at
// INFO or above, by a logger of their own or the root's level.
func TestCrossTestLogbackConfigsQuietGrpc(t *testing.T) {
	var configs []string
	err := filepath.WalkDir("../cross-test", func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasPrefix(d.Name(), "logback") && strings.HasSuffix(d.Name(), ".xml") {
			configs = append(configs, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) == 0 {
		t.Fatal("no logback configuration under cross-test")
	}
	for _, path := range configs {
		text, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var config logbackConfig
		if err := xml.Unmarshal(text, &config); err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		for _, name := range noisyLoggers {
			level := strings.ToUpper(config.Root.Level)
			for _, logger := range config.Loggers {
				if logger.Name == name || strings.HasPrefix(name, logger.Name+".") {
					level = strings.ToUpper(logger.Level)
				}
			}
			if !quietLevels[level] {
				t.Errorf("%s logs %s at %q: set it to INFO or above", path, name, level)
			}
		}
	}
}
