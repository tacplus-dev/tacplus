// Copyright (c) 2026 Jody Scott
// SPDX-License-Identifier: Apache-2.0
//
// Gate self-test for the record-egress fixture. NOT generated: run.sh copies this
// file into the generated grpcserver crate's tests/ before `cargo test -p
// grpcserver`, because the user leg needs the fixture's REAL generated grant
// catalog — the Authorizer drops any grant the catalog does not know, so a
// chassis test that cannot name a real rpc can never show a user passing.
//
// What it pins: one c2s rpc callable by entitled USERS and by the SERVICE
// identities its (spec.rpc).auth_services lists, on the same surface.
// egressvault.EgressVaultApi/CreateVaultNote declares
//   {principal: RPC_PRINCIPAL_USER, flags: [RPC_FLAG_AUTH_ENTITLED_USERS],
//    auth_services: ["notary"]}
// and run.sh asserts the generated wrapper authorizes it with FULL_METHOD and
// registers exactly the set_method_auth line this test repeats. Then, through the
// real Authorizer over real signed tokens:
//   - a user token holding the rpc's grant passes,
//   - a tenant-owned "notary" service token passes, and the verified token it
//     returns is what the wrapper binds into the request — the caller's SPIFFE
//     subject and its tenant namespace, never a client-chosen value,
//   - a service not listed is refused,
//   - a user token without the grant is still refused: auth_services opens the
//     rpc to the listed services, not to users the entitlements do not cover.

use std::collections::HashMap;

use grpcserver::authorizer::{qualified_method_key, Authorizer};
use key::keys;

/// The full method the generated EgressVaultApi wrapper passes to authorize_method.
const FULL_METHOD: &str = "/services/egressvault.EgressVaultApi/CreateVaultNote";
const METHOD: &str = "CreateVaultNote";
/// The generated exact grant for the rpc (grants catalog token).
const GRANT: &str = "EGRESSVAULT_EGRESS_VAULT_API_CREATE_VAULT_NOTE";
/// The service label the generated Authorizer accepts as token audience.
const AUDIENCE: &str = "egressvault";
const NOTARY: &str = "spiffe://example.com/tenants/acme/services/notary";
const STRANGER: &str = "spiffe://example.com/tenants/acme/services/stranger";

fn jwk_for_key(key: &keys::ImmutableKey) -> jwk::ImmutableJwk {
    let x = match key.public_key() {
        keys::PublicKey::Ed25519(ed) => {
            use base64::Engine;
            base64::engine::general_purpose::URL_SAFE_NO_PAD.encode(ed.as_bytes())
        }
        _ => panic!("expected ed25519 public key"),
    };
    jwk::ImmutableJwk::new(jwk::jwk::Jwk {
        kty: "OKP".to_string(),
        alg: Some("EdDSA".to_string()),
        kid: key.id().to_string(),
        key_use: Some("sig".to_string()),
        n: None,
        e: None,
        crv: Some("Ed25519".to_string()),
        x: Some(x),
        y: None,
        exp: None,
        created: None,
    })
    .expect("build jwk")
}

fn signed(key: &keys::ImmutableKey, customize: impl FnOnce(&mut token::Token)) -> String {
    let exp = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .expect("clock")
        .as_secs() as i64
        + 3600;
    let mut tok = token::Token::new();
    tok.set_issuer("test-issuer").expect("issuer");
    tok.set_audience(AUDIENCE).expect("audience");
    tok.set_expiration(exp).expect("expiration");
    tok.set_namespace("/tenants/acme").expect("namespace");
    customize(&mut tok);
    tok.sign(key).expect("sign");
    format!("Bearer {}", tok.base64().expect("signed token has base64"))
}

fn user_token(key: &keys::ImmutableKey, grants: &[&str]) -> String {
    signed(key, |tok| {
        tok.set_sub_type("user").expect("sub_type");
        tok.set_subject("someone@example.com").expect("subject");
        tok.set_service_entitlements(serde_json::json!({ "grants": grants }))
            .expect("service_entitlements");
    })
}

fn service_token(key: &keys::ImmutableKey, spiffe_id: &str) -> String {
    signed(key, |tok| {
        tok.set_sub_type("service").expect("sub_type");
        tok.set_subject(spiffe_id).expect("subject");
    })
}

fn authorizer(key: &keys::ImmutableKey) -> Authorizer {
    let a = Authorizer::with_seeded_jwks(vec![jwk_for_key(key)]);
    // Exactly the line the generated register_method_auth emits for this rpc.
    a.set_method_auth("egressvault.EgressVaultApi/CreateVaultNote", &["notary"]);
    assert_eq!(qualified_method_key(FULL_METHOD), "egressvault.EgressVaultApi/CreateVaultNote");
    a
}

#[tokio::test]
async fn entitled_user_passes() {
    let key = keys::generate_ed25519_key().expect("key");
    let a = authorizer(&key);
    a.authorize_grpc(FULL_METHOD, METHOD, Some(&user_token(&key, &[GRANT])), &HashMap::new())
        .await
        .expect("a user holding the rpc's grant must pass");
}

#[tokio::test]
async fn listed_service_passes_with_its_own_identity() {
    let key = keys::generate_ed25519_key().expect("key");
    let a = authorizer(&key);
    let verified = a
        .authorize_grpc(FULL_METHOD, METHOD, Some(&service_token(&key, NOTARY)), &HashMap::new())
        .await
        .expect("a service listed in auth_services must pass");
    assert_eq!(verified.subject(), Some(NOTARY));
    assert_eq!(verified.namespace(), Some("/tenants/acme"));
    assert_eq!(verified.service_path().expect("service path"), "notary");
}

#[tokio::test]
async fn unlisted_service_is_refused() {
    let key = keys::generate_ed25519_key().expect("key");
    let a = authorizer(&key);
    a.authorize_grpc(FULL_METHOD, METHOD, Some(&service_token(&key, STRANGER)), &HashMap::new())
        .await
        .err()
        .expect("a service not in auth_services must be refused");
}

#[tokio::test]
async fn unentitled_user_is_refused() {
    let key = keys::generate_ed25519_key().expect("key");
    let a = authorizer(&key);
    a.authorize_grpc(FULL_METHOD, METHOD, Some(&user_token(&key, &[])), &HashMap::new())
        .await
        .err()
        .expect("auth_services must not open the rpc to users without its grant");
}
