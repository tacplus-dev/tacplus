// Black-box traffic stays inside the service's pods (tacplus#258; tacplus-dev/roadmap
// decisions/s2s-never-crosses-tenants-2026-10-31.md): the generated ServiceManager of a
// (spec.member_port) service checks grpcconfig.member_port before startup and refuses a
// target_address that is not loopback, so nothing reaches the black-box protocol except
// through the member port. The Rust twin of MemberPortConfigTest.java; run.sh appends it
// to s2sm/mod.rs as a test module, since member_port_addresses is private.
use super::*;

use crate::types::sys::grpcconfig::grpcconfig::{Config, MemberPortConfig};
use crate::types::sys::host::host::Host;
use crate::types::sys::x509::x509::X509Cert;

fn config(target_host: &str) -> Config {
    Config {
        member_port: Some(MemberPortConfig {
            listen_address: Some(Host { host: "0.0.0.0".into(), port: 7000, ..Default::default() }),
            target_address: Some(Host { host: target_host.into(), port: 5701, ..Default::default() }),
            ..Default::default()
        }),
        spiffe_trust_domain: "example.com".into(),
        trusted_cas: vec![X509Cert::default()],
        ..Default::default()
    }
}

#[tokio::test]
async fn memberport_config_non_loopback_target_is_refused() {
    for host in ["0.0.0.0", "10.0.0.5", "192.168.1.20", "::", "fd00::5", "member.invalid"] {
        let err = member_port_addresses(&config(host))
            .await
            .expect_err(&format!("target_address {host} must be refused"));
        let msg = err.to_string();
        assert!(
            msg.contains("target_address must be a loopback address") || host == "member.invalid",
            "{host}: {msg}"
        );
    }
}

#[tokio::test]
async fn memberport_config_loopback_target_is_accepted() {
    for host in ["127.0.0.1", "127.0.0.2", "::1"] {
        let (_, target) = member_port_addresses(&config(host)).await.unwrap_or_else(|e| panic!("{host}: {e}"));
        assert!(target.ip().is_loopback(), "{host}");
        assert_eq!(target.port(), 5701);
    }
}
