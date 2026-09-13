package types

import (
	"os"
	"strings"
	"testing"
)

// TestCreateNpxWrapperUsesExclusiveTempFile guards against a regression back
// to a fixed, world-writable /tmp path: on a shared host another user could
// plant or symlink a fixed path before tacplus writes to it. os.CreateTemp
// creates the file exclusively (O_EXCL), so concurrent calls must never
// collide on the same path.
func TestCreateNpxWrapperUsesExclusiveTempFile(t *testing.T) {
	path1, err := createNpxWrapper()
	if err != nil {
		t.Fatalf("createNpxWrapper: %v", err)
	}
	defer os.Remove(path1)

	path2, err := createNpxWrapper()
	if err != nil {
		t.Fatalf("createNpxWrapper: %v", err)
	}
	defer os.Remove(path2)

	if path1 == path2 {
		t.Fatalf("two calls returned the same path %q; wrapper is not exclusively created", path1)
	}
	if path1 == "/tmp/protoc-gen-es-wrapper.sh" {
		t.Fatalf("regression: fixed, predictable /tmp path returned")
	}

	for _, path := range []string{path1, path2} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %q: %v", path, err)
		}
		if info.Mode().Perm() != 0700 {
			t.Errorf("wrapper %q has mode %v, want 0700", path, info.Mode().Perm())
		}
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %q: %v", path, err)
		}
		if !strings.Contains(string(content), "protoc-gen-es") {
			t.Errorf("wrapper %q content missing expected exec line: %q", path, content)
		}
	}
}
