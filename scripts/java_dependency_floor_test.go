// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package scripts

import (
	"encoding/xml"
	"os"
	"os/exec"
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

// TestJavaGcsArchiveDependencyJoinsTheProjectDependencies: the compile pom
// holds two <dependencies> blocks once it pins under <dependencyManagement>,
// and the GCS SDK a GcsArchiveSink build compiles against must go into the
// project's, not the managed one, where it puts nothing on the classpath.
func TestJavaGcsArchiveDependencyJoinsTheProjectDependencies(t *testing.T) {
	pom := filepath.Join(t.TempDir(), "pom.xml")
	script := `. ../cross-test/lib/java-compile.sh && write_java_compile_pom "$1" /nonexistent && add_java_gcs_archive_dependency "$1"`
	if out, err := exec.Command("bash", "-c", script, "bash", pom).CombinedOutput(); err != nil {
		t.Fatalf("writing the compile pom: %v\n%s", err, out)
	}
	data, err := os.ReadFile(pom)
	if err != nil {
		t.Fatal(err)
	}
	type dependency struct {
		ArtifactID string `xml:"artifactId"`
	}
	var project struct {
		Dependencies []dependency `xml:"dependencies>dependency"`
		Managed      []dependency `xml:"dependencyManagement>dependencies>dependency"`
	}
	if err := xml.Unmarshal(data, &project); err != nil {
		t.Fatalf("parsing %s: %v", pom, err)
	}
	has := func(deps []dependency) bool {
		for _, d := range deps {
			if d.ArtifactID == "google-cloud-storage" {
				return true
			}
		}
		return false
	}
	if !has(project.Dependencies) || has(project.Managed) {
		t.Errorf("google-cloud-storage is not (only) a project dependency:\n%s", data)
	}
}

// TestJavaGcsArchivePomMeetsItsFloors: google-cloud-storage brings in jackson
// 2, guava -jre and opentelemetry-api (#263). The SDK pin must be one whose
// jackson is past its advisories, and the pom a GcsArchiveSink build compiles
// under must pin the other two under <dependencyManagement>, as the harness
// poms pin theirs (#200). The pin is the one dependencies.yaml carries.
func TestJavaGcsArchivePomMeetsItsFloors(t *testing.T) {
	const (
		// 2.75.0 resolves jackson 2.18.3 (17 GHSAs, fixed in 2.18.11);
		// 2.76.0 resolves 2.22.4. Both still declare guava 33.6.0-jre and
		// opentelemetry-api 1.62.0.
		sdkFloor = "2.76.0"
		// GHSA-xxph-c9ww-hj94. The SDK needs the -jre flavour: the pom's
		// -android pin for grpc-java gives way to it.
		guavaFloor = "33.7.2"
		// GHSA-ccrj-gw8r-854m, tracestate parsing in opentelemetry-api.
		otelFloor = "1.67.0"
	)
	pom := filepath.Join(t.TempDir(), "pom.xml")
	script := `. ../cross-test/lib/java-compile.sh && write_java_compile_pom "$1" /nonexistent && add_java_gcs_archive_dependency "$1" && printf %s "$GCS_ARCHIVE_SDK_VERSION"`
	out, err := exec.Command("bash", "-c", script, "bash", pom).Output()
	if err != nil {
		t.Fatalf("writing the compile pom: %v\n%s", err, out)
	}
	sdk := string(out)
	data, err := os.ReadFile(pom)
	if err != nil {
		t.Fatal(err)
	}
	type dependency struct {
		GroupID    string `xml:"groupId"`
		ArtifactID string `xml:"artifactId"`
		Version    string `xml:"version"`
		Type       string `xml:"type"`
		Scope      string `xml:"scope"`
	}
	var project struct {
		Dependencies []dependency `xml:"dependencies>dependency"`
		Managed      []dependency `xml:"dependencyManagement>dependencies>dependency"`
	}
	if err := xml.Unmarshal(data, &project); err != nil {
		t.Fatalf("parsing %s: %v", pom, err)
	}
	find := func(deps []dependency, group, artifact string) []dependency {
		var found []dependency
		for _, d := range deps {
			if d.GroupID == group && d.ArtifactID == artifact {
				found = append(found, d)
			}
		}
		return found
	}

	if compareMavenVersions(sdk, sdkFloor) < 0 {
		t.Errorf("GCS_ARCHIVE_SDK_VERSION %s is below its floor %s", sdk, sdkFloor)
	}
	if got := find(project.Dependencies, "com.google.cloud", "google-cloud-storage"); len(got) != 1 || got[0].Version != sdk {
		t.Errorf("the pom declares google-cloud-storage %v, want one at %s", got, sdk)
	}
	guava := find(project.Managed, "com.google.guava", "guava")
	if len(guava) != 1 || !strings.HasSuffix(guava[0].Version, "-jre") || compareMavenVersions(guava[0].Version, guavaFloor) < 0 {
		t.Errorf("the pom pins guava %v under <dependencyManagement>, want one -jre at %s or above", guava, guavaFloor)
	}
	otel := find(project.Managed, "io.opentelemetry", "opentelemetry-bom")
	if len(otel) != 1 || otel[0].Type != "pom" || otel[0].Scope != "import" || compareMavenVersions(otel[0].Version, otelFloor) < 0 {
		t.Errorf("the pom imports opentelemetry-bom %v under <dependencyManagement>, want one at %s or above", otel, otelFloor)
	}

	for _, path := range []string{
		"../cross-test/proto-quorumwrite/dependencies.yaml",
		"../cross-test/proto-epochload/dependencies.yaml",
	} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		m := regexp.MustCompile(`(?m)^\s*com\.google\.cloud:google-cloud-storage:\s*(\S+)\s*$`).FindStringSubmatch(string(data))
		if m == nil || m[1] != sdk {
			t.Errorf("%s pins google-cloud-storage %v, want GCS_ARCHIVE_SDK_VERSION %s", path, m, sdk)
		}
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
