// Copyright (c) 2026 Second Star Cavalier LLC

package runner

import (
	"os"
	"path/filepath"
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/projectfile"
	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/tacplusyaml"
)

func TestResolveProtoTag(t *testing.T) {
	tests := []struct {
		name      string
		tag       string
		wantVer   string
		wantLocal bool
	}{
		{name: "latest", tag: "latest", wantVer: "0.0.0-SNAPSHOT", wantLocal: true},
		{name: "semver full", tag: "1.2.3", wantVer: "1.2.3", wantLocal: false},
		{name: "semver full with v", tag: "v1.2.3", wantVer: "1.2.3", wantLocal: false},
		{name: "semver minor", tag: "1.2", wantVer: "1.2", wantLocal: false},
		{name: "semver minor with v", tag: "v1.2", wantVer: "1.2", wantLocal: false},
		{name: "branch name", tag: "some-branch", wantVer: "some-branch", wantLocal: false},
		{name: "empty string", tag: "", wantVer: "", wantLocal: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ver, local := resolveProtoTag(tt.tag)
			if ver != tt.wantVer {
				t.Errorf("resolveProtoTag(%q) version = %q, want %q", tt.tag, ver, tt.wantVer)
			}
			if local != tt.wantLocal {
				t.Errorf("resolveProtoTag(%q) local = %v, want %v", tt.tag, local, tt.wantLocal)
			}
		})
	}
}

func Test_canonicalPkgToNative(t *testing.T) {
	tests := []struct {
		name       string
		canonical  string
		lang       string
		outputName string // defaults to "tacplus" when empty
		want       string
	}{
		{
			name:      "Java passthrough",
			canonical: "com.gitlab.example.services.identity",
			lang:      "java",
			want:      "com.gitlab.example.services.identity.tacplus",
		},
		// Model rename (§7): output-name "model" replaces the trailing segment.
		{
			name:       "Java model rename",
			canonical:  "com.gitlab.example.services.identity",
			lang:       "java",
			outputName: "model",
			want:       "com.gitlab.example.services.identity.model",
		},
		{
			name:       "Go model rename",
			canonical:  "com.gitlab.example.services.oauth",
			lang:       "go",
			outputName: "model",
			want:       "gitlab.com/example/services/oauth/model",
		},
		{
			name:       "Rust model rename",
			canonical:  "com.gitlab.example.services.gateway",
			lang:       "rust",
			outputName: "model",
			want:       "gateway-model",
		},
		{
			name:       "Python model rename",
			canonical:  "com.gitlab.example.mock.kitchensink",
			lang:       "python",
			outputName: "model",
			want:       "kitchensink_model",
		},
		{
			name:      "Go reverse DNS to module path",
			canonical: "com.gitlab.example.services.oauth",
			lang:      "go",
			want:      "gitlab.com/example/services/oauth/tacplus",
		},
		{
			name:      "Rust last segment",
			canonical: "com.gitlab.example.services.gateway",
			lang:      "rust",
			want:      "gateway-tacplus",
		},
		{
			name:      "TypeScript last segment",
			canonical: "com.gitlab.example.clients.webapp",
			lang:      "typescript",
			want:      "webapp-tacplus",
		},
		{
			name:      "Python last segment with underscore",
			canonical: "com.gitlab.example.mock.kitchensink",
			lang:      "python",
			want:      "kitchensink_tacplus",
		},
		{
			// A two-segment canonical pkg has no path remainder after the reversed
			// host. Joining blindly used to emit an empty path element
			// ("example.com//model"), which is not a legal Go import path — every
			// generated import in the tree would carry it (CON-6).
			name:       "Go two-segment pkg has no empty path element",
			canonical:  "com.example",
			lang:       "go",
			outputName: "model",
			want:       "example.com/model",
		},
		{
			name:      "Go two-segment pkg, default output name",
			canonical: "org.acme",
			lang:      "go",
			want:      "acme.org/tacplus",
		},
		{
			name:      "Go nested path",
			canonical: "com.gitlab.example.services.aegis.mock",
			lang:      "go",
			want:      "gitlab.com/example/services/aegis/mock/tacplus",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			on := tt.outputName
			if on == "" {
				on = "tacplus"
			}
			got := canonicalPkgToNative(tt.canonical, tt.lang, on)
			if got != tt.want {
				t.Errorf("canonicalPkgToNative(%q, %q, %q) = %q, want %q", tt.canonical, tt.lang, on, got, tt.want)
			}
		})
	}
}

