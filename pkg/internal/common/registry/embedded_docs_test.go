// Copyright (c) 2026 Second Star Cavalier LLC

package registry

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.com/seabeam/schematic/tacplus/docs"
)

// CC-6: USER.md and THEORY.md claimed registry.json carries "architectural norms
// from embedded agent.md files", implying a consumer could author an agent.md and
// have tacplus pick it up. There is no ingestion path: the docs map is populated
// only from tacplus's own embedded docs.FS (registry.go:288-310), so authoring one
// is a no-op. This pins the docs source — a real ingestion path would have to be
// added here, and the false claim can't drift back into the docs.
func TestEmbeddedRegistryDocsComeOnlyFromTacplusDocsFS(t *testing.T) {
	var want []string
	if err := fs.WalkDir(docs.FS, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(path, ".tmpl") {
			return err
		}
		want = append(want, path)
		return nil
	}); err != nil {
		t.Fatalf("walk docs.FS: %v", err)
	}
	if len(want) == 0 {
		t.Fatal("docs.FS is empty; the embedding walk would ship no documentation")
	}

	// AGENT.md is the one entry not read verbatim from docs.FS — it is rendered
	// from AGENT.md.tmpl against the registry itself.
	if _, err := fs.ReadFile(docs.FS, "AGENT.md.tmpl"); err != nil {
		t.Fatalf("AGENT.md.tmpl must live in docs.FS: %v", err)
	}

	// No generator source reads a consumer-authored agent.md.
	root := filepath.Join("..", "..", "..", "..")
	for _, dir := range []string{"pkg", "cmd"} {
		if err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			b, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			if strings.Contains(string(b), `"agent.md"`) {
				t.Errorf("%s reads a consumer-authored agent.md; the docs say tacplus does not — update the docs or this test", path)
			}
			return nil
		}); err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
}

// The corrected doc text must not regress to the old capability claim.
func TestDocsDoNotClaimConsumerAgentMdIngestion(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..", "docs")
	for _, name := range []string{"USER.md", "THEORY.md"} {
		b, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, banned := range []string{
			"embedded `agent.md` files",
			"live in `agent.md` files embedded into",
		} {
			if strings.Contains(string(b), banned) {
				t.Errorf("docs/%s says %q — tacplus has no ingestion path for consumer-authored agent.md (CC-6)", name, banned)
			}
		}
	}
}
