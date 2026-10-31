// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// javaDependencyFloors are the lowest versions of Maven artifacts the Java
// harness poms (checked in, or written by a cross-test script) may resolve:
// below them the artifact carries advisories (#200). Each comes in
// transitively, so a pom pins it under <dependencyManagement>: left out,
// Maven resolves whatever the declaring dependency names.
var javaDependencyFloors = []struct {
	group, artifact, floor string
	// requiredBy is the groupId whose dependency brings the artifact in: a
	// pom that declares it pins the artifact too.
	requiredBy string
}{
	// GHSA-xxph-c9ww-hj94, through grpc-java (grpc-api). grpc-java 1.85.1
	// still declares 33.6.0-android.
	{"com.google.guava", "guava", "33.7.2-android", "io.grpc"},
	// GHSA-7hhh-6rmp-j9qf, GHSA-p6pp-m3f8-5c89 (core), GHSA-cxp5-3px4-pw24,
	// GHSA-wv8q-qhhj-9h54, GHSA-q4xh-88c3-wmh7, GHSA-5gvw-p9qm-jgwh,
	// GHSA-gx83-3vf8-gh7j, GHSA-vvgp-rfg2-7rr6, GHSA-wjgm-6hv5-3cvf
	// (databind), through the MCP Java SDK (2.1.0 still declares 3.1.4).
	{"tools.jackson.core", "jackson-core", "3.1.7", "io.modelcontextprotocol.sdk"},
	{"tools.jackson.core", "jackson-databind", "3.1.7", "io.modelcontextprotocol.sdk"},
	{"tools.jackson.dataformat", "jackson-dataformat-yaml", "3.1.7", "io.modelcontextprotocol.sdk"},
}

func TestJavaDependencyPinsMeetTheirFloors(t *testing.T) {
	var files []string
	for _, pattern := range []string{
		"../cross-test/*/run.sh",
		"../cross-test/lib/*.sh",
		"../cross-test/*/*/pom.xml",
		"../cross-test/*/*/*/pom.xml",
	} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matches...)
	}
	poms := 0
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		if !strings.Contains(text, "<project") {
			continue
		}
		poms++
		managed := ""
		if m := regexp.MustCompile(`(?s)<dependencyManagement>.*?</dependencyManagement>`).FindString(text); m != "" {
			managed = m
		}
		for _, d := range javaDependencyFloors {
			pin := regexp.MustCompile(`<groupId>` + regexp.QuoteMeta(d.group) + `</groupId>\s*<artifactId>` +
				regexp.QuoteMeta(d.artifact) + `</artifactId>\s*<version>([^<]+)</version>`)
			if strings.Contains(text, "<groupId>"+d.requiredBy+"</groupId>") && !pin.MatchString(managed) {
				t.Errorf("%s declares %s but pins no %s:%s under <dependencyManagement> (floor %s)",
					path, d.requiredBy, d.group, d.artifact, d.floor)
			}
			for _, m := range pin.FindAllStringSubmatchIndex(text, -1) {
				version := text[m[2]:m[3]]
				if compareMavenVersions(version, d.floor) < 0 {
					line := strings.Count(text[:m[0]], "\n") + 1
					t.Errorf("%s:%d pins %s:%s %s, below its floor %s", path, line, d.group, d.artifact, version, d.floor)
				}
			}
		}
	}
	if poms == 0 {
		t.Fatal("no cross-test Java pom found")
	}
}

// compareMavenVersions orders the numeric release of two versions
// ("33.7.2-android" is 33.7.2): the floors differ from a pin only there.
func compareMavenVersions(a, b string) int {
	numbers := func(v string) []int {
		v, _, _ = strings.Cut(v, "-")
		var out []int
		for _, part := range strings.Split(v, ".") {
			n, err := strconv.Atoi(part)
			if err != nil {
				break
			}
			out = append(out, n)
		}
		return out
	}
	x, y := numbers(a), numbers(b)
	for i := 0; i < len(x) || i < len(y); i++ {
		var p, q int
		if i < len(x) {
			p = x[i]
		}
		if i < len(y) {
			q = y[i]
		}
		if p != q {
			if p < q {
				return -1
			}
			return 1
		}
	}
	return 0
}
