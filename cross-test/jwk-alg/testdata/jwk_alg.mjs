// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// The typescript leg of the jwk-alg gate (issue #179): the generated JWK and
// token chassis against cross-test/fixtures/jwk_alg_vectors.json. run.sh copies
// this file beside the built model; it mirrors testdata/jwk_alg_test.go.

import { readFileSync } from 'node:fs';

import { ImmutableJwk } from '../model/dist/chassis/jwk/index.js';
import { TokenWrapper } from '../model/dist/chassis/token/index.js';
import { JwkUseType } from '../model/dist/types/sys/jwk/jwk_pb.js';
import { KeyType } from '../model/dist/types/sys/key/key_pb.js';

const KTY = { RSA: KeyType.RSA, EC: KeyType.EC, OKP: KeyType.OKP };

function str(m, k) {
  return typeof m[k] === 'string' ? m[k] : '';
}

function loadVectors(path) {
  const v = JSON.parse(readFileSync(path, 'utf8'));
  const keys = new Map();
  for (const set of Object.values(v.jwks)) {
    for (const m of set) {
      keys.set(str(m, 'kid'), {
        kty: KTY[str(m, 'kty')] ?? KeyType.UNDEFINED,
        alg: str(m, 'alg'),
        kid: str(m, 'kid'),
        use: str(m, 'use') === 'sig' ? JwkUseType.SIG : JwkUseType.UNDEFINED,
        n: str(m, 'n'),
        e: str(m, 'e'),
        crv: str(m, 'crv'),
        x: str(m, 'x'),
        y: str(m, 'y'),
      });
    }
  }
  if (v.cases.length < 100) {
    throw new Error(`fixture has ${v.cases.length} cases; expected the full set`);
  }
  return { cases: v.cases, keys };
}

// verify builds the JWK and verifies the token against it; any refusal on the
// way (the JWK, the token's parse, its verification) is a reject.
async function verify(key, token) {
  const jwk = await ImmutableJwk.create(key);
  const tw = TokenWrapper.parse(token);
  await tw.verify(jwk);
}

const errors = [];
const { cases, keys } = loadVectors(process.argv[2]);

let accepted = 0;
for (const c of cases) {
  const key = keys.get(c.key);
  if (!key) {
    errors.push(`${c.name}: fixture has no key "${c.key}"`);
    continue;
  }
  let err = null;
  try {
    await verify(key, c.token);
  } catch (e) {
    err = e instanceof Error ? e.message : String(e);
  }
  if (c.expect === 'accept' && err !== null) {
    errors.push(`${c.name}: expected accept, got ${err}`);
  } else if (c.expect === 'reject' && err === null) {
    errors.push(`${c.name}: expected reject, but the token verified`);
  } else if (c.expect === 'accept') {
    accepted += 1;
  }
}

// The accepted set comes from the key, narrowed to a declared alg; the alg a
// JWK reports is the one it declares, never a guess.
const wantAccepted = {
  'rsa-noalg': ['RS256', 'RS384', 'RS512'],
  'rsa-rs256': ['RS256'],
  'ms-noalg': ['RS256', 'RS384', 'RS512'],
  'ec-p256-noalg': ['ES256'],
  'ec-p384-noalg': ['ES384'],
  'okp-noalg': ['EdDSA'],
  'okp-eddsa': ['EdDSA'],
};
for (const [kid, want] of Object.entries(wantAccepted)) {
  let jwk;
  try {
    jwk = await ImmutableJwk.create(keys.get(kid));
  } catch (e) {
    errors.push(`${kid}: ${e instanceof Error ? e.message : String(e)}`);
    continue;
  }
  const got = typeof jwk.acceptedAlgs === 'function' ? [...jwk.acceptedAlgs()] : undefined;
  if (JSON.stringify(got) !== JSON.stringify(want)) {
    errors.push(`${kid}: acceptedAlgs() = ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
  }
  if (jwk.getAlg() !== keys.get(kid).alg) {
    errors.push(`${kid}: getAlg() = "${jwk.getAlg()}", want the declared "${keys.get(kid).alg}"`);
  }
}

for (const message of errors) {
  console.error(`FAIL: ${message}`);
}
console.log(`jwk-alg typescript: ${cases.length} cases, ${accepted} accepted`);
process.exit(errors.length === 0 ? 0 : 1);
