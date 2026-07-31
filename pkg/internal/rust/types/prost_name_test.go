package types

import (
	"testing"

	"gitlab.com/secondstarcavalier/schematic/tacplus/pkg/internal/common/strutil"
)

// TestProstSnakeNameDigitAcronym pins the digit-acronym behavior that prost/heck
// uses for names like "MintP2SToken": the trailing acronym letter is folded into
// the digit run ("p2s"), not split ("p2_s"). References built by the dispatcher
// and clients must match the prost-generated struct/fn names exactly.
func TestProstSnakeNameDigitAcronym(t *testing.T) {
	cases := map[string]struct {
		prostType string // expected prost struct name (toProstTypeName)
		prostSnk  string // expected prost snake (prostSnakeName)
	}{
		"MintP2SToken":        {"MintP2sToken", "mint_p2s_token"},
		"MintP2STokenRequest": {"MintP2sTokenRequest", "mint_p2s_token_request"},
		"S2SCredPair":         {"S2sCredPair", "s2s_cred_pair"},
		"C2SCredPair":         {"C2sCredPair", "c2s_cred_pair"},
	}
	for in, want := range cases {
		if got := toProstTypeName(in); got != want.prostType {
			t.Errorf("toProstTypeName(%q) = %q, want %q", in, got, want.prostType)
		}
		if got := prostSnakeName(in); got != want.prostSnk {
			t.Errorf("prostSnakeName(%q) = %q, want %q", in, got, want.prostSnk)
		}
	}
}

// TestProstSnakeNameStable guards the blast radius: for prost-stable names
// (everything that compiled before this fix), prostSnakeName must be identical
// to the old strutil.ToSnakeCase(name) the dispatcher used, so no existing
// identifier shifts.
func TestProstSnakeNameStable(t *testing.T) {
	stable := []string{
		"GetUserRequest", "GetJWKSRequest", "ListTenantsResponse",
		"OAuthProvider", "Oauth2Provider", "Sha256Digest", "TLSConfig",
		"MintServiceTokenRequest", "TenantConfig", "CreateSubTenantResponse",
	}
	for _, in := range stable {
		if got, old := prostSnakeName(in), strutil.ToSnakeCase(in); got != old {
			t.Errorf("prostSnakeName(%q) = %q but ToSnakeCase = %q (would shift an existing identifier)", in, got, old)
		}
	}
}

func TestTonicServiceNames(t *testing.T) {
	cases := map[string]struct {
		typeName   string
		moduleName string
	}{
		"ContractS2S":    {"ContractS2s", "contract_s2s"},
		"ContractC2S":    {"ContractC2s", "contract_c2s"},
		"OpenAIService":  {"OpenAiService", "open_ai_service"},
		"MintP2SService": {"MintP2sService", "mint_p2s_service"},
	}

	for in, want := range cases {
		if got := tonicServiceTypeName(in); got != want.typeName {
			t.Errorf("tonicServiceTypeName(%q) = %q, want %q", in, got, want.typeName)
		}
		if got := tonicServiceModuleName(in); got != want.moduleName {
			t.Errorf("tonicServiceModuleName(%q) = %q, want %q", in, got, want.moduleName)
		}
	}
}
