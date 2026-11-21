// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

use crate::loader::load_config_from;
use std::path::PathBuf;
use std::sync::Mutex;
use uniconfig_model_types::svc::uniconfig::secrets_from_file;
use uniconfig_model_types::sys::grpcconfig::{BOOTSTRAP_CONFIG_PATH, SECRETS_PATH};

const BOOTSTRAP_YAML: &str = "instance_id: from-file\nk8s_token_file: /var/run/secrets/tacplus/kernel/token\n";

/// Serialises the loader tests: the loader reads the process environment, and
/// one test sets BOOTSTRAP_CONFIG.
static ENV_LOCK: Mutex<()> = Mutex::new(());

fn lock_env() -> std::sync::MutexGuard<'static, ()> {
    let guard = ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
    // No env-only config may leak in from the runner.
    for (key, _) in std::env::vars().filter(|(k, _)| k.starts_with("PROTO_SYS_GRPCCONFIG_CONFIG_")) {
        std::env::remove_var(key);
    }
    guard
}

fn temp_dir(name: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!("uniconfig-{}-{}", name, std::process::id()));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

fn write_file(dir: &PathBuf, name: &str, body: &str) -> String {
    let path = dir.join(name);
    std::fs::write(&path, body).unwrap();
    path.to_string_lossy().into_owned()
}

fn args(rest: &[&str]) -> Vec<String> {
    std::iter::once("svc").chain(rest.iter().copied()).map(String::from).collect()
}

#[test]
fn rust_config_paths_are_platform_constants() {
    assert_eq!(BOOTSTRAP_CONFIG_PATH, "/etc/tacplus/bootstrap.yaml");
    assert_eq!(SECRETS_PATH, "/etc/tacplus/secrets.yaml");
}

#[test]
fn rust_loader_reads_the_fixed_path() {
    let _env = lock_env();
    let dir = temp_dir("fixed");
    let path = write_file(&dir, "bootstrap.yaml", BOOTSTRAP_YAML);
    let cfg = load_config_from(&args(&[]), &path).expect("load the fixed path");
    assert_eq!(cfg.instance_id, "from-file", "instance_id must come from the fixed path");
    assert_eq!(cfg.k8s_token_file, "/var/run/secrets/tacplus/kernel/token", "k8s_token_file from config");
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn rust_loader_errors_naming_the_fixed_path_when_absent() {
    let _env = lock_env();
    let dir = temp_dir("absent");
    let missing = dir.join("bootstrap.yaml").to_string_lossy().into_owned();
    let err = load_config_from(&args(&[]), &missing).expect_err("no file and no env config must fail");
    let msg = err.to_string();
    assert!(msg.contains(&missing), "the error must name the fixed path {missing}: {msg}");
    assert!(msg.contains("-c"), "the error must name the -c flag: {msg}");
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn rust_loader_ignores_the_bootstrap_config_env_var() {
    let _env = lock_env();
    let dir = temp_dir("envvar");
    let elsewhere = write_file(&dir, "elsewhere.yaml", BOOTSTRAP_YAML);
    let missing = dir.join("bootstrap.yaml").to_string_lossy().into_owned();
    std::env::set_var("BOOTSTRAP_CONFIG", &elsewhere);
    let result = load_config_from(&args(&[]), &missing);
    std::env::remove_var("BOOTSTRAP_CONFIG");
    let err = match result {
        Ok(cfg) => panic!("BOOTSTRAP_CONFIG moved the config: loaded {cfg:?}"),
        Err(e) => e.to_string(),
    };
    assert!(err.contains(&missing), "the error must name the fixed path {missing}: {err}");
    let _ = std::fs::remove_dir_all(&dir);
}

#[test]
fn rust_loader_config_flag_wins_over_the_fixed_path() {
    let _env = lock_env();
    let dir = temp_dir("flag");
    let fixed = write_file(&dir, "bootstrap.yaml", "instance_id: fixed\n");
    let flag = write_file(&dir, "flag.yaml", "instance_id: flag\n");
    for flag_name in ["-c", "--config"] {
        let cfg = load_config_from(&args(&[flag_name, &flag]), &fixed)
            .unwrap_or_else(|e| panic!("load with {flag_name}: {e}"));
        assert_eq!(cfg.instance_id, "flag", "{flag_name} must win over the fixed path");
    }
    // A -c path that cannot be read is an error, not a fall back to the fixed path.
    let missing_flag = dir.join("missing.yaml").to_string_lossy().into_owned();
    let err = load_config_from(&args(&["-c", &missing_flag]), &fixed).expect_err("unreadable -c path");
    assert!(err.to_string().contains(&missing_flag), "the error must name the -c path: {err}");
    let _ = std::fs::remove_dir_all(&dir);
}

/// A service reads its Secrets from the platform's secrets file with the
/// generated reader, not from environment variables.
#[test]
fn rust_secrets_from_file() {
    let dir = temp_dir("secrets");
    let path = write_file(&dir, "secrets.yaml", "api_key: s3cr3t-from-the-uni-config\n");
    let secrets = secrets_from_file(&path).expect("read the secrets file");
    assert_eq!(secrets.api_key, "s3cr3t-from-the-uni-config");

    let missing = dir.join("absent.yaml");
    let err = secrets_from_file(&missing).expect_err("a missing secrets file must fail");
    assert!(err.contains(&*missing.to_string_lossy()), "the error must name the path: {err}");
    let _ = std::fs::remove_dir_all(&dir);
}
