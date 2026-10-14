package types

import (
	"testing"

	"github.com/tacplus-dev/tacplus/pkg/internal/common/strutil"
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

// TestProstEnumVariantKeyword pins the escaping of enum variant names that
// collide with a Rust keyword. Rust keywords are lowercase, so the only
// collision a PascalCase variant can hit is "Self", and Rust rejects it as a
// raw identifier — prost suffixes an underscore rather than emitting r#Self.
// The generated helpers reference prost's variants by name, so this must match
// prost-build's sanitize_identifier exactly.
func TestProstEnumVariantKeyword(t *testing.T) {
	cases := []struct {
		protoValue string
		enumName   string
		want       string
	}{
		{"AUTH_METHOD_SELF", "AuthMethod", "Self_"},
		// Keyword-looking suffixes that are NOT collisions once cased.
		{"AUTH_METHOD_TYPE", "AuthMethod", "Type"},
		{"AUTH_METHOD_MATCH", "AuthMethod", "Match"},
		{"AUTH_METHOD_MOVE", "AuthMethod", "Move"},
		// "Self" only escapes when it is the whole variant, not a word in it.
		{"AUTH_METHOD_SELF_SIGNED", "AuthMethod", "SelfSigned"},
		{"AUTH_METHOD_PASSWORD", "AuthMethod", "Password"},
	}
	for _, tc := range cases {
		if got := toProstEnumVariant(tc.protoValue, tc.enumName); got != tc.want {
			t.Errorf("toProstEnumVariant(%q, %q) = %q, want %q", tc.protoValue, tc.enumName, got, tc.want)
		}
	}
}

// TestProstEnumVariantLeadingDigit pins the handling of enum values whose
// prefix-stripped name would start with a digit. prost never emits such a
// variant: strip_enum_prefix only strips when the remainder starts with an
// uppercase character, so AUTH_METHOD_2FA keeps the enum name (AuthMethod2fa),
// and sanitize_identifier underscore-prefixes anything that still starts with
// a digit (_2FA -> _2fa). See prost-build's src/ident.rs.
func TestProstEnumVariantLeadingDigit(t *testing.T) {
	cases := []struct {
		protoValue string
		enumName   string
		want       string
	}{
		{"AUTH_METHOD_2FA", "AuthMethod", "AuthMethod2fa"},
		{"AUTH_METHOD_2FA_TOTP", "AuthMethod", "AuthMethod2faTotp"},
		// No prefix to strip, and the name itself starts with a digit.
		{"_2FA", "AuthMethod", "_2fa"},
		// A digit that is not leading does not change anything.
		{"AUTH_METHOD_V2_TOKEN", "AuthMethod", "V2Token"},
		{"AUTH_METHOD_OAUTH2", "AuthMethod", "Oauth2"},
	}
	for _, tc := range cases {
		if got := toProstEnumVariant(tc.protoValue, tc.enumName); got != tc.want {
			t.Errorf("toProstEnumVariant(%q, %q) = %q, want %q", tc.protoValue, tc.enumName, got, tc.want)
		}
	}
}
