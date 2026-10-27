use super::*;

use base64::engine::general_purpose::URL_SAFE_NO_PAD;
use p256::ecdsa::signature::{Signer, Verifier};
use p256::ecdsa::{Signature, SigningKey, VerifyingKey};
use std::net::SocketAddr;
use std::sync::Mutex;
use std::time::{Duration, SystemTime, UNIX_EPOCH};
use tonic::transport::{Endpoint, Server};

use crate::types::sys::minter::minter::minter_server::{Minter, MinterServer};
use crate::types::sys::token::token::{Token, TokenPurpose};
use crate::types::sys::tokenrequest::tokenrequest::{
    MintExternalServiceTokenRequest, MintServiceTokenRequest, TokenMintRequest, TokenMintResponse,
    X509MintRequest,
};
use crate::types::sys::x509::x509::X509CertChain;

const SUBJECT: &str = "spiffe://td.test/system/services/caller";
const ISSUER: &str = "https://fleet.test";
const SERVICE_TOKEN: &str = "service-token-for-authority";
const EXTERNAL_AUD: &str = "https://api.anthropic.com";
const MAX_EXTERNAL_LIFETIME_SECS: i64 = 600;

fn now_secs() -> i64 {
    SystemTime::now().duration_since(UNIX_EPOCH).unwrap().as_secs() as i64
}

fn jwt(header: serde_json::Value, payload: serde_json::Value, key: &SigningKey) -> String {
    let signing_input = format!(
        "{}.{}",
        URL_SAFE_NO_PAD.encode(header.to_string()),
        URL_SAFE_NO_PAD.encode(payload.to_string())
    );
    let sig: Signature = key.sign(signing_input.as_bytes());
    format!("{signing_input}.{}", URL_SAFE_NO_PAD.encode(sig.to_bytes()))
}

fn token(base64: String, payload: &[(&str, String)]) -> Token {
    Token {
        payload: payload.iter().map(|(k, v)| (k.to_string(), v.clone())).collect(),
        base64,
        ..Default::default()
    }
}

/// Kernel stand-in. The bootstrap exchange hands out a fixed service token; the
/// external mint enforces the contract the real kernel RPC must meet (the
/// presented token is the caller's own, exp no more than 600 s out) and signs a
/// fresh ES256 JWT with a new jti per call.
struct MockKernel {
    key: SigningKey,
    external_requests: Mutex<Vec<MintExternalServiceTokenRequest>>,
}

#[tonic::async_trait]
impl Minter for MockKernel {
    async fn mint_token(
        &self,
        _request: tonic::Request<TokenMintRequest>,
    ) -> Result<tonic::Response<TokenMintResponse>, tonic::Status> {
        Err(tonic::Status::unimplemented("mint_token is not part of this fixture"))
    }

    async fn mint_x509_cert(
        &self,
        _request: tonic::Request<X509MintRequest>,
    ) -> Result<tonic::Response<X509CertChain>, tonic::Status> {
        Err(tonic::Status::unimplemented("mint_x509_cert is not part of this fixture"))
    }

    async fn mint_service_token(
        &self,
        request: tonic::Request<MintServiceTokenRequest>,
    ) -> Result<tonic::Response<TokenMintResponse>, tonic::Status> {
        let req = request.into_inner();
        let exp = (now_secs() + 3600).to_string();
        let access = if req.token_purpose == TokenPurpose::Refresh as i32 {
            token("access-token".to_string(), &[("exp", exp)])
        } else {
            assert_eq!(req.input_token, "access-token", "phase 2 must present the phase-1 access token");
            token(SERVICE_TOKEN.to_string(), &[("exp", exp)])
        };
        Ok(tonic::Response::new(TokenMintResponse {
            access_token: Some(access),
            ..Default::default()
        }))
    }

    async fn mint_external_service_token(
        &self,
        request: tonic::Request<MintExternalServiceTokenRequest>,
    ) -> Result<tonic::Response<TokenMintResponse>, tonic::Status> {
        let req = request.into_inner();
        self.external_requests.lock().unwrap().push(req.clone());
        if req.input_token != SERVICE_TOKEN {
            return Err(tonic::Status::unauthenticated("input_token is not the caller's kernel token"));
        }
        let now = now_secs();
        if req.exp <= now || req.exp > now + MAX_EXTERNAL_LIFETIME_SECS {
            return Err(tonic::Status::invalid_argument("exp must be within 600 seconds"));
        }
        let jti = format!("{:032x}", rand::random::<u128>());
        let signed = jwt(
            serde_json::json!({"alg": "ES256", "typ": "JWT"}),
            serde_json::json!({
                "iss": ISSUER,
                "sub": SUBJECT,
                "aud": req.aud,
                "iat": now,
                "exp": req.exp,
                "jti": jti,
            }),
            &self.key,
        );
        Ok(tonic::Response::new(TokenMintResponse {
            access_token: Some(token(signed, &[("jti", jti), ("exp", req.exp.to_string())])),
            ..Default::default()
        }))
    }
}

