// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0

use crate::manager::bootstrap_sources;
use uniconfig_model_types::sys::grpcconfig::grpcconfig::Config;

/// The projected Kubernetes token file comes from config.k8s_token_file, never
/// from KERNEL_K8S_TOKEN_FILE. One test fn, so nothing else in this binary
/// races the env vars it sets.
#[test]
fn rust_k8s_token_file_comes_from_config_not_env() {
    std::env::remove_var("KERNEL_BOOTSTRAP_TOKEN_FILE");
    std::env::set_var("KERNEL_K8S_TOKEN_FILE", "/from/the/env/token");

    let config = Config { k8s_token_file: "/from/config/token".to_string(), ..Default::default() };
    let (bootstrap, k8s) = bootstrap_sources(&config);
    assert_eq!(bootstrap, None, "no bootstrap token file is configured");
    assert_eq!(k8s.as_deref(), Some("/from/config/token"), "k8s token file must come from config");

    let (_, k8s) = bootstrap_sources(&Config::default());
    assert_eq!(k8s, None, "empty k8s_token_file means not configured, whatever KERNEL_K8S_TOKEN_FILE says");

    // The parent-written bootstrap token file stays an env var.
    std::env::set_var("KERNEL_BOOTSTRAP_TOKEN_FILE", "/from/the/parent/bootstrap");
    let (bootstrap, _) = bootstrap_sources(&config);
    assert_eq!(bootstrap.as_deref(), Some("/from/the/parent/bootstrap"));

    std::env::remove_var("KERNEL_BOOTSTRAP_TOKEN_FILE");
    std::env::remove_var("KERNEL_K8S_TOKEN_FILE");
}
