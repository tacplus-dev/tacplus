// Copyright (c) 2026 Second Star Cavalier LLC
//
// The rust leg of the serialization-equivalence corpus (LED-2 phase 2).
// Decodes the checked-in golden bytes with the GENERATED CorpusRecord type and
// asserts every field equals the fixture's `expect` block. It never encodes:
// go owns the bytes.

use std::collections::BTreeMap;

use corpusclient_model_types::corpus::corpusclient::CorpusRecord;
use prost::Message;
use serde_json::Value;

fn fail(message: String) -> ! {
    eprintln!("FAIL: {message}");
    std::process::exit(1);
}

// The generated crates do not depend on a hex crate and the corpus does not
// justify adding one.
fn unhex(text: &str) -> Vec<u8> {
    if text.len() % 2 != 0 {
        fail(format!("hex string has odd length: {text}"));
    }
    (0..text.len())
        .step_by(2)
        .map(|i| match u8::from_str_radix(&text[i..i + 2], 16) {
            Ok(byte) => byte,
            Err(err) => fail(format!("bad hex {text}: {err}")),
        })
        .collect()
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|byte| format!("{byte:02x}")).collect()
}

fn check(name: &str, expect: &Value, golden: &[u8]) {
    let record = match CorpusRecord::decode(golden) {
        Ok(record) => record,
        Err(err) => fail(format!("{name}: decode: {err}")),
    };

    let want_negative = expect["big_negative"].as_str().unwrap_or_default();
    if record.big_negative.to_string() != want_negative {
        fail(format!(
            "{name}: big_negative: got {} want {want_negative}",
            record.big_negative
        ));
    }
    let want_unsigned = expect["big_unsigned"].as_str().unwrap_or_default();
    if record.big_unsigned.to_string() != want_unsigned {
        fail(format!(
            "{name}: big_unsigned: got {} want {want_unsigned}",
            record.big_unsigned
        ));
    }

    let want_labels: BTreeMap<String, String> = expect["labels"]
        .as_object()
        .unwrap_or_else(|| fail(format!("{name}: labels is not an object")))
        .iter()
        .map(|(key, value)| (key.clone(), value.as_str().unwrap_or_default().to_string()))
        .collect();
    if record.labels.len() != want_labels.len() {
        fail(format!(
            "{name}: labels: got {} entries want {}",
            record.labels.len(),
            want_labels.len()
        ));
    }
    for (key, want) in &want_labels {
        match record.labels.get(key) {
            None => fail(format!("{name}: labels[{key}]: missing")),
            Some(got) if got != want => {
                fail(format!("{name}: labels[{key}]: got {got:?} want {want:?}"))
            }
            Some(_) => {}
        }
    }

    let want_present = expect["opt_count_present"].as_bool().unwrap_or(false);
    if record.opt_count.is_some() != want_present {
        fail(format!(
            "{name}: opt_count presence: got {} want {want_present}",
            record.opt_count.is_some()
        ));
    }
    if want_present {
        let want_count = expect["opt_count"].as_i64().unwrap_or_default();
        let got = record.opt_count.unwrap_or_default() as i64;
        if got != want_count {
            fail(format!("{name}: opt_count: got {got} want {want_count}"));
        }
    }

    let want_blob = expect["blob_hex"].as_str().unwrap_or_default();
    let got_blob = hex(&record.blob);
    if got_blob != want_blob {
        fail(format!("{name}: blob: got {got_blob} want {want_blob}"));
    }

    println!("rust: {name} OK ({} bytes)", golden.len());
}

fn main() {
    let path = match std::env::args().nth(1) {
        Some(path) => path,
        None => fail("usage: decoder <wire_corpus.json>".to_string()),
    };
    let raw = std::fs::read_to_string(&path)
        .unwrap_or_else(|err| fail(format!("read {path}: {err}")));
    let fixture: Value =
        serde_json::from_str(&raw).unwrap_or_else(|err| fail(format!("parse {path}: {err}")));
    let vectors = fixture["vectors"]
        .as_array()
        .unwrap_or_else(|| fail("fixture has no vectors".to_string()));
    if vectors.is_empty() {
        fail("fixture has no vectors".to_string());
    }
    for vector in vectors {
        let name = vector["name"].as_str().unwrap_or_default();
        let golden = unhex(vector["hex"].as_str().unwrap_or_default());
        check(name, &vector["expect"], &golden);
    }
    println!("rust: {} vectors OK", vectors.len());
}
