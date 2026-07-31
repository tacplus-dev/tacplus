package types

import "testing"

func TestTsGeneratedSnakeNameDigitAcronym(t *testing.T) {
	tests := map[string]string{
		"ContractS2S":     "contract_s2s",
		"ContractC2S":     "contract_c2s",
		"MintP2SService":  "mint_p2s_service",
		"IdentityService": "identity_service",
	}
	for input, want := range tests {
		if got := tsGeneratedSnakeName(input); got != want {
			t.Fatalf("tsGeneratedSnakeName(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestTsClientSymbolsUseGeneratedSnakeName(t *testing.T) {
	symbols := tsClientSymbols([]tsClientIdentity{{
		ParentDir:   "contract",
		PackageName: "contractclient",
		ServiceName: "ContractS2S",
	}}, "GrpcClient")
	if len(symbols) != 1 {
		t.Fatalf("got %d symbols, want 1", len(symbols))
	}
	if got, want := symbols[0].FieldName, "contract_s2s_client"; got != want {
		t.Fatalf("FieldName = %q, want %q", got, want)
	}
}