fn reserve_local_addr() -> SocketAddr {
    let listener = std::net::TcpListener::bind("127.0.0.1:0").expect("reserve local address");
    let addr = listener.local_addr().expect("local address");
    drop(listener);
    addr
}

async fn connect_plain_channel(addr: SocketAddr) -> Channel {
    let endpoint = format!("http://{addr}");
    for _ in 0..50 {
        match Endpoint::from_shared(endpoint.clone()).expect("valid endpoint").connect().await {
            Ok(channel) => return channel,
            Err(_) => tokio::time::sleep(Duration::from_millis(10)).await,
        }
    }
    panic!("mock kernel did not accept connections at {endpoint}");
}

fn decode_segment(segment: &str) -> serde_json::Value {
    serde_json::from_slice(&URL_SAFE_NO_PAD.decode(segment).expect("base64url segment")).expect("json segment")
}

/// Serialises the tests that set the process-wide identity env vars: each test
/// builds an IdentityManager, which reads them, so they must not interleave.
static IDENTITY_ENV_LOCK: Mutex<()> = Mutex::new(());

/// Kernel stand-in for the bootstrap arm. The first refresh exchange must present
/// the bootstrap token, each renewal the refresh token the previous exchange
/// returned, and the third refresh token has been revoked. Service tokens expire
/// one second out, so every call to the identity manager re-exchanges.
struct BootstrapKernel {
    refresh_inputs: Mutex<Vec<String>>,
}

#[tonic::async_trait]
impl Minter for BootstrapKernel {
    async fn mint_token(
        &self,
        _request: tonic::Request<TokenMintRequest>,
    ) -> Result<tonic::Response<TokenMintResponse>, tonic::Status> {
        Err(tonic::Status::unimplemented("mint_token is not part of this fixture"))
    }

    async fn mint_x509_cert(
        &self,
        _request: tonic::Request<X509MintRequest>,
    ) -> Result<tonic::Response<X509CertChain>, tonic::Status> {
        Err(tonic::Status::unimplemented("mint_x509_cert is not part of this fixture"))
    }

    async fn mint_service_token(
        &self,
        request: tonic::Request<MintServiceTokenRequest>,
    ) -> Result<tonic::Response<TokenMintResponse>, tonic::Status> {
        let req = request.into_inner();
        if req.token_purpose == TokenPurpose::Refresh as i32 {
            self.refresh_inputs.lock().unwrap().push(req.input_token.clone());
            let (access, refresh) = match req.input_token.as_str() {
                "bootstrap-token" => ("access-1", "refresh-1"),
                "refresh-1" => ("access-2", "refresh-2"),
                "refresh-2" => return Err(tonic::Status::permission_denied("workload revoked")),
                other => return Err(tonic::Status::invalid_argument(format!("unexpected refresh input {other}"))),
            };
            return Ok(tonic::Response::new(TokenMintResponse {
                access_token: Some(token(access.to_string(), &[])),
                refresh_token: Some(token(refresh.to_string(), &[])),
                ..Default::default()
            }));
        }
        let n = match req.input_token.as_str() {
            "access-1" => 1,
            "access-2" => 2,
            other => return Err(tonic::Status::unauthenticated(format!("phase 2 presented {other}"))),
        };
        let exp = (now_secs() + 1).to_string();
        Ok(tonic::Response::new(TokenMintResponse {
            access_token: Some(token(format!("service-token-{n}"), &[("exp", exp)])),
            ..Default::default()
        }))
    }

    async fn mint_external_service_token(
        &self,
        _request: tonic::Request<MintExternalServiceTokenRequest>,
    ) -> Result<tonic::Response<TokenMintResponse>, tonic::Status> {
        Err(tonic::Status::unimplemented("mint_external_service_token is not part of this fixture"))
    }
}

