// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

package types

import (
	"strings"
	"testing"
)

// Issue #179: the python JWK must not guess an RSA key's alg from its size
// (RS384 for 2048-bit), and verify must check the token's alg against the
// key's accepted set rather than demand token alg == JWK alg. The behaviour is
// proven by cross-test/jwk-alg; this pins the template shape it depends on.
func TestPythonJwkAlgComesFromTheKey(t *testing.T) {
	jwk := readPythonTemplate(t, "python/chassis/jwk/jwk.py.tmpl")
	if strings.Contains(jwk, "_rsa_alg_for_key_size") {
		t.Errorf("jwk template must not pick an RSA alg from the key size")
	}
	for _, want := range []string{
		"def accepted_algs(self) -> tuple:",
		`_RSA_ALGS = ("RS256", "RS384", "RS512")`,
	} {
		if !strings.Contains(jwk, want) {
			t.Errorf("jwk template missing %q", want)
		}
	}

	tw := readPythonTemplate(t, "python/chassis/token/token_wrapper.py.tmpl")
	if strings.Contains(tw, "jwk.alg()") {
		t.Errorf("token verify must not compare against jwk.alg(); use jwk.accepted_algs()")
	}
	for _, want := range []string{
		"accepted = jwk.accepted_algs()",
		"not isinstance(header[_HEADER_ALG], str)",
		`if alg == "RS256":`,
	} {
		if !strings.Contains(tw, want) {
			t.Errorf("token_wrapper template missing %q", want)
		}
	}
}
