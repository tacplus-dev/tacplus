// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the generated Rust YAML. NOT generated: run.sh copies this
// file into the main fixture's tests/ directory and runs it with
// `cargo test --test yaml_test`.
//
// What it pins (#320): the model and the grpcserver config read and write YAML
// with serde-saphyr, off the deprecated serde_yaml and its unsafe-libyaml
// (RUSTSEC-2024-0449). The model's YAML is this exact text: double quotes, and
// a string a YAML 1.1 reader would take for a bool or a number is quoted. YAML
// is read as YAML 1.2 Core, as serde_yaml read it: an unquoted `yes` or `on` is
// a string, not a bool, so YAML written before #320 reads back unchanged.

use proto_model::types::common2::user::*;

#[test]
fn model_yaml_is_the_pinned_text_and_round_trips() {
    let msg = JsonKeyProbe {
        reset_at: 7,
        callback_url: "yes".into(),
        level: JsonKeyLevel::High as i32,
        inner: Some(JsonKeyInner { note: "a: b".into(), ..Default::default() }),
        ..Default::default()
    };
    let yaml = String::from_utf8(json_key_probe_to_yaml(&msg).unwrap()).unwrap();
    assert_eq!(
        yaml,
        "callbackUrl: \"yes\"\ninner:\n  note: \"a: b\"\nlvl: JSON_KEY_LEVEL_HIGH\nreset: \"7\"\n",
        "{yaml}"
    );
    assert_eq!(json_key_probe_from_yaml(yaml.as_bytes()).unwrap(), msg);
    assert_eq!(json_key_probe_to_yaml(&msg).unwrap(), yaml.into_bytes(), "YAML output must be deterministic");
}

#[test]
fn yaml_is_read_as_yaml_1_2_core() {
    // serde_yaml wrote these strings unquoted; they stay strings.
    let back = json_key_probe_from_yaml(b"callbackUrl: yes\ninner:\n  note: on\nreset: 9\n").unwrap();
    assert_eq!(back.callback_url, "yes");
    assert_eq!(back.inner.unwrap().note, "on");
    assert_eq!(back.reset_at, 9);
    // from_bytes falls through to YAML for a document that is not TOML.
    let auto = json_key_probe_from_bytes(b"cb: no\nlvl: JSON_KEY_LEVEL_HIGH\n").unwrap();
    assert_eq!(auto.callback_url, "no");
    assert_eq!(auto.level, JsonKeyLevel::High as i32);
}

#[test]
fn grpcserver_config_reads_yaml() {
    let cfg = grpcserver::GrpcServerConfig::from_yaml_str(
        "listen_address: \":9443\"\nenable_cors: true\ncors_origins:\n- https://a.example\n- yes\nmax_request_size: 1024\n",
    )
    .unwrap();
    assert_eq!(cfg.listen_address, ":9443");
    assert!(cfg.enable_cors);
    assert_eq!(cfg.cors_origins, vec!["https://a.example".to_string(), "yes".to_string()]);
    assert_eq!(cfg.max_request_size, 1024);
    // A YAML 1.1 bool spelling is not a bool.
    assert!(grpcserver::GrpcServerConfig::from_yaml_str("enable_cors: yes\n").is_err());
}
