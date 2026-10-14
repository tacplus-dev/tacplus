package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

func TestGenerateW2SServiceManagerExposesGrpcAlias(t *testing.T) {
	outputDir := t.TempDir()
	if err := generateW2SServiceManager(&tactype.Config{OutputDir: outputDir}); err != nil {
		t.Fatalf("generateW2SServiceManager() failed: %v", err)
	}

	modPath := filepath.Join(outputDir, "w2sm", "mod.rs")
	modBytes, err := os.ReadFile(modPath)
	if err != nil {
		t.Fatalf("read w2sm mod.rs: %v", err)
	}
	mod := string(modBytes)

	for _, want := range []string{
		"pub fn w2s_clients(&self) -> &W2SClients",
		"pub fn grpc_clients(&self) -> &W2SClients",
		"pub fn grpc(&self) -> &W2SClients",
	} {
		if !strings.Contains(mod, want) {
			t.Fatalf("w2sm mod.rs missing %q:\n%s", want, mod)
		}
	}
}
