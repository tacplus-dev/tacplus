package types

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/tactype"
)

func TestGenerateTsW2SAggregatorExposesTypedHierarchy(t *testing.T) {
	outputDir := t.TempDir()
	cfg := &tactype.Config{
		OutputDir: outputDir,
		SmPrefix:  "w2sm",
	}
	services := []ServiceInfo{{
		ProtoFile:   "contract/contract.proto",
		ParentDir:   "contract",
		PackageName: "contractclient",
		ServiceName: "ContractApi",
		Config:      cfg,
	}}

	if err := generateTsGrpcOverRestAggregators(services, true); err != nil {
		t.Fatalf("generateTsGrpcOverRestAggregators() failed: %v", err)
	}

	rootPath := filepath.Join(outputDir, "w2sm", "internal", "grpc_over_rest", "grpc_over_rest.ts")
	rootBytes, err := os.ReadFile(rootPath)
	if err != nil {
		t.Fatalf("read root aggregator: %v", err)
	}
	root := string(rootBytes)

	for _, want := range []string{
		"export class ContractclientW2SClients",
		"private readonly _contract_api_client: ContractApiClient;",
		"contract_api_client(): ContractApiClient { return this._contract_api_client; }",
		"export class W2SClients",
		"contractclient(): ContractclientW2SClients { return this._contractclient; }",
	} {
		if !strings.Contains(root, want) {
			t.Fatalf("root aggregator missing %q:\n%s", want, root)
		}
	}
}
