// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Command genvectors writes cross-test/fixtures/jwk_alg_vectors.json, the one
// fixture every generated language's JWK alg-selection test reads (issue #179).
//
// Every token is signed here, once, so no language test signs anything: each
// builds the case's JWK, verifies the case's token and must reach the case's
// expect. The keys are throwaway and generated fresh on every run; only their
// public halves are written, so re-running replaces the whole fixture.
//
//	go run ./cross-test/jwk-alg/genvectors
package main

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"hash"
	"math/big"
	"os"
	"sort"
	"strings"
	"time"
)

// exp is 2100-01-01T00:00:00Z: far enough out that the fixture never expires.
const exp = 4102444800

const iat = 1767225600 // 2026-01-01T00:00:00Z

type keySet struct {
	rsa      *rsa.PrivateKey
	rsaOther *rsa.PrivateKey
	ms       *rsa.PrivateKey
	p256     *ecdsa.PrivateKey
	p384     *ecdsa.PrivateKey
	okp      ed25519.PrivateKey
}

type fixtureCase struct {
	Name   string `json:"name"`
	Key    string `json:"key"`
	Token  string `json:"token"`
	Expect string `json:"expect"`
}

type fixture struct {
	Comment string           `json:"_comment"`
	Jwks    map[string][]any `json:"jwks"`
	Cases   []fixtureCase    `json:"cases"`
}

func main() {
	out := flag.String("out", "cross-test/fixtures/jwk_alg_vectors.json", "fixture path to write")
	flag.Parse()
	if err := run(*out); err != nil {
		fmt.Fprintln(os.Stderr, "genvectors:", err)
		os.Exit(1)
	}
}

