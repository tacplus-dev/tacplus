// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// The rust leg of the jwk-alg gate (issue #179): the generated JWK and token
// chassis against cross-test/fixtures/jwk_alg_vectors.json. run.sh builds this
// file as the jwk-alg-rust binary and passes the fixture path as its argument.

use std::collections::HashMap;
use std::process::exit;

use jwk::jwk::Jwk;
use jwk::ImmutableJwk;
use serde_json::Value;
use token::Token;

fn str_field(m: &Value, k: &str) -> Option<String> {
    m.get(k).and_then(Value::as_str).map(str::to_string)
}

// Builds the JWK from the fixture's JSON object; x5t, x5c and issuer are not
// part of the key and are ignored.
fn to_jwk(m: &Value) -> Jwk {
    Jwk {
        kty: str_field(m, "kty").unwrap_or_default(),
        alg: str_field(m, "alg"),
        kid: str_field(m, "kid").unwrap_or_default(),
        key_use: str_field(m, "use"),
        n: str_field(m, "n"),
        e: str_field(m, "e"),
        crv: str_field(m, "crv"),
        x: str_field(m, "x"),
        y: str_field(m, "y"),
        exp: None,
        created: None,
    }
}

// Builds the JWK and verifies the token against it; any refusal on the way
// (the JWK, the token's parse, its verification) is a reject.
fn verify(key: &Jwk, token: &str) -> Result<(), String> {
    let jwk = ImmutableJwk::new(key.clone()).map_err(|e| e.to_string())?;
    let tok = Token::parse(token).map_err(|e| e.to_string())?;
    tok.verify(&jwk).map_err(|e| e.to_string())
}

fn main() {
    let path = match std::env::args().nth(1) {
        Some(p) => p,
        None => {
            eprintln!("usage: jwk-alg-rust <fixture.json>");
            exit(2);
        }
    };
    let data = std::fs::read_to_string(&path).unwrap_or_else(|e| {
        eprintln!("read fixture {}: {}", path, e);
        exit(2);
    });
    let v: Value = serde_json::from_str(&data).unwrap_or_else(|e| {
        eprintln!("parse fixture: {}", e);
        exit(2);
    });

    let mut keys: HashMap<String, Jwk> = HashMap::new();
    for set in v["jwks"].as_object().expect("fixture jwks is an object").values() {
        for m in set.as_array().expect("fixture jwks group is a list") {
            let jwk = to_jwk(m);
            keys.insert(jwk.kid.clone(), jwk);
        }
    }
    let cases = v["cases"].as_array().expect("fixture cases is a list");
    let mut failures: Vec<String> = Vec::new();
    if cases.len() < 100 {
        failures.push(format!("fixture has {} cases; expected the full set", cases.len()));
    }

    let mut accepted = 0;
    for c in cases {
        let name = c["name"].as_str().unwrap_or_default();
        let kid = c["key"].as_str().unwrap_or_default();
        let token = c["token"].as_str().unwrap_or_default();
        let expect = c["expect"].as_str().unwrap_or_default();
        let key = match keys.get(kid) {
            Some(k) => k,
            None => {
                failures.push(format!("{}: fixture has no key {:?}", name, kid));
                continue;
            }
        };
        match (expect, verify(key, token)) {
            ("accept", Err(e)) => failures.push(format!("{}: expected accept, got {}", name, e)),
            ("reject", Ok(())) => {
                failures.push(format!("{}: expected reject, but the token verified", name))
            }
            ("accept", Ok(())) => accepted += 1,
            ("reject", Err(_)) => {}
            (other, _) => failures.push(format!("{}: unknown expect {:?}", name, other)),
        }
    }

    // The accepted set comes from the key, narrowed to a declared alg; the alg
    // a JWK reports is the one it declares, never a guess.
    let want: [(&str, &[&str]); 7] = [
        ("rsa-noalg", &["RS256", "RS384", "RS512"]),
        ("rsa-rs256", &["RS256"]),
        ("ms-noalg", &["RS256", "RS384", "RS512"]),
        ("ec-p256-noalg", &["ES256"]),
        ("ec-p384-noalg", &["ES384"]),
        ("okp-noalg", &["EdDSA"]),
        ("okp-eddsa", &["EdDSA"]),
    ];
    for (kid, algs) in want {
        let Some(key) = keys.get(kid) else {
            failures.push(format!("{}: fixture has no such key", kid));
            continue;
        };
        let declared = key.alg.clone().unwrap_or_default();
        match ImmutableJwk::new(key.clone()) {
            Err(e) => failures.push(format!("{}: {}", kid, e)),
            Ok(jwk) => {
                if jwk.accepted_algs() != algs {
                    failures.push(format!(
                        "{}: accepted_algs() = {:?}, want {:?}",
                        kid,
                        jwk.accepted_algs(),
                        algs
                    ));
                }
                if jwk.alg() != declared {
                    failures.push(format!(
                        "{}: alg() = {:?}, want the declared {:?}",
                        kid,
                        jwk.alg(),
                        declared
                    ));
                }
            }
        }
    }

    for f in &failures {
        println!("FAIL {}", f);
    }
    println!("jwk-alg rust: {} cases, {} accepted", cases.len(), accepted);
    if !failures.is_empty() {
        exit(1);
    }
}
