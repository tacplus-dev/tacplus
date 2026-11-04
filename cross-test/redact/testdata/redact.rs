// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

// Rust leg of the redact gate (tacplus#274). Feeds every case of
// cross-test/fixtures/redact_vectors.json through the GENERATED redact_<t> and
// sensitive-field validators:
//   - redact cases: decode input_hex as the case's type, redact_<t>, encode, and
//     compare byte for byte with expect_hex;
//   - verdict cases: a valid SecretCheck with one field set to the case's value
//     through validate(), which must reach the case's expect, name the field and
//     the rule, and never echo the value.
// Prints one line `redact rust: <n> redact cases, <m> verdicts`.

use std::io::Read;
use std::process::exit;

use redactcheck_model_types::redact::redactcheck::*;

fn unhex(s: &str) -> Vec<u8> {
    assert!(s.len() % 2 == 0, "odd hex length");
    (0..s.len())
        .step_by(2)
        .map(|i| u8::from_str_radix(&s[i..i + 2], 16).expect("bad hex"))
        .collect()
}

fn tohex(b: &[u8]) -> String {
    b.iter().map(|c| format!("{:02x}", c)).collect()
}

fn redact_hex(typ: &str, input: &[u8]) -> Result<Vec<u8>, String> {
    match typ {
        "UniConfig" => uni_config_to_binary(&redact_uni_config(&uni_config_from_binary(input)?)),
        "Secrets" => secrets_to_binary(&redact_secrets(&secrets_from_binary(input)?)),
        "Endpoint" => endpoint_to_binary(&redact_endpoint(&endpoint_from_binary(input)?)),
        "SecretCheck" => {
            secret_check_to_binary(&redact_secret_check(&secret_check_from_binary(input)?))
        }
        other => Err(format!("unknown type {other}")),
    }
}

fn valid_secret_check() -> SecretCheck {
    SecretCheck {
        token: "Xk9#mP2$vL7@qR4!wT8&".to_string(),
        key: (0u8..32).collect(),
        optional_token: String::new(),
        ..Default::default()
    }
}

fn main() {
    let path = std::env::args().nth(1).unwrap_or_else(|| {
        eprintln!("usage: redact-rust <redact_vectors.json>");
        exit(2)
    });
    let raw = std::fs::read(&path).expect("read fixture");
    let fx: serde_json::Value = serde_json::from_slice(&raw).expect("parse fixture");
    let mut failed = false;

    let redact = fx["redact"].as_array().expect("redact array");
    for c in redact {
        let name = c["name"].as_str().unwrap();
        let typ = c["type"].as_str().unwrap();
        let want = c["expect_hex"].as_str().unwrap();
        match redact_hex(typ, &unhex(c["input_hex"].as_str().unwrap())) {
            Ok(got) if tohex(&got) == want => {}
            Ok(got) => {
                eprintln!("FAIL redact {name}: got {} want {want}", tohex(&got));
                failed = true;
            }
            Err(e) => {
                eprintln!("FAIL redact {name}: {e}");
                failed = true;
            }
        }
    }

    let verdicts = fx["verdicts"].as_array().expect("verdicts array");
    for c in verdicts {
        let name = c["name"].as_str().unwrap();
        let field = c["field"].as_str().unwrap();
        let value = unhex(c["value_hex"].as_str().unwrap());
        let expect = c["expect"].as_str().unwrap();
        let mut m = valid_secret_check();
        match field {
            "token" => m.token = String::from_utf8(value.clone()).expect("utf-8 token"),
            "optional_token" => {
                m.optional_token = String::from_utf8(value.clone()).expect("utf-8 optional_token")
            }
            "key" => m.key = value.clone(),
            other => {
                eprintln!("FAIL verdict {name}: unknown field {other}");
                failed = true;
                continue;
            }
        }
        let res = m.validate();
        if expect == "ok" {
            if let Err(e) = res {
                eprintln!("FAIL verdict {name}: want ok, got error {e:?}");
                failed = true;
            }
            continue;
        }
        let rule = match expect {
            "required" => "required and empty",
            "placeholder" => "a known placeholder value",
            "entropy" => "estimated entropy below",
            other => {
                eprintln!("FAIL verdict {name}: unknown expect {other}");
                failed = true;
                continue;
            }
        };
        let err = match res {
            Ok(()) => {
                eprintln!("FAIL verdict {name}: want {expect}, validator passed");
                failed = true;
                continue;
            }
            Err(e) => e,
        };
        let want_field = format!("SecretCheck.{field}");
        if !err.contains(&want_field) || !err.contains(rule) {
            eprintln!("FAIL verdict {name}: error {err:?} does not name {want_field} and {rule:?}");
            failed = true;
        }
        if value.len() >= 6 {
            let echoed = match std::str::from_utf8(&value) {
                Ok(s) => err.contains(s),
                Err(_) => false,
            } || err.contains(&tohex(&value));
            if echoed {
                eprintln!("FAIL verdict {name}: error {err:?} echoes the value");
                failed = true;
            }
        }
    }

    // A fresh CSPRNG key of 32 bytes must always pass.
    let mut key = vec![0u8; 32];
    std::fs::File::open("/dev/urandom")
        .and_then(|mut f| f.read_exact(&mut key))
        .expect("read /dev/urandom");
    let mut m = valid_secret_check();
    m.key = key;
    if let Err(e) = m.validate() {
        eprintln!("FAIL random 32-byte key rejected: {e:?}");
        failed = true;
    }

    if failed {
        exit(1);
    }
    println!(
        "redact rust: {} redact cases, {} verdicts",
        redact.len(),
        verdicts.len()
    );
}