func run(out string) error {
	k, err := newKeys()
	if err != nil {
		return err
	}
	msCert, err := selfSignedDER(k.ms)
	if err != nil {
		return err
	}

	b64 := base64.RawURLEncoding.EncodeToString
	rsaJwk := func(kid, alg string, pub *rsa.PublicKey) map[string]any {
		m := map[string]any{"kty": "RSA", "kid": kid, "use": "sig", "n": b64(pub.N.Bytes()), "e": b64(big.NewInt(int64(pub.E)).Bytes())}
		if alg != "" {
			m["alg"] = alg
		}
		return m
	}
	ecJwk := func(kid, alg string, pub *ecdsa.PublicKey) map[string]any {
		size := (pub.Curve.Params().BitSize + 7) / 8
		m := map[string]any{"kty": "EC", "kid": kid, "use": "sig", "crv": pub.Curve.Params().Name, "x": b64(pub.X.FillBytes(make([]byte, size))), "y": b64(pub.Y.FillBytes(make([]byte, size)))}
		if alg != "" {
			m["alg"] = alg
		}
		return m
	}
	okpJwk := func(kid, alg string, pub ed25519.PublicKey) map[string]any {
		m := map[string]any{"kty": "OKP", "kid": kid, "use": "sig", "crv": "Ed25519", "x": b64(pub)}
		if alg != "" {
			m["alg"] = alg
		}
		return m
	}
	x5t := sha1.Sum(msCert)

	f := fixture{
		Comment: "JWK alg-selection vectors (issue #179), written by `go run ./cross-test/jwk-alg/genvectors`; do not edit by hand. " +
			"Each case names a key in `jwks` (by kid) and a signed token; a language test builds that JWK, verifies the token against it, and must reach `expect` " +
			"(reject: the JWK is refused, the token does not parse, or verification fails). The accepted algs come from the key: RSA RS256/RS384/RS512, " +
			"EC by curve (P-256 ES256, P-384 ES384), OKP Ed25519 EdDSA, narrowed to the key's alg when it declares one; never none, HMAC or PS*. " +
			"`jwks.microsoft` is shaped like https://login.microsoftonline.com/common/discovery/v2.0/keys (no alg; x5t, x5c, issuer). Throwaway keys; exp is 2100-01-01.",
		Jwks: map[string][]any{
			"test": {
				rsaJwk("rsa-noalg", "", &k.rsa.PublicKey),
				rsaJwk("rsa-rs256", "RS256", &k.rsa.PublicKey),
				rsaJwk("rsa-rs512", "RS512", &k.rsa.PublicKey),
				ecJwk("ec-p256-noalg", "", &k.p256.PublicKey),
				ecJwk("ec-p256-es256", "ES256", &k.p256.PublicKey),
				ecJwk("ec-p384-noalg", "", &k.p384.PublicKey),
				okpJwk("okp-noalg", "", k.okp.Public().(ed25519.PublicKey)),
				okpJwk("okp-eddsa", "EdDSA", k.okp.Public().(ed25519.PublicKey)),
				// Keys whose declared alg is outside their family, or weak: refused outright.
				rsaJwk("rsa-declared-es256", "ES256", &k.rsa.PublicKey),
				rsaJwk("rsa-declared-ps256", "PS256", &k.rsa.PublicKey),
				rsaJwk("rsa-declared-none", "none", &k.rsa.PublicKey),
				rsaJwk("rsa-declared-hs256", "HS256", &k.rsa.PublicKey),
				ecJwk("ec-p256-declared-rs256", "RS256", &k.p256.PublicKey),
				ecJwk("ec-p256-declared-es384", "ES384", &k.p256.PublicKey),
				okpJwk("okp-declared-es256", "ES256", k.okp.Public().(ed25519.PublicKey)),
			},
			"microsoft": {
				map[string]any{
					"kty":    "RSA",
					"use":    "sig",
					"kid":    "ms-noalg",
					"x5t":    b64(x5t[:]),
					"n":      b64(k.ms.N.Bytes()),
					"e":      b64(big.NewInt(int64(k.ms.E)).Bytes()),
					"x5c":    []string{base64.StdEncoding.EncodeToString(msCert)},
					"issuer": "https://login.microsoftonline.com/{tenantid}/v2.0",
				},
			},
		},
	}

	add := func(name, key, token, expect string) {
		f.Cases = append(f.Cases, fixtureCase{Name: name, Key: key, Token: token, Expect: expect})
	}
	sign := func(alg, kid string, header map[string]any) (string, error) {
		return signToken(k, alg, kid, header)
	}
	must := func(tok string, err error) string {
		if err != nil {
			panic(err)
		}
		return tok
	}
	hdr := func(alg any, kid string) map[string]any { return map[string]any{"typ": "JWT", "alg": alg, "kid": kid} }

	// Accepted: a key with no alg verifies every alg of its family it signed.
	for _, alg := range []string{"RS256", "RS384", "RS512"} {
		add("no-alg RSA key accepts "+alg, "rsa-noalg", must(sign(alg, "rsa-noalg", nil)), "accept")
	}
	add("declared RS256 key accepts RS256", "rsa-rs256", must(sign("RS256", "rsa-rs256", nil)), "accept")
	add("declared RS512 key accepts RS512", "rsa-rs512", must(sign("RS512", "rsa-rs512", nil)), "accept")
	add("no-alg EC P-256 key accepts ES256", "ec-p256-noalg", must(sign("ES256", "ec-p256-noalg", nil)), "accept")
	add("declared ES256 key accepts ES256", "ec-p256-es256", must(sign("ES256", "ec-p256-es256", nil)), "accept")
	add("no-alg EC P-384 key accepts ES384", "ec-p384-noalg", must(sign("ES384", "ec-p384-noalg", nil)), "accept")
	add("no-alg OKP key accepts EdDSA", "okp-noalg", must(sign("EdDSA", "okp-noalg", nil)), "accept")
	add("declared EdDSA key accepts EdDSA", "okp-eddsa", must(sign("EdDSA", "okp-eddsa", nil)), "accept")
	add("Microsoft-shaped no-alg key accepts RS256", "ms-noalg", must(sign("MS-RS256", "ms-noalg", nil)), "accept")

	// Sanity: a signature by another key, or a tampered one, never verifies.
	add("RS256 signed by another RSA key", "rsa-noalg", must(sign("OTHER-RS256", "rsa-noalg", nil)), "reject")
	add("RS256 with a tampered payload", "rsa-noalg", tamper(must(sign("RS256", "rsa-noalg", nil))), "reject")
	add("Microsoft-shaped key, RS384 token by another key", "ms-noalg", must(sign("RS384", "ms-noalg", nil)), "reject")

	// 1. alg none, every case, empty or absent signature, against no-alg and declared keys.
	for _, alg := range []string{"none", "None", "NONE", "nOnE"} {
		for _, key := range []string{"rsa-noalg", "rsa-rs256", "ec-p256-noalg", "okp-noalg", "okp-eddsa"} {
			unsigned := unsignedToken(hdr(alg, key))
			add(fmt.Sprintf("alg %q, empty signature, key %s", alg, key), key, unsigned+".", "reject")
			add(fmt.Sprintf("alg %q, absent signature, key %s", alg, key), key, unsigned, "reject")
			// A real RS256/ES256/EdDSA signature does not make "none" acceptable either.
			add(fmt.Sprintf("alg %q, signature of the key's own alg, key %s", alg, key), key, must(sign(ownAlg(key), key, hdr(alg, key))), "reject")
		}
	}

	// 2. header alg missing, empty, or not a string (each signed with RS256 by the key).
	for _, tc := range []struct {
		name string
		alg  any
		omit bool
	}{
		{name: "missing", omit: true},
		{name: "empty", alg: ""},
		{name: "null", alg: nil},
		{name: "number", alg: 256},
		{name: "array", alg: []string{"RS256"}},
		{name: "object", alg: map[string]string{"alg": "RS256"}},
		{name: "boolean", alg: true},
	} {
		h := hdr(tc.alg, "rsa-noalg")
		if tc.omit {
			delete(h, "alg")
		}
		add("header alg "+tc.name+", no-alg RSA key", "rsa-noalg", must(sign("RS256", "rsa-noalg", h)), "reject")
	}

	// 3. key confusion: HMAC keyed with the public key's own bytes.
	rsaSecrets, err := publicSecrets(&k.rsa.PublicKey)
	if err != nil {
		return err
	}
	rsaSecrets["jwk n (base64url text)"] = []byte(b64(k.rsa.N.Bytes()))
	rsaSecrets["jwk n (decoded bytes)"] = k.rsa.N.Bytes()
	ecSecrets, err := publicSecrets(&k.p256.PublicKey)
	if err != nil {
		return err
	}
	okpSecrets, err := publicSecrets(k.okp.Public())
	if err != nil {
		return err
	}
	okpSecrets["jwk x (base64url text)"] = []byte(b64(k.okp.Public().(ed25519.PublicKey)))
	for _, group := range []struct {
		keys    []string
		secrets map[string][]byte
	}{
		{keys: []string{"rsa-noalg", "rsa-rs256"}, secrets: rsaSecrets},
		{keys: []string{"ec-p256-noalg"}, secrets: ecSecrets},
		{keys: []string{"okp-noalg"}, secrets: okpSecrets},
	} {
		for _, key := range group.keys {
			for _, secretName := range sortedKeys(group.secrets) {
				for _, alg := range []string{"HS256", "HS384", "HS512"} {
					tok := hmacToken(alg, key, group.secrets[secretName])
					add(fmt.Sprintf("%s keyed with the public key as %s, key %s", alg, secretName, key), key, tok, "reject")
				}
			}
		}
	}

	// 4. family crossing (each token validly signed by a key of the token's family).
	for _, tc := range []struct{ alg, key string }{
		{"ES256", "rsa-noalg"}, {"ES384", "rsa-noalg"}, {"EdDSA", "rsa-noalg"},
		{"RS256", "ec-p256-noalg"}, {"RS384", "ec-p256-noalg"}, {"RS512", "ec-p256-noalg"},
		{"EdDSA", "ec-p256-noalg"}, {"ES384", "ec-p256-noalg"}, {"ES256", "ec-p384-noalg"},
		{"RS256", "okp-noalg"}, {"ES256", "okp-noalg"},
	} {
		add(fmt.Sprintf("%s token against %s", tc.alg, tc.key), tc.key, must(sign(tc.alg, tc.key, nil)), "reject")
	}

	// 5. the key declares a different alg of the same family.
	add("declared RS256 key, RS512 token", "rsa-rs256", must(sign("RS512", "rsa-rs256", nil)), "reject")
	add("declared RS256 key, RS384 token", "rsa-rs256", must(sign("RS384", "rsa-rs256", nil)), "reject")
	add("declared RS512 key, RS256 token", "rsa-rs512", must(sign("RS256", "rsa-rs512", nil)), "reject")

	// A declared alg outside the key's family (or weak) refuses the key itself.
	add("RSA key declaring ES256, RS256 token", "rsa-declared-es256", must(sign("RS256", "rsa-declared-es256", nil)), "reject")
	add("RSA key declaring ES256, ES256 token", "rsa-declared-es256", must(sign("ES256", "rsa-declared-es256", nil)), "reject")
	add("RSA key declaring PS256, PS256 token", "rsa-declared-ps256", must(sign("PS256", "rsa-declared-ps256", nil)), "reject")
	add("RSA key declaring none, unsigned none token", "rsa-declared-none", unsignedToken(hdr("none", "rsa-declared-none"))+".", "reject")
	add("RSA key declaring HS256, HS256 token keyed with n", "rsa-declared-hs256", hmacToken("HS256", "rsa-declared-hs256", []byte(b64(k.rsa.N.Bytes()))), "reject")
	add("EC key declaring RS256, ES256 token", "ec-p256-declared-rs256", must(sign("ES256", "ec-p256-declared-rs256", nil)), "reject")
	add("P-256 key declaring ES384, ES256 token", "ec-p256-declared-es384", must(sign("ES256", "ec-p256-declared-es384", nil)), "reject")
	add("OKP key declaring ES256, EdDSA token", "okp-declared-es256", must(sign("EdDSA", "okp-declared-es256", nil)), "reject")

	// 6. unknown or unsupported algs, signed where a signature is possible.
	for _, alg := range []string{"PS256", "PS384", "PS512"} {
		add(alg+" token (RSA-PSS, not supported), no-alg RSA key", "rsa-noalg", must(sign(alg, "rsa-noalg", nil)), "reject")
	}
	add("RS1 token (RSA SHA-1), no-alg RSA key", "rsa-noalg", must(sign("RS1", "rsa-noalg", nil)), "reject")
	for _, alg := range []string{"rs256", "Rs256", "RS256 ", " RS256", "RS 256", "RSA256", "RS255", "RS2560", "garbage", "ES256K", "Ed25519"} {
		add(fmt.Sprintf("alg %q (RS256 signature), no-alg RSA key", alg), "rsa-noalg", must(sign("RS256", "rsa-noalg", hdr(alg, "rsa-noalg"))), "reject")
	}
	for _, alg := range []string{"es256", "ES512", "ES256K", "garbage"} {
		add(fmt.Sprintf("alg %q (ES256 signature), no-alg EC P-256 key", alg), "ec-p256-noalg", must(sign("ES256", "ec-p256-noalg", hdr(alg, "ec-p256-noalg"))), "reject")
	}
	for _, alg := range []string{"eddsa", "Ed25519", "Ed448", "garbage"} {
		add(fmt.Sprintf("alg %q (EdDSA signature), no-alg OKP key", alg), "okp-noalg", must(sign("EdDSA", "okp-noalg", hdr(alg, "okp-noalg"))), "reject")
	}

	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := os.WriteFile(out, data, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s: %d cases\n", out, len(f.Cases))
	return nil
}

func newKeys() (*keySet, error) {
	var k keySet
	var err error
	if k.rsa, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		return nil, err
	}
	if k.rsaOther, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		return nil, err
	}
	if k.ms, err = rsa.GenerateKey(rand.Reader, 2048); err != nil {
		return nil, err
	}
	if k.p256, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader); err != nil {
		return nil, err
	}
	if k.p384, err = ecdsa.GenerateKey(elliptic.P384(), rand.Reader); err != nil {
		return nil, err
	}
	if _, k.okp, err = ed25519.GenerateKey(rand.Reader); err != nil {
		return nil, err
	}
	return &k, nil
}

