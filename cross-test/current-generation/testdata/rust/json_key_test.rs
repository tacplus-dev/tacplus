// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the main fixture's JSON keys. NOT generated: run.sh copies
// this file into the generated crate's tests/ directory and runs it with
// `cargo test --test json_key_test`.
//
// What it pins: common2.JsonKeyProbe declares (spec.json_key) and
// (spec.json_aliases), which Go's ToJSON/FromJSON and Java's toJson/fromJson
// honoured and Rust ignored until 2026-10-15. Rust now writes reset_at as
// "reset" and level as "lvl" (their pbjson json_name), finds "lvl" when it
// stringifies enums for the API, and reads callback_url from its aliases and
// JsonKeyInner.note from "memo" one message down. An alias never overrides the
// field's own key, as in Go.

use proto_model::types::common2::user::*;

#[test]
fn json_key_is_the_written_key_and_aliases_are_read() {
    let msg = JsonKeyProbe { reset_at: 7, level: JsonKeyLevel::High as i32, ..Default::default() };
    let out: serde_json::Value = serde_json::from_slice(&json_key_probe_to_json(&msg).unwrap()).unwrap();
    let obj = out.as_object().unwrap();
    assert!(obj.contains_key("reset") && !obj.contains_key("resetAt") && !obj.contains_key("reset_at"), "{out}");
    assert!(obj.contains_key("lvl"), "{out}");
    let api: serde_json::Value = serde_json::from_slice(&json_key_probe_to_api_json(&msg).unwrap()).unwrap();
    assert!(api["lvl"].is_string(), "stringify must find the json_key'd enum: {api}");

    let back = json_key_probe_from_json(br#"{"reset": 9, "lvl": "JSON_KEY_LEVEL_HIGH", "cb": "https://x", "inner": {"memo": "hi"}}"#).unwrap();
    assert_eq!(back.reset_at, 9);
    assert_eq!(back.level, JsonKeyLevel::High as i32);
    assert_eq!(back.callback_url, "https://x");
    assert_eq!(back.inner.unwrap().note, "hi");

    // An alias never overrides the field's own key.
    let both = json_key_probe_from_json(br#"{"callback_url": "own", "cb": "alias"}"#).unwrap();
    assert_eq!(both.callback_url, "own");
}
