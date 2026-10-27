// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// The go leg of the jwk-alg gate (issue #179): the generated JWK and token
// chassis against cross-test/fixtures/jwk_alg_vectors.json. run.sh copies this
// file into the generated chassis/token package.
package token_test

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	wrapperJwk "gitlab.com/example/tacplus/jwkalg/jwkalgclient/model/chassis/jwk"
	wrapperToken "gitlab.com/example/tacplus/jwkalg/jwkalgclient/model/chassis/token"
	jwkpb "gitlab.com/example/tacplus/jwkalg/jwkalgclient/model/types/sys/jwk"
	keypb "gitlab.com/example/tacplus/jwkalg/jwkalgclient/model/types/sys/key"
)

type vectors struct {
	Jwks  map[string][]map[string]any `json:"jwks"`
	Cases []struct {
		Name   string `json:"name"`
		Key    string `json:"key"`
		Token  string `json:"token"`
		Expect string `json:"expect"`
	} `json:"cases"`
}

func loadVectors(t *testing.T) (vectors, map[string]*jwkpb.Jwk) {
	t.Helper()
	path := os.Getenv("JWK_ALG_VECTOR_FIXTURE")
	if path == "" {
		t.Fatal("JWK_ALG_VECTOR_FIXTURE is not set")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var v vectors
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	str := func(m map[string]any, k string) string { s, _ := m[k].(string); return s }
	keys := map[string]*jwkpb.Jwk{}
	for _, set := range v.Jwks {
		for _, m := range set {
			kty := map[string]keypb.KeyType{"RSA": keypb.KeyType_KEY_TYPE_RSA, "EC": keypb.KeyType_KEY_TYPE_EC, "OKP": keypb.KeyType_KEY_TYPE_OKP}[str(m, "kty")]
			use := jwkpb.JwkUseType_JWK_USE_TYPE_UNDEFINED
			if str(m, "use") == "sig" {
				use = jwkpb.JwkUseType_JWK_USE_TYPE_SIG
			}
			keys[str(m, "kid")] = &jwkpb.Jwk{
				Kty: kty, Alg: str(m, "alg"), Kid: str(m, "kid"), Use: use,
				N: str(m, "n"), E: str(m, "e"), Crv: str(m, "crv"), X: str(m, "x"), Y: str(m, "y"),
			}
		}
	}
	if len(v.Cases) < 100 {
		t.Fatalf("fixture has %d cases; expected the full set", len(v.Cases))
	}
	return v, keys
}

// verify builds the JWK and verifies the token against it; any refusal on the
// way (the JWK, the token's parse, its verification) is a reject.
func verify(key *jwkpb.Jwk, token string) error {
	jwk, err := wrapperJwk.NewImmutableJwk(key)
	if err != nil {
		return err
	}
	tw, err := wrapperToken.ParseToken(token)
	if err != nil {
		return err
	}
	return tw.Verify(jwk)
}

func TestJwkAlgVectors(t *testing.T) {
	v, keys := loadVectors(t)
	accepted := 0
	for _, c := range v.Cases {
		key, ok := keys[c.Key]
		if !ok {
			t.Fatalf("%s: fixture has no key %q", c.Name, c.Key)
		}
		err := verify(key, c.Token)
		switch {
		case c.Expect == "accept" && err != nil:
			t.Errorf("%s: expected accept, got %v", c.Name, err)
		case c.Expect == "reject" && err == nil:
			t.Errorf("%s: expected reject, but the token verified", c.Name)
		case c.Expect == "accept":
			accepted++
		}
	}
	t.Logf("jwk-alg go: %d cases, %d accepted", len(v.Cases), accepted)
}

// The accepted set comes from the key, narrowed to a declared alg; the alg a
// JWK reports is the one it declares, never a guess.
func TestJwkAcceptedAlgs(t *testing.T) {
	_, keys := loadVectors(t)
	for kid, want := range map[string][]string{
		"rsa-noalg":     {"RS256", "RS384", "RS512"},
		"rsa-rs256":     {"RS256"},
		"ms-noalg":      {"RS256", "RS384", "RS512"},
		"ec-p256-noalg": {"ES256"},
		"ec-p384-noalg": {"ES384"},
		"okp-noalg":     {"EdDSA"},
		"okp-eddsa":     {"EdDSA"},
	} {
		jwk, err := wrapperJwk.NewImmutableJwk(keys[kid])
		if err != nil {
			t.Fatalf("%s: %v", kid, err)
		}
		if got := jwk.AcceptedAlgs(); !slices.Equal(got, want) {
			t.Errorf("%s: AcceptedAlgs() = %v, want %v", kid, got, want)
		}
		if got := jwk.GetAlg(); got != keys[kid].Alg {
			t.Errorf("%s: GetAlg() = %q, want the declared %q", kid, got, keys[kid].Alg)
		}
	}
}
