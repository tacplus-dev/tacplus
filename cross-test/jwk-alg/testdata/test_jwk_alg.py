# Copyright (c) 2026 Jody Scott
# SPDX-License-Identifier: Apache-2.0

"""The python leg of the jwk-alg gate (issue #179).

Verifies every case of cross-test/fixtures/jwk_alg_vectors.json with the
generated python jwk and token chassis (rendered by pyrender), mirroring
jwk_alg_test.go:

    python3 test_jwk_alg.py <fixture> <package>
"""

import importlib
import json
import sys


def main() -> int:
    if len(sys.argv) != 3:
        print("usage: test_jwk_alg.py <fixture> <package>")
        return 2
    fixture, package = sys.argv[1], sys.argv[2]

    jwk_mod = importlib.import_module(f"{package}.chassis.jwk.jwk")
    token_mod = importlib.import_module(f"{package}.chassis.token.token_wrapper")
    jwk_pb2 = importlib.import_module(f"{package}.types.sys.jwk.jwk_pb2")
    key_pb2 = importlib.import_module(f"{package}.types.sys.key.key_pb2")

    with open(fixture, encoding="utf-8") as f:
        vectors = json.load(f)

    def text(m, k):
        v = m.get(k)
        return v if isinstance(v, str) else ""

    kty_map = {
        "RSA": key_pb2.KEY_TYPE_RSA,
        "EC": key_pb2.KEY_TYPE_EC,
        "OKP": key_pb2.KEY_TYPE_OKP,
    }
    keys = {}
    for jwk_set in vectors["jwks"].values():
        for m in jwk_set:
            use = jwk_pb2.JWK_USE_TYPE_UNDEFINED
            if text(m, "use") == "sig":
                use = jwk_pb2.JWK_USE_TYPE_SIG
            keys[text(m, "kid")] = jwk_pb2.Jwk(
                kty=kty_map.get(text(m, "kty"), 0),
                alg=text(m, "alg"),
                kid=text(m, "kid"),
                use=use,
                n=text(m, "n"),
                e=text(m, "e"),
                crv=text(m, "crv"),
                x=text(m, "x"),
                y=text(m, "y"),
            )

    failures = []
    cases = vectors["cases"]
    if len(cases) < 100:
        failures.append(f"fixture has {len(cases)} cases; expected the full set")

    # Any refusal on the way (the JWK, the token's parse, its verification)
    # is a reject.
    def verify(key, token):
        try:
            jwk = jwk_mod.ImmutableJwk(key)
            tw = token_mod.TokenWrapper.parse(token)
            tw.verify(jwk)
        except Exception as e:  # noqa: BLE001 - every refusal counts
            return e
        return None

    accepted = 0
    for c in cases:
        key = keys.get(c["key"])
        if key is None:
            failures.append(f"{c['name']}: fixture has no key {c['key']!r}")
            continue
        err = verify(key, c["token"])
        if c["expect"] == "accept" and err is not None:
            failures.append(f"{c['name']}: expected accept, got {err!r}")
        elif c["expect"] == "reject" and err is None:
            failures.append(f"{c['name']}: expected reject, but the token verified")
        elif c["expect"] == "accept":
            accepted += 1

    # The accepted set comes from the key, narrowed to a declared alg; the alg
    # a JWK reports is the one it declares, never a guess.
    rsa_all = ("RS256", "RS384", "RS512")
    for kid, want in {
        "rsa-noalg": rsa_all,
        "rsa-rs256": ("RS256",),
        "ms-noalg": rsa_all,
        "ec-p256-noalg": ("ES256",),
        "ec-p384-noalg": ("ES384",),
        "okp-noalg": ("EdDSA",),
        "okp-eddsa": ("EdDSA",),
    }.items():
        try:
            jwk = jwk_mod.ImmutableJwk(keys[kid])
            got = jwk.accepted_algs()
            got_alg = jwk.alg()
        except Exception as e:  # noqa: BLE001
            failures.append(f"{kid}: {e!r}")
            continue
        if got != want:
            failures.append(f"{kid}: accepted_algs() = {got!r}, want {want!r}")
        if got_alg != keys[kid].alg:
            failures.append(
                f"{kid}: alg() = {got_alg!r}, want the declared {keys[kid].alg!r}"
            )

    for f in failures:
        print(f"MISMATCH {f}")
    print(f"jwk-alg python: {len(cases)} cases, {accepted} accepted")
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