func Test_validateCanonicalPkg(t *testing.T) {
	tests := []struct {
		name      string
		canonical string
		lang      string
		wantErr   bool
	}{
		{"valid reverse dns", "com.gitlab.example.services.identity", "rust", false},
		{"valid single segment", "identity", "go", false},
		{"valid with underscore", "com.example.gitlab_connector", "python", false},
		{"hyphen segment rust", "com.gitlab.example.services.gitlab-connector", "rust", true},
		{"hyphen segment java", "com.example.foo-bar", "java", true},
		{"hyphen segment go", "com.example.foo-bar", "go", true},
		{"leading digit", "com.example.9service", "go", true},
		{"trailing dot", "com.example.identity.", "rust", true},
		{"leading dot", ".com.example.identity", "rust", true},
		{"consecutive dots", "com..example", "go", true},
		{"space in segment", "com.example.my service", "typescript", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateCanonicalPkg(tt.canonical, tt.lang)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateCanonicalPkg(%q, %q) error = %v, wantErr %v", tt.canonical, tt.lang, err, tt.wantErr)
			}
		})
	}
}

// Test_hashConfig_overlayChangeInvalidatesCache is the H2 regression: the build
// hash must be derived from the fully MERGED config (all -c overlays merged,
// import: bases resolved), not a single re-read of cwd/tacplus.yaml. Otherwise a
// change confined to an overlay (or an imported base) leaves ConfigHash unchanged
// → cache hit → stale generated output.
func Test_hashConfig_overlayChangeInvalidatesCache(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
		return p
	}

	base := write("base.yaml", "language: go\noutput-dir: model\npkg: com.example.foo\n")
	overlayA := write("overlay_a.yaml", "output-name: modelA\n")
	overlayB := write("overlay_b.yaml", "output-name: modelB\n")

	mergedA, err := tacplusyaml.ReadFiles([]string{base, overlayA})
	if err != nil {
		t.Fatalf("ReadFiles A: %v", err)
	}
	mergedB, err := tacplusyaml.ReadFiles([]string{base, overlayB})
	if err != nil {
		t.Fatalf("ReadFiles B: %v", err)
	}

	hashA, err := hashConfig(mergedA)
	if err != nil {
		t.Fatalf("hashConfig A: %v", err)
	}
	hashB, err := hashConfig(mergedB)
	if err != nil {
		t.Fatalf("hashConfig B: %v", err)
	}

	// The only difference lives in the overlay — the hash must still diverge, or
	// the cache would serve stale code for the second build.
	if hashA == hashB {
		t.Errorf("hashConfig must differ when an overlay value changes, but both were %q", hashA)
	}

	// Same merged config must hash identically (deterministic, cache-hittable).
	mergedARepeat, err := tacplusyaml.ReadFiles([]string{base, overlayA})
	if err != nil {
		t.Fatalf("ReadFiles A repeat: %v", err)
	}
	hashARepeat, err := hashConfig(mergedARepeat)
	if err != nil {
		t.Fatalf("hashConfig A repeat: %v", err)
	}
	if hashA != hashARepeat {
		t.Errorf("hashConfig must be deterministic for identical merged config: %q != %q", hashA, hashARepeat)
	}
}