// ownAlg is the alg a test key signs with by default.
func ownAlg(kid string) string {
	switch {
	case kid == "ms-noalg":
		return "MS-RS256"
	case strings.HasPrefix(kid, "rsa"):
		return "RS256"
	case strings.HasPrefix(kid, "ec-p384"):
		return "ES384"
	case strings.HasPrefix(kid, "ec"):
		return "ES256"
	default:
		return "EdDSA"
	}
}

func claims() map[string]any {
	return map[string]any{"iss": "https://idp.example.com", "aud": "client-id", "sub": "subject", "iat": iat, "exp": exp}
}

func encodeSegment(v any) string {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes.TrimRight(buf.Bytes(), "\n"))
}

func unsignedToken(header map[string]any) string {
	return encodeSegment(header) + "." + encodeSegment(claims())
}

// signToken signs with the key family named by how (RS*, PS*, ES*, EdDSA,
// RS1, OTHER-RS256 = another RSA key, MS-RS256 = the Microsoft-shaped key).
// header defaults to {typ, alg: how, kid}.
func signToken(k *keySet, how, kid string, header map[string]any) (string, error) {
	if header == nil {
		alg := how
		switch how {
		case "OTHER-RS256", "MS-RS256":
			alg = "RS256"
		}
		header = map[string]any{"typ": "JWT", "alg": alg, "kid": kid}
	}
	input := unsignedToken(header)
	digest := func(h crypto.Hash) []byte {
		hh := h.New()
		hh.Write([]byte(input))
		return hh.Sum(nil)
	}
	var sig []byte
	var err error
	switch how {
	case "RS256", "RS384", "RS512", "OTHER-RS256", "MS-RS256", "RS1":
		key, h := k.rsa, map[string]crypto.Hash{"RS256": crypto.SHA256, "RS384": crypto.SHA384, "RS512": crypto.SHA512, "OTHER-RS256": crypto.SHA256, "MS-RS256": crypto.SHA256, "RS1": crypto.SHA1}[how]
		switch how {
		case "OTHER-RS256":
			key = k.rsaOther
		case "MS-RS256":
			key = k.ms
		}
		sig, err = rsa.SignPKCS1v15(rand.Reader, key, h, digest(h))
	case "PS256", "PS384", "PS512":
		h := map[string]crypto.Hash{"PS256": crypto.SHA256, "PS384": crypto.SHA384, "PS512": crypto.SHA512}[how]
		sig, err = rsa.SignPSS(rand.Reader, k.rsa, h, digest(h), &rsa.PSSOptions{SaltLength: rsa.PSSSaltLengthEqualsHash})
	case "ES256", "ES384":
		key, h, size := k.p256, crypto.SHA256, 32
		if how == "ES384" {
			key, h, size = k.p384, crypto.SHA384, 48
		}
		var r, s *big.Int
		if r, s, err = ecdsa.Sign(rand.Reader, key, digest(h)); err == nil {
			sig = append(r.FillBytes(make([]byte, size)), s.FillBytes(make([]byte, size))...)
		}
	case "EdDSA":
		sig = ed25519.Sign(k.okp, []byte(input))
	default:
		return "", fmt.Errorf("unknown signing scheme %q", how)
	}
	if err != nil {
		return "", err
	}
	return input + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func hmacToken(alg, kid string, secret []byte) string {
	input := unsignedToken(map[string]any{"typ": "JWT", "alg": alg, "kid": kid})
	newHash := map[string]func() hash.Hash{"HS256": sha256.New, "HS384": sha512.New384, "HS512": sha512.New}[alg]
	mac := hmac.New(newHash, secret)
	mac.Write([]byte(input))
	return input + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// publicSecrets returns the public key's PEM and DER (SPKI) encodings, the
// bytes an alg-confusion attacker would use as the HMAC secret.
func publicSecrets(pub any) (map[string][]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		"PEM": pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}),
		"DER": der,
	}, nil
}

// tamper flips the last character of the payload segment.
func tamper(tok string) string {
	b := []byte(tok)
	dot := bytes.LastIndexByte(b, '.')
	if b[dot-1] == 'A' {
		b[dot-1] = 'B'
	} else {
		b[dot-1] = 'A'
	}
	return string(b)
}

func selfSignedDER(key *rsa.PrivateKey) ([]byte, error) {
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "accounts.accesscontrol.windows.net"},
		NotBefore:    time.Unix(iat, 0).UTC(),
		NotAfter:     time.Unix(exp, 0).UTC(),
	}
	return x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
}

func sortedKeys(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