#[tokio::test]
async fn rust_bootstrap_token_is_exchanged_once_and_renewed_from_the_refresh_token() {
    let _env = IDENTITY_ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
    std::env::remove_var("KERNEL_K8S_TOKEN_FILE");
    let dir = std::env::temp_dir().join(format!("bootstrap-token-{}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    let bootstrap_file = dir.join("bootstrap");
    std::fs::write(&bootstrap_file, "bootstrap-token").unwrap();
    std::env::set_var("KERNEL_BOOTSTRAP_TOKEN_FILE", &bootstrap_file);

    let kernel = Arc::new(BootstrapKernel { refresh_inputs: Mutex::new(Vec::new()) });
    let addr = reserve_local_addr();
    let server = tokio::spawn(
        Server::builder()
            .add_service(MinterServer::from_arc(kernel.clone()))
            .serve(addr),
    );
    let channel = connect_plain_channel(addr).await;

    let identity = IdentityManager::new(channel, &Config::default())
        .await
        .expect("identity manager bootstraps from the bootstrap token");
    assert!(!bootstrap_file.exists(), "the bootstrap token file is removed once read");

    let renewed = identity.get_token_for(EXTERNAL_AUD).await.expect("renewal from the refresh token");
    assert_eq!(renewed, "service-token-2");

    let revoked = identity
        .get_token_for(EXTERNAL_AUD)
        .await
        .expect_err("a revoked workload must not renew");
    assert!(
        revoked.downcast_ref::<service_im::WorkloadRevoked>().is_some(),
        "revocation must be the typed permanent error, got: {revoked}"
    );

    assert_eq!(
        *kernel.refresh_inputs.lock().unwrap(),
        vec!["bootstrap-token", "refresh-1", "refresh-2"],
        "each refresh exchange presents the credential the previous one returned"
    );

    std::env::remove_var("KERNEL_BOOTSTRAP_TOKEN_FILE");
    server.abort();
    let _ = std::fs::remove_dir_all(&dir);
}

#[tokio::test]
async fn rust_token_for_external_mints_a_fresh_es256_token_on_every_call() {
    let _env = IDENTITY_ENV_LOCK.lock().unwrap_or_else(|e| e.into_inner());
    let dir = std::env::temp_dir().join(format!("external-token-{}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    let k8s_token_file = dir.join("token");
    std::fs::write(&k8s_token_file, "k8s.projected.token").unwrap();
    std::env::set_var("KERNEL_K8S_TOKEN_FILE", &k8s_token_file);

    let kernel = Arc::new(MockKernel {
        key: SigningKey::random(&mut rand::thread_rng()),
        external_requests: Mutex::new(Vec::new()),
    });
    let verifying_key = VerifyingKey::from(&kernel.key);
    let addr = reserve_local_addr();
    let server = tokio::spawn(
        Server::builder()
            .add_service(MinterServer::from_arc(kernel.clone()))
            .serve(addr),
    );
    let channel = connect_plain_channel(addr).await;

    let identity = IdentityManager::new(channel, &Config::default())
        .await
        .expect("identity manager bootstraps against the mock kernel");

    let before = now_secs();
    let first = identity.token_for_external(EXTERNAL_AUD).await.expect("first external token");
    let second = identity.token_for_external(EXTERNAL_AUD).await.expect("second external token");
    let after = now_secs();

    assert_ne!(first, second, "an external token must never be reused");

    let mut jtis = Vec::new();
    for jwt in [&first, &second] {
        let parts: Vec<&str> = jwt.split('.').collect();
        assert_eq!(parts.len(), 3, "compact JWT");
        let header = decode_segment(parts[0]);
        assert_eq!(header["alg"], "ES256", "external tokens are ES256, never EdDSA");
        let sig = Signature::from_slice(&URL_SAFE_NO_PAD.decode(parts[2]).unwrap()).unwrap();
        verifying_key
            .verify(format!("{}.{}", parts[0], parts[1]).as_bytes(), &sig)
            .expect("token is signed by the kernel's P-256 key");
        let claims = decode_segment(parts[1]);
        assert_eq!(claims["sub"], SUBJECT);
        assert_eq!(claims["aud"], EXTERNAL_AUD);
        assert_eq!(claims["iss"], ISSUER);
        let exp = claims["exp"].as_i64().unwrap();
        assert!(exp > before && exp <= after + MAX_EXTERNAL_LIFETIME_SECS, "exp {exp} must be within 600 s");
        jtis.push(claims["jti"].as_str().unwrap().to_string());
    }
    assert_ne!(jtis[0], jtis[1], "every call must carry a fresh jti");

    let requests = kernel.external_requests.lock().unwrap();
    assert_eq!(requests.len(), 2, "each call must reach the kernel; nothing is cached");
    for req in requests.iter() {
        assert_eq!(req.input_token, SERVICE_TOKEN, "the caller's own kernel token is the credential");
        assert_eq!(req.aud, EXTERNAL_AUD);
        assert!(req.exp > before && req.exp <= after + MAX_EXTERNAL_LIFETIME_SECS);
    }

    server.abort();
    let _ = std::fs::remove_dir_all(&dir);
}