// Test_hashConfig_pathIndependence guards the existing basename-normalization:
// two configs that differ only in the directory prefix of path-bearing fields
// (Src/OutputDir/Import) — but share basenames — must hash identically, so a
// moved/renamed build dir is not a spurious cache miss.
func Test_hashConfig_pathIndependence(t *testing.T) {
	a := &tacplusyaml.Config{Language: "go", Src: "/home/alice/proto", OutputDir: "/home/alice/proj/model"}
	b := &tacplusyaml.Config{Language: "go", Src: "/opt/build/proto", OutputDir: "/opt/build/proj/model"}

	hashA, err := hashConfig(a)
	if err != nil {
		t.Fatalf("hashConfig a: %v", err)
	}
	hashB, err := hashConfig(b)
	if err != nil {
		t.Fatalf("hashConfig b: %v", err)
	}
	if hashA != hashB {
		t.Errorf("hashConfig must be location-independent (same basenames): %q != %q", hashA, hashB)
	}

	// A genuine semantic change (different pkg) must still diverge.
	c := &tacplusyaml.Config{Language: "go", Src: "/home/alice/proto", OutputDir: "/home/alice/proj/model", Pkg: "com.example.bar"}
	hashC, err := hashConfig(c)
	if err != nil {
		t.Fatalf("hashConfig c: %v", err)
	}
	if hashA == hashC {
		t.Errorf("hashConfig must diverge on a semantic (pkg) change, both were %q", hashA)
	}
}

func Test_resolveOutputDir(t *testing.T) {
	cwd := "/home/user/project"

	tests := []struct {
		name        string
		projectType projectfile.ProjectType
		want        string
	}{
		{
			name:        "Java",
			projectType: projectfile.ProjectJava,
			want:        filepath.Join(cwd, "tacplus"),
		},
		{
			name:        "Go",
			projectType: projectfile.ProjectGo,
			want:        filepath.Join(cwd, "tacplus"),
		},
		{
			name:        "TypeScript",
			projectType: projectfile.ProjectTypeScript,
			want:        filepath.Join(cwd, "tacplus"),
		},
		{
			name:        "Python",
			projectType: projectfile.ProjectPython,
			want:        filepath.Join(cwd, "tacplus"),
		},
		{
			name:        "Rust",
			projectType: projectfile.ProjectRust,
			want:        filepath.Join(cwd, "tacplus"),
		},
		{
			name:        "Unknown",
			projectType: projectfile.ProjectUnknown,
			want:        filepath.Join(cwd, "tacplus"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveOutputDir(cwd, tt.projectType, "tacplus")
			if got != tt.want {
				t.Errorf("resolveOutputDir(%q, %v) = %q, want %q", cwd, tt.projectType, got, tt.want)
			}
		})
	}
}

// Test_goModuleBasePath is the CNA-001 regression: the Go import root must match
// the on-disk layout. When output-dir nests the generated tree below a directory
// segment not present in the canonical pkg, the import root must carry that
// prefix or generated imports resolve to a nonexistent module path and Go tries
// to fetch them remotely.
func Test_goModuleBasePath(t *testing.T) {
	cwd := "/home/user/contractclient"
	// pkg as produced by canonicalPkgToNative for the consumer-contract fixture.
	const pkg = "gitlab.com/example/tacplus/consumer/contractclient/model"

	tests := []struct {
		name       string
		outDir     string
		outputName string
		want       string
	}{
		{
			// Default layout: output-dir unset → cwd/model. Import root unchanged.
			name:       "default model layout",
			outDir:     filepath.Join(cwd, "model"),
			outputName: "model",
			want:       pkg,
		},
		{
			// output-dir: "." → cwd/model (operator/graphdb). Import root unchanged.
			name:       "output-dir dot",
			outDir:     filepath.Join(cwd, "model"),
			outputName: "model",
			want:       pkg,
		},
		{
			// CNA-001: output-dir: "tacplus" + default output-name "model" → files at
			// cwd/tacplus/model, reachable as <module>/tacplus/model. The import root
			// must carry the "tacplus" prefix.
			name:       "output-dir tacplus nests below module root",
			outDir:     filepath.Join(cwd, "tacplus", "model"),
			outputName: "model",
			want:       "gitlab.com/example/tacplus/consumer/contractclient/tacplus/model",
		},
		{
			// Multi-segment output-dir.
			name:       "output-dir gen/proto nests two segments",
			outDir:     filepath.Join(cwd, "gen", "proto", "model"),
			outputName: "model",
			want:       "gitlab.com/example/tacplus/consumer/contractclient/gen/proto/model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := goModuleBasePath(pkg, cwd, tt.outDir, tt.outputName)
			if got != tt.want {
				t.Errorf("goModuleBasePath(%q, %q, %q, %q) = %q, want %q",
					pkg, cwd, tt.outDir, tt.outputName, got, tt.want)
			}
		})
	}
}
